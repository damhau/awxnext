package rules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func sample(t *testing.T, js string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func alertEvent(t *testing.T) map[string]any {
	return sample(t, `{"id": "1", "source": "am", "type": "alert.firing", "specversion": "1.0",
		"data": {"status": "firing", "count": 3, "labels": {"alertname": "DiskFull", "instance": "web1", "severity": "critical"},
			"alerts": [{"name": "a", "value": 1.5}], "nested": {"ok": true, "n": 1}},
		"traceparent": "00-abc"}`)
}

func TestCheck(t *testing.T) {
	s := Infer([]string{"generic"}, []map[string]any{alertEvent(t)})
	for _, c := range []struct {
		expr string
		want Want
		err  string
	}{
		{`event.type == "alert.firing" && event.data.labels.severity == "critical"`, Bool, ""},
		{`event.data.labels.alertname + "/" + event.data.labels.instance`, String, ""},
		{`event.data.labels["app.kubernetes.io/name"] == "x"`, Bool, ""},
		{`event.data.nested["ok"] == true`, Bool, ""},
		{`"ok" in event.data.nested && size(event.data.nested) > 0`, Bool, ""},
		{`event.data.unknown.deeper == 3`, Bool, ""},
		{`event.data.count == 3`, Bool, ""},
		{`event.data.count > 2`, Bool, ""},
		{`event.data.count != 3 || 3 == event.data.count`, Bool, ""},
		{`event.data.count + 1 > 2`, Bool, ""},
		{`event.data.labels.severity + 1 > 2`, Bool, "no matching overload"},
		{`event.data.alerts.exists(a, a.value > 1)`, Bool, ""},
		{`events.map(e, e.data.labels.instance).join(",")`, String, ""},
		{`event.traceparent.startsWith("00")`, Bool, ""},
		{`has(event.data.labels.team)`, Bool, ""},
		{`event.data.?labels.?team.orValue("none") == "none"`, Bool, ""},
		{`event.data.labels.severity > 3`, Bool, "no matching overload"},
		{`event.data.labels.severity`, Bool, "must be true or false"},
		{`event.data.count`, String, "must be a string"},
		{`event.type.foo == 1`, Bool, "does not support field selection"},
		{`evnt.type == "x"`, Bool, "undeclared reference"},
		{`event.type ==`, Bool, "Syntax error"},
		{`event.data.nested.ok + 1`, Any, "no matching overload"},
	} {
		err := Check(s, c.expr, c.want)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.expr, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: got %v, want %q", c.expr, err, c.err)
		}
	}
}

func TestCheckWithoutEvents(t *testing.T) {
	am := Infer([]string{"alertmanager"}, nil)
	if err := Check(am, `event.data.labels.severity == "critical"`, Bool); err != nil {
		t.Error(err)
	}
	if err := Check(am, `event.data.labels.severity > 1`, Bool); err == nil {
		t.Error("alert labels are strings")
	}
	// The remediation rule of docs/operations.md.
	for expr, want := range map[string]Want{
		`event.type == "alert.firing" && event.data.labels.alertname == "ServiceDown"`: Bool,
		`event.data.labels.instance`:               String,
		`event.data.labels.instance.split(":")[0]`: String,
		`event.data.annotations.summary`:           Any,
	} {
		if err := Check(am, expr, want); err != nil {
			t.Errorf("%s: %v", expr, err)
		}
	}
	generic := Infer([]string{"generic"}, nil)
	if err := Check(generic, `event.data.anything.goes > 1`, Bool); err != nil {
		t.Error(err)
	}
	if err := Check(generic, `event.type > 1`, Bool); err == nil {
		t.Error("type is a string")
	}
}

func TestEval(t *testing.T) {
	ctx := context.Background()
	ev := alertEvent(t)
	v := Vars{Event: ev}
	ok, err := Match(ctx, `event.data.count == 3 && 3 == event.data.count && event.data.count != 4 && event.data.labels.severity == "critical"`, v)
	if err != nil || !ok {
		t.Fatalf("match: %v %v", ok, err)
	}
	if _, err := Match(ctx, `event.data.missing == 1`, v); err == nil {
		t.Error("a missing field is an error")
	}
	if ok, err := Match(ctx, `event.type == "x" && event.data.missing == 1`, v); err != nil || ok {
		t.Errorf("false && error: %v %v", ok, err)
	}
	key, err := Text(ctx, `event.data.labels.alertname + "/" + event.data.labels.instance`, v)
	if err != nil || key != "DiskFull/web1" {
		t.Fatalf("key %q %v", key, err)
	}
	out, err := Eval(ctx, `{"host": event.data.labels.instance, "n": event.data.count, "f": event.data.alerts[0].value, "all": events.size()}`, v)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["host"] != "web1" || m["n"] != int64(3) || m["f"] != 1.5 || m["all"] != int64(1) {
		t.Errorf("mapped %#v", m)
	}
	two := Vars{Event: ev, Events: []map[string]any{ev, sample(t, `{"data": {"labels": {"instance": "web2"}}}`)}}
	limit, err := Text(ctx, `events.map(e, e.data.labels.instance).join(",")`, two)
	if err != nil || limit != "web1,web2" {
		t.Errorf("limit %q %v", limit, err)
	}
	if _, err := Eval(ctx, `lists.range(1000).map(x, lists.range(1000).map(y, x + y)).size() > 0`, v); err == nil {
		t.Error("the cost limit stops a runaway expression")
	}
}
