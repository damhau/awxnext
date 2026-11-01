package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/damhau/awxnext/internal/credplugins"
)

// k8sVault is a fake HashiCorp Vault with Kubernetes auth that records every
// request it receives, and the service account tokens it is sent.
type k8sVault struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
	jwts []string
}

func startK8sVault(t *testing.T) *k8sVault {
	t.Helper()
	v := &k8sVault{}
	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		v.mu.Lock()
		v.seen = append(v.seen, r.Method+" "+r.URL.Path)
		if jwt, ok := body["jwt"].(string); ok {
			v.jwts = append(v.jwts, jwt)
		}
		v.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/auth/kubernetes/login" && body["role"] == "awx":
			jsonReply(w, 200, map[string]any{"auth": map[string]any{"client_token": "k8s-vault-token"}})
		case r.URL.Path == "/v1/kv/data/apps/checkout" && r.Header.Get("X-Vault-Token") == "k8s-vault-token":
			jsonReply(w, 200, map[string]any{"data": map[string]any{"data": map[string]any{"db_password": "from-k8s-auth"}}})
		default:
			jsonReply(w, 403, map[string]any{"errors": []string{"permission denied"}})
		}
	}))
	t.Cleanup(v.Close)
	return v
}

// requests returns what the fake received, and forgets it.
func (v *k8sVault) requests() ([]string, []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	seen, jwts := v.seen, v.jwts
	v.seen, v.jwts = nil, nil
	return seen, jwts
}

// setJobs sets a jobs setting for the test, and puts the value back after.
func setJobs(t *testing.T, a *client, key string, value any) {
	t.Helper()
	old := must(t, a.get("settings/jobs/"), 200).Body[key]
	must(t, a.patch("settings/jobs/", map[string]any{key: value}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{key: old}) })
}

// A lookup with Kubernetes auth sends awxnext's own service account token
// only to a Vault an administrator lists: anywhere else, through the test
// button or when a job resolves its credentials, nothing is sent at all.
func TestCredentialLookupServerIdentity(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	vault := startK8sVault(t)
	credplugins.KubernetesTokenPath = filepath.Join(t.TempDir(), "token")
	os.WriteFile(credplugins.KubernetesTokenPath, []byte("pod-service-account-token\n"), 0o600)
	t.Cleanup(func() { credplugins.KubernetesTokenPath = "" })

	kv := credType(t, a, "hashivault_kv")
	inputs := map[string]any{"url": vault.URL, "kubernetes_role": "awx", "api_version": "v2"}
	md := map[string]any{"secret_path": "kv/apps/checkout", "secret_key": "db_password"}
	typeTest := func(status int) resp {
		t.Helper()
		return must(t, a.post(fmt.Sprintf("credential_types/%d/test/", kv), map[string]any{"inputs": inputs, "metadata": md}), status)
	}
	lookup := must(t, a.post("credentials/", map[string]any{"name": uniq("vault k8s"), "organization": org, "credential_type": kv, "inputs": inputs}), 201).id()
	target := must(t, a.post("credentials/", map[string]any{"name": uniq("machine"), "organization": org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "svc"}}), 201).id()
	must(t, a.post(fmt.Sprintf("credentials/%d/input_sources/", target), map[string]any{"source_credential": lookup, "input_field_name": "password", "metadata": md}), 201)
	nothingSent := func(what string) {
		t.Helper()
		if seen, _ := vault.requests(); len(seen) > 0 {
			t.Fatalf("%s: the refused lookup sent %v", what, seen)
		}
	}

	// Off by default.
	if r := typeTest(400); !strings.Contains(r.Body["inputs"].(string), "CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS") {
		t.Fatalf("type test: %s", r.Raw)
	}
	nothingSent("type test")
	must(t, a.post(fmt.Sprintf("credentials/%d/test/", lookup), map[string]any{"metadata": md}), 400)
	nothingSent("credential test")
	if _, err := resolveCredential(target); err == nil || !strings.Contains(err.Error(), "service account token") {
		t.Fatalf("at launch: %v", err)
	}
	nothingSent("at launch")

	// Other hosts listed, or this one for https only: still refused.
	host := strings.TrimPrefix(vault.URL, "http://")
	setJobs(t, a, "CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS", []string{"vault.example.com", "*.vault.azure.net", host})
	typeTest(400)
	nothingSent("other hosts")
	r := must(t, a.patch("settings/jobs/", map[string]any{"CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS": []string{"vault.example.com/v1", "ftp://vault"}}), 400)
	if !strings.Contains(r.Raw, `\"vault.example.com/v1\" is not a host`) {
		t.Fatalf("invalid entry: %s", r.Raw)
	}

	// Listed: the token goes to it, from the test button and at launch.
	must(t, a.patch("settings/jobs/", map[string]any{"CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS": []string{"http://" + host}}), 200)
	typeTest(202)
	if rc, err := resolveCredential(target); err != nil || rc["password"] != "from-k8s-auth" {
		t.Fatalf("at launch: %v %v", rc, err)
	}
	if seen, jwts := vault.requests(); len(seen) != 4 || strings.Join(jwts, ",") != "pod-service-account-token,pod-service-account-token" {
		t.Fatalf("listed: %v %v", seen, jwts)
	}
}

// Lookups refuse loopback, link-local and cloud metadata addresses unless an
// administrator allows them (the other tests' fakes listen on loopback,
// which the harness allows).
func TestCredentialLookupLocalAddresses(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	var hits int
	var mu sync.Mutex
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return hits
	}
	vsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		jsonReply(w, 200, map[string]any{"data": map[string]any{"data": map[string]any{"db_password": "local"}}})
	}))
	t.Cleanup(vsrv.Close)
	inputs := map[string]any{"url": vsrv.URL, "token": "root", "api_version": "v2"}
	md := map[string]any{"secret_path": "kv/apps/checkout", "secret_key": "db_password"}
	lookup := must(t, a.post("credentials/", map[string]any{"name": uniq("vault"), "organization": org, "credential_type": credType(t, a, "hashivault_kv"), "inputs": inputs}), 201).id()
	target := must(t, a.post("credentials/", map[string]any{"name": uniq("machine"), "organization": org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "svc"}}), 201).id()
	must(t, a.post(fmt.Sprintf("credentials/%d/input_sources/", target), map[string]any{"source_credential": lookup, "input_field_name": "password", "metadata": md}), 201)

	setJobs(t, a, "CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES", []string{})
	r := typeTest(t, a, "hashivault_kv", inputs, md, 400)
	if !strings.Contains(r.Raw, "is a loopback, link-local or cloud metadata address, which is refused unless an administrator allows it in CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES") {
		t.Fatalf("type test: %s", r.Raw)
	}
	typeTest(t, a, "hashivault_kv", map[string]any{"url": "http://169.254.169.254", "token": "root"}, md, 400)
	typeTest(t, a, "aim", map[string]any{"url": vsrv.URL, "app_id": "a"}, map[string]any{"object_query": "q"}, 400)
	if _, err := resolveCredential(target); err == nil || !strings.Contains(err.Error(), "CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES") {
		t.Fatalf("at launch: %v", err)
	}
	if n := count(); n != 0 {
		t.Fatalf("refused lookups reached the server %d times", n)
	}
	must(t, a.patch("settings/jobs/", map[string]any{"CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES": []string{"localhost"}}), 400)

	// Allowed by address and port.
	must(t, a.patch("settings/jobs/", map[string]any{"CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES": []string{strings.TrimPrefix(vsrv.URL, "http://")}}), 200)
	typeTest(t, a, "hashivault_kv", inputs, md, 202)
	if rc, err := resolveCredential(target); err != nil || rc["password"] != "local" || count() != 2 {
		t.Fatalf("allowed: %v %v %d", rc, err, count())
	}
}

// Testing a credential type runs a lookup with any inputs: as in AWX, only
// superusers may. Others test a credential they may use.
func TestCredentialTypeTestForSuperusers(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	vsrv := fakeVaultKV(t)
	orgAdmin, orgAdminID := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/admins/", org), map[string]any{"id": orgAdminID}), 204)
	plain, _ := newUser(t, a)
	kv := credType(t, a, "hashivault_kv")
	body := map[string]any{"inputs": map[string]any{"url": vsrv.URL, "token": "root", "api_version": "v2"},
		"metadata": map[string]any{"secret_path": "kv/apps/checkout", "secret_key": "db_password"}}

	for _, u := range []*client{orgAdmin, plain} {
		must(t, u.post(fmt.Sprintf("credential_types/%d/test/", kv), body), 403)
	}
	must(t, a.post(fmt.Sprintf("credential_types/%d/test/", kv), body), 202)

	// The organization admin creates the credential, then tests it.
	cred := must(t, orgAdmin.post("credentials/", map[string]any{"name": uniq("vault"), "organization": org, "credential_type": kv, "inputs": body["inputs"]}), 201).id()
	must(t, orgAdmin.post(fmt.Sprintf("credentials/%d/test/", cred), map[string]any{"metadata": body["metadata"]}), 202)
	must(t, plain.post(fmt.Sprintf("credentials/%d/test/", cred), map[string]any{"metadata": body["metadata"]}), 403)
}
