package projectsign

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for f := range m {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// The files ansible-sign 0.1.6 checks when verifying (its distlib differ's
// list_files(verifying=True)), for one tree and several MANIFEST.in files.
func TestListFilesLikeAnsibleSign(t *testing.T) {
	tree := []string{"inventory", "README.md", "site.yml", "x.yml.bak", "data[1].txt", "data1.txt", "playbooks/hello.yml",
		"playbooks/sub/deep.yml", "playbooks/notes.txt", "roles/web/tasks/main.yml", "roles/web/files/big.bin", "docs/a.md",
		".ansible-sign/sha256sum.txt", "sub/.ansible-sign/other", "sub/keep.yml"}
	all := []string{"MANIFEST.in", "README.md", "data1.txt", "data[1].txt", "docs/a.md", "inventory", "playbooks/hello.yml",
		"playbooks/notes.txt", "playbooks/sub/deep.yml", "roles/web/files/big.bin", "roles/web/tasks/main.yml", "site.yml",
		"sub/keep.yml", "x.yml.bak"}
	without := func(drop ...string) []string {
		var out []string
		for _, f := range all {
			keep := true
			for _, d := range drop {
				keep = keep && f != d
			}
			if keep {
				out = append(out, f)
			}
		}
		return out
	}
	for name, c := range map[string]struct {
		rules string
		want  []string
	}{
		"includes only":    {"include inventory\nrecursive-include playbooks *.yml\n", all},
		"global and prune": {"global-exclude *.bak\nprune docs\nrecursive-exclude roles *.bin\n", without("x.yml.bak", "docs/a.md", "roles/web/files/big.bin")},
		"anchored exclude": {"# comment\n\nexclude *.md\nrecursive-exclude playbooks *.txt\ngraft roles\n", without("README.md", "playbooks/notes.txt")},
		"classes":          {"global-exclude */deep.yml\nexclude data[1].txt\nexclude data[!0-9]*\n", without("playbooks/sub/deep.yml", "data1.txt", "data[1].txt")},
		"wildcard dir":     {"prune playbooks/su?\nexclude site.yml inventory\n", without("playbooks/sub/deep.yml", "site.yml", "inventory")},
		"bare pattern":     {"*.yml\nglobal-exclude *.yml\n", without("playbooks/hello.yml", "playbooks/sub/deep.yml", "roles/web/tasks/main.yml", "site.yml", "sub/keep.yml")},
		"suffix match":     {"global-exclude ub/keep.yml\n", without("sub/keep.yml")},
	} {
		dir := t.TempDir()
		files := map[string]string{"MANIFEST.in": c.rules}
		for _, f := range tree {
			files[f] = f
		}
		writeFiles(t, dir, files)
		got, err := listFiles(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(sorted(got), c.want) {
			t.Errorf("%s:\n got %v\nwant %v", name, sorted(got), c.want)
		}
	}
}

// A checkout's .git, a directory or a worktree's file, at the root or in a
// submodule, is never part of the content.
func TestGitMetadataNeverCounts(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"MANIFEST.in": "", "site.yml": "x", ".git": "gitdir: /elsewhere",
		"mod/.git": "gitdir: ../.git/modules/mod", "mod/main.yml": "x", ".gitignore": "x"})
	got, err := listFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".gitignore", "MANIFEST.in", "mod/main.yml", "site.yml"}; !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("got %v", sorted(got))
	}
	os.Remove(filepath.Join(dir, ".git"))
	writeFiles(t, dir, map[string]string{".git/HEAD": "ref: refs/heads/main", ".git/objects/ab/cd": "x"})
	if got, _ := listFiles(dir); len(got) != 4 {
		t.Errorf(".git directory counted: %v", sorted(got))
	}
}

func TestParseManifest(t *testing.T) {
	sum := strings.Repeat("a", 64)
	got, err := parseManifest(sum + "  site.yml\r\n\n" + sum + " *bin/tool\n")
	if err != nil || !reflect.DeepEqual(got, map[string]string{"site.yml": sum, "bin/tool": sum}) {
		t.Errorf("%v %v", got, err)
	}
	for in, want := range map[string]string{
		"abc  site.yml":   "Unparsable checksum, line 1: abc  site.yml",
		sum + " site.yml": "Unparsable checksum, line 1: " + sum + " site.yml",
		sum + "  a\n" + sum + "  b\n" + sum + "  a": "Duplicate path in checksum, line 3: " + sum + "  a",
	} {
		if _, err := parseManifest(in); err == nil || err.Error() != want {
			t.Errorf("%q: %v", in, err)
		}
	}
}

// sign writes .ansible-sign/sha256sum.txt for the given files (as
// ansible-sign does: sorted, two spaces) and its detached signature, and
// returns the armored public key.
func sign(t *testing.T, dir string, files map[string]string, armored bool) string {
	t.Helper()
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA}
	e, err := openpgp.NewEntity("awxnext test", "", "test@example.com", cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	var manifest strings.Builder
	for _, f := range names {
		h := sha256.Sum256([]byte(files[f]))
		manifest.WriteString(hex.EncodeToString(h[:]) + "  " + f + "\n")
	}
	var sig bytes.Buffer
	if armored {
		err = openpgp.ArmoredDetachSign(&sig, e, strings.NewReader(manifest.String()), cfg)
	} else {
		err = openpgp.DetachSign(&sig, e, strings.NewReader(manifest.String()), cfg)
	}
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, files)
	writeFiles(t, dir, map[string]string{manifestFile: manifest.String(), signatureFile: sig.String()})
	var key bytes.Buffer
	w, _ := armor.Encode(&key, openpgp.PublicKeyType, nil)
	e.Serialize(w)
	w.Close()
	return key.String()
}

func TestVerify(t *testing.T) {
	files := map[string]string{"MANIFEST.in": "include site.yml\nrecursive-include roles *\n", "site.yml": "- hosts: all\n",
		"roles/web/tasks/main.yml": "[]\n"}
	fresh := func(armored bool) (string, string) {
		dir := t.TempDir()
		return dir, sign(t, dir, files, armored)
	}
	dir, key := fresh(true)
	if err := VerifyGPG(dir, key); err != nil {
		t.Fatalf("signed project: %v", err)
	}
	if err := VerifyChecksums(dir); err != nil {
		t.Fatalf("signed project: %v", err)
	}
	_, other := fresh(true)
	if err := VerifyGPG(dir, other); err == nil || !strings.HasPrefix(err.Error(), "GPG signature verification failed") {
		t.Errorf("another key: %v", err)
	}
	if err := VerifyGPG(dir, "not a key"); err == nil || !strings.HasPrefix(err.Error(), "GPG signature verification failed") {
		t.Errorf("bad key: %v", err)
	}

	// A binary signature checks too, with a binary key.
	bin, armoredKey := fresh(false)
	block, _ := armor.Decode(strings.NewReader(armoredKey))
	var raw bytes.Buffer
	raw.ReadFrom(block.Body)
	if err := VerifyGPG(bin, raw.String()); err != nil {
		t.Errorf("binary signature and key: %v", err)
	}

	// The manifest changed after signing: the signature no longer matches.
	edited, key := fresh(true)
	os.WriteFile(filepath.Join(edited, manifestFile), []byte(strings.Repeat("0", 64)+"  site.yml\n"), 0o644)
	if err := VerifyGPG(edited, key); err == nil || !strings.HasPrefix(err.Error(), "GPG signature verification failed") {
		t.Errorf("edited manifest: %v", err)
	}

	for name, c := range map[string]struct {
		change func(dir string)
		want   string
	}{
		"modified":       {func(d string) { os.WriteFile(filepath.Join(d, "site.yml"), []byte("- hosts: evil\n"), 0o644) }, "Checksum mismatch: site.yml"},
		"added":          {func(d string) { writeFiles(t, d, map[string]string{"roles/web/tasks/extra.yml": "x"}) }, "{'added': ['roles/web/tasks/extra.yml'], 'removed': []}"},
		"removed":        {func(d string) { os.Remove(filepath.Join(d, "roles/web/tasks/main.yml")) }, "{'added': [], 'removed': ['roles/web/tasks/main.yml']}"},
		"no MANIFEST.in": {func(d string) { os.Remove(filepath.Join(d, "MANIFEST.in")) }, "Expected file not found: MANIFEST.in"},
		"bad manifest line": {func(d string) { os.WriteFile(filepath.Join(d, manifestFile), []byte("nope\n"), 0o644) },
			"Invalid line in checksum manifest: Unparsable checksum, line 1: nope"},
	} {
		d, _ := fresh(true)
		c.change(d)
		if err := VerifyChecksums(d); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v", name, err)
		}
	}

	unsigned := t.TempDir()
	writeFiles(t, unsigned, files)
	if err := VerifyGPG(unsigned, key); err == nil || err.Error() != "Expected file not found: .ansible-sign/sha256sum.txt.sig" {
		t.Errorf("unsigned: %v", err)
	}
	if err := VerifyChecksums(unsigned); err == nil || err.Error() != "Expected file not found: .ansible-sign/sha256sum.txt" {
		t.Errorf("unsigned: %v", err)
	}
}
