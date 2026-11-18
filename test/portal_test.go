package test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const stepsPlaybook = `- name: Create the VM
  hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: created
- name: Install the packages
  hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.fail:
        msg: the mirror is down
      when: broken == "yes"
    - ansible.builtin.debug:
        msg: installed
`

// The portal's API: who lands on it, the organization's name and logo,
// what needs the user, what a request would wait for before it's sent, and
// a request's progress as the playbook's plays.
func TestPortal(t *testing.T) {
	requireAnsible(t)
	f := newApprovalFixture(t)
	a := f.a
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("portal"), "organization": f.org, "scm_type": "git",
		"scm_url": gitRepo(t, map[string]string{"steps.yml": stepsPlaybook})}), 201).id()
	f.waitProject(t)
	tpl := f.template(t, "steps.yml", map[string]any{"survey_enabled": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", tpl), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Size", "variable": "size", "type": "integer", "min": 1, "max": 10, "required": true, "default": 2},
		map[string]any{"question_name": "Broken", "variable": "broken", "type": "multiplechoice", "choices": []any{"yes", "no"}, "default": "no", "required": true},
	}}), 200)
	must(t, a.patch(fmt.Sprintf("users/%d/", f.leadID), map[string]any{"first_name": "Priya", "last_name": "Shah"}), 200)
	item := must(t, a.post("catalog_items/", map[string]any{"name": uniq("vm"), "unified_job_template": tpl, "category": "Compute",
		"step_labels":    map[string]any{"Create the VM": "Creating your VM"},
		"approval_rules": []any{map[string]any{"name": "Large VMs", "condition": "size > 5", "approvers": "team_admins", "timeout": 172800}},
	}), 201).id()

	// Who lands on the portal: those whose roles only let them request.
	itemOnly, itemOnlyID := newUser(t, a)
	grant(t, a, "CatalogItem Requester", itemOnlyID, item)
	member, memberID := newUser(t, a)
	grant(t, a, "Organization Member", memberID, f.org)
	grant(t, a, "CatalogItem Requester", memberID, item)
	for who, c := range map[string]*client{"a requester": f.requester, "an item's requester": itemOnly, "a member who requests": member} {
		if p := must(t, c.get("portal/"), 200).Body; p["request_only"] != true {
			t.Errorf("%s: %v", who, p)
		}
	}
	for who, c := range map[string]*client{"an organization admin": f.admin, "a user without roles": f.p1, "a team admin": f.lead} {
		if p := must(t, c.get("portal/"), 200).Body; p["request_only"] != false {
			t.Errorf("%s: %v", who, p)
		}
	}

	// The organization's name and logo.
	must(t, a.patch(fmt.Sprintf("organizations/%d/", f.org), map[string]any{"portal_logo": "https://example.com/logo.png"}), 400)
	logo := "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4="
	must(t, a.patch(fmt.Sprintf("organizations/%d/", f.org), map[string]any{"portal_title": "Acme self-service", "portal_logo": logo}), 200)
	if b, _ := must(t, f.requester.get("portal/"), 200).Body["brand"].(map[string]any); b == nil || b["title"] != "Acme self-service" || b["logo"] != logo {
		t.Errorf("brand: %v", b)
	}

	// Before sending: the approvals a request would wait for, and why.
	preview := fmt.Sprintf("catalog_items/%d/preview/", item)
	p := must(t, f.requester.post(preview, map[string]any{"answers": map[string]any{"size": 8}}), 200).Body
	apps, _ := p["approvals"].([]any)
	if len(apps) != 1 {
		t.Fatalf("a large VM's preview: %v", p)
	}
	g := apps[0].(map[string]any)
	who := g["approvers"].([]any)[0].(map[string]any)
	if g["rule"] != "Large VMs" || g["why"] != "Rule Large VMs: size > 5" || g["timeout"] != float64(172800) || who["name"] != "Priya Shah" || who["username"] != f.lead.user {
		t.Errorf("the approval: %v", g)
	}
	if p := must(t, f.requester.post(preview, map[string]any{"answers": map[string]any{"size": 2}}), 200).Body; len(p["approvals"].([]any)) != 0 || p["refused"] != nil || p["quota"] != nil {
		t.Errorf("a small VM's preview: %v", p)
	}
	must(t, f.requester.post(preview, map[string]any{"answers": map[string]any{"size": 20}}), 400)
	if n := must(t, a.get(fmt.Sprintf("catalog_items/%d/requests/", item)), 200).count(); n != 0 {
		t.Errorf("a preview made %d requests", n)
	}

	// What needs the user: their waiting request, the lead's approval.
	held := f.request(t, f.requester, item, map[string]any{"size": 8})
	needs := must(t, f.requester.get("portal/"), 200).Body["needs_you"].(map[string]any)
	if rs := needs["requests"].([]any); len(rs) != 1 || rs[0].(map[string]any)["id"] != held["id"] || rs[0].(map[string]any)["status"] != "awaiting_approval" {
		t.Errorf("the requester's waiting requests: %v", needs)
	}
	if n := must(t, f.lead.get("portal/"), 200).Body["needs_you"].(map[string]any)["approvals"]; n != float64(1) {
		t.Errorf("the lead's approvals: %v", n)
	}
	must(t, approve(f.lead, approvalIDs(held)[0], ""), 204)
	if n := must(t, f.lead.get("portal/"), 200).Body["needs_you"].(map[string]any)["approvals"]; n != float64(0) {
		t.Errorf("the lead's approvals once answered: %v", n)
	}
	if r := waitRequest(t, a, held["id"]); r["status"] != "successful" {
		t.Fatalf("the approved request: %v", r)
	}

	// Progress: the plays as the item names them, and the job for those who see it.
	progress := func(c *client, id any) map[string]any {
		return must(t, c.get(fmt.Sprintf("catalog_requests/%v/progress/", id)), 200).Body
	}
	steps := func(p map[string]any) string {
		var out []string
		for _, x := range p["steps"].([]any) {
			s := x.(map[string]any)
			out = append(out, fmt.Sprintf("%v:%v", s["label"], s["state"]))
		}
		return strings.Join(out, ", ")
	}
	done := progress(f.requester, held["id"])
	if steps(done) != "Creating your VM:done, Install the packages:done" || done["percent"] != float64(100) || done["job"].(map[string]any)["can_see"] != false {
		t.Errorf("a finished request's progress: %v", done)
	}
	if j := progress(a, held["id"])["job"].(map[string]any); j["can_see"] != true {
		t.Errorf("the admin's view of the job: %v", j)
	}
	broken := waitRequest(t, a, f.request(t, f.requester, item, map[string]any{"size": 2, "broken": "yes"})["id"])
	if p := progress(f.requester, broken["id"]); steps(p) != "Creating your VM:done, Install the packages:failed" || p["percent"] != nil {
		t.Errorf("a failed request's progress: %v", p)
	}
	mine := must(t, f.requester.get(fmt.Sprintf("catalog_requests/?created_by=%d", f.reqID)), 200)
	if mine.count() != 2 {
		t.Errorf("the requester's requests: %s", mine.Raw)
	}

	// A frozen item shows its freeze, and the preview says it's refused.
	win := must(t, a.post("maintenance_windows/", map[string]any{"name": uniq("freeze"), "organization": f.org, "kind": "freeze",
		"rrule": "DTSTART:" + time.Now().UTC().Add(-time.Minute).Format("20060102T150405Z") + " RRULE:FREQ=DAILY", "duration": 3600, "catalog_items": []any{item}}), 201).Body
	fz, _ := must(t, f.requester.get(fmt.Sprintf("catalog_items/%d/", item)), 200).Body["summary_fields"].(map[string]any)["freeze"].(map[string]any)
	if fz == nil || fz["name"] != win["name"] || fz["until"] == nil {
		t.Errorf("the item's freeze: %v", fz)
	}
	if r, _ := must(t, f.requester.post(preview, map[string]any{"answers": map[string]any{"size": 2}}), 200).Body["refused"].(map[string]any); r == nil ||
		!strings.Contains(fmt.Sprint(r["detail"]), "refused inside change freeze") {
		t.Errorf("a frozen item's preview: %v", r)
	}
}
