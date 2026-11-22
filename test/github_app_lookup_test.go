package test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// fakeGitHubApp answers POST /app/installations/N/access_tokens as GitHub does,
// under prefix (GitHub Enterprise Server's /api/v3), and remembers the
// tokens it issued.
type fakeGitHubApp struct {
	*httptest.Server
	mu     sync.Mutex
	issued []string
	used   map[string]bool
}

func newFakeGitHubApp(t *testing.T, pub *rsa.PublicKey, prefix string, issuers []string, installID string) *fakeGitHubApp {
	g := &fakeGitHubApp{used: map[string]bool{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := strings.CutPrefix(r.URL.Path, prefix+"/app/installations/")
		id, ok2 := strings.CutSuffix(id, "/access_tokens")
		if r.Method != "POST" || !ok || !ok2 {
			jsonReply(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			jsonReply(w, 415, map[string]any{"message": "Unsupported Media Type"})
			return
		}
		raw, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var c jwt.Claims
		tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
		if err != nil || tok.Claims(pub, &c) != nil {
			jsonReply(w, 401, map[string]any{"message": "A JSON web token could not be decoded"})
			return
		}
		now := time.Now()
		if c.IssuedAt == nil || c.Expiry == nil || c.IssuedAt.Time().After(now) || !c.Expiry.Time().After(now) || c.Expiry.Time().Sub(c.IssuedAt.Time()) > 10*time.Minute {
			jsonReply(w, 401, map[string]any{"message": "'Expiration time' claim ('exp') is too far in the future"})
			return
		}
		known := false
		for _, iss := range issuers {
			known = known || c.Issuer == iss
		}
		if !known {
			jsonReply(w, 401, map[string]any{"message": "Integration not found"})
			return
		}
		if id != installID {
			jsonReply(w, 404, map[string]any{"message": "Not Found", "documentation_url": "https://docs.github.com/rest/apps/apps#create-an-installation-access-token-for-an-app"})
			return
		}
		g.mu.Lock()
		token := fmt.Sprintf("ghs_fake%04d", len(g.issued)+1)
		g.issued = append(g.issued, token)
		g.mu.Unlock()
		jsonReply(w, 201, map[string]any{"token": token, "expires_at": now.Add(time.Hour).UTC().Format(time.RFC3339), "repository_selection": "selected"})
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGitHubApp) valid(token string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, x := range g.issued {
		if x == token {
			g.used[token] = true
			return true
		}
	}
	return false
}

func (g *fakeGitHubApp) usedCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.used)
}

// tokenGitServer serves bare repositories over smart HTTP with
// git http-backend, to clients that sign in as x-access-token with a token
// the fake GitHub issued.
func tokenGitServer(t *testing.T, g *fakeGitHubApp, repos map[string]string) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	for name, src := range repos {
		if out, err := exec.Command("git", "clone", "-q", "--bare", src, filepath.Join(root, name)).CombinedOutput(); err != nil {
			t.Fatalf("git clone --bare: %v\n%s", err, out)
		}
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: gitBin, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "x-access-token" || !g.valid(p) {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withInputs(m map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func rsaPEM(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestGitHubAppLookup(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	key, keyPEM := rsaPEM(t)
	const appID, clientID, installID = "1121547", "Iv23likIfIXeZTb5GCAA", "59980338"
	gh := newFakeGitHubApp(t, &key.PublicKey, "/api/v3", []string{appID, clientID}, installID)
	in := map[string]any{"github_api_url": gh.URL + "/api/v3/", "app_or_client_id": appID, "install_id": installID, "private_rsa_key": keyPEM}

	ct := must(t, a.get(fmt.Sprintf("credential_types/%d/", credType(t, a, "github_app_lookup"))), 200)
	if ct.Body["name"] != "GitHub App Installation Access Token Lookup" || ct.Body["kind"] != "external" {
		t.Fatalf("credential type: %s", ct.Raw)
	}

	typeTest(t, a, "github_app_lookup", in, nil, 202)
	first, err := lookupValue(t, a, org, "github_app_lookup", in, map[string]any{"description": "clone the playbooks"})
	if err != nil || !strings.HasPrefix(first, "ghs_fake") {
		t.Fatalf("token: %q %v", first, err)
	}
	second, err := lookupValue(t, a, org, "github_app_lookup", withInputs(in, "app_or_client_id", clientID), map[string]any{})
	if err != nil || !strings.HasPrefix(second, "ghs_fake") || second == first {
		t.Fatalf("a client ID gets its own token: %q then %q %v", first, second, err)
	}
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	typeTest(t, a, "github_app_lookup", withInputs(in, "private_rsa_key", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))), nil, 202)

	_, otherPEM := rsaPEM(t)
	for _, c := range []struct {
		name   string
		inputs map[string]any
		want   string
	}{
		{"not installed", withInputs(in, "install_id", "1"), "is the app installed?"},
		{"another app's key", withInputs(in, "private_rsa_key", otherPEM), "A JSON web token could not be decoded"},
		{"unknown app", withInputs(in, "app_or_client_id", "999"), "Integration not found"},
		{"installation ID", withInputs(in, "install_id", "my-org"), "must be a number"},
		{"not a key", withInputs(in, "private_rsa_key", "-----BEGIN RSA PRIVATE KEY-----\nbm9wZQ==\n-----END RSA PRIVATE KEY-----\n"), "the PEM file GitHub generated"},
		{"not a URL", withInputs(in, "github_api_url", "gh.example.com"), "must be an http(s) URL"},
	} {
		r := typeTest(t, a, "github_app_lookup", c.inputs, nil, 400)
		if !strings.Contains(r.Raw, c.want) || strings.Contains(r.Raw, "PRIVATE KEY") || strings.Contains(r.Raw, "eyJ") || strings.Contains(r.Raw, "ghs_") {
			t.Errorf("%s: %s", c.name, r.Raw)
		}
	}

	setJobs(t, a, "CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES", []string{})
	if r := typeTest(t, a, "github_app_lookup", in, nil, 400); !strings.Contains(r.Raw, "CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES") {
		t.Errorf("a loopback GitHub without listing it: %s", r.Raw)
	}
	must(t, a.patch("settings/jobs/", map[string]any{"CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES": []string{"127.0.0.1", "::1"}}), 200)

	src := must(t, a.post("credentials/", map[string]any{"name": uniq("gh-app"), "organization": org, "credential_type": credType(t, a, "github_app_lookup"), "inputs": in}), 201).id()
	must(t, a.post(fmt.Sprintf("credentials/%d/test/", src), map[string]any{"metadata": map[string]any{}}), 202)
	scm := must(t, a.post("credentials/", map[string]any{"name": uniq("scm"), "organization": org, "credential_type": credType(t, a, "scm"),
		"inputs": map[string]any{"username": "x-access-token"}}), 201).id()
	must(t, a.post(fmt.Sprintf("credentials/%d/input_sources/", scm), map[string]any{"source_credential": src, "input_field_name": "password", "metadata": map[string]any{}}), 201)

	git := tokenGitServer(t, gh, map[string]string{"playbooks.git": gitRepo(t, map[string]string{"site.yml": helloPlaybook})})
	if out, err := exec.Command("git", "ls-remote", git.URL+"/playbooks.git").CombinedOutput(); err == nil {
		t.Fatalf("the git server lets in a client without a token:\n%s", out)
	}
	before := gh.usedCount()
	p := must(t, a.post("projects/", map[string]any{"name": uniq("private"), "organization": org, "scm_type": "git", "scm_url": git.URL + "/playbooks.git", "credential": scm}), 201)
	for i := 1; i <= 2; i++ {
		ups := must(t, a.get(fmt.Sprintf("projects/%d/project_updates/?order_by=-id", p.id())), 200)
		pu := waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(ups.results()[0]["id"].(float64))))
		out := a.get(fmt.Sprintf("project_updates/%d/stdout/?format=txt", pu.id()))
		if pu.Body["status"] != "successful" {
			t.Fatalf("update %d: %v\n%s", i, pu.Body["job_explanation"], out.Raw)
		}
		if strings.Contains(out.Raw, "ghs_fake") {
			t.Errorf("update %d shows the token:\n%s", i, out.Raw)
		}
		if got := gh.usedCount() - before; got != i {
			t.Fatalf("after update %d, the git server saw %d tokens, want a fresh one per update", i, got)
		}
		if i == 1 {
			must(t, a.post(fmt.Sprintf("projects/%d/update/", p.id()), nil), 202)
		}
	}
	if pl := must(t, a.get(fmt.Sprintf("projects/%d/playbooks/", p.id())), 200); !strings.Contains(pl.Raw, "site.yml") {
		t.Fatalf("playbooks: %s", pl.Raw)
	}
}
