package test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/eventstreams"
)

// The rule editor's check type-checks a draft as a save does and runs it on
// the streams' latest events, test mode's included: which match, the part
// of the filter that failed for the others, and what the action would get.
func TestEventRuleCheck(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	cred := streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"})
	am := newEventStream(t, a, org, cred, eventstreams.TypeAlertmanager, map[string]any{"test_mode": true})
	am.alert(t, 3, testAlert{"DiskFull", "h1", "2026-11-15T08:00:00Z", "d1"}, testAlert{"HighCPU", "h2", "2026-11-15T08:01:00Z", "c1"},
		testAlert{"DiskFull", "h3", "2026-11-15T08:02:00Z", "d2"})

	check := func(body map[string]any, status int) map[string]any {
		t.Helper()
		body["streams"] = []int64{am.id}
		return must(t, a.post("event_rules/check/", body), status).Body
	}
	r := check(map[string]any{"filter": diskFull, "key": "event.data.labels.instance",
		"extra_vars": map[string]any{"host": "event.data.labels.instance", "summary": "event.data.annotations.summary"},
		"limit":      `event.data.labels.instance + ",backup"`}, 200)
	if r["checked"] != float64(3) || r["matched"] != float64(2) || fmt.Sprint(r["errors"]) != "map[]" || r["schema_events"] != float64(3) {
		t.Fatalf("check: %v", r)
	}
	events := r["events"].([]any)
	var got []string
	for _, e := range events {
		ev := e.(map[string]any)
		if ev["test_mode"] != true || ev["stream"] != float64(am.id) {
			t.Errorf("event %v", ev)
		}
		got = append(got, fmt.Sprintf("%v %v %v", ev["sequence"], ev["matched"], ev["key"]))
	}
	if want := "[3 true h3 2 false <nil> 1 true h1]"; fmt.Sprint(got) != want {
		t.Errorf("events, newest first: %v, want %s", got, want)
	}
	first := events[0].(map[string]any)
	if a := first["action"].(map[string]any); fmt.Sprint(a["extra_vars"]) != "map[host:h3 summary:DiskFull on h3]" || a["limit"] != "h3,backup" {
		t.Errorf("the action's variables: %v", a)
	}
	miss := events[1].(map[string]any)
	if f := miss["filter"].(map[string]any); f["expression"] != `event.data.labels.alertname == "DiskFull"` || !strings.Contains(fmt.Sprint(f["values"]), "HighCPU") ||
		!strings.Contains(fmt.Sprint(miss["reason"]), "is false") {
		t.Errorf("the miss: %v", miss)
	}

	// Errors as a save gives them, by field; the events are listed still.
	r = check(map[string]any{"filter": `event.data.labels.alertname > 3`, "key": "event.data.labels", "action": "post_event",
		"event_type": `"x"`, "event_data": "event.data +", "limit": "ignored: not the action's"}, 200)
	errs := r["errors"].(map[string]any)
	if !strings.Contains(fmt.Sprint(errs["filter"]), "no matching overload") || !strings.Contains(fmt.Sprint(errs["key"]), "a string") ||
		!strings.Contains(fmt.Sprint(errs["event_data"]), "Syntax error") || errs["event_type"] != nil || errs["limit"] != nil {
		t.Errorf("errors: %v", errs)
	}
	if r["matched"] != float64(0) || r["checked"] != float64(3) || r["events"].([]any)[0].(map[string]any)["matched"] != nil {
		t.Errorf("with a filter that doesn't compile: %v", r)
	}
	// An expression that fails on an event fails there only.
	r = check(map[string]any{"filter": "true", "events": 1, "extra_vars": map[string]any{"x": "event.data.nothere.deep"}}, 200)
	if e := r["events"].([]any); len(e) != 1 || !strings.Contains(fmt.Sprint(e[0].(map[string]any)["action_errors"]), "extra_vars.x") {
		t.Errorf("an evaluation error: %v", r)
	}

	check(map[string]any{"filter": "true", "events": 0}, 400)
	check(map[string]any{"filter": "true", "action": "reboot"}, 400)
	check(map[string]any{"extra_vars": map[string]any{"bad name": "1"}}, 400)
	must(t, a.post("event_rules/check/", map[string]any{"streams": []int64{}}), 400)
	must(t, a.post("event_rules/check/", map[string]any{"streams": []int64{999999}}), 400)
	stranger, _ := newUser(t, a)
	must(t, stranger.post("event_rules/check/", map[string]any{"streams": []int64{am.id}, "filter": "true"}), 403)
}
