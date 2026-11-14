package ansible

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTree(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("x"), 0o644)
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "roles", "web", "tasks"), 0o750)
	os.WriteFile(filepath.Join(src, "roles", "web", "tasks", "main.yml"), []byte("- debug:\n"), 0o640)
	os.WriteFile(filepath.Join(src, "roles", "web", "run.sh"), []byte("#!/bin/sh\n"), 0o755)
	os.Symlink("tasks/main.yml", filepath.Join(src, "roles", "web", "main.yml"))
	os.Symlink(outside, filepath.Join(src, "roles", "abs"))
	dst := filepath.Join(t.TempDir(), "copy")

	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "roles", "web", "tasks", "main.yml")); err != nil || string(b) != "- debug:\n" {
		t.Fatalf("main.yml: %q %v", b, err)
	}
	for name, want := range map[string]os.FileMode{
		"roles/web":                os.ModeDir | 0o750,
		"roles/web/tasks/main.yml": 0o640,
		"roles/web/run.sh":         0o755,
	} {
		info, err := os.Lstat(filepath.Join(dst, name))
		if err != nil || info.Mode() != want {
			t.Fatalf("%s: mode %v, want %v (%v)", name, info.Mode(), want, err)
		}
	}
	for name, want := range map[string]string{"roles/web/main.yml": "tasks/main.yml", "roles/abs": outside} {
		if link, err := os.Readlink(filepath.Join(dst, name)); err != nil || link != want {
			t.Fatalf("%s: link %q, want %q (%v)", name, link, want, err)
		}
	}
}

// A file replaced by a symlink while the tree is copied must not bring in
// what the link points to outside the tree (#144).
func TestCopyTreeSymlinkSwap(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("OUTSIDE-SECRET"), 0o600)
	src := t.TempDir()
	slot := filepath.Join(src, "slot")
	os.WriteFile(slot, []byte("inside"), 0o600)
	scratch := t.TempDir()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		link, file := filepath.Join(scratch, "link"), filepath.Join(scratch, "file")
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.Symlink(secret, link)
			os.Rename(link, slot)
			os.WriteFile(file, []byte("inside"), 0o600)
			os.Rename(file, slot)
		}
	}()
	defer func() { close(stop); <-done }()

	dst := filepath.Join(t.TempDir(), "copy")
	for range 1000 {
		os.RemoveAll(dst)
		if err := copyTree(src, dst); err != nil {
			continue
		}
		info, err := os.Lstat(filepath.Join(dst, "slot"))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if b, _ := os.ReadFile(filepath.Join(dst, "slot")); string(b) == "OUTSIDE-SECRET" {
			t.Fatal("the file outside the tree was copied")
		}
	}
}
