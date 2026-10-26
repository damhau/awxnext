package credplugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAzureKeyVaultLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 3600})
		case r.URL.Path == "/secrets/db-password/v7" && r.Header.Get("Authorization") == "Bearer at":
			json.NewEncoder(w).Encode(map[string]any{"value": "from-azure"})
		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "SecretNotFound", "message": "not found"}})
		}
	}))
	defer srv.Close()
	inputs := map[string]any{"url": srv.URL, "client": "c", "secret": "s", "tenant": "t", "authority_host": srv.URL}
	v, err := Lookup(context.Background(), "azure_kv", inputs, map[string]any{"secret_field": "db-password", "secret_version": "v7"})
	if err != nil || v != "from-azure" {
		t.Fatalf("lookup: %q %v", v, err)
	}
	if _, err := Lookup(context.Background(), "azure_kv", inputs, map[string]any{"secret_field": "nope"}); err == nil || !strings.Contains(err.Error(), "SecretNotFound") {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestVaultSSHSign(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/ssh-client/sign/deployers" || body["public_key"] != "ssh-ed25519 AAAA" || body["valid_principals"] != "deploy" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"signed_key": "ssh-ed25519-cert-v01@openssh.com AAAA..."}})
	}))
	defer srv.Close()
	v, err := Lookup(context.Background(), "hashivault_ssh", map[string]any{"url": srv.URL, "token": "t"},
		map[string]any{"public_key": "ssh-ed25519 AAAA", "secret_path": "/ssh-client/", "role": "deployers", "valid_principals": "deploy"})
	if err != nil || !strings.HasPrefix(v, "ssh-ed25519-cert") {
		t.Fatalf("sign: %q %v", v, err)
	}
}

func TestVaultKVPathSplitting(t *testing.T) {
	b, p, err := splitBackend(map[string]any{"secret_path": "/kv/apps/checkout/"})
	if err != nil || b != "kv" || p != "apps/checkout" {
		t.Fatalf("split: %q %q %v", b, p, err)
	}
	b, p, _ = splitBackend(map[string]any{"secret_backend": "secret", "secret_path": "apps/x"})
	if b != "secret" || p != "apps/x" {
		t.Fatalf("explicit backend: %q %q", b, p)
	}
	if _, err := Lookup(context.Background(), "hashivault_kv", map[string]any{"url": "http://x"}, map[string]any{"secret_path": "kv/a", "secret_key": "k"}); err == nil {
		t.Fatal("expected auth configuration error")
	}
}
