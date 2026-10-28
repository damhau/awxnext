package test

import (
	"fmt"
	"testing"
	"time"
)

// AWX's dashboard: counts of what the user sees, and finished jobs per day.
func TestDashboard(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", nil)
	// A playbook run that fails without running (its project update fails),
	// and the project's successful update from the fixture.
	must(t, a.patch(fmt.Sprintf("projects/%d/", f.project), map[string]any{"scm_url": "file:///nonexistent-awxnext-repo", "scm_update_on_launch": true}), 200)
	f.launch(t, a, jt, nil)

	total := func(c *client, path string) float64 {
		t.Helper()
		return float64(must(t, c.get(path), 200).count())
	}
	d := must(t, a.get("dashboard/"), 200)
	if d.Header.Get("Warning") == "" || d.Body["related"].(map[string]any)["jobs_graph"] != "/api/v2/dashboard/graphs/jobs/" {
		t.Errorf("dashboard: %v %s", d.Header.Get("Warning"), d.Raw)
	}
	for key, path := range map[string]string{"inventories": "inventories/", "hosts": "hosts/", "projects": "projects/", "job_templates": "job_templates/",
		"users": "users/", "organizations": "organizations/", "teams": "teams/", "credentials": "credentials/", "groups": "groups/"} {
		if got := d.Body[key].(map[string]any)["total"]; got != total(a, path) {
			t.Errorf("%s total %v, list count %v", key, got, total(a, path))
		}
	}
	git := d.Body["scm_types"].(map[string]any)["git"].(map[string]any)
	if git["total"] != total(a, "projects/?scm_type=git") || git["failed"] != total(a, "projects/?scm_type=git&last_job_failed=True") {
		t.Errorf("git projects: %v", git)
	}

	// A user who sees nothing counts nothing.
	bob, _ := newUser(t, a)
	d = must(t, bob.get("dashboard/"), 200)
	for _, key := range []string{"inventories", "hosts", "projects", "job_templates", "credentials"} {
		if got := d.Body[key].(map[string]any)["total"]; got != float64(0) {
			t.Errorf("bob's %s: %v", key, got)
		}
	}

	g := must(t, a.get("dashboard/graphs/jobs/?period=week"), 200).Body["jobs"].(map[string]any)
	today := float64(time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), time.Now().UTC().Day(), 0, 0, 0, 0, time.UTC).Unix())
	point := func(series map[string]any, status string) float64 {
		t.Helper()
		pts := series[status].([]any)
		if len(pts) != 8 {
			t.Fatalf("%s: %d points for a week", status, len(pts))
		}
		last := pts[len(pts)-1].([]any)
		if last[0] != today {
			t.Fatalf("%s: last point at %v, want %v", status, last[0], today)
		}
		return last[1].(float64)
	}
	if point(g, "failed") < 1 || point(g, "successful") < 1 {
		t.Errorf("today: %v failed, %v successful", point(g, "failed"), point(g, "successful"))
	}
	scm := must(t, a.get("dashboard/graphs/jobs/?period=week&job_type=scm_update"), 200).Body["jobs"].(map[string]any)
	play := must(t, a.get("dashboard/graphs/jobs/?period=week&job_type=playbook_run"), 200).Body["jobs"].(map[string]any)
	if point(scm, "successful") < 1 || point(play, "failed") < 1 || point(play, "failed") > point(g, "failed") {
		t.Errorf("by type: scm %v successful, playbook %v failed of %v", point(scm, "successful"), point(play, "failed"), point(g, "failed"))
	}
	if n := len(must(t, a.get("dashboard/graphs/jobs/"), 200).Body["jobs"].(map[string]any)["error"].([]any)); n < 28 {
		t.Errorf("a month has %d points", n)
	}
	if bobJobs := must(t, bob.get("dashboard/graphs/jobs/?period=day"), 200).Body["jobs"].(map[string]any); allCounts(bobJobs) != 0 {
		t.Errorf("bob sees jobs in the graph")
	}
	if r := must(t, a.get("dashboard/graphs/jobs/?period=year"), 400); r.Body["error"] != `Unknown period "year"` {
		t.Errorf("unknown period: %s", r.Raw)
	}
}

// allCounts sums every count of every series.
func allCounts(series map[string]any) float64 {
	var n float64
	for _, s := range series {
		for _, p := range s.([]any) {
			n += p.([]any)[1].(float64)
		}
	}
	return n
}
