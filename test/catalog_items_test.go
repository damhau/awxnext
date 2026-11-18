package test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

type catalogFixture struct {
	*jobFixture
	jt, cred int64
}

// newCatalogFixture: a template with a survey, a template variable and a
// machine credential, for items to publish.
func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()
	f := newLaunchFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", map[string]any{"survey_enabled": true, "extra_vars": "internal_flag: kept-from-requesters\n"})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Environment", "variable": "env", "type": "multiplechoice", "choices": []any{"dev", "staging", "prod"}, "default": "dev", "required": true},
		map[string]any{"question_name": "Size", "variable": "size", "type": "integer", "min": 1, "max": 10, "required": true},
		map[string]any{"question_name": "Owner", "variable": "owner", "type": "text", "required": false},
		map[string]any{"question_name": "Token", "variable": "token", "type": "password", "required": true},
		map[string]any{"question_name": "Target", "variable": "target", "type": "multiplechoice", "required": false, "choices_from": map[string]any{"type": "hosts"}},
	}}), 200)
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("ssh"), "organization": f.org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "deploy", "password": "cred-secret"}}), 201).id()
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": cred}), 204)
	return &catalogFixture{jobFixture: f, jt: jt, cred: cred}
}

func grant(t *testing.T, a *client, role string, uid, obj int64) {
	t.Helper()
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, role), "user": uid, "object_id": obj}), 201)
}

func variables(qs any) []string {
	out := []string{}
	list, _ := qs.([]any)
	for _, q := range list {
		out = append(out, fmt.Sprint(q.(map[string]any)["variable"]))
	}
	return out
}

// A catalog item publishes a template to people with no role on it: only
// an Admin of the template publishes it, the item's form is a view over the
// survey (reworded, narrowed, with fixed answers), a request that answers
// a fixed question is refused, and the request's job is not the
// requester's to see.
func TestCatalogItems(t *testing.T) {
	f := newCatalogFixture(t)
	a := f.a
	item := func(id int64) string { return fmt.Sprintf("catalog_items/%d/", id) }
	questions := map[string]any{
		"env":    map[string]any{"question_name": "Stage", "choices": []any{"dev", "staging"}},
		"token":  map[string]any{"value": "item-secret"},
		"owner":  map[string]any{"value": "platform", "visible": true},
		"target": map[string]any{"filter": `value != "h3"`},
	}
	body := map[string]any{"name": uniq("item"), "unified_job_template": f.jt, "category": "Network", "icon": "server", "questions": questions}

	// Execute on the template is not enough to publish it; Admin is.
	operator, opID := newUser(t, a)
	grant(t, a, "JobTemplate Execute", opID, f.jt)
	must(t, operator.post("catalog_items/", body), 403)
	owner, ownerID := newUser(t, a)
	grant(t, a, "JobTemplate Admin", ownerID, f.jt)

	// The questions are checked against the survey.
	for name, qs := range map[string]map[string]any{
		"unknown variable":       {"nope": map[string]any{"value": 1}},
		"unknown key":            {"env": map[string]any{"label": "x"}},
		"hidden without default": {"size": map[string]any{"hidden": true}},
		"fixed answer not valid": {"env": map[string]any{"value": "qa"}},
		"not a survey choice":    {"env": map[string]any{"choices": []any{"dev", "qa"}}},
		"visible password":       {"token": map[string]any{"value": "x", "visible": true}},
		"visible without value":  {"owner": map[string]any{"visible": true}},
		"filter on fixed list":   {"env": map[string]any{"filter": "true"}},
		"bad filter":             {"target": map[string]any{"filter": "value ==="}},
	} {
		b := map[string]any{"name": uniq("bad"), "unified_job_template": f.jt, "questions": qs}
		if r := owner.post("catalog_items/", b); r.Status != 400 || !strings.Contains(r.Raw, "questions") {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
	}
	must(t, owner.post("catalog_items/", map[string]any{"name": uniq("bad"), "unified_job_template": f.jt, "max_lifetime_days": 3}), 400)

	created := must(t, owner.post("catalog_items/", body), 201).Body
	id := int64(created["id"].(float64))
	if created["organization"] != float64(f.org) || created["kind"] != "run_once" ||
		created["questions"].(map[string]any)["token"].(map[string]any)["value"] != "$encrypted$" {
		t.Fatalf("item: %v", created)
	}
	// The creator administers the item; $encrypted$ keeps the fixed password.
	q2 := map[string]any{}
	for k, v := range questions {
		q2[k] = v
	}
	q2["token"] = map[string]any{"value": "$encrypted$"}
	must(t, owner.patch(item(id), map[string]any{"questions": q2, "description": "Ask for a VM"}), 200)

	// A user with only the request role.
	requester, reqID := newUser(t, a)
	grant(t, a, "CatalogItem Requester", reqID, id)
	if r := requester.post(fmt.Sprintf("job_templates/%d/launch/", f.jt), map[string]any{}); r.Status != 403 && r.Status != 404 {
		t.Errorf("requester launched the template: %d", r.Status)
	}
	if r := requester.get(fmt.Sprintf("credentials/%d/", f.cred)); r.Status != 403 && r.Status != 404 {
		t.Errorf("requester read the credential: %d", r.Status)
	}
	seen := must(t, requester.get(item(id)), 200).Body
	if _, ok := seen["questions"]; ok {
		t.Errorf("the requester sees the item's questions: %v", seen["questions"])
	}
	must(t, requester.get(item(id)+"history/"), 403)
	must(t, owner.get(item(id)+"history/"), 200)
	must(t, requester.patch(item(id), map[string]any{"description": "x"}), 403)

	// The form: the asked questions as the item words them, the fixed
	// answers it shows.
	form := must(t, requester.get(item(id)+"request/"), 200).Body
	if got := variables(form["questions"]); !slices.Equal(got, []string{"env", "size", "target"}) {
		t.Errorf("asked: %v", got)
	}
	env := form["questions"].([]any)[0].(map[string]any)
	if env["question_name"] != "Stage" || fmt.Sprint(env["choices"]) != "[dev staging]" {
		t.Errorf("env question: %v", env)
	}
	if fx := fmt.Sprint(form["fixed_answers"]); !strings.Contains(fx, "platform") {
		t.Errorf("fixed answers: %v", fx)
	}
	if strings.Contains(fmt.Sprint(form), "secret") || form["team_required"] != false || form["lifetime"] != nil {
		t.Errorf("form: %v", form)
	}
	choices := must(t, requester.get(item(id)+"survey_choices/?variable=target"), 200).Body
	if fmt.Sprint(choices["choices"]) != "[h1 h2]" {
		t.Errorf("target choices: %v", choices)
	}
	must(t, requester.get(item(id)+"survey_choices/?variable=token"), 404)

	// Refused requests.
	for name, b := range map[string]map[string]any{
		"fixed answer":       {"answers": map[string]any{"size": 2, "token": "mine"}},
		"shown fixed answer": {"answers": map[string]any{"size": 2, "owner": "me"}},
		"unknown variable":   {"answers": map[string]any{"size": 2, "extra": 1}},
		"narrowed choice":    {"answers": map[string]any{"size": 2, "env": "prod"}},
		"filtered choice":    {"answers": map[string]any{"size": 2, "target": "h3"}},
		"missing answer":     {"answers": map[string]any{}},
		"lifetime":           {"answers": map[string]any{"size": 2}, "lifetime_days": 3},
		"unknown field":      {"answers": map[string]any{"size": 2}, "extra_vars": map[string]any{"x": 1}},
	} {
		if r := requester.post(item(id)+"request/", b); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
	}
	if n := must(t, a.get(item(id)+"requests/"), 200).count(); n != 0 {
		t.Errorf("refused requests were recorded: %d", n)
	}

	// A request runs the template as the requester, with the item's answers.
	req := must(t, requester.post(item(id)+"request/", map[string]any{"answers": map[string]any{"env": "staging", "size": 3, "target": "h2"},
		"reason": "load test"}), 201).Body
	job := int64(req["job"].(float64))
	ans := req["answers"].(map[string]any)
	if req["catalog_item"] != float64(id) || req["reason"] != "load test" || req["team"] != nil || ans["env"] != "staging" ||
		ans["owner"] != "platform" || ans["token"] != nil || len(ans) != 4 {
		t.Fatalf("request: %v", req)
	}
	got := must(t, a.get(fmt.Sprintf("jobs/%d/", job)), 200).Body
	var ev map[string]any
	json.Unmarshal([]byte(got["extra_vars"].(string)), &ev)
	launcher, _ := got["summary_fields"].(map[string]any)["created_by"].(map[string]any)
	if launcher["id"] != float64(reqID) || got["launch_type"] != "manual" || ev["env"] != "staging" || ev["size"] != float64(3) ||
		ev["owner"] != "platform" || ev["token"] != "$encrypted$" || ev["internal_flag"] != "kept-from-requesters" {
		t.Errorf("job: %v %v", launcher, got["extra_vars"])
	}
	// The requester follows the request, not its job.
	if r := requester.get(fmt.Sprintf("jobs/%d/", job)); r.Status != 403 && r.Status != 404 {
		t.Errorf("the requester sees the job: %d", r.Status)
	}
	if n := must(t, requester.get("unified_jobs/"), 200).count(); n != 0 {
		t.Errorf("the requester lists the job: %d", n)
	}
	if n := must(t, requester.get("catalog_requests/"), 200).count(); n != 1 {
		t.Errorf("the requester's requests: %d", n)
	}
	must(t, requester.get(fmt.Sprintf("catalog_items/%d/requests/", id)), 200)
	other, _ := newUser(t, a)
	if n := must(t, other.get("catalog_requests/"), 200).count(); n != 0 {
		t.Errorf("another user sees the request: %d", n)
	}
	if n := must(t, owner.get("catalog_requests/"), 200).count(); n != 1 {
		t.Errorf("the item's admin sees %d requests", n)
	}
	// Their activity shows what they answered, not the item's answers.
	entries := must(t, requester.get("activity_stream/?object1=job"), 200).results()
	if len(entries) != 1 {
		t.Fatalf("requester's job entries: %v", entries)
	}
	if s := fmt.Sprint(entries[0]["changes"]); strings.Contains(s, "kept-from-requesters") || strings.Contains(s, "cred-secret") ||
		strings.Contains(s, "item-secret") || !strings.Contains(s, "staging") || strings.Contains(s, "credentials") {
		t.Errorf("job entry: %s", s)
	}

	// The request's status follows its job's.
	final := waitJob(t, a, fmt.Sprintf("jobs/%d/", job)).Body["status"]
	want := map[any]string{"successful": "successful", "canceled": "canceled"}[final]
	if want == "" {
		want = "failed"
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := must(t, requester.get(fmt.Sprintf("catalog_requests/%v/", req["id"])), 200).Body["status"]
		if st == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("request status %v, job %v", st, final)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// An item admin without Admin on the template changes how the item is
	// shown, not what runs.
	editor, edID := newUser(t, a)
	grant(t, a, "CatalogItem Admin", edID, id)
	must(t, editor.patch(item(id), map[string]any{"description": "Ask for a small VM", "category": "Compute"}), 200)
	must(t, editor.patch(item(id), map[string]any{"questions": map[string]any{}}), 403)

	// The template lists the item among what uses it.
	if !strings.Contains(must(t, a.get(fmt.Sprintf("job_templates/%d/used_by/", f.jt)), 200).Raw, "catalog_items") {
		t.Error("used_by does not list the item")
	}
}

// The team a request is for, a deployment's lifetime, and the organization
// role that lets its members request every item.
func TestCatalogItemTeamsAndLifetime(t *testing.T) {
	f := newCatalogFixture(t)
	a := f.a
	dep := must(t, a.post("catalog_items/", map[string]any{"name": uniq("vm"), "unified_job_template": f.jt, "kind": "deployment",
		"max_lifetime_days": 7, "default_lifetime_days": 3, "questions": map[string]any{"token": map[string]any{"value": "t"}}}), 201).id()
	must(t, a.post("catalog_items/", map[string]any{"name": uniq("vm"), "unified_job_template": f.jt, "kind": "deployment",
		"max_lifetime_days": 3, "default_lifetime_days": 7}), 400)

	u, uid := newUser(t, a)
	path := fmt.Sprintf("catalog_items/%d/request/", dep)
	must(t, u.get(path), 403)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization CatalogItem Requester"), "user": uid,
		"object_id": f.org}), 201)
	if n := must(t, u.get("catalog_items/"), 200).count(); n != 1 {
		t.Fatalf("the org requester lists %d items", n)
	}
	answers := map[string]any{"size": 1}

	// No team: the request is for none; one team: that one; several: say which.
	if r := must(t, u.post(path, map[string]any{"answers": answers}), 201).Body; r["team"] != nil || r["lifetime_days"] != float64(3) {
		t.Errorf("request without team: %v", r)
	}
	team := func() int64 {
		id := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": f.org}), 201).id()
		must(t, a.post(fmt.Sprintf("teams/%d/users/", id), map[string]any{"id": uid}), 204)
		return id
	}
	t1 := team()
	if r := must(t, u.post(path, map[string]any{"answers": answers, "lifetime_days": 7}), 201).Body; r["team"] != float64(t1) || r["lifetime_days"] != float64(7) {
		t.Errorf("request with one team: %v", r)
	}
	t2 := team()
	form := must(t, u.get(path), 200).Body
	if form["team_required"] != true || len(form["teams"].([]any)) != 2 || form["lifetime"].(map[string]any)["max_days"] != float64(7) {
		t.Errorf("form: %v", form)
	}
	must(t, u.post(path, map[string]any{"answers": answers}), 400)
	notMine := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": f.org}), 201).id()
	must(t, u.post(path, map[string]any{"answers": answers, "team": notMine}), 400)
	must(t, u.post(path, map[string]any{"answers": answers, "team": t2, "lifetime_days": 8}), 400)
	if r := must(t, u.post(path, map[string]any{"answers": answers, "team": t2}), 201).Body; r["team"] != float64(t2) {
		t.Errorf("request for a team: %v", r)
	}

	// A workflow template: its job, and its nodes' jobs, are hidden too.
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": f.template(t, "site.yml", nil)}), 201)
	wfItem := must(t, a.post("catalog_items/", map[string]any{"name": uniq("wf"), "unified_job_template": wf}), 201).id()
	r := must(t, u.post(fmt.Sprintf("catalog_items/%d/request/", wfItem), map[string]any{"team": t1}), 201).Body
	wj := int64(r["job"].(float64))
	if r := u.get(fmt.Sprintf("workflow_jobs/%d/", wj)); r.Status != 403 && r.Status != 404 {
		t.Errorf("the requester sees the workflow job: %d", r.Status)
	}
	waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj))
	nodes := must(t, a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", wj)), 200).results()
	if len(nodes) != 1 || nodes[0]["job"] == nil {
		t.Fatalf("nodes: %v", nodes)
	}
	if r := u.get(fmt.Sprintf("jobs/%v/", nodes[0]["job"])); r.Status != 403 && r.Status != 404 {
		t.Errorf("the requester sees the node's job: %d", r.Status)
	}

	// A template's items follow it to another organization, and its
	// requesters there.
	org2 := newOrg(t, a)
	must(t, a.patch(fmt.Sprintf("workflow_job_templates/%d/", wf), map[string]any{"organization": org2}), 200)
	if moved := must(t, a.get(fmt.Sprintf("catalog_items/%d/", wfItem)), 200).Body; moved["organization"] != float64(org2) {
		t.Errorf("item after its template moved: %v", moved["organization"])
	}
	if r := u.get(fmt.Sprintf("catalog_items/%d/", wfItem)); r.Status != 403 && r.Status != 404 {
		t.Errorf("the old organization's requester sees the moved item: %d", r.Status)
	}

	// Deleting the template deletes its items; their requests stay.
	must(t, a.delete(fmt.Sprintf("workflow_job_templates/%d/", wf)), 204)
	must(t, a.get(fmt.Sprintf("catalog_items/%d/", wfItem)), 404)
	if n := must(t, u.get("catalog_requests/?item_name="+url.QueryEscape(r["item_name"].(string))), 200).count(); n != 1 {
		t.Errorf("requests of a deleted item: %d", n)
	}
}

// Catalog items are declared in GitOps, with their templates (the item's,
// its teardown's and its actions') by kind and name and their owner team by
// name.
func TestCatalogItemGitOps(t *testing.T) {
	f := newCatalogFixture(t)
	a := f.a
	jtName := must(t, a.get(fmt.Sprintf("job_templates/%d/", f.jt)), 200).Body["name"]
	teamName := uniq("platform")
	team := must(t, a.post("teams/", map[string]any{"name": teamName, "organization": f.org}), 201).id()
	doc := fmt.Sprintf(`kind: CatalogItem
name: Small VM
description: A VM for a few days
spec:
  template: {kind: JobTemplate, name: %[1]s}
  kind: deployment
  category: Compute
  owner_team: %[2]s
  max_lifetime_days: 14
  questions:
    env: {choices: [dev, staging]}
    token: {value: from-git}
  approval_rules:
    - {name: Large, condition: size > 5, approvers: team, team: %[2]s}
  teardown_template: {kind: JobTemplate, name: %[1]s}
  extension_days: [1, 3]
  max_extensions: 2
  max_deployments: 3
  over_quota: approve
  actions:
    - {id: restart, name: Restart, template: {kind: JobTemplate, name: %[1]s}}
`, jtName, teamName)
	repo := gitRepo(t, map[string]string{"gitops/catalog.yml": doc})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": f.org, "scm_type": "git", "scm_url": "file://" + repo}), 201).id()
	waitProjectSynced(t, a, proj, "")
	sid := must(t, a.post("gitops_sources/", map[string]any{"name": uniq("src"), "organization": f.org, "project": proj, "path": "gitops",
		"sync_on_project_update": false}), 201).id()
	rep := must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), nil), 202)
	if summary(rep, "create") != 1 || summary(rep, "error") != 0 {
		t.Fatalf("sync: %s", rep.Raw)
	}
	it := one(t, a, fmt.Sprintf("catalog_items/?organization=%d&name=%s", f.org, url.QueryEscape("Small VM")))
	if it["managed_by"] != fmt.Sprintf("gitops:%d", sid) || it["unified_job_template"] != float64(f.jt) || it["owner_team"] != float64(team) ||
		it["kind"] != "deployment" || it["max_lifetime_days"] != float64(14) || it["teardown_template"] != float64(f.jt) ||
		fmt.Sprint(it["actions"]) != fmt.Sprintf("[map[description: id:restart name:Restart template:%d]]", f.jt) ||
		fmt.Sprint(it["extension_days"]) != "[1 3]" || it["max_extensions"] != float64(2) ||
		it["max_deployments"] != float64(3) || it["over_quota"] != "approve" {
		t.Fatalf("item: %v", it)
	}
	must(t, a.patch(fmt.Sprintf("catalog_items/%v/", it["id"]), map[string]any{"description": "x"}), 409)
	// A second sync changes nothing, the fixed password included.
	rep = must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), nil), 202)
	if summary(rep, "unchanged") != 1 {
		t.Fatalf("second sync: %s", rep.Raw)
	}
}
