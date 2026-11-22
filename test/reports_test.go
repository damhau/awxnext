package test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/db"
)

type reportRun struct {
	template             *int64
	org                  int64
	name, launch, status string
	daysAgo              int
	elapsed              float64
	by                   int64
	catalog, viaRunning  bool
	hosts                []string
	failed, unreachable  string
}

type reportCall struct {
	c       *client
	name, q string
}

// TestReports checks each report's numbers against made-up runs, for each
// filter and for a user who may read only some of them, then after
// cleanup_jobs deletes the runs and after the report history is cut.
func TestReports(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	at := func(daysAgo int) time.Time { return today.AddDate(0, 0, -daysAgo).Add(12 * time.Hour) }

	jtA := f.template(t, "site.yml", map[string]any{"name": uniq("a-tmpl")})
	jtB := f.template(t, "site.yml", map[string]any{"name": uniq("b-tmpl")})
	label := must(t, a.post(fmt.Sprintf("job_templates/%d/labels/", jtB), map[string]any{"name": uniq("lab"), "organization": f.org}), 201).id()
	org2 := newOrg(t, a)
	user, uid := newUser(t, a)
	grant(t, a, "JobTemplate Execute", uid, jtA)
	team := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": f.org}), 201).id()
	grant(t, a, "Team Member", uid, team)
	var adminID int64
	app.DB.QueryRow(ctx, `SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID)
	hostIDs := map[string]int64{}
	for _, h := range must(t, a.get(fmt.Sprintf("inventories/%d/hosts/", f.inv)), 200).results() {
		id, _ := h["id"].(float64)
		hostIDs[h["name"].(string)] = int64(id)
	}
	gone := uniq("c-gone")

	runs := map[string]reportRun{
		"A0a": {template: &jtA, status: "successful", daysAgo: 10, elapsed: 10, by: adminID, hosts: []string{"h1"}},
		"A0b": {template: &jtA, status: "successful", daysAgo: 9, elapsed: 10, by: adminID},
		"A1":  {template: &jtA, status: "successful", daysAgo: 6, elapsed: 10, by: adminID, hosts: []string{"h1", "h2"}},
		"A2":  {template: &jtA, status: "failed", daysAgo: 5, elapsed: 20, by: adminID, hosts: []string{"h1", "h2"}, failed: "h1"},
		"A3":  {template: &jtA, status: "successful", daysAgo: 4, elapsed: 30, by: uid, hosts: []string{"h1", "h2"}},
		"A4":  {template: &jtA, status: "canceled", daysAgo: 3, elapsed: 100, by: adminID, hosts: []string{"h1", "h2"}},
		"A5":  {template: &jtA, status: "successful", daysAgo: 2, elapsed: 40, by: adminID, hosts: []string{"h1", "h2"}, viaRunning: true},
		"A6":  {template: &jtA, status: "error", daysAgo: 1, elapsed: 50, by: adminID, hosts: []string{"h1", "h2"}, unreachable: "h2"},
		"B1":  {template: &jtB, status: "successful", daysAgo: 2, elapsed: 5, by: adminID, launch: "scheduled", hosts: []string{"h3"}},
		"B2":  {template: &jtB, status: "successful", daysAgo: 1, elapsed: 7, by: uid, catalog: true, hosts: []string{"h3"}},
		"C1":  {org: org2, name: gone, status: "successful", daysAgo: 3, elapsed: 3600, by: uid},
	}
	ids := map[string]int64{}
	for _, key := range slices.Sorted(maps.Keys(runs)) {
		r := runs[key]
		org, inv, proj, name := &r.org, &f.inv, &f.project, r.name
		if r.template != nil {
			org = &f.org
			app.DB.QueryRow(ctx, `SELECT name FROM unified_job_templates WHERE id = $1`, *r.template).Scan(&name)
		} else {
			inv, proj = nil, nil
		}
		var catalog *int64
		if r.catalog {
			n := int64(999)
			catalog = &n
		}
		status := r.status
		if r.viaRunning {
			status = "running"
		}
		finished := at(r.daysAgo)
		// Created long ago so that cleanup_jobs with days=90 deletes these
		// runs and not other tests' jobs; the reports go by finished.
		var id int64
		if err := app.DB.QueryRow(ctx, `INSERT INTO unified_jobs (type, name, unified_job_template_id, organization_id, inventory_id, project_id,
				launch_type, status, failed, started, finished, elapsed, created_by_id, catalog_request_id, created)
			VALUES ('job', $1, $2, $3, $4, $5, coalesce(nullif($6, ''), 'manual'), $7, $7 IN ('failed', 'error'), $8, $9, $10, $11, $12, now() - interval '100 days')
			RETURNING id`, name, r.template, org, inv, proj, r.launch, status, finished.Add(-time.Duration(r.elapsed)*time.Second), finished, r.elapsed, r.by, catalog).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if r.viaRunning {
			app.DB.Exec(ctx, `UPDATE unified_jobs SET status = $2 WHERE id = $1`, id, r.status)
		}
		for _, h := range r.hosts {
			// A failure is stored after the summary, as a job's last events can.
			dark := 0
			if h == r.unreachable {
				dark = 1
			}
			app.DB.Exec(ctx, `INSERT INTO job_host_summaries (job_id, host_id, host_name, dark, created) VALUES ($1, $2, $3, $4, $5)`,
				id, hostIDs[h], h, dark, finished)
			if h == r.failed {
				app.DB.Exec(ctx, `UPDATE job_host_summaries SET failures = 1 WHERE job_id = $1 AND host_name = $2`, id, h)
			}
		}
		ids[key] = id
	}
	var running int
	app.DB.QueryRow(ctx, `SELECT count(*) FROM report_runs WHERE job_id = $1 AND status = 'successful'`, ids["A5"]).Scan(&running)
	if running != 1 {
		t.Fatalf("a run that finished after it was stored is not in report_runs")
	}

	report := func(c *client, name, query string) map[string]any {
		t.Helper()
		return must(t, c.get("reports/"+name+"/?"+query), 200).Body
	}
	org1 := fmt.Sprintf("organization=%d", f.org)
	num := func(v any) any {
		switch n := v.(type) {
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
		return v
	}
	check := func(what string, got map[string]any, want map[string]any) {
		t.Helper()
		for k, w := range want {
			if g := got[k]; num(g) != num(w) {
				t.Errorf("%s: %s = %v, want %v", what, k, g, w)
			}
		}
	}
	results := func(body map[string]any, key string) []map[string]any {
		var out []map[string]any
		for _, x := range body[key].([]any) {
			out = append(out, x.(map[string]any))
		}
		return out
	}

	if r := must(t, a.get("reports/"), 200); r.Body["templates"] != "/api/v2/reports/templates/" {
		t.Errorf("root: %s", r.Raw)
	}

	// Totals, for each filter.
	totals := func(c *client, q string) map[string]any { t.Helper(); return report(c, "totals", q) }
	tot := totals(a, "period=7&"+org1)
	check("totals", tot, map[string]any{"runs": 8, "successful": 5, "failed": 2, "canceled": 1, "success_rate": 0.7143, "hours": 0.07, "host_runs": 14, "hosts": 3})
	if p := results(tot, "top_projects"); len(p) != 1 || num(p[0]["project"]) != float64(f.project) || num(p[0]["runs"]) != 8.0 {
		t.Errorf("top projects: %v", p)
	}
	period := tot["period"].(map[string]any)
	check("period", period, map[string]any{"days": 7, "from": today.AddDate(0, 0, -6).Format(time.DateOnly), "to": today.Format(time.DateOnly),
		"previous_from": today.AddDate(0, 0, -13).Format(time.DateOnly), "previous_to": today.AddDate(0, 0, -7).Format(time.DateOnly),
		"interval": "day", "cut_short": false})
	for q, want := range map[string]int{
		fmt.Sprintf("template=%d", jtA):               8,
		fmt.Sprintf("period=7&template=%d", jtA):      6,
		fmt.Sprintf("inventory=%d", f.inv):            10,
		fmt.Sprintf("project=%d", f.project):          10,
		fmt.Sprintf("label=%d", label):                2,
		fmt.Sprintf("organization=%d", org2):          1,
		org1 + "&launch_type=manual":                  8,
		org1 + "&period=7&launch_type=manual":         6,
		org1 + "&launch_type=scheduled":               1,
		org1 + "&launch_type=portal":                  1,
		org1 + "&launch_type=event_rule":              0,
		org1 + "&period=90":                           10,
		org1 + "&from=" + at(2).Format(time.DateOnly): 4,
		org1 + "&from=" + at(6).Format(time.DateOnly) + "&to=" + at(5).Format(time.DateOnly): 2,
	} {
		if got := num(totals(a, q)["runs"]); got != float64(want) {
			t.Errorf("totals?%s: %v runs, want %d", q, got, want)
		}
	}
	check("org2", totals(a, fmt.Sprintf("period=7&organization=%d", org2)), map[string]any{"hours": 1, "host_runs": 0, "success_rate": 1})

	// Templates: worst first, with the previous period, durations and flips.
	tpl := report(a, "templates", "period=7&"+org1)
	rows := results(tpl, "results")
	if len(rows) != 2 || num(rows[0]["template"]) != float64(jtA) || num(rows[1]["template"]) != float64(jtB) {
		t.Fatalf("templates: %v", rows)
	}
	check("template A", rows[0], map[string]any{"runs": 6, "successful": 3, "failed": 2, "canceled": 1, "success_rate": 0.6,
		"previous_success_rate": 1, "success_rate_change": -0.4, "median_duration": 30, "p95_duration": 48, "flips": 3})
	if lf, _ := rows[0]["last_failure"].(map[string]any); lf == nil || num(lf["job"]) != float64(ids["A6"]) || lf["exists"] != true {
		t.Errorf("template A's last failure: %v", rows[0]["last_failure"])
	}
	check("template B", rows[1], map[string]any{"runs": 2, "success_rate": 1, "previous_success_rate": nil, "success_rate_change": nil,
		"median_duration": 6, "p95_duration": 6.9, "flips": 0, "last_failure": nil})
	check("template A over 30 days", results(report(a, "templates", "period=30&"+org1), "results")[0],
		map[string]any{"runs": 8, "success_rate": 0.7143, "previous_success_rate": nil})
	order := func(q string) []int64 {
		var out []int64
		for _, r := range results(report(a, "templates", "period=7&"+org1+"&order_by="+q), "results") {
			out = append(out, int64(r["template"].(float64)))
		}
		return out
	}
	for q, want := range map[string][]int64{"-runs": {jtA, jtB}, "runs": {jtB, jtA}, "name": {jtA, jtB}, "-name": {jtB, jtA},
		"-success_rate": {jtB, jtA}, "flips": {jtB, jtA}, "last_failure": {jtA, jtB}, "-last_failure": {jtA, jtB}, "median_duration": {jtB, jtA}} {
		if got := order(q); !slices.Equal(got, want) {
			t.Errorf("order_by=%s: %v, want %v", q, got, want)
		}
	}
	if rows := results(report(a, "templates", fmt.Sprintf("period=7&organization=%d", org2)), "results"); len(rows) != 1 || rows[0]["template"] != nil || rows[0]["name"] != gone {
		t.Errorf("a deleted template's runs: %v", rows)
	}

	// Trends: one bucket a day, empty days included, or a week.
	tr := results(report(a, "trends", "period=7&"+org1), "results")
	if len(tr) != 7 || tr[0]["start"] != today.AddDate(0, 0, -6).Format(time.DateOnly) {
		t.Fatalf("daily trends: %v", tr)
	}
	check("day -6", tr[0], map[string]any{"runs": 1, "failure_rate": 0, "median_duration": 10})
	check("day -5", tr[1], map[string]any{"runs": 1, "failed": 1, "failure_rate": 1})
	check("day -3", tr[3], map[string]any{"runs": 1, "canceled": 1, "failure_rate": nil, "median_duration": nil})
	check("day -2", tr[4], map[string]any{"runs": 2, "successful": 2, "median_duration": 22.5})
	check("day -1", tr[5], map[string]any{"runs": 2, "failed": 1, "failure_rate": 0.5, "median_duration": 28.5})
	check("today", tr[6], map[string]any{"runs": 0, "failure_rate": nil})
	check("template A on day -2", results(report(a, "trends", fmt.Sprintf("period=7&template=%d", jtA)), "results")[4], map[string]any{"median_duration": 40})
	weeks := report(a, "trends", "period=90&"+org1)
	total := 0.0
	for i, w := range results(weeks, "results") {
		start, _ := time.Parse(time.DateOnly, w["start"].(string))
		if start.Weekday() != time.Monday {
			t.Errorf("week %d starts on %s", i, start.Weekday())
		}
		total += w["runs"].(float64)
	}
	if weeks["period"].(map[string]any)["interval"] != "week" || total != 10 {
		t.Errorf("weekly trends: %v runs, %v", total, weeks["period"])
	}

	// Hosts: distinct hosts per organization and month, and the failing ones.
	hosts := report(a, "hosts", "period=7&"+org1)
	wantMonths := map[string]map[string]bool{}
	for _, r := range runs {
		if r.template != nil && r.daysAgo <= 6 {
			for _, h := range r.hosts {
				m := at(r.daysAgo).Format("2006-01")
				if wantMonths[m] == nil {
					wantMonths[m] = map[string]bool{}
				}
				wantMonths[m][h] = true
			}
		}
	}
	auto := results(hosts, "automated")
	if len(auto) != len(wantMonths) {
		t.Errorf("automated: %v, want %v", auto, wantMonths)
	}
	for _, m := range auto {
		if num(m["organization"]) != float64(f.org) || num(m["hosts"]) != float64(len(wantMonths[m["month"].(string)])) {
			t.Errorf("automated in %v: %v, want %v", m["month"], m, wantMonths[m["month"].(string)])
		}
	}
	failing := results(hosts, "most_failed")
	if len(failing) != 2 {
		t.Fatalf("most failed: %v", failing)
	}
	check("h1", failing[0], map[string]any{"host_name": "h1", "host": hostIDs["h1"], "inventory": f.inv, "runs": 6, "failed": 1, "unreachable": 0})
	check("h2", failing[1], map[string]any{"host_name": "h2", "runs": 6, "failed": 0, "unreachable": 1})

	// Who launches what.
	launches := report(a, "launches", "period=7&"+org1)
	byUser := results(launches, "by_user")
	if len(byUser) != 2 {
		t.Fatalf("by user: %v", byUser)
	}
	check("admin's runs", byUser[0], map[string]any{"user": adminID, "username": "admin", "runs": 6, "success_rate": 0.6})
	check("the user's runs", byUser[1], map[string]any{"user": uid, "runs": 2, "success_rate": 1})
	if bt := results(launches, "by_team"); len(bt) != 1 || num(bt[0]["team"]) != float64(team) || num(bt[0]["runs"]) != 2.0 {
		t.Errorf("by team: %v", bt)
	}
	var kinds []string
	for _, l := range results(launches, "by_launch_type") {
		kinds = append(kinds, fmt.Sprintf("%s:%v", l["launch_type"], l["runs"]))
	}
	if got := strings.Join(kinds, " "); got != "manual:6 portal:1 scheduled:1" {
		t.Errorf("by launch type: %s", got)
	}
	var kind string
	app.DB.QueryRow(ctx, `SELECT report_launch_type('manual', NULL, 5)`).Scan(&kind)
	if kind != "event_rule" {
		t.Errorf("an event rule's run counts as %q", kind)
	}

	// A user reads only the runs they may: template A's and their own,
	// not their portal request's.
	check("the user's totals", totals(user, "period=7"), map[string]any{"runs": 7, "successful": 4, "failed": 2, "canceled": 1, "hours": 1.07, "host_runs": 12, "hosts": 2})
	if rows := results(report(user, "templates", "period=7"), "results"); len(rows) != 2 || num(rows[0]["template"]) != float64(jtA) || rows[1]["name"] != gone {
		t.Errorf("the user's templates: %v", rows)
	}
	check("the user's templates", results(report(user, "templates", "period=7"), "results")[0], map[string]any{"runs": 6, "flips": 3})
	if n := len(results(report(user, "hosts", "period=7"), "most_failed")); n != 2 {
		t.Errorf("the user's failing hosts: %d", n)
	}
	check("the user's team", results(report(user, "launches", "period=7"), "by_team")[0], map[string]any{"team": team, "runs": 2})
	if n := num(totals(user, fmt.Sprintf("period=7&template=%d", jtB))["runs"]); n != 0.0 {
		t.Errorf("the user reads %v runs of a template they can't", n)
	}

	for _, q := range []string{"period=8", "period=x", "from=2026-13-01", "from=2026-02-01&to=2026-01-01", "period=7&from=2026-01-01",
		"from=2000-01-01&to=2026-01-01", "launch_type=nope", "interval=month", "order_by=status", "organization=x", "label=0"} {
		must(t, a.get("reports/templates/?"+q), 400)
	}

	// The same numbers after cleanup_jobs deletes the runs.
	var queries []reportCall
	for _, name := range []string{"totals", "templates", "trends", "hosts", "launches"} {
		for _, q := range []string{"period=7&" + org1, "period=30&" + org1, fmt.Sprintf("period=7&organization=%d", org2), fmt.Sprintf("period=7&label=%d", label)} {
			queries = append(queries, reportCall{a, name, q})
		}
		queries = append(queries, reportCall{user, name, "period=7"})
	}
	canonical := func(c *client, name, q string) string {
		t.Helper()
		raw := must(t, c.get("reports/"+name+"/?"+q), 200).Raw
		var v any
		json.Unmarshal([]byte(raw), &v)
		out, _ := json.Marshal(v)
		return strings.ReplaceAll(string(out), `"exists":true`, `"exists":false`)
	}
	before := map[int]string{}
	for i, x := range queries {
		before[i] = canonical(x.c, x.name, x.q)
	}
	var sjt int64
	app.DB.QueryRow(ctx, `SELECT id FROM unified_job_templates WHERE type = 'system_job_template' AND job_type = 'cleanup_jobs'`).Scan(&sjt)
	cleanup := func(extra map[string]any) string {
		t.Helper()
		j := must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", sjt), map[string]any{"extra_vars": extra}), 201)
		if r := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", j.id())); r.Body["status"] != "successful" {
			t.Fatalf("cleanup: %v", r.Body)
		}
		return a.get(fmt.Sprintf("system_jobs/%d/stdout/?format=txt", j.id())).Raw
	}
	cleanup(map[string]any{"days": 90})
	must(t, a.get(fmt.Sprintf("jobs/%d/", ids["A6"])), 404)
	for i, x := range queries {
		if after := canonical(x.c, x.name, x.q); after != before[i] {
			t.Errorf("%s?%s changed after cleanup_jobs:\nbefore %s\nafter  %s", x.name, x.q, before[i], after)
		}
	}
	if lf := results(report(a, "templates", "period=7&"+org1), "results")[0]["last_failure"].(map[string]any); lf["exists"] != false {
		t.Errorf("the deleted last failure still exists: %v", lf)
	}

	// The report history is kept for REPORT_HISTORY_DAYS, then cut short.
	must(t, a.patch("settings/jobs/", map[string]any{"REPORT_HISTORY_DAYS": 3}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"REPORT_HISTORY_DAYS": 400}) })
	check("a period longer than the history", totals(a, "period=7&"+org1)["period"].(map[string]any),
		map[string]any{"kept_since": today.AddDate(0, 0, -2).Format(time.DateOnly), "cut_short": true})
	check("a period within the history", totals(a, "from="+at(1).Format(time.DateOnly))["period"].(map[string]any), map[string]any{"cut_short": false})
	kept := func() (runs, days string) {
		var r, d []string
		for _, k := range []string{"A1", "A2", "A3", "A5", "A6"} {
			var n int
			app.DB.QueryRow(ctx, `SELECT count(*) FROM report_runs WHERE job_id = $1`, ids[k]).Scan(&n)
			r = append(r, fmt.Sprintf("%s:%d", k, n))
		}
		for _, k := range []int{6, 5, 4, 2, 1} {
			var n int
			app.DB.QueryRow(ctx, `SELECT count(*) FROM report_host_days WHERE unified_job_template_id = $1 AND day = $2`, jtA, at(k)).Scan(&n)
			d = append(d, fmt.Sprintf("-%d:%d", k, n))
		}
		return strings.Join(r, " "), strings.Join(d, " ")
	}
	keys := func() string {
		var out []string
		for _, k := range []int{10, 6, 4, 2, 1} {
			var ok bool
			app.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM report_keys WHERE unified_job_template_id = $1 AND day = $2)`, jtA, at(k)).Scan(&ok)
			out = append(out, fmt.Sprintf("-%d:%t", k, ok))
		}
		return strings.Join(out, " ")
	}
	if out := cleanup(map[string]any{"days": 90, "dry_run": true}); !strings.Contains(out, "report history older than 3 days (dry run)") || !strings.Contains(out, "Report runs would delete") {
		t.Errorf("dry run:\n%s", out)
	}
	if r, d := kept(); r != "A1:1 A2:1 A3:1 A5:1 A6:1" || d != "-6:2 -5:2 -4:2 -2:2 -1:2" {
		t.Errorf("the dry run deleted report history: %s, %s", r, d)
	}
	if k := keys(); k != "-10:true -6:true -4:true -2:true -1:true" {
		t.Errorf("the rights keys before the history cleanup: %s", k)
	}
	if out := cleanup(map[string]any{"days": 90}); !strings.Contains(out, "Report host days deleted") {
		t.Errorf("cleanup:\n%s", out)
	}
	if r, d := kept(); r != "A1:0 A2:0 A3:0 A5:1 A6:1" || d != "-6:0 -5:0 -4:0 -2:2 -1:2" {
		t.Errorf("after the history cleanup: %s, %s", r, d)
	}
	if k := keys(); k != "-10:false -6:false -4:false -2:true -1:true" {
		t.Errorf("the rights keys after the history cleanup: %s", k)
	}
}

// TestReportsBackfill checks that the migration fills the report history
// from the jobs and host summaries already there.
func TestReportsBackfill(t *testing.T) {
	pool := freshDB(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`DROP TRIGGER report_run_changed ON unified_jobs; DROP TRIGGER report_host_summary_changed ON job_host_summaries;
		DROP FUNCTION report_run_changed(); DROP FUNCTION report_host_summary_changed();
		DROP VIEW report_runs_source; DROP VIEW report_host_runs_source; DROP TABLE report_runs; DROP TABLE report_host_days;
		DROP TABLE report_keys; DROP FUNCTION report_runs_inserted(); DROP FUNCTION report_host_days_inserted();
		DROP FUNCTION report_launch_type(text, bigint, bigint); DELETE FROM schema_migrations WHERE version = '0055_reports'`)
	job := func(typ, status, launch string, catalog *int64, finished *time.Time) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO unified_jobs (type, name, status, launch_type, catalog_request_id, finished, elapsed)
			VALUES ($1, 'old', $2, $3, $4, $5, 12.5) RETURNING id`, typ, status, launch, catalog, finished).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	day := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	request := int64(7)
	ok := job("job", "successful", "manual", nil, &day)
	again := job("job", "successful", "manual", nil, &day)
	failed := job("job", "failed", "scheduled", nil, &day)
	portal := job("job", "canceled", "manual", &request, nil)
	job("job", "running", "manual", nil, nil)
	job("workflow_job", "successful", "manual", nil, &day)
	for _, s := range []struct {
		job            int64
		host           string
		failures, dark int
	}{{ok, "web1", 0, 0}, {ok, "web2", 0, 0}, {again, "web1", 0, 0}, {failed, "web1", 2, 0}, {failed, "web2", 0, 1}} {
		exec(`INSERT INTO job_host_summaries (job_id, host_name, failures, dark, created) VALUES ($1, $2, $3, $4, $5)`, s.job, s.host, s.failures, s.dark, day)
	}

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	got := func(sql string) string {
		rows, err := pool.Query(ctx, sql)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				t.Fatal(err)
			}
			out = append(out, row)
		}
		return strings.Join(out, " | ")
	}
	want := fmt.Sprintf("%d successful manual t | %d successful manual t | %d failed scheduled t | %d canceled portal t", ok, again, failed, portal)
	if runs := got(`SELECT concat_ws(' ', job_id, status, launch_type, finished IS NOT NULL) FROM report_runs ORDER BY job_id`); runs != want {
		t.Errorf("report runs:\n%s\nwant\n%s", runs, want)
	}
	want = "web1 manual 2 0 0 | web1 scheduled 1 1 0 | web2 manual 1 0 0 | web2 scheduled 1 0 1"
	if days := got(`SELECT concat_ws(' ', host_name, launch_type, runs, failed, unreachable) FROM report_host_days WHERE day = '2026-03-04' ORDER BY 1`); days != want {
		t.Errorf("report host days:\n%s\nwant\n%s", days, want)
	}
	want = got(`SELECT concat_ws(' ', '2026-03-04 f |', (finished AT TIME ZONE 'UTC')::date, 't') FROM report_runs WHERE launch_type = 'portal'`)
	if keys := got(`SELECT concat_ws(' ', day, portal) FROM report_keys ORDER BY day`); keys != want {
		t.Errorf("report keys:\n%s\nwant\n%s", keys, want)
	}
}
