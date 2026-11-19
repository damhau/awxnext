package test

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/resources"
)

// The login full.yml gives each Mailpit (MP_SMTP_AUTH).
const mailpitUser, mailpitPassword = "mailer", "mailpit-test-secret"

// mailpit is one Mailpit server: its SMTP address and its API's URL, from
// AWXNEXT_TEST_MAILPIT_<MODE>, "<host:port>,<API URL>".
type mailpit struct {
	mode, host string
	port       int
	api        string
	extra      map[string]any
}

type mailpitAddress struct{ Name, Address string }

type mailpitMsg struct {
	ID      string
	From    mailpitAddress
	To      []mailpitAddress
	Subject string
	Text    string
	Headers map[string][]string
	Raw     string
}

func mailpits(t *testing.T) []mailpit {
	t.Helper()
	modes := []struct {
		name  string
		extra map[string]any
	}{
		{"PLAIN", nil},
		{"STARTTLS", map[string]any{"use_tls": true}},
		{"TLS", map[string]any{"use_ssl": true}},
	}
	var out []mailpit
	for _, m := range modes {
		v := os.Getenv("AWXNEXT_TEST_MAILPIT_" + m.name)
		if v == "" {
			continue
		}
		addr, api, ok := strings.Cut(v, ",")
		host, port, err := net.SplitHostPort(addr)
		n, _ := strconv.Atoi(port)
		if !ok || err != nil || n == 0 {
			t.Fatalf("AWXNEXT_TEST_MAILPIT_%s: want <host:port>,<API URL>, got %q", m.name, v)
		}
		extra := map[string]any{"username": mailpitUser, "password": mailpitPassword}
		for k, v := range m.extra {
			extra[k] = v
		}
		out = append(out, mailpit{mode: strings.ToLower(m.name), host: host, port: n, api: strings.TrimRight(api, "/"), extra: extra})
	}
	return out
}

func (m mailpit) config(extra map[string]any) map[string]any {
	cfg := emailConfig(m.port, m.extra)
	cfg["host"] = m.host
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func (m mailpit) call(t *testing.T, method, path string, out any) {
	t.Helper()
	req, _ := http.NewRequest(method, m.api+path, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mailpit %s: %v", m.mode, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mailpit %s: %s %s: %d %s", m.mode, method, path, res.StatusCode, b)
	}
	switch o := out.(type) {
	case nil:
	case *string:
		*o = string(b)
	default:
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("mailpit %s: %s: %v", m.mode, path, err)
		}
	}
}

func (m mailpit) clear(t *testing.T) {
	t.Helper()
	m.call(t, http.MethodDelete, "/api/v1/messages", nil)
}

// wait returns the n messages Mailpit holds, oldest first, once it has them.
func (m mailpit) wait(t *testing.T, n int) []mailpitMsg {
	t.Helper()
	var list struct {
		Total    int
		Messages []struct{ ID string }
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		m.call(t, http.MethodGet, "/api/v1/messages?limit=100", &list)
		if list.Total >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if list.Total != n {
		t.Fatalf("mailpit %s: %d messages, want %d", m.mode, list.Total, n)
	}
	out := make([]mailpitMsg, 0, n)
	for _, s := range slices.Backward(list.Messages) {
		var msg mailpitMsg
		m.call(t, http.MethodGet, "/api/v1/message/"+s.ID, &msg)
		m.call(t, http.MethodGet, "/api/v1/message/"+s.ID+"/headers", &msg.Headers)
		m.call(t, http.MethodGet, "/api/v1/message/"+s.ID+"/raw", &msg.Raw)
		out = append(out, msg)
	}
	return out
}

func (msg mailpitMsg) header(k string) string {
	for name, v := range msg.Headers {
		if strings.EqualFold(name, k) {
			return strings.Join(v, ", ")
		}
	}
	return ""
}

// mailpitSubject is the custom subject of a job's event: non-ASCII, and
// long enough that its encoded words need several header lines.
func mailpitSubject(job any, ev string) string {
	return fmt.Sprintf("Tâche « %v » %s — 日本語のテスト, ünïcödé: %s fin", job, ev, strings.Repeat("éàü ", 40))
}

// check compares a message with what AWX's email backend writes.
func (m mailpit) check(t *testing.T, msg mailpitMsg, subject, body string) {
	t.Helper()
	var to []string
	for _, a := range msg.To {
		to = append(to, a.Address)
	}
	text := strings.TrimRight(strings.ReplaceAll(msg.Text, "\r\n", "\n"), "\n")
	if msg.Subject != subject || text != body {
		t.Errorf("mailpit %s: message %s:\nsubject %q\n   want %q\nbody %q\nwant %q", m.mode, msg.ID, msg.Subject, subject, text, body)
	}
	if msg.From != (mailpitAddress{"AWX", "awx@example.com"}) || fmt.Sprint(to) != "[ops@example.com dev@example.com]" ||
		msg.header("From") != "AWX <awx@example.com>" || msg.header("To") != "ops@example.com, dev@example.com" ||
		msg.header("Mime-Version") != "1.0" || msg.header("Content-Type") != `text/plain; charset="utf-8"` ||
		msg.header("Date") == "" || msg.header("Message-Id") == "" {
		t.Errorf("mailpit %s: message %s: from %v, to %v, headers %v", m.mode, msg.ID, msg.From, to, msg.Headers)
	}
	for i, l := range strings.Split(msg.Raw, "\r\n") {
		if len(l) > 998 || strings.ContainsAny(l, "\r\n") {
			t.Errorf("mailpit %s: message %s: raw line %d is %d bytes or ends without CRLF: %.80q", m.mode, msg.ID, i+1, len(l), l)
		}
	}
}

// Email notifications through a real mail server (roadmap M11-01): a
// Mailpit for each mode full.yml starts, each requiring a login.
func TestEmailMailpit(t *testing.T) {
	servers := mailpits(t)
	if len(servers) == 0 {
		t.Skip("AWXNEXT_TEST_MAILPIT_PLAIN, _STARTTLS and _TLS are not set")
	}
	pool := x509.NewCertPool()
	if ca := os.Getenv("AWXNEXT_TEST_MAILPIT_CA"); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil || !pool.AppendCertsFromPEM(pem) {
			t.Fatalf("AWXNEXT_TEST_MAILPIT_CA: %v", err)
		}
	}
	resources.SMTPRootCAs = pool
	t.Cleanup(func() { resources.SMTPRootCAs = nil })

	f := newJobFixture(t)
	a := f.a
	base := app.Cfg.BaseURL
	long := strings.Repeat("0123456789", 150)
	messages := map[string]any{}
	for _, ev := range []string{"started", "success", "error"} {
		messages[ev] = map[string]any{
			"message": mailpitSubject("{{ job.name }}", ev),
			"body":    fmt.Sprintf("Job {{ job.id }} %s: données ✓ 日本語\n%s\nend", ev, long),
		}
	}
	nts := map[string]int64{}
	for _, m := range servers {
		m.clear(t)
		nt := newEmailTemplate(t, a, f.org, m.config(nil), map[string]any{"messages": messages})
		nts[m.mode] = nt
		n := sendTest(t, a, nt)
		if n["status"] != "successful" {
			t.Fatalf("mailpit %s: test notification: %v", m.mode, n)
		}
		got := m.wait(t, 1)
		m.check(t, got[0], fmt.Sprintf("Tower Notification Test %d %s", nt, base), fmt.Sprintf("Ansible Tower Test Notification %d %s", nt, base))
	}

	ok, failing := f.template(t, "site.yml", nil), f.template(t, "fail.yml", nil)
	for _, jt := range []int64{ok, failing} {
		for _, ev := range []string{"started", "success", "error"} {
			for _, nt := range nts {
				must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_%s/", jt, ev), map[string]any{"id": nt}), 204)
			}
		}
	}
	jobs := map[string]resp{}
	for _, m := range servers {
		m.clear(t)
	}
	jobs["success"] = f.launch(t, a, ok, nil)
	mustStatus(t, f, jobs["success"], "successful")
	jobs["error"] = f.launch(t, a, failing, nil)
	mustStatus(t, f, jobs["error"], "failed")
	for _, m := range servers {
		got := m.wait(t, 4)
		bySubject := map[string]mailpitMsg{}
		for _, msg := range got {
			bySubject[msg.Subject] = msg
		}
		for _, ev := range []string{"started", "success", "error"} {
			for _, j := range []resp{jobs["success"], jobs["error"]} {
				status := map[string]string{"successful": "success", "failed": "error"}[j.Body["status"].(string)]
				if ev != "started" && ev != status {
					continue
				}
				subject := mailpitSubject(j.Body["name"], ev)
				msg, found := bySubject[subject]
				if !found {
					t.Errorf("mailpit %s: no %s message for job %d: want subject %q among %d", m.mode, ev, j.id(), subject, len(got))
					continue
				}
				m.check(t, msg, subject, fmt.Sprintf("Job %d %s: données ✓ 日本語\n%s\nend", j.id(), ev, long))
			}
		}
	}

	// A wrong password, and a certificate the server's isn't signed by.
	for _, m := range servers {
		nt := newEmailTemplate(t, a, f.org, m.config(map[string]any{"password": "wrong"}), nil)
		if n := sendTest(t, a, nt); n["status"] != "failed" || !strings.Contains(fmt.Sprint(n["error"]), "535") {
			t.Errorf("mailpit %s: wrong password: %v", m.mode, n)
		}
	}
	resources.SMTPRootCAs = x509.NewCertPool()
	for _, m := range servers {
		if m.mode == "plain" {
			continue
		}
		nt := newEmailTemplate(t, a, f.org, m.config(nil), nil)
		if n := sendTest(t, a, nt); n["status"] != "failed" || !strings.Contains(fmt.Sprint(n["error"]), "certificate") {
			t.Errorf("mailpit %s: untrusted certificate: %v", m.mode, n)
		}
	}
}
