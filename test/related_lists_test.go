package test

import (
	"fmt"
	"testing"
)

// AWX's sub-lists that cross resources: each under a parent the requester
// sees, listing what the requester sees of its target.
func TestRelatedLists(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	ids := func(c *client, path string) map[int64]bool {
		t.Helper()
		out := map[int64]bool{}
		for _, r := range must(t, c.get(path), 200).results() {
			out[int64(r["id"].(float64))] = true
		}
		return out
	}
	assign := func(kind string, who int64, role string, object int64) {
		t.Helper()
		must(t, a.post("role_"+kind+"_assignments/", map[string]any{"role_definition": roleDef(t, role), kind: who, "object_id": object}), 201)
	}
	other := must(t, a.post("projects/", map[string]any{"name": uniq("other"), "organization": f.org, "scm_type": ""}), 201).id()

	// teams/N/projects/ and users/N/projects/.
	team := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": f.org}), 201).id()
	assign("team", team, "Project Use", f.project)
	if got := ids(a, fmt.Sprintf("teams/%d/projects/", team)); !got[f.project] || got[other] {
		t.Errorf("team projects: %v", got)
	}
	member, memberID := newUser(t, a)
	assign("user", memberID, "Project Read", other)
	if got := ids(a, fmt.Sprintf("users/%d/projects/", memberID)); !got[other] || got[f.project] {
		t.Errorf("user projects: %v", got)
	}
	// Only what the requester sees too, under a parent the requester sees.
	if got := ids(member, fmt.Sprintf("users/%d/projects/", memberID)); !got[other] {
		t.Errorf("the user's own projects: %v", got)
	}
	must(t, member.get(fmt.Sprintf("teams/%d/projects/", team)), 403)

	// teams/N/credentials/: created there, the team gets the credential.
	ssh := credType(t, a, "ssh")
	cred := must(t, a.post(fmt.Sprintf("teams/%d/credentials/", team), map[string]any{"name": uniq("cred"), "organization": f.org, "credential_type": ssh}), 201).id()
	orgCred := must(t, a.post("credentials/", map[string]any{"name": uniq("cred"), "organization": f.org, "credential_type": ssh}), 201).id()
	if got := ids(a, fmt.Sprintf("teams/%d/credentials/", team)); !got[cred] || got[orgCred] {
		t.Errorf("team credentials: %v", got)
	}

	// users/N/access_list/: superusers, the user, the admins of their organizations.
	orgAdmin, orgAdminID := newUser(t, a)
	_, strangerID := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", f.org), map[string]any{"id": memberID}), 204)
	must(t, a.post(fmt.Sprintf("organizations/%d/admins/", f.org), map[string]any{"id": orgAdminID}), 204)
	got := ids(a, fmt.Sprintf("users/%d/access_list/", memberID))
	var adminID int64
	for id := range ids(a, "users/?username=admin") {
		adminID = id
	}
	if !got[memberID] || !got[orgAdminID] || !got[adminID] || got[strangerID] {
		t.Errorf("access list: %v (member %d, org admin %d, admin %d, stranger %d)", got, memberID, orgAdminID, adminID, strangerID)
	}
	must(t, orgAdmin.get(fmt.Sprintf("users/%d/access_list/", memberID)), 200)

	// inventories/N/job_templates/.
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	if got := ids(a, fmt.Sprintf("inventories/%d/job_templates/", f.inv)); !got[jt] {
		t.Errorf("inventory job templates: %v", got)
	}

	// A job: playbook_counts, event children, the group's events, create_schedule.
	job := f.launch(t, a, jt, map[string]any{"limit": "h1"})
	mustStatus(t, f, job, "successful")
	if pc := job.Body["playbook_counts"].(map[string]any); pc["play_count"] != float64(1) || pc["task_count"].(float64) < 3 {
		t.Errorf("playbook_counts: %v", pc)
	}
	play := must(t, a.get(fmt.Sprintf("jobs/%d/job_events/?event=playbook_on_play_start", job.id())), 200).results()[0]
	children := must(t, a.get(fmt.Sprintf("job_events/%v/children/", play["id"])), 200).results()
	if len(children) < 3 {
		t.Errorf("children of the play: %d", len(children))
	}
	for _, ch := range children {
		if ch["parent_uuid"] != play["uuid"] {
			t.Errorf("child %v of %v", ch["parent_uuid"], play["uuid"])
		}
	}
	group := must(t, a.post(fmt.Sprintf("inventories/%d/groups/", f.inv), map[string]any{"name": uniq("g")}), 201).id()
	h1 := int64(must(t, a.get(fmt.Sprintf("hosts/?inventory=%d&name=h1", f.inv)), 200).results()[0]["id"].(float64))
	must(t, a.post(fmt.Sprintf("groups/%d/hosts/", group), map[string]any{"id": h1}), 204)
	events := must(t, a.get(fmt.Sprintf("groups/%d/job_events/?job=%d", group, job.id())), 200)
	if events.count() == 0 {
		t.Error("no events for the group's hosts")
	}
	for _, e := range events.results() {
		if e["host"] != float64(h1) {
			t.Errorf("event of host %v", e["host"])
		}
	}
	cs := must(t, a.get(fmt.Sprintf("jobs/%d/create_schedule/", job.id())), 200).Body
	if cs["can_schedule"] != true || cs["prompts"].(map[string]any)["limit"] != "h1" {
		t.Errorf("create_schedule: %v", cs)
	}
	s := must(t, a.post(fmt.Sprintf("jobs/%d/create_schedule/", job.id()), nil), 201).Body
	if s["name"] != fmt.Sprintf("Auto-generated schedule from job %d", job.id()) || s["enabled"] != false || s["limit"] != "h1" ||
		s["unified_job_template"] != float64(jt) {
		t.Errorf("schedule: %v", s)
	}
	if s := must(t, a.post(fmt.Sprintf("jobs/%d/create_schedule/", job.id()), nil), 201).Body; s["name"] != fmt.Sprintf("Auto-generated schedule from job %d - number 1", job.id()) {
		t.Errorf("second schedule: %v", s["name"])
	}
	must(t, member.post(fmt.Sprintf("jobs/%d/create_schedule/", job.id()), nil), 403)

	// job_templates/N/slice_workflow_jobs/.
	sliced := f.template(t, "site.yml", map[string]any{"job_slice_count": 2})
	wf := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", sliced), nil), 201)
	if got := ids(a, fmt.Sprintf("job_templates/%d/slice_workflow_jobs/", sliced)); !got[wf.id()] || len(got) != 1 {
		t.Errorf("slice workflow jobs: %v (launched %d)", got, wf.id())
	}
	waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wf.id()))
}

// access_list entries say which roles give each user access and where they
// come from: the object, a team, the organization, or the system.
func TestAccessListDetails(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", nil)
	assign := func(kind string, who int64, role string, object int64) {
		t.Helper()
		must(t, a.post("role_"+kind+"_assignments/", map[string]any{"role_definition": roleDef(t, role), kind: who, "object_id": object}), 201)
	}
	_, directID := newUser(t, a)
	assign("user", directID, "JobTemplate Execute", jt)
	team := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": f.org}), 201).id()
	_, viaTeamID := newUser(t, a)
	assign("user", viaTeamID, "Team Member", team)
	assign("team", team, "JobTemplate Admin", jt)
	_, viaOrgID := newUser(t, a)
	assign("user", viaOrgID, "Organization Admin", f.org)

	byUser := map[int64]map[string]any{}
	for _, u := range must(t, a.get(fmt.Sprintf("job_templates/%d/access_list/?page_size=200", jt)), 200).results() {
		byUser[int64(u["id"].(float64))] = u["summary_fields"].(map[string]any)
	}
	only := func(id int64, key string) map[string]any {
		t.Helper()
		sf := byUser[id]
		if sf == nil {
			t.Fatalf("user %d not in the access list", id)
		}
		list := sf[key].([]any)
		if len(list) != 1 {
			t.Fatalf("user %d %s: %v", id, key, sf)
		}
		return list[0].(map[string]any)
	}
	has := func(entry map[string]any, roles ...string) bool {
		got := map[string]bool{}
		for _, r := range entry["descendant_roles"].([]any) {
			got[r.(string)] = true
		}
		for _, r := range roles {
			if !got[r] {
				return false
			}
		}
		return true
	}

	d := only(directID, "direct_access")
	if r := d["role"].(map[string]any); r["name"] != "Execute" || r["resource_type"] != "job_template" || r["id"] == nil ||
		r["user_capabilities"].(map[string]any)["unattach"] != true || !has(d, "execute_role", "read_role") || has(d, "admin_role") {
		t.Errorf("direct grant: %v", d)
	}
	tm := only(viaTeamID, "direct_access")
	if r := tm["role"].(map[string]any); r["name"] != "Admin" || r["team_id"] != float64(team) || r["team_name"] == nil || !has(tm, "admin_role") {
		t.Errorf("team grant: %v", tm)
	}
	o := only(viaOrgID, "indirect_access")
	if r := o["role"].(map[string]any); r["resource_type"] != "organization" || r["name"] != "Admin" || !has(o, "admin_role", "execute_role") {
		t.Errorf("organization grant: %v", o)
	}
	if len(byUser[viaOrgID]["direct_access"].([]any)) != 0 {
		t.Errorf("organization grant counted as direct: %v", byUser[viaOrgID])
	}
	var adminID int64
	for _, u := range must(t, a.get("users/?username=admin"), 200).results() {
		adminID = int64(u["id"].(float64))
	}
	if s := only(adminID, "indirect_access"); s["role"].(map[string]any)["name"] != "System Administrator" || !has(s, "admin_role") {
		t.Errorf("superuser: %v", s)
	}
}
