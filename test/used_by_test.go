package test

import (
	"fmt"
	"testing"
)

// usedBy returns an object's dependents as "resource/id field effect".
func usedBy(t *testing.T, c *client, path string) (map[string]bool, resp) {
	t.Helper()
	r := must(t, c.get(path+"used_by/"), 200)
	out := map[string]bool{}
	for _, d := range r.results() {
		out[fmt.Sprintf("%v/%v %v %v", d["resource"], d["id"], d["field"], d["effect"])] = true
	}
	return out, r
}

func wantDependents(t *testing.T, what string, got map[string]bool, want ...string) {
	t.Helper()
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s: %q missing from %v", what, w, got)
		}
	}
}

// What uses an inventory, project, credential, execution environment or
// template (roadmap M7-11, awxnext's used_by), with what deleting it does
// to each: deleted with it, or cleared. Dependents a user cannot see are
// only counted.
func TestUsedBy(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	ee := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "quay.io/ansible/awx-ee:24.6.1"}), 201).id()
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("ssh"), "organization": f.org, "credential_type": 1, "inputs": map[string]any{"username": "u"}}), 201).id()
	jt := f.template(t, "site.yml", map[string]any{"ask_inventory_on_launch": true, "execution_environment": ee})
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": cred}), 204)
	must(t, a.patch(fmt.Sprintf("organizations/%d/", f.org), map[string]any{"default_environment": ee}), 200)

	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": jt, "inventory": f.inv}), 201)
	sched := must(t, a.post("schedules/", map[string]any{"name": uniq("nightly"), "unified_job_template": jt, "inventory": f.inv,
		"rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY;INTERVAL=1"}), 201).id()
	other := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": f.org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": uniq("src"), "inventory": other, "source": "scm", "source_project": f.project,
		"source_path": "hosts.ini"}), 201).id()
	ci := must(t, a.post("constructed_inventories/", map[string]any{"name": uniq("ci"), "organization": f.org}), 201).id()
	must(t, a.post(fmt.Sprintf("constructed_inventories/%d/input_inventories/", ci), map[string]any{"id": f.inv}), 204)

	got, _ := usedBy(t, a, fmt.Sprintf("inventories/%d/", f.inv))
	wantDependents(t, "inventory", got,
		fmt.Sprintf("job_templates/%d inventory cleared", jt),
		fmt.Sprintf("workflow_job_templates/%d node_inventory cleared", wf),
		fmt.Sprintf("schedules/%d inventory cleared", sched),
		fmt.Sprintf("inventories/%d input_inventories cleared", ci))
	got, _ = usedBy(t, a, fmt.Sprintf("projects/%d/", f.project))
	wantDependents(t, "project", got,
		fmt.Sprintf("job_templates/%d project cleared", jt),
		fmt.Sprintf("inventory_sources/%d source_project deleted", src))
	got, _ = usedBy(t, a, fmt.Sprintf("credentials/%d/", cred))
	wantDependents(t, "credential", got, fmt.Sprintf("job_templates/%d credentials cleared", jt))
	got, _ = usedBy(t, a, fmt.Sprintf("execution_environments/%d/", ee))
	wantDependents(t, "execution environment", got,
		fmt.Sprintf("job_templates/%d execution_environment cleared", jt),
		fmt.Sprintf("organizations/%d default_environment cleared", f.org))
	got, _ = usedBy(t, a, fmt.Sprintf("job_templates/%d/", jt))
	wantDependents(t, "job template", got,
		fmt.Sprintf("schedules/%d unified_job_template deleted", sched),
		fmt.Sprintf("workflow_job_templates/%d node_template cleared", wf))
	if got, r := usedBy(t, a, fmt.Sprintf("inventories/%d/", other)); len(got) != 0 || r.Body["hidden"] != float64(0) {
		t.Errorf("an inventory nothing uses: %v", r.Body)
	}

	// Someone who may only read the inventory sees no template, but is told
	// how many things use it.
	user, uid := newUser(t, a)
	roles := must(t, a.get(fmt.Sprintf("inventories/%d/", f.inv)), 200).Body["summary_fields"].(map[string]any)["object_roles"].(map[string]any)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": roles["read_role"].(map[string]any)["id"]}), 204)
	if got, r := usedBy(t, user, fmt.Sprintf("inventories/%d/", f.inv)); len(got) != 0 || r.Body["hidden"] != float64(4) {
		t.Errorf("used_by for a reader: %v", r.Body)
	}
	must(t, user.get(fmt.Sprintf("projects/%d/used_by/", f.project)), 403)

	// The global default execution environment: listed, and once deleted no
	// longer given to jobs.
	def := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "quay.io/ansible/awx-ee:24.6.1"}), 201).id()
	must(t, a.patch("settings/system/", map[string]any{"DEFAULT_EXECUTION_ENVIRONMENT": def}), 200)
	t.Cleanup(func() { a.patch("settings/system/", map[string]any{"DEFAULT_EXECUTION_ENVIRONMENT": nil}) })
	got, _ = usedBy(t, a, fmt.Sprintf("execution_environments/%d/", def))
	wantDependents(t, "global default", got, "settings/<nil> DEFAULT_EXECUTION_ENVIRONMENT cleared")
	must(t, a.patch(fmt.Sprintf("organizations/%d/", f.org), map[string]any{"default_environment": nil}), 200)
	plain := f.template(t, "site.yml", nil)
	must(t, a.delete(fmt.Sprintf("execution_environments/%d/", def)), 204)
	job := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", plain), map[string]any{}), 201)
	if job.Body["execution_environment"] == float64(def) {
		t.Errorf("a job was given the deleted global default execution environment")
	}
}
