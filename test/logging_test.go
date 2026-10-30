package test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/logship"
)

// receiver collects what a log aggregator receives.
type receiver struct {
	mu   sync.Mutex
	msgs []map[string]any
	auth []string
}

func (r *receiver) add(b []byte, auth string) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		m = map[string]any{"unparsed": string(b)}
	}
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.auth = append(r.auth, auth)
	r.mu.Unlock()
}

// wait returns the first message matching, as Splunk's or AWX's format.
func (r *receiver) wait(t *testing.T, match func(m map[string]any) bool) (map[string]any, string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for i, m := range r.msgs {
			body := m
			if e, ok := m["event"].(map[string]any); ok {
				body = e
			}
			if match(body) {
				auth := r.auth[i]
				r.mu.Unlock()
				return m, auth
			}
		}
		r.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no matching message among %d", r.count())
	return nil, ""
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

func (r *receiver) none(t *testing.T, within time.Duration, match func(m map[string]any) bool) {
	t.Helper()
	time.Sleep(within)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.msgs {
		if match(m) {
			t.Errorf("unexpected message: %v", m)
		}
	}
}

func message(text string) func(m map[string]any) bool {
	return func(m map[string]any) bool { return m["message"] == text }
}

func httpReceiver(t *testing.T, tls bool) (*receiver, *httptest.Server) {
	r := &receiver{}
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.add(b, req.URL.Path+" "+req.Header.Get("Authorization"))
	})
	srv := httptest.NewServer(h)
	if tls {
		srv.Close()
		srv = httptest.NewTLSServer(h)
	}
	t.Cleanup(srv.Close)
	return r, srv
}

func tcpReceiver(t *testing.T, ln net.Listener) *receiver {
	r := &receiver{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := bufio.NewScanner(conn)
				sc.Buffer(make([]byte, 1<<20), 1<<20)
				for sc.Scan() {
					r.add(sc.Bytes(), "")
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return r
}

func udpReceiver(t *testing.T) (*receiver, int) {
	r := &receiver{}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			r.add(append([]byte{}, buf[:n]...), "")
		}
	}()
	return r, pc.LocalAddr().(*net.UDPAddr).Port
}

func portOf(t *testing.T, u string) int {
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"))
	var n int
	fmt.Sscan(p, &n)
	return n
}

// AWX's external logging: its loggers, formatted as its LogstashFormatter,
// sent over HTTPS/HTTP, TCP and UDP to receivers the test starts.
func TestExternalLogging(t *testing.T) {
	a := adminClient(t)
	t.Cleanup(func() { a.delete("settings/logging/") })
	set := func(v map[string]any) {
		t.Helper()
		base := map[string]any{"LOG_AGGREGATOR_ENABLED": true, "LOG_AGGREGATOR_LEVEL": "INFO", "LOG_AGGREGATOR_TCP_TIMEOUT": 1,
			"LOG_AGGREGATOR_LOGGERS": []string{"awx", "activity_stream", "job_events", "system_tracking"}}
		for k, x := range v {
			base[k] = x
		}
		must(t, a.patch("settings/logging/", base), 200)
	}

	// Disabled by default: the test endpoint says so, and AWX's checks.
	if r := a.post("settings/logging/test/", nil); r.Status != 409 || r.Body["error"] != "Logging not enabled" {
		t.Errorf("disabled test: %d %s", r.Status, r.Raw)
	}
	if r := a.patch("settings/logging/", map[string]any{"LOG_AGGREGATOR_ENABLED": true}); r.Status != 400 ||
		!strings.Contains(r.Raw, `Cannot enable log aggregator without providing host.\nCannot enable log aggregator without providing type.`) {
		t.Errorf("enable without host: %d %s", r.Status, r.Raw)
	}
	if r := a.patch("settings/logging/", map[string]any{"LOG_AGGREGATOR_LEVEL": "LOUD"}); r.Status != 400 {
		t.Errorf("level choice: %d %s", r.Status, r.Raw)
	}
	opts := must(t, a.do("OPTIONS", "settings/logging/", nil), 200).Body["actions"].(map[string]any)["GET"].(map[string]any)
	if d := opts["LOG_AGGREGATOR_LEVEL"].(map[string]any)["default"]; d != "WARNING" {
		t.Errorf("level default: %v", d)
	}

	// HTTP, logstash, basic authentication; every logger.
	web, srv := httpReceiver(t, false)
	set(map[string]any{"LOG_AGGREGATOR_HOST": srv.URL + "/logs?src=awx", "LOG_AGGREGATOR_TYPE": "logstash", "LOG_AGGREGATOR_PROTOCOL": "https",
		"LOG_AGGREGATOR_USERNAME": "shipper", "LOG_AGGREGATOR_PASSWORD": "s3cret", "LOG_AGGREGATOR_TOWER_UUID": "cluster-7"})
	slog.Warn("logship service log", "component", "test")
	m, auth := web.wait(t, message("logship service log"))
	if auth != "/logs Basic "+base64.StdEncoding.EncodeToString([]byte("shipper:s3cret")) {
		t.Errorf("auth: %q", auth)
	}
	if m["level"] != "WARNING" || m["logger_name"] != "awx.main" || m["component"] != "test" || m["cluster_host_id"] != "test-node" ||
		m["tower_uuid"] != "cluster-7" || m["host"] == "" || !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{3})?Z$`).MatchString(fmt.Sprint(m["@timestamp"])) {
		t.Errorf("service log: %v", m)
	}
	org := uniq("logged-org")
	must(t, a.post("organizations/", map[string]any{"name": org}), 201)
	m, _ = web.wait(t, func(m map[string]any) bool {
		return m["logger_name"] == "awx.analytics.activity_stream" && fmt.Sprint(m["changes"]) != "" && strings.Contains(fmt.Sprint(m["changes"]), org)
	})
	if m["message"] != "Activity Stream update entry for organization" || m["operation"] != "create" || m["object1"] != "organization" || m["actor"] != "admin" || m["level"] != "INFO" {
		t.Errorf("activity stream: %v", m)
	}

	f := newLaunchFixture(t)
	jt := f.template(t, "facts.yml", map[string]any{"use_fact_cache": true})
	job := f.launch(t, a, jt, nil)
	m, _ = web.wait(t, func(m map[string]any) bool {
		return m["logger_name"] == "awx.analytics.job_events" && m["job"] == float64(job.id()) && m["event"] == "playbook_on_start"
	})
	if m["message"] != "Event data saved." || m["event_display"] != "Playbook Started" || m["counter"] == nil {
		t.Errorf("job event: %v", m)
	}
	m, _ = web.wait(t, func(m map[string]any) bool {
		return m["logger_name"] == "awx.analytics.system_tracking" && m["job_id"] == float64(job.id()) && m["host_name"] == "h1"
	})
	if facts, _ := m["ansible_facts"].(map[string]any); facts["app_color"] != "blue" || !strings.HasPrefix(fmt.Sprint(m["message"]), "New fact for inventory ") {
		t.Errorf("facts: %v", m)
	}

	// The level threshold and the loggers filter.
	set(map[string]any{"LOG_AGGREGATOR_LEVEL": "WARNING", "LOG_AGGREGATOR_LOGGERS": []string{"awx", "activity_stream"}})
	quiet := uniq("quiet-org")
	must(t, a.post("organizations/", map[string]any{"name": quiet}), 201)
	slog.Info("logship below the threshold")
	set(map[string]any{"LOG_AGGREGATOR_LEVEL": "INFO", "LOG_AGGREGATOR_LOGGERS": []string{"activity_stream"}})
	slog.Warn("logship unlisted logger")
	loud := uniq("loud-org")
	must(t, a.post("organizations/", map[string]any{"name": loud}), 201)
	web.wait(t, func(m map[string]any) bool { return strings.Contains(fmt.Sprint(m["changes"]), loud) })
	web.none(t, 0, func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["changes"]), quiet) || m["message"] == "logship below the threshold" || m["message"] == "logship unlisted logger"
	})

	// The test endpoint, for admins and auditors: the message on the first logger.
	must(t, a.post("settings/logging/test/", nil), 202)
	m, _ = web.wait(t, message("AWX Connection Test Message"))
	if m["level"] != "ERROR" || m["logger_name"] != "awx.analytics.activity_stream" {
		t.Errorf("test message: %v", m)
	}
	user, uid := newUser(t, a)
	must(t, user.post("settings/logging/test/", nil), 403)
	must(t, a.patch(fmt.Sprintf("users/%d/", uid), map[string]any{"is_system_auditor": true}), 200)
	must(t, user.post("settings/logging/test/", nil), 202)

	// Splunk: its event envelope and token header, over HTTPS without
	// verification; with verification the self-signed receiver gets nothing.
	splunk, tlsSrv := httpReceiver(t, true)
	set(map[string]any{"LOG_AGGREGATOR_HOST": strings.TrimPrefix(tlsSrv.URL, "https://") + "/services/collector/event", "LOG_AGGREGATOR_PORT": nil,
		"LOG_AGGREGATOR_TYPE": "splunk", "LOG_AGGREGATOR_USERNAME": "", "LOG_AGGREGATOR_PASSWORD": "hec-token", "LOG_AGGREGATOR_VERIFY_CERT": true,
		"LOG_AGGREGATOR_LOGGERS": []string{"awx"}})
	slog.Warn("logship verified")
	splunk.none(t, 1500*time.Millisecond, func(map[string]any) bool { return true })
	set(map[string]any{"LOG_AGGREGATOR_VERIFY_CERT": false})
	m, auth = splunk.wait(t, message("logship verified"))
	if auth != "/services/collector/event Splunk hec-token" || m["time"] == nil || m["event"].(map[string]any)["logger_name"] != "awx.main" {
		t.Errorf("splunk: %q %v", auth, m)
	}

	// TCP: a line per message; the test endpoint checks the port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp := tcpReceiver(t, ln)
	set(map[string]any{"LOG_AGGREGATOR_HOST": "127.0.0.1", "LOG_AGGREGATOR_PORT": ln.Addr().(*net.TCPAddr).Port, "LOG_AGGREGATOR_TYPE": "other",
		"LOG_AGGREGATOR_PROTOCOL": "tcp", "LOG_AGGREGATOR_TOWER_UUID": ""})
	slog.Error("logship over tcp")
	must(t, a.post("settings/logging/test/", nil), 202)
	m, _ = tcp.wait(t, message("logship over tcp"))
	if m["tower_uuid"] != must(t, a.get("settings/system/"), 200).Body["INSTALL_UUID"] {
		t.Errorf("tower_uuid falls back on INSTALL_UUID: %v", m["tower_uuid"])
	}
	tcp.wait(t, message("AWX Connection Test Message"))
	set(map[string]any{"LOG_AGGREGATOR_PORT": nil})
	if r := a.post("settings/logging/test/", nil); r.Status != 400 || r.Body["error"] != "Port required for tcp" {
		t.Errorf("no port: %d %s", r.Status, r.Raw)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	set(map[string]any{"LOG_AGGREGATOR_PORT": closedPort})
	if r := a.post("settings/logging/test/", nil); r.Status != 400 || r.Body["error"] == nil {
		t.Errorf("closed port: %d %s", r.Status, r.Raw)
	}

	// UDP: a datagram per message.
	udp, port := udpReceiver(t)
	set(map[string]any{"LOG_AGGREGATOR_PROTOCOL": "udp", "LOG_AGGREGATOR_PORT": port})
	slog.Error("logship over udp")
	udp.wait(t, message("logship over udp"))
	must(t, a.post("settings/logging/test/", nil), 202)

	// Turned off, nothing more is sent.
	set(map[string]any{"LOG_AGGREGATOR_ENABLED": false})
	slog.Error("logship disabled")
	udp.none(t, 500*time.Millisecond, message("logship disabled"))
}

// LOG_AGGREGATOR_INDIVIDUAL_FACTS: a message per fact.
func TestExternalLoggingIndividualFacts(t *testing.T) {
	a := adminClient(t)
	t.Cleanup(func() { a.delete("settings/logging/") })
	web, srv := httpReceiver(t, false)
	must(t, a.patch("settings/logging/", map[string]any{"LOG_AGGREGATOR_ENABLED": true, "LOG_AGGREGATOR_HOST": srv.URL, "LOG_AGGREGATOR_TYPE": "other",
		"LOG_AGGREGATOR_LEVEL": "INFO", "LOG_AGGREGATOR_LOGGERS": []string{"system_tracking"}, "LOG_AGGREGATOR_INDIVIDUAL_FACTS": true}), 200)
	f := newLaunchFixture(t)
	job := f.launch(t, a, f.template(t, "facts.yml", map[string]any{"use_fact_cache": true}), nil)
	m, _ := web.wait(t, func(m map[string]any) bool { return m["job_id"] == float64(job.id()) && m["host_name"] == "h2" })
	if facts, _ := m["ansible_facts"].(map[string]any); len(facts) != 1 {
		t.Errorf("one fact per message: %v", m)
	}
}

// rsyslog's action queue: at 90% of LOG_AGGREGATOR_ACTION_QUEUE_SIZE INFO
// and DEBUG messages are discarded, at 100% every message; the queue is
// sent once the aggregator answers.
func TestExternalLoggingQueue(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"LOG_AGGREGATOR_ENABLED": true, "LOG_AGGREGATOR_HOST": "127.0.0.1", "LOG_AGGREGATOR_PORT": ln.Addr().(*net.TCPAddr).Port,
		"LOG_AGGREGATOR_TYPE": "other", "LOG_AGGREGATOR_PROTOCOL": "tcp", "LOG_AGGREGATOR_LEVEL": "INFO", "LOG_AGGREGATOR_TCP_TIMEOUT": 1,
		"LOG_AGGREGATOR_LOGGERS": []any{"awx"}, "LOG_AGGREGATOR_ACTION_QUEUE_SIZE": 10}
	s := logship.New(func(_ context.Context, k string) any { return values[k] }, func(s string) (string, error) { return s, nil }, "n1", slog.DiscardHandler)
	ctx := context.Background()
	for i := 1; i <= 8; i++ {
		s.Log(ctx, "awx", slog.LevelWarn, fmt.Sprintf("warn %d", i), nil)
	}
	for i := 1; i <= 3; i++ {
		s.Log(ctx, "awx", slog.LevelInfo, fmt.Sprintf("info %d", i), nil)
	}
	for i := 9; i <= 13; i++ {
		s.Log(ctx, "awx", slog.LevelError, fmt.Sprintf("warn %d", i), nil)
	}
	tcp := tcpReceiver(t, ln)
	run, stop := context.WithCancel(ctx)
	defer stop()
	go s.Run(run)
	tcp.wait(t, message("warn 9"))
	time.Sleep(300 * time.Millisecond)
	var got []string
	tcp.mu.Lock()
	for _, m := range tcp.msgs {
		got = append(got, fmt.Sprint(m["message"]))
	}
	tcp.mu.Unlock()
	if strings.Join(got, ",") != "warn 1,warn 2,warn 3,warn 4,warn 5,warn 6,warn 7,warn 8,info 1,warn 9" {
		t.Errorf("delivered: %v", got)
	}
}
