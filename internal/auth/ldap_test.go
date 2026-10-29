package auth

import (
	"strings"
	"testing"
)

func TestLDAPSettingsParsing(t *testing.T) {
	one := []any{"ou=people,dc=example,dc=org", "SCOPE_SUBTREE", "(uid=%(user)s)"}
	if s, err := parseLDAPUserSearch(one); err != nil || len(s) != 1 || s[0].filter != "(uid=%(user)s)" {
		t.Errorf("one search: %v %v", s, err)
	}
	union := []any{one, []any{"ou=admins,dc=example,dc=org", "SCOPE_ONELEVEL", "(&(objectClass=person)(cn=%(user)s))"}}
	if s, err := parseLDAPUserSearch(union); err != nil || len(s) != 2 || s[1].scope != 1 {
		t.Errorf("union: %v %v", s, err)
	}
	for in, want := range map[string]any{
		"In order to ultilize LDAP Union, input element No. 2 should be a search query array.":      []any{one, "x"},
		"Error parsing LDAP Union element No. 1: Expected a list of three items but got 2 instead.": []any{[]any{"dc=x", "SCOPE_BASE"}},
		`DN must include "%(user)s" placeholder for username: (uid=alice)`:                          []any{"dc=x", "SCOPE_BASE", "(uid=alice)"},
		"Invalid filter: (uid=%(user)s": []any{"dc=x", "SCOPE_BASE", "(uid=%(user)s"},
		"Invalid DN: not a dn":          []any{"not a dn", "SCOPE_BASE", "(uid=%(user)s)"},
	} {
		if _, err := parseLDAPUserSearch(want); err == nil || err.Error() != in {
			t.Errorf("%v: %v", want, err)
		}
	}
	for _, dn := range []string{"", "cn=svc,dc=example,dc=org", `EXAMPLE\svc-awx`, "svc@example.com"} {
		if err := validateBindDN(dn); err != nil {
			t.Errorf("bind DN %q: %v", dn, err)
		}
	}
	if got := substitute("(&(uid=%(user)s)(note=100%%))", "a\\2ab"); got != "(&(uid=a\\2ab)(note=100%))" {
		t.Errorf("substitute: %s", got)
	}
	if uris, err := parseLDAPURIs("ldap://a.example.com, ldaps://b.example.com:636"); err != nil || len(uris) != 2 {
		t.Errorf("uris: %v %v", uris, err)
	}
	if _, err := newGroupType("MemberDNGroupType", map[string]any{"name_attr": "cn"}); err == nil || !strings.Contains(err.Error(), "Missing required parameters") {
		t.Errorf("MemberDNGroupType without member_attr: %v", err)
	}
	if g, _ := newGroupType("PosixUIDGroupType", map[string]any{"ldap_group_user_attr": "legacyuid"}); g.posixAttr != "legacyuid" {
		t.Errorf("PosixUIDGroupType: %+v", g)
	}
	if ldapSource("AUTH_LDAP_") != "ldap" || ldapSource("AUTH_LDAP_3_") != "ldap_3" {
		t.Error("sources")
	}
	if o, err := parseLDAPOptions(map[string]any{"OPT_X_TLS_REQUIRE_CERT": 0, "OPT_NETWORK_TIMEOUT": 2.5}); err != nil || !o.skipVerify || o.timeout.Seconds() != 2.5 {
		t.Errorf("options: %+v %v", o, err)
	}
}
