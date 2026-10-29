package test

import (
	"context"
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

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/importer"
)

// fakeOAuth is Google's and Azure AD's authorization, token and userinfo
// endpoints; it logs in whichever claims the test sets.
type fakeOAuth struct {
	srv       *httptest.Server
	mu        sync.Mutex
	claims    map[string]any
	authorize url.Values
	clients   map[string]string // client id -> secret
	codes     map[string]map[string]any
	tokens    map[string]map[string]any
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	f := &fakeOAuth{clients: map[string]string{}, codes: map[string]map[string]any{}, tokens: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/o/oauth2/auth", "/common/oauth2/authorize":
			f.authorize = r.URL.Query()
			code := uniq("code")
			f.codes[code] = f.claims
			http.Redirect(w, r, f.authorize.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(f.authorize.Get("state")), http.StatusFound)
		case "/o/oauth2/token", "/common/oauth2/token":
			r.ParseForm()
			claims, ok := f.codes[r.PostForm.Get("code")]
			if secret, known := f.clients[r.PostForm.Get("client_id")]; !ok || !known || secret != r.PostForm.Get("client_secret") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			tok := uniq("tok")
			f.tokens[tok] = claims
			body := map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600}
			if strings.HasPrefix(r.URL.Path, "/common/") {
				// An ID token whose signature python-social-auth does not check.
				payload, _ := json.Marshal(claims)
				body["id_token"] = "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".c2lnbmF0dXJl"
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(body)
		case "/oauth2/v3/userinfo":
			claims, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			if !ok {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(claims)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// AWX's Google and Azure AD logins work with AWX's settings as the
// importer brings them, without editing.
func TestGoogleAndAzureADFromAWXSettings(t *testing.T) {
	setup(t)
	ctx := context.Background()
	a := adminClient(t)
	fake := newFakeOAuth(t)
	fake.clients["google-client"], fake.clients["azure-client"] = "secret-31", "secret-32"
	old := []string{auth.GoogleAccountsURL, auth.GoogleUserinfoURL, auth.AzureADURL}
	auth.GoogleAccountsURL, auth.GoogleUserinfoURL, auth.AzureADURL = fake.srv.URL+"/", fake.srv.URL+"/oauth2/v3/userinfo", fake.srv.URL+"/common/"
	base := must(t, a.get("settings/system/"), 200).Body["TOWER_URL_BASE"]
	must(t, a.patch("settings/system/", map[string]any{"TOWER_URL_BASE": srv.URL}), 200)
	t.Cleanup(func() {
		auth.GoogleAccountsURL, auth.GoogleUserinfoURL, auth.AzureADURL = old[0], old[1], old[2]
		a.patch("settings/system/", map[string]any{"TOWER_URL_BASE": base})
		a.delete("settings/google-oauth2/")
		a.delete("settings/azuread-oauth2/")
	})

	// An AWX database with only these login settings, as AWX stores them.
	googleOrg, azureOrg := uniq("Google Org"), uniq("Azure Org")
	adminDB, err := pgx.Connect(ctx, databaseURL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close(ctx)
	name := fmt.Sprintf("awxsso_%d", time.Now().UnixNano())
	if _, err := adminDB.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	defer adminDB.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
	src, err := pgx.Connect(ctx, databaseURL(name))
	if err != nil {
		t.Fatal(err)
	}
	ts := "'2024-01-01T00:00:00Z'"
	rows := fmt.Sprintf(`INSERT INTO conf_setting VALUES
  (30,%[1]s,%[1]s,'SOCIAL_AUTH_GOOGLE_OAUTH2_KEY','"google-client"',NULL),
  (31,%[1]s,%[1]s,'SOCIAL_AUTH_GOOGLE_OAUTH2_SECRET','"$encrypted$UTF8$AESCBC$Z0FBQUFBQnE0Mjk4WlpRY0xrVzB6clJDRjMzWUlwLURLMnR0N3JfOWVrUjZrYS14dUoxN2dldk1oUDFvYXgtUUR4TV9oTmZUM2JkMFlYWUZBcWRzMmloZ0phbFJoVExvWWc9PQ=="',NULL),
  (33,%[1]s,%[1]s,'SOCIAL_AUTH_GOOGLE_OAUTH2_WHITELISTED_DOMAINS','["example.com"]',NULL),
  (34,%[1]s,%[1]s,'SOCIAL_AUTH_GOOGLE_OAUTH2_AUTH_EXTRA_ARGUMENTS','{"hd": "example.com"}',NULL),
  (35,%[1]s,%[1]s,'SOCIAL_AUTH_GOOGLE_OAUTH2_ORGANIZATION_MAP','{"%[2]s": {"admins": "ada@example.com", "users": true}}',NULL),
  (36,%[1]s,%[1]s,'SOCIAL_AUTH_AZUREAD_OAUTH2_KEY','"azure-client"',NULL),
  (32,%[1]s,%[1]s,'SOCIAL_AUTH_AZUREAD_OAUTH2_SECRET','"$encrypted$UTF8$AESCBC$Z0FBQUFBQnE0Mjk4b3hVOUhDQjhhOWY5X0RFTlRKSGtWUUFOMGZGZ19EVy04QjA0ZzNveVdiTWNiRnYtMXRUWWswbk1GaFMzUl9NYWZCQUN0U1BNMVk1UUJaV05ZY2R0d1E9PQ=="',NULL),
  (37,%[1]s,%[1]s,'SOCIAL_AUTH_AZUREAD_OAUTH2_TEAM_MAP','{"Cloud": {"organization": "%[3]s", "users": "/^[^@]+@contoso\\.com$/"}}',NULL),
  (38,%[1]s,%[1]s,'SOCIAL_AUTH_AZUREAD_OAUTH2_ORGANIZATION_MAP','{"%[3]s": {"users": true}}',NULL);`, ts, googleOrg, azureOrg)
	if _, err := src.Exec(ctx, awxSchema+rows); err != nil {
		t.Fatal(err)
	}
	src.Close(ctx)
	rep, err := importer.Run(ctx, app, importer.Options{SourceURL: databaseURL(name), SecretKey: "awx-secret-key"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["settings"] != 9 {
		t.Fatalf("imported %d settings: %v", rep.Counts["settings"], rep.Warnings)
	}
	backends := must(t, a.get("auth/"), 200).Body
	if backends["google-oauth2"] == nil || backends["azuread-oauth2"] == nil {
		t.Fatalf("auth backends: %v", backends)
	}
	if cb := must(t, a.get("settings/google-oauth2/"), 200).Body["SOCIAL_AUTH_GOOGLE_OAUTH2_CALLBACK_URL"]; cb != srv.URL+"/sso/complete/google-oauth2/" {
		t.Errorf("callback URL: %v", cb)
	}

	// Google: the email is the uid, its local part the username.
	ada := uniq("ada")
	fake.claims = map[string]any{"sub": "g-1", "email": ada + "@example.com", "name": "Ada Lovelace", "given_name": "Ada", "family_name": "King"}
	ck, msg := socialLogin(t, "google-oauth2")
	if ck == nil {
		t.Fatalf("google: %s", msg)
	}
	me := meWithCookie(t, ck)
	if me["username"] != ada || me["email"] != ada+"@example.com" || me["first_name"] != "Ada" || me["last_name"] != "King" ||
		fmt.Sprint(me["auth"]) != fmt.Sprintf("[map[provider:google-oauth2 uid:%s@example.com]]", ada) {
		t.Errorf("google user: %v", me)
	}
	if fake.authorize.Get("hd") != "example.com" || fake.authorize.Get("scope") != "profile openid email profile" || fake.authorize.Get("client_id") != "google-client" {
		t.Errorf("google authorize: %v", fake.authorize)
	}
	if !userInOrg(t, a, int64(me["id"].(float64)), googleOrg, "Organization Member") {
		t.Error("the Google map did not apply")
	}
	fake.claims = map[string]any{"sub": "g-2", "email": "eve@evil.example", "name": "Eve"}
	if _, msg := socialLogin(t, "google-oauth2"); msg != "Your credentials aren't allowed" {
		t.Errorf("other domain: %q", msg)
	}

	// Azure AD: the upn is the uid and the email, the name the username.
	fake.claims = map[string]any{"upn": "grace@contoso.com", "name": "Grace " + strings.ToUpper(ada[len(ada)-4:]), "given_name": "Grace", "family_name": "Hopper", "oid": "o-1"}
	ck, msg = socialLogin(t, "azuread-oauth2")
	if ck == nil {
		t.Fatalf("azure: %s", msg)
	}
	me = meWithCookie(t, ck)
	if me["username"] != "Grace"+strings.ToUpper(ada[len(ada)-4:]) || me["email"] != "grace@contoso.com" || me["last_name"] != "Hopper" ||
		fmt.Sprint(me["auth"]) != "[map[provider:azuread-oauth2 uid:grace@contoso.com]]" {
		t.Errorf("azure user: %v", me)
	}
	if fake.authorize.Get("scope") != "openid profile user_impersonation email" || fake.authorize.Get("client_id") != "azure-client" {
		t.Errorf("azure authorize: %v", fake.authorize)
	}
	graceID := int64(me["id"].(float64))
	if !userInOrg(t, a, graceID, azureOrg, "Organization Member") {
		t.Error("the Azure AD organization map did not apply")
	}
	if teams := must(t, a.get(fmt.Sprintf("users/%d/teams/", graceID)), 200).results(); len(teams) != 1 || teams[0]["name"] != "Cloud" {
		t.Errorf("the Azure AD team map: %v", teams)
	}
}

// userInOrg reports whether a user has a role on the named organization.
func userInOrg(t *testing.T, a *client, uid int64, org, role string) bool {
	t.Helper()
	o := must(t, a.get("organizations/?name="+url.QueryEscape(org)), 200).results()
	if len(o) == 0 {
		return false
	}
	return must(t, a.get(fmt.Sprintf("role_user_assignments/?user=%d&object_id=%d&role_definition__name=%s", uid, int64(o[0]["id"].(float64)),
		url.QueryEscape(role))), 200).count() == 1
}
