package test

import (
	"fmt"
	"strings"
	"testing"
)

func TestBulkHosts(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	must(t, a.post("hosts/", map[string]any{"name": "taken", "inventory": inv}), 201)
	if r := must(t, a.get("bulk/"), 200); r.Body["host_create"] != "/api/v2/bulk/host_create/" || r.Body["job_launch"] != "/api/v2/bulk/job_launch/" {
		t.Errorf("bulk/: %s", r.Raw)
	}

	// As the collection's bulk_host_create sends them.
	r := must(t, a.post("bulk/host_create/", map[string]any{"inventory": inv, "hosts": []any{
		map[string]any{"name": "123.456.789.123", "description": "myhost1", "variables": `{"food": "carrot", "color": "orange"}`},
		map[string]any{"name": "example.dns.gg", "description": "myhost2", "enabled": "false"},
	}}), 201)
	hosts := r.Body["hosts"].([]any)
	if r.Body["url"] != fmt.Sprintf("/api/v2/inventories/%d/", inv) || len(hosts) != 2 {
		t.Fatalf("host_create: %s", r.Raw)
	}
	h2 := hosts[1].(map[string]any)
	if h2["enabled"] != false || h2["url"] == nil || h2["inventory"] != r.Body["url"] {
		t.Errorf("created host: %v", h2)
	}
	if n := must(t, a.get(fmt.Sprintf("inventories/%d/hosts/", inv)), 200).count(); n != 3 {
		t.Errorf("inventory has %d hosts", n)
	}

	nonField := func(r resp, want string) {
		t.Helper()
		if r.Status != 400 || !strings.Contains(r.Raw, want) {
			t.Errorf("want 400 %q: %d %s", want, r.Status, r.Raw)
		}
	}
	nonField(a.post("bulk/host_create/", map[string]any{"inventory": inv, "hosts": []any{map[string]any{"name": "taken"}, map[string]any{"name": "new"}, map[string]any{"name": "new"}}}),
		`Hostnames must be unique in an inventory. Duplicates found: ['taken', 'new', 'new']`)
	smart := must(t, a.post("inventories/", map[string]any{"name": uniq("smart"), "organization": org, "kind": "smart", "host_filter": "name=x"}), 201).id()
	nonField(a.post("bulk/host_create/", map[string]any{"inventory": smart, "hosts": []any{map[string]any{"name": "x"}}}),
		"Hosts can only be created in manual inventories (not smart or constructed types).")
	bob, bobID := newUser(t, a)
	nonField(bob.post("bulk/host_create/", map[string]any{"inventory": inv, "hosts": []any{map[string]any{"name": "x"}}}),
		fmt.Sprintf("Inventory with id %d not found or lack permissions to add hosts.", inv))
	// All or none: an invalid host creates nothing.
	r = must(t, a.post("bulk/host_create/", map[string]any{"inventory": inv, "hosts": []any{map[string]any{"name": "fine"}, map[string]any{"name": ""}}}), 400)
	if errs := r.Body["hosts"].([]any); len(errs) != 2 || len(errs[1].(map[string]any)) == 0 {
		t.Errorf("host errors: %s", r.Raw)
	}
	if n := must(t, a.get(fmt.Sprintf("inventories/%d/hosts/?name=fine", inv)), 200).count(); n != 0 {
		t.Error("a refused bulk create left hosts behind")
	}
	must(t, a.patch("settings/jobs/", map[string]any{"BULK_HOST_MAX_CREATE": 1}), 200)
	t.Cleanup(func() {
		a.patch("settings/jobs/", map[string]any{"BULK_HOST_MAX_CREATE": 100, "BULK_HOST_MAX_DELETE": 250})
	})
	nonField(a.post("bulk/host_create/", map[string]any{"inventory": inv, "hosts": []any{map[string]any{"name": "p"}, map[string]any{"name": "q"}}}),
		"Number of hosts exceeds system setting BULK_HOST_MAX_CREATE")

	// Delete.
	var ids []any
	for _, h := range hosts {
		ids = append(ids, h.(map[string]any)["id"])
	}
	r = must(t, bob.post("bulk/host_delete/", map[string]any{"hosts": ids}), 403)
	if !strings.Contains(r.Raw, "Lack permissions to delete hosts from this inventory.") {
		t.Errorf("delete without permission: %s", r.Raw)
	}
	r = must(t, a.post("bulk/host_delete/", map[string]any{"hosts": append(ids, 999999)}), 400)
	if r.Body["hosts"].(map[string]any)["999999"] != "Hosts do not exist or you lack permission to delete it" {
		t.Errorf("missing host: %s", r.Raw)
	}
	must(t, a.patch("settings/jobs/", map[string]any{"BULK_HOST_MAX_DELETE": 1}), 200)
	if r = must(t, a.post("bulk/host_delete/", map[string]any{"hosts": ids}), 400); r.Body["Hosts_count"] != float64(2) {
		t.Errorf("over the limit: %s", r.Raw)
	}
	must(t, a.patch("settings/jobs/", map[string]any{"BULK_HOST_MAX_DELETE": 250}), 200)
	r = must(t, a.post("bulk/host_delete/", map[string]any{"hosts": ids}), 201)
	if msg := r.Body["hosts"].(map[string]any)[fmt.Sprint(ids[1])]; msg != "The host example.dns.gg was deleted" {
		t.Errorf("host_delete: %s", r.Raw)
	}
	if n := must(t, a.get(fmt.Sprintf("inventories/%d/hosts/", inv)), 200).count(); n != 1 {
		t.Errorf("inventory has %d hosts after the delete", n)
	}
	_ = bobID
}

func TestBulkJobLaunch(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true, "ask_inventory_on_launch": true, "ask_skip_tags_on_launch": true, "allow_simultaneous": true})

	// As the collection's bulk_job_launch sends it.
	r := must(t, a.post("bulk/job_launch/", map[string]any{
		"name": "bulk", "organization": f.org, "inventory": f.inv, "limit": "h1", "skip_tags": "skipbaz", "job_tags": "Hello World",
		"extra_vars": map[string]any{"animal": "bear", "food": "carrot"},
		"jobs": []any{
			map[string]any{"unified_job_template": jt, "inventory": f.inv, "skip_tags": "skipfoo,skipbar", "extra_data": map[string]any{"animal": "fish", "color": "orange"}},
			map[string]any{"unified_job_template": jt},
		},
	}), 201)
	if r.Body["type"] != "workflow_job" || r.Body["is_bulk_job"] != true || r.Body["skip_tags"] != "skipbaz" || r.Body["limit"] != "h1" ||
		r.Body["job_tags"] != "Hello World" || r.Body["inventory"] != float64(f.inv) || r.Body["extra_vars"] != `{"animal": "bear", "food": "carrot"}` {
		t.Fatalf("bulk job: %s", r.Raw)
	}
	wf := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", r.id()))
	if wf.Body["status"] != "successful" {
		t.Fatalf("bulk job %v: %v", wf.Body["status"], wf.Body["job_explanation"])
	}
	nodes := must(t, a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", r.id())), 200).results()
	if len(nodes) != 2 {
		t.Fatalf("nodes: %v", nodes)
	}
	for _, n := range nodes {
		job := must(t, a.get(fmt.Sprintf("jobs/%v/", n["summary_fields"].(map[string]any)["job"].(map[string]any)["id"])), 200)
		// The workflow's prompts win over the node's, as in AWX.
		if job.Body["skip_tags"] != "skipbaz" || !strings.Contains(job.Body["extra_vars"].(string), `"animal": "bear"`) {
			t.Errorf("node job: skip_tags %v extra_vars %v", job.Body["skip_tags"], job.Body["extra_vars"])
		}
	}

	// A node's prompts are checked against its template, as a workflow node's.
	r = must(t, a.post("bulk/job_launch/", map[string]any{"jobs": []any{map[string]any{"unified_job_template": jt}, map[string]any{"unified_job_template": jt, "limit": "h1"}}}), 400)
	if errs := r.Body["jobs"].([]any); len(errs[0].(map[string]any)) != 0 || errs[1].(map[string]any)["limit"] == nil {
		t.Errorf("unprompted node field: %s", r.Raw)
	}
	user, uid := newUser(t, a)
	r = must(t, user.post("bulk/job_launch/", map[string]any{"jobs": []any{map[string]any{"unified_job_template": jt}}}), 400)
	if !strings.Contains(r.Raw, fmt.Sprintf("Job Templates {%d} not found or you don't have permissions to access it", jt)) {
		t.Errorf("no execute permission: %s", r.Raw)
	}
	execute := must(t, a.get(fmt.Sprintf("job_templates/%d/", jt)), 200).Body["summary_fields"].(map[string]any)["object_roles"].(map[string]any)["execute_role"].(map[string]any)["id"]
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": execute}), 204)
	r = must(t, user.post("bulk/job_launch/", map[string]any{"jobs": []any{map[string]any{"unified_job_template": jt}}}), 400)
	if !strings.Contains(r.Raw, "User not part of any organization, please assign an organization to assign to the bulk job") {
		t.Errorf("no organization: %s", r.Raw)
	}
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", f.org), map[string]any{"id": uid}), 204)
	r = must(t, user.post("bulk/job_launch/", map[string]any{"jobs": []any{map[string]any{"unified_job_template": jt}}}), 201)
	if r.Body["organization"] != float64(f.org) {
		t.Errorf("organization of the bulk job: %v", r.Body["organization"])
	}
	// As in AWX: whoever launched it and the admins of its organization see
	// it; only superusers relaunch it, as it has no template.
	userJob := r.id()
	must(t, user.get(fmt.Sprintf("workflow_jobs/%d/", userJob)), 200)
	outsider, _ := newUser(t, a)
	must(t, outsider.get(fmt.Sprintf("workflow_jobs/%d/", userJob)), 403)
	orgAdmin, orgAdminID := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/admins/", f.org), map[string]any{"id": orgAdminID}), 204)
	must(t, orgAdmin.get(fmt.Sprintf("workflow_jobs/%d/", userJob)), 200)
	waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", userJob))
	must(t, user.post(fmt.Sprintf("workflow_jobs/%d/relaunch/", userJob), nil), 403)
	again := must(t, a.post(fmt.Sprintf("workflow_jobs/%d/relaunch/", userJob), nil), 201)
	if rj := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", again.id())); rj.Body["is_bulk_job"] != true || rj.Body["status"] != "successful" {
		t.Errorf("relaunched bulk job: %v %v", rj.Body["is_bulk_job"], rj.Body["status"])
	}
	must(t, a.patch("settings/jobs/", map[string]any{"BULK_JOB_MAX_LAUNCH": 1}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"BULK_JOB_MAX_LAUNCH": 100}) })
	r = must(t, a.post("bulk/job_launch/", map[string]any{"jobs": []any{map[string]any{"unified_job_template": jt}, map[string]any{"unified_job_template": jt}}}), 400)
	if !strings.Contains(r.Raw, "Number of requested jobs exceeds system setting BULK_JOB_MAX_LAUNCH") {
		t.Errorf("over the limit: %s", r.Raw)
	}
}
