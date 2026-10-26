// Package auth implements authentication: sessions, personal tokens, basic auth and OIDC.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/settings"
	"github.com/jackc/pgx/v5"
)

const (
	SessionCookie = "awx_sessionid"
	CSRFCookie    = "csrftoken"
	sessionAge    = 30 * time.Minute
)

type Auth struct {
	app  *api.App
	oidc *OIDC
}

// Install wires authentication into the API.
func Install(app *api.App) *Auth {
	a := &Auth{app: app, oidc: NewOIDC(app)}
	a.oidc.auth = a
	if d := settings.Lookup("OIDC_MAPPING"); d != nil {
		d.Validate = func(v any) error { _, err := ParseRules(v); return err }
	}
	api.SetAuthenticator(a.authenticate)
	app.HandlePublic("GET", "/api/login/", a.loginForm)
	app.HandlePublic("POST", "/api/login/", a.login)
	app.HandlePublic("GET", "/api/logout/", a.logout)
	app.HandlePublic("POST", "/api/logout/", a.logout)
	app.Handle("GET", "/api/v2/me/", a.me)
	app.HandlePublic("GET", "/api/v2/auth/", a.authBackends)
	a.oidc.Routes()
	tokensOnce.Do(registerTokens)
	return a
}

func randomString(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[x.Int64()]
	}
	return string(b)
}

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// LoadPrincipal builds a principal for a user id.
func LoadPrincipal(ctx context.Context, app *api.App, userID int64) (*rbac.Principal, error) {
	p := &rbac.Principal{UserID: userID}
	var active bool
	err := app.DB.QueryRow(ctx, "SELECT username, is_superuser, is_system_auditor, is_active FROM users WHERE id = $1", userID).
		Scan(&p.Username, &p.Superuser, &p.Auditor, &active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !active {
		return nil, nil
	}
	p.Teams, err = rbac.LoadTeams(ctx, app.DB, userID)
	return p, err
}

func (a *Auth) authenticate(r *http.Request, w http.ResponseWriter) (*rbac.Principal, error) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, cred, _ := strings.Cut(h, " ")
		switch strings.ToLower(scheme) {
		case "basic":
			user, pass, ok := r.BasicAuth()
			if !ok {
				return nil, api.Detail(401, "Invalid basic header.")
			}
			uid, err := a.checkPassword(ctx, user, pass)
			if err != nil {
				return nil, err
			}
			return LoadPrincipal(ctx, a.app, uid)
		case "bearer", "token":
			cred = strings.TrimSpace(cred)
			if strings.Count(cred, ".") == 2 && a.oidc.Enabled(ctx) {
				return a.oidc.AuthenticateJWT(ctx, cred)
			}
			var uid int64
			var scope string
			err := a.app.DB.QueryRow(ctx, `UPDATE auth_tokens SET last_used = now() WHERE token_hash = $1 AND expires > now()
				RETURNING user_id, scope`, hashToken(cred)).Scan(&uid, &scope)
			if err != nil {
				return nil, api.Detail(401, "Authentication credentials were not provided. To establish a login session, visit /api/login/.")
			}
			p, err := LoadPrincipal(ctx, a.app, uid)
			if p != nil {
				p.TokenScope = scope
			}
			return p, err
		}
	}
	if ck, err := r.Cookie(SessionCookie); err == nil && ck.Value != "" {
		var uid int64
		var csrf string
		var expires time.Time
		err := a.app.DB.QueryRow(ctx, "SELECT user_id, csrf, expires FROM sessions WHERE id = $1 AND expires > now()", hashToken(ck.Value)).
			Scan(&uid, &csrf, &expires)
		if err != nil {
			return nil, nil
		}
		switch r.Method {
		case "GET", "HEAD", "OPTIONS":
		default:
			got := r.Header.Get("X-CSRFToken")
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(csrf)) != 1 {
				return nil, api.Detail(403, "CSRF Failed: CSRF token missing or incorrect.")
			}
		}
		// Sliding expiration.
		if time.Until(expires) < sessionAge/2 {
			a.app.DB.Exec(ctx, "UPDATE sessions SET expires = $2 WHERE id = $1", hashToken(ck.Value), time.Now().Add(sessionAge))
			setCookie(w, r, SessionCookie, ck.Value, true, sessionAge)
		}
		return LoadPrincipal(ctx, a.app, uid)
	}
	return nil, nil
}

// verified caches successful password checks so Basic auth does not pay
// the bcrypt cost on every request. Keyed by the stored hash, so a password
// change invalidates entries.
var verified sync.Map // sha256(hash|password) -> expiry time.Time

const verifiedTTL = time.Minute

func (a *Auth) checkPassword(ctx context.Context, username, password string) (int64, error) {
	var uid int64
	var hash string
	var active bool
	var superuser bool
	err := a.app.DB.QueryRow(ctx, "SELECT id, password, is_active, is_superuser FROM users WHERE username = $1", username).Scan(&uid, &hash, &active, &superuser)
	if err != nil || !active || hash == "" {
		return 0, api.Detail(401, "Invalid username/password.")
	}
	// Superusers keep local login for bootstrap and break-glass access.
	if !superuser && a.app.Settings.Bool(ctx, "DISABLE_LOCAL_AUTH") {
		return 0, api.Detail(401, "Local authentication is disabled; sign in with single sign-on.")
	}
	key := hashToken(hash + "|" + password)
	if exp, ok := verified.Load(key); ok && time.Now().Before(exp.(time.Time)) {
		return uid, nil
	}
	if !CheckPassword(hash, password) {
		return 0, api.Detail(401, "Invalid username/password.")
	}
	verified.Store(key, time.Now().Add(verifiedTTL))
	return uid, nil
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, httpOnly bool, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: httpOnly,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(maxAge.Seconds()),
	})
}

// StartSession creates a session for the user and sets cookies.
func (a *Auth) StartSession(ctx context.Context, w http.ResponseWriter, r *http.Request, uid int64) error {
	sid := randomString(48)
	csrf := randomString(32)
	if _, err := a.app.DB.Exec(ctx, "INSERT INTO sessions (id, user_id, csrf, expires) VALUES ($1, $2, $3, $4)",
		hashToken(sid), uid, csrf, time.Now().Add(sessionAge)); err != nil {
		return err
	}
	a.app.DB.Exec(ctx, "UPDATE users SET last_login = now() WHERE id = $1", uid)
	setCookie(w, r, SessionCookie, sid, true, sessionAge)
	setCookie(w, r, CSRFCookie, csrf, false, 365*24*time.Hour)
	return nil
}

func (a *Auth) loginForm(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	csrf := randomString(32)
	if ck, err := r.Cookie(CSRFCookie); err == nil && ck.Value != "" {
		csrf = ck.Value
	}
	setCookie(w, r, CSRFCookie, csrf, false, 365*24*time.Hour)
	next := r.URL.Query().Get("next")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><title>Login</title></head><body>
<form method="post" action="/api/login/">
<input type="hidden" name="csrfmiddlewaretoken" value="%s">
<input type="hidden" name="next" value="%s">
<label>Username <input name="username"></label>
<label>Password <input name="password" type="password"></label>
<button type="submit">Log in</button>
</form></body></html>`, html.EscapeString(csrf), html.EscapeString(next))
}

func (a *Auth) login(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	var username, password, next string
	isForm := strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") ||
		strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data")
	if isForm {
		if err := r.ParseForm(); err != nil {
			api.WriteError(w, api.BadRequest("Invalid form."))
			return
		}
		// CSRF double-submit check when the client used the form flow.
		if ck, err := r.Cookie(CSRFCookie); err == nil {
			tok := r.PostForm.Get("csrfmiddlewaretoken")
			if tok == "" {
				tok = r.Header.Get("X-CSRFToken")
			}
			if tok != ck.Value {
				api.WriteError(w, api.Detail(403, "CSRF Failed: CSRF token missing or incorrect."))
				return
			}
		}
		username, password, next = r.PostForm.Get("username"), r.PostForm.Get("password"), r.PostForm.Get("next")
	} else {
		data, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		username, _ = data["username"].(string)
		password, _ = data["password"].(string)
	}
	uid, err := a.checkPassword(c, username, password)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if err := a.StartSession(c, w, r, uid); err != nil {
		api.WriteError(w, err)
		return
	}
	if isForm {
		if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
			next = "/api/"
		}
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	api.WriteJSON(w, 200, api.Obj{})
}

func (a *Auth) logout(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(SessionCookie); err == nil {
		a.app.DB.Exec(c, "DELETE FROM sessions WHERE id = $1", hashToken(ck.Value))
	}
	setCookie(w, r, SessionCookie, "", true, -1)
	if r.Method == "GET" {
		http.Redirect(w, r, "/api/", http.StatusFound)
		return
	}
	w.WriteHeader(204)
}

func (a *Auth) me(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	users := api.Lookup("users")
	api.WriteList(c, w, r, users, api.ListParams{SkipView: true, Extra: []func(api.ArgFunc) string{func(arg api.ArgFunc) string {
		return "t.id = " + arg(c.User.UserID)
	}}})
}

func (a *Auth) authBackends(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	out := api.Obj{}
	if a.oidc.Enabled(c) {
		out["oidc"] = api.Obj{"login_url": "/sso/login/oidc/", "complete_url": a.app.Cfg.BaseURL + "/sso/complete/oidc/"}
	}
	api.WriteJSON(w, 200, out)
}

var tokensOnce sync.Once

func registerTokens() {
	tokenVisible := func(c *api.Ctx, arg api.ArgFunc) string {
		if c.User.Superuser {
			return "TRUE"
		}
		return "t.user_id = " + arg(c.User.UserID)
	}
	res := api.Register(&api.Resource{
		Name:    "tokens",
		Type:    "o_auth2_access_token",
		Table:   "auth_tokens",
		Verbose: "Access Token",
		NoName:  true,
		Fields: []*api.Field{
			{Name: "description", Type: api.TText, Default: ""},
			{Name: "user", Type: api.TFK, Ref: "users", ReadOnly: true, Help: "The user representing the token owner"},
			{Name: "scope", Type: api.TChoice, Default: "write", Choices: []api.Choice{{Value: "read", Label: "Read"}, {Value: "write", Label: "Write"}},
				Help: "Allowed scopes, further restricts user's permissions. Must be a simple space-separated string with allowed scopes ['read', 'write']."},
			{Name: "expires", Type: api.TDateTime, ReadOnly: true},
			{Name: "token_hash", Type: api.TString, ReadOnly: true, Hidden: true, NoFilter: true},
			{Name: "last_used", Type: api.TDateTime, ReadOnly: true, Nullable: true, Hidden: true},
		},
		NoTimestamps: false,
		View:         tokenVisible,
		CanAdd: func(c *api.Ctx, data api.Obj) error {
			if c.User.Anonymous() {
				return api.ErrForbidden
			}
			if uid, ok := api.ID(data["user"]); ok && uid != c.User.UserID {
				return api.Detail(403, "Personal tokens can only be created for yourself.")
			}
			return nil
		},
		CanChange: func(c *api.Ctx, old, data api.Obj) error {
			if uid, _ := api.ID(old["user"]); uid != c.User.UserID && !c.User.Superuser {
				return api.ErrForbidden
			}
			return nil
		},
		CanDelete: func(c *api.Ctx, obj api.Obj) error {
			if uid, _ := api.ID(obj["user"]); uid != c.User.UserID && !c.User.Superuser {
				return api.ErrForbidden
			}
			return nil
		},
		BeforeSave: func(c *api.Ctx, old, data api.Obj) error {
			if old == nil {
				plain := randomString(40)
				c.Extra["new_token"] = plain
				data["token_hash"] = hashToken(plain)
				data["user"] = c.User.UserID
				data["expires"] = time.Now().AddDate(1000, 0, 0)
			}
			return nil
		},
		Serialize: func(c *api.Ctx, o api.Obj) {
			o["token"] = "************"
			if t, ok := c.Extra["new_token"].(string); ok {
				o["token"] = t
			}
			o["refresh_token"] = nil
			o["application"] = nil
		},
		Capabilities: func(c *api.Ctx, obj api.Obj, p map[string]bool) map[string]bool {
			uid, _ := api.ID(obj["user"])
			ok := uid == c.User.UserID || c.User.Superuser
			return map[string]bool{"edit": ok, "delete": ok}
		},
	})
	_ = res
	users := api.Lookup("users")
	for _, name := range []string{"tokens", "personal_tokens", "authorized_tokens"} {
		users.SubLists = append(users.SubLists, &api.SubList{
			Name:   name,
			Target: "tokens",
			Filter: func(arg api.ArgFunc, uid int64) string {
				return "t.user_id = " + arg(uid)
			},
			ParentField: "user",
		})
	}
}

// RequestURL returns the absolute URL for a path.
func RequestURL(base, path string, q url.Values) string {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}
