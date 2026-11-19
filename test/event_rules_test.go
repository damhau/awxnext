package test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/resources"
)

type testAlert struct {
	name, instance, startsAt, fingerprint string
}

// alertGroup is an Alertmanager notification of firing alerts.
func alertGroup(alerts ...testAlert) string {
	var list []map[string]any
	for _, a := range alerts {
		list = append(list, map[string]any{"status": "firing", "fingerprint": a.fingerprint, "startsAt": a.startsAt, "endsAt": "0001-01-01T00:00:00Z",
			"labels":      map[string]any{"alertname": a.name, "instance": a.instance, "severity": "critical"},
			"annotations": map[string]any{"summary": a.name + " on " + a.instance}})
	}
	b, _ := json.Marshal(map[string]any{"version": "4", "groupKey": "{}:{}", "status": "firing", "receiver": "awxnext",
		"externalURL": "http://alertmanager:9093", "groupLabels": map[string]any{}, "commonLabels": map[string]any{}, "alerts": list})
	return string(b)
}

func newAlertStream(t *testing.T, a *client, org int64) eventStream {
	t.Helper()
	cred := streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"})
	return newEventStream(t, a, org, cred, eventstreams.TypeAlertmanager, nil)
}

func (s eventStream) alert(t *testing.T, stored int, alerts ...testAlert) {
	t.Helper()
	code, b, _ := s.post(t, alertGroup(alerts...), headers("Authorization", "Bearer tok", "Content-Type", "application/json"))
	if code != 202 || b["stored"] != float64(stored) {
		t.Fatalf("posting alerts: %d %v", code, b)
	}
}

// waitRules waits for the rule engine to have read a stream's events.
func waitRules(t *testing.T, stream int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var pos, last int64
		if err := app.DB.QueryRow(t.Context(), `SELECT coalesce((SELECT position FROM event_consumer_positions WHERE consumer = 'rules' AND stream_id = $1), -1),
			last_sequence FROM event_streams WHERE id = $1`, stream).Scan(&pos, &last); err != nil {
			t.Fatal(err)
		}
		if pos == last {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the rules never read stream %d", stream)
}

// decisions returns a rule's decisions as "sequence outcome".
func decisions(t *testing.T, a *client, rule int64) []string {
	t.Helper()
	var out []string
	for _, d := range must(t, a.get(fmt.Sprintf("event_rules/%d/decisions/?not__outcome=not_matched&order_by=sequence&page_size=200", rule)), 200).results() {
		out = append(out, fmt.Sprintf("%v %v", d["sequence"], d["outcome"]))
	}
	return out
}

func ruleJobs(t *testing.T, a *client, rule int64) []map[string]any {
	t.Helper()
	return must(t, a.get(fmt.Sprintf("jobs/?event_rule=%d&order_by=id", rule)), 200).results()
}

func jobVars(t *testing.T, job map[string]any) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(job["extra_vars"].(string)), &v); err != nil {
		t.Fatalf("extra_vars %v: %v", job["extra_vars"], err)
	}
	return v
}

// An alert launches the rule's template with the variables and limit its
// expressions map, once per key within the throttle's window; an event
// sent again, or read again, launches nothing more.
func TestEventRuleLaunch(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true, "ask_limit_on_launch": true})
	rule := must(t, a.post("event_rules/", map[string]any{"name": uniq("disk"), "organization": f.org, "streams": []int64{am.id}, "mode": "auto",
		"filter": `event.type == "alert.firing" && event.data.labels.alertname == "DiskFull"`,
		"key":    "event.data.labels.instance", "throttle": "once_within", "throttle_window": 3600,
		"unified_job_template": jt,
		"extra_vars": map[string]any{"target": "event.data.labels.instance", "summary": "event.data.annotations.summary",
			"level": `event.data.labels.severity == "critical" ? 1 : 2`},
		"limit": `event.data.labels.instance == "web-01" ? "h1" : "h2"`}), 201)
	rid := rule.id()
	if rule.Body["owner"] == nil || rule.Body["enabled"] != true {
		t.Fatalf("rule: %s", rule.Raw)
	}
	if s := rule.Body["summary_fields"].(map[string]any)["streams"].([]any); len(s) != 1 || s[0].(map[string]any)["id"] != float64(am.id) {
		t.Errorf("summary streams: %v", s)
	}

	am.alert(t, 2, testAlert{"DiskFull", "web-01", "2026-11-15T08:00:00Z", "f1"}, testAlert{"DiskFull", "web-02", "2026-11-15T08:05:00Z", "f2"})
	am.alert(t, 1, testAlert{"HighCPU", "web-03", "2026-11-15T08:06:00Z", "f3"})
	waitRules(t, am.id)
	jobs := ruleJobs(t, a, rid)
	if len(jobs) != 2 {
		t.Fatalf("jobs: %v", jobs)
	}
	for i, want := range []struct{ target, limit, id string }{{"web-01", "h1", "f1/2026-11-15T08:00:00Z/firing"}, {"web-02", "h2", "f2/2026-11-15T08:05:00Z/firing"}} {
		job := waitJob(t, a, fmt.Sprintf("jobs/%v/", jobs[i]["id"]))
		mustStatus(t, f, job, "successful")
		v := jobVars(t, job.Body)
		if v["target"] != want.target || v["summary"] != "DiskFull on "+want.target || v["level"] != float64(1) || job.Body["limit"] != want.limit {
			t.Errorf("job %d: vars %v, limit %v", i, v, job.Body["limit"])
		}
		if job.Body["launch_type"] != "manual" {
			t.Errorf("launch_type %v", job.Body["launch_type"])
		}
		sf := job.Body["summary_fields"].(map[string]any)
		if r, _ := sf["event_rule"].(map[string]any); r["id"] != float64(rid) {
			t.Errorf("summary event_rule: %v", sf["event_rule"])
		}
		if ev, _ := sf["stream_event"].(map[string]any); ev["stream"] != float64(am.id) || ev["sequence"] != float64(i+1) || ev["id"] != want.id || ev["type"] != "alert.firing" {
			t.Errorf("summary stream_event: %v", sf["stream_event"])
		}
	}

	// A new alert for web-01 within the hour is held back.
	am.alert(t, 1, testAlert{"DiskFull", "web-01", "2026-11-15T08:30:00Z", "f1"})
	waitRules(t, am.id)
	want := []string{"1 launched", "2 launched", "4 throttled"}
	if got := decisions(t, a, rid); !slices.Equal(got, want) {
		t.Fatalf("decisions %v, want %v", got, want)
	}

	// Sent again, the alerts are duplicates; read again, they are decided.
	am.alert(t, 0, testAlert{"DiskFull", "web-01", "2026-11-15T08:00:00Z", "f1"}, testAlert{"DiskFull", "web-02", "2026-11-15T08:05:00Z", "f2"})
	if _, err := app.DB.Exec(t.Context(), "UPDATE event_consumer_positions SET position = 0 WHERE consumer = 'rules' AND stream_id = $1", am.id); err != nil {
		t.Fatal(err)
	}
	resources.WakeRules(t.Context(), app.DB, am.id)
	waitRules(t, am.id)
	if got := decisions(t, a, rid); !slices.Equal(got, want) {
		t.Errorf("decisions read again %v, want %v", got, want)
	}
	if n := len(ruleJobs(t, a, rid)); n != 2 {
		t.Errorf("%d jobs after reading again", n)
	}

	wantDependents(t, "template", must2(usedBy(t, a, fmt.Sprintf("job_templates/%d/", jt))), fmt.Sprintf("event_rules/%d unified_job_template deleted", rid))
	wantDependents(t, "stream", must2(usedBy(t, a, fmt.Sprintf("event_streams/%d/", am.id))), fmt.Sprintf("event_rules/%d streams cleared", rid))
	must(t, a.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"enabled": false}), 200)
	if n := must(t, a.get(fmt.Sprintf("event_rules/%d/history/", rid)), 200).count(); n != 2 {
		t.Errorf("%d versions", n)
	}
	must(t, a.delete(fmt.Sprintf("job_templates/%d/", jt)), 204)
	must(t, a.get(fmt.Sprintf("event_rules/%d/", rid)), 404)
}

func must2(m map[string]bool, _ resp) map[string]bool { return m }

// A once_after rule waits for its window to end, then acts once with the
// first event of each key: here it posts an event to another stream.
func TestEventRuleOnceAfter(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	am := newAlertStream(t, a, org)
	target := newEventStream(t, a, org, streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "t2"}), eventstreams.TypeCloudEvents, nil)
	must(t, a.post("event_rules/", map[string]any{"name": uniq("loop"), "organization": org, "streams": []int64{am.id}, "filter": "true",
		"action": "post_event", "target_stream": am.id, "event_type": `"x"`}), 400)
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("batch"), "organization": org, "streams": []int64{am.id}, "mode": "auto",
		"filter": `event.type == "alert.firing"`, "key": "event.data.labels.instance", "throttle": "once_after", "throttle_window": 3,
		"action": "post_event", "target_stream": target.id, "event_type": `"remediation." + event.data.labels.alertname`,
		"event_data": `{"hosts": events.map(e, e.data.labels.instance), "first": event.data.labels.instance}`}), 201).id()

	am.alert(t, 2, testAlert{"DiskFull", "web-01", "2026-11-15T08:00:00Z", "f1"}, testAlert{"DiskFull", "web-02", "2026-11-15T08:05:00Z", "f2"})
	am.alert(t, 1, testAlert{"DiskFull", "web-01", "2026-11-15T08:10:00Z", "f1"})
	waitRules(t, am.id)
	if got, want := decisions(t, a, rid), []string{"1 waiting", "2 waiting", "3 deduplicated"}; !slices.Equal(got, want) {
		t.Fatalf("decisions %v, want %v", got, want)
	}
	deadline := time.Now().Add(20 * time.Second)
	for len(target.events(t, a)) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	evs := target.events(t, a)
	if len(evs) != 1 {
		t.Fatalf("posted events: %v", evs)
	}
	ev := evs[0]
	if ev["event_type"] != "remediation.DiskFull" || !strings.HasSuffix(fmt.Sprint(ev["source"]), fmt.Sprintf("/api/v2/event_rules/%d/", rid)) ||
		ev["event_id"] != fmt.Sprintf("rule-%d-%d-1", rid, am.id) {
		t.Errorf("posted event: %v", ev)
	}
	if d := ev["data"].(map[string]any); fmt.Sprint(d["hosts"]) != "[web-01 web-02]" || d["first"] != "web-01" {
		t.Errorf("posted data: %v", d)
	}
	if got, want := decisions(t, a, rid), []string{"1 posted", "2 posted", "3 deduplicated"}; !slices.Equal(got, want) {
		t.Errorf("decisions %v, want %v", got, want)
	}
}

// A rule notifies with the message its expression gives.
func TestEventRuleNotify(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	am := newAlertStream(t, a, org)
	rec, hook := newHookServer(t)
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("tell"), "organization": org, "streams": []int64{am.id}, "mode": "auto",
		"filter": "true", "action": "notify", "notification_template": nt,
		"message": `{"text": event.data.annotations.summary, "instance": event.data.labels.instance}`}), 201).id()
	am.alert(t, 1, testAlert{"DiskFull", "web-01", "2026-11-15T08:00:00Z", "f1"})
	reqs := rec.wait(t, 1)
	if b := reqs[0].Body; b["text"] != "DiskFull on web-01" || b["instance"] != "web-01" {
		t.Errorf("notification body: %v", b)
	}
	waitRules(t, am.id)
	if got := decisions(t, a, rid); !slices.Equal(got, []string{"1 notified"}) {
		t.Errorf("decisions %v", got)
	}

	// Slack posts a text message as it is.
	slack := newFakeSlack(t)
	snt := newSlackTemplate(t, a, org, map[string]any{"token": slackToken, "channels": []any{"#alerts"}}, nil)
	must(t, a.post("event_rules/", map[string]any{"name": uniq("tell"), "organization": org, "streams": []int64{am.id}, "mode": "auto",
		"filter": "true", "action": "notify", "notification_template": snt, "message": `event.data.annotations.summary + " (Slack)"`}), 201)
	am.alert(t, 1, testAlert{"DiskFull", "web-02", "2026-11-15T08:05:00Z", "f2"})
	if p := slack.take(t, 1)[0]; fmt.Sprint(p.Body) != "map[as_user:true channel:alerts text:DiskFull on web-02 (Slack)]" {
		t.Errorf("Slack post: %v", p.Body)
	}
}

// A rule's expressions are compiled and type-checked against the schema
// of its streams' recent events when it is saved.
func TestEventRuleChecks(t *testing.T) {
	a := adminClient(t)
	f := newLaunchFixture(t)
	org := f.org
	cred := streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"})
	ce := newEventStream(t, a, org, cred, eventstreams.TypeCloudEvents, nil)
	code, b, _ := ce.post(t, `{"specversion":"1.0","id":"e1","source":"/ci","type":"build.done","data":{"host":"web-01","count":3,"ratio":0.5,"tags":["a"]}}`,
		headers("Authorization", "Bearer tok", "Content-Type", "application/cloudevents+json"))
	if code != 202 {
		t.Fatalf("post: %d %v", code, b)
	}
	sch := must(t, a.get(fmt.Sprintf("event_streams/%d/schema/", ce.id)), 200).Body
	data := sch["event"].(map[string]any)["fields"].(map[string]any)["data"].(map[string]any)["fields"].(map[string]any)
	if sch["events"] != float64(1) || data["count"].(map[string]any)["type"] != "int" || data["host"].(map[string]any)["type"] != "string" ||
		data["tags"].(map[string]any)["type"] != "list" {
		t.Errorf("schema: %v", sch)
	}

	jt := f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true})
	fixed := f.template(t, "site.yml", nil)
	base := func(kv ...any) map[string]any {
		m := map[string]any{"name": uniq("r"), "organization": org, "streams": []int64{ce.id}, "filter": `event.data.host == "web-01"`, "unified_job_template": jt}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	refuse := func(field, contains string, kv ...any) {
		t.Helper()
		r := must(t, a.post("event_rules/", base(kv...)), 400)
		if msg := fmt.Sprint(r.Body[field]); !strings.Contains(msg, contains) {
			t.Errorf("%s: %s", field, r.Raw)
		}
	}
	refuse("filter", "no matching overload", "filter", `event.data.count + "x" == 1`)
	refuse("filter", "no matching overload", "filter", `event.data.host > 3`)
	refuse("filter", "Syntax error", "filter", `event.data.host ==`)
	refuse("filter", "true or false", "filter", `event.data.host`)
	refuse("filter", "undeclared reference", "filter", `evnt.type == "x"`)
	refuse("key", "string", "key", `event.data.count`)
	refuse("extra_vars", "target", "extra_vars", map[string]any{"target": `event.data.host + 1`})
	refuse("extra_vars", "not a variable name", "extra_vars", map[string]any{"bad name": `event.data.host`})
	refuse("throttle_window", "window", "throttle", "once_within")
	refuse("unified_job_template", "needs", "unified_job_template", nil)
	refuse("extra_vars", "does not prompt", "unified_job_template", fixed, "extra_vars", map[string]any{"target": "event.data.host"})
	refuse("limit", "does not prompt", "limit", `event.data.host`)
	refuse("extra_vars", "ansible_", "extra_vars", map[string]any{"ansible_user": `"root"`})
	refuse("streams", "at least one", "streams", []int64{})

	// Fields the events don't have are dynamic; numbers compare as numbers.
	must(t, a.post("event_rules/", base("filter", `event.data.unknown.deep == "x" || event.data.count > 2 || event.data.ratio < 1 || "a" in event.data.tags`,
		"key", `event.data.host + "/" + string(event.data.count)`)), 201)
	disabled := must(t, a.post("event_rules/", base("enabled", false)), 201)
	if disabled.Body["enabled"] != false {
		t.Errorf("disabled: %s", disabled.Raw)
	}
	// A stream deleted leaves its rules.
	other := newEventStream(t, a, org, cred, eventstreams.TypeGeneric, nil)
	two := must(t, a.post("event_rules/", base("streams", []int64{ce.id, other.id}, "filter", "true")), 201).id()
	must(t, a.delete(fmt.Sprintf("event_streams/%d/", other.id)), 204)
	if s := must(t, a.get(fmt.Sprintf("event_rules/%d/", two)), 200).Body["streams"]; fmt.Sprint(s) != fmt.Sprint([]any{float64(ce.id)}) {
		t.Errorf("streams after delete: %v", s)
	}
}

// A rule runs as its owner: saving it needs the right to its action, and a
// rule whose owner lost the right stops, with the reason in its decision.
func TestEventRuleRights(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	org := newOrg(t, a)
	user, uid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Admin"), "user": uid, "object_id": org}), 201)
	am := newAlertStream(t, a, org)
	jt := f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true})
	body := map[string]any{"name": uniq("r"), "organization": org, "streams": []int64{am.id}, "filter": "true", "unified_job_template": jt, "mode": "auto",
		"extra_vars": map[string]any{"target": "event.data.labels.instance"}}
	if r := must(t, user.post("event_rules/", body), 403); !strings.Contains(r.Raw, "may not use the template") {
		t.Errorf("refusal: %s", r.Raw)
	}
	asg := must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "JobTemplate Execute"), "user": uid, "object_id": jt}), 201).id()
	rule := must(t, user.post("event_rules/", body), 201)
	rid := rule.id()
	if rule.Body["owner"] != float64(uid) {
		t.Fatalf("owner %v, want %d", rule.Body["owner"], uid)
	}
	am.alert(t, 1, testAlert{"DiskFull", "web-01", "2026-11-15T08:00:00Z", "f1"})
	waitRules(t, am.id)
	jobs := ruleJobs(t, a, rid)
	if len(jobs) != 1 {
		t.Fatalf("jobs: %v", jobs)
	}
	job := waitJob(t, a, fmt.Sprintf("jobs/%v/", jobs[0]["id"]))
	if by := job.Body["summary_fields"].(map[string]any)["created_by"].(map[string]any); by["id"] != float64(uid) {
		t.Errorf("created_by %v", by)
	}

	must(t, a.delete(fmt.Sprintf("role_user_assignments/%d/", asg)), 204)
	am.alert(t, 1, testAlert{"DiskFull", "web-02", "2026-11-15T08:01:00Z", "f2"}, testAlert{"DiskFull", "web-03", "2026-11-15T08:02:00Z", "f3"})
	waitRules(t, am.id)
	if n := len(ruleJobs(t, a, rid)); n != 1 {
		t.Errorf("%d jobs after the right was revoked", n)
	}
	if got := decisions(t, a, rid); !slices.Equal(got, []string{"1 launched", "2 refused"}) {
		t.Errorf("decisions %v", got)
	}
	ds := must(t, a.get(fmt.Sprintf("event_rules/%d/decisions/?outcome=refused", rid)), 200).results()
	if len(ds) != 1 || !strings.Contains(fmt.Sprint(ds[0]["reason"]), "may not use the template") {
		t.Errorf("refusal: %v", ds)
	}
	if n := must(t, user.get(fmt.Sprintf("event_rule_decisions/?rule=%d", rid)), 200).count(); n != 2 {
		t.Errorf("the owner sees %d decisions", n)
	}
	stranger, _ := newUser(t, a)
	if n := must(t, stranger.get(fmt.Sprintf("event_rule_decisions/?rule=%d", rid)), 200).count(); n != 0 {
		t.Errorf("a stranger sees %d decisions", n)
	}
	rule = must(t, a.get(fmt.Sprintf("event_rules/%d/", rid)), 200)
	if rule.Body["enabled"] != false || !strings.Contains(fmt.Sprint(rule.Body["stopped_reason"]), "may not use the template") {
		t.Errorf("stopped rule: %s", rule.Raw)
	}
	must(t, user.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"enabled": true}), 403)
	must(t, user.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"description": "still off"}), 200)

	// The admin takes it over: the rule now runs as the admin, from the
	// events received from then on.
	rule = must(t, a.patch(fmt.Sprintf("event_rules/%d/", rid), map[string]any{"enabled": true}), 200)
	if rule.Body["owner"] == float64(uid) || rule.Body["stopped_reason"] != "" {
		t.Errorf("taken over: %s", rule.Raw)
	}
	am.alert(t, 1, testAlert{"DiskFull", "web-04", "2026-11-15T08:03:00Z", "f4"})
	waitRules(t, am.id)
	if got := decisions(t, a, rid); !slices.Equal(got, []string{"1 launched", "2 refused", "4 launched"}) {
		t.Errorf("decisions %v", got)
	}
	for _, j := range ruleJobs(t, a, rid) {
		waitJob(t, a, fmt.Sprintf("jobs/%v/", j["id"]))
	}
}

// A node that fails before its batch commits leaves nothing behind: the
// node that takes the stream over decides each event once.
func TestEventRuleFailover(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	am := newAlertStream(t, a, f.org)
	jt := f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true})
	rid := must(t, a.post("event_rules/", map[string]any{"name": uniq("r"), "organization": f.org, "streams": []int64{am.id}, "filter": "true",
		"mode": "auto", "unified_job_template": jt, "extra_vars": map[string]any{"target": "event.data.labels.instance"}}), 201).id()

	var failures atomic.Int32
	resources.Rules.SetBeforeCommit(func(stream int64) error {
		if stream == am.id {
			failures.Add(1)
			return errors.New("the node stopped")
		}
		return nil
	})
	t.Cleanup(func() { resources.Rules.SetBeforeCommit(nil) })
	am.alert(t, 3, testAlert{"DiskFull", "web-01", "2026-11-15T08:00:00Z", "f1"}, testAlert{"DiskFull", "web-02", "2026-11-15T08:00:00Z", "f2"},
		testAlert{"DiskFull", "web-03", "2026-11-15T08:00:00Z", "f3"})
	deadline := time.Now().Add(20 * time.Second)
	for failures.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if failures.Load() == 0 {
		t.Fatal("the node never tried the batch")
	}
	if n := len(ruleJobs(t, a, rid)); n != 0 || len(decisions(t, a, rid)) != 0 {
		t.Fatalf("the failed batch left %d jobs and decisions %v", n, decisions(t, a, rid))
	}

	b := resources.NewRuleEngine(app, "test-node-b")
	t.Cleanup(b.Close)
	for {
		held, err := b.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(held, am.id) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second node never took the stream")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n, err := b.Step(t.Context(), am.id); err != nil || n != 3 {
		t.Fatalf("step: %d, %v", n, err)
	}
	if n, err := b.Step(t.Context(), am.id); err != nil || n != 0 {
		t.Fatalf("second step: %d, %v", n, err)
	}
	ds := must(t, a.get(fmt.Sprintf("event_rules/%d/decisions/?order_by=sequence", rid)), 200).results()
	if len(ds) != 3 {
		t.Fatalf("decisions: %v", ds)
	}
	for _, d := range ds {
		if d["outcome"] != "launched" || d["node"] != "test-node-b" {
			t.Errorf("decision %v", d)
		}
	}
	if n := len(ruleJobs(t, a, rid)); n != 3 {
		t.Errorf("%d jobs", n)
	}

	// The first node takes the stream back once the second lets it go.
	resources.Rules.SetBeforeCommit(nil)
	b.Close()
	am.alert(t, 1, testAlert{"DiskFull", "web-04", "2026-11-15T08:00:00Z", "f4"})
	waitRules(t, am.id)
	ds = must(t, a.get(fmt.Sprintf("event_rules/%d/decisions/?sequence=4", rid)), 200).results()
	if len(ds) != 1 || ds[0]["node"] != app.Cfg.NodeName {
		t.Errorf("decision after failback: %v", ds)
	}
	for _, j := range ruleJobs(t, a, rid) {
		waitJob(t, a, fmt.Sprintf("jobs/%v/", j["id"]))
	}
}

// A GitOps source declares rules, their streams and template by name.
func TestEventRuleGitOps(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	cred := streamCredential(t, a, f.org, eventstreams.CredToken, map[string]any{"token": "tok"})
	stream := newEventStream(t, a, f.org, cred, eventstreams.TypeAlertmanager, nil)
	streamName := a.get(fmt.Sprintf("event_streams/%d/", stream.id)).Body["name"].(string)
	jt := f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true})
	jtName := a.get(fmt.Sprintf("job_templates/%d/", jt)).Body["name"].(string)
	doc := fmt.Sprintf(`kind: EventRule
name: Disk full
description: restart the disk cleaner
spec:
  streams: [%s]
  filter: event.type == "alert.firing"
  key: event.data.labels.instance
  throttle: once_within
  throttle_window: 600
  template: {kind: JobTemplate, name: %s}
  extra_vars:
    target: event.data.labels.instance
`, streamName, jtName)
	repo := gitRepo(t, map[string]string{"site.yml": helloPlaybook, "gitops/rules.yml": doc})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": f.org, "scm_type": "git", "scm_url": "file://" + repo}), 201).id()
	waitProjectSynced(t, a, proj, "")
	sid := must(t, a.post("gitops_sources/", map[string]any{"name": uniq("src"), "organization": f.org, "project": proj, "path": "gitops",
		"sync_on_project_update": false}), 201).id()
	rep := must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), nil), 202)
	if summary(rep, "create") != 1 || summary(rep, "error") != 0 {
		t.Fatalf("sync: %s", rep.Raw)
	}
	rule := one(t, a, fmt.Sprintf("event_rules/?organization=%d&name=%s", f.org, url.QueryEscape("Disk full")))
	if rule["managed_by"] != fmt.Sprintf("gitops:%d", sid) || fmt.Sprint(rule["streams"]) != fmt.Sprint([]any{float64(stream.id)}) ||
		rule["unified_job_template"] != float64(jt) || rule["owner"] == nil || rule["throttle_window"] != float64(600) {
		t.Errorf("rule: %v", rule)
	}
	rep = must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), nil), 202)
	if summary(rep, "update") != 0 || summary(rep, "unchanged") != 1 {
		t.Errorf("second sync: %s", rep.Raw)
	}
}
