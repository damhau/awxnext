package test

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The map tester evaluates one login's organization and team maps against a
// sample user with the login's own code, saved or not saved yet, and
// changes nothing.
func TestMapTester(t *testing.T) {
	a := adminClient(t)
	org, team := uniq("Maps Org"), uniq("maps-team")
	test := func(body map[string]any) resp {
		t.Helper()
		return must(t, a.post("settings/maps/test/", body), 200)
	}
	// roles lists a result's roles as "role on organization[/team]".
	roles := func(r resp, field string) []string {
		t.Helper()
		out := []string{}
		for _, g := range r.Body[field].([]any) {
			m := g.(map[string]any)
			s := fmt.Sprintf("%s on %s", m["role_definition"], m["organization"])
			if tm, _ := m["team"].(string); tm != "" {
				s += "/" + tm
			}
			out = append(out, s)
		}
		sort.Strings(out)
		return out
	}
	expect := func(r resp, field string, want ...string) {
		t.Helper()
		sort.Strings(want)
		if got := roles(r, field); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v (%s)", field, got, want, r.Raw)
		}
	}

	// The default maps, saved: a pattern for the members, an email for the
	// admins, removed when no longer matched.
	must(t, a.patch("settings/authentication/", map[string]any{
		"SOCIAL_AUTH_ORGANIZATION_MAP": map[string]any{org: map[string]any{"users": "/^ops-/", "admins": "boss@example.com"}},
		"SOCIAL_AUTH_TEAM_MAP":         map[string]any{team: map[string]any{"organization": org, "users": true}},
	}), 200)
	t.Cleanup(func() {
		a.patch("settings/authentication/", map[string]any{"SOCIAL_AUTH_ORGANIZATION_MAP": nil, "SOCIAL_AUTH_TEAM_MAP": nil})
	})
	r := test(map[string]any{"login": "default", "username": "ops-alice"})
	expect(r, "grants", "Organization Member on "+org, "Team Member on "+org+"/"+team)
	expect(r, "removed", "Organization Admin on "+org)
	if c := fmt.Sprint(r.Body["would_create"]); !strings.Contains(c, "organization "+org) || !strings.Contains(c, "team "+team+" in "+org) {
		t.Errorf("would_create: %v", c)
	}
	r = test(map[string]any{"login": "default", "username": "bob", "email": "boss@example.com"})
	expect(r, "grants", "Organization Admin on "+org, "Team Member on "+org+"/"+team)
	expect(r, "removed", "Organization Member on "+org)

	// Values not saved yet: GitHub's own maps, not the default ones. A null
	// expression and remove_users false leave roles as they are.
	r = test(map[string]any{"login": "github-org", "username": "ops-alice", "settings": map[string]any{
		"SOCIAL_AUTH_GITHUB_ORG_ORGANIZATION_MAP": map[string]any{org: map[string]any{"users": []string{"carol"}, "remove_users": false, "admins": nil}},
	}})
	expect(r, "grants")
	expect(r, "kept", "Organization Admin on "+org, "Organization Member on "+org)
	if r.Body["superuser"] != nil {
		t.Errorf("superuser: %v", r.Body["superuser"])
	}

	// LDAP: groups are DNs, compared as DNs, and set the user flags.
	r = test(map[string]any{"login": "ldap2", "username": "carol", "groups": []string{"CN=Eng, OU=Groups,DC=example,DC=com"}, "settings": map[string]any{
		"AUTH_LDAP_2_ORGANIZATION_MAP": map[string]any{org: map[string]any{
			"users": []string{"cn=eng,ou=groups,dc=example,dc=com"}, "admins": []string{"cn=leads,ou=groups,dc=example,dc=com"},
			"auditors": []string{"cn=eng,ou=groups,dc=example,dc=com"}}},
		"AUTH_LDAP_2_USER_FLAGS_BY_GROUP": map[string]any{"is_superuser": []string{"cn=admins,ou=groups,dc=example,dc=com"},
			"is_system_auditor": []string{"cn=eng,ou=groups,dc=example,dc=com"}},
	}})
	expect(r, "grants", "Organization Member on "+org, "Organization Audit on "+org)
	expect(r, "removed", "Organization Admin on "+org)
	if r.Body["superuser"] != false || r.Body["auditor"] != true {
		t.Errorf("LDAP flags: %v %v", r.Body["superuser"], r.Body["auditor"])
	}

	// SAML: organizations and flags from the attributes.
	r = test(map[string]any{"login": "saml", "username": "dave", "attributes": map[string]any{"orgs": []string{org}, "Role": "admin"}, "settings": map[string]any{
		"SOCIAL_AUTH_SAML_ORGANIZATION_ATTR":  map[string]any{"saml_attr": "orgs"},
		"SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR": map[string]any{"is_superuser_role": []string{"admin"}},
	}})
	expect(r, "grants", "Organization Member on "+org)
	if r.Body["superuser"] != true {
		t.Errorf("SAML superuser: %s", r.Raw)
	}

	// Refused: an unknown login, no username, another login's setting, a
	// map saving would refuse, and anyone but a superuser.
	for name, body := range map[string]map[string]any{
		"login":    {"login": "nope", "username": "x"},
		"username": {"login": "default", "username": " "},
		"settings": {"login": "github", "username": "x", "settings": map[string]any{"SOCIAL_AUTH_ORGANIZATION_MAP": map[string]any{}}},
		"SOCIAL_AUTH_ORGANIZATION_MAP": {"login": "default", "username": "x", "settings": map[string]any{
			"SOCIAL_AUTH_ORGANIZATION_MAP": map[string]any{"X": map[string]any{"bogus": 1}}}},
	} {
		if r := a.post("settings/maps/test/", body); r.Status != 400 || !strings.Contains(r.Raw, `"`+name+`"`) {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
	}
	bob, _ := newUser(t, a)
	must(t, bob.post("settings/maps/test/", map[string]any{"login": "default", "username": "x"}), 403)

	// Nothing was saved or created.
	if v := must(t, a.get("settings/github-org/"), 200).Body["SOCIAL_AUTH_GITHUB_ORG_ORGANIZATION_MAP"]; v != nil {
		t.Errorf("GitHub map saved: %v", v)
	}
	if n := must(t, a.get("organizations/?name="+strings.ReplaceAll(org, " ", "%20")), 200).count(); n != 0 {
		t.Errorf("organization created")
	}
}
