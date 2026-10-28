package projectscan

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const play = "- hosts: all\n  tasks: []\n"

func TestScanSymlinks(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.yml"), []byte(play), 0o644)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "site.yml"), []byte(play), 0o644)
	os.Mkdir(filepath.Join(dir, "plays"), 0o755)
	os.WriteFile(filepath.Join(dir, "plays", "deploy.yml"), []byte(play), 0o644)
	os.Symlink("site.yml", filepath.Join(dir, "linked.yml")) // a link to a file
	os.Symlink("plays", filepath.Join(dir, "more"))          // a link to a directory
	os.Symlink(outside, filepath.Join(dir, "elsewhere"))     // a directory outside the project
	os.Symlink(filepath.Join(outside, "secret.yml"), filepath.Join(dir, "out.yml"))
	os.Symlink(".", filepath.Join(dir, "plays", "loop")) // a loop

	f, err := Scan(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"linked.yml", "plays/deploy.yml", "site.yml"}; !slices.Equal(f.Playbooks, want) {
		t.Fatalf("without following links: %v, want %v", f.Playbooks, want)
	}

	// AWX_SHOW_PLAYBOOK_LINKS: links are followed wherever they point; a
	// directory reached both ways is listed once, under its real path.
	f, _ = Scan(dir, true)
	if want := []string{"elsewhere/secret.yml", "linked.yml", "out.yml", "plays/deploy.yml", "site.yml"}; !slices.Equal(f.Playbooks, want) {
		t.Fatalf("following links: %v, want %v", f.Playbooks, want)
	}
}
