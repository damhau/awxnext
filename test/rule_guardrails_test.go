package test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/eventstreams"
)

var alertSeq atomic.Int64

// fire posts a fresh DiskFull alert for an instance and waits for the rules
// to decide it.
func (s eventStream) fire(t *testing.T, instance string) {
	t.Helper()
	n := alertSeq.Add(1)
	s.alert(t, 1, testAlert{"DiskFull", instance, time.Unix(1_800_000_000+n, 0).UTC().Format(time.RFC3339), fmt.Sprintf("g%d", n)})
	waitRules(t, s.id)
}

func ruleDecisions(t *testing.T, a *client, rule int64) []map[string]any {
	t.Helper()
	return must(t, a.get(fmt.Sprintf("event_rules/%d/decisions/?order_by=sequence&page_size=200", rule)), 200).results()
}

func lastDecision(t *testing.T, a *client, rule int64) map[string]any {
	t.Helper()
	ds := ruleDecisions(t, a, rule)
	if len(ds) == 0 {
		t.Fatalf("rule %d decided nothing", rule)
	}
	return ds[len(ds)-1]
}

// wantOutcome checks a decision's outcome, guardrail and reason.
func wantOutcome(t *testing.T, d map[string]any, outcome, guardrail, reason string) {
	t.Helper()
	if d["outcome"] != outcome || d["guardrail"] != guardrail || !strings.Contains(fmt.Sprint(d["reason"]), reason) {
		t.Errorf("decision %v %v %q, want %s %q %q (details %v)", d["outcome"], d["guardrail"], d["reason"], outcome, guardrail, reason, d["details"])
	}
}

// runningJob inserts a job that runs on an inventory with a limit, launched
// by a rule unless rule is 0. It is canceled when the test ends.
func runningJob(t *testing.T, inventory int64, limit string, rule int64) int64 {
	t.Helper()
	var by *int64
	if rule != 0 {
		by = &rule
	}
	var id int64
	if err := app.DB.QueryRow(t.Context(), `INSERT INTO unified_jobs (type, name, status, inventory_id, "limit", event_rule_id, controller_node, started)
		VALUES ('job', 'busy', 'running', $1, $2, $3, 'elsewhere', now()) RETURNING id`, inventory, limit, by).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endJob(t, id) })
	return id
}

func endJob(t *testing.T, id int64) {
	if _, err := app.DB.Exec(context.Background(), "UPDATE unified_jobs SET status = 'canceled', finished = now() WHERE id = $1 AND status = 'running'", id); err != nil {
		t.Error(err)
	}
}

// rrule starts a daily rule at now+offset.
func rrule(offset time.Duration) string {
	return "DTSTART:" + time.Now().UTC().Add(offset).Format("20060102T150405Z") + " RRULE:FREQ=DAILY;INTERVAL=1"
}

// A launch is held back beyond the rule's concurrent runs, on a host a
// running job uses, and beyond its share of a group under remediation. The
// rule starts in shadow mode: the guardrails apply, and nothing launches.
func TestEventRuleBlastRadius(t *testing.T) {
	slow(t)
	f := newLaunchFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	jtName := a.get(fmt.Sprintf("job_templates/%d/", jt)).Body["name"]
	rule := must(t, a.post("event_rules/", map[string]any{"name": uniq("radius"), "organization": f.org, "streams": []int64{am.id},
		"filter": "true", "unified_job_template": jt, "limit": "event.data.labels.instance", "max_concurrent_runs": 1}), 201)
	rid := rule.id()
	if rule.Body["mode"] != "shadow" {
		t.Fatalf("a new rule's mode: %v", rule.Body["mode"])
	}
	rurl := fmt.Sprintf("event_rules/%d/", rid)

	mine := runningJob(t, f.inv, "h3", rid)
	am.fire(t, "h1")
	wantOutcome(t, lastDecision(t, a, rid), "held", "max_concurrent_runs", "at its limit of 1 run at once")
	endJob(t, mine)
	am.fire(t, "h1")
	wantOutcome(t, lastDecision(t, a, rid), "shadow", "", fmt.Sprintf("Shadow mode: the rule would have launched job template %s on h1.", jtName))

	must(t, a.patch(rurl, map[string]any{"max_concurrent_runs": 0, "host_lock": true}), 200)
	other := runningJob(t, f.inv, "h1,h3", 0)
	am.fire(t, "h1")
	d := lastDecision(t, a, rid)
	wantOutcome(t, d, "held", "host_lock", fmt.Sprintf("Job %d, still running, uses h1.", other))
	if det := d["details"].(map[string]any); fmt.Sprint(det["hosts"]) != "[h1]" || fmt.Sprint(det["jobs"]) != fmt.Sprintf("[%d]", other) {
		t.Errorf("host lock details: %v", det)
	}
	am.fire(t, "h2")
	wantOutcome(t, lastDecision(t, a, rid), "shadow", "", "would have launched")
	// The hosts of a job in another organization's inventory are others.
	elsewhere := newLaunchFixture(t)
	runningJob(t, elsewhere.inv, "h2", 0)
	am.fire(t, "h2")
	wantOutcome(t, lastDecision(t, a, rid), "shadow", "", "would have launched")
	endJob(t, other)

	// Group web holds h1 and h2: 70% of it is one host, of all of it two.
	web := must(t, a.post("groups/", map[string]any{"name": "web", "inventory": f.inv}), 201).id()
	for _, h := range []string{"h1", "h2"} {
		hid := one(t, a, fmt.Sprintf("hosts/?inventory=%d&name=%s", f.inv, h))["id"]
		must(t, a.post(fmt.Sprintf("groups/%d/hosts/", web), map[string]any{"id": hid}), 204)
	}
	must(t, a.patch(rurl, map[string]any{"host_lock": false, "max_group_share": 70}), 200)
	runningJob(t, f.inv, "h1", rid)
	am.fire(t, "h2")
	d = lastDecision(t, a, rid)
	wantOutcome(t, d, "held", "group_share", "put 2 of the 2 hosts of group web under remediation at once, more than 70%")
	if det := d["details"].(map[string]any); det["group"] != "web" || det["allowed"] != float64(1) || det["under_remediation"] != float64(2) {
		t.Errorf("group share details: %v", det)
	}
	am.fire(t, "h3")
	wantOutcome(t, lastDecision(t, a, rid), "shadow", "", "would have launched")
	if n := must(t, a.get(fmt.Sprintf("jobs/?event_rule=%d&not__name=busy", rid)), 200).count(); n != 0 {
		t.Errorf("shadow mode launched %d jobs", n)
	}

	// A rule that doesn't launch has no blast radius.
	must(t, a.post("event_rules/", map[string]any{"name": uniq("r"), "organization": f.org, "streams": []int64{am.id}, "filter": "true",
		"action": "post_event", "target_stream": am.id, "event_type": `"x"`, "host_lock": true}), 400)
}

// Inside a maintenance window or a change freeze, a rule's action is held
// back: one listing the rule, one of its organization, or one listing a
// group the launch runs on.
func TestMaintenanceWindows(t *testing.T) {
	slow(t)
	f := newLaunchFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("win"), "organization": f.org, "streams": []int64{am.id},
		"filter": "true", "unified_job_template": jt, "limit": "event.data.labels.instance"}), 201).id()
	window := func(kv ...any) resp {
		t.Helper()
		body := map[string]any{"name": uniq("w"), "organization": f.org, "rrule": rrule(-time.Hour), "duration": 7200}
		for i := 0; i+1 < len(kv); i += 2 {
			body[kv[i].(string)] = kv[i+1]
		}
		return a.post("maintenance_windows/", body)
	}
	off := func(id int64) {
		must(t, a.patch(fmt.Sprintf("maintenance_windows/%d/", id), map[string]any{"enabled": false}), 200)
	}

	// One that ended an hour ago, and one that starts in an hour.
	past := must(t, window("event_rules", []int64{rid}, "rrule", rrule(-3*time.Hour)), 201)
	if past.Body["active"] != false || past.Body["next_start"] == nil {
		t.Errorf("past window: %s", past.Raw)
	}
	must(t, window("event_rules", []int64{rid}, "rrule", rrule(time.Hour)), 201)
	am.fire(t, "h1")
	wantOutcome(t, lastDecision(t, a, rid), "shadow", "", "would have launched")

	w := must(t, window("event_rules", []int64{rid}), 201)
	if w.Body["active"] != true || w.Body["active_until"] == nil || w.Body["kind"] != "maintenance" {
		t.Errorf("window: %s", w.Raw)
	}
	am.fire(t, "h1")
	d := lastDecision(t, a, rid)
	wantOutcome(t, d, "held", "maintenance_window", fmt.Sprintf("Inside maintenance window %s, until ", w.Body["name"]))
	if win := d["details"].(map[string]any)["window"].(map[string]any); win["id"] != float64(w.id()) {
		t.Errorf("window details: %v", d["details"])
	}
	off(w.id())

	freeze := must(t, window("kind", "freeze"), 201)
	am.fire(t, "h1")
	wantOutcome(t, lastDecision(t, a, rid), "held", "change_freeze", fmt.Sprintf("Inside change freeze %s", freeze.Body["name"]))
	// A window of another organization covers nothing here.
	otherOrg := newOrg(t, a)
	must(t, a.post("maintenance_windows/", map[string]any{"name": uniq("w"), "organization": otherOrg, "rrule": rrule(-time.Hour), "duration": 7200}), 201)
	off(freeze.id())

	web := must(t, a.post("groups/", map[string]any{"name": "web", "inventory": f.inv}), 201).id()
	hid := one(t, a, fmt.Sprintf("hosts/?inventory=%d&name=h1", f.inv))["id"]
	must(t, a.post(fmt.Sprintf("groups/%d/hosts/", web), map[string]any{"id": hid}), 204)
	gw := must(t, window("groups", []int64{web}), 201).id()
	am.fire(t, "h1")
	d = lastDecision(t, a, rid)
	wantOutcome(t, d, "held", "maintenance_window", "Inside maintenance window")
	if fmt.Sprint(d["details"].(map[string]any)["hosts"]) != "[h1]" {
		t.Errorf("group window details: %v", d["details"])
	}
	am.fire(t, "h2")
	wantOutcome(t, lastDecision(t, a, rid), "shadow", "", "would have launched")

	// With window_action approval, the action waits for approval instead.
	must(t, a.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"window_action": "approval"}), 200)
	am.fire(t, "h1")
	wantOutcome(t, lastDecision(t, a, rid), "shadow", "maintenance_window", "would have asked for approval to launch")

	// Deleting a group or a rule takes it out of the windows listing it.
	wantDependents(t, "group", must2(usedBy(t, a, fmt.Sprintf("groups/%d/", web))), fmt.Sprintf("maintenance_windows/%d groups cleared", gw))
	must(t, a.delete(fmt.Sprintf("groups/%d/", web)), 204)
	if g := must(t, a.get(fmt.Sprintf("maintenance_windows/%d/", gw)), 200).Body["groups"]; fmt.Sprint(g) != "[]" {
		t.Errorf("groups after delete: %v", g)
	}

	refuse := func(code int, field, contains string, kv ...any) {
		t.Helper()
		r := must(t, window(kv...), code)
		if !strings.Contains(fmt.Sprint(r.Body[field]), contains) {
			t.Errorf("%s: %s", field, r.Raw)
		}
	}
	refuse(400, "rrule", "DTSTART", "rrule", "RRULE:FREQ=DAILY")
	refuse(400, "duration", "", "duration", 30)
	refuse(400, "kind", "", "kind", "lunch")
	refuse(400, "event_rules", "does not exist", "event_rules", []int64{1 << 40})
	elsewhere := must(t, a.post("event_rules/", map[string]any{"name": uniq("r"), "organization": otherOrg, "streams": []int64{newAlertStream(t, a, otherOrg).id},
		"filter": "true", "action": "post_event", "target_stream": am.id, "event_type": `"x"`, "enabled": false}), 201).id()
	refuse(400, "event_rules", "not of the window's organization", "event_rules", []int64{elsewhere})

	// Holding a rule back takes the right to change it.
	user, uid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Admin"), "user": uid, "object_id": otherOrg}), 201)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "EventRule Read"), "user": uid, "object_id": rid}), 201)
	r := must(t, user.post("maintenance_windows/", map[string]any{"name": uniq("w"), "organization": otherOrg, "rrule": rrule(0), "event_rules": []int64{rid}}), 403)
	if !strings.Contains(r.Raw, "may not hold back the actions of event rule") {
		t.Errorf("refusal: %s", r.Raw)
	}
	if n := len(ruleJobs(t, a, rid)); n != 0 {
		t.Errorf("%d jobs", n)
	}
}

// K firings of one key within the breaker's window trip its circuit
// breaker: the rule acts no more for the key, and notifies, until a person
// resets it.
func TestEventRuleBreaker(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	am := newAlertStream(t, a, org)
	target := newEventStream(t, a, org, streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "t2"}), eventstreams.TypeCloudEvents, nil)
	rec, hook := newHookServer(t)
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("loop"), "organization": org, "streams": []int64{am.id}, "mode": "auto",
		"filter": "true", "key": "event.data.labels.instance", "action": "post_event", "target_stream": target.id, "event_type": `"remediate"`,
		"breaker_firings": 2, "breaker_window": 3600, "breaker_notification_template": nt}), 201).id()

	for _, h := range []string{"web-01", "web-01", "web-02", "web-01"} {
		am.fire(t, h)
	}
	ds := ruleDecisions(t, a, rid)
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%v %v", d["key"], d["outcome"]))
	}
	if want := []string{"web-01 posted", "web-01 posted", "web-02 posted", "web-01 held"}; !slices.Equal(got, want) {
		t.Fatalf("decisions %v, want %v", got, want)
	}
	wantOutcome(t, ds[3], "held", "circuit_breaker", `The circuit breaker of key "web-01" is open: the key fired 2 times within 3600 seconds.`)
	reqs := rec.wait(t, 1)
	if b := reqs[0].Body; b["key"] != "web-01" || b["firings"] != float64(2) || !strings.HasSuffix(fmt.Sprint(b["reset"]), fmt.Sprintf("/api/v2/event_rules/%d/reset_breaker/", rid)) {
		t.Errorf("breaker notification: %v", b)
	}
	breakers := must(t, a.get(fmt.Sprintf("event_rules/%d/breakers/", rid)), 200).results()
	if len(breakers) != 1 || breakers[0]["key"] != "web-01" || breakers[0]["tripped_at"] == nil || breakers[0]["firings"] != float64(2) {
		t.Fatalf("breakers: %v", breakers)
	}

	stranger, _ := newUser(t, a)
	if code := stranger.post(fmt.Sprintf("event_rules/%d/reset_breaker/", rid), nil).Status; code != 403 && code != 404 {
		t.Errorf("a stranger reset the breaker: %d", code)
	}
	must(t, a.post(fmt.Sprintf("event_rules/%d/reset_breaker/", rid), map[string]any{"key": 3}), 400)
	if r := must(t, a.post(fmt.Sprintf("event_rules/%d/reset_breaker/", rid), map[string]any{"key": "web-01"}), 200); r.Body["reset"] != float64(1) {
		t.Errorf("reset: %s", r.Raw)
	}
	b := must(t, a.get(fmt.Sprintf("event_rule_breakers/?rule=%d", rid)), 200).results()[0]
	if b["tripped_at"] != nil || b["reset_at"] == nil || b["reset_by"] == nil {
		t.Errorf("breaker after reset: %v", b)
	}
	am.fire(t, "web-01")
	wantOutcome(t, lastDecision(t, a, rid), "posted", "", "")
	am.fire(t, "web-01")
	am.fire(t, "web-01")
	wantOutcome(t, lastDecision(t, a, rid), "held", "circuit_breaker", "is open")
	if n := len(target.events(t, a)); n != 5 {
		t.Errorf("%d events posted", n)
	}
	rec.wait(t, 2)

	// Shadow mode has breakers of its own, which don't notify, and each
	// time the rule enters shadow mode they start afresh.
	rurl := fmt.Sprintf("event_rules/%d/", rid)
	must(t, a.patch(rurl, map[string]any{"mode": "shadow"}), 200)
	for _, want := range []string{"shadow", "shadow", "held"} {
		am.fire(t, "web-01")
		wantOutcome(t, lastDecision(t, a, rid), want, map[string]string{"held": "circuit_breaker"}[want], "")
	}
	must(t, a.patch(rurl, map[string]any{"mode": "auto"}), 200)
	must(t, a.patch(rurl, map[string]any{"mode": "shadow"}), 200)
	am.fire(t, "web-01")
	wantOutcome(t, lastDecision(t, a, rid), "shadow", "", "")
	if n := len(target.events(t, a)); n != 5 {
		t.Errorf("%d events posted", n)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(rec.wait(t, 2)); n != 2 {
		t.Errorf("%d breaker notifications", n)
	}
}

// Shadow mode launches nothing and records what the rule would have done;
// its throttle keys and breakers are its own, and acting takes the
// EventRule Automate role.
func TestEventRuleShadow(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true, "ask_limit_on_launch": true})
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("shadow"), "organization": f.org, "streams": []int64{am.id},
		"filter": "true", "key": "event.data.labels.instance", "throttle": "once_within", "throttle_window": 3600,
		"unified_job_template": jt, "limit": `"h1"`, "extra_vars": map[string]any{"target": "event.data.labels.instance"},
		"breaker_firings": 2}), 201).id()
	rurl := fmt.Sprintf("event_rules/%d/", rid)

	am.fire(t, "web-01")
	am.fire(t, "web-01")
	d := ruleDecisions(t, a, rid)
	if len(d) != 2 {
		t.Fatalf("decisions: %v", d)
	}
	wantOutcome(t, d[0], "shadow", "", "would have launched job template")
	wantOutcome(t, d[1], "throttled", "", "")
	if d[0]["mode"] != "shadow" || d[0]["fired"] != true {
		t.Errorf("shadow decision: %v", d[0])
	}
	act := d[0]["details"].(map[string]any)["action"].(map[string]any)
	if act["limit"] != "h1" || fmt.Sprint(act["extra_vars"]) != "map[target:web-01]" || act["launch"].(map[string]any)["id"] != float64(jt) {
		t.Errorf("what it would have done: %v", act)
	}
	if n := len(ruleJobs(t, a, rid)); n != 0 {
		t.Fatalf("shadow mode launched %d jobs", n)
	}

	// Auto mode takes the EventRule Automate role.
	user, uid := newUser(t, a)
	for role, obj := range map[string]int64{"EventRule Admin": rid, "JobTemplate Execute": jt, "EventStream Read": am.id} {
		must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, role), "user": uid, "object_id": obj}), 201)
	}
	if r := must(t, user.patch(rurl, map[string]any{"mode": "auto"}), 403); !strings.Contains(r.Raw, "EventRule Automate") {
		t.Errorf("refusal: %s", r.Raw)
	}
	must(t, user.patch(rurl, map[string]any{"mode": "approval"}), 200)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "EventRule Automate"), "user": uid, "object_id": rid}), 201)
	must(t, user.patch(rurl, map[string]any{"mode": "auto"}), 200)

	// The shadow key doesn't throttle the rule now that it acts.
	am.fire(t, "web-01")
	wantOutcome(t, lastDecision(t, a, rid), "launched", "", "")
	jobs := ruleJobs(t, a, rid)
	if len(jobs) != 1 {
		t.Fatalf("jobs: %v", jobs)
	}
	mustStatus(t, f, waitJob(t, a, fmt.Sprintf("jobs/%v/", jobs[0]["id"])), "successful")
	if n := must(t, a.get(fmt.Sprintf("event_rule_breakers/?rule=%d", rid)), 200).count(); n != 0 {
		t.Errorf("%d breakers", n)
	}
}

// In approval mode each action waits for a workflow approval: approved, it
// runs; denied or timed out, it doesn't.
func TestEventRuleApproval(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("ask"), "organization": f.org, "streams": []int64{am.id},
		"filter": "true", "key": "event.data.labels.instance", "unified_job_template": jt, "limit": "event.data.labels.instance",
		"mode": "approval"}), 201).id()
	approver, aid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "EventRule Approve"), "user": aid, "object_id": rid}), 201)
	reader, readerID := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "EventRule Read"), "user": readerID, "object_id": rid}), 201)
	stranger, _ := newUser(t, a)
	rec, hook := newHookServer(t)
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	must(t, a.post(fmt.Sprintf("organizations/%d/notification_templates_approvals/", f.org), map[string]any{"id": nt}), 204)

	pending := func() map[string]any {
		t.Helper()
		d := lastDecision(t, a, rid)
		if d["outcome"] != "awaiting_approval" || d["approval"] == nil {
			t.Fatalf("decision %v", d)
		}
		return d
	}
	waitOutcome := func(seq any) map[string]any {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			d := lastDecision(t, a, rid)
			if d["sequence"] == seq && d["outcome"] != "awaiting_approval" {
				return d
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("decision %v is still waiting", seq)
		return nil
	}

	am.fire(t, "h1")
	d := pending()
	approval := int64(d["approval"].(float64))
	ap := must(t, approver.get(fmt.Sprintf("workflow_approvals/%d/", approval)), 200).Body
	if ap["status"] != "pending" || ap["can_approve_or_deny"] != true || !strings.Contains(fmt.Sprint(ap["name"]), "launch job template") ||
		ap["summary_fields"].(map[string]any)["event_rule"].(map[string]any)["id"] != float64(rid) {
		t.Errorf("approval: %v", ap)
	}
	if n := must(t, approver.get(fmt.Sprintf("workflow_approvals/?event_rule=%d&status=pending", rid)), 200).count(); n != 1 {
		t.Errorf("the approver sees %d approvals", n)
	}
	if r := must(t, reader.get(fmt.Sprintf("workflow_approvals/%d/", approval)), 200); r.Body["can_approve_or_deny"] != false {
		t.Errorf("a reader may approve: %s", r.Raw)
	}
	must(t, reader.post(fmt.Sprintf("workflow_approvals/%d/approve/", approval), nil), 403)
	must(t, stranger.get(fmt.Sprintf("workflow_approvals/%d/", approval)), 403)
	if n := len(ruleJobs(t, a, rid)); n != 0 {
		t.Fatalf("%d jobs before the approval", n)
	}
	must(t, approver.post(fmt.Sprintf("workflow_approvals/%d/approve/", approval), nil), 204)
	d = waitOutcome(d["sequence"])
	wantOutcome(t, d, "launched", "", "")
	if by := d["details"].(map[string]any)["approval"].(map[string]any); by["status"] != "successful" || by["by"] != approver.user {
		t.Errorf("approval details: %v", by)
	}
	jobs := ruleJobs(t, a, rid)
	if len(jobs) != 1 || jobs[0]["limit"] != "h1" {
		t.Fatalf("jobs: %v", jobs)
	}
	mustStatus(t, f, waitJob(t, a, fmt.Sprintf("jobs/%v/", jobs[0]["id"])), "successful")
	// Approved again, nothing runs twice.
	must(t, a.post(fmt.Sprintf("workflow_approvals/%d/approve/", approval), nil), 400)
	// The organization's approval notifications tell of it, as of a workflow's.
	var told []string
	for _, r := range rec.wait(t, 2) {
		told = append(told, fmt.Sprint(r.Body["approval_status"]))
		if !strings.HasSuffix(fmt.Sprint(r.Body["workflow_url"]), "/approvals") {
			t.Errorf("approval notification: %v", r.Body)
		}
	}
	if !slices.Equal(told, []string{"running", "approved"}) {
		t.Errorf("approval notifications: %v", told)
	}

	am.fire(t, "h2")
	d = pending()
	must(t, a.post(fmt.Sprintf("workflow_approvals/%v/deny/", d["approval"]), nil), 204)
	wantOutcome(t, waitOutcome(d["sequence"]), "denied", "", "Denied by admin.")

	must(t, a.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"approval_timeout": 1}), 200)
	am.fire(t, "h3")
	d = pending()
	wantOutcome(t, waitOutcome(d["sequence"]), "timed_out", "", "The approval timed out.")

	// Disabling the rule cancels the approvals it waits for.
	am.fire(t, "h1")
	d = pending()
	must(t, a.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"enabled": false}), 200)
	wantOutcome(t, waitOutcome(d["sequence"]), "canceled", "", "")
	if s := a.get(fmt.Sprintf("workflow_approvals/%v/", d["approval"])).Body["status"]; s != "canceled" {
		t.Errorf("approval of a disabled rule: %v", s)
	}
	if n := len(ruleJobs(t, a, rid)); n != 1 {
		t.Errorf("%d jobs", n)
	}
}

// A GitOps source declares maintenance windows, their rules by name and
// their groups by inventory and name.
func TestMaintenanceWindowGitOps(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	stream := newAlertStream(t, a, f.org)
	streamName := a.get(fmt.Sprintf("event_streams/%d/", stream.id)).Body["name"].(string)
	jt := f.template(t, "site.yml", nil)
	jtName := a.get(fmt.Sprintf("job_templates/%d/", jt)).Body["name"].(string)
	invName := a.get(fmt.Sprintf("inventories/%d/", f.inv)).Body["name"].(string)
	web := must(t, a.post("groups/", map[string]any{"name": "web", "inventory": f.inv}), 201).id()
	doc := fmt.Sprintf(`kind: EventRule
name: Disk full
spec:
  streams: [%s]
  filter: "true"
  template: {kind: JobTemplate, name: %s}
  host_lock: true
---
kind: MaintenanceWindow
name: Patch night
description: the web servers are patched
spec:
  kind: freeze
  rrule: DTSTART;TZID=Europe/Zurich:20261107T220000 RRULE:FREQ=WEEKLY;BYDAY=SA
  duration: 7200
  event_rules: [Disk full]
  groups:
    - {inventory: %s, name: web}
`, streamName, jtName, invName)
	repo := gitRepo(t, map[string]string{"site.yml": helloPlaybook, "gitops/rules.yml": doc})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": f.org, "scm_type": "git", "scm_url": "file://" + repo}), 201).id()
	waitProjectSynced(t, a, proj, "")
	sid := must(t, a.post("gitops_sources/", map[string]any{"name": uniq("src"), "organization": f.org, "project": proj, "path": "gitops",
		"sync_on_project_update": false}), 201).id()
	rep := must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), nil), 202)
	if summary(rep, "create") != 2 || summary(rep, "error") != 0 {
		t.Fatalf("sync: %s", rep.Raw)
	}
	rule := one(t, a, fmt.Sprintf("event_rules/?organization=%d&name=%s", f.org, "Disk%20full"))
	w := one(t, a, fmt.Sprintf("maintenance_windows/?organization=%d&name=%s", f.org, "Patch%20night"))
	if w["managed_by"] != fmt.Sprintf("gitops:%d", sid) || w["kind"] != "freeze" || w["duration"] != float64(7200) ||
		fmt.Sprint(w["event_rules"]) != fmt.Sprintf("[%v]", rule["id"]) || fmt.Sprint(w["groups"]) != fmt.Sprintf("[%d]", web) {
		t.Errorf("window: %v", w)
	}
	if rule["mode"] != "shadow" || rule["host_lock"] != true {
		t.Errorf("rule: %v", rule)
	}
	rep = must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), nil), 202)
	if summary(rep, "update") != 0 || summary(rep, "unchanged") != 2 {
		t.Errorf("second sync: %s", rep.Raw)
	}
	if n := must(t, a.get(fmt.Sprintf("maintenance_windows/%v/history/", w["id"])), 200).count(); n != 1 {
		t.Errorf("%d versions", n)
	}
}
