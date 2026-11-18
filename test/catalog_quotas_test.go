package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const flakyProvisionPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.fail:
        msg: no room left on the cluster
      run_once: true
      when: name == "broken"
    - ansible.builtin.set_stats:
        data:
          host: "{{ name }}.internal"
          db_user: "{{ name }}"
          db_password: "{{ name }}-secret"
      run_once: true
`

// usageOf is a holder's row of an item's quota usage, by team or username.
func usageOf(t *testing.T, a *client, item int64, holder string) map[string]any {
	t.Helper()
	for _, x := range must(t, a.get(fmt.Sprintf("catalog_items/%d/quota_usage/", item)), 200).results() {
		team, _ := x["team"].(map[string]any)
		user, _ := x["user"].(map[string]any)
		if (team != nil && team["name"] == holder) || (user != nil && user["username"] == holder) {
			return x
		}
	}
	return nil
}

// An item's quotas count each team's deployments, or a user's when they
// request for no team, and their requests of the day: over quota, a request
// is refused or waits for the item's owner team, as the item says. A failed
// provisioning frees its place, a failed teardown keeps it, and a hand-over
// needs room in the quota of whoever gets the deployment.
func TestCatalogQuotas(t *testing.T) {
	requireAnsible(t)
	f := newApprovalFixture(t)
	a := f.a
	out := t.TempDir()
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("quota"), "organization": f.org, "scm_type": "git",
		"scm_url": gitRepo(t, map[string]string{"provision.yml": flakyProvisionPlaybook, "teardown.yml": strings.ReplaceAll(teardownPlaybook, "OUT", out)})}), 201).id()
	f.waitProject(t)
	prov := f.template(t, "provision.yml", map[string]any{"survey_enabled": true, "outputs": []any{map[string]any{"name": "host", "type": "string"},
		map[string]any{"name": "db_user", "type": "string"}, map[string]any{"name": "db_password", "type": "string", "secret": true}}})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", prov), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Name", "variable": "name", "type": "text", "required": true},
	}}), 200)
	teardown := f.template(t, "teardown.yml", nil)

	for what, body := range map[string]map[string]any{
		"a deployment quota on an item that runs once": {"max_deployments": 2},
		"a quota of none": {"kind": "deployment", "max_deployments": 0},
		"an unknown answer to a request over quota": {"over_quota": "maybe"},
	} {
		body["name"], body["unified_job_template"] = uniq("bad"), prov
		if r := a.post("catalog_items/", body); r.Status != 400 {
			t.Errorf("%s: %d %s", what, r.Status, r.Raw)
		}
	}
	name := uniq("lab")
	item := must(t, a.post("catalog_items/", map[string]any{"name": name, "unified_job_template": prov, "kind": "deployment", "max_deployments": 2,
		"teardown_template": teardown, "deployment_name": "answers.name", "owner_team": f.platform}), 201).id()
	must(t, a.post("role_team_assignments/", map[string]any{"role_definition": roleDef(t, "CatalogItem Requester"), "team": f.team, "object_id": item}), 201)
	teamName := fmt.Sprint(must(t, a.get(fmt.Sprintf("teams/%d/", f.team)), 200).Body["name"])
	form := fmt.Sprintf("catalog_items/%d/request/", item)
	used := func() string {
		q := must(t, f.requester.get(form), 200).Body["quota"].(map[string]any)
		u := q["usage"].([]any)[0].(map[string]any)
		return fmt.Sprintf("%v %v of %v", u["team"].(map[string]any)["name"] == teamName, u["deployments"], q["max_deployments"])
	}
	provision := func(n string) map[string]any {
		t.Helper()
		r := waitRequest(t, a, must(t, f.requester.post(form, map[string]any{"answers": map[string]any{"name": n}}), 201).Body["id"])
		if r["status"] == "successful" {
			waitFor(t, "the deployment", func() bool { return f.get(t, r)["deployment"] != nil })
			r = f.get(t, r)
		}
		return r
	}
	if got := used(); got != "true 0 of 2" {
		t.Errorf("the form's quota: %s", got)
	}

	// A failed provisioning gives its place back.
	first := provision("a")
	if r := provision("broken"); r["status"] != "failed" {
		t.Fatalf("the broken provisioning: %v", r)
	}
	if got := used(); got != "true 1 of 2" {
		t.Errorf("after a failed provisioning: %s", got)
	}
	second := provision("b")
	over := f.requester.post(form, map[string]any{"answers": map[string]any{"name": "c"}})
	quota, _ := over.Body["quota"].(map[string]any)
	if over.Status != 400 || !strings.Contains(fmt.Sprint(over.Body["detail"]), "Over quota: "+teamName+" has 2 of its 2 deployments of "+name) ||
		quota == nil || quota["deployments"] != float64(2) {
		t.Errorf("over quota: %d %s", over.Status, over.Raw)
	}

	// Over quota, it waits for the owner team when the item says so.
	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"over_quota": "approve"}), 200)
	held := f.request(t, f.requester, item, map[string]any{"name": "c"})
	gate := held["approvals"].([]any)[0].(map[string]any)
	if held["status"] != "awaiting_approval" || gate["kind"] != "quota" || gate["rule"] != "Over quota" || len(gate["approvers"].([]any)) != 2 {
		t.Fatalf("held over quota: %v", held)
	}
	must(t, approve(f.p1, approvalIDs(held)[0], ""), 204)
	if r := waitRequest(t, a, held["id"]); r["status"] != "successful" {
		t.Fatalf("approved over quota: %v", r)
	}
	if got := used(); got != "true 3 of 2" {
		t.Errorf("past the quota: %s", got)
	}

	// Admins see the usage; requesters don't.
	must(t, f.requester.get(fmt.Sprintf("catalog_items/%d/quota_usage/", item)), 403)
	if u := usageOf(t, a, item, teamName); u == nil || u["deployments"] != float64(3) || u["requests"] != float64(4) || u["max_deployments"] != float64(2) {
		t.Errorf("usage: %v", u)
	}

	// A failed teardown keeps its place until it's cleaned up.
	dep := fmt.Sprintf("deployments/%v/", first["deployment"])
	if err := os.WriteFile(filepath.Join(out, "fail-teardown"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, f.requester.delete(dep), 202)
	waitFor(t, "the failed teardown", func() bool { return must(t, a.get(dep), 200).Body["status"] == "delete_failed" })
	if got := used(); got != "true 3 of 2" {
		t.Errorf("after a failed teardown: %s", got)
	}
	os.Remove(filepath.Join(out, "fail-teardown"))
	must(t, f.requester.delete(dep), 202)
	waitFor(t, "the teardown", func() bool { return must(t, a.get(dep), 200).Body["status"] == "deleted" })
	if got := used(); got != "true 2 of 2" {
		t.Errorf("after the teardown: %s", got)
	}

	// Whoever gets a deployment needs room for it, unless an admin hands it over.
	dep = fmt.Sprintf("deployments/%v/", second["deployment"])
	must(t, f.requester.post(dep+"hand_over/", map[string]any{"user": f.leadID, "team": nil}), 200)
	if u := usageOf(t, a, item, f.lead.user); u == nil || u["deployments"] != float64(1) || u["team"] != nil {
		t.Errorf("the lead's usage: %v", u)
	}
	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"max_deployments": 1}), 200)
	if r := f.lead.post(dep+"hand_over/", map[string]any{"team": f.team}); r.Status != 400 ||
		!strings.Contains(fmt.Sprint(r.Body["detail"]), "can't take this one") {
		t.Errorf("a hand-over over quota: %d %s", r.Status, r.Raw)
	}
	must(t, f.admin.post(dep+"hand_over/", map[string]any{"team": f.team}), 200)

	// Requests of the day count for each team, or for the requester alone.
	once := f.item(t, map[string]any{"max_requests_per_day": 2})
	for range 2 {
		waitRequest(t, a, f.request(t, f.admin, once, map[string]any{"size": 1})["id"])
	}
	again := f.admin.post(fmt.Sprintf("catalog_items/%d/request/", once), map[string]any{"answers": map[string]any{"size": 1}})
	if again.Status != 400 || !strings.Contains(fmt.Sprint(again.Body["detail"]), "made 2 of its 2 requests") ||
		!strings.Contains(fmt.Sprint(again.Body["detail"]), "The next one fits from") {
		t.Errorf("over the day's quota: %d %s", again.Status, again.Raw)
	}
	q := must(t, f.admin.get(fmt.Sprintf("catalog_items/%d/request/", once)), 200).Body["quota"].(map[string]any)
	if u := q["usage"].([]any)[0].(map[string]any); u["user"].(map[string]any)["id"] != float64(f.adminID) || u["requests"] != float64(2) ||
		q["max_requests_per_day"] != float64(2) || q["max_deployments"] != nil {
		t.Errorf("the admin's form: %v", q)
	}
	f.request(t, f.requester, once, map[string]any{"size": 1})
}
