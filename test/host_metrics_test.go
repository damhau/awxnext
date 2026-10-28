package test

import (
	"fmt"
	"testing"
	"time"
)

// AWX's host metrics: one row per host name automated, soft-deleted, and a
// monthly summary computed by a management job.
func TestHostMetrics(t *testing.T) {
	a := adminClient(t)
	ctx := t.Context()
	name := func(s string) string { return uniq("metric-" + s) }
	old, recent, gone := name("old"), name("recent"), name("gone")
	app.DB.Exec(ctx, `INSERT INTO host_metrics (hostname, first_automation, last_automation, automated_counter) VALUES
		($1, now() - interval '2 months', now() - interval '2 months', 3), ($2, now(), now(), 1)`, old, recent)
	app.DB.Exec(ctx, `INSERT INTO host_metrics (hostname, first_automation, last_automation, automated_counter, deleted, deleted_counter, last_deleted)
		VALUES ($1, now() - interval '3 months', now() - interval '3 months', 1, true, 1, now() - interval '1 month')`, gone)

	r := must(t, a.get("host_metrics/?hostname="+old), 200)
	if r.count() != 1 {
		t.Fatalf("host_metrics: %s", r.Raw)
	}
	m := r.results()[0]
	if m["automated_counter"] != float64(3) || m["deleted"] != false || m["url"] != fmt.Sprintf("/api/v2/host_metrics/%v/", m["id"]) {
		t.Errorf("host metric: %v", m)
	}
	id := int64(m["id"].(float64))

	// Superusers and auditors only, as AWX's IsSystemAdminOrAuditor.
	bob, _ := newUser(t, a)
	if n := must(t, bob.get("host_metrics/"), 200).count(); n != 0 {
		t.Errorf("a user sees %d host metrics", n)
	}
	auditor, auditorID := newUser(t, a)
	must(t, a.patch(fmt.Sprintf("users/%d/", auditorID), map[string]any{"is_system_auditor": true}), 200)
	must(t, auditor.get(fmt.Sprintf("host_metrics/%d/", id)), 200)
	must(t, auditor.delete(fmt.Sprintf("host_metrics/%d/", id)), 403)

	// Deleting marks it deleted, once.
	must(t, a.delete(fmt.Sprintf("host_metrics/%d/", id)), 204)
	must(t, a.delete(fmt.Sprintf("host_metrics/%d/", id)), 204)
	m = must(t, a.get(fmt.Sprintf("host_metrics/%d/", id)), 200).Body
	if m["deleted"] != true || m["deleted_counter"] != float64(1) || m["last_deleted"] == nil {
		t.Errorf("soft-deleted: %v", m)
	}
	if n := must(t, a.get("host_metrics/?deleted=false&hostname="+old), 200).count(); n != 0 {
		t.Errorf("deleted filter: %d", n)
	}

	// The monthly summary.
	sjt := must(t, a.get("system_job_templates/?job_type=host_metric_summary_monthly"), 200).results()[0]
	job := must(t, a.post(fmt.Sprintf("system_job_templates/%v/launch/", sjt["id"]), nil), 201)
	if j := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", job.id())); j.Body["status"] != "successful" {
		t.Fatalf("summary job: %v %v", j.Body["status"], j.Body["job_explanation"])
	}
	rows := must(t, a.get("host_metric_summary_monthly/?page_size=200"), 200).results()
	if len(rows) < 36 {
		t.Fatalf("%d months", len(rows))
	}
	now := time.Now().UTC()
	month := func(ago int) string {
		return time.Date(now.Year(), now.Month()-time.Month(ago), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	}
	byDate := map[string]map[string]any{}
	var prev float64
	for i, row := range rows {
		byDate[row["date"].(string)] = row
		consumed := row["license_consumed"].(float64)
		if i > 0 && consumed != prev+row["hosts_added"].(float64)-row["hosts_deleted"].(float64) {
			t.Errorf("%v: consumed %v after %v, +%v -%v", row["date"], consumed, prev, row["hosts_added"], row["hosts_deleted"])
		}
		prev = consumed
	}
	for ago, want := range map[int]string{0: "hosts_added", 2: "hosts_added", 3: "hosts_added", 1: "hosts_deleted"} {
		if row := byDate[month(ago)]; row == nil || row[want].(float64) < 1 {
			t.Errorf("%s: %s %v", month(ago), want, row)
		}
	}
	if n := must(t, bob.get("host_metric_summary_monthly/"), 200).count(); n != 0 {
		t.Errorf("a user sees %d summaries", n)
	}
}
