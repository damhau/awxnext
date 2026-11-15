package rules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFilterMiss(t *testing.T) {
	ev := alertEvent(t)
	ev["data"].(map[string]any)["text"] = strings.Repeat("x", 1000)
	for _, c := range []struct {
		expr   string
		match  bool
		part   string
		err    string
		values []string
	}{
		{expr: `event.type == "alert.firing" && event.data.labels.severity == "critical"`, match: true},
		{expr: `event.type == "alert.firing" && event.data.labels.severity == "warning"`,
			part: `event.data.labels.severity == "warning"`, values: []string{`event.data.labels.severity = "critical"`}},
		{expr: `event.type == "alert.resolved" && event.data.count > 2`,
			part: `event.type == "alert.resolved"`, values: []string{`event.type = "alert.firing"`}},
		{expr: `event.type == "alert.firing" && (event.data.count > 5 || event.data.labels.instance == "web2")`,
			part:   `event.data.count > 5 || event.data.labels.instance == "web2"`,
			values: []string{`event.data.count = 3`, `event.data.labels.instance = "web1"`}},
		{expr: `event.data.labels.missing == "x" && event.type == "y"`,
			part: `event.type == "y"`, values: []string{`event.type = "alert.firing"`}},
		{expr: `event.data.labels.missing == "x" && event.type == "alert.firing"`,
			part: `event.data.labels.missing == "x"`, err: "no such key: missing"},
		{expr: `has(event.data.labels.team) && event.type == "alert.firing"`,
			part: `has(event.data.labels.team)`, values: []string{`event.data.labels is a map with the keys alertname, instance, severity`}},
		{expr: `event.data.alerts.exists(a, a.value > 2)`,
			part: `event.data.alerts.exists(a, a.value > 2)`, values: []string{`event.data.alerts = [{"name":"a","value":1.5}]`}},
		{expr: `event.data.text.startsWith("y")`,
			part: `event.data.text.startsWith("y")`, values: []string{`event.data.text is a string of 1000 characters`}},
		{expr: `!(event.data["nested"]["ok"])`,
			part: `!(event.data["nested"]["ok"])`, values: []string{`event.data["nested"]["ok"] = true`}},
		{expr: `event.data.labels.severity`, part: `event.data.labels.severity`, err: "not true or false"},
		{expr: `event.type ==`, part: `event.type ==`, err: "Syntax error"},
	} {
		ok, miss := Filter(context.Background(), c.expr, Vars{Event: ev})
		if ok != c.match {
			t.Errorf("%s: match %v, want %v (%+v)", c.expr, ok, c.match, miss)
			continue
		}
		if c.match {
			if miss != nil {
				t.Errorf("%s: a match has a miss: %+v", c.expr, miss)
			}
			continue
		}
		if miss.Expression != c.part {
			t.Errorf("%s: part %q, want %q", c.expr, miss.Expression, c.part)
		}
		if miss.Start >= 0 && string([]rune(c.expr)[miss.Start:miss.End]) != miss.Expression {
			t.Errorf("%s: offsets %d-%d don't hold %q", c.expr, miss.Start, miss.End, miss.Expression)
		}
		if !strings.Contains(miss.Error, c.err) || (c.err == "") != (miss.Error == "") {
			t.Errorf("%s: error %q, want %q", c.expr, miss.Error, c.err)
		}
		reason := miss.String()
		if !strings.HasPrefix(miss.Reason(), "The filter's condition "+c.part) {
			t.Errorf("%s: reason %q", c.expr, miss.Reason())
		}
		for _, v := range c.values {
			if !strings.Contains(reason, v) {
				t.Errorf("%s: reason %q lacks %q", c.expr, reason, v)
			}
		}
		b, err := json.Marshal(miss.Details())
		if err != nil || len(b) > 2048 {
			t.Errorf("%s: details %s (%v)", c.expr, b, err)
		}
	}
}
