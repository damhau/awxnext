package test

import (
	"fmt"
	"strings"
	"testing"
)

// A user's UI preferences on the server (roadmap M7-09,
// /api/v2/me/preferences/): saved views and stars set in one browser are
// there in another, each user has their own, a null removes one, and names
// and sizes are checked.
func TestUserPreferences(t *testing.T) {
	a := adminClient(t)
	user, _ := newUser(t, a)
	views := []any{map[string]any{"name": "failed prod", "q": "status:failed org:prod"}}
	got := must(t, user.patch("me/preferences/", map[string]any{"template-views": views, "starred-templates": []any{"job:4", "workflow:9"}}), 200).Body
	if fmt.Sprint(got["template-views"]) != fmt.Sprint(views) || len(got["starred-templates"].([]any)) != 2 {
		t.Fatalf("saved: %v", got)
	}
	// Another browser: the same user's preferences.
	again := must(t, user.get("me/preferences/"), 200).Body
	if fmt.Sprint(again["template-views"]) != fmt.Sprint(views) {
		t.Fatalf("reloaded: %v", again)
	}
	// Someone else's are their own.
	if mine := must(t, a.get("me/preferences/"), 200).Body; mine["template-views"] != nil {
		t.Fatalf("another user's preferences: %v", mine)
	}
	// A null removes one; the others stay.
	got = must(t, user.patch("me/preferences/", map[string]any{"starred-templates": nil}), 200).Body
	if _, ok := got["starred-templates"]; ok || got["template-views"] == nil {
		t.Fatalf("after removing one: %v", got)
	}
	// Names and sizes.
	must(t, user.patch("me/preferences/", map[string]any{"Bad Name": 1}), 400)
	must(t, user.patch("me/preferences/", map[string]any{"big": strings.Repeat("x", 70<<10)}), 400)
	if r := must(t, user.get("me/preferences/"), 200).Body; r["big"] != nil {
		t.Fatal("a refused preference was saved")
	}
}
