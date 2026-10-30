package test

import (
	"fmt"
	"strings"
	"testing"
)

// The template form's fields (roadmap M7-01): each saves and reloads, the
// instance groups keep their order (the form rebuilds the list when it
// changes), the webhook key is shown and rotated, and start_at_task is
// prompted at launch with awxnext's ask_start_at_task_on_launch.
func TestTemplateFormFields(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", nil)
	url := fmt.Sprintf("job_templates/%d/", jt)

	must(t, a.patch(url, map[string]any{"prevent_instance_group_fallback": true, "start_at_task": "second task", "ask_start_at_task_on_launch": true}), 200)
	got := must(t, a.get(url), 200).Body
	if got["prevent_instance_group_fallback"] != true || got["start_at_task"] != "second task" || got["ask_start_at_task_on_launch"] != true {
		t.Fatalf("reloaded: fallback %v, start at %v, ask %v", got["prevent_instance_group_fallback"], got["start_at_task"], got["ask_start_at_task_on_launch"])
	}

	// Instance groups in the order given; a new order is the list rebuilt.
	var groups []int64
	for range 3 {
		groups = append(groups, must(t, a.post("instance_groups/", map[string]any{"name": uniq("ig")}), 201).id())
	}
	order := func() []int64 {
		var ids []int64
		for _, g := range must(t, a.get(url+"instance_groups/"), 200).results() {
			ids = append(ids, int64(g["id"].(float64)))
		}
		return ids
	}
	attach := func(ids ...int64) {
		for _, id := range ids {
			must(t, a.post(url+"instance_groups/", map[string]any{"id": id}), 204)
		}
	}
	attach(groups[2], groups[0], groups[1])
	if o := order(); fmt.Sprint(o) != fmt.Sprint([]int64{groups[2], groups[0], groups[1]}) {
		t.Fatalf("order as attached: %v", o)
	}
	for _, id := range order() {
		must(t, a.post(url+"instance_groups/", map[string]any{"id": id, "disassociate": true}), 204)
	}
	attach(groups[0], groups[2], groups[1])
	if o := order(); fmt.Sprint(o) != fmt.Sprint([]int64{groups[0], groups[2], groups[1]}) {
		t.Fatalf("order after a rebuild: %v", o)
	}

	// The webhook key: shown, then rotated.
	must(t, a.patch(url, map[string]any{"webhook_service": "github"}), 200)
	key := must(t, a.get(url+"webhook_key/"), 200).Body["webhook_key"]
	rotated := must(t, a.post(url+"webhook_key/", nil), 201).Body["webhook_key"]
	if key == "" || rotated == "" || key == rotated || must(t, a.get(url+"webhook_key/"), 200).Body["webhook_key"] != rotated {
		t.Fatalf("webhook key %v, rotated %v", key, rotated)
	}

	// start_at_task at launch: the template's by default, the prompt's when given.
	info := must(t, a.get(url+"launch/"), 200).Body
	if info["ask_start_at_task_on_launch"] != true || info["defaults"].(map[string]any)["start_at_task"] != "second task" {
		t.Fatalf("launch info: %v", info)
	}
	job := must(t, a.post(url+"launch/", map[string]any{"start_at_task": "third task"}), 201).id()
	if s := must(t, a.get(fmt.Sprintf("jobs/%d/", job)), 200).Body["start_at_task"]; s != "third task" {
		t.Fatalf("job start_at_task %v", s)
	}
	if r := must(t, a.post(url+"launch/", map[string]any{"start_at_task": 3}), 400); !strings.Contains(fmt.Sprint(r.Body["start_at_task"]), "string") {
		t.Fatalf("a number: %v", r.Body)
	}
	must(t, a.patch(url, map[string]any{"ask_start_at_task_on_launch": false}), 200)
	if r := must(t, a.post(url+"launch/", map[string]any{"start_at_task": "third task"}), 400); !strings.Contains(fmt.Sprint(r.Body), "start_at_task") {
		t.Fatalf("not prompted: %v", r.Body)
	}
}
