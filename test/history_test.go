package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestVersionHistory(t *testing.T) {
	a := adminClient(t)
	name := uniq("hist")
	id := must(t, a.post("organizations/", map[string]any{"name": name, "description": "v1"}), 201).id()
	base := fmt.Sprintf("organizations/%d/", id)
	must(t, a.patch(base, map[string]any{"description": "v2"}), 200)
	must(t, a.patch(base, map[string]any{"description": "v3", "max_hosts": 10}), 200)
	// A no-op update does not create a version.
	must(t, a.patch(base, map[string]any{"description": "v3"}), 200)

	r := must(t, a.get(base+"history/"), 200)
	if r.count() != 3 {
		t.Fatalf("expected 3 versions, got %d: %s", r.count(), r.Raw)
	}
	latest := r.results()[0]
	if latest["version"].(float64) != 3 || latest["operation"] != "update" {
		t.Fatalf("latest version: %v", latest)
	}
	changes := latest["changes"].(map[string]any)
	if fmt.Sprint(changes["description"]) != "[v2 v3]" || fmt.Sprint(changes["max_hosts"]) != "[0 10]" {
		t.Fatalf("changes: %v", changes)
	}
	v1 := must(t, a.get(base+"history/1/"), 200)
	if v1.Body["data"].(map[string]any)["description"] != "v1" || v1.Body["operation"] != "create" {
		t.Fatalf("v1: %s", v1.Raw)
	}

	// Non-admins can read history of what they can see but cannot revert.
	u, uid := newUser(t, a)
	must(t, a.post(base+"users/", map[string]any{"id": uid}), 204)
	must(t, u.get(base+"history/"), 200)
	must(t, u.post(base+"history/1/revert/", nil), 403)

	r = must(t, a.post(base+"history/1/revert/", nil), 200)
	if r.Body["description"] != "v1" || r.Body["max_hosts"].(float64) != 0 {
		t.Fatalf("revert: %s", r.Raw)
	}
	r = must(t, a.get(base+"history/"), 200)
	// 3 updates + membership association + revert.
	if r.count() != 5 || r.results()[0]["operation"] != "revert" || r.results()[0]["comment"] != "Reverted to version 1" {
		t.Fatalf("after revert: %s", r.Raw)
	}
	must(t, a.get(base+"history/99/"), 404)

	// History survives deletion.
	must(t, a.delete(base), 204)
	var n int
	app.DB.QueryRow(t.Context(), "SELECT count(*) FROM object_versions WHERE resource = 'organizations' AND object_id = $1", id).Scan(&n)
	if n != 6 {
		t.Fatalf("expected 6 versions after delete, got %d", n)
	}
}

func TestVersionComment(t *testing.T) {
	a := adminClient(t)
	id := must(t, a.post("organizations/", map[string]any{"name": uniq("cmt")}), 201).id()
	body, _ := json.Marshal(map[string]any{"description": "faster"})
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("%s/api/v2/organizations/%d/", srv.URL, id), bytes.NewReader(body))
	req.SetBasicAuth("admin", "admin")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Change-Comment", "  Retries because the JVM takes 40s to warm.  ")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("patch: %v %v", err, res.Status)
	}
	res.Body.Close()
	latest := a.get(fmt.Sprintf("organizations/%d/history/", id)).results()[0]
	if latest["comment"] != "Retries because the JVM takes 40s to warm." {
		t.Fatalf("comment %q", latest["comment"])
	}
}
