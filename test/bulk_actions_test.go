package test

import (
	"fmt"
	"strings"
	"testing"
)

// What the lists' bulk bar (roadmap M7-10) relies on: each selected object
// is acted on alone, so one the user may not change fails with its own
// message while the others go through; "Select all" reads every page of the
// filtered list; schedules turn on and off; syncing what cannot be synced is
// refused with a reason; hidden columns are kept with the preferences.
func TestBulkListActions(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	user, uid := newUser(t, a)
	prefix := uniq("bulk")
	var invs []int64
	for i := 0; i < 3; i++ {
		invs = append(invs, must(t, a.post("inventories/", map[string]any{"name": fmt.Sprintf("%s-%d", prefix, i), "organization": org}), 201).id())
	}
	role := func(inv int64, name string) any {
		roles := must(t, a.get(fmt.Sprintf("inventories/%d/", inv)), 200).Body["summary_fields"].(map[string]any)["object_roles"].(map[string]any)
		return roles[name].(map[string]any)["id"]
	}
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": role(invs[0], "admin_role")}), 204)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": role(invs[1], "read_role")}), 204)

	// "Select all": every page of the filtered list.
	var all []map[string]any
	for page := 1; ; page++ {
		r := must(t, user.get(fmt.Sprintf("inventories/?name__startswith=%s&page_size=1&page=%d", prefix, page)), 200)
		all = append(all, r.results()...)
		if r.Body["next"] == nil {
			break
		}
	}
	if len(all) != 2 {
		t.Fatalf("the user's inventories matching the filter: %d", len(all))
	}

	// Delete both: the one administered goes, the one only read is refused
	// with a message the bar shows.
	// (Inventories are deleted in the background, as in AWX: 202.)
	must(t, user.delete(fmt.Sprintf("inventories/%d/", invs[0])), 202)
	if r := user.delete(fmt.Sprintf("inventories/%d/", invs[1])); r.Status != 403 || fmt.Sprint(r.Body["detail"]) == "" {
		t.Fatalf("deleting an inventory only read: %d %v", r.Status, r.Body)
	}
	must(t, a.get(fmt.Sprintf("inventories/%d/", invs[1])), 200)

	// Syncing an inventory with no source, or a manual project: refused, with why.
	if r := a.post(fmt.Sprintf("inventories/%d/update_inventory_sources/", invs[2]), map[string]any{}); r.Status != 400 || !strings.Contains(fmt.Sprint(r.Body), "No inventory sources") {
		t.Fatalf("syncing an inventory without sources: %d %v", r.Status, r.Body)
	}
	manual := must(t, a.post("projects/", map[string]any{"name": uniq("manual"), "organization": org, "scm_type": ""}), 201).id()
	if r := a.post(fmt.Sprintf("projects/%d/update/", manual), map[string]any{}); r.Status != 405 || !strings.Contains(fmt.Sprint(r.Body["detail"]), "Manual projects") {
		t.Fatalf("syncing a manual project: %d %v", r.Status, r.Body)
	}

	// Schedules turned off, then on again.
	sjt := must(t, a.get("system_job_templates/?job_type=cleanup_activitystream"), 200).results()[0]["id"]
	var scheds []int64
	for i := 0; i < 3; i++ {
		scheds = append(scheds, must(t, a.post("schedules/", map[string]any{"name": fmt.Sprintf("%s-s%d", prefix, i), "unified_job_template": sjt,
			"rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY;INTERVAL=1", "extra_data": map[string]any{"days": 30}}), 201).id())
	}
	for _, s := range scheds {
		must(t, a.patch(fmt.Sprintf("schedules/%d/", s), map[string]any{"enabled": false}), 200)
	}
	if r := must(t, a.get(fmt.Sprintf("schedules/?name__startswith=%s&enabled=false", prefix)), 200); r.count() != 3 {
		t.Fatalf("schedules turned off: %d", r.count())
	}
	for _, s := range scheds {
		if r := must(t, a.patch(fmt.Sprintf("schedules/%d/", s), map[string]any{"enabled": true}), 200); r.Body["next_run"] == nil {
			t.Fatalf("a schedule turned on has no next run: %v", r.Body)
		}
	}

	// The column chooser's hidden columns, per list, kept with the user.
	got := must(t, user.patch("me/preferences/", map[string]any{"columns.inventories": []any{"kind", "groups"}, "columns.notification_templates": []any{}}), 200).Body
	if fmt.Sprint(got["columns.inventories"]) != "[kind groups]" {
		t.Fatalf("hidden columns: %v", got)
	}
}
