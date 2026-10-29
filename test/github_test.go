package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/damhau/awxnext/internal/auth"
)

type ghUser struct {
	ID     int64
	Login  string
	Name   string
	Email  string // public email; empty sends null
	Emails []string
}

// fakeGitHub is GitHub's OAuth and API endpoints, at / and /api/v3/ (as
// GitHub Enterprise has them): it logs in whichever user the test sets.
type fakeGitHub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	user     ghUser
	codes    map[string]ghUser
	tokens   map[string]ghUser
	orgs     map[string][]string // organization -> logins
	teams    map[string][]string // team id -> logins
	secret   string
	scope    string
	clientID string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{codes: map[string]ghUser{}, tokens: map[string]ghUser{}, orgs: map[string][]string{}, teams: map[string][]string{}, secret: "gh-secret"}
	memberRe := regexp.MustCompile(`^/(orgs|teams)/([^/]+)/members/([^/]+)$`)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/login/oauth/authorize":
			q := r.URL.Query()
			f.scope, f.clientID = q.Get("scope"), q.Get("client_id")
			code := uniq("code")
			f.codes[code] = f.user
			http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
			return
		case "/login/oauth/access_token":
			r.ParseForm()
			u, ok := f.codes[r.PostForm.Get("code")]
			if !ok || r.PostForm.Get("client_secret") != f.secret {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"bad_verification_code"}`))
				return
			}
			delete(f.codes, r.PostForm.Get("code"))
			tok := uniq("gho")
			f.tokens[tok] = u
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "bearer", "scope": f.scope})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		u, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "token ")]
		if !ok {
			w.WriteHeader(401)
			return
		}
		switch {
		case path == "/user":
			var email any
			if u.Email != "" {
				email = u.Email
			}
			json.NewEncoder(w).Encode(map[string]any{"id": u.ID, "login": u.Login, "name": u.Name, "email": email})
		case path == "/user/emails":
			var out []map[string]any
			for i, e := range u.Emails {
				out = append(out, map[string]any{"email": e, "primary": i == len(u.Emails)-1, "verified": true})
			}
			json.NewEncoder(w).Encode(out)
		case memberRe.MatchString(path):
			m := memberRe.FindStringSubmatch(path)
			members := f.orgs[m[2]]
			if m[1] == "teams" {
				members = f.teams[m[2]]
			}
			for _, login := range members {
				if login == m[3] {
					w.WriteHeader(204)
					return
				}
			}
			w.WriteHeader(404)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// socialLogin runs a login through the browser redirects; it returns the
// session cookie, or the error the login page shows.
func socialLogin(t *testing.T, backend string) (*http.Cookie, string) {
	t.Helper()
	res, err := noRedirect.Get(srv.URL + "/sso/login/" + backend + "/?next=/jobs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	if strings.HasPrefix(loc, "/login?error=") {
		msg, _ := url.QueryUnescape(strings.TrimPrefix(loc, "/login?error="))
		return nil, msg
	}
	var state *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "awx_social_state" {
			state = c
		}
	}
	res, err = noRedirect.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	req, _ := http.NewRequest("GET", res.Header.Get("Location"), nil)
	if state != nil {
		req.AddCookie(state)
	}
	res, err = noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc = res.Header.Get("Location")
	if strings.HasPrefix(loc, "/login?error=") {
		msg, _ := url.QueryUnescape(strings.TrimPrefix(loc, "/login?error="))
		return nil, msg
	}
	for _, c := range res.Cookies() {
		if c.Name == "awx_sessionid" && loc == "/jobs" {
			return c, ""
		}
	}
	t.Fatalf("complete: %d %s", res.StatusCode, loc)
	return nil, ""
}

// AWX's six GitHub logins against a fake GitHub: users, emails, names,
// organization and team membership, maps, and what is refused.
func TestGitHubLogins(t *testing.T) {
	a := adminClient(t)
	gh := newFakeGitHub(t)
	oldURL, oldAPI := auth.GitHubURL, auth.GitHubAPIURL
	auth.GitHubURL, auth.GitHubAPIURL = gh.srv.URL+"/", gh.srv.URL+"/"
	base := must(t, a.get("settings/system/"), 200).Body["TOWER_URL_BASE"]
	must(t, a.patch("settings/system/", map[string]any{"TOWER_URL_BASE": srv.URL}), 200)
	categories := []string{"github", "github-org", "github-team", "github-enterprise", "github-enterprise-org", "github-enterprise-team"}
	t.Cleanup(func() {
		auth.GitHubURL, auth.GitHubAPIURL = oldURL, oldAPI
		a.patch("settings/system/", map[string]any{"TOWER_URL_BASE": base})
		a.patch("settings/authentication/", map[string]any{"SOCIAL_AUTH_ORGANIZATION_MAP": nil})
		for _, c := range categories {
			a.delete("settings/" + c + "/")
		}
	})
	if _, msg := socialLogin(t, "github"); msg != "This login is not configured." {
		t.Errorf("unconfigured: %q", msg)
	}

	org, global := uniq("GitHub Org"), uniq("Global Org")
	// Logins with their own maps do not use the global map, as in AWX.
	must(t, a.patch("settings/authentication/", map[string]any{"SOCIAL_AUTH_ORGANIZATION_MAP": map[string]any{global: map[string]any{"users": true}}}), 200)
	must(t, a.patch("settings/github/", map[string]any{"SOCIAL_AUTH_GITHUB_KEY": "gh-key", "SOCIAL_AUTH_GITHUB_SECRET": gh.secret,
		"SOCIAL_AUTH_GITHUB_ORGANIZATION_MAP": map[string]any{org: map[string]any{"users": true, "admins": "/^octo/"}}}), 200)
	gs := must(t, a.get("settings/github/"), 200).Body
	if gs["SOCIAL_AUTH_GITHUB_CALLBACK_URL"] != srv.URL+"/sso/complete/github/" || gs["SOCIAL_AUTH_GITHUB_SECRET"] != "$encrypted$" {
		t.Errorf("github settings: %v", gs)
	}
	backends := must(t, a.get("auth/"), 200).Body
	if backends["github"] == nil || backends["github-org"] != nil {
		t.Errorf("auth backends: %v", backends)
	}

	octo := uniq("octocat")
	gh.user = ghUser{ID: 1001, Login: octo, Name: "Mona Lisa Octocat", Emails: []string{octo + "-old@example.com", octo + "@example.com"}}
	ck, msg := socialLogin(t, "github")
	if ck == nil {
		t.Fatalf("octocat: %s", msg)
	}
	me := meWithCookie(t, ck)
	octoID := int64(me["id"].(float64))
	if me["username"] != octo || me["email"] != octo+"@example.com" || me["first_name"] != "Mona" || me["last_name"] != "Lisa Octocat" ||
		me["external_account"] != "social" || fmt.Sprint(me["auth"]) != "[map[provider:github uid:1001]]" {
		t.Errorf("octocat: %v", me)
	}
	if gh.scope != "user:email,read:org" || gh.clientID != "gh-key" {
		t.Errorf("authorize: scope %q client %q", gh.scope, gh.clientID)
	}
	orgRole := func(name, orgName string, uid int64) bool {
		t.Helper()
		o := must(t, a.get("organizations/?name="+url.QueryEscape(orgName)), 200).results()
		if len(o) == 0 {
			return false
		}
		return must(t, a.get(fmt.Sprintf("role_user_assignments/?user=%d&object_id=%d&role_definition__name=%s", uid, int64(o[0]["id"].(float64)),
			url.QueryEscape(name))), 200).count() == 1
	}
	if !orgRole("Organization Member", org, octoID) || !orgRole("Organization Admin", org, octoID) || orgRole("Organization Member", global, octoID) {
		t.Error("octocat's memberships")
	}
	// The same GitHub user is the same account; the name follows GitHub.
	gh.user.Name = "Mona Octocat"
	if ck, _ := socialLogin(t, "github"); ck == nil || int64(meWithCookie(t, ck)["id"].(float64)) != octoID {
		t.Error("second login")
	}
	if u := must(t, a.get(fmt.Sprintf("users/%d/", octoID)), 200).Body; u["last_name"] != "Octocat" {
		t.Errorf("names: %v %v", u["first_name"], u["last_name"])
	}

	// A taken username gets random hex; an email of another account is refused.
	_, localID := newUserNamed(t, a, uniq("hubber"))
	local := must(t, a.get(fmt.Sprintf("users/%d/", localID)), 200).Body
	gh.user = ghUser{ID: 1002, Login: local["username"].(string), Email: uniq("hubber") + "@example.com"}
	if ck, msg := socialLogin(t, "github"); ck == nil || !regexp.MustCompile("^"+regexp.QuoteMeta(local["username"].(string))+"[0-9a-f]{16}$").
		MatchString(meWithCookie(t, ck)["username"].(string)) {
		t.Errorf("taken username: %s", msg)
	}
	must(t, a.patch(fmt.Sprintf("users/%d/", localID), map[string]any{"email": "admin-" + octo + "@example.com"}), 200)
	gh.user = ghUser{ID: 1003, Login: uniq("mallory"), Email: "admin-" + octo + "@example.com"}
	if _, msg := socialLogin(t, "github"); msg != "The given email address is associated with another account." {
		t.Errorf("email of another account: %q", msg)
	}
	gh.secret = "rotated"
	if _, msg := socialLogin(t, "github"); msg != "Could not complete login with the provider." {
		t.Errorf("wrong secret: %q", msg)
	}
	gh.secret = "gh-secret"

	// Organization and team logins check membership, publicly and on Enterprise.
	gh.orgs["acme"] = []string{"member-org"}
	gh.teams["42"] = []string{"member-team"}
	api := gh.srv.URL + "/api/v3/"
	conf := map[string]map[string]any{
		"github-org":             {"SOCIAL_AUTH_GITHUB_ORG_KEY": "k", "SOCIAL_AUTH_GITHUB_ORG_SECRET": gh.secret, "SOCIAL_AUTH_GITHUB_ORG_NAME": "acme"},
		"github-team":            {"SOCIAL_AUTH_GITHUB_TEAM_KEY": "k", "SOCIAL_AUTH_GITHUB_TEAM_SECRET": gh.secret, "SOCIAL_AUTH_GITHUB_TEAM_ID": "42"},
		"github-enterprise":      {"SOCIAL_AUTH_GITHUB_ENTERPRISE_URL": gh.srv.URL, "SOCIAL_AUTH_GITHUB_ENTERPRISE_API_URL": api, "SOCIAL_AUTH_GITHUB_ENTERPRISE_KEY": "k", "SOCIAL_AUTH_GITHUB_ENTERPRISE_SECRET": gh.secret},
		"github-enterprise-org":  {"SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_URL": gh.srv.URL, "SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_API_URL": api, "SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_KEY": "k", "SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_SECRET": gh.secret, "SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_NAME": "acme"},
		"github-enterprise-team": {"SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_URL": gh.srv.URL, "SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_API_URL": api, "SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_KEY": "k", "SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_SECRET": gh.secret, "SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_ID": "42"},
	}
	for i, backend := range categories[1:] {
		must(t, a.patch("settings/"+backend+"/", conf[backend]), 200)
		member, refusal := "member-org", "User doesn't belong to the organization"
		if strings.HasSuffix(backend, "-team") {
			member, refusal = "member-team", "User doesn't belong to the team"
		}
		if !strings.HasSuffix(backend, "-org") && !strings.HasSuffix(backend, "-team") {
			member, refusal = "", ""
		}
		id := int64(2000 + 10*i)
		if refusal != "" {
			gh.user = ghUser{ID: id, Login: uniq("outsider"), Email: uniq("outsider") + "@example.com"}
			if _, msg := socialLogin(t, backend); msg != refusal {
				t.Errorf("%s outsider: %q", backend, msg)
			}
		}
		login := member
		if login == "" {
			login = uniq("ghe")
		} else {
			// Each backend gets its own member, so the accounts differ.
			login = uniq(member)
			gh.orgs["acme"] = append(gh.orgs["acme"], login)
			gh.teams["42"] = append(gh.teams["42"], login)
		}
		gh.user = ghUser{ID: id + 1, Login: login, Name: "Member", Email: login + "@example.com"}
		ck, msg := socialLogin(t, backend)
		if ck == nil {
			t.Errorf("%s member: %s", backend, msg)
			continue
		}
		if me := meWithCookie(t, ck); fmt.Sprint(me["auth"]) != fmt.Sprintf("[map[provider:%s uid:%d]]", backend, id+1) {
			t.Errorf("%s: %v", backend, me["auth"])
		}
	}
	// A GitHub user is one account across GitHub's logins.
	gh.orgs["acme"] = append(gh.orgs["acme"], octo)
	gh.user = ghUser{ID: 1001, Login: octo, Name: "Mona Octocat", Email: octo + "@example.com"}
	if ck, msg := socialLogin(t, "github-org"); ck == nil || int64(meWithCookie(t, ck)["id"].(float64)) != octoID {
		t.Errorf("octocat through github-org: %s", msg)
	}

	// AWX's validation of the settings.
	for key, v := range map[string]any{
		"SOCIAL_AUTH_GITHUB_ENTERPRISE_API_URL":    "not a url",
		"SOCIAL_AUTH_GITHUB_TEAM_ORGANIZATION_MAP": map[string]any{"X": map[string]any{"remove": true}},
		"SOCIAL_AUTH_GITHUB_ORG_TEAM_MAP":          map[string]any{"X": map[string]any{"users": true}},
	} {
		cat := map[string]string{"SOCIAL_AUTH_GITHUB_ENTERPRISE_API_URL": "github-enterprise", "SOCIAL_AUTH_GITHUB_TEAM_ORGANIZATION_MAP": "github-team",
			"SOCIAL_AUTH_GITHUB_ORG_TEAM_MAP": "github-org"}[key]
		if r := a.patch("settings/"+cat+"/", map[string]any{key: v}); r.Status != 400 || !strings.Contains(r.Raw, key) {
			t.Errorf("%s accepted: %d %s", key, r.Status, r.Raw)
		}
	}
}
