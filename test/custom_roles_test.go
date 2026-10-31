package test

import (
	"fmt"
	"strings"
	"testing"
)

// Custom role definitions (roadmap M7-17): made by a superuser from the
// permissions role_metadata/ allows for a type, they grant exactly those
// permissions, on the object or the organization they are assigned on, and
// edits apply to the people who already hold them.
func TestCustomRoleDefinitions(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", nil)
	other := f.template(t, "site.yml", nil)

	// What a definition may contain.
	bad := a.post("role_definitions/", map[string]any{"name": uniq("bad"), "content_type": "awx.jobtemplate", "permissions": []any{"awx.execute_jobtemplate"}})
	if bad.Status != 400 || !strings.Contains(fmt.Sprint(bad.Body), "awx.view_jobtemplate") {
		t.Fatalf("a role that cannot see what it grants: %d %v", bad.Status, bad.Body)
	}
	bad = a.post("role_definitions/", map[string]any{"name": uniq("bad"), "content_type": "awx.jobtemplate", "permissions": []any{"awx.view_jobtemplate", "awx.view_inventory"}})
	if bad.Status != 400 || !strings.Contains(fmt.Sprint(bad.Body), "not valid for content type") {
		t.Fatalf("a permission of another type: %d %v", bad.Status, bad.Body)
	}
	user, uid := newUser(t, a)
	must(t, user.post("role_definitions/", map[string]any{"name": uniq("mine"), "content_type": "awx.jobtemplate", "permissions": []any{"awx.view_jobtemplate"}}), 403)

	// A launcher: may see and launch, nothing else.
	rd := must(t, a.post("role_definitions/", map[string]any{"name": uniq("Template launcher"), "description": "Launch, without changing",
		"content_type": "awx.jobtemplate", "permissions": []any{"awx.view_jobtemplate", "awx.execute_jobtemplate"}}), 201)
	if rd.Body["managed"] != false || rd.Body["content_type"] != "awx.jobtemplate" {
		t.Fatalf("custom role: %v", rd.Body)
	}
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": rd.id(), "user": uid, "object_id": jt}), 201)
	must(t, user.get(fmt.Sprintf("job_templates/%d/", jt)), 200)
	must(t, user.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{}), 201)
	must(t, user.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"description": "changed"}), 403)
	must(t, user.delete(fmt.Sprintf("job_templates/%d/", jt)), 403)
	if r := user.get(fmt.Sprintf("job_templates/%d/", other)); r.Status != 403 && r.Status != 404 {
		t.Fatalf("another template: %d", r.Status)
	}

	// Edited: may change, no longer launch.
	must(t, a.patch(fmt.Sprintf("role_definitions/%d/", rd.id()), map[string]any{"permissions": []any{"awx.view_jobtemplate", "awx.change_jobtemplate"}}), 200)
	must(t, user.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"description": "changed"}), 200)
	must(t, user.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{}), 403)

	// On an organization: every inventory in it, read and use only.
	org := must(t, a.post("role_definitions/", map[string]any{"name": uniq("Inventory user"), "content_type": "shared.organization",
		"permissions": []any{"awx.view_inventory", "awx.use_inventory"}}), 201)
	user2, uid2 := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": org.id(), "user": uid2, "object_id": f.org}), 201)
	must(t, user2.get(fmt.Sprintf("inventories/%d/", f.inv)), 200)
	must(t, user2.patch(fmt.Sprintf("inventories/%d/", f.inv), map[string]any{"description": "changed"}), 403)
	if r := user2.get(fmt.Sprintf("job_templates/%d/", jt)); r.Status != 403 && r.Status != 404 {
		t.Fatalf("a template of the organization: %d", r.Status)
	}

	// The definitions the access tabs offer for an object's type include it.
	found := false
	for _, d := range must(t, a.get("role_definitions/?page_size=200"), 200).results() {
		found = found || (d["id"] == float64(rd.id()) && d["content_type"] == "awx.jobtemplate")
	}
	if !found {
		t.Errorf("the custom role is not listed for job templates")
	}
	// Deleting it removes the access it gave.
	must(t, a.delete(fmt.Sprintf("role_definitions/%d/", rd.id())), 204)
	if r := user.get(fmt.Sprintf("job_templates/%d/", jt)); r.Status != 403 && r.Status != 404 {
		t.Fatalf("after the role was deleted: %d", r.Status)
	}
}
