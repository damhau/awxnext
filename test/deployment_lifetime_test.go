package test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func parseAPITime(t *testing.T, v any) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, fmt.Sprint(v))
	if err != nil {
		t.Fatalf("time %v: %v", v, err)
	}
	return ts
}

// nextMail returns the next email whose subject has want, skipping others.
func nextMail(t *testing.T, s *smtpServer, want string) smtpMsg {
	t.Helper()
	for range 10 {
		if m := s.next(t); strings.Contains(m.subject(), want) {
			return m
		}
	}
	t.Fatalf("no email about %q", want)
	return smtpMsg{}
}

// A deployment expires when its requester chose; its owners extend it by
// the item's steps, through its rules and within its caps; they are reminded
// 24 and 2 hours before it expires; and at expiry, its teardown runs, inside
// a change freeze too unless the item says otherwise.
func TestDeploymentLifetime(t *testing.T) {
	requireAnsible(t)
	f := newApprovalFixture(t)
	a := f.a
	out := t.TempDir()
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("lifetime"), "organization": f.org, "scm_type": "git",
		"scm_url": gitRepo(t, map[string]string{"provision.yml": provisionPlaybook, "teardown.yml": strings.ReplaceAll(teardownPlaybook, "OUT", out)})}), 201).id()
	f.waitProject(t)
	prov := f.template(t, "provision.yml", map[string]any{"survey_enabled": true, "outputs": []any{map[string]any{"name": "host", "type": "string"},
		map[string]any{"name": "db_user", "type": "string"}, map[string]any{"name": "db_password", "type": "string", "secret": true}}})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", prov), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Name", "variable": "name", "type": "text", "required": true},
	}}), 200)
	teardown := f.template(t, "teardown.yml", nil)
	s := startSMTP(t, &smtpServer{})
	rec, hook := newHookServer(t)
	webhook := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()

	for what, body := range map[string]map[string]any{
		"extensions on an item that runs once": {"extension_days": []any{1}},
		"a cap on an item that runs once":      {"max_extensions": 2},
		"an extension of no day":               {"kind": "deployment", "extension_days": []any{0}},
		"one extension twice":                  {"kind": "deployment", "extension_days": []any{3, 3}},
		"an extension as text":                 {"kind": "deployment", "extension_days": []any{"1"}},
	} {
		body["name"], body["unified_job_template"] = uniq("bad"), prov
		if r := a.post("catalog_items/", body); r.Status != 400 {
			t.Errorf("%s: %d %s", what, r.Status, r.Raw)
		}
	}
	item := must(t, a.post("catalog_items/", map[string]any{
		"name": uniq("lab"), "unified_job_template": prov, "kind": "deployment", "max_lifetime_days": 10, "default_lifetime_days": 2,
		"extension_days": []any{1, 3, 5}, "max_extensions": 2, "teardown_template": teardown, "deployment_name": "answers.name",
		"notification_template": newEmailTemplate(t, a, f.org, emailConfig(s.port, nil), nil),
		"approval_rules":        []any{map[string]any{"name": "Long", "condition": `kind == "extend" && request.lifetime_days > 3`, "approvers": "team_admins"}},
	}), 201).id()
	must(t, a.post(fmt.Sprintf("catalog_items/%d/notification_templates_reminders/", item), map[string]any{"id": webhook}), 204)

	// It expires at the time its requester chose.
	req := must(t, f.requester.post(fmt.Sprintf("catalog_items/%d/request/", item), map[string]any{"answers": map[string]any{"name": "lab"}, "lifetime_days": 3}), 201).Body
	if req = waitRequest(t, a, req["id"]); req["status"] != "successful" {
		t.Fatalf("provisioning: %v", req)
	}
	waitFor(t, "the deployment", func() bool { return f.get(t, req)["deployment"] != nil })
	id := f.get(t, req)["deployment"]
	dep := fmt.Sprintf("deployments/%v/", id)
	start := parseAPITime(t, req["created"])
	expiry := func() time.Time { return parseAPITime(t, must(t, f.requester.get(dep), 200).Body["expires_at"]) }
	if got := expiry(); !got.Equal(start.Add(72 * time.Hour)) {
		t.Errorf("expires at %v, want %v", got, start.Add(72*time.Hour))
	}

	// The owners see which extensions are left and who approves each.
	opts := must(t, f.requester.get(dep+"extend/"), 200).Body
	steps := opts["steps"].([]any)
	if len(steps) != 3 || opts["extensions"] != float64(0) || !parseAPITime(t, opts["until"]).Equal(start.Add(240*time.Hour)) {
		t.Fatalf("extensions: %v", opts)
	}
	five := steps[2].(map[string]any)
	if one := steps[0].(map[string]any); len(one["approvals"].([]any)) != 0 || one["refused"] != "" {
		t.Errorf("one day: %v", one)
	}
	if apps := five["approvals"].([]any); len(apps) != 1 || apps[0].(map[string]any)["approvers"].([]any)[0].(map[string]any)["username"] != f.lead.user {
		t.Errorf("five days: %v", five)
	}

	// A short extension applies at once.
	if r := f.requester.post(dep+"extend/", map[string]any{"days": 2}); r.Status != 400 || !strings.Contains(r.Raw, "1, 3 or 5 days") {
		t.Errorf("an extension the item doesn't list: %d %s", r.Status, r.Raw)
	}
	must(t, f.p1.post(dep+"extend/", map[string]any{"days": 1}), 403)
	ext := must(t, f.requester.post(dep+"extend/", map[string]any{"days": 1, "reason": "demo on Friday"}), 201).Body
	if ext["status"] != "successful" || ext["kind"] != "extend" || ext["lifetime_days"] != float64(1) || ext["deployment"] != id {
		t.Errorf("a short extension: %v", ext)
	}
	if got := expiry(); !got.Equal(start.Add(96 * time.Hour)) {
		t.Errorf("extended to %v", got)
	}
	activity := must(t, a.get(dep+"activity_stream/?operation=update"), 200)
	if !strings.Contains(activity.Raw, `"extensions":[0,1]`) {
		t.Errorf("the extension's activity: %s", activity.Raw)
	}

	// A long one waits for the item's rule.
	long := must(t, f.requester.post(dep+"extend/", map[string]any{"days": 5, "reason": "the demo moved"}), 201).Body
	if long["status"] != "awaiting_approval" || len(approvalIDs(long)) != 1 {
		t.Fatalf("a long extension: %v", long)
	}
	if got := expiry(); !got.Equal(start.Add(96 * time.Hour)) {
		t.Errorf("extended before approval: %v", got)
	}
	if r := f.requester.post(dep+"extend/", map[string]any{"days": 1}); r.Status != 400 || !strings.Contains(r.Raw, "hasn't finished") {
		t.Errorf("an extension while one waits: %d %s", r.Status, r.Raw)
	}
	must(t, approve(f.lead, approvalIDs(long)[0], ""), 204)
	if long = waitRequest(t, a, long["id"]); long["status"] != "successful" {
		t.Fatalf("the approved extension: %v", long)
	}
	if got := expiry(); !got.Equal(start.Add(216 * time.Hour)) {
		t.Errorf("approved extension to %v", got)
	}

	// The caps refuse one more: on the number of extensions, then on the
	// whole lifetime.
	if r := f.requester.post(dep+"extend/", map[string]any{"days": 1}); r.Status != 400 || !strings.Contains(r.Raw, "2 of 2 times") {
		t.Errorf("a third extension: %d %s", r.Status, r.Raw)
	}
	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"max_extensions": 5}), 200)
	if r := f.requester.post(dep+"extend/", map[string]any{"days": 3}); r.Status != 400 || !strings.Contains(r.Raw, "10 days at most") {
		t.Errorf("past the longest lifetime: %d %s", r.Status, r.Raw)
	}
	must(t, f.requester.post(dep+"extend/", map[string]any{"days": 1}), 201)
	if got := expiry(); !got.Equal(start.Add(240 * time.Hour)) {
		t.Errorf("extended to the longest: %v", got)
	}

	// Past the caps, only the item's admins set when it expires.
	if r := f.requester.patch(dep, map[string]any{"expires_at": time.Now().Add(23 * time.Hour).Format(time.RFC3339)}); r.Status != 400 {
		t.Errorf("an owner set the expiry: %d %s", r.Status, r.Raw)
	}
	must(t, a.patch(dep, map[string]any{"expires_at": time.Now().Add(-time.Minute).Format(time.RFC3339)}), 400)

	// The owners are reminded 24 hours, then 2 hours before.
	link := fmt.Sprintf("/portal/deployments/%v", id)
	must(t, a.patch(dep, map[string]any{"expires_at": time.Now().Add(23 * time.Hour).Format(time.RFC3339)}), 200)
	m := nextMail(t, s, "expires in 24 hours")
	if strings.Join(m.To, ",") != f.requester.user+"@example.com,"+f.lead.user+"@example.com" || !strings.Contains(m.Body, link) || !strings.Contains(m.Body, "extend it") {
		t.Errorf("the 24-hour reminder: %v %q", m.To, m.Body)
	}
	hooks := rec.wait(t, 1)
	if b := hooks[0].Body; b["hours_left"] != float64(24) || !strings.HasSuffix(fmt.Sprint(b["deployment"].(map[string]any)["url"]), link) ||
		b["deployment"].(map[string]any)["owner"] != f.requester.user {
		t.Errorf("the 24-hour webhook: %v", b)
	}
	must(t, a.patch(dep, map[string]any{"expires_at": time.Now().Add(90 * time.Minute).Format(time.RFC3339)}), 200)
	nextMail(t, s, "expires in 2 hours")
	if b := rec.wait(t, 2)[1].Body; b["hours_left"] != float64(2) {
		t.Errorf("the 2-hour webhook: %v", b)
	}

	// At expiry, an extension that waits is canceled, and inside a freeze,
	// the teardown waits when the item says so.
	held := must(t, f.requester.post(dep+"extend/", map[string]any{"days": 5}), 201).Body
	if held["status"] != "awaiting_approval" {
		t.Fatalf("a held extension: %v", held)
	}
	from := time.Now().UTC().Add(-10 * time.Minute).Format("20060102T150405Z")
	must(t, a.post("maintenance_windows/", map[string]any{"name": uniq("freeze"), "organization": f.org, "kind": "freeze",
		"rrule": "DTSTART:" + from + " RRULE:FREQ=DAILY", "duration": 3600, "catalog_items": []any{item}}), 201)
	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"expiry_waits_for_freeze": true}), 200)
	expires := time.Now().Add(3 * time.Second)
	must(t, a.patch(dep, map[string]any{"expires_at": expires.Format(time.RFC3339Nano)}), 200)
	time.Sleep(time.Until(expires) + 3*time.Second)
	if d := must(t, a.get(dep), 200).Body; d["status"] != "active" {
		t.Fatalf("expired inside the freeze: %v", d)
	}
	must(t, a.patch(fmt.Sprintf("catalog_items/%d/", item), map[string]any{"expiry_waits_for_freeze": false}), 200)
	var d map[string]any
	waitFor(t, "the expiry's teardown", func() bool {
		d = must(t, a.get(dep), 200).Body
		return d["status"] == "deleted"
	})
	if h := f.get(t, held); h["status"] != "canceled" || !strings.Contains(fmt.Sprint(h["explanation"]), "expired") {
		t.Errorf("the extension that waited: %v", h)
	}
	del := must(t, a.get(dep+"requests/?kind=delete"), 200).results()
	if len(del) != 1 || del[0]["reason"] != "It expired." || del[0]["summary_fields"].(map[string]any)["created_by"] != nil {
		t.Fatalf("the expiry's request: %v", del)
	}
	if got := readOut(t, out, fmt.Sprintf("teardown-%v", del[0]["job"])); got != "Init-lab-Secret db.lab.internal" {
		t.Errorf("the teardown got %q", got)
	}
	nextMail(t, s, "lab is deleted")
	var sent []any
	for _, h := range rec.wait(t, 3) {
		sent = append(sent, h.Body["hours_left"])
	}
	if fmt.Sprint(sent) != "[24 2 2]" {
		t.Errorf("reminders, one per expiry and time: %v", sent)
	}
}
