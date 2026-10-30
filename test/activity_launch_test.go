package test

import (
	"fmt"
	"strings"
	"testing"
)

// Job launches, and roles given or taken through the role assignment API,
// are in the activity stream with the user who made them, as in AWX.
func TestActivityStreamLaunchesAndAssignments(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	u, uid := newUser(t, a)
	username := must(t, a.get(fmt.Sprintf("users/%d/", uid)), 200).Body["username"]
	actor := func(e map[string]any) any {
		sf, _ := e["summary_fields"].(map[string]any)
		if who, ok := sf["actor"].(map[string]any); ok {
			return who["username"]
		}
		return nil
	}
	entries := func(path string) []map[string]any {
		t.Helper()
		return must(t, a.get(path), 200).results()
	}

	// A role given through the role assignment API, as the role API records it.
	jt := f.template(t, "site.yml", map[string]any{"survey_enabled": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Secret", "variable": "secret", "type": "password", "required": true},
	}}), 200)
	asg := must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "JobTemplate Execute"), "user": uid, "object_id": jt}), 201).id()
	linked := func(op string) map[string]any {
		t.Helper()
		for _, e := range entries(fmt.Sprintf("users/%d/activity_stream/?operation=%s", uid, op)) {
			if e["object1"] == "role" && e["object2"] == "user" {
				return e
			}
		}
		t.Fatalf("no %s entry for the user", op)
		return nil
	}
	e := linked("associate")
	if actor(e) != "admin" || e["object_association"] != "role.users" && e["object_relationship_type"] != "role.users" {
		t.Errorf("assignment entry: %v", e)
	}
	role, _ := e["summary_fields"].(map[string]any)["role"].([]any)
	jtName := must(t, a.get(fmt.Sprintf("job_templates/%d/", jt)), 200).Body["name"]
	if len(role) != 1 || role[0].(map[string]any)["role_field"] != "execute_role" || role[0].(map[string]any)["resource_type"] != "job_template" ||
		role[0].(map[string]any)["resource_name"] != jtName {
		t.Errorf("role summary: %v", e["summary_fields"])
	}

	// A launch by that user: its fields, credentials and labels by name, and
	// survey passwords hidden.
	job := must(t, u.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": map[string]any{"secret": "hunter2"}}), 201).id()
	waitJob(t, a, fmt.Sprintf("jobs/%d/", job))
	got := entries(fmt.Sprintf("jobs/%d/activity_stream/", job))
	if len(got) != 1 || got[0]["operation"] != "create" || got[0]["object1"] != "job" || actor(got[0]) != username {
		t.Fatalf("launch entries: %v", got)
	}
	ch, _ := got[0]["changes"].(map[string]any)
	if ch["name"] == nil || ch["launch_type"] != "manual" || fmt.Sprint(ch["credentials"]) == "" || ch["labels"] == nil ||
		strings.Contains(fmt.Sprint(ch["extra_vars"]), "hunter2") || !strings.Contains(fmt.Sprint(ch["extra_vars"]), "$encrypted$") {
		t.Errorf("launch changes: %v", ch)
	}
	// A relaunch too.
	again := must(t, u.post(fmt.Sprintf("jobs/%d/relaunch/", job), map[string]any{}), 201).id()
	waitJob(t, a, fmt.Sprintf("jobs/%d/", again))
	if got := entries(fmt.Sprintf("jobs/%d/activity_stream/", again)); len(got) != 1 || actor(got[0]) != username {
		t.Errorf("relaunch entries: %v", got)
	}

	// Workflow launches.
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org}), 201).id()
	wj := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), nil), 201).id()
	if got := entries(fmt.Sprintf("workflow_jobs/%d/activity_stream/", wj)); len(got) != 1 || got[0]["object1"] != "workflow_job" || actor(got[0]) != "admin" {
		t.Errorf("workflow launch entries: %v", got)
	}

	// Taken away through the role assignment API.
	must(t, a.delete(fmt.Sprintf("role_user_assignments/%d/", asg)), 204)
	if e := linked("disassociate"); actor(e) != "admin" {
		t.Errorf("unassignment entry: %v", e)
	}

	// A role definition with no role on the object is recorded on the definition.
	rd := must(t, a.post("role_definitions/", map[string]any{"name": uniq("Launcher"), "content_type": "awx.jobtemplate",
		"permissions": []string{"awx.view_jobtemplate", "awx.execute_jobtemplate"}}), 201).id()
	must(t, a.post("role_team_assignments/", map[string]any{"role_definition": rd, "team": must(t, a.post("teams/", map[string]any{"name": uniq("t"), "organization": f.org}), 201).id(), "object_id": jt}), 201)
	found := false
	for _, e := range entries(fmt.Sprintf("activity_stream/?object1=role_definition&operation=associate")) {
		ch, _ := e["changes"].(map[string]any)
		if e["object2"] == "team" && ch["object_id"] == float64(jt) && ch["content_type"] == "awx.jobtemplate" && actor(e) == "admin" {
			found = true
		}
	}
	if !found {
		t.Error("no entry for the custom role definition")
	}
}
