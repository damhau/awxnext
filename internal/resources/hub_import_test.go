package resources

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

func TestReadCollectionArtifact(t *testing.T) {
	a, err := ReadCollectionArtifact(artifact(t, map[string]string{
		"MANIFEST.json":    `{"collection_info": {"namespace": "acme", "name": "tools", "version": "1.2.3", "dependencies": null}, "format": 1}`,
		"FILES.json":       `{"files": [], "format": 1}`,
		"meta/runtime.yml": "requires_ansible: '>=2.15'\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Namespace != "acme" || a.Name != "tools" || a.Version != "1.2.3" || a.RequiresAnsible != ">=2.15" || a.Files == nil || a.Manifest["format"] == nil {
		t.Fatalf("artifact: %+v", a)
	}
	if deps, ok := a.Info["dependencies"].(map[string]any); !ok || len(deps) != 0 {
		t.Fatalf("dependencies: %v", a.Info["dependencies"])
	}
	for name, members := range map[string]map[string]string{
		"no manifest":   {"FILES.json": "{}"},
		"bad version":   {"MANIFEST.json": `{"collection_info": {"namespace": "acme", "name": "tools", "version": "1.2"}}`},
		"bad namespace": {"MANIFEST.json": `{"collection_info": {"namespace": "Acme", "name": "tools", "version": "1.2.0"}}`},
		"bad deps":      {"MANIFEST.json": `{"collection_info": {"namespace": "acme", "name": "tools", "version": "1.2.0", "dependencies": []}}`},
		"huge manifest": {"MANIFEST.json": strings.Repeat(" ", maxManifestBytes+1)},
	} {
		if _, err := ReadCollectionArtifact(artifact(t, members)); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	if _, err := ReadCollectionArtifact([]byte("plain text")); err == nil {
		t.Error("not gzip: read")
	}
}
