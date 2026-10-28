package test

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

const scmInventory = `all:
  vars:
    site: lab
  children:
    web:
      vars:
        http_port: 8080
      hosts:
        web1: {state: up}
        web2: {state: down}
    db:
      hosts:
        db1: {}
      children:
        db_replicas:
          hosts:
            db2: {}
  hosts:
    loose: {}
`

const scmInventoryV2 = `all:
  children:
    web:
      vars:
        https_port: 8443
      hosts:
        web1: {state: up}
        web3: {state: up}
`

func names(r resp) []string {
	var out []string
	for _, o := range r.results() {
		out = append(out, o["name"].(string))
	}
	sort.Strings(out)
	return out
}

func waitUpdate(t *testing.T, a *client, r resp) resp {
	t.Helper()
	id, _ := r.Body["inventory_update"].(float64)
	if id == 0 {
		t.Fatalf("no inventory update: %v", r.Body)
	}
	u := waitJob(t, a, fmt.Sprintf("inventory_updates/%d/", int64(id)))
	if u.Body["status"] != "successful" {
		t.Fatalf("inventory update %v: %v\n%s", u.Body["status"], u.Body["job_explanation"],
			a.get(fmt.Sprintf("inventory_updates/%d/stdout/?format=txt", int64(id))).Raw)
	}
	return u
}

func TestInventorySourceValidation(t *testing.T) {
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	smart := must(t, a.post("inventories/", map[string]any{"name": uniq("smart"), "organization": org, "kind": "smart", "host_filter": "name=x"}), 201).id()
	repo := gitRepo(t, map[string]string{"hosts.yml": scmInventory})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": repo}), 201).id()

	must(t, a.post("inventory_sources/", map[string]any{"name": "s", "inventory": inv, "source": "scm"}), 400)
	must(t, a.post("inventory_sources/", map[string]any{"name": "s", "inventory": inv, "source": "ec2", "source_project": proj}), 400)
	must(t, a.post("inventory_sources/", map[string]any{"name": "s", "inventory": smart, "source": "scm", "source_project": proj}), 400)
	must(t, a.post("inventory_sources/", map[string]any{"name": "s", "inventory": inv, "source": "constructed"}), 400)
	must(t, a.post("inventory_sources/", map[string]any{"name": "s", "inventory": inv, "source": "scm", "source_project": proj, "source_path": "../etc"}), 400)
	must(t, a.post("inventory_sources/", map[string]any{"name": "s", "inventory": inv, "source": "scm", "source_project": proj, "scm_branch": "dev"}), 400)
	must(t, a.post("inventory_sources/", map[string]any{"name": "s", "inventory": inv, "source": "scm", "source_project": proj, "host_filter": "("}), 400)

	// A cloud source requires a matching cloud credential.
	ssh := must(t, a.post("credentials/", map[string]any{"name": uniq("ssh"), "organization": org, "credential_type": credType(t, a, "ssh"), "inputs": map[string]any{"username": "u"}}), 201).id()
	aws := must(t, a.post("credentials/", map[string]any{"name": uniq("aws"), "organization": org, "credential_type": credType(t, a, "aws"),
		"inputs": map[string]any{"username": "AKIA", "password": "secret"}}), 201).id()
	must(t, a.post("inventory_sources/", map[string]any{"name": "ec2", "inventory": inv, "source": "ec2", "credential": ssh}), 400)
	ec2 := must(t, a.post("inventory_sources/", map[string]any{"name": "ec2", "inventory": inv, "source": "ec2", "credential": aws,
		"source_vars": "regions: [eu-west-1]"}), 201)
	if ec2.Body["status"] != "never updated" || ec2.Body["type"] != "inventory_source" {
		t.Fatalf("source: %v", ec2.Body)
	}
	// Names are unique per inventory.
	must(t, a.post("inventory_sources/", map[string]any{"name": "ec2", "inventory": inv, "source": "ec2", "credential": aws}), 400)
	must(t, a.get(fmt.Sprintf("inventories/%d/inventory_sources/", inv)), 200)
	if r := a.get(fmt.Sprintf("inventories/%d/", inv)); r.Body["total_inventory_sources"] != float64(1) || r.Body["has_inventory_sources"] != true {
		t.Fatalf("inventory summary: %v", r.Body)
	}
	if r := must(t, a.get(fmt.Sprintf("inventory_sources/%d/update/", ec2.id())), 200); r.Body["can_update"] != true {
		t.Fatalf("can_update: %v", r.Body)
	}

	// Inventory admins manage sources; users with read access cannot.
	user, uid := newUser(t, a)
	must(t, user.get(fmt.Sprintf("inventory_sources/%d/", ec2.id())), 403)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "inventories", inv, "read_role")}), 204)
	must(t, user.get(fmt.Sprintf("inventory_sources/%d/", ec2.id())), 200)
	must(t, user.patch(fmt.Sprintf("inventory_sources/%d/", ec2.id()), map[string]any{"description": "x"}), 403)
	must(t, user.post(fmt.Sprintf("inventory_sources/%d/update/", ec2.id()), nil), 403)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "inventories", inv, "update_role")}), 204)
	if r := must(t, user.get(fmt.Sprintf("inventory_sources/%d/update/", ec2.id())), 200); r.Body["can_update"] != true {
		t.Fatalf("can_update after update_role: %v", r.Body)
	}
}

func objectRole(t *testing.T, a *client, res string, id int64, field string) int64 {
	t.Helper()
	r := must(t, a.get(fmt.Sprintf("%s/%d/", res, id)), 200)
	roles := r.Body["summary_fields"].(map[string]any)["object_roles"].(map[string]any)
	return int64(roles[field].(map[string]any)["id"].(float64))
}

func TestInventoryUpdateFromProject(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	repo := gitRepo(t, map[string]string{"inventories/lab.yml": scmInventory})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": repo,
		"scm_update_on_launch": true}), 201).id()
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org, "variables": "owner: ops"}), 201).id()
	// A manually created host is matched by name and kept.
	must(t, a.post("hosts/", map[string]any{"name": "web1", "inventory": inv, "variables": "rack: r1"}), 201)
	src := must(t, a.post("inventory_sources/", map[string]any{"name": "lab", "inventory": inv, "source": "scm", "source_project": proj,
		"source_path": "inventories/lab.yml", "overwrite": true, "enabled_var": "state", "enabled_value": "up", "verbosity": 0}), 201).id()

	u := waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
	if u.Body["scm_revision"] == "" || u.Body["source"] != "scm" || u.Body["inventory_source"] != float64(src) {
		t.Fatalf("update: %v", u.Body)
	}
	if got := names(a.get(fmt.Sprintf("inventories/%d/hosts/", inv))); strings.Join(got, ",") != "db1,db2,loose,web1,web2" {
		t.Fatalf("hosts: %v", got)
	}
	if got := names(a.get(fmt.Sprintf("inventories/%d/groups/", inv))); strings.Join(got, ",") != "db,db_replicas,web" {
		t.Fatalf("groups: %v", got)
	}
	web2 := a.get(fmt.Sprintf("inventories/%d/hosts/?name=web2", inv)).results()[0]
	if web2["enabled"] != false {
		t.Fatalf("web2 should be disabled by enabled_var: %v", web2)
	}
	web1 := a.get(fmt.Sprintf("inventories/%d/hosts/?name=web1", inv)).results()[0]
	if web1["enabled"] != true || !strings.Contains(web1["variables"].(string), "rack") || !strings.Contains(web1["variables"].(string), "state") {
		t.Fatalf("web1 vars should be merged: %v", web1)
	}
	invObj := a.get(fmt.Sprintf("inventories/%d/", inv))
	if v := invObj.Body["variables"].(string); !strings.Contains(v, "site") || !strings.Contains(v, "owner") {
		t.Fatalf("inventory vars: %v", v)
	}
	db := a.get(fmt.Sprintf("inventories/%d/groups/?name=db", inv)).results()[0]
	if got := names(a.get(fmt.Sprintf("groups/%d/children/", int64(db["id"].(float64))))); strings.Join(got, ",") != "db_replicas" {
		t.Fatalf("db children: %v", got)
	}
	if got := names(a.get(fmt.Sprintf("inventory_sources/%d/hosts/", src))); len(got) != 5 {
		t.Fatalf("source hosts: %v", got)
	}
	if r := a.get(fmt.Sprintf("hosts/%d/inventory_sources/", int64(web2["id"].(float64)))); r.count() != 1 {
		t.Fatalf("host sources: %v", r.Body)
	}
	if r := a.get(fmt.Sprintf("inventory_updates/%d/events/", u.id())); r.count() == 0 {
		t.Fatalf("no events")
	}
	if s := a.get(fmt.Sprintf("inventory_updates/%d/stdout/?format=txt", u.id())).Raw; !strings.Contains(s, "Loaded 3 groups, 5 hosts") {
		t.Fatalf("stdout: %s", s)
	}
	if r := a.get(fmt.Sprintf("inventory_sources/%d/", src)); r.Body["status"] != "successful" || r.Body["last_update_failed"] != false {
		t.Fatalf("source status: %v", r.Body)
	}

	// Second revision: removed hosts and groups disappear (overwrite), group vars are replaced only with overwrite_vars.
	commitFiles(t, repo, map[string]string{"inventories/lab.yml": scmInventoryV2}, "shrink")
	must(t, a.patch(fmt.Sprintf("inventory_sources/%d/", src), map[string]any{"overwrite_vars": true}), 200)
	waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
	if got := names(a.get(fmt.Sprintf("inventories/%d/hosts/", inv))); strings.Join(got, ",") != "web1,web3" {
		t.Fatalf("hosts after overwrite: %v", got)
	}
	if got := names(a.get(fmt.Sprintf("inventories/%d/groups/", inv))); strings.Join(got, ",") != "web" {
		t.Fatalf("groups after overwrite: %v", got)
	}
	web := a.get(fmt.Sprintf("inventories/%d/groups/?name=web", inv)).results()[0]
	if v := web["variables"].(string); strings.Contains(v, "http_port") || !strings.Contains(v, "https_port") {
		t.Fatalf("web vars should be replaced: %v", v)
	}
	web1 = a.get(fmt.Sprintf("inventories/%d/hosts/?name=web1", inv)).results()[0]
	if strings.Contains(web1["variables"].(string), "rack") {
		t.Fatalf("overwrite_vars should drop local host vars: %v", web1)
	}

	// update_inventory_sources on the inventory launches all sources.
	r := must(t, a.post(fmt.Sprintf("inventories/%d/update_inventory_sources/", inv), nil), 202)
	var started []map[string]any
	if err := json.Unmarshal([]byte(r.Raw), &started); err != nil || len(started) != 1 {
		t.Fatalf("update_inventory_sources: %s", r.Raw)
	}
	waitUpdate(t, a, resp{Body: started[0]})
}

func TestInventoryUpdateHostFilterAndNoOverwrite(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	repo := gitRepo(t, map[string]string{"hosts.yml": scmInventory})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": repo}), 201).id()
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": "lab", "inventory": inv, "source": "scm", "source_project": proj,
		"source_path": "hosts.yml", "host_filter": "^web"}), 201).id()
	waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
	if got := names(a.get(fmt.Sprintf("inventories/%d/hosts/", inv))); strings.Join(got, ",") != "web1,web2" {
		t.Fatalf("filtered hosts: %v", got)
	}
	// Without overwrite, hosts that leave the source stay.
	must(t, a.patch(fmt.Sprintf("inventory_sources/%d/", src), map[string]any{"host_filter": "^web1$"}), 200)
	waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
	if got := names(a.get(fmt.Sprintf("inventories/%d/hosts/", inv))); strings.Join(got, ",") != "web1,web2" {
		t.Fatalf("hosts without overwrite: %v", got)
	}
}

func TestConstructedInventory(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	org := newOrg(t, a)
	in1 := must(t, a.post("inventories/", map[string]any{"name": uniq("in1"), "organization": org}), 201).id()
	in2 := must(t, a.post("inventories/", map[string]any{"name": uniq("in2"), "organization": org}), 201).id()
	must(t, a.post("hosts/", map[string]any{"name": "app1", "inventory": in1, "variables": "role: web\ntier: 1"}), 201)
	must(t, a.post("hosts/", map[string]any{"name": "app2", "inventory": in1, "variables": "role: db"}), 201)
	must(t, a.post("hosts/", map[string]any{"name": "app3", "inventory": in2, "variables": "role: web"}), 201)

	ci := must(t, a.post("constructed_inventories/", map[string]any{"name": uniq("ci"), "organization": org,
		"source_vars": "plugin: constructed\nstrict: true\ngroups:\n  webservers: role == 'web'\ncompose:\n  is_web: role == 'web'\n",
		"limit":       "webservers"}), 201)
	cid := ci.id()
	if ci.Body["kind"] != "constructed" || ci.Body["limit"] != "webservers" {
		t.Fatalf("constructed inventory: %v", ci.Body)
	}
	for _, in := range []int64{in1, in2} {
		must(t, a.post(fmt.Sprintf("constructed_inventories/%d/input_inventories/", cid), map[string]any{"id": in}), 204)
	}
	// Hosts cannot be added by hand.
	must(t, a.post("hosts/", map[string]any{"name": "manual", "inventory": cid}), 400)
	srcs := a.get(fmt.Sprintf("inventories/%d/inventory_sources/", cid)).results()
	if len(srcs) != 1 || srcs[0]["source"] != "constructed" {
		t.Fatalf("auto-created source: %v", srcs)
	}
	waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", int64(srcs[0]["id"].(float64))), nil), 202))
	if got := names(a.get(fmt.Sprintf("inventories/%d/hosts/", cid))); strings.Join(got, ",") != "app1,app3" {
		t.Fatalf("constructed hosts: %v", got)
	}
	g := a.get(fmt.Sprintf("inventories/%d/groups/?name=webservers", cid))
	if g.count() != 1 {
		t.Fatalf("webservers group missing: %v", names(a.get(fmt.Sprintf("inventories/%d/groups/", cid))))
	}
	if got := names(a.get(fmt.Sprintf("groups/%d/hosts/", int64(g.results()[0]["id"].(float64))))); strings.Join(got, ",") != "app1,app3" {
		t.Fatalf("webservers hosts: %v", got)
	}
	h := a.get(fmt.Sprintf("inventories/%d/hosts/?name=app1", cid)).results()[0]
	if v := h["variables"].(string); !strings.Contains(v, "is_web") || !strings.Contains(v, "tier") {
		t.Fatalf("constructed host vars: %v", v)
	}
	// Changing the limit through the constructed inventory updates its source.
	must(t, a.patch(fmt.Sprintf("constructed_inventories/%d/", cid), map[string]any{"limit": "app1"}), 200)
	if r := a.get(fmt.Sprintf("inventory_sources/%d/", int64(srcs[0]["id"].(float64)))); r.Body["limit"] != "app1" {
		t.Fatalf("source limit: %v", r.Body["limit"])
	}
}

func TestJobUpdatesInventoryOnLaunch(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	repo := gitRepo(t, map[string]string{"hosts.yml": "all:\n  vars:\n    ansible_connection: local\n    ansible_python_interpreter: '{{ ansible_playbook_python }}'\n  hosts:\n    dyn1: {}\n    dyn2: {}\n"})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("invproj"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("dyn"), "organization": f.org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": "dyn", "inventory": inv, "source": "scm", "source_project": proj,
		"source_path": "hosts.yml", "update_on_launch": true, "update_cache_timeout": 300}), 201).id()
	jt := f.template(t, "site.yml", map[string]any{"inventory": inv})
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	out := f.stdout(t, job.id())
	if !strings.Contains(out, "from dyn1") || !strings.Contains(out, "from dyn2") {
		t.Fatalf("job did not see synced hosts:\n%s", out)
	}
	if r := a.get(fmt.Sprintf("inventory_sources/%d/inventory_updates/", src)); r.count() != 1 || r.results()[0]["launch_type"] != "dependency" {
		t.Fatalf("dependency updates: %v", r.Body)
	}
	// Within the cache timeout no new update runs.
	mustStatus(t, f, f.launch(t, a, jt, nil), "successful")
	if r := a.get(fmt.Sprintf("inventory_sources/%d/inventory_updates/", src)); r.count() != 1 {
		t.Fatalf("cache timeout ignored: %d updates", r.count())
	}
}

// An inventory sync cannot take the organization over its max_hosts.
func TestInventorySyncRespectsMaxHosts(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	allowFileURLs(t, a)
	org := must(t, a.post("organizations/", map[string]any{"name": uniq("org"), "max_hosts": 3}), 201).id()
	repo := gitRepo(t, map[string]string{"inventories/lab.yml": scmInventory})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": repo,
		"scm_update_on_launch": true}), 201).id()
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": "lab", "inventory": inv, "source": "scm", "source_project": proj,
		"source_path": "inventories/lab.yml", "overwrite": true}), 201).id()

	started := must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202)
	u := waitJob(t, a, fmt.Sprintf("inventory_updates/%d/", int64(started.Body["inventory_update"].(float64))))
	if u.Body["status"] != "failed" || !strings.Contains(fmt.Sprint(u.Body["job_explanation"]), "max_hosts") {
		t.Fatalf("a sync of 5 hosts into an organization allowing 3: %v %v", u.Body["status"], u.Body["job_explanation"])
	}
	if n := a.get(fmt.Sprintf("inventories/%d/hosts/", inv)).count(); n != 0 {
		t.Fatalf("a refused sync imported %d hosts", n)
	}
	must(t, a.patch(fmt.Sprintf("organizations/%d/", org), map[string]any{"max_hosts": 5}), 200)
	waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
}
