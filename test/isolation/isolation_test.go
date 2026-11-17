// Package isolation tests job users on the all-in-one image (CI's compose
// job): AWXNEXT_TEST_ISOLATION_URL is an awxnext started from
// deploy/compose, AWXNEXT_TEST_ISOLATION_PASSWORD its admin password.
package isolation

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/hubsign"
	"github.com/damhau/awxnext/internal/jobexec"
)

type api struct {
	t    *testing.T
	base string
	pass string
}

func (a *api) call(method, path string, body any) (int, map[string]any, string) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.base+"/api/v2/"+path, rd)
	req.SetBasicAuth("admin", a.pass)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err.Error()
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return res.StatusCode, m, string(raw)
}

func (a *api) create(path string, body any) int64 {
	a.t.Helper()
	status, m, raw := a.call("POST", path, body)
	if status != 201 {
		a.t.Fatalf("POST %s: %d %s", path, status, raw)
	}
	return int64(m["id"].(float64))
}

// wait polls a unified job until it finishes.
func (a *api) wait(path string, timeout time.Duration) map[string]any {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, m, _ := a.call("GET", path, nil)
		switch m["status"] {
		case "successful", "failed", "error", "canceled":
			return m
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("%s did not finish: %v", path, m["status"])
		}
		time.Sleep(time.Second)
	}
}

func (a *api) stdout(path string) string {
	_, _, out := a.call("GET", path+"stdout/?format=txt", nil)
	return out
}

// A job, while another runs, cannot find the other's private data (its
// extra vars, in its directory and its processes'), cannot list the jobs'
// directories, and cannot write to a project checkout nor list them. Both
// run as job users, each its own.
func TestJobsIsolated(t *testing.T) {
	base := os.Getenv("AWXNEXT_TEST_ISOLATION_URL")
	if base == "" {
		t.Skip("AWXNEXT_TEST_ISOLATION_URL is not set (CI's compose job)")
	}
	a := &api{t: t, base: strings.TrimRight(base, "/"), pass: os.Getenv("AWXNEXT_TEST_ISOLATION_PASSWORD")}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		if status, _, _ := a.call("GET", "ping/", nil); status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("awxnext is not answering")
		}
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	org := a.create("organizations/", map[string]any{"name": "isolation-" + suffix})
	inv := a.create("inventories/", map[string]any{"name": "isolation-" + suffix, "organization": org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"})
	a.create("hosts/", map[string]any{"name": "localhost", "inventory": inv})
	project := a.create("projects/", map[string]any{"name": "isolation-" + suffix, "organization": org, "scm_type": "git",
		"scm_url": "https://github.com/ansible/ansible-tower-samples"})
	var checkout string
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(2 * time.Second) {
		_, p, _ := a.call("GET", fmt.Sprintf("projects/%d/", project), nil)
		if p["status"] == "successful" {
			checkout = filepath.Join("/var/lib/awxnext/projects", fmt.Sprint(p["local_path"]))
			break
		}
		if p["status"] == "failed" || p["status"] == "error" || time.Now().After(deadline) {
			t.Fatalf("project sync: %v", p["status"])
		}
	}

	// The holder's extra vars are in its private data directory while it
	// sleeps.
	holder := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell",
		"module_args": "id -un; sleep 40", "extra_vars": fmt.Sprintf(`{"isolation_secret": "holder-secret-%s"}`, suffix)})
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		_, h, _ := a.call("GET", fmt.Sprintf("ad_hoc_commands/%d/", holder), nil)
		_, ev, _ := a.call("GET", fmt.Sprintf("ad_hoc_commands/%d/events/?event=runner_on_start", holder), nil)
		if h["status"] == "running" && ev["count"] != nil && ev["count"].(float64) > 0 {
			break
		}
		if h["status"] != "pending" && h["status"] != "waiting" && h["status"] != "running" || time.Now().After(deadline) {
			t.Fatalf("holder: %v", h["status"])
		}
	}
	// The secret is put together at run time: the prober's own command
	// holds its two halves only (and the objects' names, in every job's
	// extra vars, are isolation-<suffix>).
	script := strings.Join([]string{
		"id -un",
		"m=holder-secret-; m=${m}" + suffix,
		`echo "found: $(grep -rl "$m" /var/lib/awxnext /proc/[0-9]*/environ /proc/[0-9]*/cwd/ /tmp 2>/dev/null | head -3 | tr '\n' ' ')"`,
		`echo "list jobs: $(ls /var/lib/awxnext/jobs 2>&1 | head -1)"`,
		`echo "list work: $(ls /var/lib/awxnext/jobs/work 2>&1 | head -1)"`,
		`echo "list projects: $(ls /var/lib/awxnext/projects 2>&1 | head -1)"`,
		fmt.Sprintf(`echo "write checkout: $(touch %s/written 2>&1 && echo written)"`, checkout),
		fmt.Sprintf(`echo "read checkout: $(cat %s/hello_world.yml 2>&1 | head -1)"`, checkout),
	}, "\n")
	prober := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell", "module_args": script})
	p := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", prober), 3*time.Minute)
	out := a.stdout(fmt.Sprintf("ad_hoc_commands/%d/", prober))
	if p["status"] != "successful" {
		t.Fatalf("prober: %v\n%s", p["status"], out)
	}
	line := func(prefix string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(l, prefix))
			}
		}
		t.Fatalf("no %q in:\n%s", prefix, out)
		return ""
	}
	if found := line("found:"); found != "" {
		t.Errorf("the prober found the holder's extra vars: %s", found)
	}
	for _, l := range []string{"list jobs:", "list work:", "list projects:", "write checkout:", "read checkout:"} {
		if got := line(l); !strings.Contains(got, "Permission denied") {
			t.Errorf("%s %s (want Permission denied)", l, got)
		}
	}
	h := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", holder), 3*time.Minute)
	hout := a.stdout(fmt.Sprintf("ad_hoc_commands/%d/", holder))
	if h["status"] != "successful" {
		t.Fatalf("holder: %v\n%s", h["status"], hout)
	}
	user := regexp.MustCompile(`(?m)^(awxjob\d+)\s*$`)
	jobUser := func(s string) string {
		if m := user.FindStringSubmatch(s); m != nil {
			return m[1]
		}
		return ""
	}
	hu, pu := jobUser(hout), jobUser(out)
	if hu == "" || pu == "" || hu == pu {
		t.Errorf("job users: holder %q, prober %q\n%s\n%s", hu, pu, hout, out)
	}
}

// A canceled job leaves nothing running, not even a process it moved out
// of its session: the helper kills all of the job user's processes.
func TestCanceledJobLeavesNoProcess(t *testing.T) {
	base := os.Getenv("AWXNEXT_TEST_ISOLATION_URL")
	if base == "" {
		t.Skip("AWXNEXT_TEST_ISOLATION_URL is not set (CI's compose job)")
	}
	a := &api{t: t, base: strings.TrimRight(base, "/"), pass: os.Getenv("AWXNEXT_TEST_ISOLATION_PASSWORD")}
	suffix := fmt.Sprint(time.Now().UnixNano())
	org := a.create("organizations/", map[string]any{"name": "cancel-" + suffix})
	inv := a.create("inventories/", map[string]any{"name": "cancel-" + suffix, "organization": org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"})
	a.create("hosts/", map[string]any{"name": "localhost", "inventory": inv})
	sleeper := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell",
		"module_args": "(setsid sleep 300 > /dev/null 2>&1 &); sleep 300"})
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		_, s, _ := a.call("GET", fmt.Sprintf("ad_hoc_commands/%d/", sleeper), nil)
		_, ev, _ := a.call("GET", fmt.Sprintf("ad_hoc_commands/%d/events/?event=runner_on_start", sleeper), nil)
		if s["status"] == "running" && ev["count"] != nil && ev["count"].(float64) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sleeper: %v", s["status"])
		}
	}
	time.Sleep(2 * time.Second)
	if status, _, raw := a.call("POST", fmt.Sprintf("ad_hoc_commands/%d/cancel/", sleeper), nil); status != 202 {
		t.Fatalf("cancel: %d %s", status, raw)
	}
	if s := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", sleeper), 2*time.Minute); s["status"] != "canceled" {
		t.Fatalf("sleeper: %v", s["status"])
	}
	// The other job users' processes, seen from a job (status is readable);
	// counted again for a while, the cancel finishing in the background.
	count := `me=$(id -u); n=0; for s in /proc/[0-9]*/status; do u=$(awk '/^Uid:/ {print $2}' "$s" 2>/dev/null); ` +
		`if [ -n "$u" ] && [ "$u" -ge 50000 ] && [ "$u" -lt 50128 ] && [ "$u" != "$me" ]; then n=$((n+1)); fi; done; echo "others: $n"`
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) {
		counter := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell", "module_args": count})
		c := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", counter), 2*time.Minute)
		out := a.stdout(fmt.Sprintf("ad_hoc_commands/%d/", counter))
		if c["status"] != "successful" {
			t.Fatalf("counter: %v\n%s", c["status"], out)
		}
		if strings.Contains(out, "others: 0") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes of other job users after the cancel:\n%s", out)
		}
	}
}

// gpg is in the image and verifies, as a job user with a home of its own,
// a signature and keyring made as the hub makes them: project syncs check
// collection signatures with it.
func TestGPGVerifiesAsJobUser(t *testing.T) {
	base := os.Getenv("AWXNEXT_TEST_ISOLATION_URL")
	if base == "" {
		t.Skip("AWXNEXT_TEST_ISOLATION_URL is not set (CI's compose job)")
	}
	a := &api{t: t, base: strings.TrimRight(base, "/"), pass: os.Getenv("AWXNEXT_TEST_ISOLATION_PASSWORD")}
	key, err := hubsign.Generate("isolation", "")
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"collection_info": {"namespace": "isolation"}}`)
	sig, err := hubsign.Sign(key.PrivateKey, manifest)
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := hubsign.Keyring(key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString
	script := strings.Join([]string{
		`export GNUPGHOME=$(mktemp -d)`,
		fmt.Sprintf(`echo %s | base64 -d > $GNUPGHOME/hub.gpg`, b64(keyring)),
		fmt.Sprintf(`echo %s | base64 -d > $GNUPGHOME/MANIFEST.json`, b64(manifest)),
		fmt.Sprintf(`echo %s | base64 -d | gpg --status-fd 1 --verify --batch --no-tty --no-default-keyring --keyring=$GNUPGHOME/hub.gpg - $GNUPGHOME/MANIFEST.json`, b64([]byte(sig))),
	}, "\n")
	suffix := fmt.Sprint(time.Now().UnixNano())
	org := a.create("organizations/", map[string]any{"name": "gpg-" + suffix})
	inv := a.create("inventories/", map[string]any{"name": "gpg-" + suffix, "organization": org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"})
	a.create("hosts/", map[string]any{"name": "localhost", "inventory": inv})
	cmd := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell", "module_args": script})
	c := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", cmd), 3*time.Minute)
	out := a.stdout(fmt.Sprintf("ad_hoc_commands/%d/", cmd))
	if c["status"] != "successful" || !strings.Contains(out, "VALIDSIG "+key.Fingerprint) {
		t.Fatalf("gpg as a job user: %v\n%s", c["status"], out)
	}
}

// collectionTarball is a collection artifact holding files, with the
// MANIFEST.json and FILES.json ansible-galaxy writes.
func collectionTarball(t *testing.T, ns, name string, files map[string]string) []byte {
	t.Helper()
	files["README.md"] = "# " + ns + "." + name + "\n"
	files["meta/runtime.yml"] = "requires_ansible: '>=2.15.0'\n"
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	sum := func(b []byte) string {
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	entries := []map[string]any{{"name": ".", "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1}}
	for _, n := range names {
		entries = append(entries, map[string]any{"name": n, "ftype": "file", "chksum_type": "sha256", "chksum_sha256": sum([]byte(files[n])), "format": 1})
	}
	filesJSON, _ := json.Marshal(map[string]any{"files": entries, "format": 1})
	manifest, _ := json.Marshal(map[string]any{
		"collection_info": map[string]any{"namespace": ns, "name": name, "version": "1.0.0", "authors": []string{"awxnext tests"},
			"readme": "README.md", "tags": []string{}, "description": "", "license": []string{"GPL-3.0-or-later"}, "dependencies": map[string]any{}},
		"file_manifest_file": map[string]any{"name": "FILES.json", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": sum(filesJSON), "format": 1},
		"format":             1,
	})
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Now()})
		tw.Write(data)
	}
	add("MANIFEST.json", manifest)
	add("FILES.json", filesJSON)
	for _, n := range names {
		add(n, []byte(files[n]))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// The docs of a collection's modules are read by ansible-doc as a job
// user: the code ansible-doc imports from the collection (a doc fragment)
// doesn't run as the server's user.
func TestCollectionDocsAsJobUser(t *testing.T) {
	base := os.Getenv("AWXNEXT_TEST_ISOLATION_URL")
	if base == "" {
		t.Skip("AWXNEXT_TEST_ISOLATION_URL is not set (CI's compose job)")
	}
	a := &api{t: t, base: strings.TrimRight(base, "/"), pass: os.Getenv("AWXNEXT_TEST_ISOLATION_PASSWORD")}
	suffix := fmt.Sprint(time.Now().UnixNano())
	org := a.create("organizations/", map[string]any{"name": "docs-" + suffix})
	ns := "docs" + suffix
	a.create("collection_namespaces/", map[string]any{"name": ns, "organization": org, "auto_approve": true})
	data := collectionTarball(t, ns, "kit", map[string]string{
		"plugins/modules/hello.py":       fmt.Sprintf("DOCUMENTATION = r'''\nmodule: hello\nshort_description: Says hello\ndescription: Says hello.\nextends_documentation_fragment:\n  - %s.kit.where\n'''\n", ns),
		"plugins/doc_fragments/where.py": "import os\nclass ModuleDocFragment(object):\n    DOCUMENTATION = r'''\noptions:\n  uid:\n    description: '%d'\n    type: int\n''' % os.getuid()\n",
	})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	sum := sha256.Sum256(data)
	mw.WriteField("sha256", hex.EncodeToString(sum[:]))
	fw, _ := mw.CreateFormFile("file", ns+"-kit-1.0.0.tar.gz")
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", a.base+"/api/galaxy/v3/artifacts/collections/", &body)
	req.SetBasicAuth("admin", a.pass)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("upload: %d %s", res.StatusCode, raw)
	}
	_, versions, _ := a.call("GET", "collection_versions/?search="+ns, nil)
	if versions["count"] != float64(1) {
		t.Fatalf("versions: %v", versions)
	}
	id := int64(versions["results"].([]any)[0].(map[string]any)["id"].(float64))
	deadline := time.Now().Add(5 * time.Minute)
	var docs map[string]any
	for {
		_, docs, _ = a.call("GET", fmt.Sprintf("collection_versions/%d/docs/", id), nil)
		if st := docs["state"]; st != "pending" && st != "reading" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("docs: %v", docs)
		}
		time.Sleep(time.Second)
	}
	if docs["state"] != "read" || !strings.Contains(fmt.Sprint(docs["message"]), "as job user "+jobexec.Prefix) {
		t.Fatalf("docs: %v", docs)
	}
	_, hello, raw2 := a.call("GET", fmt.Sprintf("collection_versions/%d/docs/?type=module&name=hello", id), nil)
	doc, _ := hello["doc"].(map[string]any)
	inner, _ := doc["doc"].(map[string]any)
	options, _ := inner["options"].(map[string]any)
	uidOpt, _ := options["uid"].(map[string]any)
	var uid int
	if _, err := fmt.Sscan(fmt.Sprint(uidOpt["description"]), &uid); err != nil || uid < jobexec.MinUID {
		t.Fatalf("the fragment ran as uid %v: %s", uidOpt["description"], raw2)
	}
}
