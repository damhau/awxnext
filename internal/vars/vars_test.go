package vars

import (
	"testing"
)

func TestParse(t *testing.T) {
	m, err := Parse("http_port: 8080\nusers: [a, b]\nnested: {x: 1}\n")
	if err != nil || m["http_port"] != 8080 || len(m["users"].([]any)) != 2 {
		t.Fatalf("yaml: %v %v", m, err)
	}
	m, err = Parse(`{"a": true, "b": null}`)
	if err != nil || m["a"] != true {
		t.Fatalf("json: %v %v", m, err)
	}
	if m, _ := Parse("  "); len(m) != 0 {
		t.Fatal("empty should be empty map")
	}
	for _, bad := range []string{"- a\n- b", "just a string", "a: [1"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestParseVault(t *testing.T) {
	m, err := Parse("db_password: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  6162636465\nplain: x\n")
	if err != nil {
		t.Fatal(err)
	}
	v, ok := m["db_password"].(map[string]any)
	if !ok || v["__ansible_vault"] != "$ANSIBLE_VAULT;1.1;AES256\n6162636465\n" {
		t.Fatalf("vault value: %#v", m["db_password"])
	}
	// The JSON form passes through untouched.
	m, _ = Parse(`{"s": {"__ansible_vault": "$ANSIBLE_VAULT;1.1;AES256\n61"}}`)
	if m["s"].(map[string]any)["__ansible_vault"] == nil {
		t.Fatal("json vault form lost")
	}
}

func TestMergeKeys(t *testing.T) {
	m, err := Parse("base: &b {a: 1, b: 2}\nderived:\n  <<: *b\n  b: 3\n")
	if err != nil {
		t.Fatal(err)
	}
	d := m["derived"].(map[string]any)
	if d["a"] != 1 || d["b"] != 3 {
		t.Fatalf("merge: %v", d)
	}
}
