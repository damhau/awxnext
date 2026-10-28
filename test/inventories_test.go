package test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

type invFixture struct {
	org, inv     int64
	web, db, all int64 // groups: all -> web, all -> db
	hosts        map[string]int64
}

func newInventory(t *testing.T, a *client) invFixture {
	t.Helper()
	f := invFixture{org: newOrg(t, a), hosts: map[string]int64{}}
	f.inv = must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": f.org, "variables": "env: prod\nntp: [ntp1, ntp2]"}), 201).id()
	f.all = must(t, a.post(fmt.Sprintf("inventories/%d/groups/", f.inv), map[string]any{"name": "allservers"}), 201).id()
	f.web = must(t, a.post(fmt.Sprintf("groups/%d/children/", f.all), map[string]any{"name": "web", "variables": `{"http_port": 8080}`}), 201).id()
	f.db = must(t, a.post(fmt.Sprintf("groups/%d/children/", f.all), map[string]any{"name": "db"}), 201).id()
	for _, h := range []string{"web1", "web2"} {
		f.hosts[h] = must(t, a.post(fmt.Sprintf("groups/%d/hosts/", f.web), map[string]any{"name": h}), 201).id()
	}
	f.hosts["db1"] = must(t, a.post(fmt.Sprintf("groups/%d/hosts/", f.db), map[string]any{"name": "db1",
		"variables": "db_password: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  616263\n"}), 201).id()
	f.hosts["lonely"] = must(t, a.post("hosts/", map[string]any{"name": "lonely", "inventory": f.inv}), 201).id()
	f.hosts["off"] = must(t, a.post("hosts/", map[string]any{"name": "off", "inventory": f.inv, "enabled": false}), 201).id()
	return f
}

func TestInventoryStructure(t *testing.T) {
	a := adminClient(t)
	f := newInventory(t, a)

	inv := must(t, a.get(fmt.Sprintf("inventories/%d/", f.inv)), 200)
	if inv.Body["total_hosts"].(float64) != 5 || inv.Body["total_groups"].(float64) != 3 {
		t.Fatalf("counts: %v %v", inv.Body["total_hosts"], inv.Body["total_groups"])
	}
	// Cycles and cross-inventory associations are rejected.
	must(t, a.post(fmt.Sprintf("groups/%d/children/", f.web), map[string]any{"id": f.all}), 400)
	must(t, a.post(fmt.Sprintf("groups/%d/children/", f.web), map[string]any{"id": f.web}), 400)
	other := newInventory(t, a)
	must(t, a.post(fmt.Sprintf("groups/%d/hosts/", f.web), map[string]any{"id": other.hosts["web1"]}), 400)
	// Duplicate host names in one inventory are rejected.
	must(t, a.post("hosts/", map[string]any{"name": "web1", "inventory": f.inv}), 400)
	// Variables must be a mapping.
	must(t, a.post("hosts/", map[string]any{"name": uniq("h"), "inventory": f.inv, "variables": "- a\n- b"}), 400)
	must(t, a.post("hosts/", map[string]any{"name": uniq("h"), "inventory": f.inv, "variables": "a: [1"}), 400)

	r := must(t, a.get(fmt.Sprintf("inventories/%d/root_groups/", f.inv)), 200)
	if r.count() != 1 || r.results()[0]["name"] != "allservers" {
		t.Fatalf("root groups: %s", r.Raw)
	}
	r = must(t, a.get(fmt.Sprintf("groups/%d/all_hosts/", f.all)), 200)
	if r.count() != 3 {
		t.Fatalf("all_hosts: %d", r.count())
	}
	r = must(t, a.get(fmt.Sprintf("hosts/%d/all_groups/", f.hosts["web1"])), 200)
	if r.count() != 2 {
		t.Fatalf("all_groups: %d", r.count())
	}
	r = must(t, a.get(fmt.Sprintf("groups/%d/potential_children/", f.web)), 200)
	if r.count() != 1 || r.results()[0]["name"] != "db" {
		t.Fatalf("potential_children: %s", r.Raw)
	}
	tree := must(t, a.get(fmt.Sprintf("inventories/%d/tree/", f.inv)), 200)
	if !strings.Contains(tree.Raw, `"name":"allservers"`) || !strings.Contains(tree.Raw, `"name":"web"`) {
		t.Fatalf("tree: %s", tree.Raw)
	}
	// Filters through relations and JSON.
	r = must(t, a.get(fmt.Sprintf("hosts/?inventory=%d&groups__name=web", f.inv)), 200)
	if r.count() != 2 {
		t.Fatalf("groups__name filter: %d", r.count())
	}
	app.DB.Exec(t.Context(), `UPDATE hosts SET ansible_facts = '{"os_family": "RedHat", "distribution_major_version": "9"}' WHERE id = $1`, f.hosts["web1"])
	r = must(t, a.get(fmt.Sprintf("hosts/?inventory=%d&ansible_facts__os_family=RedHat", f.inv)), 200)
	if r.count() != 1 {
		t.Fatalf("facts filter: %d", r.count())
	}
	facts := must(t, a.get(fmt.Sprintf("hosts/%d/ansible_facts/", f.hosts["web1"])), 200)
	if facts.Body["os_family"] != "RedHat" {
		t.Fatalf("facts: %s", facts.Raw)
	}
}

func TestInventoryScript(t *testing.T) {
	a := adminClient(t)
	f := newInventory(t, a)
	r := must(t, a.get(fmt.Sprintf("inventories/%d/script/?hostvars=1", f.inv)), 200)
	all := r.Body["all"].(map[string]any)
	if fmt.Sprint(all["children"]) != "[allservers ungrouped]" || all["vars"].(map[string]any)["env"] != "prod" {
		t.Fatalf("all: %v", all)
	}
	web := r.Body["web"].(map[string]any)
	if fmt.Sprint(web["hosts"]) != "[web1 web2]" || web["vars"].(map[string]any)["http_port"] != float64(8080) {
		t.Fatalf("web: %v", web)
	}
	if fmt.Sprint(r.Body["allservers"].(map[string]any)["children"]) != "[db web]" {
		t.Fatalf("children: %v", r.Body["allservers"])
	}
	// Disabled hosts are excluded unless all=1.
	if fmt.Sprint(r.Body["ungrouped"].(map[string]any)["hosts"]) != "[lonely]" {
		t.Fatalf("ungrouped: %v", r.Body["ungrouped"])
	}
	hv := r.Body["_meta"].(map[string]any)["hostvars"].(map[string]any)
	vault := hv["db1"].(map[string]any)["db_password"].(map[string]any)
	if !strings.HasPrefix(vault["__ansible_vault"].(string), "$ANSIBLE_VAULT;1.1;AES256") {
		t.Fatalf("vault var: %v", vault)
	}
	r = must(t, a.get(fmt.Sprintf("inventories/%d/script/?all=1", f.inv)), 200)
	if fmt.Sprint(r.Body["ungrouped"].(map[string]any)["hosts"]) != "[lonely off]" {
		t.Fatalf("all=1: %v", r.Body["ungrouped"])
	}
	if _, ok := r.Body["_meta"]; ok {
		t.Fatal("_meta without hostvars=1")
	}
}

func TestSmartInventory(t *testing.T) {
	a := adminClient(t)
	f := newInventory(t, a)
	second := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": f.org}), 201).id()
	dup := must(t, a.post("hosts/", map[string]any{"name": "web1", "inventory": second}), 201).id()
	web3 := must(t, a.post("hosts/", map[string]any{"name": "web3", "inventory": second}), 201).id()
	ctx := t.Context()
	app.DB.Exec(ctx, `UPDATE hosts SET ansible_facts = '{"os_family": "RedHat", "ips": ["10.0.0.5"]}' WHERE id = ANY($1)`, []int64{f.hosts["web1"], dup, web3})
	app.DB.Exec(ctx, `UPDATE hosts SET ansible_facts = '{"os_family": "Debian"}' WHERE id = $1`, f.hosts["web2"])

	must(t, a.post("inventories/", map[string]any{"name": uniq("smart"), "organization": f.org, "kind": "smart"}), 400)
	must(t, a.post("inventories/", map[string]any{"name": uniq("smart"), "organization": f.org, "kind": "smart", "host_filter": "name=web and ("}), 400)
	must(t, a.post("inventories/", map[string]any{"name": uniq("x"), "organization": f.org, "host_filter": "name=web"}), 400)

	smart := must(t, a.post("inventories/", map[string]any{"name": uniq("smart"), "organization": f.org, "kind": "smart",
		"host_filter": `name__startswith=web and ansible_facts__os_family="RedHat"`}), 201)
	sid := smart.id()
	if smart.Body["total_hosts"].(float64) != 2 {
		t.Fatalf("smart total_hosts: %v", smart.Body["total_hosts"])
	}
	r := must(t, a.get(fmt.Sprintf("inventories/%d/hosts/", sid)), 200)
	var names []string
	for _, h := range r.results() {
		names = append(names, h["name"].(string))
	}
	sort.Strings(names)
	if fmt.Sprint(names) != "[web1 web3]" {
		t.Fatalf("smart hosts (deduped by name): %v", names)
	}
	// Hosts cannot be added to a smart inventory.
	must(t, a.post("hosts/", map[string]any{"name": uniq("h"), "inventory": sid}), 400)
	// Other expression forms.
	for filter, want := range map[string]int{
		`ansible_facts__ips[]="10.0.0.5"`:             2,
		`groups__name=web or name=db1`:                3,
		`not ansible_facts__os_family=RedHat and web`: 1,
		`(name=web1 or name=web2) and enabled=true`:   2,
	} {
		r := must(t, a.patch(fmt.Sprintf("inventories/%d/", sid), map[string]any{"host_filter": filter}), 200)
		if int(r.Body["total_hosts"].(float64)) != want {
			t.Errorf("filter %q: want %d got %v", filter, want, r.Body["total_hosts"])
		}
	}
	// Quoted values with SQL metacharacters are escaped, not executed.
	r = must(t, a.patch(fmt.Sprintf("inventories/%d/", sid), map[string]any{"host_filter": `name="x'); DROP TABLE hosts; --"`}), 200)
	if r.Body["total_hosts"].(float64) != 0 {
		t.Fatalf("injection filter matched hosts: %v", r.Body["total_hosts"])
	}
	must(t, a.get(fmt.Sprintf("hosts/%d/", f.hosts["web1"])), 200)
	must(t, a.patch(fmt.Sprintf("inventories/%d/", sid), map[string]any{"host_filter": `name=x'; DROP TABLE hosts; --`}), 400)
	script := must(t, a.get(fmt.Sprintf("inventories/%d/script/?hostvars=1", sid)), 200)
	if _, ok := script.Body["ungrouped"]; !ok {
		t.Fatalf("smart script: %s", script.Raw)
	}
	r = must(t, a.get(fmt.Sprintf("hosts/%d/smart_inventories/", f.hosts["web1"])), 200)
	_ = r
}

func TestInventoryAccess(t *testing.T) {
	a := adminClient(t)
	f := newInventory(t, a)
	u, uid := newUser(t, a)
	must(t, u.get(fmt.Sprintf("hosts/%d/", f.hosts["web1"])), 403)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Inventory Use"), "user": uid, "object_id": f.inv}), 201)
	must(t, u.get(fmt.Sprintf("hosts/%d/", f.hosts["web1"])), 200)
	if n := must(t, u.get(fmt.Sprintf("inventories/%d/hosts/", f.inv)), 200).count(); n != 5 {
		t.Fatalf("use role sees %d hosts", n)
	}
	must(t, u.post("hosts/", map[string]any{"name": uniq("h"), "inventory": f.inv}), 403)
	must(t, u.patch(fmt.Sprintf("groups/%d/", f.web), map[string]any{"description": "x"}), 403)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Inventory Admin"), "user": uid, "object_id": f.inv}), 201)
	must(t, u.post("hosts/", map[string]any{"name": uniq("h"), "inventory": f.inv}), 201)
	must(t, u.post(fmt.Sprintf("groups/%d/hosts/", f.db), map[string]any{"id": f.hosts["lonely"]}), 204)
	// Hosts cannot be moved between inventories.
	other := newInventory(t, a)
	must(t, a.patch(fmt.Sprintf("hosts/%d/", f.hosts["lonely"]), map[string]any{"inventory": other.inv}), 400)
}

func TestInventoryMaxHostsCopyAndVariableData(t *testing.T) {
	a := adminClient(t)
	f := newInventory(t, a)
	must(t, a.patch(fmt.Sprintf("organizations/%d/", f.org), map[string]any{"max_hosts": 5}), 200)
	r := must(t, a.post("hosts/", map[string]any{"name": uniq("h"), "inventory": f.inv}), 400)
	if !strings.Contains(r.Raw, "maximum number of 5 hosts") {
		t.Fatalf("max_hosts: %s", r.Raw)
	}
	must(t, a.patch(fmt.Sprintf("organizations/%d/", f.org), map[string]any{"max_hosts": 0}), 200)

	cp := must(t, a.post(fmt.Sprintf("inventories/%d/copy/", f.inv), map[string]any{"name": uniq("copy")}), 201)
	if cp.Body["total_hosts"].(float64) != 5 || cp.Body["total_groups"].(float64) != 3 {
		t.Fatalf("copy counts: %v %v", cp.Body["total_hosts"], cp.Body["total_groups"])
	}
	script := must(t, a.get(fmt.Sprintf("inventories/%d/script/", cp.id())), 200)
	if fmt.Sprint(script.Body["web"].(map[string]any)["hosts"]) != "[web1 web2]" {
		t.Fatalf("copied memberships: %v", script.Body["web"])
	}

	vd := must(t, a.get(fmt.Sprintf("groups/%d/variable_data/", f.web)), 200)
	if vd.Body["http_port"].(float64) != 8080 {
		t.Fatalf("variable_data: %s", vd.Raw)
	}
	vd = must(t, a.patch(fmt.Sprintf("groups/%d/variable_data/", f.web), map[string]any{"max_conn": 100}), 200)
	if vd.Body["http_port"].(float64) != 8080 || vd.Body["max_conn"].(float64) != 100 {
		t.Fatalf("variable_data patch: %s", vd.Raw)
	}
	vd = must(t, a.do("PUT", fmt.Sprintf("groups/%d/variable_data/", f.web), map[string]any{"only": true}), 200)
	if _, ok := vd.Body["http_port"]; ok {
		t.Fatalf("variable_data put should replace: %s", vd.Raw)
	}
}

func TestInventoryCopyKeepsInstanceGroups(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	ig := must(t, a.post("instance_groups/", map[string]any{"name": uniq("ig")}), 201).id()
	must(t, a.post(fmt.Sprintf("inventories/%d/instance_groups/", inv), map[string]any{"id": ig}), 204)
	cp := must(t, a.post(fmt.Sprintf("inventories/%d/copy/", inv), map[string]any{"name": uniq("copy")}), 201).id()
	if got := a.get(fmt.Sprintf("inventories/%d/instance_groups/", cp)).results(); len(got) != 1 || got[0]["id"] != float64(ig) {
		t.Fatalf("instance groups of the copy: %v", got)
	}
}

// /api/ no longer advertises /api/o/: awxnext has no OAuth2 applications.
func TestAPIRootHasNoOAuth2(t *testing.T) {
	root := must(t, (&client{t: t}).get("/api/"), 200)
	if _, ok := root.Body["oauth2"]; ok {
		t.Fatalf("API root: %s", root.Raw)
	}
}

// Deleting a group deletes what AWX's Group.delete_recursive deletes: its
// hosts left in no group, and each child left without a parent, with its
// hosts left in no group. What also belongs elsewhere stays.
func TestGroupDeleteRecursive(t *testing.T) {
	a := adminClient(t)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": newOrg(t, a)}), 201).id()
	group := func(name string) int64 {
		return must(t, a.post(fmt.Sprintf("inventories/%d/groups/", inv), map[string]any{"name": name}), 201).id()
	}
	host := func(name string, groups ...int64) int64 {
		id := must(t, a.post("hosts/", map[string]any{"name": name, "inventory": inv}), 201).id()
		for _, g := range groups {
			must(t, a.post(fmt.Sprintf("groups/%d/hosts/", g), map[string]any{"id": id}), 204)
		}
		return id
	}
	child := func(parent, ch int64) {
		must(t, a.post(fmt.Sprintf("groups/%d/children/", parent), map[string]any{"id": ch}), 204)
	}
	g, only, shared, grand, other := group("g"), group("only"), group("shared"), group("grand"), group("other")
	child(g, only)
	child(g, shared)
	child(other, shared)
	child(only, grand)
	gone := map[string]int64{"in g": host("in-g", g), "in only": host("in-only", only), "in grand": host("in-grand", grand)}
	stay := map[string]int64{"in g and other": host("in-g-other", g, other), "in only and other": host("in-only-other", only, other),
		"in shared": host("in-shared", shared), "in no group": host("ungrouped")}

	must(t, a.delete(fmt.Sprintf("groups/%d/", g)), 204)
	for name, id := range map[string]int64{"g": g, "only child": only, "its child": grand} {
		if r := a.get(fmt.Sprintf("groups/%d/", id)); r.Status != 404 {
			t.Errorf("group %s should be deleted: %d", name, r.Status)
		}
	}
	must(t, a.get(fmt.Sprintf("groups/%d/", shared)), 200)
	must(t, a.get(fmt.Sprintf("groups/%d/", other)), 200)
	for name, id := range gone {
		if r := a.get(fmt.Sprintf("hosts/%d/", id)); r.Status != 404 {
			t.Errorf("host %s should be deleted: %d", name, r.Status)
		}
	}
	for name, id := range stay {
		if r := a.get(fmt.Sprintf("hosts/%d/", id)); r.Status != 200 {
			t.Errorf("host %s should stay: %d", name, r.Status)
		}
	}
	if n := must(t, a.get(fmt.Sprintf("groups/%d/children/?id=%d", other, shared)), 200).count(); n != 1 {
		t.Error("the shared child lost its other parent")
	}
}
