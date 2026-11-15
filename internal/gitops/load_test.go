package gitops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkipsRuleTests(t *testing.T) {
	dir := t.TempDir()
	doc := "kind: EventRule\nname: R\nspec: {filter: \"true\"}\n---\nkind: EventRuleTest\nname: T\nspec:\n  events: [{type: x}]\n"
	if err := os.WriteFile(filepath.Join(dir, "rules.yml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	docs, err := Load(dir)
	if err != nil || len(docs) != 1 || docs[0].Kind != "EventRule" {
		t.Fatalf("Load: %v, %+v", err, docs)
	}
	docs, tests, err := LoadWithTests(dir)
	if err != nil || len(docs) != 1 || len(tests) != 1 || tests[0].Name != "T" || tests[0].File != "rules.yml#2" {
		t.Fatalf("LoadWithTests: %v, %+v, %+v", err, docs, tests)
	}
}
