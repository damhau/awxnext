package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fakes of the ovirt and insights inventory plugins: each calls a fake API
// with what the source's credential injects, as the real plugins do.
const fakeOvirtPlugin = `from ansible.plugins.inventory import BaseInventoryPlugin
import base64, configparser, json, os, urllib.request

DOCUMENTATION = r"""
name: ovirt
plugin_type: inventory
short_description: fake ovirt inventory for awxnext tests
options:
  plugin:
    description: the plugin
    required: true
"""


class InventoryModule(BaseInventoryPlugin):
    NAME = "ovirt.ovirt.ovirt"

    def verify_file(self, path):
        return path.endswith(("ovirt.yml", "ovirt.yaml"))

    def parse(self, inventory, loader, path, cache=True):
        super().parse(inventory, loader, path, cache)
        self._read_config_data(path)
        # The INI file and the environment, as the real plugin and modules read them.
        ini = configparser.ConfigParser()
        ini.read(os.environ["OVIRT_INI_PATH"])
        o = ini["ovirt"]
        if o["ovirt_url"] != os.environ["OVIRT_URL"] or o["ovirt_username"] != os.environ["OVIRT_USERNAME"]:
            raise Exception("the INI file and the environment differ")
        req = urllib.request.Request(o["ovirt_url"] + "/vms")
        auth = base64.b64encode((o["ovirt_username"] + ":" + o["ovirt_password"]).encode()).decode()
        req.add_header("Authorization", "Basic " + auth)
        inventory.add_group("ovirt")
        for vm in json.load(urllib.request.urlopen(req))["vms"]:
            inventory.add_host(vm["name"], group="ovirt")
            inventory.set_variable(vm["name"], "ansible_host", vm["ip"])
            inventory.set_variable(vm["name"], "ovirt_ca_file", o.get("ovirt_ca_file", ""))
`

const fakeInsightsPlugin = `from ansible.plugins.inventory import BaseInventoryPlugin
import base64, json, os, urllib.request

DOCUMENTATION = r"""
name: insights
plugin_type: inventory
short_description: fake insights inventory for awxnext tests
options:
  plugin:
    description: the plugin
    required: true
  server:
    description: the API
    required: true
"""


class InventoryModule(BaseInventoryPlugin):
    NAME = "redhatinsights.insights.insights"

    def verify_file(self, path):
        return path.endswith(("insights.yml", "insights.yaml"))

    def parse(self, inventory, loader, path, cache=True):
        super().parse(inventory, loader, path, cache)
        self._read_config_data(path)
        req = urllib.request.Request(self.get_option("server") + "/hosts")
        auth = base64.b64encode((os.environ["INSIGHTS_USER"] + ":" + os.environ["INSIGHTS_PASSWORD"]).encode()).decode()
        req.add_header("Authorization", "Basic " + auth)
        inventory.add_group("insights")
        for h in json.load(urllib.request.urlopen(req))["results"]:
            inventory.add_host(h["fqdn"], group="insights")
            inventory.set_variable(h["fqdn"], "insights_id", h["id"])
`

// fakeCollections writes the fake plugins as collections and points jobs and
// updates at them (AWX_TASK_ENV).
func fakeCollections(t *testing.T, a *client) {
	t.Helper()
	root := t.TempDir()
	for _, p := range []struct{ ns, coll, file, src string }{
		{"ovirt", "ovirt", "ovirt.py", fakeOvirtPlugin},
		{"redhatinsights", "insights", "insights.py", fakeInsightsPlugin},
	} {
		dir := filepath.Join(root, "ansible_collections", p.ns, p.coll, "plugins", "inventory")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, p.file), []byte(p.src), 0o644)
		os.WriteFile(filepath.Join(root, "ansible_collections", p.ns, p.coll, "galaxy.yml"),
			[]byte(fmt.Sprintf("namespace: %s\nname: %s\nversion: 1.0.0\n", p.ns, p.coll)), 0o644)
	}
	must(t, a.patch("settings/jobs/", map[string]any{"AWX_TASK_ENV": map[string]any{"ANSIBLE_COLLECTIONS_PATH": root}}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"AWX_TASK_ENV": map[string]any{}}) })
}

// fakeAPI answers path with body when called with user:pass in basic auth.
func fakeAPI(t *testing.T, user, pass, path string, body any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != path {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The Insights and Red Hat Virtualization sources sync with their
// credentials, whose types are AWX's.
func TestInsightsAndRHVSources(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	org := newOrg(t, a)
	fakeCollections(t, a)

	types := map[string]map[string]any{}
	for _, ns := range []string{"insights", "rhv"} {
		r := a.get("credential_types/?namespace=" + ns).results()
		if len(r) != 1 || r[0]["managed"] != true {
			t.Fatalf("%s credential type: %v", ns, r)
		}
		types[ns] = r[0]
	}
	if types["insights"]["name"] != "Insights" || types["insights"]["kind"] != "insights" ||
		types["rhv"]["name"] != "Red Hat Virtualization" || types["rhv"]["kind"] != "cloud" {
		t.Fatalf("types: %v", types)
	}

	sync := func(source string, cred int64, vars string) []map[string]any {
		t.Helper()
		inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
		src := must(t, a.post("inventory_sources/", map[string]any{"name": uniq(source), "inventory": inv, "source": source,
			"credential": cred, "source_vars": vars, "overwrite": true, "verbosity": 2}), 201).id()
		u := waitUpdate(t, a, must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202))
		if u.Body["status"] != "successful" {
			out := a.get(fmt.Sprintf("inventory_updates/%v/stdout/?format=txt", u.Body["id"])).Raw
			t.Fatalf("%s update %v: %v\n%s", source, u.Body["status"], u.Body["job_explanation"], out)
		}
		return a.get(fmt.Sprintf("inventories/%d/hosts/?order_by=name", inv)).results()
	}

	// Red Hat Virtualization: the plugin's INI file and the environment.
	url := fakeAPI(t, "admin@internal", "rhv-secret", "/vms", map[string]any{"vms": []any{
		map[string]any{"name": "vm1", "ip": "10.0.0.1"}, map[string]any{"name": "vm2", "ip": "10.0.0.2"}}})
	rhv := must(t, a.post("credentials/", map[string]any{"name": uniq("rhv"), "organization": org, "credential_type": types["rhv"]["id"],
		"inputs": map[string]any{"host": url, "username": "admin@internal", "password": "rhv-secret", "ca_file": "/etc/pki/ovirt.pem"}}), 201).id()
	hosts := sync("rhv", rhv, "")
	if len(hosts) != 2 || hosts[0]["name"] != "vm1" || !strings.Contains(fmt.Sprint(hosts[0]["variables"]), "10.0.0.1") ||
		!strings.Contains(fmt.Sprint(hosts[0]["variables"]), "/etc/pki/ovirt.pem") {
		t.Fatalf("rhv hosts: %v", hosts)
	}

	// Insights: its environment.
	server := fakeAPI(t, "insights-user", "insights-secret", "/hosts", map[string]any{"results": []any{
		map[string]any{"fqdn": "web.example.com", "id": "abc-123"}}})
	ins := must(t, a.post("credentials/", map[string]any{"name": uniq("insights"), "organization": org, "credential_type": types["insights"]["id"],
		"inputs": map[string]any{"username": "insights-user", "password": "insights-secret"}}), 201).id()
	hosts = sync("insights", ins, "server: "+server)
	if len(hosts) != 1 || hosts[0]["name"] != "web.example.com" || !strings.Contains(fmt.Sprint(hosts[0]["variables"]), "abc-123") {
		t.Fatalf("insights hosts: %v", hosts)
	}

	// Each source takes its own credential type.
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	r := must(t, a.post("inventory_sources/", map[string]any{"name": uniq("rhv"), "inventory": inv, "source": "rhv", "credential": ins}), 400)
	if !strings.Contains(r.Raw, "require credentials for the matching cloud service") {
		t.Fatalf("insights credential on a rhv source: %s", r.Raw)
	}
}
