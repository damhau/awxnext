package auth

import (
	"testing"
)

func mustRules(t *testing.T, v any) []Rule {
	t.Helper()
	r, err := ParseRules(v)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEvaluate(t *testing.T) {
	rules := mustRules(t, []any{
		map[string]any{"group": "admins", "superuser": true},
		map[string]any{"group_regex": "^team-(\\w+)-(\\w+)$", "organization": "$1", "team": "$2", "role": "admin"},
		map[string]any{"group": "ops", "organization": "Ops", "role_definition": "Organization Execute"},
	})
	res := Evaluate(rules, []string{"team-acme-web", "ops", "noise"})
	if res.Superuser == nil || *res.Superuser {
		t.Fatalf("superuser should be authoritative false, got %v", res.Superuser)
	}
	if res.Auditor != nil {
		t.Fatal("auditor should be untouched when no auditor rules exist")
	}
	want := map[string]bool{
		"Team Admin|acme|web":       true,
		"Organization Member|acme|": true,
		"Organization Execute|Ops|": true,
	}
	if len(res.Grants) != len(want) {
		t.Fatalf("grants: %+v", res.Grants)
	}
	for _, g := range res.Grants {
		if !want[g.RoleDefinition+"|"+g.Organization+"|"+g.Team] {
			t.Errorf("unexpected grant %+v", g)
		}
	}
	if su := Evaluate(rules, []string{"admins"}).Superuser; su == nil || !*su {
		t.Fatal("admins should be superuser")
	}
}

func TestParseRulesErrors(t *testing.T) {
	for _, bad := range []any{
		[]any{map[string]any{"group": "x"}},
		[]any{map[string]any{"group": "x", "group_regex": "y", "superuser": true}},
		[]any{map[string]any{"group_regex": "(", "superuser": true}},
		[]any{map[string]any{"group": "x", "team": "t", "organization": ""}},
		[]any{map[string]any{"group": "x", "organization": "o", "role": "owner"}},
		[]any{map[string]any{"group": "x", "organization": "o", "team": "t", "role": "auditor"}},
		"not a list",
	} {
		if _, err := ParseRules(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
}

func TestClaimHelpers(t *testing.T) {
	claims := map[string]any{"realm_access": map[string]any{"roles": []any{"a", "b"}}, "groups": "x, y z"}
	if got := claimStrings(claimPath(claims, "realm_access.roles")); len(got) != 2 {
		t.Fatalf("nested: %v", got)
	}
	if got := claimStrings(claimPath(claims, "groups")); len(got) != 3 {
		t.Fatalf("string groups: %v", got)
	}
	if claimPath(claims, "missing.path") != nil {
		t.Fatal("missing path should be nil")
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"":                     "/",
		"/jobs/1":              "/jobs/1",
		"//evil.example":       "/",
		"https://evil.example": "/",
		"/\\evil.example":      "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
