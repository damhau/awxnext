package test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// The connection tests try a login method's settings, saved or not,
// against its server or provider, and report each step.

// steps lists a connection test's steps as "step[idp]:status".
func steps(r resp) string {
	var out []string
	for _, s := range r.Body["steps"].([]any) {
		m := s.(map[string]any)
		p := m["step"].(string)
		if idp, _ := m["idp"].(string); idp != "" {
			p += "[" + idp + "]"
		}
		out = append(out, p+":"+m["status"].(string))
	}
	return strings.Join(out, " ")
}

// stepDetail is the detail of a step, "step" or "step[idp]".
func stepDetail(r resp, name string) string {
	for _, s := range r.Body["steps"].([]any) {
		m := s.(map[string]any)
		p := m["step"].(string)
		if idp, _ := m["idp"].(string); idp != "" {
			p += "[" + idp + "]"
		}
		if p == name {
			return m["detail"].(string)
		}
	}
	return ""
}

func connectionTest(t *testing.T, a *client, body map[string]any, want string) resp {
	t.Helper()
	r := must(t, a.post("settings/connections/test/", body), 200)
	if got := steps(r); got != want {
		t.Errorf("steps: %s\nwant:  %s\n%s", got, want, r.Raw)
	}
	if ok := !strings.Contains(want, ":failed"); r.Body["ok"] != ok {
		t.Errorf("ok: %v, want %v", r.Body["ok"], ok)
	}
	return r
}

func TestConnectionTestLDAP(t *testing.T) {
	a := adminClient(t)
	t.Cleanup(func() { a.delete("settings/ldap/") })
	dir := newLDAPDir(t, false)
	people, groups := "ou=people,dc=example,dc=org", "ou=groups,dc=example,dc=org"
	dir.add("dc=example,dc=org", map[string][]string{"objectClass": {"domain"}}, "")
	dir.add(people, map[string][]string{"objectClass": {"organizationalUnit"}}, "")
	dir.add(groups, map[string][]string{"objectClass": {"organizationalUnit"}}, "")
	dir.add("cn=svc,dc=example,dc=org", map[string][]string{"objectClass": {"person"}}, "svc-pw")
	user := func(uid string) string {
		dn := "uid=" + uid + "," + people
		dir.add(dn, map[string][]string{"objectClass": {"inetOrgPerson"}, "uid": {uid}, "mail": {uid + "@example.org"}, "givenName": {strings.ToUpper(uid[:1]) + uid[1:]}}, uid+"-pw")
		return dn
	}
	alice, bob := user("alice"), user("bob")
	g := func(cn string) string { return "cn=" + cn + "," + groups }
	dir.addGroup(g("awx-users"), "groupOfNames", "member", alice)
	dir.addGroup(g("admins"), "groupOfNames", "member", alice)
	dir.addGroup(g("staff"), "groupOfNames", "member", bob)
	org := uniq("LDAP Test Org")
	// Server 3, not saved: the first URI does not answer.
	conf := map[string]any{
		"AUTH_LDAP_3_SERVER_URI":         "ldap://127.0.0.1:1 " + dir.url("ldap"),
		"AUTH_LDAP_3_CONNECTION_OPTIONS": map[string]any{"OPT_NETWORK_TIMEOUT": 5},
		"AUTH_LDAP_3_BIND_DN":            "cn=svc,dc=example,dc=org",
		"AUTH_LDAP_3_BIND_PASSWORD":      "svc-pw",
		"AUTH_LDAP_3_USER_SEARCH":        []any{people, "SCOPE_SUBTREE", "(uid=%(user)s)"},
		"AUTH_LDAP_3_GROUP_SEARCH":       []any{groups, "SCOPE_SUBTREE", "(objectClass=groupOfNames)"},
		"AUTH_LDAP_3_GROUP_TYPE":         "GroupOfNamesType",
		"AUTH_LDAP_3_REQUIRE_GROUP":      g("awx-users"),
		"AUTH_LDAP_3_USER_ATTR_MAP":      map[string]any{"first_name": "givenName", "email": "mail"},
		"AUTH_LDAP_3_ORGANIZATION_MAP":   map[string]any{org: map[string]any{"users": true, "admins": []any{g("admins")}}},
	}
	with := func(changes map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range conf {
			out[k] = v
		}
		for k, v := range changes {
			out[k] = v
		}
		return out
	}

	// Without a username: connect and bind.
	r := connectionTest(t, a, map[string]any{"login": "ldap3", "settings": conf}, "connect:ok bind:ok")
	if d := stepDetail(r, "connect"); !strings.Contains(d, "Connected to "+dir.url("ldap")) || !strings.Contains(d, "Before it: ldap://127.0.0.1:1: dial tcp 127.0.0.1:1: ") || strings.Contains(d, "Result Code") {
		t.Errorf("connect: %s", d)
	}
	if d := stepDetail(r, "bind"); d != "Bound as cn=svc,dc=example,dc=org." {
		t.Errorf("bind: %s", d)
	}

	// A user, with the password: every step, and what they would get.
	r = connectionTest(t, a, map[string]any{"login": "ldap3", "settings": conf, "username": "alice", "password": "alice-pw"},
		"connect:ok bind:ok user:ok password:ok groups:ok allowed:ok access:ok")
	u := r.Body["user"].(map[string]any)
	if u["dn"] != alice || u["first_name"] != "Alice" || u["email"] != "alice@example.org" ||
		!slices.Contains(toStrings(u["groups"]), g("admins")) || !slices.Contains(toStrings(u["groups"]), g("awx-users")) {
		t.Errorf("user: %v", u)
	}
	if grants := fmt.Sprint(u["grants"]); !strings.Contains(grants, "Organization Admin") || !strings.Contains(grants, "Organization Member") ||
		!strings.Contains(fmt.Sprint(u["would_create"]), "organization "+org) {
		t.Errorf("access: %v %v", u["grants"], u["would_create"])
	}

	// Each failure stops the test at its step, with why.
	r = connectionTest(t, a, map[string]any{"login": "ldap3", "settings": with(map[string]any{"AUTH_LDAP_3_BIND_PASSWORD": "wrong"})}, "connect:ok bind:failed")
	if d := stepDetail(r, "bind"); d != "The directory refuses the password of cn=svc,dc=example,dc=org (AUTH_LDAP_3_BIND_PASSWORD)." {
		t.Errorf("bind: %s", d)
	}
	connectionTest(t, a, map[string]any{"login": "ldap3", "settings": with(map[string]any{"AUTH_LDAP_3_SERVER_URI": "ldap://127.0.0.1:1"})}, "connect:failed")
	r = connectionTest(t, a, map[string]any{"login": "ldap3", "settings": conf, "username": "nobody"}, "connect:ok bind:ok user:failed")
	if d := stepDetail(r, "user"); d != "The user search finds no nobody." {
		t.Errorf("user: %s", d)
	}
	connectionTest(t, a, map[string]any{"login": "ldap3", "settings": conf, "username": "alice", "password": "wrong"}, "connect:ok bind:ok user:ok password:failed")
	r = connectionTest(t, a, map[string]any{"login": "ldap3", "settings": conf, "username": "bob"}, "connect:ok bind:ok user:ok groups:ok allowed:failed")
	if d := stepDetail(r, "allowed"); !strings.Contains(d, "Not a member of the required group "+g("awx-users")) {
		t.Errorf("allowed: %s", d)
	}
	r = connectionTest(t, a, map[string]any{"login": "ldap5"}, "settings:failed")
	if d := stepDetail(r, "settings"); d != "Set the server URI (AUTH_LDAP_5_SERVER_URI)." {
		t.Errorf("settings: %s", d)
	}
	connectionTest(t, a, map[string]any{"login": "ldap3", "settings": with(map[string]any{"AUTH_LDAP_3_GROUP_SEARCH": nil})}, "settings:failed")

	// Saved, a secret left as it is ($encrypted$, as the form has it) is the saved one.
	must(t, a.patch("settings/ldap/", conf), 200)
	connectionTest(t, a, map[string]any{"login": "ldap3", "settings": map[string]any{"AUTH_LDAP_3_BIND_PASSWORD": "$encrypted$"}, "username": "alice"},
		"connect:ok bind:ok user:ok groups:ok allowed:ok access:ok")

	// Refused: another server's setting, an invalid value, anyone but a superuser.
	for name, body := range map[string]map[string]any{
		"settings":                {"login": "ldap3", "settings": map[string]any{"AUTH_LDAP_SERVER_URI": dir.url("ldap")}},
		"AUTH_LDAP_3_USER_SEARCH": {"login": "ldap3", "settings": map[string]any{"AUTH_LDAP_3_USER_SEARCH": []any{"x"}}},
		"login":                   {"login": "ldap6"},
	} {
		if r := a.post("settings/connections/test/", body); r.Status != 400 || !strings.Contains(r.Raw, `"`+name+`"`) {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
	}
	other, _ := newUser(t, a)
	must(t, other.post("settings/connections/test/", map[string]any{"login": "ldap3"}), 403)
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestConnectionTestOIDC(t *testing.T) {
	a := adminClient(t)
	idp := newFakeIdP(t)
	r := connectionTest(t, a, map[string]any{"login": "oidc", "settings": map[string]any{
		"SOCIAL_AUTH_OIDC_OIDC_ENDPOINT": idp.srv.URL + "/.well-known/openid-configuration", "SOCIAL_AUTH_OIDC_KEY": "awx"}}, "discovery:ok keys:ok")
	if d := stepDetail(r, "discovery"); !strings.Contains(d, "sign-in at "+idp.srv.URL+"/authorize, tokens at "+idp.srv.URL+"/token") {
		t.Errorf("discovery: %s", d)
	}
	if d := stepDetail(r, "keys"); !strings.Contains(d, "1 signing key: k1 (RS256)") {
		t.Errorf("keys: %s", d)
	}

	// Not set up, then a URL that is not a provider.
	r = connectionTest(t, a, map[string]any{"login": "oidc", "settings": map[string]any{"SOCIAL_AUTH_OIDC_OIDC_ENDPOINT": "", "SOCIAL_AUTH_OIDC_KEY": ""}}, "settings:failed")
	if d := stepDetail(r, "settings"); !strings.Contains(d, "SOCIAL_AUTH_OIDC_OIDC_ENDPOINT") || !strings.Contains(d, "SOCIAL_AUTH_OIDC_KEY") {
		t.Errorf("settings: %s", d)
	}
	connectionTest(t, a, map[string]any{"login": "oidc", "settings": map[string]any{"SOCIAL_AUTH_OIDC_OIDC_ENDPOINT": idp.srv.URL + "/nope", "SOCIAL_AUTH_OIDC_KEY": "awx"}}, "discovery:failed")

	// A provider without the groups scope, whose keys are missing.
	var broken *httptest.Server
	broken = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"issuer": broken.URL, "authorization_endpoint": broken.URL + "/authorize", "token_endpoint": broken.URL + "/token",
			"jwks_uri": broken.URL + "/jwks", "scopes_supported": []string{"openid", "profile", "email"}})
	}))
	t.Cleanup(broken.Close)
	r = connectionTest(t, a, map[string]any{"login": "oidc", "settings": map[string]any{"SOCIAL_AUTH_OIDC_OIDC_ENDPOINT": broken.URL, "SOCIAL_AUTH_OIDC_KEY": "awx"}},
		"discovery:ok scopes:warning keys:failed")
	if d := stepDetail(r, "scopes"); !strings.HasPrefix(d, "The provider does not list groups among its scopes") {
		t.Errorf("scopes: %s", d)
	}
	if d := stepDetail(r, "keys"); d != broken.URL+"/jwks answers HTTP 404." {
		t.Errorf("keys: %s", d)
	}
}

// certFor is a self-signed certificate valid from notBefore to notAfter.
func certFor(t *testing.T, cn string, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, NotBefore: notBefore, NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c
}

func TestConnectionTestSAML(t *testing.T) {
	a := adminClient(t)
	idp := newFakeSAMLIdP(t)
	spKey, spCert := selfSigned(t, "awxnext")
	b64 := func(c *x509.Certificate) string { return base64.StdEncoding.EncodeToString(c.Raw) }
	expired := certFor(t, "old-idp", time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	idps := func(testCert string) map[string]any {
		return map[string]any{
			"test":  map[string]any{"entity_id": idp.idp.MetadataURL.String(), "url": idp.idp.SSOURL.String(), "x509cert": testCert},
			"other": map[string]any{"entity_id": "urn:example:other", "url": idp.idp.SSOURL.String(), "x509cert": b64(expired)},
		}
	}
	conf := map[string]any{
		"SOCIAL_AUTH_SAML_SP_PUBLIC_CERT":    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: spCert.Raw})),
		"SOCIAL_AUTH_SAML_SP_PRIVATE_KEY":    string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(spKey)})),
		"SOCIAL_AUTH_SAML_ORG_INFO":          map[string]any{"en-US": map[string]any{"name": "example", "displayname": "Example", "url": "https://www.example.com"}},
		"SOCIAL_AUTH_SAML_TECHNICAL_CONTACT": map[string]any{"givenName": "Tech", "emailAddress": "tech@example.com"},
		"SOCIAL_AUTH_SAML_SUPPORT_CONTACT":   map[string]any{"givenName": "Support", "emailAddress": "support@example.com"},
		"SOCIAL_AUTH_SAML_ENABLED_IDPS":      idps(b64(idp.cert)),
	}
	with := func(k string, v any) map[string]any {
		out := map[string]any{}
		for key, val := range conf {
			out[key] = val
		}
		out[k] = v
		return out
	}

	// The test IdP serves its metadata at its entity ID; the other's entity
	// ID is not a URL, and its certificate has expired.
	r := connectionTest(t, a, map[string]any{"login": "saml", "settings": conf},
		"service-provider:ok certificate[other]:failed metadata[other]:skipped sso[other]:ok certificate[test]:ok metadata[test]:ok sso[test]:ok")
	if d := stepDetail(r, "certificate[other]"); !strings.HasPrefix(d, "CN=old-idp expired on ") {
		t.Errorf("certificate: %s", d)
	}
	if d := stepDetail(r, "metadata[test]"); d != "Read "+idp.idp.MetadataURL.String()+": the entity ID, the certificate and the sign-in URL match." {
		t.Errorf("metadata: %s", d)
	}
	if d := stepDetail(r, "sso[test]"); !strings.HasPrefix(d, idp.idp.SSOURL.String()+" answers (HTTP ") {
		t.Errorf("sso: %s", d)
	}

	// A metadata URL given for the other: the metadata is not its.
	r = connectionTest(t, a, map[string]any{"login": "saml", "settings": conf, "metadata_urls": map[string]any{"other": idp.idp.MetadataURL.String()}},
		"service-provider:ok certificate[other]:failed metadata[other]:failed sso[other]:ok certificate[test]:ok metadata[test]:ok sso[test]:ok")
	if d := stepDetail(r, "metadata[other]"); !strings.Contains(d, "is for "+idp.idp.MetadataURL.String()+", not urn:example:other") {
		t.Errorf("metadata: %s", d)
	}

	// A certificate the identity provider does not sign with.
	_, stranger := selfSigned(t, "stranger")
	r = connectionTest(t, a, map[string]any{"login": "saml", "settings": with("SOCIAL_AUTH_SAML_ENABLED_IDPS", idps(b64(stranger)))},
		"service-provider:ok certificate[other]:failed metadata[other]:skipped sso[other]:ok certificate[test]:ok metadata[test]:failed sso[test]:ok")
	if d := stepDetail(r, "metadata[test]"); !strings.Contains(d, "The certificate set here is not one the metadata") || !strings.Contains(d, "CN=idp, valid until") {
		t.Errorf("metadata: %s", d)
	}

	// The service provider's key and certificate do not match.
	otherKey, _ := selfSigned(t, "other")
	r = connectionTest(t, a, map[string]any{"login": "saml", "settings": with("SOCIAL_AUTH_SAML_SP_PRIVATE_KEY",
		string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(otherKey)})))},
		"service-provider:failed certificate[other]:failed metadata[other]:skipped sso[other]:ok certificate[test]:ok metadata[test]:ok sso[test]:ok")
	if d := stepDetail(r, "service-provider"); !strings.Contains(d, "does not match") {
		t.Errorf("service provider: %s", d)
	}

	// Not set up: what is missing.
	r = connectionTest(t, a, map[string]any{"login": "saml", "settings": map[string]any{"SOCIAL_AUTH_SAML_ENABLED_IDPS": map[string]any{}}}, "settings:failed")
	if d := stepDetail(r, "settings"); !strings.Contains(d, "SAML is not configured") {
		t.Errorf("settings: %s", d)
	}
}
