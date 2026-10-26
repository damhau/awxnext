package kms

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damhau/awxnext/internal/azureclient"
	"github.com/damhau/awxnext/internal/vaultclient"
)

// fakeVault implements transit encrypt/decrypt and AppRole login. Tokens
// expire after expireAfter requests to exercise re-login.
func fakeVault(t *testing.T, expireAfter int32) (*httptest.Server, *int32) {
	var logins, uses int32
	tok := func() string { return "tok-" + string(rune('a'+atomic.LoadInt32(&logins))) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/v1/auth/approle/login":
			if body["role_id"] != "rid" || body["secret_id"] != "sid" {
				w.WriteHeader(400)
				return
			}
			atomic.AddInt32(&logins, 1)
			atomic.StoreInt32(&uses, 0)
			json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": tok()}})
			return
		case r.Header.Get("X-Vault-Token") != tok() || atomic.AddInt32(&uses, 1) > expireAfter:
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"errors": []string{"permission denied"}})
			return
		case r.URL.Path == "/v1/transit/encrypt/awx":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ciphertext": "vault:v1:" + body["plaintext"].(string)}})
		case r.URL.Path == "/v1/transit/decrypt/awx":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"plaintext": strings.TrimPrefix(body["ciphertext"].(string), "vault:v1:")}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &logins
}

func TestVaultTransitRoundTripAndRelogin(t *testing.T) {
	srv, logins := fakeVault(t, 2)
	c, err := vaultclient.New(vaultclient.Config{Addr: srv.URL, RoleID: "rid", SecretID: "sid"})
	if err != nil {
		t.Fatal(err)
	}
	k := NewVaultTransit(c, srv.URL, "transit", "awx")
	dek := []byte("0123456789abcdef0123456789abcdef")
	for i := 0; i < 3; i++ {
		w, err := k.Wrap(context.Background(), dek)
		if err != nil {
			t.Fatal(err)
		}
		got, err := k.Unwrap(context.Background(), w)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, dek) {
			t.Fatalf("round trip mismatch")
		}
	}
	if atomic.LoadInt32(logins) < 2 {
		t.Fatalf("expected re-login after token expiry, logins=%d", *logins)
	}
	if !strings.HasPrefix(k.ID(), "vault-transit:127.0.0.1") {
		t.Fatalf("id: %s", k.ID())
	}
}

func TestAzureKeyVaultRoundTrip(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			r.ParseForm()
			if r.PostForm.Get("client_secret") != "cs" || r.PostForm.Get("scope") != "https://vault.azure.net/.default" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 3600})
		case r.Header.Get("Authorization") != "Bearer at":
			w.WriteHeader(401)
		case r.URL.Path == "/keys/kek/wrapkey":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			json.NewEncoder(w).Encode(map[string]any{"kid": srv.URL + "/keys/kek/v1", "value": "W" + body["value"]})
		case r.URL.Path == "/keys/kek/v1/unwrapkey":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			json.NewEncoder(w).Encode(map[string]any{"kid": srv.URL + "/keys/kek/v1", "value": strings.TrimPrefix(body["value"], "W")})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := azureclient.New(azureclient.Config{TenantID: "t", ClientID: "c", ClientSecret: "cs", AuthorityHost: srv.URL})
	k := NewAzureKeyVault(c, srv.URL, "kek")
	dek := []byte("0123456789abcdef0123456789abcdef")
	w, err := k.Wrap(context.Background(), dek)
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.Unwrap(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("round trip mismatch")
	}
	// A tampered key reference must not receive our token.
	if _, err := k.Unwrap(context.Background(), "https://evil.example/keys/kek/v1|x"); err == nil {
		t.Fatal("expected rejection of foreign key id")
	}
}
