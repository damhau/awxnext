package galaxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestClient(t *testing.T) {
	artifact := []byte("artifact bytes")
	sum := sha256.Sum256(artifact)
	var mu sync.Mutex
	auth := map[string]string{}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth["other"] = r.Header.Get("Authorization")
		mu.Unlock()
		w.Write(artifact)
	}))
	defer other.Close()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		reply := func(v any) { json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/galaxy/api/":
			reply(map[string]any{"available_versions": map[string]any{"v3": "v3/"}})
		case "/galaxy/api/v3/collections/ns/one/versions/":
			if r.URL.Query().Get("offset") == "" {
				reply(map[string]any{"links": map[string]any{"next": "/galaxy/api/v3/collections/ns/one/versions/?offset=1"}, "data": []any{map[string]any{"version": "1.0.0"}}})
			} else {
				reply(map[string]any{"links": map[string]any{"next": nil}, "data": []any{map[string]any{"version": "2.0.0"}}})
			}
		case "/galaxy/api/v3/collections/ns/loop/versions/":
			reply(map[string]any{"links": map[string]any{"next": r.URL.RequestURI()}, "data": []any{}})
		case "/galaxy/api/v3/collections/ns/away/versions/":
			reply(map[string]any{"links": map[string]any{"next": other.URL + "/next"}, "data": []any{}})
		case "/galaxy/api/v3/collections/ns/one/versions/1.0.0/":
			reply(map[string]any{"namespace": map[string]any{"name": "ns"}, "collection": map[string]any{"name": "one"}, "version": "1.0.0",
				"download_url": "/download/ns-one-1.0.0.tar.gz", "artifact": map[string]any{"sha256": hex.EncodeToString(sum[:])}})
		case "/galaxy/api/v3/collections/ns/one/versions/9.9.9/":
			reply(map[string]any{"namespace": map[string]any{"name": "ns"}, "collection": map[string]any{"name": "one"}, "version": "1.0.0"})
		case "/download/ns-one-1.0.0.tar.gz":
			w.Write(artifact)
		default:
			w.WriteHeader(404)
			reply(map[string]any{"errors": []any{map[string]any{"detail": "Not found."}}})
		}
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), URL: srv.URL + "/galaxy", Token: "secret"}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.Root() != srv.URL+"/galaxy/api/" {
		t.Fatalf("root %s", c.Root())
	}
	if v, err := c.Versions(t.Context(), "ns", "one"); err != nil || strings.Join(v, ",") != "1.0.0,2.0.0" {
		t.Fatalf("versions %v %v", v, err)
	}
	if _, err := c.Versions(t.Context(), "ns", "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing collection: %v", err)
	}
	if _, err := c.Versions(t.Context(), "ns", "loop"); err == nil || !strings.Contains(err.Error(), "doesn't end") {
		t.Errorf("pages in a loop: %v", err)
	}
	if _, err := c.Versions(t.Context(), "ns", "away"); err == nil || !strings.Contains(err.Error(), "leaves the server") {
		t.Errorf("a next page on another server: %v", err)
	}
	if _, err := c.Version(t.Context(), "ns", "one", "9.9.9"); err == nil || !strings.Contains(err.Error(), "answered with ns.one 1.0.0") {
		t.Errorf("another version's detail: %v", err)
	}
	v, err := c.Version(t.Context(), "ns", "one", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if data, err := c.Download(t.Context(), v, 1024); err != nil || string(data) != string(artifact) {
		t.Fatalf("download: %q %v", data, err)
	}
	if _, err := c.Download(t.Context(), v, 4); err == nil || !strings.Contains(err.Error(), "larger than 4 bytes") {
		t.Errorf("an artifact over the limit: %v", err)
	}
	v.Artifact.SHA256 = strings.Repeat("0", 64)
	if _, err := c.Download(t.Context(), v, 1024); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("an artifact of another checksum: %v", err)
	}
	v.Artifact.SHA256, v.DownloadURL = hex.EncodeToString(sum[:]), other.URL+"/ns-one-1.0.0.tar.gz"
	if _, err := c.Download(t.Context(), v, 1024); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if auth["/galaxy/api/"] != "Token secret" || auth["/download/ns-one-1.0.0.tar.gz"] != "Token secret" || auth["other"] != "" {
		t.Errorf("authorization sent: %v", auth)
	}
}

func TestClientSSO(t *testing.T) {
	var mu sync.Mutex
	issued, uses := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/token" {
			r.ParseForm()
			if r.Form.Get("refresh_token") != "offline" || r.Form.Get("client_id") != "cloud-services" || r.Form.Get("grant_type") != "refresh_token" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			issued, uses = issued+1, 0
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access" + string(rune('0'+issued))})
			return
		}
		uses++
		if r.Header.Get("Authorization") != "Bearer access"+string(rune('0'+issued)) || uses > 1 {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"available_versions": map[string]any{"v3": "v3/"}, "data": []any{}})
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), URL: srv.URL + "/api/", AuthURL: srv.URL + "/token", Token: "offline"}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Each access token serves one request here: the client gets another.
	if _, err := c.Versions(t.Context(), "ns", "one"); err != nil || issued != 2 {
		t.Fatalf("after expiry: %v, %d tokens", err, issued)
	}
	bad := &Client{HTTP: srv.Client(), URL: srv.URL + "/api/", AuthURL: srv.URL + "/token", Token: "wrong"}
	if err := bad.Connect(t.Context()); err == nil || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "wrong") {
		t.Errorf("a refused offline token: %v", err)
	}
}
