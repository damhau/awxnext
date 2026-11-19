package test

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/resources"
)

// waitDocs waits until the hub has read a version's docs, or found it
// can't.
func waitDocs(t *testing.T, a *client, id int64) resp {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		r := must(t, a.get(fmt.Sprintf("collection_versions/%d/docs/", id)), 200)
		if st := r.Body["state"]; st != "pending" && st != "reading" {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("the docs are still %v", r.Body["state"])
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func hubDocsMode(t *testing.T, a *client, mode string) {
	t.Helper()
	must(t, a.patch("settings/system/", map[string]any{"HUB_COLLECTION_DOCS": mode}), 200)
	t.Cleanup(func() { a.patch("settings/system/", map[string]any{"HUB_COLLECTION_DOCS": "isolated"}) })
}

func importMessages(t *testing.T, a *client, version int64) string {
	t.Helper()
	imp := must(t, a.get(fmt.Sprintf("collection_imports/?collection_version=%d", version)), 200).results()[0]
	return fmt.Sprint(imp["messages"])
}

// TestHubDocsNotRead: where ansible-doc can't run, or may not, the version
// has its README but no docs, and its page says why; the import is not
// failed for it.
func TestHubDocsNotRead(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ns := galaxyName("nodocs")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org}), 201).id()
	owner, ownerID := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Admin", ownerID, nsID)
	uploader, uploaderID := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Upload", uploaderID, nsID)
	tok := writePAT(t, uploader)
	if imp := publish(t, tok, collectionTarball(t, ns, "kit", "1.0.0", nil, map[string]string{"plugins/modules/m.py": "DOCUMENTATION = ''\n"})); imp["state"] != "completed" {
		t.Fatalf("upload: %v", imp)
	}
	id := versionID(t, ns, "kit", "1.0.0")

	// The tests' server runs jobs as its own user, without job users.
	d := waitDocs(t, owner, id)
	if d.Body["state"] != "none" || !strings.Contains(fmt.Sprint(d.Body["message"]), "HUB_COLLECTION_DOCS") {
		t.Fatalf("docs: %s", d.Raw)
	}
	if r := d.Body["readme"].(map[string]any); r["name"] != "README.md" || r["text"] != "# "+ns+".kit\n" {
		t.Errorf("readme: %v", r)
	}
	if len(d.Body["contents"].([]any)) != 0 {
		t.Errorf("contents: %v", d.Body["contents"])
	}
	if msgs := importMessages(t, owner, id); !strings.Contains(msgs, "Docs: Not read.") {
		t.Errorf("the import's log: %s", msgs)
	}

	// Reading them again is for the namespace's curators.
	must(t, uploader.post(fmt.Sprintf("collection_versions/%d/read_docs/", id), nil), 403)
	hubDocsMode(t, a, "server_user")
	// A node without ansible-doc.
	bin := t.TempDir()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sh, filepath.Join(bin, "sh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	must(t, owner.post(fmt.Sprintf("collection_versions/%d/read_docs/", id), nil), 202)
	d = waitDocs(t, owner, id)
	if d.Body["state"] != "none" || !strings.Contains(fmt.Sprint(d.Body["message"]), "ansible-doc isn't installed on node test-node as the server's user") {
		t.Fatalf("docs without ansible-doc: %s", d.Raw)
	}
	if imp := must(t, owner.get(fmt.Sprintf("collection_imports/?collection_version=%d", id)), 200).results()[0]; imp["state"] != "completed" {
		t.Errorf("the import: %v", imp)
	}

	// galaxy_ng's docs-blob, once the version is published.
	must(t, owner.post(fmt.Sprintf("collection_versions/%d/approve/", id), nil), 200)
	status, blob := galaxyCall(t, "GET", tok, fmt.Sprintf("/api/galaxy/v3/plugin/ansible/content/published/collections/index/%s/kit/versions/1.0.0/docs-blob/", ns), nil)
	readme, _ := blob["docs_blob"].(map[string]any)["collection_readme"].(map[string]any)
	if status != 200 || readme["name"] != "README.md" || readme["html"] != "<pre># "+ns+".kit\n</pre>" {
		t.Fatalf("docs-blob: %d %v", status, blob)
	}
}

const docsModule = `#!/usr/bin/python
DOCUMENTATION = r'''
module: hello
short_description: Says hello
description:
  - Says hello to someone.
options:
  name:
    description: Who to greet.
    type: str
    required: true
extends_documentation_fragment:
  - %s.common
author: awxnext tests
'''
EXAMPLES = r'''
- %s.hello:
    name: world
'''
RETURN = r'''
greeting:
  description: The greeting.
  returned: always
  type: str
'''
`

// The fragment's code runs when ansible-doc reads the module: it says
// where.
const docsFragment = `import os
import tempfile
class ModuleDocFragment(object):
    DOCUMENTATION = r'''
options:
  where:
    description: Read as uid %d in %s.
    type: str
''' % (os.getuid(), tempfile.gettempdir())
`

// TestHubDocs: ansible-doc reads the docs of a version's modules, plugins
// and roles after its import, in a temporary directory removed after;
// they are served on /api/v2/ and in galaxy_ng's docs-blob.
func TestHubDocs(t *testing.T) {
	requireAnsible(t)
	if _, err := exec.LookPath("ansible-doc"); err != nil {
		t.Skip("ansible-doc not installed")
	}
	a := adminClient(t)
	hubDocsMode(t, a, "server_user")
	org := newOrg(t, a)
	ns := galaxyName("docs")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org, "auto_approve": true}), 201).id()
	reader, readerID := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Read", readerID, nsID)
	tmp := os.TempDir()
	before, _ := filepath.Glob(filepath.Join(tmp, "awxnext-docs-*"))
	tok := writePAT(t, a)
	files := map[string]string{
		"plugins/modules/hello.py":        fmt.Sprintf(docsModule, ns+".kit", ns+".kit"),
		"plugins/modules/broken.py":       "DOCUMENTATION = r'''\nmodule: broken\nshort_description: [unclosed\n'''\n",
		"plugins/doc_fragments/common.py": docsFragment,
		"plugins/lookup/things.py": `DOCUMENTATION = r'''
name: things
short_description: Looks things up
description: Looks things up.
'''
from ansible.plugins.lookup import LookupBase
class LookupModule(LookupBase):
    def run(self, terms, variables=None, **kwargs):
        return terms
`,
		"roles/web/meta/argument_specs.yml": "argument_specs:\n  main:\n    short_description: Sets up a web server\n    options:\n      port:\n        type: int\n        description: The port.\n",
		"roles/web/tasks/main.yml":          "- ansible.builtin.debug:\n    msg: hi\n",
	}
	if imp := publish(t, tok, collectionTarball(t, ns, "kit", "1.0.0", nil, files)); imp["state"] != "completed" {
		t.Fatalf("upload: %v", imp)
	}
	id := versionID(t, ns, "kit", "1.0.0")
	d := waitDocs(t, reader, id)
	if d.Body["state"] != "read" || !strings.Contains(fmt.Sprint(d.Body["message"]), "ansible-doc read 3 modules and plugins on node test-node as the server's user; 1 have docs it couldn't read") {
		t.Fatalf("docs: %s", d.Raw)
	}
	index := map[string]map[string]any{}
	for _, c := range d.Body["contents"].([]any) {
		c := c.(map[string]any)
		index[fmt.Sprint(c["type"], "/", c["name"])] = c
	}
	if len(index) != 4 || index["module/hello"]["short_description"] != "Says hello" || index["lookup/things"]["short_description"] != "Looks things up" ||
		index["role/web"]["short_description"] != "Sets up a web server" || index["module/broken"]["error"] == "" {
		t.Fatalf("contents: %v", d.Body["contents"])
	}
	if e := fmt.Sprint(index["module/broken"]["error"]); !strings.Contains(e, "plugins/modules/broken.py") || strings.Contains(e, "awxnext-docs-") {
		t.Errorf("the broken module's error names its file in the collection, not the temporary directory: %s", e)
	}
	hello := must(t, reader.get(fmt.Sprintf("collection_versions/%d/docs/?type=module&name=hello", id)), 200)
	doc := hello.Body["doc"].(map[string]any)
	options := doc["doc"].(map[string]any)["options"].(map[string]any)
	if options["name"].(map[string]any)["description"] != "Who to greet." || !strings.Contains(fmt.Sprint(doc["examples"]), ns+".kit.hello") ||
		doc["return"].(map[string]any)["greeting"] == nil {
		t.Fatalf("hello's docs: %s", hello.Raw)
	}
	where := fmt.Sprint(options["where"].(map[string]any)["description"])
	if !strings.Contains(where, fmt.Sprintf("uid %d in ", os.Getuid())) {
		t.Errorf("the fragment ran: %s", where)
	}
	must(t, reader.get(fmt.Sprintf("collection_versions/%d/docs/?type=module&name=nothing", id)), 404)
	if msgs := importMessages(t, a, id); !strings.Contains(msgs, "Docs: ansible-doc read 3") {
		t.Errorf("the import's log: %s", msgs)
	}
	after, _ := filepath.Glob(filepath.Join(tmp, "awxnext-docs-*"))
	if len(after) != len(before) {
		t.Errorf("temporary directories left: %v, before %v", after, before)
	}

	status, blob := galaxyCall(t, "GET", writePAT(t, reader), fmt.Sprintf("/api/galaxy/v3/plugin/ansible/content/published/collections/index/%s/kit/versions/1.0.0/docs-blob/", ns), nil)
	if status != 200 {
		t.Fatalf("docs-blob: %d %v", status, blob)
	}
	found := false
	for _, c := range blob["docs_blob"].(map[string]any)["contents"].([]any) {
		c := c.(map[string]any)
		if c["content_type"] == "module" && c["content_name"] == "hello" {
			found = c["doc_strings"].(map[string]any)["doc"].(map[string]any)["short_description"] == "Says hello"
		}
		if c["content_name"] == "broken" {
			t.Errorf("docs-blob lists the module whose docs couldn't be read: %v", c)
		}
	}
	if !found {
		t.Errorf("docs-blob: %v", blob)
	}
}

// TestHubDocsPod: a node in a Kubernetes cluster, which runs jobs neither in
// containers nor as job users, reads docs in a pod of the execution
// environment, as a container group runs a job, and deletes the pod after.
func TestHubDocsPod(t *testing.T) {
	requireAnsible(t)
	if _, err := exec.LookPath("ansible-doc"); err != nil {
		t.Skip("ansible-doc not installed")
	}
	a := adminClient(t)
	k := &fakeKube{t: t, pods: map[string]map[string]any{}, dir: t.TempDir()}
	kube := httptest.NewServer(k)
	t.Cleanup(kube.Close)
	resources.DocsPodCredential = &agent.KubeCredential{Host: kube.URL, BearerToken: "kube-token"}
	t.Cleanup(func() { resources.DocsPodCredential = nil })
	org := newOrg(t, a)
	ns := galaxyName("poddocs")
	must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org, "auto_approve": true}), 201)
	files := map[string]string{
		"plugins/modules/hello.py":        fmt.Sprintf(docsModule, ns+".kit", ns+".kit"),
		"plugins/doc_fragments/common.py": docsFragment,
	}
	if imp := publish(t, writePAT(t, a), collectionTarball(t, ns, "kit", "1.0.0", nil, files)); imp["state"] != "completed" {
		t.Fatalf("upload: %v", imp)
	}
	id := versionID(t, ns, "kit", "1.0.0")
	d := waitDocs(t, a, id)
	if d.Body["state"] != "read" || !strings.HasPrefix(fmt.Sprint(d.Body["message"]), "ansible-doc read 1 modules and plugins in a pod of the execution environment quay.io/ansible/awx-ee:latest") {
		t.Fatalf("docs: %s", d.Raw)
	}
	hello := must(t, a.get(fmt.Sprintf("collection_versions/%d/docs/?type=module&name=hello", id)), 200)
	if !strings.Contains(hello.Raw, "Who to greet.") {
		t.Fatalf("hello's docs: %s", hello.Raw)
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.pods) != 1 {
		t.Fatalf("pods: %v", k.pods)
	}
	for name, pod := range k.pods {
		meta := pod["metadata"].(map[string]any)
		spec := pod["spec"].(map[string]any)
		worker := spec["containers"].([]any)[0].(map[string]any)
		if meta["generateName"] != fmt.Sprintf("collection-docs-%d-", id) || meta["labels"].(map[string]any)[agent.DocsPodLabel] != fmt.Sprint(id) ||
			worker["image"] != "quay.io/ansible/awx-ee:latest" || spec["automountServiceAccountToken"] != false || spec["restartPolicy"] != "Never" {
			t.Fatalf("pod: %v", pod)
		}
		if len(k.deleted) != 1 || k.deleted[0] != name {
			t.Fatalf("pod %s deleted: %v", name, k.deleted)
		}
	}
}
