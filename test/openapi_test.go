package test

import (
	"fmt"
	"testing"
)

func TestOpenAPI(t *testing.T) {
	setup(t)
	r := must(t, (&client{t: t}).get("/api/v2/openapi.json"), 200)
	if r.Body["openapi"] != "3.0.3" {
		t.Fatalf("not an OpenAPI document: %.200s", r.Raw)
	}
	paths := r.Body["paths"].(map[string]any)
	org := paths["/api/v2/organizations/"].(map[string]any)
	if org["get"] == nil || org["post"] == nil {
		t.Fatal("organizations list/create missing")
	}
	for _, p := range []string{"/api/v2/organizations/{id}/history/{version}/revert/", "/api/v2/teams/{id}/users/", "/api/v2/ping/", "/api/v2/settings/{cat}/"} {
		if paths[p] == nil {
			t.Errorf("missing path %s", p)
		}
	}
	schemas := r.Body["components"].(map[string]any)["schemas"].(map[string]any)
	w := schemas["TeamWrite"].(map[string]any)
	if fmt.Sprint(w["required"]) != "[name organization]" {
		t.Fatalf("TeamWrite required: %v", w["required"])
	}
	// operationIds must be unique for client generators.
	seen := map[string]string{}
	for p, item := range paths {
		for m, op := range item.(map[string]any) {
			o, ok := op.(map[string]any)
			if !ok {
				continue
			}
			id, _ := o["operationId"].(string)
			if id == "" {
				continue
			}
			if prev, dup := seen[id]; dup {
				t.Errorf("duplicate operationId %s: %s and %s %s", id, prev, m, p)
			}
			seen[id] = m + " " + p
		}
	}
}

func TestManagedObjectsAreReadOnly(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	team := must(t, a.post("teams/", map[string]any{"name": uniq("gitops"), "organization": org}), 201).id()
	// managed_by cannot be set through the API.
	r := must(t, a.patch(fmt.Sprintf("teams/%d/", team), map[string]any{"managed_by": "git"}), 200)
	if r.Body["managed_by"] != "" {
		t.Fatalf("managed_by writable: %v", r.Body["managed_by"])
	}
	app.DB.Exec(t.Context(), "UPDATE teams SET managed_by = 'git:infra/awx.git' WHERE id = $1", team)
	r = must(t, a.patch(fmt.Sprintf("teams/%d/", team), map[string]any{"description": "x"}), 409)
	if r.Body["detail"] != "This team is managed by git:infra/awx.git. Change it at the source." {
		t.Fatalf("message: %s", r.Raw)
	}
	must(t, a.delete(fmt.Sprintf("teams/%d/", team)), 409)
	r = must(t, a.get(fmt.Sprintf("teams/?managed_by__startswith=git:&id=%d", team)), 200)
	if r.count() != 1 {
		t.Fatal("managed_by not filterable")
	}
}
