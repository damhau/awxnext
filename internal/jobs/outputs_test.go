package jobs

import (
	"fmt"
	"testing"
)

func TestHideOutputs(t *testing.T) {
	o := &Output{}
	o.HideOutputs("db_password", "pin")
	stats := map[string]any{"ansible_stats": map[string]any{"aggregate": true, "per_host": false,
		"data": map[string]any{"db_password": "Tr0ub4dor&3", "pin": "123", "db_host": "db1"}}}
	task := Event{Event: "runner_on_ok", Stdout: `ok: [localhost] => {"ansible_stats": {"data": {"db_password": "Tr0ub4dor&3"}}}`,
		EventData: map[string]any{"res": stats}}
	e := o.hideOutputs(task)
	e = o.masked(e)
	if got := fmt.Sprint(e.EventData); got != "map[res:map[ansible_stats:map[aggregate:true data:map[db_host:db1 db_password:$encrypted$ pin:$encrypted$] per_host:false]]]" {
		t.Errorf("event data: %s", got)
	}
	if e.Stdout != `ok: [localhost] => {"ansible_stats": {"data": {"db_password": "$encrypted$"}}}` {
		t.Errorf("stdout: %s", e.Stdout)
	}
	if stats["ansible_stats"].(map[string]any)["data"].(map[string]any)["db_password"] != "Tr0ub4dor&3" {
		t.Error("the runner's data changed")
	}

	loop := Event{Event: "runner_on_ok", EventData: map[string]any{"res": map[string]any{"results": []any{stats}}}}
	if got := fmt.Sprint(o.hideOutputs(loop).EventData); got != "map[res:map[results:[map[ansible_stats:map[aggregate:true data:map[db_host:db1 db_password:$encrypted$ pin:$encrypted$] per_host:false]]]]]" {
		t.Errorf("loop: %s", got)
	}
	end := Event{Event: "playbook_on_stats", EventData: map[string]any{
		"artifact_data": map[string]any{"db_password": "Tr0ub4dor&3", "db_host": "db1"},
		"custom":        map[string]any{"web1": map[string]any{"pin": "123"}}}}
	if got := fmt.Sprint(o.hideOutputs(end).EventData); got != "map[artifact_data:map[db_host:db1 db_password:$encrypted$] custom:map[web1:map[pin:$encrypted$]]]" {
		t.Errorf("stats: %s", got)
	}
	if e := o.masked(Event{Event: "runner_on_ok", Stdout: "later: Tr0ub4dor&3, pin 123"}); e.Stdout != "later: $encrypted$, pin 123" {
		t.Errorf("later event: %s", e.Stdout)
	}
	other := Event{Event: "verbose", Stdout: "x", EventData: map[string]any{"artifact_data": map[string]any{"db_password": "x"}}}
	if e := o.hideOutputs(other); fmt.Sprint(e.EventData) != "map[artifact_data:map[db_password:x]]" {
		t.Errorf("verbose event: %v", e.EventData)
	}
	if e := (&Output{}).hideOutputs(task); fmt.Sprint(e.EventData) != fmt.Sprint(task.EventData) {
		t.Errorf("no secret outputs: %v", e.EventData)
	}
}
