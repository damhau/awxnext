package test

import (
	"fmt"
	"testing"
)

// django-ansible-base's role metadata and the ansible_id of assignments, as
// AWX 24.6 serves them.
func TestRoleMetadataAndAnsibleIDs(t *testing.T) {
	a := adminClient(t)
	allowed := must(t, a.get("role_metadata/"), 200).Body["allowed_permissions"].(map[string]any)
	contains := func(key, perm string) bool {
		for _, p := range allowed[key].([]any) {
			if p == perm {
				return true
			}
		}
		return false
	}
	if !contains("awx.credential", "awx.use_credential") || !contains("shared.organization", "shared.view_organization") ||
		!contains("shared.organization", "awx.add_project") || !contains("shared.team", "shared.member_team") {
		t.Errorf("allowed_permissions: %v", allowed)
	}
	rd := must(t, a.get(fmt.Sprintf("role_definitions/%d/", roleDef(t, "Organization Admin"))), 200).Body
	perms := map[string]bool{}
	for _, p := range rd["permissions"].([]any) {
		perms[p.(string)] = true
	}
	if !perms["shared.change_organization"] || !perms["awx.add_project"] || rd["content_type"] != "shared.organization" {
		t.Errorf("role definition: %v %v", rd["content_type"], rd["permissions"])
	}

	org := newOrg(t, a)
	_, uid := newUser(t, a)
	var userAID, orgAID string
	app.DB.QueryRow(t.Context(), "SELECT ansible_id::text FROM users WHERE id = $1", uid).Scan(&userAID)
	app.DB.QueryRow(t.Context(), "SELECT ansible_id::text FROM organizations WHERE id = $1", org).Scan(&orgAID)
	r := must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": uid, "object_id": org}), 201)
	if r.Body["user_ansible_id"] != userAID || r.Body["object_ansible_id"] != orgAID {
		t.Errorf("assignment: %s", r.Raw)
	}
	// By ansible_id instead of ids.
	r = must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Audit"),
		"user_ansible_id": userAID, "object_ansible_id": orgAID}), 201)
	if r.Body["user"] != float64(uid) || r.Body["object_id"] != fmt.Sprint(org) {
		t.Errorf("assignment by ansible_id: %s", r.Raw)
	}
	team := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": org}), 201).id()
	var teamAID string
	app.DB.QueryRow(t.Context(), "SELECT ansible_id::text FROM teams WHERE id = $1", team).Scan(&teamAID)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	r = must(t, a.post("role_team_assignments/", map[string]any{"role_definition": roleDef(t, "Inventory Use"), "team_ansible_id": teamAID, "object_id": inv}), 201)
	if r.Body["team"] != float64(team) || r.Body["team_ansible_id"] != teamAID || r.Body["object_ansible_id"] != nil {
		t.Errorf("team assignment: %s", r.Raw)
	}
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Inventory Use"), "object_id": inv}), 400)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Inventory Use"),
		"user_ansible_id": "00000000-0000-0000-0000-000000000000", "object_id": inv}), 400)
}
