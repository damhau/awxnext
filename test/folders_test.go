package test

import (
	"fmt"
	"testing"
)

func roleDef(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	if err := app.DB.QueryRow(t.Context(), "SELECT id FROM role_definitions WHERE name = $1", name).Scan(&id); err != nil {
		t.Fatalf("role definition %q: %v", name, err)
	}
	return id
}

func folderPath(r resp) []string {
	var out []string
	for _, p := range r.Body["summary_fields"].(map[string]any)["path"].([]any) {
		out = append(out, p.(map[string]any)["name"].(string))
	}
	return out
}

func TestFolders(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	other := newOrg(t, a)
	fa := must(t, a.post("folders/", map[string]any{"name": "A", "organization": org}), 201).id()
	fb := must(t, a.post("folders/", map[string]any{"name": "B", "organization": org, "parent": fa}), 201).id()
	fc := must(t, a.post(fmt.Sprintf("folders/%d/folders/", fb), map[string]any{"name": "C", "organization": org}), 201).id()

	r := must(t, a.get(fmt.Sprintf("folders/%d/", fc)), 200)
	if p := folderPath(r); fmt.Sprint(p) != "[A B C]" {
		t.Fatalf("path: %v", p)
	}
	// Parent must be in the same organization.
	must(t, a.post("folders/", map[string]any{"name": "X", "organization": other, "parent": fa}), 400)
	// Same name allowed under different parents, not under the same parent.
	dup := must(t, a.post("folders/", map[string]any{"name": "C", "organization": org, "parent": fa}), 201).id()
	must(t, a.post("folders/", map[string]any{"name": "C", "organization": org, "parent": fb}), 400)

	u, uid := newUser(t, a)
	must(t, u.get(fmt.Sprintf("folders/%d/", fb)), 403)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Folder Admin"), "user": uid, "object_id": fa}), 201)

	// Admin on A applies to descendants.
	must(t, u.get(fmt.Sprintf("folders/%d/", fc)), 200)
	must(t, u.patch(fmt.Sprintf("folders/%d/", fc), map[string]any{"description": "by u"}), 200)
	must(t, u.post("folders/", map[string]any{"name": "D", "organization": org, "parent": fb}), 201)
	// But not at the organization level.
	must(t, u.post("folders/", map[string]any{"name": "top", "organization": org}), 403)
	must(t, u.patch(fmt.Sprintf("organizations/%d/", org), map[string]any{"description": "x"}), 403)

	// Moving into a parent that already has a folder of that name conflicts.
	must(t, a.patch(fmt.Sprintf("folders/%d/", fc), map[string]any{"parent": fa}), 400)
	must(t, a.delete(fmt.Sprintf("folders/%d/", dup)), 204)
	// Move C under A: path and descendants update.
	must(t, a.post("folders/", map[string]any{"name": "E", "organization": org, "parent": fc}), 201)
	must(t, u.patch(fmt.Sprintf("folders/%d/", fc), map[string]any{"parent": fa}), 200)
	r = must(t, a.get(fmt.Sprintf("folders/?parent=%d&name=E", fc)), 200)
	if r.count() != 1 {
		t.Fatalf("E not found: %s", r.Raw)
	}
	e := must(t, a.get(fmt.Sprintf("folders/%d/", int64(r.results()[0]["id"].(float64)))), 200)
	if p := folderPath(e); fmt.Sprint(p) != "[A C E]" {
		t.Fatalf("descendant path after move: %v", p)
	}
	// Cycles are rejected.
	must(t, a.patch(fmt.Sprintf("folders/%d/", fa), map[string]any{"parent": fc}), 400)
	must(t, a.patch(fmt.Sprintf("folders/%d/", fa), map[string]any{"parent": fa}), 400)
	// Organization cannot change.
	must(t, a.patch(fmt.Sprintf("folders/%d/", fa), map[string]any{"organization": other}), 400)
	// Non-empty folders cannot be deleted.
	must(t, a.delete(fmt.Sprintf("folders/%d/", fa)), 409)

	// Read on a deep folder makes its ancestors navigable, but not siblings.
	v, vid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Folder Read"), "user": vid, "object_id": fc}), 201)
	r = must(t, v.get(fmt.Sprintf("folders/?organization=%d", org)), 200)
	names := map[string]bool{}
	for _, f := range r.results() {
		names[f["name"].(string)] = true
	}
	if !names["A"] || !names["C"] || !names["E"] || names["B"] || names["D"] {
		t.Fatalf("v sees %v", names)
	}
	must(t, v.patch(fmt.Sprintf("folders/%d/", fc), map[string]any{"description": "x"}), 403)
}

func TestNestedTeams(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	inner := must(t, a.post("teams/", map[string]any{"name": uniq("inner"), "organization": org}), 201).id()
	outer := must(t, a.post("teams/", map[string]any{"name": uniq("outer"), "organization": org}), 201).id()
	folder := must(t, a.post("folders/", map[string]any{"name": uniq("f"), "organization": org}), 201).id()

	w, wid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("teams/%d/users/", inner), map[string]any{"id": wid}), 204)
	// inner is a member of outer; outer administers the folder.
	must(t, a.post("role_team_assignments/", map[string]any{"role_definition": roleDef(t, "Team Member"), "team": inner, "object_id": outer}), 201)
	must(t, a.post("role_team_assignments/", map[string]any{"role_definition": roleDef(t, "Folder Admin"), "team": outer, "object_id": folder}), 201)

	must(t, w.patch(fmt.Sprintf("folders/%d/", folder), map[string]any{"description": "via nested team"}), 200)
	r := must(t, a.get(fmt.Sprintf("folders/%d/access_list/", folder)), 200)
	found := false
	for _, u := range r.results() {
		if int64(u["id"].(float64)) == wid {
			found = true
		}
	}
	if !found {
		t.Fatalf("access_list missing nested member: %s", r.Raw)
	}
	// Cycles in team membership do not break resolution.
	must(t, a.post("role_team_assignments/", map[string]any{"role_definition": roleDef(t, "Team Member"), "team": outer, "object_id": inner}), 201)
	must(t, w.get(fmt.Sprintf("folders/%d/", folder)), 200)
	// Removing the nesting revokes access.
	r = must(t, a.get(fmt.Sprintf("role_team_assignments/?team=%d&object_id=%d", inner, outer)), 200)
	must(t, a.delete(fmt.Sprintf("role_team_assignments/%d/", int64(r.results()[0]["id"].(float64)))), 204)
	must(t, w.get(fmt.Sprintf("folders/%d/", folder)), 403)
}
