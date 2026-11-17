package projectscan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCollectionRequirements(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("collections/requirements.yml", `collections:
  - acme.tools
  - acme.net:>=1.0.0,<2.0.0
  - name: acme.cloud
    version: 1.0
    source: https://hub.example.com/api/galaxy/
  - name: https://github.com/acme/thing.git
    type: git
  - name: acme.local
    type: dir
  - name: not a collection
`)
	write("requirements.yml", "roles:\n  - acme.role\ncollections:\n  - name: acme.top\n")
	got := CollectionRequirements(dir)
	want := []CollectionRequirement{
		{Name: "acme.tools", File: "collections/requirements.yml"},
		{Name: "acme.net", Version: ">=1.0.0,<2.0.0", File: "collections/requirements.yml"},
		{Name: "acme.cloud", Version: "1.0", Source: "https://hub.example.com/api/galaxy/", File: "collections/requirements.yml"},
		{Name: "acme.top", File: "requirements.yml"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "r.yml"), []byte("collections:\n  - secret.thing\n"), 0o644)
	linked := t.TempDir()
	os.MkdirAll(filepath.Join(linked, "collections"), 0o755)
	if err := os.Symlink(filepath.Join(outside, "r.yml"), filepath.Join(linked, "collections", "requirements.yml")); err != nil {
		t.Fatal(err)
	}
	if got := CollectionRequirements(linked); len(got) != 0 {
		t.Fatalf("read a requirement file outside the checkout: %+v", got)
	}
}
