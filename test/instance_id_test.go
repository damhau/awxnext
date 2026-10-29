package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// An ec2 source uses AWX's defaults: hosts are matched by instance_id, so a
// host renamed in the cloud keeps its record; ec2_state=running enables
// hosts; groups left empty are not imported.
func TestCloudSourceInstanceID(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	org := newOrg(t, a)
	fakeCollections(t, a)
	data := filepath.Join(t.TempDir(), "ec2.json")
	write := func(v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := os.WriteFile(data, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host := func(name, id, state string) map[string]any {
		return map[string]any{"name": name, "groups": []any{"web"}, "vars": map[string]any{"instance_id": id, "ec2_state": state}}
	}
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": uniq("ec2"), "inventory": inv, "source": "ec2",
		"source_vars": "data: " + data, "overwrite": true}), 201).id()
	sync := func() map[string]map[string]any {
		t.Helper()
		u := waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
		if u.Body["status"] != "successful" {
			t.Fatalf("update %v: %v\n%s", u.Body["status"], u.Body["job_explanation"], a.get(fmt.Sprintf("inventory_updates/%v/stdout/?format=txt", u.Body["id"])).Raw)
		}
		out := map[string]map[string]any{}
		for _, h := range a.get(fmt.Sprintf("inventories/%d/hosts/", inv)).results() {
			out[h["name"].(string)] = h
		}
		return out
	}

	write(map[string]any{"groups": []any{"web", "unused"}, "hosts": []any{host("web-1", "i-111", "running"), host("web-2", "i-222", "stopped")}})
	hosts := sync()
	if len(hosts) != 2 || hosts["web-1"]["instance_id"] != "i-111" || hosts["web-1"]["enabled"] != true || hosts["web-2"]["enabled"] != false {
		t.Fatalf("first sync: %v", hosts)
	}
	if g := a.get(fmt.Sprintf("inventories/%d/groups/?name=unused", inv)).count(); g != 0 {
		t.Fatal("an empty group was imported")
	}
	id1 := hosts["web-1"]["id"]
	// Tag the host by hand; a rename in the cloud must keep it.
	must(t, a.patch(fmt.Sprintf("hosts/%v/", id1), map[string]any{"description": "keep me"}), 200)

	write(map[string]any{"groups": []any{"web"}, "hosts": []any{host("web-1-renamed", "i-111", "running"), host("web-2", "i-222", "running")}})
	hosts = sync()
	h := hosts["web-1-renamed"]
	if len(hosts) != 2 || h == nil || h["id"] != id1 || h["description"] != "keep me" || hosts["web-2"]["enabled"] != true {
		t.Fatalf("after the rename: %v", hosts)
	}
}
