package test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewjam/saml"
)

// selfSigned makes an RSA key and a certificate for it.
func selfSigned(t *testing.T, cn string) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return key, cert
}

// fakeSAMLIdP is an identity provider (crewjam/saml's) that reads
// awxnext's metadata and logs in whichever session the test sets.
type fakeSAMLIdP struct {
	srv     *httptest.Server
	idp     *saml.IdentityProvider
	cert    *x509.Certificate
	mu      sync.Mutex
	session *saml.Session
}

func (f *fakeSAMLIdP) GetSession(w http.ResponseWriter, r *http.Request, req *saml.IdpAuthnRequest) *saml.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := *f.session
	s.ID, s.CreateTime, s.ExpireTime = fmt.Sprint(time.Now().UnixNano()), time.Now(), time.Now().Add(time.Hour)
	return &s
}

func (f *fakeSAMLIdP) GetServiceProvider(r *http.Request, id string) (*saml.EntityDescriptor, error) {
	res, err := http.Get(srv.URL + "/sso/metadata/saml/")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var md saml.EntityDescriptor
	if err := xml.Unmarshal(b, &md); err != nil {
		return nil, fmt.Errorf("metadata: %v: %s", err, b)
	}
	return &md, nil
}

func newFakeSAMLIdP(t *testing.T) *fakeSAMLIdP {
	t.Helper()
	key, cert := selfSigned(t, "idp")
	f := &fakeSAMLIdP{cert: cert}
	var handler http.Handler
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(f.srv.Close)
	meta, _ := url.Parse(f.srv.URL + "/metadata")
	sso, _ := url.Parse(f.srv.URL + "/sso")
	f.idp = &saml.IdentityProvider{Key: key, Certificate: cert, Logger: discardLogger{}, MetadataURL: *meta, SSOURL: *sso,
		ServiceProviderProvider: f, SessionProvider: f}
	handler = f.idp.Handler()
	return f
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}
func (discardLogger) Print(...any)          {}
func (discardLogger) Println(...any)        {}
func (discardLogger) Fatal(...any)          {}
func (discardLogger) Fatalf(string, ...any) {}
func (discardLogger) Fatalln(...any)        {}
func (discardLogger) Panic(...any)          {}
func (discardLogger) Panicf(string, ...any) {}
func (discardLogger) Panicln(...any)        {}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// samlForm runs the login up to the identity provider's form.
func samlForm(t *testing.T, idpName string) url.Values {
	t.Helper()
	res, err := noRedirect.Get(srv.URL + "/sso/login/saml/?idp=" + idpName + "&next=/jobs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	if res.StatusCode != 302 || strings.HasPrefix(loc, "/login") {
		t.Fatalf("login: %d %s", res.StatusCode, loc)
	}
	page, err := http.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(page.Body)
	page.Body.Close()
	form := url.Values{}
	for _, name := range []string{"SAMLResponse", "RelayState"} {
		m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("identity provider page: %s", b)
		}
		form.Set(name, html.UnescapeString(string(m[1])))
	}
	return form
}

// samlPost posts a response to the ACS; it returns the session cookie, or
// the error the login page would show.
func samlPost(t *testing.T, form url.Values) (*http.Cookie, string) {
	t.Helper()
	res, err := noRedirect.PostForm(srv.URL+"/sso/complete/saml/", form)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	if strings.HasPrefix(loc, "/login?error=") {
		msg, _ := url.QueryUnescape(strings.TrimPrefix(loc, "/login?error="))
		return nil, msg
	}
	for _, c := range res.Cookies() {
		if c.Name == "awx_sessionid" {
			if loc != "/jobs" {
				t.Errorf("redirected to %q", loc)
			}
			return c, ""
		}
	}
	t.Fatalf("complete: %d %s", res.StatusCode, loc)
	return nil, ""
}

func meWithCookie(t *testing.T, ck *http.Cookie) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/v2/me/", nil)
	req.AddCookie(ck)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ Results []map[string]any }
	b, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(b, &out); err != nil || len(out.Results) != 1 {
		t.Fatalf("me: %s", b)
	}
	return out.Results[0]
}

func attr(name string, values ...string) saml.Attribute {
	a := saml.Attribute{Name: name, NameFormat: "urn:oasis:names:tc:SAML:2.0:attrname-format:basic"}
	for _, v := range values {
		a.Values = append(a.Values, saml.AttributeValue{Type: "xs:string", Value: v})
	}
	return a
}

// AWX's SAML login against a fake identity provider: the metadata, the
// login, user fields, organizations and teams from attributes and maps,
// flags, and what is refused.
func TestSAMLLogin(t *testing.T) {
	a := adminClient(t)
	base := must(t, a.get("settings/system/"), 200).Body["TOWER_URL_BASE"]
	must(t, a.patch("settings/system/", map[string]any{"TOWER_URL_BASE": srv.URL}), 200)
	t.Cleanup(func() {
		a.delete("settings/saml/")
		a.patch("settings/system/", map[string]any{"TOWER_URL_BASE": base})
	})
	idp := newFakeSAMLIdP(t)

	// Until it is configured, the metadata says what is missing.
	res, _ := http.Get(srv.URL + "/sso/metadata/saml/")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "SAML is not configured: set SOCIAL_AUTH_SAML_ENABLED_IDPS") {
		t.Errorf("unconfigured metadata: %s", b)
	}

	spKey, spCert := selfSigned(t, "awxnext")
	orgA, orgB, orgC := uniq("SAML A"), uniq("SAML B"), uniq("SAML C")
	conf := map[string]any{
		"SOCIAL_AUTH_SAML_SP_PUBLIC_CERT":    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: spCert.Raw})),
		"SOCIAL_AUTH_SAML_SP_PRIVATE_KEY":    string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(spKey)})),
		"SOCIAL_AUTH_SAML_ORG_INFO":          map[string]any{"en-US": map[string]any{"name": "example", "displayname": "Example Org", "url": "https://www.example.com"}},
		"SOCIAL_AUTH_SAML_TECHNICAL_CONTACT": map[string]any{"givenName": "Tech", "emailAddress": "tech@example.com"},
		"SOCIAL_AUTH_SAML_SUPPORT_CONTACT":   map[string]any{"givenName": "Support", "emailAddress": "support@example.com"},
		"SOCIAL_AUTH_SAML_ENABLED_IDPS": map[string]any{"test": map[string]any{"entity_id": idp.idp.MetadataURL.String(), "url": idp.idp.SSOURL.String(),
			"x509cert": base64.StdEncoding.EncodeToString(idp.cert.Raw), "attr_user_permanent_id": "name_id"}},
		"SOCIAL_AUTH_SAML_ORGANIZATION_ATTR":  map[string]any{"saml_attr": "member-of", "saml_admin_attr": "admin-of"},
		"SOCIAL_AUTH_SAML_TEAM_ATTR":          map[string]any{"saml_attr": "team", "team_org_map": []any{map[string]any{"team": "Blue", "team_alias": "Blue Team", "organization": orgA}, map[string]any{"team": "Red", "organization": orgA}}},
		"SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR": map[string]any{"is_superuser_role": []any{"awx_admins"}, "is_system_auditor_attr": "audit", "is_system_auditor_value": []any{"yes"}},
		"SOCIAL_AUTH_SAML_ORGANIZATION_MAP":   map[string]any{orgC: map[string]any{"users": true}},
	}
	must(t, a.patch("settings/saml/", conf), 200)
	s := must(t, a.get("settings/saml/"), 200).Body
	if s["SOCIAL_AUTH_SAML_CALLBACK_URL"] != srv.URL+"/sso/complete/saml/" || s["SOCIAL_AUTH_SAML_METADATA_URL"] != srv.URL+"/sso/metadata/saml/" ||
		s["SOCIAL_AUTH_SAML_SP_PRIVATE_KEY"] != "$encrypted$" {
		t.Errorf("saml settings: %v", s)
	}
	if backends := must(t, a.get("auth/"), 200).Body; backends["saml:test"] == nil {
		t.Errorf("auth backends: %v", backends)
	}

	// The metadata: entity ID, ACS, certificate, organization and contacts.
	res, _ = http.Get(srv.URL + "/sso/metadata/saml/")
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	var md saml.EntityDescriptor
	if err := xml.Unmarshal(b, &md); err != nil || res.Header.Get("Content-Type") != "text/xml" {
		t.Fatalf("metadata: %v %s", err, b)
	}
	sp := md.SPSSODescriptors[0]
	if md.EntityID != srv.URL || sp.AssertionConsumerServices[0].Location != srv.URL+"/sso/complete/saml/" ||
		!bytes.Contains(b, []byte(base64.StdEncoding.EncodeToString(spCert.Raw)[:40])) || md.Organization.OrganizationDisplayNames[0].Value != "Example Org" ||
		len(sp.ContactPeople) != 2 || sp.ContactPeople[1].EmailAddresses[0] != "support@example.com" {
		t.Errorf("metadata: %s", b)
	}

	login := func(s saml.Session) (*http.Cookie, string) {
		t.Helper()
		idp.mu.Lock()
		idp.session = &s
		idp.mu.Unlock()
		return samlPost(t, samlForm(t, "test"))
	}
	alice := saml.Session{NameID: "alice-pid", UserName: "alice-" + strings.ToLower(orgA[len(orgA)-6:]), UserEmail: uniq("alice") + "@example.com",
		UserGivenName: "Alice", UserSurname: "Liddell", CustomAttributes: []saml.Attribute{attr("Role", "awx_admins"), attr("member-of", orgA),
			attr("admin-of", orgB), attr("team", "Blue"), attr("audit", "no")}}
	ck, msg := login(alice)
	if ck == nil {
		t.Fatalf("alice: %s", msg)
	}
	me := meWithCookie(t, ck)
	aliceID := int64(me["id"].(float64))
	if me["username"] != alice.UserName || me["email"] != alice.UserEmail || me["first_name"] != "Alice" || me["last_name"] != "Liddell" ||
		me["is_superuser"] != true || me["is_system_auditor"] != false || me["external_account"] != "enterprise" ||
		fmt.Sprint(me["auth"]) != "[map[provider:saml uid:test:alice-pid]]" {
		t.Errorf("alice: %v", me)
	}
	role := func(name, org, team string) bool {
		t.Helper()
		q := fmt.Sprintf("role_user_assignments/?user=%d&role_definition__name=%s", aliceID, url.QueryEscape(name))
		var obj []map[string]any
		if team != "" {
			obj = must(t, a.get("teams/?name="+url.QueryEscape(team)), 200).results()
		} else {
			obj = must(t, a.get("organizations/?name="+url.QueryEscape(org)), 200).results()
		}
		if len(obj) == 0 {
			return false
		}
		return must(t, a.get(fmt.Sprintf("%s&object_id=%d", q, int64(obj[0]["id"].(float64)))), 200).count() == 1
	}
	if !role("Organization Member", orgA, "") || !role("Organization Admin", orgB, "") || role("Organization Member", orgB, "") ||
		!role("Organization Member", orgC, "") || !role("Team Member", orgA, "Blue Team") || role("Team Member", orgA, "Red") {
		t.Error("alice's memberships")
	}

	// The same response cannot be used twice.
	form := samlForm(t, "test")
	if ck, _ := samlPost(t, form); ck == nil {
		t.Fatal("second login")
	}
	if _, msg := samlPost(t, form); msg != "This SAML response was already used." {
		t.Errorf("replay: %q", msg)
	}

	// Each login maps again: attributes gone, memberships and flags gone.
	alice.CustomAttributes = []saml.Attribute{attr("audit", "yes")}
	login(alice)
	me = meWithCookie(t, ck)
	if role("Organization Member", orgA, "") || role("Organization Admin", orgB, "") || role("Team Member", orgA, "Blue Team") || !role("Organization Member", orgC, "") {
		t.Error("memberships after the attributes changed")
	}
	if u := must(t, a.get(fmt.Sprintf("users/%d/", aliceID)), 200).Body; u["is_superuser"] != false || u["is_system_auditor"] != true {
		t.Errorf("flags: %v %v", u["is_superuser"], u["is_system_auditor"])
	}
	// remove false keeps them.
	must(t, a.patch("settings/saml/", map[string]any{"SOCIAL_AUTH_SAML_ORGANIZATION_ATTR": map[string]any{"saml_attr": "member-of", "remove": false},
		"SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR": map[string]any{"is_system_auditor_attr": "audit", "is_system_auditor_value": []any{"yes"}, "remove_system_auditors": false}}), 200)
	alice.CustomAttributes = []saml.Attribute{attr("member-of", orgA)}
	login(alice)
	alice.CustomAttributes = nil
	login(alice)
	if !role("Organization Member", orgA, "") {
		t.Error("remove false removed the membership")
	}
	if u := must(t, a.get(fmt.Sprintf("users/%d/", aliceID)), 200).Body; u["is_system_auditor"] != true {
		t.Error("remove_system_auditors false removed the flag")
	}

	// A username already taken gets random hex; an email of another account is refused.
	_, localID := newUserNamed(t, a, uniq("carol"))
	local := must(t, a.get(fmt.Sprintf("users/%d/", localID)), 200).Body
	ck, msg = login(saml.Session{NameID: "carol-pid", UserName: local["username"].(string), UserEmail: uniq("carol") + "@example.com"})
	if ck == nil {
		t.Fatalf("carol: %s", msg)
	}
	if name := meWithCookie(t, ck)["username"].(string); !regexp.MustCompile("^" + regexp.QuoteMeta(local["username"].(string)) + "[0-9a-f]{16}$").MatchString(name) {
		t.Errorf("carol's username: %s", name)
	}
	must(t, a.patch(fmt.Sprintf("users/%d/", localID), map[string]any{"email": "taken-" + alice.UserEmail}), 200)
	if _, msg := login(saml.Session{NameID: "mallory-pid", UserName: "mallory", UserEmail: "taken-" + alice.UserEmail}); msg != "The given email address is associated with another account." {
		t.Errorf("email of another account: %q", msg)
	}
	if _, msg := login(saml.Session{UserName: "nobody"}); !strings.Contains(msg, "no name_id attribute") {
		t.Errorf("no NameID: %q", msg)
	}

	// The security settings awxnext uses: signed requests, a signed response
	// and an encrypted assertion (crewjam's identity provider does both), a NameID.
	must(t, a.patch("settings/saml/", map[string]any{"SOCIAL_AUTH_SAML_SECURITY_CONFIG": map[string]any{"authnRequestsSigned": true,
		"wantMessagesSigned": true, "wantAssertionsSigned": true, "wantAssertionsEncrypted": true, "wantNameId": true}}), 200)
	if ck, msg := login(alice); ck == nil {
		t.Errorf("with the security settings: %s", msg)
	}
	if res, _ := noRedirect.Get(srv.URL + "/sso/login/saml/?idp=test"); !strings.Contains(res.Header.Get("Location"), "Signature=") {
		t.Errorf("unsigned request: %s", res.Header.Get("Location"))
	}

	// A response signed by another key is refused.
	other, otherCert := selfSigned(t, "evil")
	idp.idp.Key, idp.idp.Certificate = other, otherCert
	if _, msg := login(alice); msg != "The SAML response is not valid." {
		t.Errorf("foreign signature: %q", msg)
	}
	if res, _ := noRedirect.Get(srv.URL + "/sso/login/saml/?idp=nope"); !strings.Contains(res.Header.Get("Location"), "Unknown") {
		t.Errorf("unknown identity provider: %v", res.Header.Get("Location"))
	}

	// AWX's validation of the settings.
	for key, v := range map[string]any{
		"SOCIAL_AUTH_SAML_ENABLED_IDPS":       map[string]any{"x": map[string]any{"entity_id": "e", "x509cert": "MII"}},
		"SOCIAL_AUTH_SAML_ORG_INFO":           map[string]any{"english": map[string]any{"name": "n", "displayname": "d", "url": "https://x"}},
		"SOCIAL_AUTH_SAML_TECHNICAL_CONTACT":  map[string]any{"givenName": "T", "emailAddress": "not an email"},
		"SOCIAL_AUTH_SAML_ORGANIZATION_ATTR":  map[string]any{"saml_attr": "x", "bogus": true},
		"SOCIAL_AUTH_SAML_TEAM_ATTR":          map[string]any{"saml_attr": "t", "team_org_map": []any{map[string]any{"team": "x"}}},
		"SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR": map[string]any{"is_admin_role": []any{"x"}},
		"SOCIAL_AUTH_SAML_SP_PUBLIC_CERT":     "not a certificate",
		"SOCIAL_AUTH_SAML_SECURITY_CONFIG":    map[string]any{"wantAssertionsSigned": "yes please"},
	} {
		if r := a.patch("settings/saml/", map[string]any{key: v}); r.Status != 400 || !strings.Contains(r.Raw, key) {
			t.Errorf("%s accepted: %d %s", key, r.Status, r.Raw)
		}
	}
}
