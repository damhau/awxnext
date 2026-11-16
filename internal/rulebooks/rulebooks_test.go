package rulebooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/damhau/awxnext/internal/rules"
)

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// connectorEvent is an event as a connector posts it: ansible-rulebook's
// event is the CloudEvent's data.
func connectorEvent(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(data)
	return map[string]any{"id": data["id"], "source": "/api/v2/event_connectors/1/", "type": "generic", "data": rules.Data(b)}
}

// TestSemantics checks the translated conditions of testdata/semantics.yml
// against the events ansible-rulebook 1.3.2 matched with them.
func TestSemantics(t *testing.T) {
	text, err := os.ReadFile("testdata/semantics.yml")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string][]string
	readJSON(t, "testdata/semantics.expected.json", &expected)
	plan, err := Translate(text, Options{Name: "semantics.yml"})
	if err != nil {
		t.Fatal(err)
	}
	sets, _ := parse(text)
	payload := sets[0].Sources[0].Args["payload"].([]any)
	var events []map[string]any
	for _, p := range payload {
		events = append(events, connectorEvent(t, p.(map[string]any)))
	}
	byRule := map[string]Rule{}
	for _, r := range plan.Rules {
		byRule[r.Rule] = r
	}
	for name, want := range expected {
		r, ok := byRule[name]
		if !ok {
			_, ar := plan.Report.Lookup("sem", name, 0)
			t.Errorf("%s: not translated: %+v", name, ar)
			continue
		}
		var got []string
		for _, ev := range events {
			ok, err := rules.Match(context.Background(), r.Filter, rules.Vars{Event: ev})
			if err != nil {
				ok = false
			}
			if ok {
				got = append(got, ev["id"].(string))
			}
		}
		if want == nil {
			want = []string{}
		}
		if got == nil {
			got = []string{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: %s matched %v, ansible-rulebook %v", name, r.Filter, got, want)
		}
	}
}

func TestConditionParse(t *testing.T) {
	for _, c := range []struct{ in, cel string }{
		{`event.i == 1`, `(event.?data.?i.hasValue() && event.data.i == 1)`},
		{`event["a-b"].c != "x"`, `(event.?data[?"a-b"].?c.hasValue() && event.data["a-b"].c != "x")`},
		{`event.x == stop`, `(event.?data.?x.hasValue() && event.data.x == "stop")`},
		{`event.l[0] == -2`, `(event.?data.?l[?0].hasValue() && event.data.l[0] == -2)`},
		{`not (event.a == 1)`, `(event.?data.?a.hasValue() && !(event.?data.?a.hasValue() && event.data.a == 1))`},
		{`event.a is not defined`, `!(event.?data.?a.hasValue())`},
	} {
		n, err := parseCondition(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		got, err := (&condCtx{v: view{kind: KindConnector, base: "event"}}).boolean(n)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got != c.cel {
			t.Errorf("%s:\n got %s\nwant %s", c.in, got, c.cel)
		}
		if _, err := rules.Compile(got); err != nil {
			t.Errorf("%s: %v", got, err)
		}
	}
	for _, bad := range []string{`event.a ==`, `event.a is frobbed`, `(event.a == 1`, `event.a is select("~", 1)`} {
		if _, err := parseCondition(bad); err == nil {
			t.Errorf("%s: parsed", bad)
		}
	}
}

// TestViews checks where each source's events put what ansible-rulebook
// reads.
func TestViews(t *testing.T) {
	alert := map[string]any{"id": "x", "source": "http://am", "type": "alert.firing",
		"data": map[string]any{"status": "firing", "labels": map[string]any{"alertname": "Down", "instance": "web1:9100"}}}
	hook := map[string]any{"id": "y", "source": "/api/v2/event_streams/2/", "type": "generic", "data": map[string]any{"repo": "a"}}
	for _, c := range []struct {
		kind, cond string
		ev         map[string]any
		want       bool
	}{
		{KindAlertmanager, `event.alert.labels.alertname == "Down"`, alert, true},
		{KindAlertmanager, `event.meta.hosts contains "web1"`, alert, true},
		{KindAlertmanager, `event.alert.status == "resolved"`, alert, false},
		{KindAlertmanager, `event.i == 1`, alert, false},
		{KindWebhook, `event.payload.repo == "a"`, hook, true},
		{KindWebhook, `event.repo == "a"`, hook, false},
	} {
		n, err := parseCondition(c.cond)
		if err != nil {
			t.Fatal(err)
		}
		v := view{kind: c.kind, base: "event", hostPath: []string{"labels", "instance"}}
		expr, err := (&condCtx{v: v}).boolean(n)
		if err != nil {
			t.Errorf("%s: %v", c.cond, err)
			continue
		}
		got, err := rules.Match(context.Background(), expr, rules.Vars{Event: c.ev})
		if err != nil || got != c.want {
			t.Errorf("%s (%s): %v %v, want %v", c.cond, expr, got, err, c.want)
		}
	}
	v := view{kind: KindAlertmanager, base: "event", hostPath: []string{"labels", "instance"}}
	limit, err := rules.Text(context.Background(), v.limit("all"), rules.Vars{Event: alert})
	if err != nil || limit != "web1" {
		t.Errorf("limit %q %v", limit, err)
	}
	for _, kind := range []string{KindAlertmanager, KindWebhook, KindConnector} {
		v := view{kind: kind, base: "event", hostPath: []string{"labels", "instance"}}
		for _, ref := range []string{"event.meta.endpoint", "event.payload"} {
			n, _ := parseCondition(ref + ` == "x"`)
			if _, err := (&condCtx{v: v}).boolean(n); err == nil && kind != KindConnector && !(kind == KindWebhook && ref == "event.payload") {
				t.Errorf("%s %s: translated", kind, ref)
			}
		}
	}
}

// examples are the example rulebooks of ansible-rulebook and ansible.eda,
// vendored under testdata/examples.
func examples(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{"testdata/examples/ansible-rulebook-1.3.2", "testdata/examples/ansible.eda-2.13.0"} {
		m, err := filepath.Glob(filepath.Join(dir, "*.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(m) == 0 {
			t.Fatalf("no rulebook in %s", dir)
		}
		files = append(files, m...)
	}
	sort.Strings(files)
	return files
}

type golden struct {
	Error  string  `json:"error,omitempty"`
	Report *Report `json:"report,omitempty"`
	Rules  []Rule  `json:"rules,omitempty"`
}

// TestExamples translates every example rulebook: each rule is translated
// or says why not, and the reports match testdata/examples.golden.json
// (UPDATE_GOLDEN=1 writes it).
func TestExamples(t *testing.T) {
	out := map[string]golden{}
	vars := map[string]any{"repo": "https://github.com/example/app.git", "src_path": "/tmp/app", "ansible_eda": nil}
	for _, f := range examples(t) {
		text, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(filepath.Dir(f)) + "/" + filepath.Base(f)
		plan, err := Translate(text, Options{Name: filepath.Base(f), Variables: vars})
		if err != nil {
			out[name] = golden{Error: err.Error()}
			continue
		}
		for _, rs := range plan.Report.Rulesets {
			for _, s := range rs.Sources {
				if s.Status != Translated && len(s.Reasons) == 0 {
					t.Errorf("%s: source %s: %s without a reason", name, s.Name, s.Status)
				}
			}
			for _, r := range rs.Rules {
				for _, a := range r.Actions {
					if a.Status != Translated && a.Reason == "" {
						t.Errorf("%s: rule %s: action %s: not translated without a reason", name, r.Name, a.Action)
					}
				}
				if r.Status != Translated && len(r.Reasons) == 0 {
					missing := true
					for _, a := range r.Actions {
						if a.Reason != "" {
							missing = false
						}
					}
					if missing {
						t.Errorf("%s: rule %s: %s without a reason", name, r.Name, r.Status)
					}
				}
				if len(r.Actions) == 0 && len(r.Reasons) == 0 {
					t.Errorf("%s: rule %s: no action and no reason", name, r.Name)
				}
			}
		}
		made := map[string]bool{}
		for _, r := range plan.Rules {
			made[r.Key] = true
			if err := r.check(); err != nil {
				t.Errorf("%s: %s: %v", name, r.Key, err)
			}
		}
		for _, rs := range plan.Report.Rulesets {
			for _, rr := range rs.Rules {
				for i, a := range rr.Actions {
					key := "rule:" + rs.Name + "/" + rr.Name + "/" + strconv.Itoa(i)
					if a.Status == Translated && a.As != "decisions" && !made[key] {
						t.Errorf("%s: %s is translated but has no rule", name, key)
					}
				}
			}
		}
		out[name] = golden{Report: plan.Report, Rules: plan.Rules}
	}
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const path = "testdata/examples.golden.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Errorf("the reports differ from %s: run UPDATE_GOLDEN=1 go test ./internal/rulebooks/ and review the diff", path)
	}
}
