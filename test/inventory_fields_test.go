package test

import (
	"fmt"
	"testing"
)

// The inventory form's fields (roadmap M7-02): instance groups in order,
// prevent_instance_group_fallback and labels save and reload, on a
// regular inventory and on a constructed one (saved through
// constructed_inventories/, as the form does).
func TestInventoryFormFields(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	var groups []int64
	for range 2 {
		groups = append(groups, must(t, a.post("instance_groups/", map[string]any{"name": uniq("ig")}), 201).id())
	}
	regular := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	constructed := must(t, a.post("constructed_inventories/", map[string]any{"name": uniq("ci"), "organization": org}), 201).id()
	for _, c := range []struct {
		res string
		id  int64
	}{{"inventories", regular}, {"constructed_inventories", constructed}} {
		url := fmt.Sprintf("%s/%d/", c.res, c.id)
		must(t, a.patch(url, map[string]any{"prevent_instance_group_fallback": true}), 200)
		sub := fmt.Sprintf("inventories/%d/", c.id)
		for _, g := range []int64{groups[1], groups[0]} {
			must(t, a.post(sub+"instance_groups/", map[string]any{"id": g}), 204)
		}
		label := must(t, a.post(sub+"labels/", map[string]any{"name": uniq("lab"), "organization": org}), 201).id()

		got := must(t, a.get(url), 200).Body
		if got["prevent_instance_group_fallback"] != true {
			t.Errorf("%s: prevent_instance_group_fallback %v", c.res, got["prevent_instance_group_fallback"])
		}
		var order []int64
		for _, g := range must(t, a.get(sub+"instance_groups/"), 200).results() {
			order = append(order, int64(g["id"].(float64)))
		}
		if fmt.Sprint(order) != fmt.Sprint([]int64{groups[1], groups[0]}) {
			t.Errorf("%s: instance groups %v", c.res, order)
		}
		labels := must(t, a.get(fmt.Sprintf("inventories/%d/", c.id)), 200).Body["summary_fields"].(map[string]any)["labels"].(map[string]any)["results"].([]any)
		if len(labels) != 1 || int64(labels[0].(map[string]any)["id"].(float64)) != label {
			t.Errorf("%s: labels %v", c.res, labels)
		}
	}
}
