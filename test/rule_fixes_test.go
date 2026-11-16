package test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/eventstreams"
)

// resolve posts a resolved DiskFull alert for an instance and waits for the
// rules to decide it.
func (s eventStream) resolve(t *testing.T, instance string) {
	t.Helper()
	n := alertSeq.Add(1)
	b, _ := json.Marshal(map[string]any{"version": "4", "groupKey": "{}:{}", "status": "resolved", "receiver": "awxnext",
		"externalURL": "http://alertmanager:9093", "groupLabels": map[string]any{}, "commonLabels": map[string]any{},
		"alerts": []map[string]any{{"status": "resolved", "fingerprint": fmt.Sprintf("g%d", n),
			"startsAt": time.Unix(1_800_000_000+n, 0).UTC().Format(time.RFC3339), "endsAt": time.Now().UTC().Format(time.RFC3339),
			"labels": map[string]any{"alertname": "DiskFull", "instance": instance, "severity": "critical"}}}})
	code, body, _ := s.post(t, string(b), headers("Authorization", "Bearer tok", "Content-Type", "application/json"))
	if code != 202 || body["stored"] != float64(1) {
		t.Fatalf("posting a resolved alert: %d %v", code, body)
	}
	waitRules(t, s.id)
}

// waitChecks waits for a rule to have n settled checks, and returns its
// checks in order.
func waitChecks(t *testing.T, a *client, rule int64, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var cs []map[string]any
	for time.Now().Before(deadline) {
		cs = must(t, a.get(fmt.Sprintf("event_rules/%d/checks/?order_by=id", rule)), 200).results()
		settled := 0
		for _, c := range cs {
			if c["state"] == "settled" {
				settled++
			}
		}
		if settled >= n {
			return cs
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("rule %d settled fewer than %d checks: %v", rule, n, cs)
	return nil
}

func wantCheck(t *testing.T, c map[string]any, key, outcome, reason string) {
	t.Helper()
	if c["key"] != key || c["outcome"] != outcome || c["state"] != "settled" || !strings.Contains(fmt.Sprint(c["reason"]), reason) {
		t.Errorf("check %v %v %v %q, want %s %s %q", c["key"], c["state"], c["outcome"], c["reason"], key, outcome, reason)
	}
}

func checksSummary(t *testing.T, a *client, rule int64) (any, map[string]any) {
	t.Helper()
	r := must(t, a.get(fmt.Sprintf("event_rules/%d/", rule)), 200).Body
	return r["success_rate"], r["summary_fields"].(map[string]any)["checks"].(map[string]any)
}

// A fix the alert's resolution follows is fixed; one it doesn't is not
// fixed and escalates: the rule's check notification, an approval before
// the key's next action, and one more firing for the circuit breaker. The
// success rate counts them.
func TestEventRuleFixByEvent(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	am := newAlertStream(t, a, org)
	target := newEventStream(t, a, org, streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "t2"}), eventstreams.TypeCloudEvents, nil)
	rec, hook := newHookServer(t)
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	body := map[string]any{"name": uniq("fix"), "organization": org, "streams": []int64{am.id}, "mode": "auto",
		"filter": `event.type == "alert.firing"`, "key": "event.data.labels.instance", "action": "post_event", "target_stream": target.id,
		"event_type": `"remediate"`, "breaker_firings": 3, "breaker_window": 3600,
		"check": "event", "check_condition": `event.type == "alert.resolved"`, "check_window": 2,
		"check_notification_template": nt, "check_approval": true}
	rule := must(t, a.post("event_rules/", body), 201)
	rid := rule.id()
	if rule.Body["success_rate"] != nil {
		t.Errorf("a new rule's success rate: %v", rule.Body["success_rate"])
	}

	am.fire(t, "web-01")
	d := lastDecision(t, a, rid)
	if d["outcome"] != "posted" || d["check_outcome"] != "pending" || d["check"] == nil {
		t.Fatalf("decision %v", d)
	}
	am.resolve(t, "web-01")
	cs := waitChecks(t, a, rid, 1)
	wantCheck(t, cs[0], "web-01", "fixed", "resolved the key")
	if cs[0]["resolved_by"] == nil || cs[0]["kind"] != "event" || cs[0]["action"] != "posted" || cs[0]["escalated"] != false {
		t.Errorf("check %v", cs[0])
	}
	if d := lastDecision(t, a, rid); d["check_outcome"] != "fixed" || d["check"] != cs[0]["id"] {
		t.Errorf("the decision after its check: %v", d)
	}

	am.fire(t, "web-02")
	cs = waitChecks(t, a, rid, 2)
	wantCheck(t, cs[1], "web-02", "not_fixed", "No event resolved the key within 2s after the action finished.")
	if cs[1]["escalated"] != true {
		t.Errorf("check %v", cs[1])
	}
	reqs := rec.wait(t, 1)
	if b := reqs[0].Body; b["key"] != "web-02" || b["check"].(map[string]any)["outcome"] != "not_fixed" {
		t.Errorf("check notification: %v", b)
	}

	am.fire(t, "web-02")
	d = lastDecision(t, a, rid)
	wantOutcome(t, d, "awaiting_approval", "check_escalation", fmt.Sprintf(`The rule's last check of key "web-02" (check %v) found the key not fixed: its next action waits for approval.`, cs[1]["id"]))
	must(t, a.post(fmt.Sprintf("workflow_approvals/%v/approve/", d["approval"]), nil), 204)
	cs = waitChecks(t, a, rid, 3)
	wantCheck(t, cs[2], "web-02", "not_fixed", "No event resolved")
	// Two firings and two fixes that didn't work trip the breaker.
	am.fire(t, "web-02")
	wantOutcome(t, lastDecision(t, a, rid), "held", "circuit_breaker", `the key fired, or its fix didn't work, 3 times within 3600 seconds`)

	am.fire(t, "web-01")
	wantOutcome(t, lastDecision(t, a, rid), "posted", "", "")
	am.resolve(t, "web-01")
	waitChecks(t, a, rid, 4)
	rate, sum := checksSummary(t, a, rid)
	if rate != 0.5 || sum["checked"] != float64(4) || sum["fixed"] != float64(2) || sum["not_fixed"] != float64(2) || sum["days"] != float64(30) {
		t.Errorf("success rate %v, checks %v", rate, sum)
	}
	if list := must(t, a.get(fmt.Sprintf("event_rules/?id=%d", rid)), 200).results(); list[0]["success_rate"] != 0.5 {
		t.Errorf("the list's success rate: %v", list[0]["success_rate"])
	}
	wantMetrics(t, fmt.Sprintf(`awxnext_event_rule_checks_total{rule="%d",outcome="fixed"} 2`, rid),
		fmt.Sprintf(`awxnext_event_rule_checks_total{rule="%d",outcome="not_fixed"} 2`, rid))
	if n := must(t, a.get(fmt.Sprintf("event_rule_checks/?rule=%d&outcome=fixed", rid)), 200).count(); n != 2 {
		t.Errorf("%d fixed checks", n)
	}
	stranger, _ := newUser(t, a)
	must(t, stranger.get(fmt.Sprintf("event_rule_checks/%v/", cs[0]["id"])), 403)
	if n := must(t, stranger.get("event_rule_checks/"), 200).count(); n != 0 {
		t.Errorf("a stranger sees %d checks", n)
	}

	// The backtest counts the firings the log shows resolved, without
	// escalating: a key not fixed waits for no approval.
	bt := must(t, a.post(fmt.Sprintf("event_rules/%d/backtest/", rid), map[string]any{"check_window": 3600}), 200).Body
	if c := bt["checks"].(map[string]any); c["checked"] != float64(2) || c["resolved"] != float64(2) || c["pending"] != float64(3) || c["resolved_rate"] != 1.0 {
		t.Errorf("backtest checks: %v (%v)", c, bt["outcomes"])
	}
	fixedDecisions := 0
	for _, d := range bt["decisions"].([]any) {
		if d := d.(map[string]any); d["check_outcome"] == "fixed" && d["resolved_by"] != nil {
			fixedDecisions++
		}
	}
	if fixedDecisions != 2 {
		t.Errorf("backtest decisions resolved: %d, want 2", fixedDecisions)
	}

	// The editor's check runs the check condition on the latest events.
	ck := must(t, a.post("event_rules/check/", map[string]any{"streams": []int64{am.id}, "filter": `event.type == "alert.firing"`,
		"key": "event.data.labels.instance", "action": "post_event", "event_type": `"x"`, "check": "event", "check_condition": `event.type == "alert.resolved"`}), 200).Body
	resolving := 0
	for _, e := range ck["events"].([]any) {
		if ev := e.(map[string]any); ev["resolves"] == true {
			resolving++
			if ev["key"] != "web-01" || ev["matched"] != false {
				t.Errorf("a resolving event: %v", ev)
			}
		}
	}
	if resolving != 2 {
		t.Errorf("%d resolving events: %v", resolving, ck)
	}
	r := must(t, a.post("event_rules/check/", map[string]any{"streams": []int64{am.id}, "filter": "true", "check": "event", "check_condition": `event.type`}), 200).Body
	if errs, _ := r["errors"].(map[string]any); errs["check_condition"] == nil {
		t.Errorf("the check of a check condition that isn't a condition: %v", r["errors"])
	}

	// Disabling the rule cancels the checks it waits on.
	am.fire(t, "web-03")
	must(t, a.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"enabled": false}), 200)
	cs = waitChecks(t, a, rid, 5)
	wantCheck(t, cs[4], "web-03", "canceled", "The rule was disabled")
	if d := lastDecision(t, a, rid); d["check_outcome"] != "canceled" {
		t.Errorf("the canceled check's decision: %v", d)
	}
}

// In shadow mode a check only watches: a key that resolves by itself shows
// that a fix would have looked fixed anyway. It doesn't count in the
// success rate, nor escalate.
func TestEventRuleFixShadow(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	am := newAlertStream(t, a, org)
	rec, hook := newHookServer(t)
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("watch"), "organization": org, "streams": []int64{am.id},
		"filter": `event.type == "alert.firing"`, "key": "event.data.labels.instance", "action": "notify", "notification_template": nt,
		"check": "event", "check_condition": `event.type == "alert.resolved"`, "check_window": 1, "check_notification_template": nt}), 201).id()
	am.fire(t, "db1")
	am.resolve(t, "db1")
	am.fire(t, "db2")
	cs := waitChecks(t, a, rid, 2)
	wantCheck(t, cs[0], "db1", "resolved", "a fix would have looked fixed anyway")
	wantCheck(t, cs[1], "db2", "unresolved", "No event resolved the key within 1s of the decision.")
	if cs[1]["shadow"] != true || cs[1]["escalated"] != false {
		t.Errorf("shadow check %v", cs[1])
	}
	rate, sum := checksSummary(t, a, rid)
	shadow := sum["shadow"].(map[string]any)
	if rate != nil || sum["checked"] != float64(0) || shadow["checked"] != float64(2) || shadow["resolved"] != float64(1) || shadow["resolved_rate"] != 0.5 {
		t.Errorf("success rate %v, checks %v", rate, sum)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(rec.wait(t, 0)); n != 0 {
		t.Errorf("%d notifications in shadow mode", n)
	}
}

// A rule's check is checked when it is saved.
func TestEventRuleFixValidation(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	noLimit := f.template(t, "site.yml", nil)
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": uniq("v"), "organization": f.org, "streams": []int64{am.id}, "filter": "true",
			"unified_job_template": jt, "limit": "event.data.labels.instance"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	for _, c := range []struct {
		body  map[string]any
		field string
		msg   string
	}{
		{map[string]any{"check": "event", "check_window": 30}, "check_condition", "needs the condition"},
		{map[string]any{"check": "event", "check_condition": "true"}, "check_window", "one second at least"},
		{map[string]any{"check": "event", "check_condition": "event.data.labels.instance", "check_window": 30}, "check_condition", ""},
		{map[string]any{"check_condition": "true"}, "check_condition", "Only a rule with an event check"},
		{map[string]any{"check_approval": true}, "check_approval", "Only a rule that checks its fixes"},
		{map[string]any{"check": "template"}, "check_template", "needs the job or workflow template"},
		{map[string]any{"check": "template", "check_template": noLimit}, "check_template", "does not prompt for a limit"},
		{map[string]any{"check": "template", "check_template": f.project}, "check_template", "job template or a workflow template"},
	} {
		r := must(t, a.post("event_rules/", base(c.body)), 400)
		if msg, ok := r.Body[c.field]; !ok || !strings.Contains(fmt.Sprint(msg), c.msg) {
			t.Errorf("%v: %s", c.body, r.Raw)
		}
	}
	rid := must(t, a.post("event_rules/", base(map[string]any{"check": "template", "check_template": jt, "check_approval": true})), 201).id()
	r := must(t, a.get(fmt.Sprintf("event_rules/%d/", rid)), 200).Body
	if r["check"] != "template" || r["check_template"] != float64(jt) || r["summary_fields"].(map[string]any)["checks"] == nil {
		t.Errorf("rule %v", r)
	}
}

// A check template decides whether the fix worked: its job's result, or
// the fixed artifact it sets. It launches as the rule's owner with the
// action's limit once the action's job finished.
func TestEventRuleFixByTemplate(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	probe := f.template(t, "fail.yml", map[string]any{"ask_limit_on_launch": true})
	unfixed := f.template(t, "unfixed.yml", map[string]any{"ask_limit_on_launch": true})
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("probe"), "organization": f.org, "streams": []int64{am.id}, "mode": "auto",
		"filter": "true", "key": "event.data.labels.instance", "unified_job_template": jt, "limit": "event.data.labels.instance",
		"check": "template", "check_template": probe}), 201).id()

	am.fire(t, "h1")
	cs := waitChecks(t, a, rid, 1)
	wantCheck(t, cs[0], "h1", "fixed", "succeeded")
	if cs[0]["job"] == nil || cs[0]["check_job"] == nil || cs[0]["action_finished"] == nil {
		t.Fatalf("check %v", cs[0])
	}
	cj := must(t, a.get(fmt.Sprintf("jobs/%v/", cs[0]["check_job"])), 200).Body
	if cj["limit"] != "h1" || cj["status"] != "successful" || cj["unified_job_template"] != float64(probe) {
		t.Errorf("check job %v %v %v", cj["limit"], cj["status"], cj["unified_job_template"])
	}
	for job, role := range map[any]string{cs[0]["job"]: "action", cs[0]["check_job"]: "check"} {
		j := must(t, a.get(fmt.Sprintf("jobs/%v/", job)), 200).Body
		list, _ := j["summary_fields"].(map[string]any)["event_rule_checks"].([]any)
		if len(list) != 1 || list[0].(map[string]any)["role"] != role || list[0].(map[string]any)["outcome"] != "fixed" {
			t.Errorf("job %v's checks: %v", job, list)
		}
	}

	am.fire(t, "h2")
	cs = waitChecks(t, a, rid, 2)
	wantCheck(t, cs[1], "h2", "not_fixed", "failed")

	must(t, a.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"check_template": unfixed}), 200)
	am.fire(t, "h3")
	cs = waitChecks(t, a, rid, 3)
	wantCheck(t, cs[2], "h3", "not_fixed", "set fixed to false")
	if rate, sum := checksSummary(t, a, rid); sum["checked"] != float64(3) || sum["fixed"] != float64(1) || fmt.Sprintf("%.3f", rate) != "0.333" {
		t.Errorf("success rate %v, checks %v", rate, sum)
	}
}
