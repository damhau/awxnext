package test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/runners/ansible"
)

// Two versions of the same inventory: v2 drops web2, adds app1 and changes web1's role.
var importFiles = map[string][2]string{
	"hosts.ini": {
		"[web]\nweb1 role=web\nweb2\n\n[db]\ndb1\n\n[all:vars]\nsite=dc1\n",
		"[web]\nweb1 role=api\napp1\n\n[db]\ndb1\n\n[all:vars]\nsite=dc1\n",
	},
	"hosts.yml": {
		"all:\n  vars:\n    site: dc1\n  children:\n    web:\n      hosts:\n        web1: {role: web}\n        web2:\n    db:\n      hosts:\n        db1:\n",
		"all:\n  vars:\n    site: dc1\n  children:\n    web:\n      hosts:\n        web1: {role: api}\n        app1:\n    db:\n      hosts:\n        db1:\n",
	},
}

// awxnext inventory-import imports INI and YAML inventories, merging or
// overwriting as awx-manage inventory_import does, and records the runs as
// a file source with its updates.
func TestInventoryImportCommand(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	org := newOrg(t, a)
	ctx := context.Background()
	names := func(path string) string {
		t.Helper()
		var out []string
		for _, o := range a.get(path).results() {
			out = append(out, o["name"].(string))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	for _, file := range []string{"hosts.ini", "hosts.yml"} {
		for _, overwrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s overwrite=%v", file, overwrite), func(t *testing.T) {
				invName := uniq("imported")
				inv := must(t, a.post("inventories/", map[string]any{"name": invName, "organization": org}), 201).id()
				path := filepath.Join(t.TempDir(), file)
				run := func(content string) int64 {
					t.Helper()
					os.WriteFile(path, []byte(content), 0o644)
					var log strings.Builder
					id, err := ansible.ImportFile(ctx, app, ansible.FileImport{InventoryName: invName, Source: path, Overwrite: overwrite,
						Args: []string{"awxnext", "inventory-import", "--inventory-name", invName, "--source", path}}, &log)
					if err != nil {
						t.Fatalf("import: %v\n%s", err, log.String())
					}
					if strings.Contains(log.String(), "Could not store") {
						t.Fatalf("output: %s", log.String())
					}
					return id
				}

				first := run(importFiles[file][0])
				if got := names(fmt.Sprintf("inventories/%d/hosts/", inv)); got != "db1,web1,web2" {
					t.Fatalf("hosts: %s", got)
				}
				if got := names(fmt.Sprintf("inventories/%d/groups/", inv)); got != "db,web" {
					t.Fatalf("groups: %s", got)
				}
				if v := a.get(fmt.Sprintf("inventories/%d/variable_data/", inv)).Body; v["site"] != "dc1" {
					t.Fatalf("inventory variables: %v", v)
				}
				must(t, a.post("hosts/", map[string]any{"name": "manual1", "inventory": inv}), 201)

				second := run(importFiles[file][1])
				want := "app1,db1,manual1,web1,web2"
				wantWeb := "app1,web1,web2"
				if overwrite {
					// Hosts the source no longer lists go; hosts added by hand stay.
					want, wantWeb = "app1,db1,manual1,web1", "app1,web1"
				}
				if got := names(fmt.Sprintf("inventories/%d/hosts/", inv)); got != want {
					t.Fatalf("hosts after the second import: %s, want %s", got, want)
				}
				web := a.get(fmt.Sprintf("inventories/%d/groups/?name=web", inv)).results()[0]
				if got := names(fmt.Sprintf("groups/%v/hosts/", web["id"])); got != wantWeb {
					t.Fatalf("web hosts: %s, want %s", got, wantWeb)
				}
				web1 := a.get(fmt.Sprintf("inventories/%d/hosts/?name=web1", inv)).results()[0]
				if !strings.Contains(fmt.Sprint(web1["variables"]), "api") {
					t.Fatalf("web1 variables: %v", web1["variables"])
				}

				// One file source per inventory, path and options, with an update per run.
				srcs := a.get(fmt.Sprintf("inventories/%d/inventory_sources/", inv)).results()
				if len(srcs) != 1 || srcs[0]["source"] != "file" || srcs[0]["source_path"] != path || srcs[0]["overwrite"] != overwrite {
					t.Fatalf("sources: %v", srcs)
				}
				ups := a.get(fmt.Sprintf("inventory_sources/%v/inventory_updates/?order_by=id", srcs[0]["id"])).results()
				if len(ups) != 2 || ups[0]["id"] != float64(first) || ups[1]["id"] != float64(second) {
					t.Fatalf("updates: %v", ups)
				}
				u := a.get(fmt.Sprintf("inventory_updates/%d/", second)).Body
				if u["status"] != "successful" || u["launch_type"] != "manual" || !strings.Contains(fmt.Sprint(u["job_args"]), "--source") {
					t.Fatalf("update: %v", u)
				}
				if out := a.get(fmt.Sprintf("inventory_updates/%d/stdout/?format=txt", second)).Raw; !strings.Contains(out, "Loaded 2 groups, 3 hosts") {
					t.Fatalf("update output:\n%s", out)
				}
			})
		}
	}
}

func TestInventoryImportCommandChecks(t *testing.T) {
	requireAnsible(t)
	a := adminClient(t)
	org := newOrg(t, a)
	ctx := context.Background()
	invName := uniq("imported")
	inv := must(t, a.post("inventories/", map[string]any{"name": invName, "organization": org}), 201).id()
	path := filepath.Join(t.TempDir(), "hosts.ini")
	os.WriteFile(path, []byte("[web]\nweb1\nweb2\n[db]\ndb1\n[empty]\n"), 0o644)

	for o, want := range map[*ansible.FileImport]string{
		{InventoryName: invName, InventoryID: inv, Source: path}: "--inventory-name and --inventory-id are mutually exclusive",
		{Source: path}:                                    "--inventory-name or --inventory-id is required",
		{InventoryID: inv}:                                "--source is required",
		{InventoryID: 999999, Source: path}:               "Inventory with id = 999999 cannot be found",
		{InventoryID: inv, Source: path + ".missing"}:     "Source does not exist",
		{InventoryID: inv, Source: path, HostFilter: "("}: "invalid regular expression for --host-filter",
	} {
		if _, err := ansible.ImportFile(ctx, app, *o, io.Discard); err == nil || !strings.Contains(err.Error(), want) || !ansible.IsUsageError(err) {
			t.Errorf("%+v: %v, want %q", *o, err, want)
		}
	}

	// Filters match from the start of names, as AWX's; empty groups can be left out.
	if _, err := ansible.ImportFile(ctx, app, ansible.FileImport{InventoryID: inv, Source: path, HostFilter: "web", GroupFilter: "web|empty",
		ExcludeEmptyGroups: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var hosts, groups []string
	for _, h := range a.get(fmt.Sprintf("inventories/%d/hosts/?order_by=name", inv)).results() {
		hosts = append(hosts, h["name"].(string))
	}
	for _, g := range a.get(fmt.Sprintf("inventories/%d/groups/?order_by=name", inv)).results() {
		groups = append(groups, g["name"].(string))
	}
	if fmt.Sprint(hosts) != "[web1 web2]" || fmt.Sprint(groups) != "[web]" {
		t.Fatalf("hosts %v, groups %v", hosts, groups)
	}

	// File sources come only from the command, and nothing updates them.
	src := a.get(fmt.Sprintf("inventories/%d/inventory_sources/", inv)).results()[0]
	must(t, a.post("inventory_sources/", map[string]any{"name": uniq("f"), "inventory": inv, "source": "file"}), 400)
	if r := a.get(fmt.Sprintf("inventory_sources/%v/update/", src["id"])); r.Body["can_update"] != false {
		t.Fatalf("can_update: %v", r.Body)
	}
	must(t, a.post(fmt.Sprintf("inventory_sources/%v/update/", src["id"]), nil), 405)
	if r := must(t, a.post(fmt.Sprintf("inventories/%d/update_inventory_sources/", inv), nil), 400); !strings.Contains(r.Raw, "can_update") {
		t.Fatalf("update all: %s", r.Raw)
	}
}
