package test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

type approvalFixture struct {
	*catalogFixture
	requester             *client
	reqID, team, platform int64
	p1, p2, lead, admin   *client
	p1ID, p2ID, leadID    int64
	adminID               int64
}

// newApprovalFixture: a requester in a team with an admin, a Platform team
// of two, and an organization admin, all with email addresses.
func newApprovalFixture(t *testing.T) *approvalFixture {
	t.Helper()
	f := &approvalFixture{catalogFixture: newCatalogFixture(t)}
	a := f.a
	user := func() (*client, int64) {
		c, id := newUser(t, a)
		must(t, a.patch(fmt.Sprintf("users/%d/", id), map[string]any{"email": c.user + "@example.com"}), 200)
		return c, id
	}
	team := func() int64 {
		return must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": f.org}), 201).id()
	}
	f.requester, f.reqID = user()
	f.lead, f.leadID = user()
	f.p1, f.p1ID = user()
	f.p2, f.p2ID = user()
	f.admin, f.adminID = user()
	f.team, f.platform = team(), team()
	grant(t, a, "Team Member", f.reqID, f.team)
	grant(t, a, "Team Admin", f.leadID, f.team)
	grant(t, a, "Team Member", f.p1ID, f.platform)
	grant(t, a, "Team Member", f.p2ID, f.platform)
	grant(t, a, "Organization Admin", f.adminID, f.org)
	grant(t, a, "Organization CatalogItem Requester", f.reqID, f.org)
	grant(t, a, "Organization CatalogItem Requester", f.leadID, f.org)
	return f
}

func (f *approvalFixture) item(t *testing.T, extra map[string]any) int64 {
	t.Helper()
	body := map[string]any{"name": uniq("item"), "unified_job_template": f.jt, "questions": map[string]any{"token": map[string]any{"value": "t"}}}
	for k, v := range extra {
		body[k] = v
	}
	return must(t, f.a.post("catalog_items/", body), 201).id()
}

func (f *approvalFixture) request(t *testing.T, c *client, item int64, answers map[string]any) map[string]any {
	t.Helper()
	return must(t, c.post(fmt.Sprintf("catalog_items/%d/request/", item), map[string]any{"answers": answers, "reason": "load test"}), 201).Body
}

func (f *approvalFixture) get(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	return must(t, f.a.get(fmt.Sprintf("catalog_requests/%v/", req["id"])), 200).Body
}

func approvalIDs(req map[string]any) []int64 {
	var out []int64
	for _, x := range req["approvals"].([]any) {
		out = append(out, int64(x.(map[string]any)["id"].(float64)))
	}
	return out
}

func approve(c *client, approval int64, comment string) resp {
	body := map[string]any{}
	if comment != "" {
		body["comment"] = comment
	}
	return c.post(fmt.Sprintf("workflow_approvals/%d/approve/", approval), body)
}

func deny(c *client, approval int64, comment string) resp {
	body := map[string]any{}
	if comment != "" {
		body["comment"] = comment
	}
	return c.post(fmt.Sprintf("workflow_approvals/%d/deny/", approval), body)
}

// waitRequest waits for a request to leave a status.
func (f *approvalFixture) waitRequest(t *testing.T, req map[string]any, from string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r := f.get(t, req)
		if r["status"] != from {
			return r
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("request %v still %s", req["id"], from)
	return nil
}

// An item's rules hold a request for their approvers when their condition
// holds: a group's N approvals run it with the answers given, a deny (with
// a comment) or a timeout ends it, and nobody approves their own request.
func TestCatalogApprovals(t *testing.T) {
	f := newApprovalFixture(t)
	a := f.a
	bad := func(rules any, want string) {
		t.Helper()
		r := a.post("catalog_items/", map[string]any{"name": uniq("bad"), "unified_job_template": f.jt, "approval_rules": rules})
		if r.Status != 400 || !strings.Contains(r.Raw, want) {
			t.Errorf("rules %v: %d %s", rules, r.Status, r.Raw)
		}
	}
	bad([]any{map[string]any{"name": "x", "approvers": "team_admins", "condition": "size >"}}, "condition")
	bad([]any{map[string]any{"name": "x", "approvers": "team_admins", "condition": "token == \"t\""}}, "condition")
	bad([]any{map[string]any{"name": "x", "approvers": "team_admins", "condition": "size"}}, "condition")
	bad([]any{map[string]any{"name": "x", "approvers": "boss"}}, "approvers")
	bad([]any{map[string]any{"name": "x", "approvers": "team"}}, "team")
	bad([]any{map[string]any{"name": "x", "approvers": "users"}}, "users_from")
	bad([]any{map[string]any{"name": "x", "approvers": "team_admins", "required": 0}}, "required")
	bad([]any{map[string]any{"name": "x", "approvers": "team_admins"}, map[string]any{"name": "x", "approvers": "team_admins"}}, "Two rules")
	other := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": newOrg(t, a)}), 201).id()
	bad([]any{map[string]any{"name": "x", "approvers": "team", "team": other}}, "team")

	item := f.item(t, map[string]any{"approval_rules": []any{
		map[string]any{"name": "Big", "condition": "size > 5", "approvers": "team", "team": f.platform, "required": 2},
		map[string]any{"name": "Staging", "condition": `env == "staging" && team != nil`, "approvers": "team_admins"},
	}})
	rules := must(t, a.get(fmt.Sprintf("catalog_items/%d/", item)), 200).Body["approval_rules"].([]any)
	if r := rules[1].(map[string]any); r["required"] != float64(1) || r["timeout"] != float64(7*24*3600) {
		t.Errorf("rule defaults: %v", r)
	}

	// No condition holds: the request runs at once.
	r := f.request(t, f.requester, item, map[string]any{"size": 2})
	if r["status"] == "awaiting_approval" || r["job"] == nil || len(r["approvals"].([]any)) != 0 {
		t.Fatalf("request without a rule: %v", r)
	}

	// One holds: the request waits for two of Platform.
	r = f.request(t, f.requester, item, map[string]any{"size": 8})
	if r["status"] != "awaiting_approval" || r["job"] != nil || len(r["approvals"].([]any)) != 1 {
		t.Fatalf("held request: %v", r)
	}
	gate := r["approvals"].([]any)[0].(map[string]any)
	if gate["rule"] != "Big" || gate["required"] != float64(2) || gate["fallback"] != false || len(gate["approvers"].([]any)) != 2 {
		t.Errorf("gate: %v", gate)
	}
	ap := approvalIDs(r)[0]
	if r := must(t, f.requester.post(fmt.Sprintf("workflow_approvals/%d/approve/", ap), nil), 403); !strings.Contains(r.Raw, "Nobody approves their own request") {
		t.Errorf("self-approval: %s", r.Raw)
	}
	if r := f.lead.post(fmt.Sprintf("workflow_approvals/%d/approve/", ap), nil); r.Status != 403 && r.Status != 404 {
		t.Errorf("an outsider approved: %d", r.Status)
	}
	seen := must(t, f.p1.get(fmt.Sprintf("workflow_approvals/%d/", ap)), 200).Body
	if seen["can_approve_or_deny"] != true || seen["summary_fields"].(map[string]any)["catalog_request"].(map[string]any)["id"] != r["id"] {
		t.Errorf("the approver's view: %v", seen)
	}
	if n := must(t, f.p1.get("workflow_approvals/?status=pending"), 200).count(); n != 1 {
		t.Errorf("the approver's pending approvals: %d", n)
	}
	must(t, approve(f.p1, ap, ""), 204)
	if again := approve(f.p1, ap, ""); again.Status != 400 || !strings.Contains(again.Raw, "already answered") {
		t.Errorf("a second answer: %d %s", again.Status, again.Raw)
	}
	if f.get(t, r)["status"] != "awaiting_approval" {
		t.Fatal("one approval of two ran the request")
	}
	must(t, approve(f.p2, ap, "fine"), 204)
	done := f.waitRequest(t, r, "awaiting_approval")
	if done["job"] == nil || done["status"] == "failed" || done["status"] == "denied" {
		t.Fatalf("approved request: %v", done)
	}
	job := must(t, a.get(fmt.Sprintf("jobs/%v/", done["job"])), 200).Body
	if !strings.Contains(job["extra_vars"].(string), `"size": 8`) || job["summary_fields"].(map[string]any)["created_by"].(map[string]any)["id"] != float64(f.reqID) {
		t.Errorf("the approved request's job: %v %v", job["extra_vars"], job["summary_fields"])
	}
	answers := done["approvals"].([]any)[0].(map[string]any)["answers"].([]any)
	if len(answers) != 2 || answers[1].(map[string]any)["comment"] != "fine" {
		t.Errorf("answers: %v", answers)
	}

	// A deny needs a comment, and ends the request.
	r = f.request(t, f.requester, item, map[string]any{"size": 9})
	ap = approvalIDs(r)[0]
	if r := deny(f.p1, ap, ""); r.Status != 400 || !strings.Contains(r.Raw, "comment") {
		t.Errorf("a deny without a comment: %d %s", r.Status, r.Raw)
	}
	must(t, deny(f.p1, ap, "too big for a test"), 204)
	done = f.waitRequest(t, r, "awaiting_approval")
	if done["status"] != "denied" || done["job"] != nil || !strings.Contains(done["explanation"].(string), "too big for a test") ||
		!strings.Contains(done["explanation"].(string), f.p1.user) {
		t.Errorf("denied request: %v", done)
	}

	// Both rules hold: both must approve, and one deny ends it.
	team := f.team
	r = must(t, f.requester.post(fmt.Sprintf("catalog_items/%d/request/", item), map[string]any{"answers": map[string]any{"size": 7, "env": "staging"}, "team": team}), 201).Body
	ids := approvalIDs(r)
	if len(ids) != 2 {
		t.Fatalf("two rules: %v", r["approvals"])
	}
	must(t, approve(f.lead, ids[1], ""), 204)
	if f.get(t, r)["status"] != "awaiting_approval" {
		t.Fatal("one rule of two ran the request")
	}
	must(t, deny(f.p2, ids[0], "no"), 204)
	if done := f.waitRequest(t, r, "awaiting_approval"); done["status"] != "denied" {
		t.Errorf("one deny of two rules: %v", done)
	}

	// A timeout denies.
	r = f.request(t, f.requester, item, map[string]any{"size": 6})
	ap = approvalIDs(r)[0]
	if _, err := app.DB.Exec(t.Context(), "UPDATE unified_jobs SET created = now() - interval '8 days' WHERE id = $1", ap); err != nil {
		t.Fatal(err)
	}
	done = f.waitRequest(t, r, "awaiting_approval")
	if done["status"] != "denied" || !strings.Contains(done["explanation"].(string), "timed out") {
		t.Errorf("timed out request: %v", done)
	}
	if n := must(t, a.get(fmt.Sprintf("workflow_approvals/?id=%d&timed_out=true", ap)), 200).count(); n != 1 {
		t.Error("the approval did not time out")
	}
}

// Nobody approves their own request, Team Admins included: when nobody
// else is left, the organization's admins approve, and they may answer any
// waiting request, which the request's activity stream records.
func TestCatalogApprovalFallback(t *testing.T) {
	f := newApprovalFixture(t)
	a := f.a
	item := f.item(t, map[string]any{"approval_rules": []any{map[string]any{"name": "Lead", "approvers": "team_admins"}}})

	// The lead requests: the team has no other admin.
	grant(t, a, "Team Member", f.leadID, f.team)
	r := f.request(t, f.lead, item, map[string]any{"size": 1})
	gate := r["approvals"].([]any)[0].(map[string]any)
	people := []string{}
	for _, p := range gate["approvers"].([]any) {
		people = append(people, p.(map[string]any)["username"].(string))
	}
	if gate["fallback"] != true || !slices.Contains(people, f.admin.user) || slices.Contains(people, f.lead.user) {
		t.Fatalf("fallback gate: %v", gate)
	}
	ap := approvalIDs(r)[0]
	must(t, approve(f.lead, ap, ""), 403)
	must(t, approve(f.admin, ap, ""), 204)
	if done := f.waitRequest(t, r, "awaiting_approval"); done["job"] == nil {
		t.Fatalf("approved by the fallback: %v", done)
	}

	// The requester's lead approves; an organization admin may answer
	// instead, as an admin, and it decides.
	r = f.request(t, f.requester, item, map[string]any{"size": 1})
	gate = r["approvals"].([]any)[0].(map[string]any)
	if gate["fallback"] != false || len(gate["approvers"].([]any)) != 1 || gate["approvers"].([]any)[0].(map[string]any)["id"] != float64(f.leadID) {
		t.Fatalf("team admins gate: %v", gate)
	}
	ap = approvalIDs(r)[0]
	must(t, approve(f.p1, ap, ""), 403)
	must(t, deny(f.admin, ap, "not this week"), 204)
	done := f.waitRequest(t, r, "awaiting_approval")
	if done["status"] != "denied" || !strings.Contains(done["explanation"].(string), "as an organization admin") {
		t.Errorf("denied by an admin: %v", done)
	}
	var changes string
	if err := app.DB.QueryRow(t.Context(), `SELECT changes::text FROM activity_stream WHERE object1 = 'catalog_request' AND object1_id = $1
		AND operation = 'update' ORDER BY id DESC LIMIT 1`, done["id"]).Scan(&changes); err != nil || !strings.Contains(changes, `"as_admin": true`) ||
		!strings.Contains(changes, "not this week") {
		t.Errorf("the admin's answer in the activity stream: %q %v", changes, err)
	}

	// Users that a picked item names: matched by username or email.
	named := f.item(t, map[string]any{"approval_rules": []any{map[string]any{"name": "Owners", "approvers": "users",
		"users_from": fmt.Sprintf(`[%q, %q, "nobody@example.com"]`, strings.ToUpper(f.p1.user), f.p2.user+"@example.com")}}})
	r = f.request(t, f.requester, named, map[string]any{"size": 1})
	if gate := r["approvals"].([]any)[0].(map[string]any); len(gate["approvers"].([]any)) != 2 || gate["fallback"] != false {
		t.Errorf("named approvers: %v", gate)
	}
}

// A window that lists an item refuses its requests, or holds them for the
// exception that the item's team approves.
func TestCatalogApprovalFreeze(t *testing.T) {
	f := newApprovalFixture(t)
	a := f.a
	item := f.item(t, nil)
	start := time.Now().UTC().Add(-10 * time.Minute).Format("20060102T150405Z")
	win := must(t, a.post("maintenance_windows/", map[string]any{"name": uniq("freeze"), "organization": f.org, "kind": "freeze",
		"rrule": "DTSTART:" + start + " RRULE:FREQ=DAILY", "duration": 3600, "catalog_items": []any{item}}), 201).Body
	if win["active"] != true {
		t.Fatalf("window: %v", win)
	}
	r := f.requester.post(fmt.Sprintf("catalog_items/%d/request/", item), map[string]any{"answers": map[string]any{"size": 1}})
	if r.Status != 400 || !strings.Contains(r.Raw, "change freeze") || r.Body["window"] == nil {
		t.Fatalf("a request inside a freeze: %d %s", r.Status, r.Raw)
	}
	other := f.item(t, nil)
	f.request(t, f.requester, other, map[string]any{"size": 1})

	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"freeze_exception_team": f.platform}), 200)
	held := f.request(t, f.requester, item, map[string]any{"size": 1})
	gate := held["approvals"].([]any)[0].(map[string]any)
	if held["status"] != "awaiting_approval" || gate["kind"] != "freeze" || len(gate["approvers"].([]any)) != 2 || gate["required"] != float64(1) {
		t.Fatalf("an exception request: %v", held)
	}
	ap := must(t, a.get(fmt.Sprintf("workflow_approvals/%d/", approvalIDs(held)[0])), 200).Body
	if timeout := ap["timeout"].(float64); timeout < 2900 || timeout > 3000 {
		t.Errorf("the exception's timeout, until the window's end: %v", timeout)
	}
	must(t, approve(f.p2, approvalIDs(held)[0], ""), 204)
	if done := f.waitRequest(t, held, "awaiting_approval"); done["job"] == nil {
		t.Errorf("the exception approved: %v", done)
	}

	// A window that lists items only covers no event rule; deleting the
	// item drops it from the window.
	must(t, a.delete(fmt.Sprintf("catalog_items/%d/", item)), 204)
	if left := must(t, a.get(fmt.Sprintf("maintenance_windows/%v/", win["id"])), 200).Body["catalog_items"].([]any); len(left) != 0 {
		t.Errorf("the deleted item is still listed: %v", left)
	}
}

// Approvers are emailed at their own addresses when a request waits for
// them, and the requester when it is decided; the organization's approval
// notifications are sent as for any workflow approval.
func TestCatalogApprovalNotifications(t *testing.T) {
	f := newApprovalFixture(t)
	a := f.a
	s := startSMTP(t, &smtpServer{})
	personal := newEmailTemplate(t, a, f.org, emailConfig(s.port, nil), nil)
	orgWide := newEmailTemplate(t, a, f.org, emailConfig(s.port, map[string]any{"recipients": []any{"approvals@example.com"}}), nil)
	must(t, a.post(fmt.Sprintf("organizations/%d/notification_templates_approvals/", f.org), map[string]any{"id": orgWide}), 204)
	hook := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": "https://example.com/hook", "http_method": "POST"}}), 201).id()
	must(t, a.post("catalog_items/", map[string]any{"name": uniq("x"), "unified_job_template": f.jt, "notification_template": hook}), 400)
	item := f.item(t, map[string]any{"notification_template": personal,
		"approval_rules": []any{map[string]any{"name": "Platform", "approvers": "team", "team": f.platform}}})

	r := f.request(t, f.requester, item, map[string]any{"size": 3})
	byTo := map[string]smtpMsg{}
	for range 2 {
		m := s.next(t)
		byTo[strings.Join(m.To, ",")] = m
	}
	approvers := strings.Join([]string{f.p1.user + "@example.com", f.p2.user + "@example.com"}, ",")
	if m, ok := byTo[approvers]; !ok || !strings.Contains(m.subject(), "Approval needed") || !linksTo(m.Body, fmt.Sprintf("/portal/approvals/%d", approvalIDs(r)[0])) ||
		!strings.Contains(m.Body, "load test") {
		t.Errorf("approvers' email: %v", byTo)
	}
	if _, ok := byTo["approvals@example.com"]; !ok {
		t.Errorf("the organization's approval notification: %v", byTo)
	}

	must(t, approve(f.p1, approvalIDs(r)[0], ""), 204)
	byTo = map[string]smtpMsg{}
	for range 2 {
		m := s.next(t)
		byTo[strings.Join(m.To, ",")] = m
	}
	if m, ok := byTo[f.requester.user+"@example.com"]; !ok || !strings.Contains(m.subject(), "is approved") || !linksTo(m.Body, fmt.Sprintf("/portal/requests/%v", r["id"])) {
		t.Errorf("the requester's email: %v", byTo)
	}
	if m, ok := byTo["approvals@example.com"]; !ok || !strings.Contains(strings.ToLower(m.subject()), "approved") {
		t.Errorf("the organization's approved notification: %v", byTo)
	}
}

// linksTo tells whether a mail's body has a link to path, whole.
func linksTo(body, path string) bool {
	for _, f := range strings.Fields(body) {
		if strings.HasPrefix(f, "http") && strings.HasSuffix(f, path) {
			return true
		}
	}
	return false
}
