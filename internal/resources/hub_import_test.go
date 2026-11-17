package resources

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	// semver.org's example of precedence, shuffled.
	want := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.2.0", "1.10.0", "2.0.0+build.5"}
	got := []string{"1.10.0", "1.0.0-beta.11", "1.0.0", "2.0.0+build.5", "1.0.0-alpha.beta", "1.0.0-rc.1", "1.0.0-alpha", "1.2.0", "1.0.0-beta.2", "1.0.0-alpha.1", "1.0.0-beta"}
	sort.Slice(got, func(i, j int) bool { return compareSemver(got[i], got[j]) < 0 })
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("order: %v", got)
	}
	if compareSemver("1.0.0+a", "1.0.0+b") != 0 {
		t.Fatal("build metadata counts in precedence")
	}
	for _, bad := range []string{"1.0", "01.0.0", "1.0.0-", "v1.0.0", "1.0.0-01"} {
		if _, ok := parseSemver(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if highestVersion([]hubVersion{{version: "2.0.0-rc.1"}, {version: "1.5.0"}}) != "1.5.0" ||
		highestVersion([]hubVersion{{version: "2.0.0-rc.1"}}) != "2.0.0-rc.1" {
		t.Fatal("highest version")
	}
}

// tarMember is a member of a test artifact: a regular file by default.
type tarMember struct {
	name, data, link string
	typ              byte
	// unlisted members are left out of FILES.json.
	unlisted bool
}

func baseInfo() map[string]any {
	return map[string]any{"namespace": "acme", "name": "tools", "version": "1.2.3", "authors": []any{"Me"}, "readme": "README.md",
		"license": []any{"MIT"}, "tags": []any{"tools"}, "dependencies": map[string]any{"acme.base": ">=1.0.0,<2.0.0"}}
}

// buildArtifact makes a collection tarball as ansible-galaxy builds it:
// MANIFEST.json and FILES.json first, with their checksums. tamper changes
// FILES.json's entries before they are summed.
func buildArtifact(t *testing.T, info map[string]any, members []tarMember, tamper func(entries []map[string]any)) []byte {
	t.Helper()
	all := append([]tarMember{{name: "README.md", data: "# tools\n"}, {name: "meta", typ: tar.TypeDir},
		{name: "meta/runtime.yml", data: "requires_ansible: '>=2.15'\n"}}, members...)
	contents := map[string]string{}
	for _, m := range all {
		if m.typ == 0 || m.typ == tar.TypeReg {
			contents[m.name] = m.data
		}
	}
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	entries := []map[string]any{{"name": ".", "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1}}
	for _, m := range all {
		switch {
		case m.unlisted:
		case m.typ == tar.TypeDir:
			entries = append(entries, map[string]any{"name": m.name, "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1})
		default:
			data := m.data
			if m.typ == tar.TypeSymlink {
				data = contents[path.Join(path.Dir(m.name), m.link)]
			}
			entries = append(entries, map[string]any{"name": m.name, "ftype": "file", "chksum_type": "sha256", "chksum_sha256": sum([]byte(data)), "format": 1})
		}
	}
	if tamper != nil {
		tamper(entries)
	}
	files, _ := json.Marshal(map[string]any{"files": entries, "format": 1})
	manifest, _ := json.Marshal(map[string]any{"collection_info": info, "format": 1, "file_manifest_file": map[string]any{
		"name": "FILES.json", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": sum(files), "format": 1}})
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(m tarMember) {
		h := &tar.Header{Name: m.name, Mode: 0o644, Typeflag: m.typ, Linkname: m.link}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(m.data))
		}
		if h.Typeflag == tar.TypeDir {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(m.data))
	}
	write(tarMember{name: "MANIFEST.json", data: string(manifest)})
	write(tarMember{name: "FILES.json", data: string(files)})
	for _, m := range all {
		write(m)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestReadCollectionArtifact(t *testing.T) {
	log := &ImportLog{}
	a, err := checkCollectionArtifact(buildArtifact(t, baseInfo(), []tarMember{
		{name: "plugins", typ: tar.TypeDir},
		{name: "plugins/modules", typ: tar.TypeDir},
		{name: "plugins/modules/thing.py", data: "print(1)\n"},
		{name: "plugins/modules/alias.py", typ: tar.TypeSymlink, link: "thing.py"},
	}, nil), log)
	if err != nil {
		t.Fatal(err)
	}
	if a.Namespace != "acme" || a.Name != "tools" || a.Version != "1.2.3" || a.RequiresAnsible != ">=2.15" || a.Files == nil || a.Manifest["format"] == nil {
		t.Fatalf("artifact: %+v", a)
	}
	var lines []string
	for _, m := range log.Messages {
		lines = append(lines, m.Level+" "+m.Message)
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{"INFO Layout: 9 members", "INFO Checksums: MANIFEST.json's of FILES.json, and FILES.json's of each of the 4 files, match.",
		"INFO Metadata: acme.tools 1.2.3, requires Ansible >=2.15, depends on acme.base >=1.0.0,<2.0.0."} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}

	with := func(edit func(info map[string]any)) map[string]any {
		info := baseInfo()
		edit(info)
		return info
	}
	cases := []struct {
		name    string
		info    map[string]any
		members []tarMember
		tamper  func(entries []map[string]any)
		code    string
	}{
		{name: "dot dot", members: []tarMember{{name: "docs/../../etc/passwd", data: "x"}}, code: "invalid_artifact"},
		{name: "absolute", members: []tarMember{{name: "/etc/passwd", data: "x"}}, code: "invalid_artifact"},
		{name: "link outside", members: []tarMember{{name: "evil", typ: tar.TypeSymlink, link: "../../etc/passwd"}}, code: "invalid_artifact"},
		{name: "absolute link", members: []tarMember{{name: "evil", typ: tar.TypeSymlink, link: "/etc/passwd"}}, code: "invalid_artifact"},
		{name: "hard link outside", members: []tarMember{{name: "evil", typ: tar.TypeLink, link: "/etc/passwd"}}, code: "invalid_artifact"},
		{name: "device", members: []tarMember{{name: "dev", typ: tar.TypeChar}}, code: "invalid_artifact"},
		{name: "twice", members: []tarMember{{name: "a.txt", data: "1"}, {name: "a.txt", data: "2", unlisted: true}}, code: "invalid_artifact"},
		{name: "unlisted file", members: []tarMember{{name: "extra.txt", data: "x", unlisted: true}}, code: "invalid_artifact"},
		{name: "file checksum", members: []tarMember{{name: "a.txt", data: "1"}}, tamper: func(entries []map[string]any) {
			entries[len(entries)-1]["chksum_sha256"] = strings.Repeat("0", 64)
		}, code: "checksum_mismatch"},
		{name: "listed but missing", tamper: func(entries []map[string]any) {
			entries[len(entries)-1]["name"] = "meta/gone.yml"
		}, code: "checksum_mismatch"},
		{name: "no readme", info: with(func(i map[string]any) { i["readme"] = "MISSING.md" }), code: "invalid_metadata"},
		{name: "no authors", info: with(func(i map[string]any) { delete(i, "authors") }), code: "invalid_metadata"},
		{name: "bad version", info: with(func(i map[string]any) { i["version"] = "1.2" }), code: "invalid_metadata"},
		{name: "bad namespace", info: with(func(i map[string]any) { i["namespace"] = "Acme" }), code: "invalid_metadata"},
		{name: "deps list", info: with(func(i map[string]any) { i["dependencies"] = []any{} }), code: "invalid_metadata"},
		{name: "bad dep range", info: with(func(i map[string]any) { i["dependencies"] = map[string]any{"acme.base": "latest"} }), code: "invalid_metadata"},
		{name: "self dep", info: with(func(i map[string]any) { i["dependencies"] = map[string]any{"acme.tools": "*"} }), code: "invalid_metadata"},
		{name: "bad tag", info: with(func(i map[string]any) { i["tags"] = []any{"Not A Tag"} }), code: "invalid_metadata"},
	}
	for _, tc := range cases {
		info := tc.info
		if info == nil {
			info = baseInfo()
		}
		_, err := checkCollectionArtifact(buildArtifact(t, info, tc.members, tc.tamper), nil)
		var ie *ImportError
		if !errors.As(err, &ie) || ie.Code != tc.code {
			t.Errorf("%s: %v, want code %s", tc.name, err, tc.code)
		}
	}

	data := buildArtifact(t, baseInfo(), nil, nil)
	gz, _ := gzip.NewReader(bytes.NewReader(data))
	tr := tar.NewReader(gz)
	var tampered bytes.Buffer
	gw := gzip.NewWriter(&tampered)
	tw := tar.NewWriter(gw)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		if h.Name == "FILES.json" {
			b = bytes.Replace(b, []byte(`"format":1}`), []byte(`"format": 1}`), 1)
			h.Size = int64(len(b))
		}
		tw.WriteHeader(h)
		tw.Write(b)
	}
	tw.Close()
	gw.Close()
	var ie *ImportError
	if _, err := checkCollectionArtifact(tampered.Bytes(), nil); !errors.As(err, &ie) || ie.Code != "checksum_mismatch" || !strings.Contains(ie.Message, "FILES.json's SHA-256") {
		t.Errorf("FILES.json changed after MANIFEST.json summed it: %v", err)
	}

	defer func(n int) { maxArtifactMembers = n }(maxArtifactMembers)
	maxArtifactMembers = 5
	if _, err := checkCollectionArtifact(buildArtifact(t, baseInfo(), []tarMember{{name: "a"}, {name: "b"}}, nil), nil); err == nil ||
		!strings.Contains(err.Error(), "more than 5 members") {
		t.Errorf("too many members: %v", err)
	}
	maxArtifactMembers = 50000
	defer func(n int64) { maxArtifactBytes = n }(maxArtifactBytes)
	maxArtifactBytes = 64 << 10
	if _, err := checkCollectionArtifact(buildArtifact(t, baseInfo(), []tarMember{{name: "big", data: strings.Repeat("a", 128<<10)}}, nil), nil); err == nil ||
		!strings.Contains(err.Error(), "decompressed") {
		t.Errorf("too large decompressed: %v", err)
	}
	maxArtifactBytes = 1 << 30

	for name, data := range map[string][]byte{
		"not gzip":      []byte("plain text"),
		"no manifest":   artifact(t, map[string]string{"FILES.json": "{}"}),
		"huge manifest": artifact(t, map[string]string{"MANIFEST.json": strings.Repeat(" ", maxManifestBytes+1)}),
	} {
		if _, err := ReadCollectionArtifact(data); err == nil {
			t.Errorf("%s: read", name)
		}
	}
}

func artifact(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range members {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write([]byte(data))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestVersionMeets(t *testing.T) {
	for _, tc := range []struct {
		version, spec string
		want          bool
	}{
		{"1.2.3", "", true}, {"1.2.3", "*", true}, {"1.2.3", "1.2.3", true}, {"1.2.3", "==1.2.3", true}, {"1.2.4", "1.2.3", false},
		{"1.2.3", ">=1.0.0,<2.0.0", true}, {"2.0.0", ">=1.0.0,<2.0.0", false}, {"1.2.3", "!=1.2.3", false}, {"1.2.3", ">1.2", true},
		{"1.0.0", ">=1", true}, {"0.9.0", ">=1", false}, {"2.0.0-rc.1", "<2.0.0", true}, {"1.2.3", "latest", false},
	} {
		if got := versionMeets(tc.version, tc.spec); got != tc.want {
			t.Errorf("versionMeets(%s, %q) = %v", tc.version, tc.spec, got)
		}
	}
}
