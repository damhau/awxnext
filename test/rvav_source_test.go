package test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// rvavInventory is what rvav serves at /api/v1/ansible/inventory: the format
// of ansible-inventory --list, a group per project, environment and OS
// family, and rvav's context in host variables.
const rvavInventory = `{
  "_meta": {"hostvars": {
    "web-01": {"ansible_host": "10.0.0.11", "rvav_project": "Web", "rvav_power_state": "poweredOn"},
    "db-01": {"ansible_host": "10.0.0.12", "rvav_project": "Web", "rvav_power_state": "poweredOff"}
  }},
  "all": {"children": ["env_dev", "os_linux", "project_web", "rvav"]},
  "rvav": {"hosts": ["db-01", "web-01"]},
  "project_web": {"hosts": ["db-01", "web-01"]},
  "env_dev": {"hosts": ["web-01"]},
  "os_linux": {"hosts": ["db-01", "web-01"]}
}`

// startFakeRvav serves rvavInventory to the bearer token "good", and 401 to
// any other, counting the requests.
func startFakeRvav(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/ansible/inventory" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			jsonReply(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(rvavInventory))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// An rvav source is read by awxnext itself, with no execution environment:
// it takes an rvav credential and no plugin options or limit, says what to
// do when it has no credential or rvav refuses the token, refuses awxnext's
// own addresses unless allowed, and imports rvav's hosts and groups.
func TestRvavSource(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("rvav"), "organization": org}), 201).id()
	rvav, requests := startFakeRvav(t)

	// The source type and its credential type.
	if opts := must(t, a.do("OPTIONS", "inventory_sources/", nil), 200); !strings.Contains(opts.Raw, `["rvav","rvav"]`) {
		t.Fatalf("source choices: %s", opts.Raw)
	}
	ct := credType(t, a, "rvav")
	typ := must(t, a.get(fmt.Sprintf("credential_types/%d/", ct)), 200).Body
	if typ["managed"] != true || typ["kind"] != "cloud" || fmt.Sprint(typ["inputs"].(map[string]any)["required"]) != "[url token]" {
		t.Fatalf("rvav credential type: %v", typ)
	}
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("rvav"), "organization": org, "credential_type": ct,
		"inputs": map[string]any{"url": rvav.URL + "/", "token": "good"}}), 201).id()
	ssh := must(t, a.post("credentials/", map[string]any{"name": uniq("ssh"), "organization": org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "u"}}), 201).id()

	// What it takes.
	source := func(extra map[string]any) map[string]any {
		m := map[string]any{"name": uniq("rvav"), "inventory": inv, "source": "rvav"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for field, extra := range map[string]map[string]any{
		"credential":  {"credential": ssh},
		"source_vars": {"credential": cred, "source_vars": "url: https://elsewhere"},
		"limit":       {"credential": cred, "limit": "web-*"},
	} {
		if r := must(t, a.post("inventory_sources/", source(extra)), 400); r.Body[field] == nil {
			t.Fatalf("%s: %s", field, r.Raw)
		}
	}
	src := must(t, a.post("inventory_sources/", source(map[string]any{"source_vars": "---\n", "overwrite": true})), 201).id()
	sync := func() resp {
		t.Helper()
		r := must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202)
		return waitJob(t, a, fmt.Sprintf("inventory_updates/%d/", int64(r.Body["inventory_update"].(float64))))
	}
	failsWith := func(what, want string) {
		t.Helper()
		if u := sync(); u.Body["status"] != "failed" || !strings.Contains(fmt.Sprint(u.Body["job_explanation"]), want) {
			t.Fatalf("%s: %v, %v", what, u.Body["status"], u.Body["job_explanation"])
		}
	}

	// No credential yet: rvav sets one when it makes the token.
	failsWith("no credential", "Inventory token")
	must(t, a.patch(fmt.Sprintf("inventory_sources/%d/", src), map[string]any{"credential": cred}), 200)

	// awxnext's own addresses, the fake's among them, are refused unless allowed.
	setJobs(t, a, "INVENTORY_SOURCE_ALLOWED_LOCAL_ADDRESSES", []any{})
	failsWith("loopback", "INVENTORY_SOURCE_ALLOWED_LOCAL_ADDRESSES")
	if n := requests.Load(); n != 0 {
		t.Fatalf("the refused source sent %d requests", n)
	}
	setJobs(t, a, "INVENTORY_SOURCE_ALLOWED_LOCAL_ADDRESSES", []any{"127.0.0.1", "::1"})

	// rvav's hosts and groups.
	u := sync()
	if u.Body["status"] != "successful" {
		t.Fatalf("sync: %v %v\n%s", u.Body["status"], u.Body["job_explanation"], a.get(fmt.Sprintf("inventory_updates/%d/stdout/?format=txt", u.id())).Raw)
	}
	if out := a.get(fmt.Sprintf("inventory_updates/%d/stdout/?format=txt", u.id())).Raw; !strings.Contains(out, "Reading the inventory from "+rvav.URL+"/api/v1/ansible/inventory") || !strings.Contains(out, "2 hosts") {
		t.Fatalf("stdout: %s", out)
	}
	if got := names(a.get(fmt.Sprintf("inventories/%d/hosts/", inv))); fmt.Sprint(got) != "[db-01 web-01]" {
		t.Fatalf("hosts: %v", got)
	}
	if got := names(a.get(fmt.Sprintf("inventories/%d/groups/", inv))); fmt.Sprint(got) != "[env_dev os_linux project_web rvav]" {
		t.Fatalf("groups: %v", got)
	}
	hosts := a.get(fmt.Sprintf("inventories/%d/hosts/?name=web-01", inv)).results()
	if len(hosts) != 1 || !strings.Contains(fmt.Sprint(hosts[0]["variables"]), "10.0.0.11") {
		t.Fatalf("web-01: %v", hosts)
	}
	envDev := a.get(fmt.Sprintf("inventories/%d/groups/?name=env_dev", inv)).results()
	if len(envDev) != 1 || fmt.Sprint(names(a.get(fmt.Sprintf("groups/%d/hosts/", int64(envDev[0]["id"].(float64)))))) != "[web-01]" {
		t.Fatalf("env_dev: %v", envDev)
	}

	// A token rvav no longer takes.
	must(t, a.patch(fmt.Sprintf("credentials/%d/", cred), map[string]any{"inputs": map[string]any{"url": rvav.URL, "token": "revoked"}}), 200)
	failsWith("revoked token", "rvav refused the token (HTTP 401)")
}
