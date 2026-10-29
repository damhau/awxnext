// Package projectsign verifies projects signed with ansible-sign, as AWX's
// verify_project action does when a project has a signature validation
// credential: the GPG signature of .ansible-sign/sha256sum.txt, then the
// project's files against that manifest, MANIFEST.in deciding which files
// count (ansible-sign's distlib differ).
package projectsign

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

const (
	manifestFile  = ".ansible-sign/sha256sum.txt"
	signatureFile = ".ansible-sign/sha256sum.txt.sig"
)

// VerifyGPG checks the manifest's detached signature (armored or binary)
// with an armored or binary public key.
func VerifyGPG(dir, publicKey string) error {
	for _, f := range []string{signatureFile, manifestFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return fmt.Errorf("Expected file not found: %s", f)
		}
	}
	keys, err := openpgp.ReadArmoredKeyRing(strings.NewReader(publicKey))
	if err != nil {
		if keys, err = openpgp.ReadKeyRing(strings.NewReader(publicKey)); err != nil {
			return fmt.Errorf("GPG signature verification failed: the public key cannot be read: %v", err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, signatureFile))
	if err != nil {
		return err
	}
	if block, err := armor.Decode(bytes.NewReader(sig)); err == nil {
		if sig, err = io.ReadAll(block.Body); err != nil {
			return fmt.Errorf("GPG signature verification failed: %v", err)
		}
	}
	if _, err := openpgp.CheckDetachedSignature(keys, bytes.NewReader(manifest), bytes.NewReader(sig), nil); err != nil {
		return fmt.Errorf("GPG signature verification failed: %v", err)
	}
	return nil
}

// VerifyChecksums checks the project's files against the manifest: the same
// files (as MANIFEST.in selects them) and the same SHA-256 sums.
func VerifyChecksums(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return fmt.Errorf("Expected file not found: %s", manifestFile)
	}
	sums, err := parseManifest(string(raw))
	if err != nil {
		return fmt.Errorf("Invalid line in checksum manifest: %v", err)
	}
	real, err := listFiles(dir)
	if err != nil {
		return err
	}
	var added, removed []string
	for f := range real {
		if _, ok := sums[f]; !ok {
			added = append(added, f)
		}
	}
	for f := range sums {
		if !real[f] {
			removed = append(removed, f)
		}
	}
	if len(added) > 0 || len(removed) > 0 {
		sort.Strings(added)
		sort.Strings(removed)
		return fmt.Errorf("{'added': %s, 'removed': %s}", pyList(added), pyList(removed))
	}
	var mismatched []string
	for f, want := range sums {
		got, err := sha256File(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return err
		}
		if got != want {
			mismatched = append(mismatched, f)
		}
	}
	if len(mismatched) > 0 {
		sort.Strings(mismatched)
		return fmt.Errorf("Checksum mismatch: %s", strings.Join(mismatched, ", "))
	}
	return nil
}

func pyList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "'" + s + "'"
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// parseManifest reads GNU-style lines: 64 hex digits, a space, then a space
// or "*", then the path.
func parseManifest(s string) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sum, rest, ok := strings.Cut(line, " ")
		if !ok || len(sum) != 64 || len(rest) < 2 || (rest[0] != ' ' && rest[0] != '*') {
			return nil, fmt.Errorf("Unparsable checksum, line %d: %s", i+1, line)
		}
		p := rest[1:]
		if _, dup := out[p]; dup {
			return nil, fmt.Errorf("Duplicate path in checksum, line %d: %s", i+1, line)
		}
		out[p] = sum
	}
	return out, nil
}

// listFiles is ansible-sign's verifying file list: every file
// ("global-include *"), then MANIFEST.in's directives, without
// .ansible-sign's files, with MANIFEST.in. A .git entry, at any depth, is a
// checkout's version control data and never counts: awxnext's worktrees have
// a .git file, submodules too, which a MANIFEST.in written for AWX's clones
// (where "prune .git" removes a directory) would not exclude.
func listFiles(dir string) (map[string]bool, error) {
	base, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	rules, err := os.ReadFile(filepath.Join(base, "MANIFEST.in"))
	if err != nil {
		return nil, errors.New("Expected file not found: MANIFEST.in")
	}
	m := &manifest{base: base, files: map[string]bool{}}
	if err := m.findAll(); err != nil {
		return nil, err
	}
	lines := append([]string{"global-include *"}, strings.Split(string(rules), "\n")...)
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || l[0] == '#' {
			continue
		}
		if err := m.process(l); err != nil {
			return nil, fmt.Errorf("MANIFEST.in: %v", err)
		}
	}
	out := map[string]bool{}
	for f := range m.files {
		rel, err := filepath.Rel(base, f)
		if err != nil {
			continue
		}
		rel = path.Clean(filepath.ToSlash(rel))
		parts := strings.Split(rel, "/")
		// .ansible-sign and .ansible-sign/** as pathlib matches them: the
		// last component, or the one before it.
		if parts[len(parts)-1] == ".ansible-sign" || len(parts) > 1 && parts[len(parts)-2] == ".ansible-sign" {
			continue
		}
		out[rel] = true
	}
	out["MANIFEST.in"] = true
	return out, nil
}

// manifest is distlib's Manifest: files found under base, selected by
// directives whose patterns are matched against absolute paths.
type manifest struct {
	base  string
	all   []string
	files map[string]bool
}

func (m *manifest) findAll() error {
	return filepath.WalkDir(m.base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == m.base {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Type().IsRegular():
			m.all = append(m.all, p)
		case d.Type()&fs.ModeSymlink != 0:
			// Only symlinks to files count; symlinked directories are not walked.
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				m.all = append(m.all, p)
			}
		}
		return nil
	})
}

var actions = map[string]bool{"include": true, "exclude": true, "global-include": true, "global-exclude": true,
	"recursive-include": true, "recursive-exclude": true, "graft": true, "prune": true}

func (m *manifest) process(line string) error {
	words := strings.Fields(line)
	if len(words) == 1 && !actions[words[0]] {
		words = append([]string{"include"}, words...)
	}
	action := words[0]
	switch action {
	case "include", "exclude", "global-include", "global-exclude":
		if len(words) < 2 {
			return fmt.Errorf("'%s' expects <pattern1> <pattern2> ...", action)
		}
		for _, p := range words[1:] {
			re := m.pattern(p, !strings.HasPrefix(action, "global-"), "")
			m.apply(re, !strings.HasSuffix(action, "exclude"))
		}
	case "recursive-include", "recursive-exclude":
		if len(words) < 3 {
			return fmt.Errorf("'%s' expects <dir> <pattern1> <pattern2> ...", action)
		}
		for _, p := range words[2:] {
			m.apply(m.pattern(p, true, words[1]), action == "recursive-include")
		}
	case "graft", "prune":
		if len(words) != 2 {
			return fmt.Errorf("'%s' expects a single <dir_pattern>", action)
		}
		m.apply(m.pattern("", true, words[1]), action == "graft")
	default:
		return fmt.Errorf("unknown action '%s'", action)
	}
	return nil
}

// apply adds the files matching re (from all files) or removes them.
func (m *manifest) apply(re *regexp.Regexp, include bool) {
	if include {
		for _, f := range m.all {
			if re.MatchString(f) {
				m.files[f] = true
			}
		}
		return
	}
	for f := range m.files {
		if re.MatchString(f) {
			delete(m.files, f)
		}
	}
}

// pattern is distlib's _translate_pattern: anchored at the base, a path
// suffix anywhere when not anchored, or under a directory prefix.
func (m *manifest) pattern(pat string, anchor bool, prefix string) *regexp.Regexp {
	base := regexp.QuoteMeta(m.base + "/")
	p := globToRe(pat)
	switch {
	case prefix != "":
		return regexp.MustCompile(`(?s)^` + base + globToRe(prefix) + `/.*` + p + `\z`)
	case anchor:
		return regexp.MustCompile(`(?s)^` + base + p + `\z`)
	default:
		return regexp.MustCompile(`(?s)` + p + `\z`)
	}
}

// globToRe is fnmatch.translate as distlib uses it: "*" and "?" do not
// match "/".
func globToRe(pat string) string {
	var b strings.Builder
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch c {
		case '*':
			b.WriteString(`[^/]*`)
		case '?':
			b.WriteString(`[^/]`)
		case '[':
			j := i + 1
			if j < len(pat) && pat[j] == '!' {
				j++
			}
			if j < len(pat) && pat[j] == ']' {
				j++
			}
			for j < len(pat) && pat[j] != ']' {
				j++
			}
			if j >= len(pat) {
				b.WriteString(`\[`)
				continue
			}
			class := strings.ReplaceAll(pat[i+1:j], `\`, `\\`)
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			} else if strings.HasPrefix(class, "^") {
				class = `\` + class
			}
			b.WriteString("[" + class + "]")
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}
