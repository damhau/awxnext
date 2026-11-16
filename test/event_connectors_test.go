package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/resources"
)

// Sources of a fake collection, test.fake: burst puts count events (with
// what its credential gave it) then ends, or fails; tick puts an event every
// delay seconds until stopped.
const fakeBurst = `import asyncio, os

async def main(queue, args):
    for i in range(args.get("count", 3)):
        e = {"i": i}
        if "file" in args:
            e["secret"] = open(args["file"]).read()
        if args.get("env"):
            e["env"] = os.environ.get(args["env"])
        if "password" in args:
            e["password_set"] = bool(args["password"])
        await queue.put(e)
    if args.get("fail"):
        raise RuntimeError("boom")
`

const fakeTick = `import asyncio

async def main(queue, args):
    i = 0
    while True:
        await queue.put({"i": i})
        i += 1
        await asyncio.sleep(args.get("delay", 0.2))
`

// requireConnectors: the test runs connectors with python3 and a fake
// collection, on ANSIBLE_COLLECTIONS_PATH with extra, if any (slow).
func requireConnectors(t *testing.T, extra ...string) {
	t.Helper()
	slow(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "ansible_collections", "test", "fake", "extensions", "eda", "plugins", "event_source")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{"burst.py": fakeBurst, "tick.py": fakeTick} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ANSIBLE_COLLECTIONS_PATH", strings.Join(append([]string{root}, extra...), ":"))
}

// requireEDA: the test runs ansible.eda's plugins (slow). The collection
// is looked for on ANSIBLE_COLLECTIONS_PATH, then in
// ~/.cache/awxnext/collections (ansible-galaxy collection install
// ansible.eda -p ~/.cache/awxnext/collections), and its Python is
// AWXNEXT_TEST_EDA_PYTHON or python3.
func requireEDA(t *testing.T) string {
	t.Helper()
	slow(t)
	home, _ := os.UserHomeDir()
	paths := append(filepath.SplitList(os.Getenv("ANSIBLE_COLLECTIONS_PATH")), filepath.Join(home, ".cache", "awxnext", "collections"))
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(p, "ansible_collections", "ansible", "eda", "extensions", "eda", "plugins", "event_source", "range.py")); err == nil {
			py := edaPython()
			if err := exec.Command(py, "-c", "import yaml").Run(); err != nil {
				t.Skipf("%s has no PyYAML, which ansible.eda's sources need", py)
			}
			requireConnectors(t, p)
			return py
		}
	}
	t.Skip("ansible.eda is not installed")
	return ""
}

func edaPython() string {
	if py := os.Getenv("AWXNEXT_TEST_EDA_PYTHON"); py != "" {
		return py
	}
	return "python3"
}

type connectorFixture struct {
	a      *client
	org    int64
	stream eventStream
}

func newConnectorFixture(t *testing.T) *connectorFixture {
	t.Helper()
	a := adminClient(t)
	org := newOrg(t, a)
	cred := streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"})
	return &connectorFixture{a: a, org: org, stream: newEventStream(t, a, org, cred, eventstreams.TypeGeneric, nil)}
}

func (f *connectorFixture) connector(t *testing.T, body map[string]any) int64 {
	t.Helper()
	b := map[string]any{"name": uniq("conn"), "organization": f.org, "event_stream": f.stream.id, "restart_policy": "never"}
	for k, v := range body {
		b[k] = v
	}
	id := must(t, f.a.post("event_connectors/", b), 201).id()
	t.Cleanup(func() { f.a.patch(fmt.Sprintf("event_connectors/%d/", id), map[string]any{"enabled": false}) })
	return id
}

func waitConnector(t *testing.T, a *client, id int64, what string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = must(t, a.get(fmt.Sprintf("event_connectors/%d/", id)), 200).Body
		if ok(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	runs := a.get(fmt.Sprintf("event_connectors/%d/runs/", id))
	t.Fatalf("connector %d never %s: %v\nruns: %s", id, what, last, runs.Raw)
	return nil
}

func status(s string) func(map[string]any) bool {
	return func(o map[string]any) bool { return o["status"] == s }
}

func waitEvents(t *testing.T, a *client, s eventStream, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var evs []map[string]any
	for time.Now().Before(deadline) {
		if evs = s.events(t, a); len(evs) >= n {
			return evs
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%d events, want %d", len(evs), n)
	return nil
}

func TestEventConnectorAPI(t *testing.T) {
	f := newConnectorFixture(t)
	a := f.a
	base := map[string]any{"name": uniq("conn"), "organization": f.org, "event_stream": f.stream.id, "enabled": false}
	with := func(kv ...any) map[string]any {
		b := map[string]any{}
		for k, v := range base {
			b[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			b[kv[i].(string)] = kv[i+1]
		}
		return b
	}
	for name, body := range map[string]map[string]any{
		"no source":       with(),
		"short name":      with("source", "kafka"),
		"filters":         with("source", "ansible.eda.range", "filters", []any{"ansible.eda.json_filter"}),
		"filter name":     with("source", "ansible.eda.range", "filters", []any{map[string]any{"json_filter": nil}}),
		"args":            with("source", "ansible.eda.range", "args", []any{1}),
		"python":          with("source", "ansible.eda.range", "python", "python3; rm -rf /"),
		"policy":          with("source", "ansible.eda.range", "restart_policy", "sometimes"),
		"buffer":          with("source", "ansible.eda.range", "buffer_size", 0),
		"credential":      with("source", "ansible.eda.range", "credentials", []any{999999}),
		"other org":       with("source", "ansible.eda.range", "organization", newOrg(t, a)),
		"missing stream":  with("source", "ansible.eda.range", "event_stream", 999999),
		"credential list": with("source", "ansible.eda.range", "credentials", "1"),
	} {
		if r := a.post("event_connectors/", body); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
	}
	r := must(t, a.post("event_connectors/", with("source", "ansible.eda.range", "args", map[string]any{"limit": 5},
		"filters", []any{map[string]any{"ansible.eda.json_filter": map[string]any{"exclude_keys": []any{"x"}}}})), 201)
	id := r.id()
	url := fmt.Sprintf("event_connectors/%d/", id)
	if r.Body["status"] != "stopped" || r.Body["restart_policy"] != "on-failure" || r.Body["buffer_size"] != float64(1000) ||
		r.Body["summary_fields"].(map[string]any)["event_stream"].(map[string]any)["id"] != float64(f.stream.id) {
		t.Fatalf("created: %v", r.Body)
	}
	if _, ok := r.Body["token_hash"]; ok {
		t.Fatal("the token's hash is shown")
	}
	must(t, a.post(url+"restart/", nil), 400)
	if v := must(t, a.get(url+"history/"), 200); v.count() != 1 {
		t.Fatalf("versions: %s", v.Raw)
	}

	// A stranger sees nothing; an org member needs the right to change the
	// stream, and to use each credential.
	stranger, _ := newUser(t, a)
	must(t, stranger.get(url), 403)
	user, uid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": uid, "object_id": f.org}), 201)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "EventStream Read"), "user": uid, "object_id": f.stream.id}), 201)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Admin"), "user": uid, "object_id": newOrg(t, a)}), 201)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "EventConnector Admin"), "user": uid, "object_id": id}), 201)
	must(t, user.get(url), 200)
	must(t, user.patch(url, map[string]any{"description": "x"}), 200)
	must(t, user.patch(url, map[string]any{"event_stream": f.stream.id, "source": "ansible.eda.tick"}), 200)
	other := newEventStream(t, a, f.org, streamCredential(t, a, f.org, eventstreams.CredToken, map[string]any{"token": "t"}), eventstreams.TypeGeneric, nil)
	must(t, user.patch(url, map[string]any{"event_stream": other.id}), 403)
	cred := streamCredential(t, a, f.org, eventstreams.CredToken, map[string]any{"token": "t"})
	must(t, user.patch(url, map[string]any{"credentials": []any{cred}}), 403)
	must(t, a.patch(url, map[string]any{"credentials": []any{cred}}), 200)
	if got := must(t, a.get(url), 200).Body["summary_fields"].(map[string]any)["credentials"].([]any); len(got) != 1 {
		t.Fatalf("credentials summary: %v", got)
	}

	// Posting with a connector token: only the current token of a running
	// connector of the stream, and its events name the connector.
	tok := agent.NewToken()
	if _, err := app.DB.Exec(context.Background(), "UPDATE event_connectors SET enabled = true, status = 'running', token_hash = $2 WHERE id = $1",
		id, agent.HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	batch := `[{"specversion": "1.0", "id": "e1", "source": "/elsewhere/", "type": "ansible.eda.tick", "data": {"i": 1}}]`
	ce := "application/cloudevents-batch+json"
	if code, body, _ := f.stream.post(t, batch, headers("Content-Type", ce, "Authorization", "Connector "+tok)); code != 202 {
		t.Fatalf("connector post: %d %v", code, body)
	}
	if code, _, _ := f.stream.post(t, batch, headers("Content-Type", ce, "Authorization", "Connector nope")); code != 401 {
		t.Fatalf("a wrong token: %d", code)
	}
	if code, _, _ := other.post(t, batch, headers("Content-Type", ce, "Authorization", "Connector "+tok)); code != 401 {
		t.Fatalf("another stream: %d", code)
	}
	evs := f.stream.events(t, a)
	if len(evs) != 1 || evs[0]["source"] != fmt.Sprintf("/api/v2/event_connectors/%d/", id) || evs[0]["event_id"] != "e1" {
		t.Fatalf("events: %v", evs)
	}
	// Turning it off revokes the token.
	must(t, a.patch(url, map[string]any{"enabled": false}), 200)
	if code, _, _ := f.stream.post(t, batch, headers("Content-Type", ce, "Authorization", "Connector "+tok)); code != 401 {
		t.Fatalf("a revoked token: %d", code)
	}

	// Deleting the credential takes it off the connector; deleting the
	// stream deletes the connector.
	must(t, a.delete(fmt.Sprintf("credentials/%d/", cred)), 204)
	if got := must(t, a.get(url), 200).Body["credentials"].([]any); len(got) != 0 {
		t.Fatalf("credentials after delete: %v", got)
	}
	ub := must(t, a.get(fmt.Sprintf("event_streams/%d/used_by/", f.stream.id)), 200)
	if !strings.Contains(string(ub.Raw), fmt.Sprintf(`"id":%d`, id)) {
		t.Fatalf("used_by: %s", ub.Raw)
	}
	must(t, a.delete(fmt.Sprintf("event_streams/%d/", f.stream.id)), 204)
	must(t, a.get(url), 404)
}

// A connector on this node posts its source's events, with its
// credentials, and stops or restarts as its policy says.
func TestEventConnectorRuns(t *testing.T) {
	requireConnectors(t)
	f := newConnectorFixture(t)
	a := f.a
	ct := must(t, a.post("credential_types/", map[string]any{"name": uniq("sasl"), "kind": "cloud",
		"inputs": map[string]any{"fields": []any{map[string]any{"id": "user", "label": "User"}, map[string]any{"id": "password", "label": "Password", "secret": true},
			map[string]any{"id": "cert", "label": "Cert", "multiline": true}}},
		"injectors": map[string]any{"env": map[string]any{"FAKE_USER": "{{ user }}"}, "file": map[string]any{"template": "{{ cert }}"},
			"extra_vars": map[string]any{"password": "{{ password }}", "file": "{{ eda.filename }}"}}}), 201).id()
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("c"), "organization": f.org, "credential_type": ct,
		"inputs": map[string]any{"user": "alice", "password": "hunter2-secret", "cert": "-----CERT-----"}}), 201).id()
	id := f.connector(t, map[string]any{"source": "test.fake.burst", "args": map[string]any{"count": 4, "env": "FAKE_USER"}, "credentials": []any{cred}})
	c := waitConnector(t, a, id, "completed", status("completed"))
	if c["status_explanation"] != "The source ended." || c["controller_node"] != "" || c["stats"].(map[string]any)["sent"] != float64(4) {
		t.Fatalf("completed: %v", c)
	}
	evs := waitEvents(t, a, f.stream, 4)
	for i, e := range evs {
		d := e["data"].(map[string]any)
		meta := d["meta"].(map[string]any)
		if d["i"] != float64(i) || d["env"] != "alice" || d["secret"] != "-----CERT-----" || d["password_set"] != true ||
			e["event_type"] != "test.fake.burst" || e["source"] != fmt.Sprintf("/api/v2/event_connectors/%d/", id) || meta["uuid"] != e["event_id"] {
			t.Fatalf("event %d: %v", i, e)
		}
	}
	runs := must(t, a.get(fmt.Sprintf("event_connectors/%d/runs/", id)), 200).results()
	if len(runs) != 1 || runs[0]["status"] != "completed" || runs[0]["rc"] != float64(0) || runs[0]["controller_node"] != "test-node" ||
		!strings.Contains(runs[0]["log"].(string), "starting test.fake.burst") {
		t.Fatalf("runs: %v", runs)
	}
	if log := runs[0]["log"].(string); strings.Contains(log, "hunter2") || strings.Contains(log, "CERT") || strings.Contains(log, "awxnext-status") {
		t.Fatalf("the log shows a secret or a status line:\n%s", log)
	}

	// on-failure: a failing source restarts, with a backoff, until
	// max_restarts.
	failing := f.connector(t, map[string]any{"source": "test.fake.burst", "args": map[string]any{"count": 1, "fail": true},
		"restart_policy": "on-failure", "restart_delay": 1, "max_restarts": 2})
	c = waitConnector(t, a, failing, "gave up", func(o map[string]any) bool { return o["status"] == "failed" })
	if !strings.Contains(c["status_explanation"].(string), "Stopped after 2 restarts") || c["restart_count"] != float64(2) {
		t.Fatalf("gave up: %v", c)
	}
	runs = must(t, a.get(fmt.Sprintf("event_connectors/%d/runs/", failing)), 200).results()
	if len(runs) != 3 || runs[0]["status"] != "failed" || runs[0]["rc"] != float64(1) || !strings.Contains(runs[0]["log"].(string), "RuntimeError: boom") {
		t.Fatalf("failing runs: %v", runs)
	}
	started := func(r map[string]any) time.Time {
		tm, _ := time.Parse(time.RFC3339Nano, r["started"].(string))
		return tm
	}
	if gap := started(runs[0]).Sub(started(runs[1])); gap < 2*time.Second {
		t.Fatalf("second restart after %s, want the delay doubled", gap)
	}
	// restart/ starts it again.
	must(t, a.post(fmt.Sprintf("event_connectors/%d/restart/", failing), nil), 202)
	waitConnector(t, a, failing, "restarted", func(o map[string]any) bool { return o["restart_count"] == float64(0) && o["status"] != "failed" })

	// always: a source that ends starts again.
	always := f.connector(t, map[string]any{"source": "test.fake.burst", "args": map[string]any{"count": 1}, "restart_policy": "always", "restart_delay": 1})
	waitConnector(t, a, always, "restarted twice", func(o map[string]any) bool { return o["restart_count"].(float64) >= 2 })

	// A change restarts a running connector with it.
	ticking := f.connector(t, map[string]any{"source": "test.fake.tick", "args": map[string]any{"delay": 0.1}})
	waitConnector(t, a, ticking, "running", status("running"))
	must(t, a.patch(fmt.Sprintf("event_connectors/%d/", ticking), map[string]any{"args": map[string]any{"delay": 0.05}}), 200)
	waitConnector(t, a, ticking, "running again", func(o map[string]any) bool {
		runs := must(t, a.get(fmt.Sprintf("event_connectors/%d/runs/", ticking)), 200).results()
		return o["status"] == "running" && len(runs) == 2 && runs[1]["status"] == "stopped"
	})
	must(t, a.patch(fmt.Sprintf("event_connectors/%d/", ticking), map[string]any{"enabled": false}), 200)
	waitConnector(t, a, ticking, "stopped", func(o map[string]any) bool {
		runs := must(t, a.get(fmt.Sprintf("event_connectors/%d/runs/", ticking)), 200).results()
		return o["status"] == "stopped" && runs[0]["finished"] != nil
	})
}

// While awxnext does not take its events, a connector keeps them, and
// posts each once when it does again.
func TestEventConnectorBuffers(t *testing.T) {
	requireConnectors(t)
	f := newConnectorFixture(t)
	target, _ := url.Parse(srv.URL)
	var down atomic.Bool
	down.Store(true)
	var refused atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			refused.Add(1)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
	}))
	defer proxy.Close()
	internal := app.Cfg.InternalURL
	app.Cfg.InternalURL = proxy.URL
	t.Cleanup(func() { app.Cfg.InternalURL = internal })

	id := f.connector(t, map[string]any{"source": "test.fake.burst", "args": map[string]any{"count": 20}, "buffer_size": 50})
	deadline := time.Now().Add(30 * time.Second)
	for refused.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := len(f.stream.events(t, f.a)); n != 0 || refused.Load() < 2 {
		t.Fatalf("while down: %d events, %d refused posts", n, refused.Load())
	}
	down.Store(false)
	c := waitConnector(t, f.a, id, "completed", status("completed"))
	evs := waitEvents(t, f.a, f.stream, 20)
	seen := map[float64]bool{}
	for _, e := range evs {
		seen[e["data"].(map[string]any)["i"].(float64)] = true
	}
	stats := c["stats"].(map[string]any)
	if len(evs) != 20 || len(seen) != 20 || stats["sent"] != float64(20) || stats["retries"].(float64) < 2 || stats["dropped"] != float64(0) {
		t.Fatalf("after the outage: %d events (%d distinct), stats %v", len(evs), len(seen), stats)
	}
	runs := must(t, f.a.get(fmt.Sprintf("event_connectors/%d/runs/", id)), 200).results()
	if log := runs[0]["log"].(string); !strings.Contains(log, "buffering events") || !strings.Contains(log, "took events again") {
		t.Fatalf("log:\n%s", log)
	}
}

// A connector placed on an execution node runs through its agent, and
// moves to another node of its group when that node is lost; one placed
// on a node whose controller goes silent moves too.
func TestEventConnectorMovesOffLostNode(t *testing.T) {
	requireConnectors(t)
	f := newConnectorFixture(t)
	a := f.a
	ig := must(t, a.post("instance_groups/", map[string]any{"name": uniq("conn-nodes")}), 201).id()
	type node struct {
		host   string
		id     int64
		cancel context.CancelFunc
		done   chan struct{}
	}
	nodes := map[string]*node{}
	for i := 0; i < 2; i++ {
		n := &node{host: uniq("conn-exec"), done: make(chan struct{})}
		n.id = must(t, a.post("instances/", map[string]any{"hostname": n.host, "node_type": "execution"}), 201).id()
		tok := must(t, a.post(fmt.Sprintf("instances/%d/agent_token/", n.id), nil), 201).Body["token"].(string)
		must(t, a.post(fmt.Sprintf("instance_groups/%d/instances/", ig), map[string]any{"id": n.id}), 204)
		var ctx context.Context
		ctx, n.cancel = context.WithCancel(context.Background())
		go func() {
			defer close(n.done)
			agent.RunAgent(ctx, agent.Config{URL: srv.URL, Token: tok, Runtime: "none", WorkDir: t.TempDir(), Capacity: 10, Version: "test"})
		}()
		t.Cleanup(func() { n.cancel(); <-n.done })
		waitInstance(t, a, n.id, "ready")
		nodes[n.host] = n
	}
	id := f.connector(t, map[string]any{"source": "test.fake.tick", "args": map[string]any{"delay": 0.1}, "instance_group": ig})
	c := waitConnector(t, a, id, "running", status("running"))
	first := nodes[c["execution_node"].(string)]
	if first == nil || c["controller_node"] != "test-node" {
		t.Fatalf("placed on %v, controlled by %v", c["execution_node"], c["controller_node"])
	}
	waitEvents(t, a, f.stream, 3)

	// The node's agent goes away.
	first.cancel()
	<-first.done
	c = waitConnector(t, a, id, "moved", func(o map[string]any) bool {
		return o["status"] == "running" && o["execution_node"] != first.host && o["execution_node"] != ""
	})
	before := len(f.stream.events(t, a))
	waitEvents(t, a, f.stream, before+3)
	runs := must(t, a.get(fmt.Sprintf("event_connectors/%d/runs/", id)), 200).results()
	if len(runs) != 2 || runs[1]["execution_node"] != first.host || runs[1]["status"] != "lost" || runs[0]["execution_node"] != c["execution_node"] {
		t.Fatalf("runs: %v", runs)
	}

	// Its controller goes silent: the leader moves it.
	ghost := uniq("ghost")
	ctx := context.Background()
	if _, err := app.DB.Exec(ctx, `INSERT INTO instances (hostname, node_type, last_seen) VALUES ($1, 'hybrid', now() - interval '3 minutes')`, ghost); err != nil {
		t.Fatal(err)
	}
	var gen int64
	if err := app.DB.QueryRow(ctx, "UPDATE event_connectors SET controller_node = $2 WHERE id = $1 RETURNING generation", id, ghost).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	// Only the leader's move clears a connector whose controller is not
	// this node, for it to be placed again.
	waitConnector(t, a, id, "moved off the silent controller", func(o map[string]any) bool {
		var now int64
		app.DB.QueryRow(ctx, "SELECT generation FROM event_connectors WHERE id = $1", id).Scan(&now)
		return o["controller_node"] == "test-node" && o["status"] == "running" && now > gen
	})
}

// A node that stops hands its connectors back, for another node to start
// them without waiting for it to be found lost.
func TestEventConnectorHandedBackOnShutdown(t *testing.T) {
	requireConnectors(t)
	f := newConnectorFixture(t)
	ctx := context.Background()
	// First in name order, so that placement would choose it again.
	node := "0-" + uniq("handoff")
	if _, err := app.DB.Exec(ctx, `WITH i AS (INSERT INTO instances (hostname, node_type, last_seen) VALUES ($1, 'hybrid', now()) RETURNING id)
		INSERT INTO instance_group_instances (instance_group_id, instance_id) SELECT g.id, i.id FROM instance_groups g, i WHERE g.name = 'default'`, node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.DB.Exec(ctx, "DELETE FROM instances WHERE hostname = $1", node) })
	id := f.connector(t, map[string]any{"source": "test.fake.tick", "args": map[string]any{"delay": 0.1}, "enabled": false})
	if _, err := app.DB.Exec(ctx, `UPDATE event_connectors SET enabled = true, status = 'starting', controller_node = $2, execution_node = $2,
		generation = generation + 1 WHERE id = $1`, id, node); err != nil {
		t.Fatal(err)
	}
	sup := resources.NewConnectorSupervisor(app, node)
	sctx, stop := context.WithCancel(ctx)
	go sup.Run(sctx)
	waitConnector(t, f.a, id, "running on "+node, func(o map[string]any) bool { return o["status"] == "running" && o["controller_node"] == node })
	stop()
	sup.Wait()
	runs := must(t, f.a.get(fmt.Sprintf("event_connectors/%d/runs/", id)), 200).results()
	if len(runs) != 1 || runs[0]["status"] != "stopped" || runs[0]["explanation"] != "Node "+node+" stopped." || runs[0]["finished"] == nil {
		t.Fatalf("runs: %v", runs)
	}
	// The stopped node is still counted up, but not chosen again.
	waitConnector(t, f.a, id, "placed on another node", func(o map[string]any) bool {
		return o["controller_node"] != node && o["controller_node"] != "" && o["status"] == "running"
	})
}

// A connector in a container group runs in a pod.
func TestEventConnectorPod(t *testing.T) {
	requireConnectors(t)
	f := newConnectorFixture(t)
	a := f.a
	k := &fakeKube{t: t, pods: map[string]map[string]any{}, dir: t.TempDir()}
	kube := httptest.NewServer(k)
	defer kube.Close()
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("k8s"), "organization": f.org, "credential_type": credType(t, a, "kubernetes_bearer_token"),
		"inputs": map[string]any{"host": kube.URL, "bearer_token": "kube-token", "verify_ssl": false}}), 201).id()
	ig := must(t, a.post("instance_groups/", map[string]any{"name": uniq("pods"), "is_container_group": true, "credential": cred}), 201).id()
	ee := must(t, a.post("execution_environments/", map[string]any{"name": uniq("de"), "image": "quay.io/ansible/ansible-rulebook:v1.3.2"}), 201).id()
	id := f.connector(t, map[string]any{"source": "test.fake.burst", "args": map[string]any{"count": 2}, "instance_group": ig, "execution_environment": ee})
	c := waitConnector(t, a, id, "completed", status("completed"))
	waitEvents(t, a, f.stream, 2)
	if c["execution_node"] != "" {
		t.Fatalf("execution node: %v", c["execution_node"])
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.pods) != 1 || len(k.deleted) != 1 {
		t.Fatalf("pods created %d, deleted %v", len(k.pods), k.deleted)
	}
	for name, pod := range k.pods {
		meta := pod["metadata"].(map[string]any)
		worker := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		b, _ := json.Marshal(worker["env"])
		if !strings.HasPrefix(name, fmt.Sprintf("event-connector-%d-", id)) || meta["labels"].(map[string]any)["awxnext-event-connector-id"] != fmt.Sprint(id) ||
			worker["image"] != "quay.io/ansible/ansible-rulebook:v1.3.2" || strings.Contains(string(b), `"HOME"`) {
			t.Fatalf("pod %s: %v", name, pod)
		}
	}
}

// ansible.eda's range and generic sources, with a filter.
func TestEventConnectorEDA(t *testing.T) {
	py := requireEDA(t)
	f := newConnectorFixture(t)
	a := f.a
	f.connector(t, map[string]any{"source": "ansible.eda.range", "args": map[string]any{"limit": 3}, "python": py})
	waitEvents(t, a, f.stream, 3)
	f.connector(t, map[string]any{"source": "ansible.eda.generic", "python": py,
		"args":    map[string]any{"payload": []any{map[string]any{"host": "web-01", "secret": "x"}, map[string]any{"host": "web-02", "secret": "y"}}},
		"filters": []any{map[string]any{"ansible.eda.json_filter": map[string]any{"exclude_keys": []any{"secret"}}}}})
	evs := waitEvents(t, a, f.stream, 5)
	var hosts []string
	for _, e := range evs {
		d := e["data"].(map[string]any)
		if e["event_type"] == "ansible.eda.range" && d["i"] == nil {
			t.Fatalf("range event: %v", e)
		}
		if e["event_type"] == "ansible.eda.generic" {
			if _, ok := d["secret"]; ok {
				t.Fatalf("the filter left secret: %v", d)
			}
			hosts = append(hosts, fmt.Sprint(d["host"]))
		}
	}
	if strings.Join(hosts, ",") != "web-01,web-02" {
		t.Fatalf("generic events: %v", hosts)
	}
}

// ansible.eda's Kafka source against a broker: full.yml starts one and sets
// AWXNEXT_TEST_KAFKA_BOOTSTRAP (host:port).
func TestEventConnectorKafka(t *testing.T) {
	bootstrap := os.Getenv("AWXNEXT_TEST_KAFKA_BOOTSTRAP")
	if bootstrap == "" {
		t.Skip("AWXNEXT_TEST_KAFKA_BOOTSTRAP is not set")
	}
	py := requireEDA(t)
	if err := exec.Command(py, "-c", "import aiokafka, aiohttp, fastavro").Run(); err != nil {
		t.Skipf("%s lacks the Kafka source's dependencies", py)
	}
	host, port, _ := strings.Cut(bootstrap, ":")
	topic := uniq("awxnext-connector")
	// The messages go first: the source then reads the topic from its
	// start, and its offsets give the events their ids.
	produce := `import asyncio, json, sys
from aiokafka import AIOKafkaProducer
async def main():
    p = AIOKafkaProducer(bootstrap_servers=sys.argv[1])
    await p.start()
    try:
        for i in range(3):
            await p.send_and_wait(sys.argv[2], json.dumps({"alert": "disk", "n": i}).encode())
    finally:
        await p.stop()
asyncio.run(main())
`
	if out, err := exec.Command(py, "-c", produce, bootstrap, topic).CombinedOutput(); err != nil {
		t.Fatalf("producing: %v\n%s", err, out)
	}
	f := newConnectorFixture(t)
	f.connector(t, map[string]any{"source": "ansible.eda.kafka", "python": py,
		"args": map[string]any{"host": host, "port": port, "topic": topic, "group_id": topic, "offset": "earliest"}})
	evs := waitEvents(t, f.a, f.stream, 3)
	for i, e := range evs {
		body := e["data"].(map[string]any)["body"].(map[string]any)
		meta := e["data"].(map[string]any)["meta"].(map[string]any)
		if body["alert"] != "disk" || body["n"] != float64(i) || e["event_type"] != "ansible.eda.kafka" ||
			meta["source_offset"] != fmt.Sprintf("%s:0:%d", topic, i) || e["event_id"] != meta["uuid"] {
			t.Fatalf("event %d: %v", i, e)
		}
	}
}

// A GitOps source declares connectors, their stream and credentials by
// name.
func TestEventConnectorGitOps(t *testing.T) {
	f := newConnectorFixture(t)
	a := f.a
	allowFileURLs(t, a)
	streamName := a.get(fmt.Sprintf("event_streams/%d/", f.stream.id)).Body["name"].(string)
	credName := uniq("kafka")
	cred := must(t, a.post("credentials/", map[string]any{"name": credName, "organization": f.org,
		"credential_type": credType(t, a, eventstreams.CredToken), "inputs": map[string]any{"token": "t"}}), 201).id()
	doc := fmt.Sprintf(`kind: EventConnector
name: orders
spec:
  event_stream: %s
  source: ansible.eda.kafka
  args: {host: kafka.example.com, port: 9093, topic: orders}
  credentials: [%s]
  restart_policy: always
  enabled: false
`, streamName, credName)
	repo := gitRepo(t, map[string]string{"gitops/connectors.yml": doc})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": f.org, "scm_type": "git", "scm_url": "file://" + repo}), 201).id()
	waitProjectSynced(t, a, proj, "")
	sid := must(t, a.post("gitops_sources/", map[string]any{"name": uniq("src"), "organization": f.org, "project": proj, "path": "gitops",
		"sync_on_project_update": false}), 201).id()
	rep := must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), nil), 202)
	if summary(rep, "create") != 1 || summary(rep, "error") != 0 {
		t.Fatalf("sync: %s", rep.Raw)
	}
	c := one(t, a, fmt.Sprintf("event_connectors/?organization=%d&name=orders", f.org))
	if c["managed_by"] != fmt.Sprintf("gitops:%d", sid) || c["event_stream"] != float64(f.stream.id) || c["restart_policy"] != "always" ||
		fmt.Sprint(c["credentials"]) != fmt.Sprintf("[%d]", cred) || c["args"].(map[string]any)["topic"] != "orders" || c["status"] != "stopped" {
		t.Fatalf("connector: %v", c)
	}
	must(t, a.patch(fmt.Sprintf("event_connectors/%v/", c["id"]), map[string]any{"description": "x"}), 409)
}
