package test

import (
	"fmt"
	"testing"
)

// An inventory source's schedules (roadmap M7-03, the source's Schedules
// tab): created on the source, edited, and switched off, which clears its
// next run; listed under the source and as its schedule.
func TestInventorySourceSchedules(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": uniq("ec2"), "inventory": inv, "source": "ec2"}), 201).id()
	base := fmt.Sprintf("inventory_sources/%d/schedules/", src)

	s := must(t, a.post(base, map[string]any{"name": uniq("nightly"), "rrule": "DTSTART:20300101T020000Z RRULE:FREQ=DAILY;INTERVAL=1"}), 201)
	id := s.id()
	if s.Body["unified_job_template"] != float64(src) || s.Body["next_run"] == nil || s.Body["enabled"] != true {
		t.Fatalf("created: template %v, next run %v, enabled %v", s.Body["unified_job_template"], s.Body["next_run"], s.Body["enabled"])
	}
	if must(t, a.get(base), 200).count() != 1 {
		t.Fatal("not listed under the source")
	}

	url := fmt.Sprintf("schedules/%d/", id)
	edited := must(t, a.patch(url, map[string]any{"rrule": "DTSTART:20300101T060000Z RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=MO", "description": "Mondays"}), 200).Body
	if edited["description"] != "Mondays" || edited["next_run"] != "2030-01-07T06:00:00Z" {
		t.Fatalf("edited: description %v, next run %v", edited["description"], edited["next_run"])
	}

	off := must(t, a.patch(url, map[string]any{"enabled": false}), 200).Body
	if off["enabled"] != false || off["next_run"] != nil {
		t.Fatalf("switched off: enabled %v, next run %v", off["enabled"], off["next_run"])
	}
	if next := must(t, a.get(fmt.Sprintf("inventory_sources/%d/", src)), 200).Body["next_job_run"]; next != nil {
		t.Errorf("the source's next run with its only schedule off: %v", next)
	}
}
