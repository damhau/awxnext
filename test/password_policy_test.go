package test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/auth"
)

// AWX's LOCAL_PASSWORD_MIN_* rules, through the API: on create, update and
// createsuperuser, in AWX's order and words.
func TestPasswordPolicy(t *testing.T) {
	a := adminClient(t)
	t.Cleanup(func() {
		a.patch("settings/authentication/", map[string]any{"LOCAL_PASSWORD_MIN_LENGTH": 0, "LOCAL_PASSWORD_MIN_DIGITS": 0,
			"LOCAL_PASSWORD_MIN_UPPER": 0, "LOCAL_PASSWORD_MIN_SPECIAL": 0})
	})
	create := func(pw string) resp {
		t.Helper()
		return a.post("users/", map[string]any{"username": uniq("pw"), "password": pw})
	}
	refused := func(r resp, msg string) {
		t.Helper()
		if r.Status != 400 || !strings.Contains(r.Raw, `"password"`) || !strings.Contains(r.Raw, msg) {
			t.Errorf("want %q: %d %s", msg, r.Status, r.Raw)
		}
	}
	// No rule by default.
	must(t, create("x"), 201)

	for _, c := range []struct {
		setting      string
		value        int
		weak, strong string
		message      string
	}{
		{"LOCAL_PASSWORD_MIN_LENGTH", 10, "short1A!", "longer1A!x", "Password must be at least 10 characters long."},
		{"LOCAL_PASSWORD_MIN_DIGITS", 2, "only1digitA!xx", "two22digitsA!", "Password must contain at least 2 digits."},
		{"LOCAL_PASSWORD_MIN_UPPER", 2, "oneupper11A!xx", "TwoUpper11!xx", "Password must contain at least 2 uppercase characters."},
		{"LOCAL_PASSWORD_MIN_SPECIAL", 2, "onespecial11!AA", "two special11!AA", "Password must contain at least 2 special characters."},
	} {
		must(t, a.patch("settings/authentication/", map[string]any{c.setting: c.value}), 200)
		refused(create(c.weak), c.message)
		must(t, create(c.strong), 201)
	}
	// The rules are checked in AWX's order: length first.
	refused(create("a"), "Password must be at least 10 characters long.")

	// Updates are checked; "$encrypted$" keeps the password; other fields need none.
	uid := must(t, create("StrOng11!!xx"), 201).id()
	refused(a.patch(fmt.Sprintf("users/%d/", uid), map[string]any{"password": "weak"}), "Password must be at least 10 characters long.")
	must(t, a.patch(fmt.Sprintf("users/%d/", uid), map[string]any{"password": "$encrypted$", "first_name": "Kept"}), 200)
	must(t, a.patch(fmt.Sprintf("users/%d/", uid), map[string]any{"password": "AnOther22!!x"}), 200)
	if r := (&client{t: t, user: must(t, a.get(fmt.Sprintf("users/%d/", uid)), 200).Body["username"].(string), pass: "AnOther22!!x"}).get("me/"); r.Status != 200 {
		t.Errorf("the new password: %d", r.Status)
	}

	// createsuperuser too.
	ctx := context.Background()
	if err := auth.EnsureSuperuser(ctx, app, uniq("root"), "", "weak"); err == nil || err.Error() != "Password must be at least 10 characters long." {
		t.Errorf("createsuperuser: %v", err)
	}
	if err := auth.EnsureSuperuser(ctx, app, uniq("root"), "", "StrOng11!!xx"); err != nil {
		t.Errorf("createsuperuser: %v", err)
	}

	// Any user can read the rules, to show them; negative values are refused.
	name := uniq("reader")
	must(t, a.post("users/", map[string]any{"username": name, "password": "StrOng11!!xx"}), 201)
	s := must(t, (&client{t: t, user: name, pass: "StrOng11!!xx"}).get("settings/authentication/"), 200).Body
	if s["LOCAL_PASSWORD_MIN_LENGTH"] != float64(10) || s["LOCAL_PASSWORD_MIN_SPECIAL"] != float64(2) || s["SESSION_COOKIE_AGE"] != nil {
		t.Errorf("rules for users: %v", s)
	}
	if r := a.patch("settings/authentication/", map[string]any{"LOCAL_PASSWORD_MIN_DIGITS": -1}); r.Status != 400 ||
		!strings.Contains(r.Raw, "Ensure this value is greater than or equal to 0.") {
		t.Errorf("negative: %d %s", r.Status, r.Raw)
	}
}
