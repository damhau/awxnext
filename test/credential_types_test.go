package test

import (
	"fmt"
	"strings"
	"testing"
)

// What the credential types page relies on (roadmap M7-04): a custom type
// created as the page sends it (inputs and injectors as objects), its
// errors under "inputs" and "injectors", built-in types read-only, the
// credentials using a type counted, and a type in use keeping its inputs
// and its existence while its injectors can change.
func TestCredentialTypesPage(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	inputs := map[string]any{"fields": []any{map[string]any{"id": "token", "label": "Token", "type": "string", "secret": true}}, "required": []any{"token"}}
	injectors := map[string]any{"env": map[string]any{"MYAPP_TOKEN": "{{ token }}"}}

	if r := must(t, a.post("credential_types/", map[string]any{"name": uniq("bad"), "kind": "cloud", "inputs": inputs,
		"injectors": map[string]any{"env": map[string]any{"X": "{{ tokn }}"}}}), 400); r.Body["injectors"] == nil {
		t.Fatalf("an undefined variable: %v", r.Body)
	}
	if r := must(t, a.post("credential_types/", map[string]any{"name": uniq("bad"), "kind": "ssh", "inputs": inputs}), 400); r.Body["kind"] == nil {
		t.Fatalf("a custom machine type: %v", r.Body)
	}
	ct := must(t, a.post("credential_types/", map[string]any{"name": uniq("myapp"), "kind": "cloud", "inputs": inputs, "injectors": injectors}), 201).id()
	url := fmt.Sprintf("credential_types/%d/", ct)
	if caps := must(t, a.get(url), 200).Body["summary_fields"].(map[string]any)["user_capabilities"].(map[string]any); caps["edit"] != true || caps["delete"] != true {
		t.Fatalf("capabilities of a custom type: %v", caps)
	}

	ssh := must(t, a.get("credential_types/?namespace=ssh"), 200).results()[0]
	if caps := ssh["summary_fields"].(map[string]any)["user_capabilities"].(map[string]any); caps["edit"] != false {
		t.Errorf("a built-in type is editable: %v", caps)
	}
	must(t, a.patch(fmt.Sprintf("credential_types/%v/", ssh["id"]), map[string]any{"description": "x"}), 403)

	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("c"), "organization": org, "credential_type": ct, "inputs": map[string]any{"token": "s3cret"}}), 201).id()
	if n := must(t, a.get(url+"credentials/?page_size=1"), 200).count(); n != 1 {
		t.Fatalf("credentials of the type: %d", n)
	}
	more := map[string]any{"fields": append(inputs["fields"].([]any), map[string]any{"id": "region", "label": "Region", "type": "string"})}
	if r := must(t, a.patch(url, map[string]any{"inputs": more}), 403); !strings.Contains(fmt.Sprint(r.Body), "in use") {
		t.Fatalf("inputs of a type in use: %v", r.Body)
	}
	must(t, a.patch(url, map[string]any{"injectors": map[string]any{"env": map[string]any{"MYAPP_TOKEN": "{{ token }}", "MYAPP": "1"}}}), 200)
	must(t, a.delete(url), 403)
	must(t, a.delete(fmt.Sprintf("credentials/%d/", cred)), 204)
	must(t, a.delete(url), 204)
}
