package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/damhau/awxnext/internal/eecontents"
)

// eeImage pushes an image made of layers of files to ref.
func eeImage(t *testing.T, ref string, layers ...map[string][]byte) v1.Image {
	t.Helper()
	img := empty.Image
	for _, files := range layers {
		l, err := crane.Layer(files)
		if err != nil {
			t.Fatal(err)
		}
		if img, err = mutate.AppendLayers(img, l); err != nil {
			t.Fatal(err)
		}
	}
	if err := crane.Push(img, ref); err != nil {
		t.Fatal(err)
	}
	return img
}

func manifest(ns, name, version string) []byte {
	return []byte(fmt.Sprintf(`{"collection_info": {"namespace": %q, "name": %q, "version": %q}}`, ns, name, version))
}

// An execution environment's contents (roadmap M7-07) are read from its
// registry, without running the image: collections (the layers flattened,
// a deleted one gone), ansible-core and Python versions, ansible.cfg. A read
// is reused for the same digest, done again when the tag moves, and
// reports the registry's error.
func TestExecutionEnvironmentContents(t *testing.T) {
	a := adminClient(t)
	var blobs atomic.Int64
	reg := registry.New()
	rs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/blobs/") {
			blobs.Add(1)
		}
		reg.ServeHTTP(w, r)
	}))
	defer rs.Close()
	ref := strings.TrimPrefix(rs.URL, "http://") + "/ee/custom:1"
	coll := "usr/share/ansible/collections/ansible_collections/"
	img := eeImage(t, ref, map[string][]byte{
		coll + "community/general/MANIFEST.json":                                   manifest("community", "general", "9.5.0"),
		coll + "ansible/posix/MANIFEST.json":                                       manifest("ansible", "posix", "1.6.2"),
		"usr/lib/python3.11/site-packages/ansible_core-2.16.14.dist-info/METADATA": []byte("Name: ansible-core\nVersion: 2.16.14\n"),
		"etc/ansible/ansible.cfg":                                                  []byte("[defaults]\nforks = 10\n"),
	}, map[string][]byte{
		// A later layer removes ansible.posix.
		coll + "ansible/.wh.posix": nil,
	})
	digest, _ := img.Digest()

	ee := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": ref}), 201).id()
	read := func(ee int64) map[string]any {
		t.Helper()
		must(t, a.post(fmt.Sprintf("execution_environments/%d/contents/", ee), nil), 202)
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			r := must(t, a.get(fmt.Sprintf("execution_environments/%d/contents/", ee)), 200).Body
			if r["status"] != "running" {
				return r
			}
			if time.Now().After(deadline) {
				t.Fatalf("still reading: %v", r)
			}
		}
	}
	if r := must(t, a.get(fmt.Sprintf("execution_environments/%d/contents/", ee)), 200).Body; r["status"] != "none" {
		t.Fatalf("before any read: %v", r)
	}
	r := read(ee)
	c, _ := r["contents"].(map[string]any)
	if r["status"] != "successful" || r["digest"] != digest.String() || c == nil {
		t.Fatalf("read: %v", r)
	}
	cols, _ := c["collections"].([]any)
	if c["ansible_core"] != "2.16.14" || c["python"] != "3.11" || !strings.Contains(fmt.Sprint(c["ansible_cfg"]), "forks = 10") ||
		len(cols) != 1 || cols[0].(map[string]any)["name"] != "community.general" || cols[0].(map[string]any)["version"] != "9.5.0" {
		t.Fatalf("contents: %v", c)
	}

	// Another environment of the same image: no layer downloaded again.
	other := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": ref}), 201).id()
	before := blobs.Load()
	if r := read(other); r["status"] != "successful" || r["digest"] != digest.String() {
		t.Fatalf("same digest: %v", r)
	}
	// Its digest comes from the manifest: no blob is downloaded.
	if n := blobs.Load() - before; n != 0 {
		t.Errorf("blobs downloaded for a digest read before: %d", n)
	}

	// The tag moves: read again.
	eeImage(t, ref, map[string][]byte{coll + "kubernetes/core/MANIFEST.json": manifest("kubernetes", "core", "5.0.0")})
	r = read(ee)
	if c, _ := r["contents"].(map[string]any); r["digest"] == digest.String() || !strings.Contains(fmt.Sprint(c["collections"]), "kubernetes.core") {
		t.Fatalf("after the tag moved: %v", r)
	}

	// An image that is not there: the registry's error.
	gone := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": strings.TrimPrefix(rs.URL, "http://") + "/ee/missing:1"}), 201).id()
	if r := read(gone); r["status"] != "failed" || !strings.Contains(fmt.Sprint(r["error"]), "ee/missing:1 from its registry") {
		t.Fatalf("a missing image: %v", r)
	}
}

// The contents of a real execution environment, read from its registry
// (full.yml sets AWXNEXT_TEST_EE_IMAGE to quay.io/ansible/awx-ee).
func TestRealExecutionEnvironmentContents(t *testing.T) {
	slow(t)
	ref := os.Getenv("AWXNEXT_TEST_EE_IMAGE")
	if ref == "" {
		t.Skip("AWXNEXT_TEST_EE_IMAGE is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	c, err := eecontents.Read(ctx, ref, eecontents.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: ansible-core %s, Python %s, %d collections, ansible.cfg %d bytes", ref, c.AnsibleCore, c.Python, len(c.Collections), len(c.AnsibleCfg))
	if c.AnsibleCore == "" || c.Python == "" || len(c.Collections) < 5 {
		t.Fatalf("contents of %s: %+v", ref, c)
	}
}
