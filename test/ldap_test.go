package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ldapLogin signs in through /api/login/ and returns /api/v2/me/, or nil
// when the login is refused.
func ldapLogin(t *testing.T, user, pass string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	res, err := http.Post(srv.URL+"/api/login/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var ck *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "awx_sessionid" {
			ck = c
		}
	}
	if ck == nil {
		return nil
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v2/me/", nil)
	req.AddCookie(ck)
	me, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer me.Body.Close()
	b, _ := io.ReadAll(me.Body)
	var out struct {
		Results []map[string]any `json:"results"`
	}
	json.Unmarshal(b, &out)
	if len(out.Results) != 1 {
		t.Fatalf("me: %s", b)
	}
	return out.Results[0]
}

// addGroup adds a group whose attr lists its members.
func (d *ldapDir) addGroup(dn string, class, attr string, members ...string) {
	cn := strings.TrimPrefix(strings.SplitN(dn, ",", 2)[0], "cn=")
	d.add(dn, map[string][]string{"objectClass": {class}, "cn": {cn}, attr: members}, "")
}

// AWX's LDAP login against a directory the test runs: user search and DN
// template, StartTLS and LDAPS, required and denied groups, user fields and
// flags, organization and team maps by group, group types, six servers.
func TestLDAPLogin(t *testing.T) {
	a := adminClient(t)
	t.Cleanup(func() { a.delete("settings/ldap/") })
	dir := newLDAPDir(t, false)
	people, groups := "ou=people,dc=example,dc=org", "ou=groups,dc=example,dc=org"
	dir.add("dc=example,dc=org", map[string][]string{"objectClass": {"domain"}}, "")
	dir.add(people, map[string][]string{"objectClass": {"organizationalUnit"}}, "")
	dir.add(groups, map[string][]string{"objectClass": {"organizationalUnit"}}, "")
	dir.add("cn=svc,dc=example,dc=org", map[string][]string{"objectClass": {"person"}}, "svc-pw")
	user := func(uid string, extra map[string][]string) string {
		dn := "uid=" + uid + "," + people
		attrs := map[string][]string{"objectClass": {"inetOrgPerson", "posixAccount"}, "uid": {uid}, "mail": {uid + "@example.org"}}
		for k, v := range extra {
			attrs[k] = v
		}
		dir.add(dn, attrs, uid+"-pw")
		return dn
	}
	alice := user("alice", map[string][]string{"givenName": {"Alice"}, "sn": {"Liddell"}, "gidNumber": {"6000"}})
	bob := user("bob", nil)
	carol := user("carol", nil)
	mallory := user("mallory", nil)
	user("dave", nil)
	user("erin", nil)
	g := func(cn string) string { return "cn=" + cn + "," + groups }
	dir.addGroup(g("awx-users"), "groupOfNames", "member", alice, bob, carol, mallory, "uid=erin,"+people)
	dir.addGroup(g("disabled"), "groupOfNames", "member", mallory)
	dir.addGroup(g("admins"), "groupOfNames", "member", alice)
	dir.addGroup(g("auditors"), "groupOfNames", "member", bob)
	dir.addGroup(g("web-leads"), "groupOfNames", "member", carol)
	dir.addGroup(g("web"), "groupOfNames", "member", g("web-leads"), bob)
	dir.addGroup(g("ops-posix"), "posixGroup", "memberUid", "carol")
	dir.binds = nil

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(caFile, []byte(dir.ca.pem), 0o600)
	org := uniq("LDAP Org")
	conf := map[string]any{
		"AUTH_LDAP_SERVER_URI":          dir.url("ldap"),
		"AUTH_LDAP_START_TLS":           true,
		"AUTH_LDAP_CONNECTION_OPTIONS":  map[string]any{"OPT_X_TLS_CACERTFILE": caFile, "OPT_NETWORK_TIMEOUT": 5, "OPT_REFERRALS": 0},
		"AUTH_LDAP_BIND_DN":             "cn=svc,dc=example,dc=org",
		"AUTH_LDAP_BIND_PASSWORD":       "svc-pw",
		"AUTH_LDAP_USER_SEARCH":         []any{people, "SCOPE_SUBTREE", "(uid=%(user)s)"},
		"AUTH_LDAP_GROUP_SEARCH":        []any{groups, "SCOPE_SUBTREE", "(objectClass=groupOfNames)"},
		"AUTH_LDAP_GROUP_TYPE":          "GroupOfNamesType",
		"AUTH_LDAP_GROUP_TYPE_PARAMS":   map[string]any{"name_attr": "cn"},
		"AUTH_LDAP_REQUIRE_GROUP":       g("awx-users"),
		"AUTH_LDAP_DENY_GROUP":          g("disabled"),
		"AUTH_LDAP_USER_ATTR_MAP":       map[string]any{"first_name": "givenName", "last_name": "sn", "email": "mail"},
		"AUTH_LDAP_USER_FLAGS_BY_GROUP": map[string]any{"is_superuser": g("admins"), "is_system_auditor": []any{g("auditors")}},
		"AUTH_LDAP_ORGANIZATION_MAP":    map[string]any{org: map[string]any{"admins": g("admins"), "auditors": g("auditors"), "users": true}},
		"AUTH_LDAP_TEAM_MAP":            map[string]any{"Web": map[string]any{"organization": org, "users": []any{g("web")}}},
	}
	must(t, a.patch("settings/ldap/", conf), 200)

	me := ldapLogin(t, "alice", "alice-pw")
	if me == nil {
		t.Fatal("alice could not log in")
	}
	if me["username"] != "alice" || me["first_name"] != "Alice" || me["last_name"] != "Liddell" || me["email"] != "alice@example.org" ||
		me["is_superuser"] != true || me["external_account"] != "ldap" || me["ldap_dn"] != alice {
		t.Errorf("alice: %v", me)
	}
	aliceID := int64(me["id"].(float64))
	// Searches as the service account, the password checked with alice's own bind.
	if fmt.Sprint(dir.binds) != fmt.Sprintf("[cn=svc,dc=example,dc=org %s cn=svc,dc=example,dc=org]", alice) {
		t.Errorf("binds: %v", dir.binds)
	}
	orgID := must(t, a.get("organizations/?name="+url.QueryEscape(org)), 200).results()[0]["id"].(float64)
	roles := map[string]string{"admins": "Organization Admin", "auditors": "Organization Audit", "users": "Organization Member"}
	in := func(role string, uid int64) bool {
		t.Helper()
		return must(t, a.get(fmt.Sprintf("role_user_assignments/?user=%d&object_id=%d&role_definition__name=%s", uid, int64(orgID),
			url.QueryEscape(roles[role]))), 200).count() == 1
	}
	if !in("admins", aliceID) || !in("users", aliceID) || in("auditors", aliceID) {
		t.Error("alice is not an admin and member, and only that, of the mapped organization")
	}
	if n := must(t, a.get(fmt.Sprintf("role_user_assignments/?user=%d&role_definition__name=Nope", aliceID)), 200).count(); n != 0 {
		t.Fatalf("the role filter is ignored: %d", n)
	}

	me = ldapLogin(t, "bob", "bob-pw")
	if me == nil || me["is_superuser"] != false || me["is_system_auditor"] != true {
		t.Fatalf("bob: %v", me)
	}
	bobID := int64(me["id"].(float64))
	webs := must(t, a.get("teams/?name=Web&organization="+fmt.Sprint(int64(orgID))), 200).results()
	if len(webs) != 1 {
		t.Fatalf("team Web: %v", webs)
	}
	web := int64(webs[0]["id"].(float64))
	teamHas := func(uid int64) bool {
		return must(t, a.get(fmt.Sprintf("teams/%d/users/?id=%d", web, uid)), 200).count() == 1
	}
	if !teamHas(bobID) || !in("auditors", bobID) || in("admins", bobID) {
		t.Error("bob's memberships")
	}
	// GroupOfNamesType does not follow web-leads into web.
	me = ldapLogin(t, "carol", "carol-pw")
	carolID := int64(me["id"].(float64))
	if teamHas(carolID) {
		t.Error("carol joined Web through a nested group")
	}

	// Refused: wrong or empty password, unknown user, denied group, missing required group.
	for _, c := range [][2]string{{"alice", "nope"}, {"alice", ""}, {"nobody", "x"}, {"mallory", "mallory-pw"}, {"dave", "dave-pw"}} {
		if ldapLogin(t, c[0], c[1]) != nil {
			t.Errorf("%s logged in", c[0])
		}
	}
	// The username is matched case-insensitively and kept in lower case.
	if me := ldapLogin(t, "ALICE", "alice-pw"); me == nil || int64(me["id"].(float64)) != aliceID {
		t.Errorf("ALICE: %v", me)
	}
	// Basic auth works for LDAP users too.
	if r := (&client{t: t, user: "bob", pass: "bob-pw"}).get("me/"); r.Status != 200 {
		t.Errorf("basic auth: %d", r.Status)
	}

	// Each login maps again: alice leaves admins.
	dir.remove(g("admins"), "member", alice)
	if me := ldapLogin(t, "alice", "alice-pw"); me["is_superuser"] != false || in("admins", aliceID) || !in("users", aliceID) {
		t.Errorf("after leaving admins: superuser %v, admin %v", me["is_superuser"], in("admins", aliceID))
	}
	// remove_users false keeps a membership the map no longer gives.
	conf["AUTH_LDAP_ORGANIZATION_MAP"] = map[string]any{org: map[string]any{"users": false, "remove_users": false}}
	must(t, a.patch("settings/ldap/", map[string]any{"AUTH_LDAP_ORGANIZATION_MAP": conf["AUTH_LDAP_ORGANIZATION_MAP"]}), 200)
	ldapLogin(t, "alice", "alice-pw")
	if !in("users", aliceID) {
		t.Error("remove_users false removed alice")
	}
	must(t, a.patch("settings/ldap/", map[string]any{"AUTH_LDAP_ORGANIZATION_MAP": map[string]any{org: map[string]any{"users": false}}}), 200)
	ldapLogin(t, "alice", "alice-pw")
	if in("users", aliceID) {
		t.Error("an unmatched user stayed")
	}

	// A local account is never taken over by LDAP.
	_, erinID := newUserNamed(t, a, "erin")
	must(t, a.patch(fmt.Sprintf("users/%d/", erinID), map[string]any{"password": "local-pw"}), 200)
	if ldapLogin(t, "erin", "erin-pw") != nil || ldapLogin(t, "erin", "local-pw") == nil {
		t.Error("erin's local account")
	}

	// LDAP manages the username, attribute map fields and the superuser flag.
	r := a.patch(fmt.Sprintf("users/%d/", aliceID), map[string]any{"first_name": "Al"})
	if r.Status != 400 || !strings.Contains(r.Raw, "Unable to change first_name on user managed by LDAP.") {
		t.Errorf("managed field: %d %s", r.Status, r.Raw)
	}
	must(t, a.patch(fmt.Sprintf("users/%d/", aliceID), map[string]any{"password": "ignored-pw"}), 200)
	if ldapLogin(t, "alice", "ignored-pw") != nil {
		t.Error("a password was set on an LDAP user")
	}

	// Posix groups, by memberUid and gidNumber.
	must(t, a.patch("settings/ldap/", map[string]any{
		"AUTH_LDAP_GROUP_SEARCH":      []any{groups, "SCOPE_SUBTREE", "(objectClass=posixGroup)"},
		"AUTH_LDAP_GROUP_TYPE":        "PosixGroupType",
		"AUTH_LDAP_GROUP_TYPE_PARAMS": map[string]any{},
		"AUTH_LDAP_REQUIRE_GROUP":     nil, "AUTH_LDAP_DENY_GROUP": nil, "AUTH_LDAP_USER_FLAGS_BY_GROUP": map[string]any{},
		"AUTH_LDAP_ORGANIZATION_MAP": map[string]any{org: map[string]any{"users": g("ops-posix")}},
	}), 200)
	dir.addGroup(g("primary"), "posixGroup", "memberUid")
	dir.entries[len(dir.entries)-1].attrs["gidNumber"] = []string{"6000"}
	must(t, a.patch("settings/ldap/", map[string]any{"AUTH_LDAP_ORGANIZATION_MAP": map[string]any{org: map[string]any{"users": g("ops-posix"), "admins": g("primary")}}}), 200)
	ldapLogin(t, "carol", "carol-pw")
	ldapLogin(t, "alice", "alice-pw")
	if !in("users", carolID) || in("users", aliceID) || !in("admins", aliceID) {
		t.Errorf("posix: carol member %v, alice member %v admin %v", in("users", carolID), in("users", aliceID), in("admins", aliceID))
	}

	// Server 1: LDAPS, a DN template, anonymous searches, nested groups.
	corp := newLDAPDir(t, true)
	corpPeople, corpGroups := "ou=people,dc=corp,dc=org", "ou=groups,dc=corp,dc=org"
	corp.add("dc=corp,dc=org", map[string][]string{"objectClass": {"domain"}}, "")
	frank := "uid=frank," + corpPeople
	corp.add(frank, map[string][]string{"objectClass": {"inetOrgPerson"}, "uid": {"frank"}, "mail": {"frank@corp.org"}}, "frank-pw")
	corp.addGroup("cn=leads,"+corpGroups, "groupOfUniqueNames", "uniqueMember", frank)
	corp.addGroup("cn=eng,"+corpGroups, "groupOfUniqueNames", "uniqueMember", "cn=leads,"+corpGroups)
	corpCA := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(corpCA, []byte(corp.ca.pem), 0o600)
	org2 := uniq("Corp")
	must(t, a.patch("settings/ldap/", map[string]any{
		"AUTH_LDAP_1_SERVER_URI":         "ldaps://127.0.0.1:1 " + corp.url("ldaps"),
		"AUTH_LDAP_1_CONNECTION_OPTIONS": map[string]any{"OPT_X_TLS_CACERTFILE": corpCA, "OPT_NETWORK_TIMEOUT": 5},
		"AUTH_LDAP_1_USER_DN_TEMPLATE":   "uid=%(user)s," + corpPeople,
		"AUTH_LDAP_1_GROUP_SEARCH":       []any{corpGroups, "SCOPE_ONELEVEL", "(objectClass=groupOfUniqueNames)"},
		"AUTH_LDAP_1_GROUP_TYPE":         "NestedGroupOfUniqueNamesType",
		"AUTH_LDAP_1_USER_ATTR_MAP":      map[string]any{"email": "mail"},
		"AUTH_LDAP_1_TEAM_MAP":           map[string]any{"Engineering": map[string]any{"organization": org2, "users": "cn=eng," + corpGroups}},
	}), 200)
	me = ldapLogin(t, "frank", "frank-pw")
	if me == nil || me["email"] != "frank@corp.org" || me["ldap_dn"] != frank {
		t.Fatalf("frank: %v", me)
	}
	eng := must(t, a.get("teams/?name=Engineering"), 200).results()
	if len(eng) != 1 || must(t, a.get(fmt.Sprintf("teams/%d/users/?id=%v", int64(eng[0]["id"].(float64)), me["id"])), 200).count() != 1 {
		t.Error("frank is not in Engineering through leads")
	}
	if fmt.Sprint(corp.binds) != fmt.Sprintf("[%s ]", frank) {
		t.Errorf("corp binds: %v", corp.binds)
	}

	// AWX's validation of the settings.
	for key, v := range map[string]any{
		"AUTH_LDAP_SERVER_URI":          "http://ldap.example.com",
		"AUTH_LDAP_USER_SEARCH":         []any{people, "SCOPE_SUBTREE", "(uid=alice)"},
		"AUTH_LDAP_2_GROUP_SEARCH":      []any{groups, "SCOPE_DEEP", "(objectClass=*)"},
		"AUTH_LDAP_USER_DN_TEMPLATE":    "uid=alice," + people,
		"AUTH_LDAP_GROUP_TYPE":          "UnknownGroupType",
		"AUTH_LDAP_GROUP_TYPE_PARAMS":   map[string]any{"colour": "blue"},
		"AUTH_LDAP_USER_ATTR_MAP":       map[string]any{"phone": "telephoneNumber"},
		"AUTH_LDAP_USER_FLAGS_BY_GROUP": map[string]any{"is_admin": g("admins")},
		"AUTH_LDAP_ORGANIZATION_MAP":    map[string]any{org: map[string]any{"remove": true}},
		"AUTH_LDAP_REQUIRE_GROUP":       "not a dn",
		"AUTH_LDAP_CONNECTION_OPTIONS":  map[string]any{"TIMEOUT": 1},
	} {
		if r := a.patch("settings/ldap/", map[string]any{key: v}); r.Status != 400 || !strings.Contains(r.Raw, key) {
			t.Errorf("%s accepted: %d %s", key, r.Status, r.Raw)
		}
	}
}
