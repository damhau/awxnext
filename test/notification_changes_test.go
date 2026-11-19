package test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// changeEventsOf reads the events a run notified with a notification
// template, from the subjects, which are the default messages. AWX's event is
// sent last, so once it is there, the run's change events are too.
func changeEventsOf(t *testing.T, a *client, job resp, nt int64) []string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var events []string
		done := false
		for _, n := range a.get(fmt.Sprintf("jobs/%d/notifications/?notification_template=%d", job.id(), nt)).results() {
			s := fmt.Sprint(n["subject"])
			switch {
			case strings.Contains(s, " after a success: "):
				events = append(events, "first_error")
			case strings.Contains(s, " after a failure: "):
				events = append(events, "recovered")
			case strings.Contains(s, ", more than twice its median of "):
				events = append(events, "slow")
			case strings.Contains(s, " successful: "):
				events, done = append(events, "success"), true
			default:
				events, done = append(events, "error"), true
			}
		}
		if done {
			slices.Sort(events)
			return events
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("job %d sent no success or error notification", job.id())
	return nil
}

func TestNotificationChangeEvents(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	_, hook := newHookServer(t)
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("changes"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	attach := func(jt int64, events ...string) {
		t.Helper()
		for _, ev := range events {
			must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_%s/", jt, ev), map[string]any{"id": nt}), 204)
		}
	}
	want := func(job resp, events ...string) {
		t.Helper()
		slices.Sort(events)
		if got := changeEventsOf(t, a, job, nt); !slices.Equal(got, events) {
			t.Errorf("job %d (%v): events %v, want %v", job.id(), job.Body["status"], got, events)
		}
	}

	jt := f.template(t, "site.yml", nil)
	attach(jt, "success", "error", "first_error", "recovered", "slow")
	playbook := func(p string) {
		t.Helper()
		must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"playbook": p}), 200)
	}
	// The first run has nothing to change from.
	want(f.launch(t, a, jt, nil), "success")
	playbook("fail.yml")
	failed := f.launch(t, a, jt, nil)
	mustStatus(t, f, failed, "failed")
	want(failed, "error", "first_error")
	want(f.launch(t, a, jt, nil), "error")
	// A canceled run is neither a change nor the run the next one compares with.
	pending := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), nil), 201).id()
	must(t, a.post(fmt.Sprintf("jobs/%d/cancel/", pending), nil), 202)
	canceled := waitJob(t, a, fmt.Sprintf("jobs/%d/", pending))
	mustStatus(t, f, canceled, "canceled")
	want(canceled, "error")
	playbook("site.yml")
	want(f.launch(t, a, jt, nil), "recovered", "success")
	want(f.launch(t, a, jt, nil), "success")

	// slow: twice the median of the last 10 successful runs, once there are
	// 5, and not again while the runs stay slow. Fast runs are made up.
	slow := f.template(t, "site.yml", nil)
	attach(slow, "success", "slow")
	teams := newFakeTeams(t)
	must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_slow/", slow), map[string]any{"id": newTeamsTemplate(t, a, f.org, teams.url("/ok"), nil)}), 204)
	fast := func(n int, ago string) {
		t.Helper()
		for i := range n {
			if _, err := app.DB.Exec(t.Context(), `INSERT INTO unified_jobs (type, name, status, unified_job_template_id, started, finished, elapsed)
				SELECT 'job', 'fast', 'successful', $1, f - interval '50 milliseconds', f, 0.05
				FROM (SELECT now() - $2::interval - $3 * interval '1 millisecond' AS f) t`, slow, ago, n-i); err != nil {
				t.Fatal(err)
			}
		}
	}
	fast(4, "1 hour")
	want(f.launch(t, a, slow, nil), "success")
	// These finish after that run, which stays not slow.
	fast(2, "0 seconds")
	second := f.launch(t, a, slow, nil)
	want(second, "slow", "success")
	for _, n := range a.get(fmt.Sprintf("jobs/%d/notifications/", second.id())).results() {
		if s := fmt.Sprint(n["subject"]); strings.Contains(s, "median") && !strings.HasSuffix(strings.Split(s, ": ")[0], "more than twice its median of 0.05 s") {
			t.Errorf("slow message: %s", s)
		}
	}
	if c := teams.take(t, 1)[0].card(t); c.color != "Warning" || c.facts["Median"] != "0s" || !strings.Contains(c.title, "more than twice its median of 0.05 s") {
		t.Errorf("slow card: %+v", c)
	}
	want(f.launch(t, a, slow, nil), "success")
}

func TestNotificationChangeEventsAPI(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", nil)
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": "https://hooks.example.com/x"}}), 201).id()

	for _, path := range []string{fmt.Sprintf("job_templates/%d/", jt), fmt.Sprintf("projects/%d/", f.project), fmt.Sprintf("organizations/%d/", f.org)} {
		rel, _ := a.get(path).Body["related"].(map[string]any)
		for _, ev := range []string{"first_error", "recovered", "slow"} {
			if !strings.HasSuffix(fmt.Sprint(rel["notification_templates_"+ev]), "/notification_templates_"+ev+"/") {
				t.Errorf("%s related: no %s", path, ev)
			}
		}
	}
	sjt := must(t, a.get("system_job_templates/"), 200).results()[0]
	if rel, _ := sjt["related"].(map[string]any); rel["notification_templates_first_error"] != nil {
		t.Errorf("a management job has change events: %v", rel)
	}
	must(t, a.get(fmt.Sprintf("system_job_templates/%v/notification_templates_slow/", sjt["id"])), 404)

	for _, ev := range []string{"first_error", "recovered", "slow"} {
		must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_%s/", jt, ev), map[string]any{"id": nt}), 204)
	}
	if a.get(fmt.Sprintf("job_templates/%d/notification_templates_recovered/", jt)).count() != 1 {
		t.Fatal("not attached")
	}
	used := must(t, a.get(fmt.Sprintf("notification_templates/%d/used_by/", nt)), 200).results()
	if len(used) != 1 || fmt.Sprint(used[0]["events"]) != "[first_error recovered slow]" {
		t.Fatalf("used_by: %v", used)
	}
	must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_slow/", jt), map[string]any{"id": nt, "disassociate": true}), 204)
	if a.get(fmt.Sprintf("job_templates/%d/notification_templates_slow/", jt)).count() != 0 {
		t.Fatal("not detached")
	}

	// Custom messages go under the events' keys, as for AWX's events.
	msgs := map[string]any{
		"first_error": map[string]any{"message": "{{ job.name }} broke", "body": nil},
		"recovered":   map[string]any{"message": "{{ job.name }} works again", "body": nil},
		"slow":        map[string]any{"message": "{{ job.name }} took {{ job.elapsed }} s, usually {{ median_elapsed }} s", "body": nil},
	}
	r := must(t, a.patch(fmt.Sprintf("notification_templates/%d/", nt), map[string]any{"messages": msgs}), 200)
	if m, _ := r.Body["messages"].(map[string]any); m["slow"] == nil {
		t.Errorf("messages: %v", r.Body["messages"])
	}
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", nt), map[string]any{"messages": map[string]any{"flaky": map[string]any{"message": "x"}}}), 400)
}
