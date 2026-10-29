package auth

import (
	"reflect"
	"sort"
	"testing"
)

func grantKeys(gs []Grant) []string {
	out := []string{}
	for _, g := range gs {
		out = append(out, g.RoleDefinition+" "+g.Organization+" "+g.Team)
	}
	sort.Strings(out)
	return out
}

// The cases of AWX's test_saml_pipeline.py.
func TestSAMLMappingLikeAWX(t *testing.T) {
	attrs := map[string][]string{"memberOf": {"Default1", "Default2"}, "admins": {"Default3"}, "auditors": {"Default4"}, "groups": {"Blue", "Red"}}
	alias, _ := ParseOrgMap(map[string]any{"Default1": map[string]any{"organization_alias": "o1_alias"}})
	orgAttr := map[string]any{"saml_attr": "memberOf", "saml_admin_attr": "admins", "saml_auditor_attr": "auditors", "remove": true, "remove_admins": true}

	res := samlMapping(&samlConfig{orgAttr: orgAttr, orgMap: alias}, "cmeyers", "", attrs, false, false)
	want := []string{"Organization Admin Default3 ", "Organization Audit Default4 ", "Organization Member Default2 ", "Organization Member o1_alias "}
	if got := grantKeys(res.Grants); !reflect.DeepEqual(got, want) || len(res.KeepRoles) != 0 {
		t.Errorf("organizations by attribute: %v, keep %v", got, res.KeepRoles)
	}
	orgAttr["remove"], orgAttr["remove_admins"], orgAttr["remove_auditors"] = false, false, false
	res = samlMapping(&samlConfig{orgAttr: orgAttr}, "cmeyers", "", attrs, false, false)
	if !reflect.DeepEqual(res.KeepRoles, []string{"Organization Member", "Organization Admin", "Organization Audit"}) {
		t.Errorf("remove false: %v", res.KeepRoles)
	}

	// The organization map adds membership without undoing the attribute's admin role.
	orgMap, _ := ParseOrgMap(map[string]any{"Default1": map[string]any{"users": "foobar", "remove_users": true, "organization_alias": "o1_alias"}})
	res = samlMapping(&samlConfig{orgAttr: map[string]any{"saml_attr": "memberOf", "saml_admin_attr": "admins", "saml_auditor_attr": "auditors"}, orgMap: orgMap},
		"foobar", "", map[string][]string{"admins": {"Default1"}}, false, false)
	if got := grantKeys(res.Grants); !reflect.DeepEqual(got, []string{"Organization Admin o1_alias ", "Organization Member o1_alias "}) {
		t.Errorf("map and attribute: %v", got)
	}
	// Unlike the social pipeline, a null expression removes.
	nullMap, _ := ParseOrgMap(map[string]any{"Ops": map[string]any{"users": nil}})
	if res := samlMapping(&samlConfig{orgMap: nullMap}, "x", "", nil, false, false); !reflect.DeepEqual(grantKeys(res.Revoke),
		[]string{"Organization Admin Ops ", "Organization Audit Ops ", "Organization Member Ops "}) || len(res.Keep) != 0 {
		t.Errorf("null expression: revoke %v keep %v", grantKeys(res.Revoke), res.Keep)
	}

	teams := []any{
		map[string]any{"team": "Blue", "organization": "Default1"}, map[string]any{"team": "Blue", "organization": "Default2"},
		map[string]any{"team": "Blue", "organization": "Default3"}, map[string]any{"team": "Red", "organization": "Default1"},
		map[string]any{"team": "Green", "organization": "Default1"}, map[string]any{"team": "Green", "organization": "Default3"},
		map[string]any{"team": "Yellow", "team_alias": "Yellow_Alias", "organization": "Default4"},
	}
	for _, c := range []struct {
		remove        bool
		groups        []string
		grant, revoke []string
	}{
		{false, []string{"Blue", "Red"}, []string{"Team Member Default1 Blue", "Team Member Default1 Red", "Team Member Default2 Blue", "Team Member Default3 Blue"},
			[]string{"Team Member Default1 Green", "Team Member Default3 Green", "Team Member Default4 Yellow"}},
		{true, []string{"Green"}, []string{"Team Member Default1 Green", "Team Member Default3 Green"},
			[]string{"Team Member Default1 Blue", "Team Member Default1 Red", "Team Member Default2 Blue", "Team Member Default3 Blue", "Team Member Default4 Yellow"}},
		{true, []string{"Yellow"}, []string{"Team Member Default4 Yellow_Alias"}, nil},
	} {
		res := samlMapping(&samlConfig{teamAttr: map[string]any{"saml_attr": "groups", "remove": c.remove, "team_org_map": teams}}, "x", "",
			map[string][]string{"groups": c.groups}, false, false)
		got := grantKeys(res.Grants)
		var teamRevokes []string
		for _, k := range grantKeys(res.Revoke) {
			if k[:4] == "Team" {
				teamRevokes = append(teamRevokes, k)
			}
		}
		if !reflect.DeepEqual(got, c.grant) || c.revoke != nil && !reflect.DeepEqual(teamRevokes, c.revoke) ||
			(len(res.KeepRoles) > 0) == c.remove {
			t.Errorf("teams %v remove %v: grant %v revoke %v keep %v", c.groups, c.remove, got, teamRevokes, res.KeepRoles)
		}
	}
}

// AWX's test__check_flag.
func TestSAMLFlagsLikeAWX(t *testing.T) {
	attrs := map[string][]string{"email": {"noone@nowhere.com"}, "is_superuser": {"something", "else", "true"}, "username": {"test_id"},
		"Role": {"test-role-1", "something", "different"}, "name_id": {"test_id"}}
	for _, c := range []struct {
		flags map[string]any
		old   bool
		want  bool
	}{
		{map[string]any{}, false, false},
		{map[string]any{"is_superuser_role": "test-role-1"}, false, true},
		{map[string]any{"is_superuser_attr": "is_superuser"}, false, true},
		{map[string]any{"is_superuser_attr": "is_superuser", "is_superuser_value": "junk"}, false, false},
		{map[string]any{"is_superuser_attr": "is_superuser", "is_superuser_value": "true"}, false, true},
		{map[string]any{"is_superuser_role": "test-role-1", "is_superuser_attr": "gibberish", "is_superuser_value": "true"}, false, true},
		{map[string]any{"is_superuser_role": "test-role-1", "is_superuser_attr": "test-role-1"}, false, true},
		{map[string]any{"is_superuser_role": "test-role-1", "is_superuser_attr": "is_superuser", "is_superuser_value": "junk"}, false, false},
		{map[string]any{"is_superuser_role": "test-role-1", "is_superuser_attr": "is_superuser", "is_superuser_value": "true"}, false, true},
		{map[string]any{"is_superuser_attr": "name_id", "is_superuser_value": "test_id"}, false, true},
		{map[string]any{"is_superuser_attr": "name_id", "is_superuser_value": "junk"}, false, false},
		{map[string]any{"is_superuser_attr": "name_id", "is_superuser_value": "junk", "remove_superusers": true}, true, false},
		{map[string]any{"is_superuser_attr": "name_id", "is_superuser_value": "junk", "remove_superusers": false}, true, true},
		{map[string]any{"is_superuser_attr": "is_superuser", "is_superuser_value": []any{"junk", "junk2", "else", "junk"}}, false, true},
		{map[string]any{"is_superuser_attr": "is_superuser", "is_superuser_value": []any{"junk", "junk2", "junk"}}, true, false},
		{map[string]any{"is_superuser_role": []any{"junk", "junk2", "something", "junk"}}, false, true},
		{map[string]any{"is_superuser_role": []any{"junk", "junk2", "junk"}}, true, false},
	} {
		got := samlFlag(c.flags, "superuser", attrs, c.old)
		value := c.old
		if got != nil {
			value = *got
		}
		if value != c.want {
			t.Errorf("%v (was %v): %v", c.flags, c.old, value)
		}
	}
}
