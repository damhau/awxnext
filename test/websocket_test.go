package test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func wsDial(t *testing.T, header http.Header) (*websocket.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/websocket/", &websocket.DialOptions{HTTPHeader: header})
	return conn, err
}

func basicHeader(user, pass string) http.Header {
	return http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))}}
}

func wsRead(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var m map[string]any
	if err := wsjson.Read(ctx, conn, &m); err != nil {
		t.Fatalf("websocket read: %v", err)
	}
	return m
}

func wsSend(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, v); err != nil {
		t.Fatal(err)
	}
}

func TestWebsocket(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	if _, err := wsDial(t, nil); err == nil {
		t.Fatal("anonymous websocket accepted")
	}
	conn, err := wsDial(t, basicHeader("admin", "admin"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if m := wsRead(t, conn, 5*time.Second); m["accept"] != true {
		t.Fatalf("hello: %v", m)
	}

	jt := f.template(t, "site.yml", nil)
	job := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), nil), 201).id()
	wsSend(t, conn, map[string]any{"groups": map[string]any{"jobs": []any{"status_changed"}, "job_events": []any{job}}})
	m := wsRead(t, conn, 5*time.Second)
	if fmt.Sprint(m["groups_current"]) != fmt.Sprintf("[job_events-%d jobs-status_changed]", job) {
		t.Fatalf("subscription: %v", m)
	}
	var events int
	var statuses []string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		m := wsRead(t, conn, 60*time.Second)
		switch m["group_name"] {
		case "job_events":
			if m["job"] != float64(job) || m["counter"] == nil {
				t.Fatalf("event message: %v", m)
			}
			events++
		case "jobs":
			if m["unified_job_id"] == float64(job) {
				statuses = append(statuses, m["status"].(string))
			}
		}
		if len(statuses) > 0 && statuses[len(statuses)-1] == "successful" {
			break
		}
	}
	if events == 0 || !strings.Contains(strings.Join(statuses, ","), "running") {
		t.Fatalf("events=%d statuses=%v", events, statuses)
	}

	// Users only join event groups of jobs they can see.
	user, _ := newUser(t, a)
	uconn, err := wsDial(t, basicHeader(user.user, user.pass))
	if err != nil {
		t.Fatal(err)
	}
	defer uconn.CloseNow()
	wsRead(t, uconn, 5*time.Second)
	wsSend(t, uconn, map[string]any{"groups": map[string]any{"job_events": []any{job}, "control": []any{"limit_reached_1"}}})
	errs := 0
	for {
		m := wsRead(t, uconn, 5*time.Second)
		if e, ok := m["error"].(string); ok && strings.Contains(e, "access denied") {
			errs++
			continue
		}
		if fmt.Sprint(m["groups_current"]) != "[]" || errs != 2 {
			t.Fatalf("unauthorized subscription: %v (errors %d)", m, errs)
		}
		break
	}

	// Session clients must echo the CSRF cookie as xrftoken.
	jar := &cookieJar{}
	hc := &http.Client{Jar: jar}
	res, err := hc.Post(srv.URL+"/api/login/", "application/json", strings.NewReader(`{"username":"admin","password":"admin"}`))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("login: %v", err)
	}
	res.Body.Close()
	cookies := http.Header{"Cookie": {"awx_sessionid=" + jar.get("awx_sessionid") + "; csrftoken=" + jar.get("csrftoken")}}
	sconn, err := wsDial(t, cookies)
	if err != nil {
		t.Fatal(err)
	}
	defer sconn.CloseNow()
	wsRead(t, sconn, 5*time.Second)
	wsSend(t, sconn, map[string]any{"groups": map[string]any{"jobs": []any{"status_changed"}}})
	if m := wsRead(t, sconn, 5*time.Second); m["error"] != "access denied to channel" {
		t.Fatalf("missing xrftoken accepted: %v", m)
	}
	wsSend(t, sconn, map[string]any{"groups": map[string]any{"jobs": []any{"status_changed"}}, "xrftoken": jar.get("csrftoken")})
	if m := wsRead(t, sconn, 5*time.Second); fmt.Sprint(m["groups_current"]) != "[jobs-status_changed]" {
		t.Fatalf("session subscription: %v", m)
	}
}
