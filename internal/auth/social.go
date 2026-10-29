package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/settings"
)

// OAuth2 social logins as python-social-auth's backends do them, with AWX's
// SOCIAL_AUTH_<BACKEND>_* settings: /sso/login/<backend>/ sends the user to
// the provider, /sso/complete/<backend>/ finishes the login, creates the
// user as python-social-auth's pipeline does and applies the backend's
// organization and team maps.

// The providers' public URLs; tests point them at fakes.
var (
	GitHubURL         = "https://github.com/"
	GitHubAPIURL      = "https://api.github.com/"
	GoogleAccountsURL = "https://accounts.google.com/"
	GoogleUserinfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
	AzureADURL        = "https://login.microsoftonline.com/common/"
)

// socialUser is what a backend learns about the user.
type socialUser struct {
	uid                                  string
	username, email, firstName, lastName string
}

// socialBackend is one python-social-auth OAuth2 backend.
type socialBackend struct {
	name, prefix, label string
	// family are the backends sharing the provider's user IDs (GitHub's
	// three logins, or GitHub Enterprise's): a user found by uid in any of
	// them is the same user, and the family's name tags their grants.
	family []string
	scopes []string
	// scopeSep joins the scopes: "," for GitHub, " " for the others.
	scopeSep string
	// extraParams are added to the authorization request.
	extraParams func(get func(string) any) map[string]string
	// allowed is python-social-auth's auth_allowed.
	allowed func(get func(string) any, u *socialUser) error
	// required are the settings (after the prefix) the backend needs, as
	// AWX enables a backend only when they are set.
	required []string
	// endpoints returns the authorization and token URLs and the API URL.
	endpoints func(get func(string) any) (authURL, tokenURL, apiURL string)
	// user loads the user with the access token.
	user func(ctx context.Context, b *socialBackend, get func(string) any, client *http.Client, token *oauth2.Token, api string) (*socialUser, error)
}

func appendSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}

func resolve(base, path string) string {
	b, err := url.Parse(appendSlash(base))
	if err != nil {
		return base + path
	}
	return b.ResolveReference(&url.URL{Path: path}).String()
}

// splitName is python-social-auth's get_user_names.
func splitName(full string) (string, string) {
	full = strings.TrimSpace(full)
	if first, last, ok := strings.Cut(full, " "); ok {
		return strings.TrimSpace(first), strings.TrimSpace(last)
	}
	return full, ""
}

// githubJSON gets an API path with the token.
func githubJSON(ctx context.Context, client *http.Client, u, token string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "awxnext")
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("%s: %s", u, res.Status)
	}
	if out != nil && len(b) > 0 {
		return res.StatusCode, json.Unmarshal(b, out)
	}
	return res.StatusCode, nil
}

// githubUser is GithubOAuth2.user_data and its member check: the user,
// their primary email when it is not public, and for organization and team
// logins the membership (any 2xx answer).
func githubUser(member string) func(context.Context, *socialBackend, func(string) any, *http.Client, *oauth2.Token, string) (*socialUser, error) {
	return func(ctx context.Context, b *socialBackend, get func(string) any, client *http.Client, tok *oauth2.Token, api string) (*socialUser, error) {
		token := tok.AccessToken
		var data struct {
			ID    json.Number `json:"id"`
			Login string      `json:"login"`
			Name  string      `json:"name"`
			Email string      `json:"email"`
		}
		if _, err := githubJSON(ctx, client, resolve(api, "user"), token, &data); err != nil {
			return nil, err
		}
		if data.Email == "" {
			var emails []struct {
				Email   string `json:"email"`
				Primary bool   `json:"primary"`
			}
			if _, err := githubJSON(ctx, client, resolve(api, "user/emails"), token, &emails); err == nil && len(emails) > 0 {
				data.Email = emails[0].Email
				for _, e := range emails {
					if e.Primary {
						data.Email = e.Email
						break
					}
				}
			}
		}
		switch member {
		case "NAME":
			org, _ := get(b.prefix + "NAME").(string)
			if _, err := githubJSON(ctx, client, resolve(api, "orgs/"+url.PathEscape(org)+"/members/"+url.PathEscape(data.Login)), token, nil); err != nil {
				return nil, errors.New("User doesn't belong to the organization")
			}
		case "ID":
			team, _ := get(b.prefix + "ID").(string)
			if _, err := githubJSON(ctx, client, resolve(api, "teams/"+url.PathEscape(team)+"/members/"+url.PathEscape(data.Login)), token, nil); err != nil {
				return nil, errors.New("User doesn't belong to the team")
			}
		}
		first, last := splitName(data.Name)
		return &socialUser{uid: data.ID.String(), username: data.Login, email: data.Email, firstName: first, lastName: last}, nil
	}
}

// socialBackends are the OAuth2 backends, by name.
func socialBackends() map[string]*socialBackend {
	out := map[string]*socialBackend{}
	for _, v := range settings.GitHubVariants {
		v := v
		b := &socialBackend{name: v.Backend, prefix: v.Prefix, label: v.Label, scopes: []string{"user:email", "read:org"}, scopeSep: ",",
			required: []string{"KEY", "SECRET"}, user: githubUser(v.Member), family: []string{"github", "github-org", "github-team"}}
		if v.Enterprise {
			b.family = []string{"github-enterprise", "github-enterprise-org", "github-enterprise-team"}
		}
		if v.Member != "" {
			b.required = append(b.required, v.Member)
		}
		if v.Enterprise {
			b.required = append([]string{"URL", "API_URL"}, b.required...)
			b.endpoints = func(get func(string) any) (string, string, string) {
				base, _ := get(v.Prefix + "URL").(string)
				api, _ := get(v.Prefix + "API_URL").(string)
				return resolve(base, "login/oauth/authorize"), resolve(base, "login/oauth/access_token"), appendSlash(api)
			}
		} else {
			b.endpoints = func(func(string) any) (string, string, string) {
				return resolve(GitHubURL, "login/oauth/authorize"), resolve(GitHubURL, "login/oauth/access_token"), GitHubAPIURL
			}
		}
		out[b.name] = b
	}
	// GoogleOAuth2: AWX's SCOPE ["profile"] then the backend's defaults.
	out["google-oauth2"] = &socialBackend{name: "google-oauth2", prefix: "SOCIAL_AUTH_GOOGLE_OAUTH2_", label: "Google", family: []string{"google-oauth2"},
		scopes: []string{"profile", "openid", "email", "profile"}, scopeSep: " ", required: []string{"KEY", "SECRET"},
		endpoints: func(func(string) any) (string, string, string) {
			return resolve(GoogleAccountsURL, "o/oauth2/auth"), resolve(GoogleAccountsURL, "o/oauth2/token"), GoogleUserinfoURL
		},
		extraParams: func(get func(string) any) map[string]string {
			out := map[string]string{}
			if m, ok := get("SOCIAL_AUTH_GOOGLE_OAUTH2_AUTH_EXTRA_ARGUMENTS").(map[string]any); ok {
				for k, v := range m {
					out[k] = fmt.Sprint(v)
				}
			}
			return out
		},
		allowed: func(get func(string) any, u *socialUser) error {
			domains := asStrings(get("SOCIAL_AUTH_GOOGLE_OAUTH2_WHITELISTED_DOMAINS"))
			if u.email == "" || len(domains) == 0 {
				return nil
			}
			_, domain, _ := strings.Cut(strings.ToLower(u.email), "@")
			for _, d := range domains {
				if strings.ToLower(d) == domain {
					return nil
				}
			}
			return errors.New("Your credentials aren't allowed")
		},
		user: googleUser,
	}
	out["azuread-oauth2"] = &socialBackend{name: "azuread-oauth2", prefix: "SOCIAL_AUTH_AZUREAD_OAUTH2_", label: "Azure AD", family: []string{"azuread-oauth2"},
		scopes: []string{"openid", "profile", "user_impersonation", "email"}, scopeSep: " ", required: []string{"KEY", "SECRET"},
		endpoints: func(func(string) any) (string, string, string) {
			return resolve(AzureADURL, "oauth2/authorize"), resolve(AzureADURL, "oauth2/token"), ""
		},
		user: azureUser,
	}
	return out
}

// googleUser is GoogleOAuth2: the userinfo endpoint; the email is the uid
// (AWX does not set USE_UNIQUE_USER_ID) and its local part the username.
func googleUser(ctx context.Context, b *socialBackend, get func(string) any, client *http.Client, tok *oauth2.Token, api string) (*socialUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("Google userinfo: %s", res.Status)
	}
	var data struct {
		Email, Name, GivenName, FamilyName string
	}
	var raw map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	data.Email, _ = raw["email"].(string)
	data.Name, _ = raw["name"].(string)
	data.GivenName, _ = raw["given_name"].(string)
	data.FamilyName, _ = raw["family_name"].(string)
	if data.Email == "" {
		return nil, errors.New("Google did not return an email address.")
	}
	first, last := userNames(data.Name, data.GivenName, data.FamilyName)
	local, _, _ := strings.Cut(data.Email, "@")
	return &socialUser{uid: data.Email, username: local, email: data.Email, firstName: first, lastName: last}, nil
}

// azureUser is AzureADOAuth2: the claims of the token response's id_token,
// read without checking its signature as python-social-auth does (it comes
// straight from the token endpoint); the upn is the uid and the name the
// username.
func azureUser(ctx context.Context, b *socialBackend, get func(string) any, client *http.Client, tok *oauth2.Token, api string) (*socialUser, error) {
	idToken, _ := tok.Extra("id_token").(string)
	if idToken == "" {
		idToken = tok.AccessToken
	}
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return nil, errors.New("Azure AD did not return an ID token.")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, errors.New("Azure AD's ID token cannot be read.")
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errors.New("Azure AD's ID token cannot be read.")
	}
	str := func(k string) string { s, _ := claims[k].(string); return s }
	upn := str("upn")
	if upn == "" {
		return nil, errors.New("Azure AD's ID token has no upn.")
	}
	email := str("email")
	if _, ok := claims["email"]; !ok {
		email = upn
	}
	return &socialUser{uid: upn, username: str("name"), email: email, firstName: str("given_name"), lastName: str("family_name")}, nil
}

// userNames is python-social-auth's get_user_names: the given and family
// names, or the full name split at its first space.
func userNames(full, first, last string) (string, string) {
	if full != "" && first == "" && last == "" {
		return splitName(full)
	}
	return strings.TrimSpace(first), strings.TrimSpace(last)
}

// Social logs users in through the OAuth2 backends.
type Social struct {
	auth     *Auth
	backends map[string]*socialBackend
	client   *http.Client
}

func newSocial(a *Auth) *Social {
	return &Social{auth: a, backends: socialBackends(), client: &http.Client{Timeout: 30 * time.Second}}
}

func (s *Social) get(ctx context.Context) func(string) any {
	return func(k string) any { return s.auth.app.Settings.Get(ctx, k) }
}

// enabled reports whether a backend's required settings are set.
func (s *Social) enabled(ctx context.Context, b *socialBackend) bool {
	get := s.get(ctx)
	for _, k := range b.required {
		if v, _ := get(b.prefix + k).(string); strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}

// Routes registers the endpoints of every backend.
func (s *Social) Routes() {
	app := s.auth.app
	const backends = "{backend:(?:github[a-z-]*|google-oauth2|azuread-oauth2)}"
	app.HandlePublic("GET", "/sso/login/"+backends+"/", s.login)
	app.HandlePublic("GET", "/sso/complete/"+backends+"/", s.complete)
}

func (s *Social) callback(ctx context.Context, b *socialBackend) string {
	return samlBase(s.auth.app, s.get(ctx)) + "/sso/complete/" + b.name + "/"
}

func (s *Social) config(ctx context.Context, b *socialBackend) (*oauth2.Config, string, error) {
	get := s.get(ctx)
	secret, err := s.auth.app.Crypto.Decrypt(fmt.Sprint(get(b.prefix + "SECRET")))
	if err != nil {
		return nil, "", err
	}
	authURL, tokenURL, apiURL := b.endpoints(get)
	key, _ := get(b.prefix + "KEY").(string)
	return &oauth2.Config{ClientID: key, ClientSecret: secret, RedirectURL: s.callback(ctx, b),
		Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
		// python-social-auth separates GitHub's scopes with commas.
		Scopes: []string{strings.Join(b.scopes, b.scopeSep)},
	}, apiURL, nil
}

func (s *Social) fail(w http.ResponseWriter, r *http.Request, b string, msg string, err error) {
	slog.Warn("social login failed", "backend", b, "reason", msg, "err", err)
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

const socialStateCookie = "awx_social_state"

type socialState struct {
	Backend string `json:"b"`
	State   string `json:"s"`
	Next    string `json:"x"`
	Expires int64  `json:"e"`
}

func (s *Social) backend(c *api.Ctx, r *http.Request) *socialBackend {
	b := s.backends[chi.URLParam(r, "backend")]
	if b == nil || !s.enabled(c, b) {
		return nil
	}
	return b
}

func (s *Social) login(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	b := s.backend(c, r)
	if b == nil {
		s.fail(w, r, chi.URLParam(r, "backend"), "This login is not configured.", nil)
		return
	}
	oc, _, err := s.config(c, b)
	if err != nil {
		s.fail(w, r, b.name, "Internal error.", err)
		return
	}
	st := socialState{Backend: b.name, State: randomString(32), Next: safeNext(r.URL.Query().Get("next")), Expires: time.Now().Add(10 * time.Minute).Unix()}
	raw, _ := json.Marshal(st)
	sealed, err := s.auth.app.Crypto.Encrypt(string(raw))
	if err != nil {
		s.fail(w, r, b.name, "Internal error.", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: socialStateCookie, Value: url.QueryEscape(sealed), Path: "/sso/", HttpOnly: true,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode, MaxAge: 600})
	var opts []oauth2.AuthCodeOption
	if b.extraParams != nil {
		for k, v := range b.extraParams(s.get(c)) {
			opts = append(opts, oauth2.SetAuthURLParam(k, v))
		}
	}
	http.Redirect(w, r, oc.AuthCodeURL(st.State, opts...), http.StatusFound)
}

func (s *Social) complete(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "backend")
	b := s.backend(c, r)
	if b == nil {
		s.fail(w, r, name, "This login is not configured.", nil)
		return
	}
	ck, err := r.Cookie(socialStateCookie)
	if err != nil {
		s.fail(w, r, name, "Login session expired, please try again.", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: socialStateCookie, Value: "", Path: "/sso/", MaxAge: -1})
	raw, _ := url.QueryUnescape(ck.Value)
	plain, err := s.auth.app.Crypto.Decrypt(raw)
	var st socialState
	if err != nil || json.Unmarshal([]byte(plain), &st) != nil || time.Now().Unix() > st.Expires || st.Backend != name {
		s.fail(w, r, name, "Login session expired, please try again.", err)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.fail(w, r, name, "The provider returned an error: "+e, nil)
		return
	}
	if q.Get("state") != st.State {
		s.fail(w, r, name, "Invalid login state.", nil)
		return
	}
	oc, apiURL, err := s.config(c, b)
	if err != nil {
		s.fail(w, r, name, "Internal error.", err)
		return
	}
	ctx := context.WithValue(c, oauth2.HTTPClient, s.client)
	tok, err := oc.Exchange(ctx, q.Get("code"))
	if err != nil {
		s.fail(w, r, name, "Could not complete login with the provider.", err)
		return
	}
	u, err := b.user(ctx, b, s.get(c), s.client, tok, apiURL)
	if err == nil && b.allowed != nil {
		err = b.allowed(s.get(c), u)
	}
	if err != nil {
		s.fail(w, r, name, err.Error(), err)
		return
	}
	uid, err := s.provision(c, b, u)
	if err != nil {
		s.fail(w, r, name, err.Error(), err)
		return
	}
	if err := s.auth.StartSession(c, w, r, uid); err != nil {
		s.fail(w, r, name, "Internal error.", err)
		return
	}
	http.Redirect(w, r, safeNext(st.Next), http.StatusFound)
}

// provision is python-social-auth's pipeline: the user of this backend and
// uid, or a new user with a unique username; then the backend's maps.
func (s *Social) provision(ctx context.Context, b *socialBackend, u *socialUser) (int64, error) {
	maps, err := LoadAWXMaps(s.get(ctx), b.name)
	if err != nil {
		return 0, err
	}
	id, username, email, err := findOrCreateExternal(ctx, s.auth.app, b.family, b.name, u.uid, u.username, u.email)
	if err != nil {
		return 0, err
	}
	tx, err := s.auth.app.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE users SET first_name = coalesce(nullif($2, ''), first_name), last_name = coalesce(nullif($3, ''), last_name),
		last_login = now() WHERE id = $1`, id, u.firstName, u.lastName); err != nil {
		return 0, err
	}
	res := Mapping{AWX: maps}.Evaluate(Identity{Username: username, Email: email})
	warnings, err := ApplyMapping(ctx, tx, id, b.family[0], res)
	if err != nil {
		return 0, err
	}
	for _, w := range warnings {
		slog.Warn("social mapping", "backend", b.name, "user", username, "warning", w)
	}
	return id, tx.Commit(ctx)
}

// findOrCreateExternal is python-social-auth's social_user, get_username,
// associate_by_email and create_user: the user with this uid in one of the
// sources (now marked with source); otherwise, unless another account has
// the email, a new user whose username is cleaned and made unique with
// random hex. It returns the user's id, username and email.
func findOrCreateExternal(ctx context.Context, app *api.App, sources []string, source, uid, username, email string) (int64, string, string, error) {
	var id int64
	var active bool
	var name, mail string
	err := app.DB.QueryRow(ctx, "SELECT id, is_active, username, email FROM users WHERE external_source = ANY($1) AND external_id = $2 ORDER BY id LIMIT 1",
		sources, uid).Scan(&id, &active, &name, &mail)
	if err == nil {
		if !active {
			return 0, "", "", errors.New("Your account is inactive")
		}
		app.DB.Exec(ctx, "UPDATE users SET external_source = $2 WHERE id = $1", id, source)
		return id, name, mail, nil
	}
	if email != "" {
		var n int
		app.DB.QueryRow(ctx, "SELECT count(*) FROM users WHERE lower(email) = lower($1)", email).Scan(&n)
		if n > 0 {
			return 0, "", "", errors.New("The given email address is associated with another account.")
		}
	}
	base := usernameCleanRe.ReplaceAllString(username, "")
	if base == "" {
		base = randomHex(32)
	}
	if len(base) > 150 {
		base = base[:150]
	}
	name = base
	for {
		var taken bool
		app.DB.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE lower(username) = lower($1))", name).Scan(&taken)
		if !taken {
			break
		}
		short := base
		if len(short) > 150-16 {
			short = short[:150-16]
		}
		name = short + randomHex(16)
	}
	if err := app.DB.QueryRow(ctx, `INSERT INTO users (username, email, external_source, external_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		name, email, source, uid).Scan(&id); err != nil {
		return 0, "", "", err
	}
	return id, name, email, nil
}

// socialBackendsList adds the enabled backends to /api/v2/auth/, as AWX does.
func (s *Social) backendsList(ctx context.Context, out api.Obj) {
	for name, b := range s.backends {
		if s.enabled(ctx, b) {
			out[name] = api.Obj{"login_url": "/sso/login/" + name + "/", "complete_url": s.callback(ctx, b), "name": b.label}
		}
	}
}
