package credplugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// store is a fake secret store that records every request it receives.
type store struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newStore(t *testing.T, h http.HandlerFunc) *store {
	t.Helper()
	s := &store{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *store) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func (s *store) host() string {
	u, _ := url.Parse(s.URL)
	return u.Host
}

// refused checks that a lookup failed, naming the setting that would allow
// it, and that none of the stores received any request.
func refused(t *testing.T, err error, setting string, stores ...*store) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), setting) {
		t.Fatalf("expected a refusal naming %s, got %v", setting, err)
	}
	for _, s := range stores {
		if r := s.requests(); len(r) > 0 {
			t.Fatalf("a refused lookup sent requests: %v", r)
		}
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var loopback = []string{"127.0.0.1", "::1"}

// Kubernetes auth sends the pod's service account token: only to a Vault an
// administrator lists, and nothing at all reaches one that is not.
func TestVaultKubernetesAuthOnlyToListedHosts(t *testing.T) {
	KubernetesTokenPath = writeFile(t, "pod-sa-token\n")
	t.Cleanup(func() { KubernetesTokenPath = "" })
	vault := newStore(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/v1/auth/kubernetes/login" && body["jwt"] == "pod-sa-token" && body["role"] == "awx":
			json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "vt"}})
		case r.URL.Path == "/v1/kv/data/app" && r.Header.Get("X-Vault-Token") == "vt":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]any{"password": "from-k8s-auth"}}})
		default:
			w.WriteHeader(403)
		}
	})
	inputs := map[string]any{"url": vault.URL, "kubernetes_role": "awx", "api_version": "v2"}
	md := map[string]any{"secret_path": "kv/app", "secret_key": "password"}
	ctx := context.Background()

	for _, hosts := range [][]string{
		nil,                                    // off by default
		{"vault.example.com", "*.example.com"}, // other hosts
		{vault.host()},                         // without a scheme, https only
		{"http://" + vault.host() + "/x"},      // not a host: skipped
		{"http://127.0.0.1:1"},                 // another port
	} {
		_, err := Lookup(ctx, NewPolicy(hosts, loopback), "hashivault_kv", inputs, md)
		refused(t, err, IdentityHostsSetting, vault)
	}
	// The SSH signer logs in the same way.
	_, err := Lookup(ctx, NewPolicy(nil, loopback), "hashivault_ssh", inputs, map[string]any{"public_key": "k", "secret_path": "ssh", "role": "r"})
	refused(t, err, IdentityHostsSetting, vault)

	for _, entry := range []string{"http://" + vault.host(), "HTTP://" + vault.host() + "/", "http://127.0.0.1"} {
		v, err := Lookup(ctx, NewPolicy([]string{entry}, loopback), "hashivault_kv", inputs, md)
		if err != nil || v != "from-k8s-auth" {
			t.Fatalf("listed as %s: %q %v", entry, v, err)
		}
	}
	// A token of the user's own needs no listing.
	if _, err := Lookup(ctx, NewPolicy(nil, loopback), "hashivault_kv", map[string]any{"url": vault.URL, "token": "vt", "api_version": "v2"}, md); err != nil {
		t.Fatalf("token auth: %v", err)
	}
}

// A listed Vault that redirects elsewhere does not take the token with it.
func TestVaultKubernetesAuthNotRedirected(t *testing.T) {
	KubernetesTokenPath = writeFile(t, "pod-sa-token")
	t.Cleanup(func() { KubernetesTokenPath = "" })
	other := newStore(t, func(w http.ResponseWriter, r *http.Request) {})
	vault := newStore(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	_, err := Lookup(context.Background(), NewPolicy([]string{"http://" + vault.host()}, loopback), "hashivault_kv",
		map[string]any{"url": vault.URL, "kubernetes_role": "awx"}, map[string]any{"secret_path": "kv/app", "secret_key": "k"})
	refused(t, err, IdentityHostsSetting, other)
	if r := vault.requests(); len(r) != 1 {
		t.Fatalf("listed vault: %v", r)
	}
}

// Without a client secret, Azure Key Vault uses awxnext's workload identity:
// its federated token goes only to the cloud's authority or a listed one,
// and its access token only to a listed vault.
func TestAzureWorkloadIdentityOnlyToListedHosts(t *testing.T) {
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", writeFile(t, "federated-jwt"))
	authority := newStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/oauth2/v2.0/token" || r.PostForm.Get("client_assertion") != "federated-jwt" && r.PostForm.Get("client_secret") != "s" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 3600})
	})
	vault := newStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/secrets/db" || r.Header.Get("Authorization") != "Bearer at" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"value": "from-workload-identity"})
	})
	inputs := map[string]any{"url": vault.URL, "client": "c", "tenant": "tenant", "authority_host": authority.URL}
	md := map[string]any{"secret_field": "db"}
	ctx := context.Background()
	for _, hosts := range [][]string{nil, {"http://" + vault.host()}, {"http://" + authority.host()}, {"*.vault.azure.net"}} {
		_, err := Lookup(ctx, NewPolicy(hosts, loopback), "azure_kv", inputs, md)
		refused(t, err, IdentityHostsSetting, vault, authority)
	}
	v, err := Lookup(ctx, NewPolicy([]string{"http://" + vault.host(), "http://" + authority.host()}, loopback), "azure_kv", inputs, md)
	if err != nil || v != "from-workload-identity" {
		t.Fatalf("listed: %q %v", v, err)
	}
	// With a client secret of the user's own, nothing needs listing.
	inputs["secret"] = "s"
	if v, err := Lookup(ctx, NewPolicy(nil, loopback), "azure_kv", inputs, md); err != nil || v != "from-workload-identity" {
		t.Fatalf("client secret: %q %v", v, err)
	}
}

// Managed identity: awxnext asks its own metadata endpoint for a token,
// which the address guard leaves alone, and sends it only to a listed vault.
func TestAzureManagedIdentityOnlyToListedVaults(t *testing.T) {
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", "")
	imds := newStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata") != "true" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "mi", "expires_in": "3600"})
	})
	AzureIMDSEndpoint = imds.URL + "/metadata/identity/oauth2/token"
	t.Cleanup(func() { AzureIMDSEndpoint = "" })
	vault := newStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mi" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"value": "from-managed-identity"})
	})
	inputs := map[string]any{"url": vault.URL}
	md := map[string]any{"secret_field": "db"}
	// Only the vault's own address and port may be reached; the metadata
	// endpoint, on another port, is not in the list and still answers.
	only := []string{vault.host()}
	_, err := Lookup(context.Background(), NewPolicy(nil, only), "azure_kv", inputs, md)
	refused(t, err, IdentityHostsSetting, vault, imds)
	v, err := Lookup(context.Background(), NewPolicy([]string{"http://" + vault.host()}, only), "azure_kv", inputs, md)
	if err != nil || v != "from-managed-identity" {
		t.Fatalf("listed: %q %v", v, err)
	}
}

// Every lookup refuses loopback, link-local and metadata addresses unless
// an administrator allows them, before sending anything.
func TestLookupsRefuseLocalAddresses(t *testing.T) {
	srv := newStore(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"k": "v"}, "Content": "v"})
	})
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	ctx := context.Background()
	kv := func(p Policy, u string) error {
		_, err := Lookup(ctx, p, "hashivault_kv", map[string]any{"url": u, "token": "t"}, map[string]any{"secret_path": "kv/x", "secret_key": "k"})
		return err
	}
	for _, u := range []string{srv.URL, "http://localhost:" + port, "http://[::ffff:127.0.0.1]:" + port} {
		refused(t, kv(Policy{}, u), LocalAddressesSetting, srv)
	}
	if err := kv(Policy{}, "http://169.254.169.254"); err == nil || !strings.Contains(err.Error(), "169.254.169.254 is a loopback, link-local or cloud metadata address") {
		t.Fatalf("metadata address: %v", err)
	}
	// The other lookups too.
	_, err := Lookup(ctx, Policy{}, "aim", map[string]any{"url": srv.URL, "app_id": "a"}, map[string]any{"object_query": "q"})
	refused(t, err, LocalAddressesSetting, srv)
	AWSEndpoint = srv.URL
	t.Cleanup(func() { AWSEndpoint = "" })
	_, err = Lookup(ctx, Policy{}, "aws_secretsmanager_credential", map[string]any{"aws_access_key": "a", "aws_secret_key": "b"}, map[string]any{"region_name": "us-east-1", "secret_name": "s"})
	refused(t, err, LocalAddressesSetting, srv)

	// Allowed: by address, network, or address and port.
	for _, allow := range []string{"127.0.0.1", "127.0.0.0/8", "127.0.0.1:" + port} {
		if err := kv(NewPolicy(nil, []string{allow}), srv.URL); err != nil {
			t.Fatalf("allowed %s: %v", allow, err)
		}
	}
	refused(t, kv(NewPolicy(nil, []string{"127.0.0.1:1"}), srv.URL), LocalAddressesSetting)
}
