package resources

import (
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/schedule"
)

// Maintenance windows and change freezes (awxnext, roadmap M14-03):
// recurring periods, written as schedules write them, during which event
// rules' actions are held back or wait for approval, as each rule's
// window_action says. A window covers the rules it lists, the launches on
// the hosts of the inventory groups it lists, the catalog items it lists
// (roadmap M15-04, catalog_approvals.go), or, listing none of them, every
// rule of its organization. The engine's check is in rule_guardrails.go.

const (
	windowMaintenance = "maintenance"
	windowFreeze      = "freeze"
	maxWindowDuration = 90 * 24 * 3600
	maxWindowTargets  = 200
)

func validateIDList(what string) func(c *Ctx, v any, data Obj) error {
	return func(c *Ctx, v any, data Obj) error {
		var list []any
		switch x := v.(type) {
		case []any:
			list = x
		case []int64:
			for _, id := range x {
				list = append(list, id)
			}
		case nil:
		default:
			return fmt.Errorf("Expected a list of %s ids.", what)
		}
		if len(list) > maxWindowTargets {
			return fmt.Errorf("A window lists at most %d %ss.", maxWindowTargets, what)
		}
		for _, e := range list {
			if _, ok := api.ID(e); !ok {
				return fmt.Errorf("Invalid %s id %v.", what, e)
			}
		}
		return nil
	}
}

func registerMaintenanceWindows() {
	api.Register(&Resource{
		Name:        "maintenance_windows",
		Type:        "maintenance_window",
		Table:       "maintenance_windows",
		Verbose:     "Maintenance Window",
		Ordering:    "name",
		ContentType: "maintenancewindow",
		OrgColumn:   "organization_id",
		Versioned:   true,
		Managed:     true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true},
			{Name: "kind", Type: api.TChoice, Default: windowMaintenance, Choices: labelled(windowMaintenance, "Maintenance window", windowFreeze, "Change freeze"),
				Help: "A maintenance window, while hosts are worked on, or a change freeze, while nothing may change. Both hold rules' actions back, or make them wait for approval."},
			{Name: "rrule", Type: api.TString, Required: true, Help: "When the window starts: DTSTART with RRULE, EXRULE, RDATE and EXDATE, as a schedule's rrule.",
				Validate: func(c *Ctx, v any, data Obj) error { _, err := schedule.Parse(str(v)); return err }},
			{Name: "duration", Type: api.TInt, Default: 3600, Validate: between(60, maxWindowDuration), Help: "How long each occurrence lasts, in seconds."},
			{Name: "enabled", Type: api.TBool, Default: true},
			{Name: "event_rules", Type: api.TJSON, Default: []any{}, Validate: validateIDList("event rule"),
				Help: "The ids of the event rules the window covers."},
			{Name: "groups", Column: "inventory_groups", Type: api.TJSON, Default: []any{}, Validate: validateIDList("inventory group"),
				Help: "The ids of the inventory groups the window covers: a launch on one of their hosts. With no rules, groups or items, the window covers every rule of its organization."},
			{Name: "catalog_items", Type: api.TJSON, Default: []any{}, Validate: validateIDList("catalog item"),
				Help: "The ids of the catalog items the window covers: their requests are refused, or wait for the item's freeze exception."},
			{Name: "active", Type: api.TBool, ReadOnly: true, Virtual: true, Help: "Inside an occurrence now."},
			{Name: "active_until", Type: api.TDateTime, ReadOnly: true, Virtual: true, Nullable: true, Help: "When the occurrence going on ends."},
			{Name: "next_start", Type: api.TDateTime, ReadOnly: true, Virtual: true, Nullable: true, Help: "When the next occurrence starts."},
		},
		BeforeSave: saveMaintenanceWindow,
		Summary:    windowSummary,
	})
	for res, column := range map[string]string{"event_rules": "event_rules", "groups": "inventory_groups"} {
		r := api.Lookup(res)
		prev, drop := r.AfterDelete, removeFromWindows(column)
		r.AfterDelete = func(c *Ctx, obj Obj) error {
			if prev != nil {
				if err := prev(c, obj); err != nil {
					return err
				}
			}
			return drop(c, obj)
		}
	}
}

// removeFromWindows drops a deleted object from the windows that list it.
func removeFromWindows(column string) func(c *Ctx, obj Obj) error {
	return func(c *Ctx, obj Obj) error {
		_, err := c.DB().Exec(c, `UPDATE maintenance_windows SET `+column+` = (SELECT coalesce(jsonb_agg(e ORDER BY e), '[]'::jsonb)
			FROM jsonb_array_elements(`+column+`) e WHERE e <> to_jsonb($1::bigint)), modified = now()
			WHERE `+column+` @> jsonb_build_array($1::bigint)`, obj["id"])
		return err
	}
}

// saveMaintenanceWindow checks that the rules and the groups a window lists
// are of its organization, and that the user may change them: a window
// holds their actions back.
func saveMaintenanceWindow(c *Ctx, old, data Obj) error {
	org, _ := api.ID(data["organization"])
	if _, given := data["organization"]; !given && old != nil {
		org, _ = api.ID(old["organization"])
	}
	moved := old != nil && !sameValue(old["organization"], org)
	for _, field := range []string{"event_rules", "groups", "catalog_items"} {
		v, given := data[field]
		if !given {
			if !moved {
				continue
			}
			v = old[field]
		}
		ids := streamIDs(v)
		if ids == nil {
			ids = []int64{}
		}
		data[field] = ids
		if old != nil && !moved && sameValue(old[field], ids) {
			continue
		}
		for _, id := range ids {
			if err := windowTarget(c, field, id, org); err != nil {
				return err
			}
		}
	}
	return nil
}

func windowTarget(c *Ctx, field string, id, org int64) error {
	missing := api.FieldError(field, fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
	var resName string
	var target, owner int64
	var err error
	switch field {
	case "event_rules", "catalog_items":
		resName, target = field, id
		err = c.DB().QueryRow(c, "SELECT organization_id FROM "+field+" WHERE id = $1", id).Scan(&owner)
	default:
		resName = "inventories"
		err = c.DB().QueryRow(c, "SELECT i.id, i.organization_id FROM groups g JOIN inventories i ON i.id = g.inventory_id WHERE g.id = $1", id).Scan(&target, &owner)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return missing
	}
	if err != nil {
		return err
	}
	ok, err := api.HasPermID(c, resName, target, "change")
	if errors.Is(err, api.ErrNotFound) {
		return missing
	}
	if err != nil {
		return err
	}
	if !ok {
		return api.Detail(403, fmt.Sprintf("%s may not hold back %s %d.", c.User.Username,
			map[string]string{"event_rules": "the actions of event rule", "groups": "the actions of group", "catalog_items": "the requests of catalog item"}[field], id))
	}
	if owner != org {
		return api.FieldError(field, fmt.Sprintf("%d is not of the window's organization.", id))
	}
	return nil
}

// windowSummary says whether each window is going on, until when, and
// when it starts next.
func windowSummary(c *Ctx, objs []Obj) error {
	now := time.Now()
	for _, o := range objs {
		o["active"], o["active_until"], o["next_start"] = false, nil, nil
		rule, err := schedule.Parse(str(o["rrule"]))
		if err != nil {
			continue
		}
		d, _ := api.ID(o["duration"])
		if _, end, ok := rule.Active(now, time.Duration(d)*time.Second); ok && boolv(o["enabled"]) {
			o["active"], o["active_until"] = true, api.FormatTime(end)
		}
		if next, ok := rule.After(now); ok {
			o["next_start"] = api.FormatTime(next)
		}
	}
	return nil
}
