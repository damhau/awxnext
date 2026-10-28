// Package test contains API integration tests run against a real PostgreSQL.
//
// Set AWXNEXT_TEST_DATABASE_URL to a server the tests may create databases on;
// without it they start a throwaway PostgreSQL (see internal/pgtest). Tests are
// skipped when the configured server is unreachable.
package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/pgtest"
	"github.com/damhau/awxnext/internal/server"
	"github.com/jackc/pgx/v5"
)

var (
	srv        *httptest.Server
	app        *api.App
	jobManager *jobs.Manager
	dbSkip     string
	// dbServer is the URL of the server the test databases are created on.
	dbServer string
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	base, stopPG, err := pgtest.URL()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer stopPG()
	dbServer = base
	ctx := context.Background()
	name := fmt.Sprintf("awxnext_test_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		dbSkip = "postgres unavailable: " + err.Error()
		return m.Run()
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		panic(err)
	}
	cfg := config.Load()
	cfg.DatabaseURL = databaseURL(name)
	cfg.SecretKey = "test-secret"
	cfg.NodeName = "test-node"
	app, err = server.Setup(ctx, cfg)
	if err != nil {
		panic(err)
	}
	hash, _ := auth.HashPassword("admin")
	if _, err := app.DB.Exec(ctx, "INSERT INTO users (username, password, is_superuser) VALUES ('admin', $1, true)", hash); err != nil {
		panic(err)
	}
	cfg.ProjectsRoot, _ = os.MkdirTemp("", "awxnext-projects-")
	cfg.JobsRoot, _ = os.MkdirTemp("", "awxnext-jobs-")
	cfg.ContainerRuntime = "none"
	if _, err := os.Stat("/opt/ansible-venv/bin/ansible-runner"); err == nil {
		os.Setenv("PATH", "/opt/ansible-venv/bin:"+os.Getenv("PATH"))
	}
	jobsCtx, stopJobs := context.WithCancel(ctx)
	jobManager = server.StartJobs(jobsCtx, app)
	srv = httptest.NewServer(server.Handler(app))
	cfg.ServiceURL, cfg.InternalURL = srv.URL, srv.URL
	code := m.Run()
	stopJobs()
	os.RemoveAll(cfg.ProjectsRoot)
	os.RemoveAll(cfg.JobsRoot)
	srv.Close()
	app.DB.Close()
	admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
	admin.Close(ctx)
	return code
}

// databaseURL is the URL of database name on the test server.
func databaseURL(name string) string {
	u, err := url.Parse(dbServer)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// client performs authenticated API calls.
type client struct {
	t     *testing.T
	user  string
	pass  string
	token string
}

func setup(t *testing.T) {
	t.Helper()
	if dbSkip != "" {
		t.Skip(dbSkip)
	}
}

func adminClient(t *testing.T) *client {
	setup(t)
	return &client{t: t, user: "admin", pass: "admin"}
}

type resp struct {
	Status int
	Body   map[string]any
	Raw    string
	Header http.Header
}

func (r resp) results() []map[string]any {
	var out []map[string]any
	for _, x := range r.Body["results"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func (r resp) id() int64 {
	n, _ := api.ID(r.Body["id"])
	return n
}

func (r resp) count() int {
	n, _ := api.ID(r.Body["count"])
	return int(n)
}

func (c *client) do(method, path string, body any) resp {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/api/v2/" + path
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	r := resp{Status: res.StatusCode, Raw: string(raw), Header: res.Header}
	if len(raw) > 0 && raw[0] == '{' {
		json.Unmarshal(raw, &r.Body)
	}
	return r
}

func (c *client) get(path string) resp          { c.t.Helper(); return c.do("GET", path, nil) }
func (c *client) post(path string, b any) resp  { c.t.Helper(); return c.do("POST", path, b) }
func (c *client) patch(path string, b any) resp { c.t.Helper(); return c.do("PATCH", path, b) }
func (c *client) delete(path string) resp       { c.t.Helper(); return c.do("DELETE", path, nil) }

// must asserts the status and returns the response.
func must(t *testing.T, r resp, status int) resp {
	t.Helper()
	if r.Status != status {
		t.Fatalf("expected %d, got %d: %s", status, r.Status, r.Raw)
	}
	return r
}

var seq int

// uniq returns a unique name for test objects.
func uniq(prefix string) string {
	seq++
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano()%100000, seq)
}

// newUser creates a non-superuser and returns a client for it.
func newUser(t *testing.T, a *client) (*client, int64) {
	t.Helper()
	name := uniq("user")
	r := must(t, a.post("users/", map[string]any{"username": name, "password": "pw"}), 201)
	return &client{t: t, user: name, pass: "pw"}, r.id()
}

func newOrg(t *testing.T, a *client) int64 {
	t.Helper()
	return must(t, a.post("organizations/", map[string]any{"name": uniq("org")}), 201).id()
}

// cookieJar is a minimal jar that exposes cookie values by name.
type cookieJar struct {
	cookies map[string]*http.Cookie
}

func (j *cookieJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	if j.cookies == nil {
		j.cookies = map[string]*http.Cookie{}
	}
	for _, c := range cs {
		j.cookies[c.Name] = c
	}
}

func (j *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range j.cookies {
		if c.MaxAge >= 0 {
			out = append(out, c)
		}
	}
	return out
}

func (j *cookieJar) get(name string) string {
	if c, ok := j.cookies[name]; ok {
		return c.Value
	}
	return ""
}
