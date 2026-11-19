package kind

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const helloModule = `#!/usr/bin/python
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
'''
`

// The chart's install reads a collection's module docs in a pod of the
// execution environment, which the chart's NetworkPolicy selects, and
// deletes the pod after.
func TestCollectionDocsPod(t *testing.T) {
	a := connect(t, "AWXNEXT_TEST_KIND_URL")
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl is not installed")
	}
	selector := docsPolicySelector(t)
	pods := watchPods(t, selector)

	suffix := fmt.Sprint(time.Now().UnixNano())
	org := a.create("organizations/", map[string]any{"name": "kind-docs-" + suffix})
	ns := "kind" + suffix[len(suffix)-9:]
	nsID := a.create("collection_namespaces/", map[string]any{"name": ns, "organization": org, "auto_approve": true})
	_, tok, raw := a.call("POST", "tokens/", map[string]any{"description": "kind", "scope": "write"})
	token, _ := tok["token"].(string)
	if token == "" {
		t.Fatalf("token: %s", raw)
	}
	a.publish(token, collection(t, ns, "kit", map[string]string{"plugins/modules/hello.py": helloModule}))

	_, list, raw := a.call("GET", fmt.Sprintf("collection_versions/?namespace=%d", nsID), nil)
	results, _ := list["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("versions: %s", raw)
	}
	id := int64(results[0].(map[string]any)["id"].(float64))
	var docs map[string]any
	for deadline := time.Now().Add(10 * time.Minute); ; time.Sleep(2 * time.Second) {
		_, docs, raw = a.call("GET", fmt.Sprintf("collection_versions/%d/docs/", id), nil)
		if st := docs["state"]; st != "pending" && st != "reading" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the docs are still %v", docs["state"])
		}
	}
	if docs["state"] != "read" || !strings.Contains(fmt.Sprint(docs["message"]), "in a pod of the execution environment") {
		t.Fatalf("docs: %s", raw)
	}
	if _, _, hello := a.call("GET", fmt.Sprintf("collection_versions/%d/docs/?type=module&name=hello", id), nil); !strings.Contains(hello, "Who to greet.") {
		t.Fatalf("hello's docs: %s", hello)
	}

	label := fmt.Sprintf("awxnext-collection-docs=%d", id)
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		out, err := exec.Command("kubectl", "get", "pods", "-l", label, "-o", "name").CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl get pods: %v %s", err, out)
		}
		if strings.TrimSpace(string(out)) == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the docs pod is still there: %s", out)
		}
	}
	if seen := pods.named(fmt.Sprintf("collection-docs-%d-", id)); len(seen) != 1 {
		t.Fatalf("pods the NetworkPolicy selects (%s): %v", selector, pods.all())
	}
}

// docsPolicySelector is the label selector of the chart's NetworkPolicy
// for the docs pods.
func docsPolicySelector(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("kubectl", "get", "networkpolicy", "awxnext-collection-docs", "-o", "json").Output()
	if err != nil {
		t.Fatalf("the chart's NetworkPolicy: %v", err)
	}
	var np struct {
		Spec struct {
			PodSelector struct {
				MatchLabels      map[string]string `json:"matchLabels"`
				MatchExpressions []struct {
					Key, Operator string
					Values        []string
				} `json:"matchExpressions"`
			} `json:"podSelector"`
			PolicyTypes []string `json:"policyTypes"`
			Ingress     []any    `json:"ingress"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &np); err != nil {
		t.Fatal(err)
	}
	if strings.Join(np.Spec.PolicyTypes, ",") != "Ingress,Egress" || len(np.Spec.Ingress) != 0 {
		t.Fatalf("the NetworkPolicy doesn't deny ingress and limit egress: %s", out)
	}
	var terms []string
	for k, v := range np.Spec.PodSelector.MatchLabels {
		terms = append(terms, k+"="+v)
	}
	for _, e := range np.Spec.PodSelector.MatchExpressions {
		switch e.Operator {
		case "Exists":
			terms = append(terms, e.Key)
		case "In":
			terms = append(terms, fmt.Sprintf("%s in (%s)", e.Key, strings.Join(e.Values, ",")))
		default:
			t.Fatalf("selector operator %s", e.Operator)
		}
	}
	sort.Strings(terms)
	if len(terms) == 0 {
		t.Fatalf("the NetworkPolicy selects every pod: %s", out)
	}
	return strings.Join(terms, ",")
}

// podWatch records the names of the pods a selector matched while the test
// ran: a docs pod lives for seconds.
type podWatch struct {
	mu    sync.Mutex
	names map[string]bool
}

func watchPods(t *testing.T, selector string) *podWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-l", selector, "--watch", "-o", "name")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w := &podWatch{names: map[string]bool{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s := bufio.NewScanner(stdout)
		for s.Scan() {
			if name := strings.TrimPrefix(strings.TrimSpace(s.Text()), "pod/"); name != "" {
				w.mu.Lock()
				w.names[name] = true
				w.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		cmd.Wait()
	})
	return w
}

func (w *podWatch) named(prefix string) []string {
	var names []string
	for _, n := range w.all() {
		if strings.HasPrefix(n, prefix) {
			names = append(names, n)
		}
	}
	return names
}

func (w *podWatch) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	names := make([]string, 0, len(w.names))
	for n := range w.names {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// publish uploads an artifact as ansible-galaxy collection publish does;
// the hub imports it before it answers.
func (a *api) publish(token string, data []byte) {
	a.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	sum := sha256.Sum256(data)
	mw.WriteField("sha256", hex.EncodeToString(sum[:]))
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="collection.tar.gz"`)
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Transfer-Encoding", "base64")
	fw, _ := mw.CreatePart(h)
	fw.Write([]byte(base64.StdEncoding.EncodeToString(data)))
	mw.Close()
	galaxy := func(method, path string, rd io.Reader, contentType string) map[string]any {
		req, _ := http.NewRequest(method, a.base+path, rd)
		req.Header.Set("Authorization", "Token "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			a.t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var m map[string]any
		json.Unmarshal(raw, &m)
		if res.StatusCode >= 300 {
			a.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
		return m
	}
	task, _ := galaxy("POST", "/api/galaxy/v3/artifacts/collections/", &body, mw.FormDataContentType())["task"].(string)
	if imp := galaxy("GET", task, nil, ""); imp["state"] != "completed" {
		a.t.Fatalf("import: %v", imp)
	}
}

// collection builds an artifact as ansible-galaxy collection build does:
// MANIFEST.json, FILES.json with each file's checksum, and the files.
func collection(t *testing.T, ns, name string, files map[string]string) []byte {
	t.Helper()
	files["README.md"] = "# " + ns + "." + name + "\n"
	files["meta/runtime.yml"] = "requires_ansible: '>=2.15.0'\n"
	dirs := map[string]bool{}
	var names []string
	for n := range files {
		names = append(names, n)
		for d := n; strings.Contains(d, "/"); {
			d = d[:strings.LastIndex(d, "/")]
			dirs[d] = true
		}
	}
	for d := range dirs {
		names = append(names, d+"/")
	}
	sort.Strings(names)
	entries := []map[string]any{{"name": ".", "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1}}
	for _, n := range names {
		if d, ok := strings.CutSuffix(n, "/"); ok {
			entries = append(entries, map[string]any{"name": d, "ftype": "dir", "chksum_type": nil, "chksum_sha256": nil, "format": 1})
			continue
		}
		sum := sha256.Sum256([]byte(files[n]))
		entries = append(entries, map[string]any{"name": n, "ftype": "file", "chksum_type": "sha256", "chksum_sha256": hex.EncodeToString(sum[:]), "format": 1})
	}
	filesJSON, _ := json.Marshal(map[string]any{"files": entries, "format": 1})
	filesSum := sha256.Sum256(filesJSON)
	manifest, _ := json.Marshal(map[string]any{
		"collection_info": map[string]any{"namespace": ns, "name": name, "version": "1.0.0", "authors": []string{"awxnext tests"},
			"readme": "README.md", "tags": []string{"test"}, "description": "A test collection", "license": []string{"GPL-3.0-or-later"},
			"license_file": nil, "dependencies": map[string]any{}, "repository": nil, "documentation": nil, "homepage": nil, "issues": nil},
		"file_manifest_file": map[string]any{"name": "FILES.json", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": hex.EncodeToString(filesSum[:]), "format": 1},
		"format":             1,
	})
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Now()}); err != nil {
			t.Fatal(err)
		}
		tw.Write(data)
	}
	add("MANIFEST.json", manifest)
	add("FILES.json", filesJSON)
	for _, n := range names {
		if d, ok := strings.CutSuffix(n, "/"); ok {
			tw.WriteHeader(&tar.Header{Name: d, Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Now()})
			continue
		}
		add(n, []byte(files[n]))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
