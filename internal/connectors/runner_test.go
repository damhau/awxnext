package connectors

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const fakeSource = `import asyncio, os

async def main(queue, args):
    for i in range(args.get("count", 3)):
        await queue.put({"i": i, "secret": open(args["file"]).read(), "env": os.environ.get("FAKE_ENV")})
    if args.get("fail"):
        raise RuntimeError("boom")
`

const fakeFilter = `def main(event, key, value):
    event[key] = value
    return event
`

// fakeCollection makes a collection test.fake with the source burst and
// the filter tag, and returns its collections path.
func fakeCollection(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "ansible_collections", "test", "fake", "extensions", "eda", "plugins")
	for _, f := range []struct{ dir, name, src string }{{"event_source", "burst.py", fakeSource}, {"event_filter", "tag.py", fakeFilter}} {
		if err := os.MkdirAll(filepath.Join(base, f.dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, f.dir, f.name), []byte(f.src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type fakeStream struct {
	mu       sync.Mutex
	refuse   int // requests answered 502 before taking events
	status   int
	requests int
	auth     []string
	events   []map[string]any
}

func (f *fakeStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	body, _ := io.ReadAll(r.Body)
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if f.refuse > 0 {
		f.refuse--
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	var batch []map[string]any
	if r.Header.Get("Content-Type") != "application/cloudevents-batch+json" || json.Unmarshal(body, &batch) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.events = append(f.events, batch...)
	w.WriteHeader(http.StatusAccepted)
}

func (f *fakeStream) received() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func runRunner(t *testing.T, base string, cfg Config) (string, int) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	cfg.Runner = Runner
	line, err := cfg.Line()
	if err != nil {
		t.Fatal(err)
	}
	argv := Argv("python3")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "ANSIBLE_COLLECTIONS_PATH="+fakeCollection(t))
	cmd.Stdin = bytes.NewReader(append(Site{BaseURL: base}.Line(), line...))
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return string(out), cmd.ProcessState.ExitCode()
}

func fakeConfig(t *testing.T) Config {
	return Config{Connector: 7, Name: "fake", PostPath: "/api/v2/event_streams/x/post/", Token: "tok-secret", Source: "test.fake.burst",
		Args:    map[string]any{"count": 5, "file": "@@AWXNEXT_FILE_0@@"},
		Filters: []map[string]any{{"test.fake.tag": map[string]any{"key": "tagged", "value": "yes"}}, {"eda.builtin.insert_meta_info": nil}},
		Env:     map[string]any{"FAKE_ENV": "from-env"}, Files: []File{{Placeholder: "@@AWXNEXT_FILE_0@@", Content: "file-content"}},
		Buffer: 10, Batch: 2}
}

// The runner posts each event once, in batches, with its filters and meta,
// after awxnext comes back, and keeps the token out of its log.
func TestRunnerPostsThroughOutage(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: it waits out the runner's backoff")
	}
	fs := &fakeStream{refuse: 2}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	out, rc := runRunner(t, srv.URL, fakeConfig(t))
	if rc != 0 {
		t.Fatalf("rc %d:\n%s", rc, out)
	}
	if strings.Contains(out, "tok-secret") || strings.Contains(out, "file-content") {
		t.Fatalf("the log shows a secret:\n%s", out)
	}
	if !strings.Contains(out, "buffering events") || !strings.Contains(out, "took events again") {
		t.Fatalf("log:\n%s", out)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.events) != 5 {
		t.Fatalf("events %d, want 5:\n%s", len(fs.events), out)
	}
	ids := map[string]bool{}
	for i, e := range fs.events {
		data := e["data"].(map[string]any)
		meta := data["meta"].(map[string]any)
		src := meta["source"].(map[string]any)
		if data["i"] != float64(i) || data["secret"] != "file-content" || data["env"] != "from-env" || data["tagged"] != "yes" ||
			src["name"] != "fake" || src["type"] != "test.fake.burst" || e["id"] != meta["uuid"] ||
			e["source"] != "/api/v2/event_connectors/7/" || e["type"] != "test.fake.burst" {
			t.Fatalf("event %d: %v", i, e)
		}
		ids[e["id"].(string)] = true
	}
	if len(ids) != 5 {
		t.Fatalf("ids %v", ids)
	}
	for _, a := range fs.auth {
		if a != "Connector tok-secret" {
			t.Fatalf("authorization %q", a)
		}
	}
	var last Status
	for _, l := range strings.Split(out, "\n") {
		if s, ok := ParseStatus(l); ok {
			last = s
		}
	}
	if last.State != "ended" || last.Sent != 5 || last.Buffered != 0 || last.Retries != 2 {
		t.Fatalf("last status %+v", last)
	}
}

func TestRunnerExits(t *testing.T) {
	fs := &fakeStream{}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	cfg := fakeConfig(t)
	cfg.Args["fail"] = true
	if out, rc := runRunner(t, srv.URL, cfg); rc != ExitPlugin || !strings.Contains(out, "RuntimeError: boom") || fs.received() != 5 {
		t.Fatalf("failing plugin: rc %d, %d events:\n%s", rc, fs.received(), out)
	}
	cfg = fakeConfig(t)
	cfg.Source = "test.fake.missing"
	if out, rc := runRunner(t, srv.URL, cfg); rc != ExitConfig || !strings.Contains(out, "no event_source plugin test.fake.missing") {
		t.Fatalf("missing plugin: rc %d:\n%s", rc, out)
	}
	fs.mu.Lock()
	fs.status = http.StatusUnauthorized
	fs.mu.Unlock()
	if out, rc := runRunner(t, srv.URL, fakeConfig(t)); rc != ExitRefused || !strings.Contains(out, "HTTP 401") {
		t.Fatalf("revoked token: rc %d:\n%s", rc, out)
	}
}

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"wss://awx.example.com/agent/v1/work/ctl/abc/": "https://awx.example.com",
		"http://10.0.0.1:8052/":                        "http://10.0.0.1:8052",
		"ws://svc:8052/agent/v1/connect/":              "http://svc:8052",
	} {
		if got := BaseURL(in); got != want {
			t.Errorf("BaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
