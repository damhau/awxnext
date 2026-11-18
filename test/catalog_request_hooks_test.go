package test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// hookEvents is what the item's request webhooks heard about a request, in
// order.
func hookEvents(reqs []recordedRequest, request any) ([]string, []map[string]any) {
	var events []string
	var bodies []map[string]any
	for _, r := range reqs {
		if q, _ := r.Body["request"].(map[string]any); q != nil && q["id"] == request {
			events = append(events, fmt.Sprint(r.Body["event"]))
			bodies = append(bodies, r.Body)
		}
	}
	return events, bodies
}

// Another catalog, such as ServiceNow, requests an item through the API,
// follows the request to ready and reads the deployment's outputs; the
// item's request webhooks hear of each change of state, once.
func TestCatalogRequestHooks(t *testing.T) {
	requireAnsible(t)
	f := newApprovalFixture(t)
	a := f.a
	teardown := "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.debug:\n        msg: \"deleting {{ host }}\"\n"
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("hooks"), "organization": f.org, "scm_type": "git",
		"scm_url": gitRepo(t, map[string]string{"provision.yml": provisionPlaybook, "teardown.yml": teardown,
			"fail.yml": "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.fail:\n        msg: no capacity\n"})}), 201).id()
	f.waitProject(t)
	prov := f.template(t, "provision.yml", map[string]any{"survey_enabled": true, "outputs": []any{map[string]any{"name": "host", "type": "string"},
		map[string]any{"name": "db_user", "type": "string"}, map[string]any{"name": "db_password", "type": "string", "secret": true}}})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", prov), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Name", "variable": "name", "type": "text", "required": true},
		map[string]any{"question_name": "Size", "variable": "size", "type": "multiplechoice", "choices": []any{"small", "large"}, "default": "small", "required": false},
	}}), 200)
	rec, hook := newHookServer(t)
	webhook := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	item := must(t, a.post("catalog_items/", map[string]any{
		"name": uniq("db"), "unified_job_template": prov, "kind": "deployment", "teardown_template": f.template(t, "teardown.yml", nil),
		"deployment_name": "answers.name",
		"connection":      []any{map[string]any{"label": "Host", "value": "host"}, map[string]any{"label": "Password", "value": "db_password"}},
		"approval_rules":  []any{map[string]any{"name": "Large", "condition": `size == "large"`, "approvers": "team_admins"}},
	}), 201).id()
	links := fmt.Sprintf("catalog_items/%d/notification_templates_requests/", item)
	if rel := must(t, a.get(fmt.Sprintf("catalog_items/%d/", item)), 200).Body["related"].(map[string]any); !strings.HasSuffix(fmt.Sprint(rel["notification_templates_requests"]), links) {
		t.Errorf("the item's related links: %v", rel)
	}
	must(t, f.requester.post(links, map[string]any{"id": webhook}), 403)
	must(t, a.post(links, map[string]any{"id": webhook}), 204)
	if l := must(t, a.get(links), 200).Body; l["count"] != float64(1) {
		t.Fatalf("the item's request webhooks: %v", l)
	}

	// A request that waits for approval, then runs and is ready.
	req := must(t, f.requester.post(fmt.Sprintf("catalog_items/%d/request/", item), map[string]any{
		"answers": map[string]any{"name": "shop", "size": "large"}, "reason": "load test"}), 201).Body
	if req["status"] != "awaiting_approval" {
		t.Fatalf("a large one: %v", req)
	}
	waitFor(t, "the waiting webhook", func() bool { e, _ := hookEvents(rec.wait(t, 1), req["id"]); return len(e) == 1 })
	must(t, approve(f.lead, approvalIDs(req)[0], ""), 204)
	if done := waitRequest(t, f.requester, req["id"]); done["status"] != "successful" || done["deployment"] == nil {
		t.Fatalf("the request: %v", done)
	}
	var bodies []map[string]any
	waitFor(t, "the ready webhook", func() bool {
		var e []string
		e, bodies = hookEvents(rec.wait(t, 1), req["id"])
		return slices.Contains(e, "ready")
	})
	events, _ := hookEvents(rec.wait(t, 1), req["id"])
	if !slices.Equal(events, []string{"waiting", "approved", "running", "ready"}) {
		t.Errorf("the request's webhooks: %v", events)
	}

	// The deployment's outputs, through the API and in the webhook, with
	// secrets masked.
	dep := fmt.Sprintf("deployments/%v/", f.get(t, req)["deployment"])
	d := must(t, f.requester.get(dep), 200).Body
	if outs := d["outputs"].(map[string]any); outs["host"] != "db.shop.internal" || outs["db_password"] != "$encrypted$" {
		t.Errorf("the deployment's outputs: %v", outs)
	}
	ready := bodies[len(bodies)-1]
	r, hd := ready["request"].(map[string]any), ready["deployment"].(map[string]any)
	if r["status"] != "successful" || r["kind"] != "new" || r["requester"] != f.requester.user || r["reason"] != "load test" ||
		!strings.HasSuffix(fmt.Sprint(r["url"]), fmt.Sprintf("/portal/requests/%v", req["id"])) || ready["time"] == nil {
		t.Errorf("the ready webhook's request: %v", r)
	}
	if outs := hd["outputs"].(map[string]any); hd["name"] != "shop" || hd["status"] != "active" || outs["host"] != "db.shop.internal" ||
		outs["db_password"] != "$encrypted$" || !strings.HasSuffix(fmt.Sprint(hd["url"]), fmt.Sprintf("/portal/deployments/%v", d["id"])) {
		t.Errorf("the ready webhook's deployment: %v", hd)
	}
	conn := fmt.Sprint(hd["connection"])
	if !strings.Contains(conn, "db.shop.internal") || strings.Contains(conn, "Init-shop-Secret") {
		t.Errorf("the ready webhook's connection: %v", conn)
	}
	if body := fmt.Sprint(ready); strings.Contains(body, "Init-shop-Secret") {
		t.Errorf("a secret in the webhook: %v", body)
	}

	// A denied request.
	big := must(t, f.requester.post(fmt.Sprintf("catalog_items/%d/request/", item), map[string]any{
		"answers": map[string]any{"name": "big", "size": "large"}}), 201).Body
	must(t, deny(f.lead, approvalIDs(big)[0], "use a small one"), 204)
	waitFor(t, "the denied webhook", func() bool { e, _ := hookEvents(rec.wait(t, 1), big["id"]); return slices.Contains(e, "denied") })
	if e, b := hookEvents(rec.wait(t, 1), big["id"]); !slices.Equal(e, []string{"waiting", "denied"}) ||
		b[1]["request"].(map[string]any)["explanation"] == "" {
		t.Errorf("the denied request's webhooks: %v %v", e, b)
	}

	// A request that fails.
	broken := must(t, a.post("catalog_items/", map[string]any{"name": uniq("broken"), "unified_job_template": f.template(t, "fail.yml", nil)}), 201).id()
	must(t, a.post(fmt.Sprintf("catalog_items/%d/notification_templates_requests/", broken), map[string]any{"id": webhook}), 204)
	failed := must(t, f.requester.post(fmt.Sprintf("catalog_items/%d/request/", broken), map[string]any{}), 201).Body
	if r := waitRequest(t, a, failed["id"]); r["status"] != "failed" {
		t.Fatalf("the broken request: %v", r)
	}
	waitFor(t, "the failed webhook", func() bool { e, _ := hookEvents(rec.wait(t, 1), failed["id"]); return slices.Contains(e, "failed") })
	if e, b := hookEvents(rec.wait(t, 1), failed["id"]); !slices.Equal(e, []string{"running", "failed"}) || b[1]["deployment"] != nil {
		t.Errorf("the failed request's webhooks: %v %v", e, b)
	}

	// A day before it expires.
	must(t, a.patch(dep, map[string]any{"expires_at": time.Now().Add(23 * time.Hour).Format(time.RFC3339)}), 200)
	waitFor(t, "the expiring webhook", func() bool { e, _ := hookEvents(rec.wait(t, 1), req["id"]); return slices.Contains(e, "expiring") })
	if e, b := hookEvents(rec.wait(t, 1), req["id"]); e[len(e)-1] != "expiring" || b[len(b)-1]["hours_left"] != float64(24) ||
		b[len(b)-1]["deployment"].(map[string]any)["expires_at"] == nil {
		t.Errorf("the expiring webhook: %v %v", e, b[len(b)-1])
	}

	// Deleting the deployment: its request runs, then it is deleted.
	must(t, f.requester.delete(dep), 202)
	waitFor(t, "the deletion", func() bool { return must(t, a.get(dep), 200).Body["status"] == "deleted" })
	del := must(t, a.get(dep+"requests/?kind=delete"), 200).Body["results"].([]any)[0].(map[string]any)
	waitFor(t, "the deleted webhook", func() bool { e, _ := hookEvents(rec.wait(t, 1), del["id"]); return slices.Contains(e, "deleted") })
	if e, b := hookEvents(rec.wait(t, 1), del["id"]); !slices.Equal(e, []string{"running", "deleted"}) || b[1]["deployment"].(map[string]any)["status"] != "deleted" {
		t.Errorf("the deletion's webhooks: %v %v", e, b)
	}

	// Each change is sent once.
	time.Sleep(2 * time.Second)
	if n := len(rec.wait(t, 1)); n != 11 {
		t.Errorf("%d webhooks, want 11", n)
	}
}
