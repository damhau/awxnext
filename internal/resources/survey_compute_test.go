package resources

import (
	"strings"
	"testing"
)

func TestCheckValueFrom(t *testing.T) {
	earlier := map[string]string{"vlan": "multiplechoice", "env": "text", "admin_password": "password", "sites": "multiselect"}
	for _, ok := range []map[string]any{
		{"type": "text", "value_from": `item("vlan").name + "-" + user.username`},
		{"type": "text", "value_from": `answers.env == "prod" ? "p" : "d"`},
		{"type": "integer", "value_from": `len(item("sites"))`},
		{"type": "password", "value_from": "password(24)"},
		{"type": "text", "value_from": `request?.expires_at ?? ""`},
		{"type": "text", "value_from": `team?.name ?? user.teams[0]`},
	} {
		if err := checkValueFrom(ok, earlier, 1); err != nil {
			t.Errorf("%v: %v", ok["value_from"], err)
		}
	}
	for want, bad := range map[string]map[string]any{
		"must be an expression":                               {"type": "text", "value_from": 3},
		"needs a text, textarea, password":                    {"type": "multiplechoice", "value_from": `"a"`},
		"takes no default":                                    {"type": "text", "value_from": `"a"`, "default": "b"},
		`item("env") needs an earlier multiplechoice`:         {"type": "text", "value_from": `item("env")`},
		`item("later") needs an earlier`:                      {"type": "text", "value_from": `item("later")`},
		"item() takes the variable of an earlier":             {"type": "text", "value_from": `item(answers.env)`},
		"answers.nope is not an earlier question":             {"type": "text", "value_from": `answers.nope`},
		"answers.admin_password is a password":                {"type": "text", "value_from": `answers.admin_password`},
		"password() generates the answer of a password":       {"type": "text", "value_from": "password(24)"},
		"password(4): the length must be between 8 and":       {"type": "password", "value_from": "password(4)"},
		"'value_from' in survey question 1: unexpected token": {"type": "text", "value_from": "1 +"},
	} {
		if err := checkValueFrom(bad, earlier, 1); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v, want %q", bad["value_from"], err, want)
		}
	}
}

func TestComputeAnswer(t *testing.T) {
	env := &computeEnv{
		answers: map[string]any{"env": "prod", "size": int64(3)},
		picked:  map[string]any{"vlan": map[string]any{"name": "vlan-12", "vid": 12.0}, "sites": []any{"gva", "zrh"}},
		user:    map[string]any{"id": int64(7), "username": "alice", "teams": []any{"web"}},
		request: map[string]any{"id": int64(42), "expires_at": "2026-12-01T00:00:00Z"},
	}
	for _, c := range []struct {
		typ, src string
		want     any
	}{
		{"text", `item("vlan").name + "-" + user.username + "-" + string(request.id)`, "vlan-12-alice-42"},
		{"integer", `item("vlan").vid`, int64(12)},
		{"integer", `len(item("sites")) * answers.size`, int64(6)},
		{"float", `answers.size / 2`, 1.5},
		{"text", `answers.size`, "3"},
		{"text", `team?.name`, nil},
		{"text", `request.expires_at`, "2026-12-01T00:00:00Z"},
	} {
		got, err := computeAnswer(map[string]any{"type": c.typ, "value_from": c.src}, env)
		if err != nil || got != c.want {
			t.Errorf("%s: %#v %v, want %#v", c.src, got, err, c.want)
		}
	}
	for src, want := range map[string]string{
		`item("vlan")`:    "gives an object, not a text",
		`answers.env + 1`: "invalid operation",
		`password(200)`:   "password(200): the length must be between 8 and 128",
	} {
		typ := "text"
		if strings.HasPrefix(src, "password") {
			typ = "password"
		}
		if _, err := computeAnswer(map[string]any{"type": typ, "value_from": src}, env); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
	if _, err := computeAnswer(map[string]any{"type": "integer", "value_from": `answers.size / 2`}, env); err == nil || !strings.Contains(err.Error(), "not an integer") {
		t.Errorf("integer from a float: %v", err)
	}
	a, _ := computeAnswer(map[string]any{"type": "password", "value_from": "password(24)"}, env)
	b, _ := computeAnswer(map[string]any{"type": "password", "value_from": "password(24)"}, env)
	if s, _ := a.(string); len(s) != 24 || a == b || strings.Trim(s, passwordAlphabet) != "" {
		t.Errorf("passwords %q, %q", a, b)
	}
	if got, err := computeAnswer(map[string]any{"type": "text", "value_from": `request?.id`}, &computeEnv{}); got != nil || err != nil {
		t.Errorf("no request: %v %v", got, err)
	}
}
