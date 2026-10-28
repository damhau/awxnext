package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// login signs in through /api/login/ and returns the session cookie.
func login(t *testing.T, user, pass string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	res, err := http.Post(srv.URL+"/api/login/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Name == "awx_sessionid" {
			return c
		}
	}
	t.Fatalf("login as %s: %d, no session cookie", user, res.StatusCode)
	return nil
}

// meWith calls /api/v2/me/ with a session cookie and returns the status.
func meWith(t *testing.T, ck *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/v2/me/", nil)
	req.AddCookie(ck)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestSessionAndLoginSettings(t *testing.T) {
	a := adminClient(t)
	// A token client: basic auth is turned off below.
	tok := must(t, a.post("tokens/", map[string]any{"description": "settings test"}), 201).Body["token"].(string)
	adm := &client{t: t, token: tok}
	t.Cleanup(func() {
		adm.delete("settings/authentication/")
		adm.delete("settings/ui/")
	})
	set := func(category string, kv map[string]any) { must(t, adm.patch("settings/"+category+"/", kv), 200) }

	// SESSION_COOKIE_AGE: at least 60 s; a session lasts that long without activity.
	must(t, adm.patch("settings/authentication/", map[string]any{"SESSION_COOKIE_AGE": 30}), 400)
	set("authentication", map[string]any{"SESSION_COOKIE_AGE": 120})
	user, _ := newUser(t, a)
	ck := login(t, user.user, user.pass)
	if ck.MaxAge != 120 {
		t.Fatalf("cookie Max-Age %d, want 120", ck.MaxAge)
	}
	var age float64
	if err := app.DB.QueryRow(t.Context(), `SELECT extract(epoch FROM s.expires - s.created) FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE u.username = $1 ORDER BY s.created DESC LIMIT 1`, user.user).Scan(&age); err != nil || age < 115 || age > 125 {
		t.Fatalf("session lasts %v s (%v), want 120", age, err)
	}
	if meWith(t, ck) != 200 {
		t.Fatal("fresh session refused")
	}
	app.DB.Exec(t.Context(), `UPDATE sessions SET expires = now() - interval '1 second' WHERE user_id = (SELECT id FROM users WHERE username = $1)`, user.user)
	if meWith(t, ck) != 401 {
		t.Fatal("an idle session past its age still works")
	}

	// SESSIONS_PER_USER: logging in beyond the limit ends the oldest session.
	set("authentication", map[string]any{"SESSIONS_PER_USER": 2})
	s1, s2, s3 := login(t, user.user, user.pass), login(t, user.user, user.pass), login(t, user.user, user.pass)
	if meWith(t, s1) != 401 || meWith(t, s2) != 200 || meWith(t, s3) != 200 {
		t.Fatalf("sessions after a third login: %d %d %d, want 401 200 200", meWith(t, s1), meWith(t, s2), meWith(t, s3))
	}

	// AUTH_BASIC_ENABLED: off refuses basic auth; tokens and sessions still work.
	set("authentication", map[string]any{"AUTH_BASIC_ENABLED": false})
	if r := a.get("me/"); r.Status != 401 || !strings.Contains(r.Raw, "AUTH_BASIC_ENABLED") {
		t.Fatalf("basic auth while off: %d %s", r.Status, r.Raw)
	}
	must(t, adm.get("me/"), 200)
	if meWith(t, login(t, user.user, user.pass)) != 200 {
		t.Fatal("session login while basic auth is off")
	}
	set("authentication", map[string]any{"AUTH_BASIC_ENABLED": true})
	must(t, a.get("me/"), 200)

	// The login page settings are on the API root, readable before logging in.
	logo := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	must(t, adm.patch("settings/ui/", map[string]any{"CUSTOM_LOGO": "https://example.com/logo.png"}), 400)
	set("ui", map[string]any{"CUSTOM_LOGO": logo, "CUSTOM_LOGIN_INFO": "Ask the platform team for an account."})
	set("authentication", map[string]any{"LOGIN_REDIRECT_OVERRIDE": "https://sso.example.com/login"})
	anon := &client{t: t}
	root := must(t, anon.get("/api/"), 200)
	if root.Body["custom_logo"] != logo || root.Body["custom_login_info"] != "Ask the platform team for an account." ||
		root.Body["login_redirect_override"] != "https://sso.example.com/login" {
		t.Fatalf("API root: %s", root.Raw)
	}
}
