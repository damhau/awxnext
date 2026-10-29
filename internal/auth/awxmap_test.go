package auth

import (
	"reflect"
	"strings"
	"testing"
)

func TestAWXMapExpressions(t *testing.T) {
	alice := IdentityMatcher("alice", "alice@example.com")
	for _, c := range []struct {
		in       any
		set, hit bool
	}{
		{nil, false, false}, {"", false, false}, {"null", false, false},
		{true, true, true}, {"true", true, true}, {"yes", true, true}, {float64(1), true, true},
		{false, true, false}, {"no", true, false}, {float64(0), true, false}, {[]any{}, true, false},
		{"alice", true, true}, {"alice@example.com", true, true}, {"Alice", true, false},
		{"/^ALI/i", true, true}, {"/^ALI/", true, false}, {"/example/", true, false}, // matched from the start
		{"/.*example/", true, true}, {[]any{"bob", "/^al/"}, true, true}, {[]any{"bob", "/^bo/"}, true, false},
	} {
		e, err := parseExpr(c.in)
		if err != nil {
			t.Fatalf("%v: %v", c.in, err)
		}
		if e.set != c.set || e.set && e.matches(alice) != c.hit {
			t.Errorf("%#v: set %v match %v", c.in, e.set, e.matches(alice))
		}
	}
	for in, want := range map[string]any{
		"Expected None, True, False, a string or list of strings but got <class 'dict'> instead.": map[string]any{},
		"error parsing regexp":    "/(?=x)/",
		"but got <class 'float'>": float64(2),
	} {
		if _, err := parseExpr(want); err == nil || !strings.Contains(err.Error(), in) {
			t.Errorf("%v: %v", want, err)
		}
	}
}

// memberships applies results as ApplyMapping does: grants are added,
// kept roles stay if present, the others go.
type memberships map[string]map[string]bool // user -> role key

func (m memberships) login(user string, res MappingResult) {
	before := m[user]
	after := map[string]bool{}
	for _, g := range res.Grants {
		after[g.RoleDefinition+"|"+g.Organization+"|"+g.Team] = true
	}
	for _, g := range res.Keep {
		if k := g.RoleDefinition + "|" + g.Organization + "|" + g.Team; before[k] {
			after[k] = true
		}
	}
	m[user] = after
}

func (m memberships) count(key string) int {
	n := 0
	for _, roles := range m {
		if roles[key] {
			n++
		}
	}
	return n
}

// The scenarios of AWX's test_social_pipeline.py.
func TestAWXMapsLikeAWX(t *testing.T) {
	users := []string{"user1@foo.com", "user2@foo.com", "user3@foo.com"}
	run := func(m memberships, orgMap, teamMap any, user string) {
		t.Helper()
		orgs, err := ParseOrgMap(orgMap)
		if err != nil {
			t.Fatal(err)
		}
		teams, err := ParseTeamMap(teamMap)
		if err != nil {
			t.Fatal(err)
		}
		m.login(user, Mapping{AWX: AWXMaps{Orgs: orgs, Teams: teams}}.Evaluate(Identity{Username: user, Email: user}))
	}
	org := func(admins, users any, removeAdmins, removeUsers bool, alias string) map[string]any {
		return map[string]any{"Default": map[string]any{"admins": admins, "users": users, "remove_admins": removeAdmins,
			"remove_users": removeUsers, "organization_alias": alias}}
	}
	admin, member := "Organization Admin|Default|", "Organization Member|Default|"

	m := memberships{}
	for _, u := range users {
		run(m, org("/.*/", "/.*/", true, true, ""), nil, u)
	}
	if m.count(admin) != 3 || m.count(member) != 3 {
		t.Fatalf("regex: %d admins, %d members", m.count(admin), m.count(member))
	}
	run(m, org(false, false, true, true, ""), nil, users[0])
	if m.count(admin) != 2 || m.count(member) != 2 {
		t.Errorf("remove: %d admins, %d members", m.count(admin), m.count(member))
	}
	run(m, org(false, false, false, false, ""), nil, users[1])
	if m.count(admin) != 2 || m.count(member) != 2 {
		t.Errorf("remove disabled: %d admins, %d members", m.count(admin), m.count(member))
	}
	// A null expression changes nothing, whatever the remove option.
	run(m, org(nil, nil, true, true, ""), nil, users[2])
	if !m[users[2]][admin] || !m[users[2]][member] {
		t.Errorf("null expressions: %v", m[users[2]])
	}
	run(m, org("/.*/", nil, true, true, "Default_Alias"), nil, users[0])
	if !m[users[0]]["Organization Admin|Default_Alias|"] {
		t.Errorf("alias: %v", m[users[0]])
	}

	teams := func(users any, remove bool) map[string]any {
		return map[string]any{"Blue": map[string]any{"organization": "Default", "remove": remove, "users": users},
			"Red": map[string]any{"organization": "Default", "remove": remove, "users": users}}
	}
	blue, red := "Team Member|Default|Blue", "Team Member|Default|Red"
	m = memberships{}
	for _, u := range users {
		run(m, nil, teams("/.*/", true), u)
	}
	if m.count(blue) != 3 || m.count(red) != 3 || m.count(member) != 0 {
		t.Fatalf("teams: %d blue, %d red, %d organization members", m.count(blue), m.count(red), m.count(member))
	}
	run(m, nil, teams(false, true), users[0])
	if m.count(blue) != 2 || m.count(red) != 2 {
		t.Errorf("team remove: %d blue, %d red", m.count(blue), m.count(red))
	}
	run(m, nil, teams(false, false), users[1])
	if m.count(blue) != 2 || m.count(red) != 2 {
		t.Errorf("team remove disabled: %d blue, %d red", m.count(blue), m.count(red))
	}
}

func TestAWXMapValidation(t *testing.T) {
	for in, want := range map[string]any{
		`Default: Invalid key(s): "remove".`:                   map[string]any{"Default": map[string]any{"remove": true, "users": true}},
		`Default: admins: Expected None, True, False`:          map[string]any{"Default": map[string]any{"admins": map[string]any{}}},
		`Default: remove_users: Must be a valid boolean.`:      map[string]any{"Default": map[string]any{"remove_users": "maybe"}},
		`Default: Expected a dictionary of items but got type`: map[string]any{"Default": []any{}},
		`Expected a dictionary of items but got type "list".`:  []any{},
	} {
		if _, err := ParseOrgMap(want); err == nil || !strings.HasPrefix(err.Error(), in) {
			t.Errorf("org map %v: %v", want, err)
		}
	}
	if _, err := ParseTeamMap(map[string]any{"Web": map[string]any{"users": true}}); err == nil || err.Error() != "Web: organization: This field is required." {
		t.Errorf("team without organization: %v", err)
	}
	if _, err := ParseTeamMap(map[string]any{"Web": map[string]any{"organization": "Ops", "admins": true}}); err == nil || err.Error() != `Web: Invalid key(s): "admins".` {
		t.Errorf("team admins: %v", err)
	}
	if m, err := ParseOrgMap(nil); err != nil || len(m) != 0 {
		t.Errorf("null map: %v %v", m, err)
	}
}

// A login with its own maps uses them even when they are unset, as AWX's
// settings return None for them; OIDC uses the global maps.
func TestMapSettingsPerProvider(t *testing.T) {
	settings := map[string]any{"SOCIAL_AUTH_ORGANIZATION_MAP": map[string]any{"Default": map[string]any{"users": true}}}
	get := func(k string) any { return settings[k] }
	if o, tm := MapSettings("oidc"); o != "SOCIAL_AUTH_ORGANIZATION_MAP" || tm != "SOCIAL_AUTH_TEAM_MAP" {
		t.Errorf("oidc: %s %s", o, tm)
	}
	if o, tm := MapSettings("github-enterprise-org"); o != "SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_ORGANIZATION_MAP" || tm != "SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_TEAM_MAP" {
		t.Errorf("github-enterprise-org: %s %s", o, tm)
	}
	if m, _ := LoadAWXMaps(get, "oidc"); len(m.Orgs) != 1 {
		t.Errorf("oidc maps: %+v", m)
	}
	if m, _ := LoadAWXMaps(get, "github"); !m.Empty() {
		t.Errorf("github falls back to the global map: %+v", m)
	}
	settings["SOCIAL_AUTH_GITHUB_TEAM_MAP"] = map[string]any{"Web": map[string]any{"organization": "Ops", "users": true}}
	if m, _ := LoadAWXMaps(get, "github"); len(m.Orgs) != 0 || len(m.Teams) != 1 {
		t.Errorf("github maps: %+v", m)
	}
}

// Rules and maps combine: what either grants is granted, and a role both
// keep and grant is only granted.
func TestMappingCombinesRulesAndMaps(t *testing.T) {
	rules, err := ParseRules([]any{map[string]any{"group": "ops", "organization": "Ops", "role": "member"}})
	if err != nil {
		t.Fatal(err)
	}
	orgs, _ := ParseOrgMap(map[string]any{"Ops": map[string]any{"users": false, "remove_users": false, "admins": "/^alice$/"}})
	res := Mapping{Rules: rules, AWX: AWXMaps{Orgs: orgs}}.Evaluate(Identity{Username: "alice", Groups: []string{"ops"}})
	var granted []string
	for _, g := range res.Grants {
		granted = append(granted, g.RoleDefinition+" "+g.Organization+" "+g.Group+g.Map)
	}
	want := []string{`Organization Admin Ops organization map "Ops" admins`, "Organization Member Ops ops"}
	if !reflect.DeepEqual(granted, want) || len(res.Keep) != 0 {
		t.Errorf("grants %q, kept %v", granted, res.Keep)
	}
}
