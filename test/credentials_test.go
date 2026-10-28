package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/resources"
)

func credType(t *testing.T, a *client, namespace string) int64 {
	t.Helper()
	r := must(t, a.get("credential_types/?namespace="+namespace), 200)
	if r.count() != 1 {
		t.Fatalf("credential type %s not found: %s", namespace, r.Raw)
	}
	return int64(r.results()[0]["id"].(float64))
}

const testKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n"

func TestManagedCredentialTypes(t *testing.T) {
	a := adminClient(t)
	r := must(t, a.get("credential_types/?managed=true&page_size=100"), 200)
	names := map[string]bool{}
	for _, ct := range r.results() {
		names[ct["namespace"].(string)] = true
	}
	for _, ns := range []string{"ssh", "scm", "vault", "net", "aws", "azure_rm", "gce", "vmware", "kubernetes_bearer_token", "registry", "galaxy_api_token", "hashivault_kv", "hashivault_ssh", "azure_kv"} {
		if !names[ns] {
			t.Errorf("missing managed type %s", ns)
		}
	}
	vault := must(t, a.get(fmt.Sprintf("credential_types/%d/", credType(t, a, "hashivault_kv"))), 200)
	req := fmt.Sprint(vault.Body["inputs"].(map[string]any)["required"])
	if !strings.Contains(req, "secret_path") || !strings.Contains(req, "url") {
		t.Fatalf("hashivault_kv required: %s", req)
	}
	// Managed types are read-only.
	must(t, a.patch(fmt.Sprintf("credential_types/%d/", credType(t, a, "ssh")), map[string]any{"description": "x"}), 403)
}

func TestCustomCredentialType(t *testing.T) {
	a := adminClient(t)
	bad := []map[string]any{
		{"name": uniq("ct"), "kind": "ssh", "inputs": map[string]any{}},
		{"name": uniq("ct"), "kind": "cloud", "inputs": map[string]any{"fields": []any{map[string]any{"id": "1bad", "label": "x"}}}},
		{"name": uniq("ct"), "kind": "cloud", "inputs": map[string]any{"fields": []any{map[string]any{"id": "tok", "label": "T"}}},
			"injectors": map[string]any{"env": map[string]any{"PATH": "{{tok}}"}}},
		{"name": uniq("ct"), "kind": "cloud", "inputs": map[string]any{"fields": []any{map[string]any{"id": "tok", "label": "T"}}},
			"injectors": map[string]any{"env": map[string]any{"MY_TOKEN": "{{ nope }}"}}},
		// Jinja2 checked as AWX does: filters, syntax, names in blocks, nested extra_vars, file syntax.
		{"name": uniq("ct"), "kind": "cloud", "inputs": map[string]any{"fields": []any{map[string]any{"id": "tok", "label": "T"}}},
			"injectors": map[string]any{"env": map[string]any{"MY_TOKEN": "{{ tok | b64encode }}"}}},
		{"name": uniq("ct"), "kind": "cloud", "inputs": map[string]any{"fields": []any{map[string]any{"id": "tok", "label": "T"}}},
			"injectors": map[string]any{"env": map[string]any{"MY_TOKEN": "{% if tok %}x"}}},
		{"name": uniq("ct"), "kind": "cloud", "inputs": map[string]any{"fields": []any{map[string]any{"id": "tok", "label": "T"}}},
			"injectors": map[string]any{"env": map[string]any{"MY_TOKEN": "{% if nope %}x{% endif %}"}}},
		{"name": uniq("ct"), "kind": "cloud", "inputs": map[string]any{"fields": []any{map[string]any{"id": "tok", "label": "T"}}},
			"injectors": map[string]any{"extra_vars": map[string]any{"cfg": map[string]any{"token": "{{ nope }}"}}}},
		{"name": uniq("ct"), "kind": "cloud", "inputs": map[string]any{"fields": []any{map[string]any{"id": "tok", "label": "T"}}},
			"injectors": map[string]any{"file": map[string]any{"template": "{{ tok }}", "template.other": "{{ tok }}"}}},
	}
	for _, b := range bad {
		must(t, a.post("credential_types/", b), 400)
	}
	ct := must(t, a.post("credential_types/", map[string]any{
		"name": uniq("ct"), "kind": "cloud",
		"inputs": map[string]any{"fields": []any{
			map[string]any{"id": "api_token", "label": "API Token", "secret": true},
			map[string]any{"id": "region", "label": "Region", "choices": []string{"eu", "us"}},
		}, "required": []string{"api_token"}},
		"injectors": map[string]any{
			"env":  map[string]any{"MYCLOUD_TOKEN": "{{ api_token }}", "MYCLOUD_CONFIG": "{{ tower.filename.cfg }}"},
			"file": map[string]any{"template.cfg": "region={{ region | default('eu') }}"},
		},
	}), 201).id()
	// Conditionals, loops, set and filters are accepted.
	must(t, a.post("credential_types/", map[string]any{
		"name": uniq("ct"), "kind": "cloud",
		"inputs": map[string]any{"fields": []any{map[string]any{"id": "hosts", "label": "Hosts"}, map[string]any{"id": "tls", "label": "TLS", "type": "boolean"}}},
		"injectors": map[string]any{
			"env":        map[string]any{"MY_URLS": "{% set s = 'https' if tls else 'http' %}{% for h in hosts.split(',') %}{{ s }}://{{ h | trim }}{{ ',' if not loop.last }}{% endfor %}"},
			"extra_vars": map[string]any{"my": map[string]any{"{{ 'secure' if tls else 'plain' }}": []any{"{{ hosts | upper }}"}}},
		},
	}), 201)
	org := newOrg(t, a)
	must(t, a.post("credentials/", map[string]any{"name": "c", "organization": org, "credential_type": ct, "inputs": map[string]any{"region": "mars"}}), 400)
	cred := must(t, a.post("credentials/", map[string]any{"name": "c", "organization": org, "credential_type": ct, "inputs": map[string]any{"api_token": "t0p", "region": "eu"}}), 201).id()
	// In use: inputs cannot change, type cannot be deleted.
	must(t, a.patch(fmt.Sprintf("credential_types/%d/", ct), map[string]any{"inputs": map[string]any{"fields": []any{}}}), 403)
	must(t, a.delete(fmt.Sprintf("credential_types/%d/", ct)), 403)
	must(t, a.delete(fmt.Sprintf("credentials/%d/", cred)), 204)
	must(t, a.delete(fmt.Sprintf("credential_types/%d/", ct)), 204)
}

func TestCredentialSecrets(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	machine := credType(t, a, "ssh")
	r := must(t, a.post("credentials/", map[string]any{
		"name": "ops ssh", "organization": org, "credential_type": machine,
		"inputs": map[string]any{"username": "deploy", "password": "hunter2", "ssh_key_data": testKey, "become_method": "sudo"},
	}), 201)
	id := r.id()
	in := r.Body["inputs"].(map[string]any)
	if in["password"] != "$encrypted$" || in["ssh_key_data"] != "$encrypted$" || in["username"] != "deploy" {
		t.Fatalf("secrets not masked: %v", in)
	}
	if r.Body["kind"] != "ssh" {
		t.Fatalf("kind: %v", r.Body["kind"])
	}
	var stored map[string]any
	app.DB.QueryRow(t.Context(), "SELECT inputs FROM credentials WHERE id = $1", id).Scan(&stored)
	if !strings.HasPrefix(stored["password"].(string), "$encrypted$v1$") {
		t.Fatalf("password stored in clear: %v", stored["password"])
	}
	// Round trip with $encrypted$ keeps secrets; changing username only.
	must(t, a.patch(fmt.Sprintf("credentials/%d/", id), map[string]any{"inputs": map[string]any{
		"username": "deploy2", "password": "$encrypted$", "ssh_key_data": "$encrypted$", "become_method": "sudo"}}), 200)
	var plain string
	app.DB.QueryRow(t.Context(), "SELECT inputs->>'password' FROM credentials WHERE id = $1", id).Scan(&plain)
	if dec, _ := app.Crypto.Decrypt(plain); dec != "hunter2" {
		t.Fatalf("password lost after $encrypted$ round trip: %q", dec)
	}
	// Validation: unknown field, bad key, dependency.
	must(t, a.post("credentials/", map[string]any{"name": uniq("x"), "organization": org, "credential_type": machine, "inputs": map[string]any{"nope": "x"}}), 400)
	must(t, a.post("credentials/", map[string]any{"name": uniq("x"), "organization": org, "credential_type": machine, "inputs": map[string]any{"ssh_key_data": "not a key"}}), 400)
	must(t, a.post("credentials/", map[string]any{"name": uniq("x"), "organization": org, "credential_type": machine, "inputs": map[string]any{"ssh_key_unlock": "pw"}}), 400)
	// ASK is allowed for prompt-at-launch fields.
	must(t, a.post("credentials/", map[string]any{"name": uniq("x"), "organization": org, "credential_type": machine, "inputs": map[string]any{"password": "ASK"}}), 201)
	// Activity stream and history never expose ciphertext.
	act := must(t, a.get(fmt.Sprintf("credentials/%d/activity_stream/", id)), 200)
	if strings.Contains(act.Raw, "$encrypted$v1$") || strings.Contains(act.Raw, "hunter2") {
		t.Fatal("activity stream leaks secrets")
	}
	hist := must(t, a.get(fmt.Sprintf("credentials/%d/history/1/", id)), 200)
	if strings.Contains(hist.Raw, "$encrypted$v1$") || strings.Contains(hist.Raw, "hunter2") {
		t.Fatal("history leaks secrets")
	}
	// Copy keeps the encrypted secret.
	cp := must(t, a.post(fmt.Sprintf("credentials/%d/copy/", id), map[string]any{"name": "ops ssh copy"}), 201)
	if cp.Body["inputs"].(map[string]any)["password"] != "$encrypted$" {
		t.Fatalf("copy lost secret: %s", cp.Raw)
	}
}

func TestAnsibleVaultCredentials(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	vault := credType(t, a, "vault")
	r := must(t, a.post("credentials/", map[string]any{"name": "vault prod", "organization": org, "credential_type": vault,
		"inputs": map[string]any{"vault_password": "s3cret", "vault_id": "prod"}}), 201)
	if r.Body["inputs"].(map[string]any)["vault_password"] != "$encrypted$" || r.Body["kind"] != "vault" {
		t.Fatalf("vault credential: %s", r.Raw)
	}
	must(t, a.post("credentials/", map[string]any{"name": "vault ask", "organization": org, "credential_type": vault,
		"inputs": map[string]any{"vault_password": "ASK", "vault_id": "dev"}}), 201)
	must(t, a.post("credentials/", map[string]any{"name": uniq("v"), "organization": org, "credential_type": vault,
		"inputs": map[string]any{"vault_password": "x", "vault_id": "bad id@x"}}), 400)
}

func TestPersonalAndTeamCredentials(t *testing.T) {
	a := adminClient(t)
	u, uid := newUser(t, a)
	machine := credType(t, a, "ssh")
	// Personal credential: no organization, owned by the creator.
	r := must(t, u.post("credentials/", map[string]any{"name": "mine", "credential_type": machine, "user": uid, "inputs": map[string]any{"username": "me"}}), 201)
	id := r.id()
	owners := r.Body["summary_fields"].(map[string]any)["owners"].([]any)
	if len(owners) != 1 || owners[0].(map[string]any)["type"] != "user" {
		t.Fatalf("owners: %v", owners)
	}
	other, _ := newUser(t, a)
	must(t, other.get(fmt.Sprintf("credentials/%d/", id)), 403)
	must(t, u.patch(fmt.Sprintf("credentials/%d/", id), map[string]any{"description": "mine"}), 200)

	// Team credential: team members can use it.
	org := newOrg(t, a)
	team := must(t, a.post("teams/", map[string]any{"name": uniq("t"), "organization": org}), 201).id()
	_, oid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("teams/%d/users/", team), map[string]any{"id": oid}), 204)
	tc := must(t, a.post("credentials/", map[string]any{"name": "team cred", "organization": org, "team": team, "credential_type": machine}), 201).id()
	var ok bool
	app.DB.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
		WHERE ra.team_id = $1 AND ra.content_type = 'credential' AND ra.object_id = $2 AND rd.name = 'Credential Use')`, team, tc).Scan(&ok)
	if !ok {
		t.Fatal("team was not granted use")
	}
}

// fakeVaultKV serves KV v1 and v2 reads.
func fakeVaultKV(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "root" {
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"errors": []string{"permission denied"}})
			return
		}
		switch r.URL.Path {
		case "/v1/kv/data/apps/checkout":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]any{"db_password": "from-vault-v2"}}})
		case "/v1/secret/apps/legacy":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"db_password": "from-vault-v1"}})
		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"errors": []string{}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCredentialInputSources(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	vsrv := fakeVaultKV(t)
	lookup := must(t, a.post("credentials/", map[string]any{"name": "vault kv", "organization": org, "credential_type": credType(t, a, "hashivault_kv"),
		"inputs": map[string]any{"url": vsrv.URL, "token": "root", "api_version": "v2"}}), 201).id()

	// Test endpoint.
	must(t, a.post(fmt.Sprintf("credentials/%d/test/", lookup), map[string]any{"metadata": map[string]any{"secret_path": "kv/apps/checkout", "secret_key": "db_password"}}), 202)
	r := must(t, a.post(fmt.Sprintf("credentials/%d/test/", lookup), map[string]any{"metadata": map[string]any{"secret_path": "kv/apps/missing", "secret_key": "db_password"}}), 400)
	if _, ok := r.Body["inputs"].(string); !ok {
		t.Fatalf("test error format: %s", r.Raw)
	}
	must(t, a.post(fmt.Sprintf("credential_types/%d/test/", credType(t, a, "hashivault_kv")), map[string]any{
		"inputs":   map[string]any{"url": vsrv.URL, "token": "root", "api_version": "v1"},
		"metadata": map[string]any{"secret_backend": "secret", "secret_path": "apps/legacy", "secret_key": "db_password"},
	}), 202)

	target := must(t, a.post("credentials/", map[string]any{"name": "db machine", "organization": org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "postgres"}}), 201).id()
	// Validation: unknown field, missing metadata, non-external source.
	must(t, a.post("credential_input_sources/", map[string]any{"target_credential": target, "source_credential": lookup, "input_field_name": "nope",
		"metadata": map[string]any{"secret_path": "kv/x", "secret_key": "k"}}), 400)
	must(t, a.post("credential_input_sources/", map[string]any{"target_credential": target, "source_credential": lookup, "input_field_name": "password",
		"metadata": map[string]any{"secret_path": "kv/x"}}), 400)
	must(t, a.post("credential_input_sources/", map[string]any{"target_credential": lookup, "source_credential": target, "input_field_name": "password",
		"metadata": map[string]any{}}), 400)
	must(t, a.post(fmt.Sprintf("credentials/%d/input_sources/", target), map[string]any{"source_credential": lookup, "input_field_name": "password",
		"metadata": map[string]any{"secret_path": "kv/apps/checkout", "secret_key": "db_password"}}), 201)

	rc, err := resolveCredential(target)
	if err != nil {
		t.Fatal(err)
	}
	if rc["password"] != "from-vault-v2" || rc["username"] != "postgres" {
		t.Fatalf("resolved inputs: %v", rc)
	}
}

func resolveCredential(id int64) (map[string]any, error) {
	rc, err := resources.ResolveCredential(context.Background(), app, id)
	if err != nil {
		return nil, err
	}
	return rc.Inputs, nil
}
