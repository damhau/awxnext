package test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/resources"
)

var patternSeq atomic.Int64

func newCEStream(t *testing.T, a *client, org int64) eventStream {
	t.Helper()
	return newEventStream(t, a, org, streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"}), eventstreams.TypeCloudEvents, nil)
}

// send posts a CloudEvent of a type about a host, and returns its id.
func (s eventStream) send(t *testing.T, typ, host string) string {
	t.Helper()
	id := fmt.Sprintf("p%d", patternSeq.Add(1))
	code, b, _ := s.post(t, fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":"/monitor","type":%q,"data":{"host":%q}}`, id, typ, host),
		headers("Authorization", "Bearer tok", "Content-Type", "application/cloudevents+json"))
	if code != 202 {
		t.Fatalf("posting %s: %d %v", typ, code, b)
	}
	return id
}

// byEvent returns a rule's decisions by event id.
func byEvent(t *testing.T, a *client, rule int64) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, d := range must(t, a.get(fmt.Sprintf("event_rule_decisions/?rule=%d&page_size=200", rule)), 200).results() {
		out[d["event_id"].(string)] = d
	}
	return out
}

// wantDecision checks the outcome of the decision on an event, and that its
// reason holds reason.
func wantDecision(t *testing.T, ds map[string]map[string]any, event, outcome, reason string) map[string]any {
	t.Helper()
	d := ds[event]
	if d == nil {
		t.Fatalf("no decision on %s", event)
	}
	if d["outcome"] != outcome || !strings.Contains(fmt.Sprint(d["reason"]), reason) {
		t.Errorf("decision on %s: %v %q, want %s %q (details %v)", event, d["outcome"], d["reason"], outcome, reason, d["details"])
	}
	return d
}

func progress(d map[string]any) string {
	p, _ := d["details"].(map[string]any)["pattern"].(map[string]any)
	return fmt.Sprintf("%v %v of %v", p["kind"], p["seen"], p["needed"])
}

// waitPosted waits for a stream to hold n events, and returns them.
func waitPosted(t *testing.T, a *client, s eventStream, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for len(s.events(t, a)) < n && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	evs := s.events(t, a)
	if len(evs) != n {
		t.Fatalf("%d events posted, want %d: %v", len(evs), n, evs)
	}
	return evs
}

// A count waits for its events across the rule's streams, fires once with
// all of them, and starts over; each firing counts once for the breaker,
// and a backtest counts the firings.
func TestEventRulePatternCount(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	s1, s2 := newCEStream(t, a, org), newCEStream(t, a, org)
	target := newCEStream(t, a, org)
	rule := func(name, mode string, count int, extra map[string]any) int64 {
		body := map[string]any{"name": uniq(name), "organization": org, "streams": []int64{s1.id, s2.id}, "mode": mode,
			"filter": `event.type == "disk.full"`, "key": "event.data.host", "pattern": "count", "pattern_count": count, "pattern_window": 600,
			"action": "post_event", "target_stream": target.id, "event_type": `"escalate"`,
			"event_data": `{"hosts": events.map(e, e.data.host), "n": size(events), "first": event.id}`}
		for k, v := range extra {
			body[k] = v
		}
		return must(t, a.post("event_rules/", body), 201).id()
	}
	r1 := rule("count", "auto", 3, nil)
	r2 := rule("shadow", "shadow", 2, map[string]any{"breaker_firings": 2})

	e1 := s1.send(t, "disk.full", "a")
	e2 := s2.send(t, "disk.full", "a")
	e3 := s1.send(t, "disk.full", "b")
	waitRules(t, s1.id)
	waitRules(t, s2.id)
	ds := byEvent(t, a, r1)
	if d := wantDecision(t, ds, e1, "waiting", "1 of 3 events within the window: waiting for 2 more before"); progress(d) != "count 1 of 3" {
		t.Errorf("progress %s", progress(d))
	}
	if d := wantDecision(t, ds, e2, "waiting", "2 of 3 events"); progress(d) != "count 2 of 3" || d["key"] != "a" {
		t.Errorf("decision %v", d)
	}
	wantDecision(t, ds, e3, "waiting", "1 of 3 events")
	ds2 := byEvent(t, a, r2)
	wantDecision(t, ds2, e2, "shadow", "would have posted")
	if n := must(t, a.get(fmt.Sprintf("event_rules/%d/breakers/", r2)), 200).count(); n != 0 {
		t.Errorf("one firing of two events tripped the breaker")
	}

	e4 := s2.send(t, "disk.full", "a")
	waitRules(t, s2.id)
	ev := waitPosted(t, a, target, 1)[0]
	if d := ev["data"].(map[string]any); fmt.Sprint(d["hosts"]) != "[a a a]" || d["n"] != float64(3) || d["first"] != e1 {
		t.Errorf("posted data %v", d)
	}
	ds = byEvent(t, a, r1)
	fired := 0
	for _, e := range []string{e1, e2, e4} {
		d := wantDecision(t, ds, e, "posted", "")
		if d["fired"] == true {
			fired++
		}
		if progress(d) != "count 3 of 3" {
			t.Errorf("progress of %s: %s", e, progress(d))
		}
	}
	if fired != 1 {
		t.Errorf("%d decisions of the firing count for the breaker", fired)
	}

	e5 := s1.send(t, "disk.full", "a")
	waitRules(t, s1.id)
	wantDecision(t, byEvent(t, a, r1), e5, "waiting", "1 of 3 events")
	wantDecision(t, byEvent(t, a, r2), e5, "shadow", "")
	brk := must(t, a.get(fmt.Sprintf("event_rules/%d/breakers/", r2)), 200).results()
	if len(brk) != 1 || brk[0]["key"] != "a" || brk[0]["firings"] != float64(2) || brk[0]["shadow"] != true {
		t.Errorf("breakers after two firings: %v", brk)
	}

	ws := must(t, a.get(fmt.Sprintf("event_rules/%d/patterns/?order_by=key", r1)), 200).results()
	if len(ws) != 2 || ws[0]["key"] != "a" || ws[1]["key"] != "b" || len(ws[0]["events"].([]any)) != 1 {
		t.Fatalf("windows %v", ws)
	}
	if p := ws[1]["progress"].(map[string]any); p["seen"] != float64(1) || p["needed"] != float64(3) || p["until"] == nil {
		t.Errorf("progress %v", p)
	}

	bt := must(t, a.post(fmt.Sprintf("event_rules/%d/backtest/", r1), map[string]any{}), 200).Body
	if bt["firings"] != float64(1) || bt["fired"] != float64(3) || fmt.Sprint(bt["outcomes"]) != "map[fired:3 waiting:2]" {
		t.Errorf("backtest: firings %v, fired %v, outcomes %v", bt["firings"], bt["fired"], bt["outcomes"])
	}

	// Changing the pattern drops the windows open.
	must(t, a.patch(fmt.Sprintf("event_rules/%d/", r1), map[string]any{"pattern_window": 300}), 200)
	ds = byEvent(t, a, r1)
	wantDecision(t, ds, e3, "dropped", "pattern changed")
	wantDecision(t, ds, e5, "dropped", "pattern changed")
	if n := must(t, a.get(fmt.Sprintf("event_rules/%d/patterns/", r1)), 200).count(); n != 0 {
		t.Errorf("%d windows left", n)
	}
}

// An absence fires at the end of its window, from the timer, with the
// events of the first condition, unless one of the second clears them. The
// timer expires a count's events as its window slides past them.
func TestEventRulePatternAbsence(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	s := newCEStream(t, a, org)
	target := newCEStream(t, a, org)
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("absence"), "organization": org, "streams": []int64{s.id}, "mode": "auto",
		"filter": "true", "key": "event.data.host", "pattern": "absence", "conditions": []string{`event.type == "down"`, `event.type == "up"`},
		"pattern_window": 3, "action": "post_event", "target_stream": target.id, "event_type": `"still.down"`,
		"event_data": `{"host": event.data.host, "n": size(events)}`}), 201).id()
	count := must(t, a.post("event_rules/", map[string]any{"name": uniq("count"), "organization": org, "streams": []int64{s.id}, "mode": "auto",
		"filter": `event.type == "down"`, "key": "event.data.host", "pattern": "count", "pattern_count": 5, "pattern_window": 3,
		"action": "post_event", "target_stream": target.id, "event_type": `"five"`}), 201).id()

	a1 := s.send(t, "down", "a")
	b1 := s.send(t, "down", "b")
	a2 := s.send(t, "down", "a")
	b2 := s.send(t, "up", "b")
	other := s.send(t, "restart", "a")
	waitRules(t, s.id)
	ds := byEvent(t, a, rid)
	wantDecision(t, ds, b1, "cleared", "An event of condition 2 came before the window ended.")
	wantDecision(t, ds, b2, "cleared", "cleared the 1 event of condition 1")
	wantDecision(t, ds, a1, "waiting", "1 event of condition 1 waiting for an event of condition 2 until")
	d := wantDecision(t, ds, a2, "waiting", "2 events of condition 1 waiting")
	if p := d["details"].(map[string]any)["pattern"].(map[string]any); p["kind"] != "absence" || p["events"] != float64(2) || p["seen"] != nil {
		t.Errorf("progress %v", p)
	}
	if d := wantDecision(t, ds, other, "not_matched", "matches none of the rule's conditions"); len(d["details"].(map[string]any)["conditions"].([]any)) != 2 {
		t.Errorf("details %v", d["details"])
	}
	until, err := time.Parse(time.RFC3339, d["details"].(map[string]any)["pattern"].(map[string]any)["until"].(string))
	if err != nil {
		t.Fatal(err)
	}

	ev := waitPosted(t, a, target, 1)[0]
	if d := ev["data"].(map[string]any); d["host"] != "a" || d["n"] != float64(2) {
		t.Errorf("posted %v", d)
	}
	ds = byEvent(t, a, rid)
	for _, e := range []string{a1, a2} {
		d := wantDecision(t, ds, e, "posted", "")
		at, _ := time.Parse(time.RFC3339Nano, d["modified"].(string))
		if at.Before(until) {
			t.Errorf("fired at %s, before the deadline %s", at, until)
		}
	}
	late := s.send(t, "up", "a")
	waitRules(t, s.id)
	wantDecision(t, byEvent(t, a, rid), late, "ignored", "No event of condition 1 waits")

	ds = byEvent(t, a, count)
	for _, e := range []string{a1, b1, a2} {
		wantDecision(t, ds, e, "expired", "before the pattern was complete")
	}
	if n := must(t, a.get(fmt.Sprintf("event_rules/%d/patterns/", count)), 200).count(); n != 0 {
		t.Errorf("%d windows left after they expired", n)
	}
}

// A sequence fires on its last step, in order; all of several conditions
// in any order; a pattern in approval mode waits for one approval for the
// firing's events.
func TestEventRulePatternSequence(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	s := newCEStream(t, a, org)
	target := newCEStream(t, a, org)
	rule := func(name, mode, pattern string, conds []string) int64 {
		return must(t, a.post("event_rules/", map[string]any{"name": uniq(name), "organization": org, "streams": []int64{s.id}, "mode": mode,
			"filter": "true", "key": "event.data.host", "pattern": pattern, "conditions": conds, "pattern_window": 600,
			"action": "post_event", "target_stream": target.id, "event_type": `"` + pattern + `"`,
			"event_data": `{"types": events.map(e, e.type)}`}), 201).id()
	}
	seq := rule("seq", "auto", "sequence", []string{`event.type == "a"`, `event.type == "b"`, `event.type == "c"`})
	all := rule("all", "approval", "all", []string{`event.type == "x"`, `event.type == "y"`})

	var ids []string
	for _, typ := range []string{"b", "a", "c", "b", "c", "y", "x"} {
		ids = append(ids, s.send(t, typ, "h"))
	}
	waitRules(t, s.id)
	ds := byEvent(t, a, seq)
	wantDecision(t, ds, ids[0], "ignored", "matches step 2, but no event of step 1 came before it")
	wantDecision(t, ds, ids[2], "ignored", "matches step 3")
	if d := wantDecision(t, ds, ids[3], "posted", ""); progress(d) != "sequence 3 of 3" {
		t.Errorf("progress %s", progress(d))
	}
	wantDecision(t, ds, ids[1], "posted", "")
	wantDecision(t, ds, ids[4], "posted", "")
	ev := waitPosted(t, a, target, 1)[0]
	if ev["event_type"] != "sequence" || fmt.Sprint(ev["data"].(map[string]any)["types"]) != "[a b c]" {
		t.Errorf("posted %v", ev)
	}

	ds = byEvent(t, a, all)
	wantDecision(t, ds, ids[5], "awaiting_approval", "")
	d := wantDecision(t, ds, ids[6], "awaiting_approval", "")
	if d["approval"] == nil || d["approval"] != ds[ids[5]]["approval"] {
		t.Fatalf("approvals %v and %v", d["approval"], ds[ids[5]]["approval"])
	}
	if d["fired"] == ds[ids[5]]["fired"] {
		t.Errorf("both events of the firing count for the breaker")
	}
	must(t, a.post(fmt.Sprintf("workflow_approvals/%v/approve/", d["approval"]), map[string]any{}), 204)
	evs := waitPosted(t, a, target, 2)
	types := []string{fmt.Sprint(evs[0]["event_type"]), fmt.Sprint(evs[1]["event_type"])}
	if !slices.Contains(types, "all") {
		t.Errorf("posted %v", evs)
	}
	ds = byEvent(t, a, all)
	wantDecision(t, ds, ids[5], "posted", "")
	wantDecision(t, ds, ids[6], "posted", "")
}

// A window outlives the node that opened it: the node taking the stream
// over completes it.
func TestEventRulePatternFailover(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	s := newCEStream(t, a, org)
	target := newCEStream(t, a, org)
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("fo"), "organization": org, "streams": []int64{s.id}, "mode": "auto",
		"filter": "true", "key": "event.data.host", "pattern": "count", "pattern_count": 2, "pattern_window": 600,
		"action": "post_event", "target_stream": target.id, "event_type": `"twice"`}), 201).id()
	e1 := s.send(t, "x", "h")
	waitRules(t, s.id)
	wantDecision(t, byEvent(t, a, rid), e1, "waiting", "1 of 2")

	var failures atomic.Int32
	resources.Rules.SetBeforeCommit(func(stream int64) error {
		if stream == s.id {
			failures.Add(1)
			return errors.New("the node stopped")
		}
		return nil
	})
	t.Cleanup(func() { resources.Rules.SetBeforeCommit(nil) })
	e2 := s.send(t, "x", "h")
	deadline := time.Now().Add(20 * time.Second)
	for failures.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if failures.Load() == 0 {
		t.Fatal("the node never tried the batch")
	}
	b := resources.NewRuleEngine(app, "test-node-b")
	t.Cleanup(b.Close)
	for {
		held, err := b.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(held, s.id) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second node never took the stream")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n, err := b.Step(t.Context(), s.id); err != nil || n != 1 {
		t.Fatalf("step: %d, %v", n, err)
	}
	ds := byEvent(t, a, rid)
	wantDecision(t, ds, e1, "posted", "")
	if d := wantDecision(t, ds, e2, "posted", ""); d["node"] != "test-node-b" {
		t.Errorf("decided on %v", d["node"])
	}
	waitPosted(t, a, target, 1)
	resources.Rules.SetBeforeCommit(nil)
	b.Close()
}

// A pattern's shape is checked when the rule is saved, and the editor's
// check says which conditions each event matches.
func TestEventRulePatternChecks(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	s := newCEStream(t, a, org)
	target := newCEStream(t, a, org)
	s.send(t, "a", "h")
	base := func(extra map[string]any) map[string]any {
		body := map[string]any{"name": uniq("chk"), "organization": org, "streams": []int64{s.id}, "filter": "true",
			"action": "post_event", "target_stream": target.id, "event_type": `"x"`}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	for _, c := range []struct {
		body  map[string]any
		field string
		want  string
	}{
		{map[string]any{"pattern": "count", "pattern_window": 60}, "pattern_count", "2 events at least"},
		{map[string]any{"pattern": "count", "pattern_count": 2}, "pattern_window", "at least one second"},
		{map[string]any{"pattern": "absence", "pattern_window": 60, "conditions": []string{"true", "true", "true"}}, "conditions", "2 conditions"},
		{map[string]any{"pattern": "sequence", "pattern_window": 60, "conditions": []string{"true"}}, "conditions", "2 conditions at least"},
		{map[string]any{"pattern": "count", "pattern_count": 2, "pattern_window": 60, "throttle": "once_within", "throttle_window": 60}, "throttle", "no throttle"},
		{map[string]any{"conditions": []string{"true", "true"}}, "conditions", "Only a rule with an all, sequence or absence pattern"},
		{map[string]any{"pattern": "all", "pattern_window": 60, "conditions": []string{"true", "event.data.host"}}, "conditions", "2: "},
		{map[string]any{"pattern": "all", "pattern_window": 60, "conditions": []any{"true", 3}}, "conditions", "2: expected a CEL condition"},
	} {
		r := must(t, a.post("event_rules/", base(c.body)), 400)
		if !strings.Contains(fmt.Sprint(r.Body[c.field]), c.want) {
			t.Errorf("%v: %s", c.body, r.Raw)
		}
	}
	must(t, a.post("event_rules/", base(map[string]any{"pattern": "sequence", "pattern_window": 60,
		"conditions": []string{`event.type == "a"`, `event.type == "b"`}})), 201)

	res := must(t, a.post("event_rules/check/", map[string]any{"streams": []int64{s.id}, "filter": "true", "pattern": "sequence",
		"conditions": []string{`event.type == "b"`, `event.type == "a"`, "event.data.host"}}), 200).Body
	if errs := res["errors"].(map[string]any); errs["conditions.2"] == nil || len(errs) != 1 {
		t.Errorf("errors %v", errs)
	}
	evs := res["events"].([]any)
	if len(evs) != 1 || fmt.Sprint(evs[0].(map[string]any)["conditions"]) != "[1]" || evs[0].(map[string]any)["matched"] != true {
		t.Errorf("events %v", evs)
	}
	res = must(t, a.post("event_rules/check/", map[string]any{"streams": []int64{s.id}, "filter": "true", "pattern": "all",
		"conditions": []string{`event.type == "b"`, `event.type == "c"`}}), 200).Body
	if ev := res["events"].([]any)[0].(map[string]any); ev["matched"] != false || res["matched"] != float64(0) ||
		!strings.Contains(fmt.Sprint(ev["reason"]), "none of the rule's conditions") {
		t.Errorf("check %v", res)
	}
}
