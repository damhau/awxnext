package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/damhau/awxnext/internal/tracing"
)

const diskFull = `event.type == "alert.firing" && event.data.labels.alertname == "DiskFull"`

func promText(t *testing.T) string {
	t.Helper()
	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func wantMetrics(t *testing.T, lines ...string) {
	t.Helper()
	out := promText(t)
	for _, l := range lines {
		if !strings.Contains(out, "\n"+l+"\n") {
			t.Errorf("no line %q in /metrics", l)
		}
	}
}

// Each event leaves a decision for each rule of its stream: a filter that
// didn't match names the part that failed, and every decision has the
// event's trace. They are read from the rule and from the event, and
// counted in the metrics.
func TestEventRuleDecisionLog(t *testing.T) {
	slow(t)
	f := newLaunchFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("log"), "organization": f.org, "streams": []int64{am.id},
		"filter": diskFull, "key": "event.data.labels.instance", "throttle": "once_within", "throttle_window": 3600,
		"unified_job_template": jt, "limit": "event.data.labels.instance", "max_concurrent_runs": 1}), 201).id()
	cpu := must(t, a.post("event_rules/", map[string]any{"name": uniq("cpu"), "organization": f.org, "streams": []int64{am.id},
		"filter": `event.data.labels.alertname == "HighCPU"`, "unified_job_template": jt}), 201).id()

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	code, b, _ := am.post(t, alertGroup(testAlert{"DiskFull", "web-1", "2026-11-15T08:00:00Z", "d1"},
		testAlert{"HighCPU", "web-1", "2026-11-15T08:00:00Z", "c1"}, testAlert{"DiskFull", "web-1", "2026-11-15T08:01:00Z", "d2"}),
		headers("Authorization", "Bearer tok", "Content-Type", "application/json", "Traceparent", "00-"+traceID+"-00f067aa0ba902b7-01"))
	if code != 202 || b["stored"] != float64(3) {
		t.Fatalf("post: %d %v", code, b)
	}
	waitRules(t, am.id)
	ds := must(t, a.get(fmt.Sprintf("event_rules/%d/decisions/?order_by=sequence", rid)), 200).results()
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%v %v", d["sequence"], d["outcome"]))
		if d["trace_id"] != traceID {
			t.Errorf("decision %v: trace %v", d["sequence"], d["trace_id"])
		}
	}
	if !slices.Equal(got, []string{"1 shadow", "2 not_matched", "3 throttled"}) {
		t.Fatalf("decisions %v", got)
	}
	miss := ds[1]
	wantOutcome(t, miss, "not_matched", "", `The filter's condition event.data.labels.alertname == "DiskFull" is false.`)
	fl, _ := miss["details"].(map[string]any)["filter"].(map[string]any)
	part := `event.data.labels.alertname == "DiskFull"`
	start := strings.Index(diskFull, part)
	if fl["expression"] != part || fl["start"] != float64(start) || fl["end"] != float64(start+len(part)) ||
		fmt.Sprint(fl["values"]) != "[map[expression:event.data.labels.alertname value:HighCPU]]" {
		t.Errorf("the failed part: %v", fl)
	}
	if n := must(t, a.get(fmt.Sprintf("event_rule_decisions/?rule=%d&outcome=not_matched", rid)), 200).count(); n != 1 {
		t.Errorf("%d not_matched decisions", n)
	}

	// The event's decisions: one per rule.
	events := am.events(t, a)
	var cpuEvent any
	for _, e := range events {
		if e["sequence"] == float64(2) {
			cpuEvent = e["id"]
		}
	}
	got = nil
	for _, d := range must(t, a.get(fmt.Sprintf("stream_events/%v/decisions/", cpuEvent)), 200).results() {
		got = append(got, fmt.Sprintf("%v %v", d["rule"], d["outcome"]))
	}
	if want := []string{fmt.Sprintf("%d not_matched", rid), fmt.Sprintf("%d shadow", cpu)}; !slices.Equal(got, want) {
		t.Errorf("the event's decisions %v, want %v", got, want)
	}
	stranger, _ := newUser(t, a)
	must(t, stranger.get(fmt.Sprintf("stream_events/%v/decisions/", cpuEvent)), 403)

	// Held back, and refused.
	runningJob(t, f.inv, "x", rid)
	am.fire(t, "web-2")
	wantOutcome(t, lastDecision(t, a, rid), "held", "max_concurrent_runs", "at its limit")
	if code, _, _ := am.post(t, alertGroup(), headers("Authorization", "Bearer nope", "Content-Type", "application/json")); code != 401 {
		t.Errorf("a wrong token: %d", code)
	}
	r, s := idText(rid), idText(am.id)
	wantMetrics(t,
		fmt.Sprintf(`awxnext_event_stream_received_total{stream="%s"} 4`, s),
		fmt.Sprintf(`awxnext_event_stream_refused_total{stream="%s",reason="unauthorized"} 1`, s),
		fmt.Sprintf(`awxnext_event_stream_lag_seconds_count{stream="%s"} 4`, s),
		fmt.Sprintf(`awxnext_event_stream_backlog_events{stream="%s"} 0`, s),
		fmt.Sprintf(`awxnext_event_rule_matched_total{rule="%s"} 3`, r),
		fmt.Sprintf(`awxnext_event_rule_decisions_total{rule="%s",outcome="not_matched"} 1`, r),
		fmt.Sprintf(`awxnext_event_rule_decisions_total{rule="%s",outcome="shadow"} 1`, r),
		fmt.Sprintf(`awxnext_event_rule_decisions_total{rule="%s",outcome="throttled"} 1`, r),
		fmt.Sprintf(`awxnext_event_rule_held_total{rule="%s",guardrail="max_concurrent_runs"} 1`, r),
	)
}

func idText(id int64) string { return fmt.Sprint(id) }

// A backtest runs a rule, or a draft of it, over the log as shadow mode
// would have: it counts the firings, the keys and hosts, and what the
// guardrails hold back, and changes nothing.
func TestEventRuleBacktest(t *testing.T) {
	slow(t)
	f := newLaunchFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	busy := runningJob(t, f.inv, "h2", 0)
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("bt"), "organization": f.org, "streams": []int64{am.id},
		"filter": diskFull, "key": "event.data.labels.instance", "throttle": "once_within", "throttle_window": 3600,
		"unified_job_template": jt, "limit": "event.data.labels.instance"}), 201).id()
	am.alert(t, 4, testAlert{"DiskFull", "h1", "2026-11-15T08:00:00Z", "d1"}, testAlert{"DiskFull", "h1", "2026-11-15T08:01:00Z", "d2"},
		testAlert{"DiskFull", "h2", "2026-11-15T08:02:00Z", "d3"}, testAlert{"HighCPU", "h3", "2026-11-15T08:03:00Z", "c1"})
	waitRules(t, am.id)

	state := func() string {
		var ds, keys int
		app.DB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM event_rule_decisions WHERE rule_id = $1), (SELECT count(*) FROM event_rule_keys WHERE rule_id = $1)`, rid).Scan(&ds, &keys)
		return fmt.Sprintf("%d decisions, %d keys", ds, keys)
	}
	before := state()
	url := fmt.Sprintf("event_rules/%d/backtest/", rid)
	bt := must(t, a.post(url, map[string]any{}), 200).Body
	if bt["rule"] != float64(rid) || bt["events"] != float64(4) || bt["matched"] != float64(3) || bt["fired"] != float64(2) || bt["truncated"] != false {
		t.Errorf("backtest: %v", bt)
	}
	if fmt.Sprint(bt["outcomes"]) != "map[fired:2 not_matched:1 throttled:1]" || fmt.Sprint(bt["held"]) != "map[]" {
		t.Errorf("outcomes %v, held %v", bt["outcomes"], bt["held"])
	}
	if fmt.Sprint(bt["keys"]) != "[map[deduplicated:0 events:2 fired:1 held:0 key:h1 throttled:1] map[deduplicated:0 events:1 fired:1 held:0 key:h2 throttled:0]]" {
		t.Errorf("keys %v", bt["keys"])
	}
	if fmt.Sprint(bt["hosts"]) != "[map[fired:1 host:h1] map[fired:1 host:h2]]" {
		t.Errorf("hosts %v", bt["hosts"])
	}
	misses := bt["misses"].([]any)
	if len(misses) != 1 || misses[0].(map[string]any)["expression"] != `event.data.labels.alertname == "DiskFull"` || misses[0].(map[string]any)["count"] != float64(1) {
		t.Errorf("misses %v", misses)
	}
	if ds := bt["decisions"].([]any); len(ds) != 3 || ds[0].(map[string]any)["outcome"] != "fired" ||
		!strings.Contains(fmt.Sprint(ds[0].(map[string]any)["reason"]), "would have launched") {
		t.Errorf("decisions %v", ds)
	}

	// A draft of the rule: the guardrails it would add, over the same log.
	held := func(body map[string]any) string {
		t.Helper()
		r := must(t, a.post(url, body), 200).Body
		return fmt.Sprint(r["outcomes"], " ", r["held"])
	}
	if got := held(map[string]any{"host_lock": true}); got != "map[fired:1 held:1 not_matched:1 throttled:1] map[host_lock:1]" {
		t.Errorf("host lock: %s", got)
	}
	if got := held(map[string]any{"max_concurrent_runs": 1}); got != "map[fired:1 held:1 not_matched:1 throttled:1] map[max_concurrent_runs:1]" {
		t.Errorf("max runs: %s", got)
	}
	r := must(t, a.post(url, map[string]any{"throttle": "", "breaker_firings": 1}), 200).Body
	if got := fmt.Sprint(r["outcomes"], " ", r["held"]); got != "map[fired:2 held:1 not_matched:1] map[circuit_breaker:1]" {
		t.Errorf("breaker: %s", got)
	}
	if b := r["breakers"].([]any); len(b) != 2 {
		t.Errorf("breakers %v", b)
	}
	endJob(t, busy)
	if got := held(map[string]any{"host_lock": true}); got != "map[fired:1 held:1 not_matched:1 throttled:1] map[host_lock:1]" {
		t.Errorf("host lock, the job since ended: %s", got)
	}
	if r := must(t, a.post(url, map[string]any{"max_events": 2}), 200).Body; r["events"] != float64(2) || r["truncated"] != true {
		t.Errorf("max_events: %v %v", r["events"], r["truncated"])
	}
	if r := must(t, a.post(url, map[string]any{"since": "2001-01-01T00:00:00Z", "until": "2001-01-02T00:00:00Z"}), 200).Body; r["events"] != float64(0) {
		t.Errorf("an empty period: %v", r["events"])
	}
	must(t, a.post(url, map[string]any{"since": "2026-01-02T00:00:00Z", "until": "2026-01-01T00:00:00Z"}), 400)
	must(t, a.post(url, map[string]any{"max_events": 0}), 400)
	must(t, a.post(url, map[string]any{"since": "yesterday"}), 400)
	must(t, a.post(url, map[string]any{"filter": "event.data.labels.alertname > 3"}), 400)
	if after := state(); after != before {
		t.Errorf("the backtests changed the rule's state: %s, then %s", before, after)
	}
	if rule := must(t, a.get(fmt.Sprintf("event_rules/%d/", rid)), 200).Body; rule["throttle"] != "once_within" || rule["host_lock"] != false {
		t.Errorf("the backtests changed the rule: %v", rule)
	}

	// A rule not saved yet.
	draft := map[string]any{"organization": f.org, "streams": []int64{am.id}, "filter": `event.data.labels.alertname == "HighCPU"`,
		"unified_job_template": jt, "limit": "event.data.labels.instance"}
	r = must(t, a.post("event_rules/backtest/", draft), 200).Body
	if r["rule"] != nil || r["matched"] != float64(1) || fmt.Sprint(r["hosts"]) != "[map[fired:1 host:h3]]" {
		t.Errorf("draft: %v", r)
	}
	must(t, a.post("event_rules/backtest/", map[string]any{"streams": []int64{am.id}, "filter": "true"}), 400)

	// The backtest reads the streams' events and the template's hosts:
	// one who may not see them may not run it.
	stranger, _ := newUser(t, a)
	must(t, stranger.post(url, map[string]any{}), 403)
	if code := stranger.post("event_rules/backtest/", draft).Status; code != 403 && code != 400 {
		t.Errorf("a stranger's draft: %d", code)
	}
}

// The cleanup job drops the days of decisions past every retention, then
// each stream's: decisions that didn't match go with their events, the
// others stay EVENT_RULE_DECISION_RETENTION_DAYS. Expired throttle keys
// go too, and patterns' windows a day past their deadline, whose waiting
// decisions are dropped.
func TestEventRuleDecisionCleanup(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ctx := t.Context()
	am := newAlertStream(t, a, org)
	must(t, a.patch(fmt.Sprintf("event_streams/%d/", am.id), map[string]any{"retention_days": 1}), 200)
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("clean"), "organization": org, "streams": []int64{am.id},
		"filter": "true", "action": "post_event", "target_stream": newAlertStream(t, a, org).id, "event_type": `"x"`, "enabled": false}), 201).id()
	if _, err := app.DB.Exec(ctx, `CREATE TABLE IF NOT EXISTS event_rule_decisions_20010101 PARTITION OF event_rule_decisions
		FOR VALUES FROM ('2001-01-01T00:00:00Z') TO ('2001-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for i, d := range []struct {
		outcome string
		at      string
	}{{"not_matched", "now() - interval '3 days'"}, {"shadow", "now() - interval '3 days'"}, {"shadow", "now() - interval '100 days'"},
		{"not_matched", "now()"}, {"shadow", "'2001-01-01T12:00:00Z'"}, {"waiting", "now()"}} {
		if _, err := app.DB.Exec(ctx, `INSERT INTO event_rule_decisions (rule_id, stream_id, sequence, event_id, event_source, event_type, received, outcome)
			VALUES ($1, $2, $3, 'e', '/s', 't', `+d.at+`, $4)`, rid, am.id, i+1, d.outcome); err != nil {
			t.Fatal(err)
		}
	}
	app.DB.Exec(ctx, `INSERT INTO event_rule_keys (rule_id, key, held_until) VALUES ($1, 'old', now() - interval '2 days'), ($1, 'new', now() + interval '1 hour')`, rid)
	if _, err := app.DB.Exec(ctx, `INSERT INTO event_rule_patterns (rule_id, key, opened, clock, deadline, events)
		SELECT $1, 'stale', now() - interval '3 days', now() - interval '3 days', now() - interval '2 days',
			jsonb_build_array(jsonb_build_object('decision', d.id, 'received', d.received))
		FROM event_rule_decisions d WHERE d.rule_id = $1 AND d.sequence = 6`, rid); err != nil {
		t.Fatal(err)
	}
	app.DB.Exec(ctx, `INSERT INTO event_rule_patterns (rule_id, key, opened, clock, deadline) VALUES ($1, 'fresh', now(), now(), now() + interval '1 hour')`, rid)
	left := func() []string {
		rows, _ := app.DB.Query(ctx, `SELECT sequence || ' ' || outcome FROM event_rule_decisions WHERE rule_id = $1
			UNION ALL SELECT 'key ' || key FROM event_rule_keys WHERE rule_id = $1
			UNION ALL SELECT 'window ' || key FROM event_rule_patterns WHERE rule_id = $1 ORDER BY 1`, rid)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	var sjt int64
	app.DB.QueryRow(ctx, `SELECT id FROM unified_job_templates WHERE type = 'system_job_template' AND job_type = 'cleanup_event_streams'`).Scan(&sjt)
	launch := func(extra map[string]any) string {
		t.Helper()
		j := must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", sjt), map[string]any{"extra_vars": extra}), 201)
		if r := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", j.id())); r.Body["status"] != "successful" {
			t.Fatalf("cleanup: %v", r.Body)
		}
		return a.get(fmt.Sprintf("system_jobs/%d/stdout/?format=txt", j.id())).Raw
	}
	all := []string{"1 not_matched", "2 shadow", "3 shadow", "4 not_matched", "5 shadow", "6 waiting", "key new", "key old", "window fresh", "window stale"}
	out := launch(map[string]any{"dry_run": true})
	if !strings.Contains(out, "event_rule_decisions_20010101 would be dropped") || !strings.Contains(out, "Decisions would delete") ||
		!strings.Contains(out, "Pattern windows a day past their deadline would delete: 1") {
		t.Errorf("dry run output:\n%s", out)
	}
	if got := left(); !slices.Equal(got, all) {
		t.Fatalf("the dry run removed %v", got)
	}
	out = launch(map[string]any{})
	if got, want := left(), []string{"2 shadow", "4 not_matched", "6 dropped", "key new", "window fresh"}; !slices.Equal(got, want) {
		t.Errorf("after cleanup %v, want %v\n%s", got, want, out)
	}
	var exists bool
	app.DB.QueryRow(ctx, `SELECT to_regclass('event_rule_decisions_20010101') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Error("the old day's partition is still there")
	}
}

// One trace runs from the event, whose sender's traceparent it carries,
// through the rule's decision, to the job it launched.
func TestEventRuleTrace(t *testing.T) {
	requireAnsible(t)
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	restore := tracing.Install(tp)
	t.Cleanup(func() {
		restore()
		tp.Shutdown(context.Background())
	})
	f := newJobFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", nil)
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("trace"), "organization": f.org, "streams": []int64{am.id},
		"filter": diskFull, "unified_job_template": jt, "mode": "auto"}), 201).id()
	traceID := "0af7651916cd43dd8448eb211c80319c"
	if code, b, _ := am.post(t, alertGroup(testAlert{"DiskFull", "web-1", "2026-11-15T08:00:00Z", "t1"}),
		headers("Authorization", "Bearer tok", "Content-Type", "application/json", "Traceparent", "00-"+traceID+"-b7ad6b7169203331-01")); code != 202 {
		t.Fatalf("post: %d %v", code, b)
	}
	waitRules(t, am.id)
	jobs := ruleJobs(t, a, rid)
	if len(jobs) != 1 {
		t.Fatalf("jobs %v", jobs)
	}
	waitJob(t, a, fmt.Sprintf("jobs/%v/", jobs[0]["id"]))
	if d := lastDecision(t, a, rid); d["trace_id"] != traceID {
		t.Errorf("the decision's trace: %v", d["trace_id"])
	}
	byName := map[string]tracetest.SpanStub{}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(byName) < 3 {
		for _, s := range exp.GetSpans() {
			if s.SpanContext.TraceID().String() != traceID {
				continue
			}
			switch {
			case s.Name == "event_stream event", s.Name == "event_rule decide":
				byName[s.Name] = s
			case s.Name == "job job" && fmt.Sprint(jobAttr(s)) == fmt.Sprint(jobs[0]["id"]):
				byName["job"] = s
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	ev, decide, job := byName["event_stream event"], byName["event_rule decide"], byName["job"]
	if ev.Name == "" || decide.Name == "" || job.Name == "" {
		t.Fatalf("spans of the trace: %v", byName)
	}
	if ev.Parent.SpanID().String() != "b7ad6b7169203331" || decide.Parent.SpanID() != ev.SpanContext.SpanID() || job.Parent.SpanID() != decide.SpanContext.SpanID() {
		t.Errorf("parents: event %s, decide %s (event %s), job %s (decide %s)", ev.Parent.SpanID(), decide.Parent.SpanID(), ev.SpanContext.SpanID(),
			job.Parent.SpanID(), decide.SpanContext.SpanID())
	}
}

func jobAttr(s tracetest.SpanStub) any {
	for _, kv := range s.Attributes {
		if kv.Key == "awx.job.id" {
			return kv.Value.AsInt64()
		}
	}
	return nil
}
