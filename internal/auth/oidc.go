package auth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
	"golang.org/x/oauth2"
)

const stateCookie = "awx_oidc_state"

// OIDC implements OpenID Connect browser login, provider-issued bearer JWT
// validation and authoritative group mapping.
type OIDC struct {
	app  *api.App
	auth *Auth

	mu       sync.Mutex
	provider *oidc.Provider
	provKey  string
	client   *http.Client

	// Validated bearer tokens, keyed by token hash.
	cache sync.Map // string -> cachedPrincipal
}

type cachedPrincipal struct {
	userID  int64
	scope   string
	expires time.Time
}

type oidcConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	VerifySSL     bool
	Scopes        []string
	UsernameClaim string
	GroupsClaim   string
	Audiences     []string
	AllowBearer   bool
	LinkExisting  bool
	Rules         []Rule
	// AWX's organization and team maps.
	AWX AWXMaps
}

func NewOIDC(app *api.App) *OIDC { return &OIDC{app: app} }

func (o *OIDC) config(ctx context.Context) (*oidcConfig, error) {
	s := o.app.Settings
	cfg := &oidcConfig{
		Issuer:        strings.TrimSuffix(strings.TrimSuffix(s.String(ctx, "SOCIAL_AUTH_OIDC_OIDC_ENDPOINT"), "/.well-known/openid-configuration"), "/"),
		ClientID:      s.String(ctx, "SOCIAL_AUTH_OIDC_KEY"),
		VerifySSL:     s.Bool(ctx, "SOCIAL_AUTH_OIDC_VERIFY_SSL"),
		UsernameClaim: s.String(ctx, "OIDC_USERNAME_CLAIM"),
		GroupsClaim:   s.String(ctx, "OIDC_GROUPS_CLAIM"),
		AllowBearer:   s.Bool(ctx, "OIDC_ALLOW_BEARER"),
		LinkExisting:  s.Bool(ctx, "OIDC_LINK_EXISTING_USERS"),
	}
	secret, err := o.app.Crypto.Decrypt(s.String(ctx, "SOCIAL_AUTH_OIDC_SECRET"))
	if err != nil {
		return nil, fmt.Errorf("decrypt OIDC secret: %w", err)
	}
	cfg.ClientSecret = secret
	s.Decode(ctx, "OIDC_SCOPES", &cfg.Scopes)
	s.Decode(ctx, "OIDC_AUDIENCES", &cfg.Audiences)
	if len(cfg.Audiences) == 0 && cfg.ClientID != "" {
		cfg.Audiences = []string{cfg.ClientID}
	}
	cfg.Rules, err = ParseRules(s.Get(ctx, "OIDC_MAPPING"))
	if err != nil {
		return nil, err
	}
	if cfg.AWX, err = LoadAWXMaps(func(k string) any { return s.Get(ctx, k) }, "oidc"); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Enabled reports whether an OIDC provider is configured.
func (o *OIDC) Enabled(ctx context.Context) bool {
	s := o.app.Settings
	return s.String(ctx, "SOCIAL_AUTH_OIDC_OIDC_ENDPOINT") != "" && s.String(ctx, "SOCIAL_AUTH_OIDC_KEY") != ""
}

func (o *OIDC) providerFor(ctx context.Context, cfg *oidcConfig) (*oidc.Provider, context.Context, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := fmt.Sprintf("%s|%v", cfg.Issuer, cfg.VerifySSL)
	if o.provider == nil || o.provKey != key {
		o.client = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: !cfg.VerifySSL},
		}}
		p, err := oidc.NewProvider(oidc.ClientContext(ctx, o.client), cfg.Issuer)
		if err != nil {
			return nil, nil, fmt.Errorf("OIDC discovery: %w", err)
		}
		o.provider, o.provKey = p, key
	}
	return o.provider, oidc.ClientContext(ctx, o.client), nil
}

// Routes registers the SSO endpoints.
func (o *OIDC) Routes() {
	o.app.HandlePublic("GET", "/sso/login/oidc/", o.login)
	o.app.HandlePublic("GET", "/sso/complete/oidc/", o.complete)
	o.app.Handle("POST", "/api/v2/settings/oidc/test/", o.dryRun)
}

func (o *OIDC) oauthConfig(p *oidc.Provider, cfg *oidcConfig) *oauth2.Config {
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  o.app.Cfg.BaseURL + "/sso/complete/oidc/",
		Scopes:       scopes,
	}
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	Expires  int64  `json:"e"`
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func (o *OIDC) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.Warn("oidc login failed", "reason", msg, "err", err)
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

func (o *OIDC) login(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	cfg, err := o.config(c)
	if err != nil || !o.Enabled(c) {
		o.fail(w, r, "OIDC is not configured.", err)
		return
	}
	p, _, err := o.providerFor(c, cfg)
	if err != nil {
		o.fail(w, r, "The identity provider is unavailable.", err)
		return
	}
	st := loginState{State: randomString(32), Nonce: randomString(32), Verifier: oauth2.GenerateVerifier(),
		Next: safeNext(r.URL.Query().Get("next")), Expires: time.Now().Add(10 * time.Minute).Unix()}
	b, _ := json.Marshal(st)
	sealed, err := o.app.Crypto.Encrypt(string(b))
	if err != nil {
		o.fail(w, r, "Internal error.", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: url.QueryEscape(sealed), Path: "/sso/", HttpOnly: true,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode, MaxAge: 600})
	u := o.oauthConfig(p, cfg).AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	http.Redirect(w, r, u, http.StatusFound)
}

func (o *OIDC) complete(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie(stateCookie)
	if err != nil {
		o.fail(w, r, "Login session expired, please try again.", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/sso/", MaxAge: -1})
	raw, _ := url.QueryUnescape(ck.Value)
	plain, err := o.app.Crypto.Decrypt(raw)
	var st loginState
	if err != nil || json.Unmarshal([]byte(plain), &st) != nil || time.Now().Unix() > st.Expires {
		o.fail(w, r, "Login session expired, please try again.", err)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		o.fail(w, r, "The identity provider returned an error: "+e, nil)
		return
	}
	if q.Get("state") != st.State {
		o.fail(w, r, "Invalid login state.", nil)
		return
	}
	cfg, err := o.config(c)
	if err != nil {
		o.fail(w, r, "OIDC is misconfigured.", err)
		return
	}
	p, pctx, err := o.providerFor(c, cfg)
	if err != nil {
		o.fail(w, r, "The identity provider is unavailable.", err)
		return
	}
	oc := o.oauthConfig(p, cfg)
	tok, err := oc.Exchange(pctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		o.fail(w, r, "Could not complete login with the identity provider.", err)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idt, err := p.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(pctx, rawID)
	if err != nil || idt.Nonce != st.Nonce {
		o.fail(w, r, "Invalid identity token.", err)
		return
	}
	claims := map[string]any{}
	idt.Claims(&claims)
	// Groups normally come from the token; fall back to userinfo when the claim is absent.
	if claimPath(claims, cfg.GroupsClaim) == nil {
		if ui, err := p.UserInfo(pctx, oauth2.StaticTokenSource(tok)); err == nil {
			extra := map[string]any{}
			if ui.Claims(&extra) == nil {
				for k, v := range extra {
					if _, ok := claims[k]; !ok {
						claims[k] = v
					}
				}
			}
		}
	}
	uid, err := o.provision(c, cfg, claims)
	if err != nil {
		o.fail(w, r, err.Error(), err)
		return
	}
	if err := o.auth.StartSession(c, w, r, uid); err != nil {
		o.fail(w, r, "Internal error.", err)
		return
	}
	http.Redirect(w, r, st.Next, http.StatusFound)
}

// AuthenticateJWT validates a provider-issued bearer token.
func (o *OIDC) AuthenticateJWT(ctx context.Context, token string) (*rbac.Principal, error) {
	key := hashToken(token)
	if v, ok := o.cache.Load(key); ok {
		cp := v.(cachedPrincipal)
		if time.Now().Before(cp.expires) {
			p, err := LoadPrincipal(ctx, o.app, cp.userID)
			if p != nil {
				p.TokenScope = cp.scope
			}
			return p, err
		}
		o.cache.Delete(key)
	}
	unauthorized := api.Detail(401, "Invalid bearer token.")
	cfg, err := o.config(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.AllowBearer {
		return nil, unauthorized
	}
	p, pctx, err := o.providerFor(ctx, cfg)
	if err != nil {
		return nil, api.Detail(503, "The identity provider is unavailable.")
	}
	t, err := p.Verifier(&oidc.Config{SkipClientIDCheck: true}).Verify(pctx, token)
	if err != nil {
		return nil, unauthorized
	}
	audOK := false
	for _, a := range t.Audience {
		for _, want := range cfg.Audiences {
			if a == want {
				audOK = true
			}
		}
	}
	if !audOK {
		return nil, unauthorized
	}
	claims := map[string]any{}
	t.Claims(&claims)
	uid, err := o.provision(ctx, cfg, claims)
	if err != nil {
		return nil, api.Detail(401, err.Error())
	}
	exp := t.Expiry
	if max := time.Now().Add(5 * time.Minute); exp.After(max) {
		exp = max
	}
	o.cache.Store(key, cachedPrincipal{userID: uid, expires: exp})
	return LoadPrincipal(ctx, o.app, uid)
}

// username picks the username claim, falling back to client id (machine clients) and subject.
func username(cfg *oidcConfig, claims map[string]any) string {
	for _, k := range []string{cfg.UsernameClaim, "preferred_username", "upn", "email", "client_id", "azp", "appid", "sub"} {
		if k == "" {
			continue
		}
		if s, ok := claimPath(claims, k).(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// provision finds or creates the user for the claims and applies group mapping.
func (o *OIDC) provision(ctx context.Context, cfg *oidcConfig, claims map[string]any) (int64, error) {
	sub, _ := claims["sub"].(string)
	iss, _ := claims["iss"].(string)
	name := username(cfg, claims)
	if sub == "" || name == "" {
		return 0, errors.New("The identity token has no subject or username.")
	}
	externalID := iss + "|" + sub
	email, _ := claims["email"].(string)
	first, _ := claims["given_name"].(string)
	last, _ := claims["family_name"].(string)
	groups := claimStrings(claimPath(claims, cfg.GroupsClaim))

	tx, err := o.app.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var uid int64
	var active bool
	err = tx.QueryRow(ctx, "SELECT id, is_active FROM users WHERE external_source = 'oidc' AND external_id = $1", externalID).Scan(&uid, &active)
	if err != nil {
		var source string
		err = tx.QueryRow(ctx, "SELECT id, is_active, external_source FROM users WHERE username = $1", name).Scan(&uid, &active, &source)
		switch {
		case err == nil && source == "" && cfg.LinkExisting:
			_, err = tx.Exec(ctx, "UPDATE users SET external_source = 'oidc', external_id = $2 WHERE id = $1", uid, externalID)
			if err != nil {
				return 0, err
			}
		case err == nil:
			return 0, fmt.Errorf("A user named %q already exists and is not linked to this identity.", name)
		default:
			active = true
			if err := tx.QueryRow(ctx, `INSERT INTO users (username, email, first_name, last_name, external_source, external_id)
				VALUES ($1, $2, $3, $4, 'oidc', $5) RETURNING id`, name, email, first, last, externalID).Scan(&uid); err != nil {
				return 0, err
			}
		}
	}
	if !active {
		return 0, errors.New("This account is disabled.")
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET email = coalesce(nullif($2, ''), email), first_name = coalesce(nullif($3, ''), first_name),
		last_name = coalesce(nullif($4, ''), last_name), last_login = now() WHERE id = $1`, uid, email, first, last); err != nil {
		return 0, err
	}
	// AWX's maps match the user's username and email, as stored.
	var dbName, dbEmail string
	if err := tx.QueryRow(ctx, "SELECT username, email FROM users WHERE id = $1", uid).Scan(&dbName, &dbEmail); err != nil {
		return 0, err
	}
	result := Mapping{Rules: cfg.Rules, AWX: cfg.AWX}.Evaluate(Identity{Username: dbName, Email: dbEmail, Groups: groups})
	warnings, err := ApplyMapping(ctx, tx, uid, "oidc", result)
	if err != nil {
		return 0, err
	}
	for _, w := range warnings {
		slog.Warn("oidc mapping", "user", name, "warning", w)
	}
	return uid, tx.Commit(ctx)
}

// dryRun evaluates mapping rules against sample claims without changing anything.
func (o *OIDC) dryRun(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	if !c.User.Superuser {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	data, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	cfg, err := o.config(c)
	if err != nil {
		api.WriteError(w, api.BadRequest(err.Error()))
		return
	}
	if m, ok := data["mapping"]; ok {
		rules, err := ParseRules(normalizeJSON(m))
		if err != nil {
			api.WriteError(w, api.FieldError("mapping", err.Error()))
			return
		}
		cfg.Rules = rules
	}
	if gc, ok := data["groups_claim"].(string); ok && gc != "" {
		cfg.GroupsClaim = gc
	}
	if v, ok := data["organization_map"]; ok {
		if cfg.AWX.Orgs, err = ParseOrgMap(normalizeJSON(v)); err != nil {
			api.WriteError(w, api.FieldError("organization_map", err.Error()))
			return
		}
	}
	if v, ok := data["team_map"]; ok {
		if cfg.AWX.Teams, err = ParseTeamMap(normalizeJSON(v)); err != nil {
			api.WriteError(w, api.FieldError("team_map", err.Error()))
			return
		}
	}
	claims, _ := normalizeJSON(data["claims"]).(map[string]any)
	if claims == nil {
		api.WriteError(w, api.FieldError("claims", "An object with token claims is required."))
		return
	}
	groups := claimStrings(claimPath(claims, cfg.GroupsClaim))
	name, _ := claims["email"].(string)
	email := name
	name = username(cfg, claims)
	// An existing user is matched with the username and email it has.
	c.App.DB.QueryRow(c, "SELECT email FROM users WHERE username = $1 AND email <> ''", name).Scan(&email)
	res := Mapping{Rules: cfg.Rules, AWX: cfg.AWX}.Evaluate(Identity{Username: name, Email: email, Groups: groups})
	// Report objects that would be created.
	creates := []string{}
	listed := map[string]bool{}
	create := func(what string) {
		if !listed[what] {
			listed[what] = true
			creates = append(creates, what)
		}
	}
	for _, g := range res.Grants {
		if !g.Create {
			continue
		}
		var exists bool
		c.App.DB.QueryRow(c, "SELECT EXISTS(SELECT 1 FROM organizations WHERE name = $1)", g.Organization).Scan(&exists)
		if !exists {
			create("organization " + g.Organization)
		}
		if g.Team != "" {
			exists = exists && c.App.DB.QueryRow(c, "SELECT 1 FROM teams t JOIN organizations o ON o.id = t.organization_id WHERE o.name = $1 AND t.name = $2",
				g.Organization, g.Team).Scan(new(int)) == nil
			if !exists {
				create("team " + g.Team + " in " + g.Organization)
			}
		}
	}
	api.WriteJSON(w, 200, api.Obj{
		"username":     name,
		"email":        email,
		"groups_claim": cfg.GroupsClaim,
		"groups":       groups,
		"superuser":    res.Superuser,
		"auditor":      res.Auditor,
		"grants":       res.Grants,
		"kept":         res.Keep,
		"would_create": creates,
		"errors":       nonNilStrings(res.Errors),
	})
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// normalizeJSON converts json.Number values (from the request decoder) to plain JSON types.
func normalizeJSON(v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal(b, &out)
	return out
}
