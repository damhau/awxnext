// Package rulestest runs `awxnext rules test`: the EventRuleTest documents
// of a GitOps directory send sample events through its EventRule
// documents, offline, and compare the decisions with those expected.
//
//	kind: EventRuleTest
//	name: Restart once per instance
//	spec:
//	  rule: Restart on ServiceDown
//	  events:
//	    - type: alert.firing
//	      data: {labels: {alertname: ServiceDown, instance: web1}}
//	      expect: {outcome: fired, key: web1}
//	    - at: 10m
//	      type: alert.firing
//	      data: {labels: {alertname: ServiceDown, instance: web1}}
//	      expect: throttled
//
// What needs no database is checked as the engine decides: the filter (a
// miss names the part that failed), the key, the throttle or the pattern,
// the circuit breaker, the maintenance windows and change freezes the
// directory declares, and the action's expressions. The blast radius needs
// the jobs running, and inventories: it is not checked. A test ends at its
// until, or else once every window has ended.
package rulestest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/gitops"
	"github.com/damhau/awxnext/internal/rules"
	"github.com/damhau/awxnext/internal/schedule"
)

// DefaultStart is when a test's first event is received, unless the test
// says.
var DefaultStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const testSource = "/api/v2/event_streams/test/"

// Result is a test's run.
type Result struct {
	File, Name string
	Events     int
	// Failures say how decisions differ from those expected.
	Failures []string
	// Notes say what the test could not check.
	Notes []string
}

// Error is a directory the tests cannot run on: a document that doesn't
// load, or an expression that doesn't compile.
type Error struct{ Problems []string }

func (e *Error) Error() string { return strings.Join(e.Problems, "\n") }

type rule struct {
	name                       string
	filter, key, throttle      string
	window                     int64
	action                     string
	extraVars                  map[string]string
	limit, message             string
	eventType, eventData       string
	mode, windowAction         string
	breakerFirings, breakerWin int64
	pattern                    string
	conditions                 []string
	patternCount, patternWin   int64
}

type window struct {
	name, kind, rrule string
	duration          int64
	rules             []string
	groups            bool
}

// event is a test's event: as the rule sees it, when it is received, and
// what is expected of its decision.
type event struct {
	label    string
	value    map[string]any
	received time.Time
	expect   *expectation
}

type expectation struct {
	outcome, key, guardrail, reason string
	extraVars                       map[string]any
	limit                           *string
	message, eventData              any
	eventType                       *string
}

// Run loads dir and runs its tests, in the order of their files.
func Run(ctx context.Context, dir string) ([]Result, error) {
	decls, tests, err := gitops.LoadWithTests(dir)
	if err != nil {
		return nil, &Error{Problems: []string{err.Error()}}
	}
	var problems []string
	ruleDocs := map[string]*rule{}
	var names []string
	var windows []window
	for _, d := range decls {
		switch d.Kind {
		case "EventRule":
			r, err := loadRule(d)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s %s: %v", d.File, d.Name, err))
				continue
			}
			ruleDocs[d.Name] = r
			names = append(names, d.Name)
		case "MaintenanceWindow":
			w, err := loadWindow(d)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s %s: %v", d.File, d.Name, err))
				continue
			}
			if w.rrule != "" {
				windows = append(windows, w)
			}
		}
	}
	// The engine looks at change freezes first.
	sort.SliceStable(windows, func(i, j int) bool { return windows[i].kind == "freeze" && windows[j].kind != "freeze" })
	type prepared struct {
		doc    *gitops.Document
		rule   *rule
		events []event
		until  time.Time
	}
	var runs []prepared
	samples := map[*rule][]map[string]any{}
	formats := map[*rule]map[string]bool{}
	for _, t := range tests {
		name, _ := t.Spec["rule"].(string)
		if name == "" && len(names) == 1 {
			name = names[0]
		}
		r := ruleDocs[name]
		if r == nil {
			problems = append(problems, fmt.Sprintf("%s %s: no EventRule named %q in the directory", t.File, t.Name, name))
			continue
		}
		evs, fs, err := loadEvents(t.Spec)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s %s: %v", t.File, t.Name, err))
			continue
		}
		until, err := testEnd(t.Spec, evs)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s %s: %v", t.File, t.Name, err))
			continue
		}
		if formats[r] == nil {
			formats[r] = map[string]bool{}
		}
		for f := range fs {
			formats[r][f] = true
		}
		for _, ev := range evs {
			samples[r] = append(samples[r], ev.value)
		}
		runs = append(runs, prepared{doc: t, rule: r, events: evs, until: until})
	}
	checked := map[*rule]bool{}
	for _, p := range runs {
		if checked[p.rule] {
			continue
		}
		checked[p.rule] = true
		types := make([]string, 0, len(formats[p.rule]))
		for f := range formats[p.rule] {
			types = append(types, f)
		}
		sort.Strings(types)
		if err := p.rule.check(rules.Infer(types, samples[p.rule])); err != nil {
			problems = append(problems, fmt.Sprintf("EventRule %s: %v", p.rule.name, err))
		}
	}
	if len(problems) > 0 {
		return nil, &Error{Problems: problems}
	}
	results := make([]Result, 0, len(runs))
	for _, p := range runs {
		results = append(results, run(ctx, p.doc, p.rule, windows, p.events, p.until))
	}
	return results, nil
}

// Report writes results and returns how many tests failed.
func Report(w io.Writer, results []Result, verbose bool) int {
	failed := 0
	for _, r := range results {
		status := "ok  "
		if len(r.Failures) > 0 {
			status = "FAIL"
			failed++
		} else if !verbose {
			continue
		}
		fmt.Fprintf(w, "%s  %s  %s (%d events)\n", status, r.File, r.Name, r.Events)
		for _, f := range r.Failures {
			fmt.Fprintf(w, "        %s\n", strings.ReplaceAll(f, "\n", "\n          "))
		}
		for _, n := range r.Notes {
			fmt.Fprintf(w, "        note: %s\n", n)
		}
	}
	noun := "tests"
	if len(results) == 1 {
		noun = "test"
	}
	fmt.Fprintf(w, "%d %s, %d passed, %d failed\n", len(results), noun, len(results)-failed, failed)
	return failed
}

// endOfTime ends a test that doesn't say when: every window ends.
var endOfTime = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// testEnd is when a test ends: until, after its start, or when every
// window has ended.
func testEnd(spec map[string]any, evs []event) (time.Time, error) {
	v, ok := spec["until"]
	if !ok {
		return endOfTime, nil
	}
	start := DefaultStart
	if s, ok := spec["start"]; ok {
		start, _ = stamp(s)
	}
	t, err := when(start, v)
	if err != nil {
		return t, fmt.Errorf("until: %v", err)
	}
	if len(evs) > 0 && t.Before(evs[len(evs)-1].received) {
		return t, errors.New("until: a test ends after its last event")
	}
	return t, nil
}

func run(ctx context.Context, doc *gitops.Document, r *rule, windows []window, events []event, until time.Time) Result {
	res := Result{File: doc.File, Name: doc.Name, Events: len(events)}
	details := map[int]map[string]any{}
	sr := rules.SimRule{Filter: r.filter, Key: r.key, Throttle: r.throttle, Window: time.Duration(r.window) * time.Second,
		BreakerFirings: r.breakerFirings, BreakerWindow: time.Duration(r.breakerWin) * time.Second}
	if r.pattern != "" {
		sr.Pattern = rules.Pattern{Kind: r.pattern, Count: int(r.patternCount), Conditions: len(r.conditions),
			Window: time.Duration(r.patternWin) * time.Second}
		if r.pattern != rules.PatternCount {
			sr.Conditions = r.conditions
		}
	}
	sim := rules.NewSimulator(sr,
		func(ms []rules.Member, at time.Time) rules.Outcome {
			out := r.act(ctx, windows, ms, at)
			for _, m := range ms {
				details[m.Index] = out.Details
			}
			return out
		})
	for _, ev := range events {
		sim.Add(ctx, rules.SimEvent{Value: ev.value, Received: ev.received})
	}
	sim.Finish(until)
	for _, w := range windows {
		if w.groups && len(w.rules) == 0 {
			res.Notes = append(res.Notes, fmt.Sprintf("window %s covers inventory groups, which only the server knows: not checked", w.name))
		}
	}
	for i, ev := range events {
		if ev.expect == nil {
			continue
		}
		d := sim.Decisions[i]
		if msg := ev.expect.compare(d, details[i]); msg != "" {
			res.Failures = append(res.Failures, fmt.Sprintf("%s: %s", ev.label, msg))
		}
	}
	return res
}

// describe is a decision as a failure tells it.
func describe(d rules.SimDecision) string {
	s := d.Outcome
	if d.Guardrail != "" {
		s += " (" + d.Guardrail + ")"
	}
	if d.Key != "" {
		s += fmt.Sprintf(", key %q", d.Key)
	}
	reason := d.Reason
	if d.Miss != nil {
		reason = d.Miss.String()
	}
	if reason != "" {
		s += ": " + reason
	}
	return s
}

var blastRadius = map[string]bool{"max_concurrent_runs": true, "host_lock": true, "group_share": true}

func (e *expectation) compare(d rules.SimDecision, details map[string]any) string {
	if blastRadius[e.guardrail] {
		return fmt.Sprintf("the %s guardrail needs the jobs running on the server: awxnext rules test doesn't check it", e.guardrail)
	}
	got := d.Outcome
	if e.outcome != got {
		return fmt.Sprintf("want %s, got %s", e.outcome, describe(d))
	}
	var diffs []string
	if e.key != "" && e.key != d.Key {
		diffs = append(diffs, fmt.Sprintf("key %q, want %q", d.Key, e.key))
	}
	if e.guardrail != "" && e.guardrail != d.Guardrail {
		diffs = append(diffs, fmt.Sprintf("guardrail %q, want %q", d.Guardrail, e.guardrail))
	}
	reason := d.Reason
	if d.Miss != nil {
		reason = d.Miss.String()
	}
	if e.reason != "" && !strings.Contains(reason, e.reason) {
		diffs = append(diffs, fmt.Sprintf("reason %q does not contain %q", reason, e.reason))
	}
	for k, want := range e.extraVars {
		vars, _ := details["extra_vars"].(map[string]any)
		if v, ok := vars[k]; !ok || !same(v, want) {
			diffs = append(diffs, fmt.Sprintf("extra_vars.%s %s, want %s", k, show(v), show(want)))
		}
	}
	if e.limit != nil && !same(details["limit"], *e.limit) {
		diffs = append(diffs, fmt.Sprintf("limit %s, want %q", show(details["limit"]), *e.limit))
	}
	if e.message != nil && !same(details["message"], e.message) {
		diffs = append(diffs, fmt.Sprintf("message %s, want %s", show(details["message"]), show(e.message)))
	}
	if e.eventType != nil && !same(details["event_type"], *e.eventType) {
		diffs = append(diffs, fmt.Sprintf("event_type %s, want %q", show(details["event_type"]), *e.eventType))
	}
	if e.eventData != nil && !same(details["event_data"], e.eventData) {
		diffs = append(diffs, fmt.Sprintf("event_data %s, want %s", show(details["event_data"]), show(e.eventData)))
	}
	if len(diffs) == 0 {
		return ""
	}
	return d.Outcome + " as expected, but " + strings.Join(diffs, "; ")
}

// same compares values as JSON has them: 1 and 1.0 alike.
func same(a, b any) bool {
	return reflect.DeepEqual(normal(a), normal(b))
}

func normal(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	json.Unmarshal(b, &out)
	return out
}

func show(v any) string {
	if v == nil {
		return "none"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// act evaluates the action's expressions and applies the windows and the
// mode, as the engine's guard does before the blast radius: the windows at
// the first event's time, or when the rule's pattern fired.
func (r *rule) act(ctx context.Context, windows []window, ms []rules.Member, firedAt time.Time) rules.Outcome {
	vars := rules.Vars{Event: ms[0].Event.Value}
	for _, m := range ms {
		vars.Events = append(vars.Events, m.Event.Value)
	}
	details := map[string]any{}
	fail := func(what string, err error) rules.Outcome {
		return rules.Outcome{Name: rules.Error, Reason: what + " could not be evaluated: " + err.Error(), Details: details}
	}
	switch r.action {
	case "launch":
		names := make([]string, 0, len(r.extraVars))
		for k := range r.extraVars {
			names = append(names, k)
		}
		sort.Strings(names)
		if len(names) > 0 {
			ev := map[string]any{}
			for _, k := range names {
				v, err := rules.Eval(ctx, r.extraVars[k], vars)
				if err != nil {
					return fail("extra_vars."+k, err)
				}
				ev[k] = v
			}
			details["extra_vars"] = ev
		}
		if r.limit != "" {
			l, err := rules.Text(ctx, r.limit, vars)
			if err != nil {
				return fail("The limit", err)
			}
			details["limit"] = l
		}
	case "notify":
		if r.message != "" {
			v, err := rules.Eval(ctx, r.message, vars)
			if err != nil {
				return fail("The message", err)
			}
			details["message"] = v
		}
	case "post_event":
		t, err := rules.Text(ctx, r.eventType, vars)
		if err != nil {
			return fail("The event type", err)
		}
		details["event_type"] = t
		if r.eventData != "" {
			v, err := rules.Eval(ctx, r.eventData, vars)
			if err != nil {
				return fail("The event data", err)
			}
			details["event_data"] = v
		} else {
			details["event_data"] = ms[0].Event.Value["data"]
		}
	}
	approval := r.mode == "approval"
	at := ms[0].Event.Received
	if r.pattern != "" {
		at = firedAt
	}
	for _, w := range windows {
		if !w.covers(r.name) {
			continue
		}
		rr, err := schedule.Parse(w.rrule)
		if err != nil {
			continue
		}
		_, end, active := rr.Active(at, time.Duration(w.duration)*time.Second)
		if !active {
			continue
		}
		guardrail, what := "maintenance_window", "maintenance window"
		if w.kind == "freeze" {
			guardrail, what = "change_freeze", "change freeze"
		}
		reason := fmt.Sprintf("Inside %s %s, until %s.", what, w.name, end.UTC().Format(time.RFC3339))
		details["window"] = map[string]any{"name": w.name, "kind": w.kind, "until": end.UTC().Format(time.RFC3339)}
		if r.windowAction != "approval" {
			return rules.Outcome{Name: rules.Held, Guardrail: guardrail, Reason: reason, Details: details}
		}
		return rules.Outcome{Name: rules.Approval, Guardrail: guardrail, Reason: reason, Details: details}
	}
	if approval {
		return rules.Outcome{Name: rules.Approval, Reason: "The rule is in approval mode.", Details: details}
	}
	return rules.Outcome{Name: rules.Fired, Details: details}
}

// covers says whether a window covers the rule offline: by naming it, or
// by naming no rule and no group (its organization's).
func (w window) covers(name string) bool {
	if len(w.rules) == 0 {
		return !w.groups
	}
	for _, r := range w.rules {
		if r == name {
			return true
		}
	}
	return false
}

func (r *rule) check(s *rules.Schema) error {
	var errs []string
	check := func(field, expr string, want rules.Want) {
		if err := rules.Check(s, expr, want); err != nil {
			errs = append(errs, field+": "+err.Error())
		}
	}
	check("filter", r.filter, rules.Bool)
	for i, c := range r.conditions {
		check(fmt.Sprintf("conditions[%d]", i), c, rules.Bool)
	}
	if r.key != "" {
		check("key", r.key, rules.String)
	}
	switch r.action {
	case "launch":
		names := make([]string, 0, len(r.extraVars))
		for k := range r.extraVars {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			check("extra_vars."+k, r.extraVars[k], rules.Any)
		}
		if r.limit != "" {
			check("limit", r.limit, rules.String)
		}
	case "notify":
		if r.message != "" {
			check("message", r.message, rules.Any)
		}
	case "post_event":
		check("event_type", r.eventType, rules.String)
		if r.eventData != "" {
			check("event_data", r.eventData, rules.Any)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func loadRule(d *gitops.Document) (*rule, error) {
	s := d.Spec
	r := &rule{name: d.Name, action: "launch", mode: "shadow", windowAction: "hold", breakerWin: 3600, extraVars: map[string]string{}}
	var errs []string
	text := func(field string, into *string) {
		switch v := s[field].(type) {
		case nil:
		case string:
			*into = v
		default:
			errs = append(errs, field+": expected a string")
		}
	}
	number := func(field string, into *int64, lo, hi int64) {
		switch v := s[field].(type) {
		case nil:
		case int:
			if int64(v) < lo || int64(v) > hi {
				errs = append(errs, fmt.Sprintf("%s: expected a number from %d to %d", field, lo, hi))
				return
			}
			*into = int64(v)
		default:
			errs = append(errs, field+": expected a whole number")
		}
	}
	text("filter", &r.filter)
	text("key", &r.key)
	text("throttle", &r.throttle)
	text("action", &r.action)
	text("limit", &r.limit)
	text("message", &r.message)
	text("event_type", &r.eventType)
	text("event_data", &r.eventData)
	text("mode", &r.mode)
	text("window_action", &r.windowAction)
	number("throttle_window", &r.window, 0, 7*24*3600)
	number("breaker_firings", &r.breakerFirings, 0, 10000)
	number("breaker_window", &r.breakerWin, 1, 7*24*3600)
	text("pattern", &r.pattern)
	number("pattern_count", &r.patternCount, 0, rules.MaxPatternEvents)
	number("pattern_window", &r.patternWin, 0, 7*24*3600)
	switch v := s["conditions"].(type) {
	case nil:
	case []any:
		for i, c := range v {
			e, isText := c.(string)
			if !isText || strings.TrimSpace(e) == "" {
				errs = append(errs, fmt.Sprintf("conditions[%d]: expected a CEL condition", i))
				continue
			}
			r.conditions = append(r.conditions, e)
		}
	default:
		errs = append(errs, "conditions: expected a list of CEL conditions")
	}
	if m, ok := s["extra_vars"].(map[string]any); ok {
		for k, v := range m {
			e, isText := v.(string)
			if !isText {
				errs = append(errs, "extra_vars."+k+": expected a CEL expression")
				continue
			}
			r.extraVars[k] = e
		}
	}
	if strings.TrimSpace(r.filter) == "" {
		errs = append(errs, "filter: a rule needs a filter")
	}
	switch r.throttle {
	case "", rules.OnceWithin, rules.OnceAfter:
	default:
		errs = append(errs, fmt.Sprintf("throttle: %q is not none, %s or %s", r.throttle, rules.OnceWithin, rules.OnceAfter))
	}
	if r.throttle != "" && r.window < 1 {
		errs = append(errs, "throttle_window: a throttle needs a window of at least one second")
	}
	errs = append(errs, r.patternProblems()...)
	switch r.action {
	case "launch", "notify":
	case "post_event":
		if strings.TrimSpace(r.eventType) == "" {
			errs = append(errs, "event_type: a rule that posts events needs their type")
		}
	default:
		errs = append(errs, fmt.Sprintf("action: %q is not launch, notify or post_event", r.action))
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return r, nil
}

// patternProblems checks the rule's pattern as the server does.
func (r *rule) patternProblems() []string {
	n := len(r.conditions)
	var errs []string
	switch r.pattern {
	case "":
		if n > 0 {
			errs = append(errs, "conditions: only a rule with an all, sequence or absence pattern has conditions")
		}
		return errs
	case rules.PatternCount:
		if r.patternCount < 2 {
			errs = append(errs, "pattern_count: a count pattern waits for 2 events at least")
		}
		if n > 0 {
			errs = append(errs, "conditions: a count pattern has no conditions")
		}
	case rules.PatternAll, rules.PatternSequence:
		if n < 2 || n > rules.MaxConditions {
			errs = append(errs, fmt.Sprintf("conditions: an all or sequence pattern has 2 to %d conditions", rules.MaxConditions))
		}
	case rules.PatternAbsence:
		if n != 2 {
			errs = append(errs, "conditions: an absence pattern has 2 conditions")
		}
	default:
		return append(errs, fmt.Sprintf("pattern: %q is not none, count, all, sequence or absence", r.pattern))
	}
	if r.patternWin < 1 {
		errs = append(errs, "pattern_window: a pattern needs a window of at least one second")
	}
	if r.throttle != "" {
		errs = append(errs, "throttle: a rule with a pattern takes no throttle")
	}
	return errs
}

func loadWindow(d *gitops.Document) (window, error) {
	s := d.Spec
	w := window{name: d.Name, kind: "maintenance"}
	if k, ok := s["kind"].(string); ok {
		w.kind = k
	}
	if enabled, ok := s["enabled"].(bool); ok && !enabled {
		return window{name: d.Name}, nil
	}
	w.rrule, _ = s["rrule"].(string)
	if _, err := schedule.Parse(w.rrule); err != nil {
		return w, fmt.Errorf("rrule: %v", err)
	}
	n, ok := s["duration"].(int)
	if !ok || n < 1 {
		return w, errors.New("duration: expected a number of seconds")
	}
	w.duration = int64(n)
	if list, ok := s["event_rules"].([]any); ok {
		for _, x := range list {
			if name, ok := x.(string); ok {
				w.rules = append(w.rules, name)
			}
		}
	}
	list, _ := s["groups"].([]any)
	w.groups = len(list) > 0
	return w, nil
}

// loadEvents reads a test's events, and the stream formats their payloads
// are in.
func loadEvents(spec map[string]any) ([]event, map[string]bool, error) {
	start := DefaultStart
	if v, ok := spec["start"]; ok {
		t, ok := stamp(v)
		if !ok {
			return nil, nil, fmt.Errorf("start: expected a date and time, such as 2026-11-07T22:30:00Z")
		}
		start = t
	}
	mapping := map[string]string{}
	if m, ok := spec["mapping"].(map[string]any); ok {
		for k, v := range m {
			mapping[k] = fmt.Sprint(v)
		}
	}
	list, ok := spec["events"].([]any)
	if !ok || len(list) == 0 {
		return nil, nil, errors.New("events: a test needs a list of events")
	}
	formats := map[string]bool{}
	var out []event
	at := start
	for i, x := range list {
		e, ok := x.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("events[%d]: expected an event", i)
		}
		where := fmt.Sprintf("events[%d]", i)
		if v, ok := e["at"]; ok {
			t, err := when(start, v)
			if err != nil {
				return nil, nil, fmt.Errorf("%s.at: %v", where, err)
			}
			if t.Before(at) {
				return nil, nil, fmt.Errorf("%s.at: events are received in order: this one is before the one above", where)
			}
			at = t
		} else if i > 0 {
			at = at.Add(time.Second)
		}
		evs, format, err := cloudEvents(e, i, mapping)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", where, err)
		}
		formats[format] = true
		exps, err := expectations(e["expect"], len(evs))
		if err != nil {
			return nil, nil, fmt.Errorf("%s.expect: %v", where, err)
		}
		for j, ev := range evs {
			label := fmt.Sprintf("event %d (%s, at +%s)", i+1, ev.ID, at.Sub(start))
			if len(evs) > 1 {
				label = fmt.Sprintf("event %d.%d (%s, at +%s)", i+1, j+1, ev.ID, at.Sub(start))
			}
			stored := eventstreams.Stored{Event: ev, Sequence: int64(len(out) + 1), Received: at}
			out = append(out, event{label: label, value: rules.Value(stored), received: at, expect: exps[j]})
		}
	}
	return out, formats, nil
}

// when is an event's receipt: a duration after the test's start, or a
// date and time.
func when(start time.Time, v any) (time.Time, error) {
	if s, ok := v.(string); ok {
		if d, err := time.ParseDuration(s); err == nil {
			return start.Add(d), nil
		}
	}
	t, ok := stamp(v)
	if !ok {
		return time.Time{}, errors.New("expected a duration after the start, such as 90s or 10m, or a date and time")
	}
	return t, nil
}

// stamp reads a date and time, which YAML may have decoded already.
func stamp(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, true
	case string:
		t, err := time.Parse(time.RFC3339, x)
		return t, err == nil
	}
	return time.Time{}, false
}

// cloudEvents makes the events of a test's entry: a payload as a stream of
// a format receives it, or a CloudEvent's attributes and data.
func cloudEvents(e map[string]any, i int, mapping map[string]string) ([]eventstreams.Event, string, error) {
	if payload, ok := e["payload"]; ok {
		format, _ := e["format"].(string)
		if format == "" {
			return nil, "", errors.New("format: a payload needs the stream type it is in, such as alertmanager")
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, "", fmt.Errorf("payload: %v", err)
		}
		h := http.Header{"Content-Type": {"application/json"}}
		if hs, ok := e["headers"].(map[string]any); ok {
			for k, v := range hs {
				h.Set(k, fmt.Sprint(v))
			}
		}
		evs, err := eventstreams.Parse(format, eventstreams.Request{Header: h, Body: body, Source: testSource, Mapping: mapping})
		if err != nil {
			return nil, "", fmt.Errorf("payload: %v", err)
		}
		return evs, format, nil
	}
	str := func(k, def string) string {
		if v, ok := e[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return def
	}
	ev := eventstreams.Event{ID: str("id", fmt.Sprintf("e%d", i+1)), Source: str("source", testSource), Type: str("type", ""),
		Subject: str("subject", ""), DataContentType: "application/json", Extensions: map[string]any{}}
	if ev.Type == "" {
		return nil, "", errors.New("type: an event needs a type, or a payload and its format")
	}
	if v, ok := e["time"]; ok {
		t, ok := stamp(v)
		if !ok {
			return nil, "", errors.New("time: expected a date and time")
		}
		ev.Time = &t
	}
	if v, ok := e["data"]; ok {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, "", fmt.Errorf("data: %v", err)
		}
		ev.Data = b
	}
	if x, ok := e["extensions"].(map[string]any); ok {
		for k, v := range x {
			ev.Extensions[k] = v
		}
	}
	return []eventstreams.Event{ev}, eventstreams.TypeCloudEvents, nil
}

var outcomes = map[string]bool{rules.NotMatched: true, rules.Fired: true, rules.Approval: true, rules.Held: true,
	rules.Throttled: true, rules.Deduplicated: true, rules.Waiting: true, rules.Error: true,
	rules.Expired: true, rules.Cleared: true, rules.Ignored: true}

// expectations reads what is expected of an entry's n events: one
// expectation for all, or one each.
func expectations(v any, n int) ([]*expectation, error) {
	out := make([]*expectation, n)
	if v == nil {
		return out, nil
	}
	if list, ok := v.([]any); ok {
		if len(list) != n {
			return nil, fmt.Errorf("the entry makes %d events: expected one expectation, or %d", n, n)
		}
		for i, x := range list {
			e, err := expectation1(x)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %v", i, err)
			}
			out[i] = e
		}
		return out, nil
	}
	e, err := expectation1(v)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i] = e
	}
	return out, nil
}

func expectation1(v any) (*expectation, error) {
	e := &expectation{}
	switch x := v.(type) {
	case string:
		e.outcome = x
	case map[string]any:
		e.outcome, _ = x["outcome"].(string)
		e.key, _ = x["key"].(string)
		e.guardrail, _ = x["guardrail"].(string)
		e.reason, _ = x["reason"].(string)
		if m, ok := x["extra_vars"].(map[string]any); ok {
			e.extraVars = m
		}
		if l, ok := x["limit"].(string); ok {
			e.limit = &l
		}
		if t, ok := x["event_type"].(string); ok {
			e.eventType = &t
		}
		e.message, e.eventData = x["message"], x["event_data"]
	default:
		return nil, errors.New("expected an outcome, or an object with one")
	}
	if !outcomes[e.outcome] {
		names := make([]string, 0, len(outcomes))
		for o := range outcomes {
			names = append(names, o)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("outcome %q is not one of %s", e.outcome, strings.Join(names, ", "))
	}
	return e, nil
}
