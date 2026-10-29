package test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// fakeIdP is a minimal OpenID Connect provider for tests.
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]map[string]any // code -> claims (incl. nonce, challenge)
	claims map[string]any            // claims for the next browser login
}

func newFakeIdP(t *testing.T) *fakeIdP {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeIdP{key: key, codes: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.srv.URL,
			"authorization_endpoint":                p.srv.URL + "/authorize",
			"token_endpoint":                        p.srv.URL + "/token",
			"userinfo_endpoint":                     p.srv.URL + "/userinfo",
			"jwks_uri":                              p.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := fmt.Sprint(time.Now().UnixNano())
		p.mu.Lock()
		c := map[string]any{}
		for k, v := range p.claims {
			c[k] = v
		}
		c["nonce"] = q.Get("nonce")
		c["_challenge"] = q.Get("code_challenge")
		p.codes[code] = c
		p.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		r.ParseForm()
		p.mu.Lock()
		c := p.codes[r.PostForm.Get("code")]
		delete(p.codes, r.PostForm.Get("code"))
		p.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if c == nil || base64.RawURLEncoding.EncodeToString(sum[:]) != c["_challenge"] {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		delete(c, "_challenge")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "opaque", "token_type": "Bearer", "expires_in": 300, "id_token": p.sign(t, c, "awx-client")})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeIdP) sign(t *testing.T, claims map[string]any, aud string) string {
	return p.signWith(t, p.key, claims, aud, time.Now().Add(5*time.Minute))
}

func (p *fakeIdP) signWith(t *testing.T, key *rsa.PrivateKey, claims map[string]any, aud string, exp time.Time) string {
	t.Helper()
	c := map[string]any{"iss": p.srv.URL, "aud": aud, "iat": time.Now().Unix(), "exp": exp.Unix()}
	for k, v := range claims {
		c[k] = v
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(c)
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := obj.CompactSerialize()
	return s
}

func configureOIDC(t *testing.T, a *client, idp *fakeIdP, mapping []map[string]any) {
	t.Helper()
	must(t, a.patch("settings/oidc/", map[string]any{
		"SOCIAL_AUTH_OIDC_OIDC_ENDPOINT": idp.srv.URL,
		"SOCIAL_AUTH_OIDC_KEY":           "awx-client",
		"SOCIAL_AUTH_OIDC_SECRET":        "client-secret",
		"OIDC_MAPPING":                   mapping,
	}), 200)
	t.Cleanup(func() { a.delete("settings/oidc/") })
}

var testMapping = []map[string]any{
	{"group": "awx-admins", "superuser": true},
	{"group_regex": "^ops-(.+)$", "organization": "OIDC Ops", "team": "$1", "role": "member"},
	{"group": "ops-leads", "organization": "OIDC Ops", "role": "admin"},
}

func TestOIDCBearerAndMapping(t *testing.T) {
	a := adminClient(t)
	idp := newFakeIdP(t)
	configureOIDC(t, a, idp, testMapping)
	bearer := func(tok string) *client { return &client{t: t, token: tok} }

	// Superuser via group.
	tok := idp.sign(t, map[string]any{"sub": "u-root", "preferred_username": "oidc-root", "groups": []string{"awx-admins"}}, "awx-client")
	me := must(t, bearer(tok).get("me/"), 200).results()[0]
	if me["username"] != "oidc-root" || me["is_superuser"] != true {
		t.Fatalf("me: %v", me)
	}

	// Team + org membership created from a regex rule.
	sub := uniq("sub")
	tok = idp.sign(t, map[string]any{"sub": sub, "preferred_username": "oidc-alice", "email": "alice@example.com",
		"groups": []string{"ops-web", "ops-leads", "unrelated"}}, "awx-client")
	alice := bearer(tok)
	me = must(t, alice.get("me/"), 200).results()[0]
	aliceID := int64(me["id"].(float64))
	if me["email"] != "alice@example.com" {
		t.Fatalf("profile not synced: %v", me)
	}
	org := must(t, a.get("organizations/OIDC%20Ops/"), 200)
	orgID := org.id()
	team := must(t, a.get("teams/web++OIDC%20Ops/"), 200)
	if n := must(t, a.get(fmt.Sprintf("teams/%d/users/?id=%d", team.id(), aliceID)), 200).count(); n != 1 {
		t.Fatal("alice not in mapped team")
	}
	if n := must(t, a.get(fmt.Sprintf("organizations/%d/admins/?id=%d", orgID, aliceID)), 200).count(); n != 1 {
		t.Fatal("alice not org admin")
	}
	must(t, alice.patch(fmt.Sprintf("organizations/%d/", orgID), map[string]any{"description": "alice admin"}), 200)

	// A manual grant survives re-mapping; mapped grants are revoked when groups disappear.
	other := must(t, a.post("teams/", map[string]any{"name": uniq("manual"), "organization": orgID}), 201).id()
	must(t, a.post(fmt.Sprintf("teams/%d/users/", other), map[string]any{"id": aliceID}), 204)
	tok = idp.sign(t, map[string]any{"sub": sub, "preferred_username": "oidc-alice", "groups": []string{}}, "awx-client")
	must(t, bearer(tok).get("me/"), 200)
	if n := must(t, a.get(fmt.Sprintf("teams/%d/users/?id=%d", team.id(), aliceID)), 200).count(); n != 0 {
		t.Fatal("mapped team membership not revoked")
	}
	if n := must(t, a.get(fmt.Sprintf("organizations/%d/admins/?id=%d", orgID, aliceID)), 200).count(); n != 0 {
		t.Fatal("mapped org admin not revoked")
	}
	if n := must(t, a.get(fmt.Sprintf("teams/%d/users/?id=%d", other, aliceID)), 200).count(); n != 1 {
		t.Fatal("manual grant was revoked")
	}

	// Rejections: wrong audience, expired, foreign signing key.
	must(t, bearer(idp.sign(t, map[string]any{"sub": "x", "preferred_username": "x"}, "someone-else")).get("me/"), 401)
	must(t, bearer(idp.signWith(t, idp.key, map[string]any{"sub": "x", "preferred_username": "x"}, "awx-client", time.Now().Add(-time.Minute))).get("me/"), 401)
	evil, _ := rsa.GenerateKey(rand.Reader, 2048)
	must(t, bearer(idp.signWith(t, evil, map[string]any{"sub": "x", "preferred_username": "x"}, "awx-client", time.Now().Add(time.Minute))).get("me/"), 401)

	// A local account with the same username is not taken over.
	_, _ = newUserNamed(t, a, "oidc-local-bob")
	must(t, bearer(idp.sign(t, map[string]any{"sub": "bob-sub", "preferred_username": "oidc-local-bob"}, "awx-client")).get("me/"), 401)

	// Machine clients without a username claim use client_id.
	tok = idp.sign(t, map[string]any{"sub": "svc-sub", "client_id": "ci-pipeline", "groups": []string{"ops-deploy"}}, "awx-client")
	me = must(t, bearer(tok).get("me/"), 200).results()[0]
	if me["username"] != "ci-pipeline" {
		t.Fatalf("machine client username: %v", me["username"])
	}
}

func newUserNamed(t *testing.T, a *client, name string) (*client, int64) {
	t.Helper()
	r := must(t, a.post("users/", map[string]any{"username": name, "password": "pw"}), 201)
	return &client{t: t, user: name, pass: "pw"}, r.id()
}

func TestOIDCBrowserLogin(t *testing.T) {
	a := adminClient(t)
	idp := newFakeIdP(t)
	configureOIDC(t, a, idp, testMapping)
	idp.claims = map[string]any{"sub": uniq("browser"), "preferred_username": "oidc-browser", "groups": []string{"ops-db"}}

	jar := &cookieJar{}
	noFollow := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noFollow.Get(srv.URL + "/sso/login/oidc/?next=/jobs/42")
	if err != nil || res.StatusCode != 302 {
		t.Fatalf("login start: %v %v", err, res.StatusCode)
	}
	authz, _ := url.Parse(res.Header.Get("Location"))
	if authz.Query().Get("code_challenge_method") != "S256" || authz.Query().Get("nonce") == "" {
		t.Fatalf("missing PKCE/nonce: %s", authz)
	}
	res, err = noFollow.Get(authz.String())
	if err != nil || res.StatusCode != 302 {
		t.Fatalf("authorize: %v", err)
	}
	back, _ := url.Parse(res.Header.Get("Location"))
	// The redirect URI uses the configured base URL; replay it against the test server.
	res, err = noFollow.Get(srv.URL + back.Path + "?" + back.RawQuery)
	if err != nil || res.StatusCode != 302 {
		t.Fatalf("complete: %v %d", err, res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/jobs/42" {
		t.Fatalf("redirected to %q", loc)
	}
	res, _ = noFollow.Get(srv.URL + "/api/v2/me/")
	var me map[string]any
	json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	if res.StatusCode != 200 || me["results"].([]any)[0].(map[string]any)["username"] != "oidc-browser" {
		t.Fatalf("session after OIDC login: %d %v", res.StatusCode, me)
	}

	// Replaying the callback (state cookie consumed) fails.
	res, _ = noFollow.Get(srv.URL + back.Path + "?" + back.RawQuery)
	if !strings.HasPrefix(res.Header.Get("Location"), "/login?error=") {
		t.Fatalf("replay not rejected: %s", res.Header.Get("Location"))
	}
	// Open redirects are neutralized.
	res, _ = noFollow.Get(srv.URL + "/sso/login/oidc/?next=//evil.example")
	res.Body.Close()
}

func TestOIDCDryRunAndValidation(t *testing.T) {
	a := adminClient(t)
	idp := newFakeIdP(t)
	configureOIDC(t, a, idp, testMapping)
	r := must(t, a.post("settings/oidc/test/", map[string]any{
		"claims": map[string]any{"sub": "s", "preferred_username": "dry", "groups": []string{"ops-newteam", "awx-admins"}},
	}), 200)
	if r.Body["superuser"] != true || r.Body["username"] != "dry" {
		t.Fatalf("dry run: %s", r.Raw)
	}
	grants := r.Body["grants"].([]any)
	if len(grants) != 2 {
		t.Fatalf("grants: %v", grants)
	}
	if !strings.Contains(fmt.Sprint(r.Body["would_create"]), "team newteam") {
		t.Fatalf("would_create: %v", r.Body["would_create"])
	}
	// Nothing was created.
	must(t, a.get("teams/newteam++OIDC%20Ops/"), 404)
	// Keycloak-style nested claim with an inline mapping override.
	r = must(t, a.post("settings/oidc/test/", map[string]any{
		"groups_claim": "realm_access.roles",
		"mapping":      []map[string]any{{"group": "auditor", "auditor": true}},
		"claims":       map[string]any{"sub": "s", "preferred_username": "kc", "realm_access": map[string]any{"roles": []string{"auditor"}}},
	}), 200)
	if r.Body["auditor"] != true {
		t.Fatalf("nested claim: %s", r.Raw)
	}
	// Invalid rules are rejected when saved.
	must(t, a.patch("settings/oidc/", map[string]any{"OIDC_MAPPING": []map[string]any{{"group_regex": "(", "superuser": true}}}), 400)
	must(t, a.patch("settings/oidc/", map[string]any{"OIDC_MAPPING": []map[string]any{{"group": "x"}}}), 400)
	bob, _ := newUser(t, a)
	must(t, bob.post("settings/oidc/test/", map[string]any{"claims": map[string]any{}}), 403)
}

func TestDisableLocalAuth(t *testing.T) {
	a := adminClient(t)
	bob, _ := newUser(t, a)
	must(t, bob.get("me/"), 200)
	must(t, a.patch("settings/authentication/", map[string]any{"DISABLE_LOCAL_AUTH": true}), 200)
	t.Cleanup(func() { a.patch("settings/authentication/", map[string]any{"DISABLE_LOCAL_AUTH": false}) })
	must(t, bob.get("me/"), 401)
	must(t, a.get("me/"), 200) // break-glass superuser
}

// AWX's SOCIAL_AUTH_ORGANIZATION_MAP and SOCIAL_AUTH_TEAM_MAP give OIDC
// users the memberships AWX's social pipeline gives (its
// test_social_pipeline.py scenarios), re-applied at each login.
func TestOIDCAWXMaps(t *testing.T) {
	a := adminClient(t)
	idp := newFakeIdP(t)
	configureOIDC(t, a, idp, []map[string]any{})
	t.Cleanup(func() {
		a.patch("settings/authentication/", map[string]any{"SOCIAL_AUTH_ORGANIZATION_MAP": nil, "SOCIAL_AUTH_TEAM_MAP": nil})
	})
	orgName := uniq("AWX Map Org")
	setMaps := func(orgOpts, teamMap map[string]any) {
		t.Helper()
		body := map[string]any{"SOCIAL_AUTH_ORGANIZATION_MAP": map[string]any{orgName: orgOpts}, "SOCIAL_AUTH_TEAM_MAP": teamMap}
		must(t, a.patch("settings/authentication/", body), 200)
	}
	domain := uniq("foo") + ".com"
	ids := map[string]int64{}
	login := func(n int) {
		t.Helper()
		name := fmt.Sprintf("user%d@%s", n, domain)
		tok := idp.sign(t, map[string]any{"sub": name, "preferred_username": name, "email": name, "jti": uniq("jti")}, "awx-client")
		me := must(t, (&client{t: t, token: tok}).get("me/"), 200).results()[0]
		ids[name] = int64(me["id"].(float64))
	}
	count := func(role string) int {
		t.Helper()
		org := must(t, a.get("organizations/?name="+url.QueryEscape(orgName)), 200).results()
		if len(org) == 0 {
			return 0
		}
		return must(t, a.get(fmt.Sprintf("organizations/%d/%s/?username__endswith=%s", int64(org[0]["id"].(float64)), role, domain)), 200).count()
	}

	setMaps(map[string]any{"admins": "/.*/", "users": "/.*/"}, nil)
	for n := 1; n <= 3; n++ {
		login(n)
	}
	if count("admins") != 3 || count("users") != 3 {
		t.Fatalf("regex: %d admins, %d users", count("admins"), count("users"))
	}
	setMaps(map[string]any{"admins": false, "users": false}, nil)
	login(1)
	if count("admins") != 2 || count("users") != 2 {
		t.Errorf("remove: %d admins, %d users", count("admins"), count("users"))
	}
	setMaps(map[string]any{"admins": false, "users": false, "remove_admins": false, "remove_users": false}, nil)
	login(2)
	if count("admins") != 2 || count("users") != 2 {
		t.Errorf("remove disabled: %d admins, %d users", count("admins"), count("users"))
	}

	// A manual membership is never removed by a map.
	orgID := must(t, a.get("organizations/?name="+url.QueryEscape(orgName)), 200).results()[0]["id"].(float64)
	user1 := ids["user1@"+domain]
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", int64(orgID)), map[string]any{"id": user1}), 204)
	setMaps(map[string]any{"users": false}, nil)
	login(1)
	if n := must(t, a.get(fmt.Sprintf("organizations/%d/users/?id=%d", int64(orgID), user1)), 200).count(); n != 1 {
		t.Error("the manual membership was removed")
	}

	// Teams, by email; a team member does not join the organization.
	team := uniq("Blue")
	setMaps(map[string]any{"users": nil}, map[string]any{team: map[string]any{"organization": orgName, "users": []string{"user3@" + domain}}})
	login(3)
	login(2)
	tm := must(t, a.get("teams/?name="+url.QueryEscape(team)), 200).results()
	if len(tm) != 1 {
		t.Fatalf("team not created: %v", tm)
	}
	if n := must(t, a.get(fmt.Sprintf("teams/%d/users/", int64(tm[0]["id"].(float64)))), 200).count(); n != 1 {
		t.Errorf("team members: %d", n)
	}

	// AWX's validation and the dry run.
	r := a.patch("settings/authentication/", map[string]any{"SOCIAL_AUTH_ORGANIZATION_MAP": map[string]any{"X": map[string]any{"remove": true}}})
	if r.Status != 400 || !strings.Contains(r.Raw, `Invalid key(s): \"remove\".`) {
		t.Errorf("validation: %d %s", r.Status, r.Raw)
	}
	dry := must(t, a.post("settings/oidc/test/", map[string]any{
		"claims":           map[string]any{"preferred_username": "user2@" + domain},
		"organization_map": map[string]any{orgName: map[string]any{"admins": "/^user2@/", "users": false, "remove_users": false}},
		"team_map":         map[string]any{},
	}), 200).Body
	grants, kept := dry["grants"].([]any), dry["kept"].([]any)
	if len(grants) != 1 || grants[0].(map[string]any)["role_definition"] != "Organization Admin" ||
		grants[0].(map[string]any)["map"] != fmt.Sprintf("organization map %q admins", orgName) ||
		len(kept) != 1 || kept[0].(map[string]any)["role_definition"] != "Organization Member" || dry["email"] != "user2@"+domain {
		t.Errorf("dry run: %v", dry)
	}
	// What would be created is listed once each.
	newOrg := uniq("New Org")
	dry = must(t, a.post("settings/oidc/test/", map[string]any{
		"claims":           map[string]any{"preferred_username": "someone"},
		"organization_map": map[string]any{newOrg: map[string]any{"admins": true, "users": true}},
		"team_map":         map[string]any{"Web": map[string]any{"organization": newOrg, "users": true}},
	}), 200).Body
	if fmt.Sprint(dry["would_create"]) != fmt.Sprintf("[organization %s team Web in %s]", newOrg, newOrg) {
		t.Errorf("would create: %v", dry["would_create"])
	}
}
