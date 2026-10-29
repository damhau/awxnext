package test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/resources"
)

func emailConfig(port int, extra map[string]any) map[string]any {
	cfg := map[string]any{"host": "127.0.0.1", "port": port, "sender": "AWX <awx@example.com>", "recipients": []any{"ops@example.com", "dev@example.com"}}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func newEmailTemplate(t *testing.T, a *client, org int64, cfg map[string]any, extra map[string]any) int64 {
	t.Helper()
	body := map[string]any{"name": uniq("mail"), "organization": org, "notification_type": "email", "notification_configuration": cfg}
	for k, v := range extra {
		body[k] = v
	}
	return must(t, a.post("notification_templates/", body), 201).id()
}

// sendTest sends a template's test notification and waits for its outcome.
func sendTest(t *testing.T, a *client, nt int64) map[string]any {
	t.Helper()
	nid := int64(must(t, a.post(fmt.Sprintf("notification_templates/%d/test/", nt), nil), 202).Body["notification"].(float64))
	return waitNotification(t, a, nid)
}

func waitNotification(t *testing.T, a *client, id int64) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n := a.get(fmt.Sprintf("notifications/%d/", id)).Body
		if n["status"] != "pending" {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("notification %d still pending", id)
	return nil
}

func TestEmailNotificationValidation(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	post := func(cfg map[string]any, extra map[string]any) resp {
		body := map[string]any{"name": uniq("mail"), "organization": org, "notification_type": "email", "notification_configuration": cfg}
		for k, v := range extra {
			body[k] = v
		}
		return a.post("notification_templates/", body)
	}
	for _, c := range []struct {
		cfg  map[string]any
		want string
	}{
		{map[string]any{"host": "smtp.example.com"}, "Missing required fields for Notification Configuration: ['port', 'sender', 'recipients']."},
		{emailConfig(25, map[string]any{"port": "25"}), "Configuration field 'port' incorrect type, expected int."},
		{emailConfig(25, map[string]any{"use_tls": "yes"}), "Configuration field 'use_tls' incorrect type, expected bool."},
		{emailConfig(25, map[string]any{"recipients": "ops@example.com"}), "Configuration field 'recipients' incorrect type, expected list."},
		{emailConfig(25, map[string]any{"recipients": []any{}}), "No values specified for field 'recipients'"},
		{emailConfig(25, map[string]any{"recipients": []any{"not an address"}}), `"not an address" is not a valid email address.`},
		{emailConfig(25, map[string]any{"sender": "nobody"}), `sender is not a valid email address: "nobody".`},
		{emailConfig(25, map[string]any{"use_tls": true, "use_ssl": true}), "use_tls and use_ssl are mutually exclusive: set at most one."},
		{emailConfig(70000, nil), "port must be between 1 and 65535."},
		{emailConfig(25, map[string]any{"timeout": 0}), "timeout must be at least 1 second."},
		{emailConfig(25, map[string]any{"url": "https://x"}), `Unexpected field "url" for email notifications.`},
	} {
		r := must(t, post(c.cfg, nil), 400)
		if fmt.Sprint(r.Body["notification_configuration"]) != "["+c.want+"]" {
			t.Errorf("%v: %s", c.cfg, r.Raw)
		}
	}

	// Defaults are filled and the password is stored encrypted.
	r := must(t, post(emailConfig(25, map[string]any{"username": "u", "password": "s3cret"}), nil), 201)
	cfg := r.Body["notification_configuration"].(map[string]any)
	if cfg["password"] != "$encrypted$" || cfg["timeout"] != float64(30) || cfg["use_tls"] != false || cfg["use_ssl"] != false {
		t.Fatalf("config: %v", cfg)
	}
	var stored string
	app.DB.QueryRow(t.Context(), "SELECT notification_configuration->>'password' FROM notification_templates WHERE id = $1", r.id()).Scan(&stored)
	if plain, err := app.Crypto.Decrypt(stored); err != nil || plain != "s3cret" {
		t.Fatalf("stored password: %q %v", plain, err)
	}
	// Sending the placeholder back keeps it.
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", r.id()), map[string]any{"notification_configuration": cfg}), 200)
	app.DB.QueryRow(t.Context(), "SELECT notification_configuration->>'password' FROM notification_templates WHERE id = $1", r.id()).Scan(&stored)
	if plain, _ := app.Crypto.Decrypt(stored); plain != "s3cret" {
		t.Fatalf("password after save: %q", plain)
	}

	// Messages: subjects are one line; email bodies are free text, webhook bodies JSON.
	r = must(t, post(emailConfig(25, nil), map[string]any{"messages": map[string]any{"success": map[string]any{"message": "a\nb"}}}), 400)
	if !strings.Contains(r.Raw, "Messages cannot contain newlines (found newline in success event)") {
		t.Fatalf("newline: %s", r.Raw)
	}
	mail := must(t, post(emailConfig(25, nil), map[string]any{"messages": map[string]any{"success": map[string]any{"message": "{{ job.name }} ok", "body": "Done: {{ url }}"}}}), 201).id()
	hook := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": "https://example.com/hook"}}), 201).id()
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", hook), map[string]any{"messages": map[string]any{"success": map[string]any{"body": "Done: {{ url }}"}}}), 400)

	// Changing the type needs a configuration for the new type.
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", hook), map[string]any{"notification_type": "email"}), 400)
	r = must(t, a.patch(fmt.Sprintf("notification_templates/%d/", hook), map[string]any{"notification_type": "email", "notification_configuration": emailConfig(25, nil)}), 200)
	if r.Body["notification_type"] != "email" {
		t.Fatalf("type: %v", r.Body)
	}
	// A webhook needs JSON bodies, so an email with a text body cannot become one.
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", mail), map[string]any{"notification_type": "webhook", "notification_configuration": map[string]any{"url": "https://example.com/hook"}}), 400)
}

func TestEmailNotificationSend(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	tlsConf, pool := selfSignedTLS(t)
	resources.SMTPRootCAs = pool
	t.Cleanup(func() { resources.SMTPRootCAs = nil })

	// In clear, no login: the test message, with AWX's subject and body.
	plain := startSMTP(t, &smtpServer{})
	nt := newEmailTemplate(t, a, org, emailConfig(plain.port, nil), nil)
	n := sendTest(t, a, nt)
	if n["status"] != "successful" || n["notifications_sent"] != float64(1) || n["notification_type"] != "email" || n["recipients"] != "['ops@example.com', 'dev@example.com']" {
		t.Fatalf("notification: %v", n)
	}
	m := plain.next(t)
	base := app.Cfg.BaseURL
	if m.From != "awx@example.com" || fmt.Sprint(m.To) != "[ops@example.com dev@example.com]" || m.TLS || m.User != "" ||
		m.subject() != fmt.Sprintf("Tower Notification Test %d %s", nt, base) || m.Body != fmt.Sprintf("Ansible Tower Test Notification %d %s", nt, base) ||
		m.Header.Get("From") != "AWX <awx@example.com>" || m.Header.Get("To") != "ops@example.com, dev@example.com" {
		t.Fatalf("message: %+v", m)
	}

	// STARTTLS, then a PLAIN login.
	starttls := startSMTP(t, &smtpServer{tls: tlsConf, auth: []string{"PLAIN", "LOGIN"}, user: "mailer", pw: "s3cret"})
	nt = newEmailTemplate(t, a, org, emailConfig(starttls.port, map[string]any{"use_tls": true, "username": "mailer", "password": "s3cret"}), nil)
	if n := sendTest(t, a, nt); n["status"] != "successful" {
		t.Fatalf("starttls: %v", n)
	}
	if m := starttls.next(t); !m.TLS || m.User != "mailer" || m.Mech != "PLAIN" {
		t.Fatalf("starttls message: %+v", m)
	}
	// A wrong password fails with the server's answer.
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", nt), map[string]any{"notification_configuration": emailConfig(starttls.port, map[string]any{"use_tls": true, "username": "mailer", "password": "nope"})}), 200)
	if n := sendTest(t, a, nt); n["status"] != "failed" || !strings.Contains(fmt.Sprint(n["error"]), "535") {
		t.Fatalf("wrong password: %v", n)
	}

	// TLS from the start, and a LOGIN login.
	ssl := startSMTP(t, &smtpServer{tls: tlsConf, implicit: true, auth: []string{"LOGIN"}, user: "mailer", pw: "s3cret"})
	nt = newEmailTemplate(t, a, org, emailConfig(ssl.port, map[string]any{"use_ssl": true, "username": "mailer", "password": "s3cret"}), nil)
	if n := sendTest(t, a, nt); n["status"] != "successful" {
		t.Fatalf("ssl: %v", n)
	}
	if m := ssl.next(t); !m.TLS || m.Mech != "LOGIN" {
		t.Fatalf("ssl message: %+v", m)
	}

	// Failures: STARTTLS not offered, an untrusted certificate, a server that never answers.
	nt = newEmailTemplate(t, a, org, emailConfig(plain.port, map[string]any{"use_tls": true}), nil)
	if n := sendTest(t, a, nt); n["status"] != "failed" || !strings.Contains(fmt.Sprint(n["error"]), "STARTTLS extension not supported by server.") {
		t.Fatalf("no starttls: %v", n)
	}
	resources.SMTPRootCAs = nil
	nt = newEmailTemplate(t, a, org, emailConfig(starttls.port, map[string]any{"use_tls": true, "username": "mailer", "password": "s3cret"}), nil)
	if n := sendTest(t, a, nt); n["status"] != "failed" || !strings.Contains(fmt.Sprint(n["error"]), "certificate") {
		t.Fatalf("untrusted certificate: %v", n)
	}
	silent := startSMTP(t, &smtpServer{silent: true})
	nt = newEmailTemplate(t, a, org, emailConfig(silent.port, map[string]any{"timeout": 1}), nil)
	start := time.Now()
	if n := sendTest(t, a, nt); n["status"] != "failed" || !strings.Contains(fmt.Sprint(n["error"]), "timeout") || time.Since(start) > 10*time.Second {
		t.Fatalf("timeout: %v after %v", n, time.Since(start))
	}
}

// Job and approval notifications by email, with AWX's default and custom messages.
func TestEmailJobNotifications(t *testing.T) {
	w := newWorkflowFixture(t)
	a := w.a
	srv := startSMTP(t, &smtpServer{})
	base := strings.TrimRight(app.Cfg.BaseURL, "/")

	def := newEmailTemplate(t, a, w.org, emailConfig(srv.port, nil), nil)
	custom := newEmailTemplate(t, a, w.org, emailConfig(srv.port, nil), map[string]any{"messages": map[string]any{
		"started": map[string]any{"message": "Starting {{ job.name }}", "body": "{{ job_friendly_name }} {{ job.id }} started by {{ job.summary_fields.created_by.username }}"},
	}})
	must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_success/", w.site), map[string]any{"id": def}), 204)
	must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_started/", w.site), map[string]any{"id": custom}), 204)
	job := w.jobFixture.launch(t, a, w.site, nil)
	mustStatus(t, w.jobFixture, job, "successful")
	got := map[string]smtpMsg{}
	for range 2 {
		m := srv.next(t)
		got[strings.Fields(m.subject())[0]] = m
	}
	name := job.Body["name"].(string)
	if m := got["Starting"]; m.subject() != "Starting "+name || m.Body != fmt.Sprintf("Job %d started by admin", job.id()) {
		t.Fatalf("started: %q %q", m.subject(), m.Body)
	}
	m := got["Job"]
	url := fmt.Sprintf("%s/jobs/%d", base, job.id())
	if m.subject() != fmt.Sprintf("Job #%d '%s' successful: %s", job.id(), name, url) ||
		!strings.HasPrefix(m.Body, fmt.Sprintf("Job #%d had status successful, view details at %s\n\n{", job.id(), url)) || !strings.Contains(m.Body, `"playbook": "site.yml"`) {
		t.Fatalf("success: %q\n%s", m.subject(), m.Body)
	}
	ns := a.get(fmt.Sprintf("notification_templates/%d/notifications/", def)).results()
	if len(ns) != 1 || ns[0]["status"] != "successful" || !strings.HasPrefix(fmt.Sprint(ns[0]["body"]), fmt.Sprintf("Job #%d had status successful", job.id())) {
		t.Fatalf("notifications: %v", ns)
	}

	// Approvals.
	wf := w.wfjt(t, nil)
	w.approval(t, wf, "Gate", 0)
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/notification_templates_approvals/", wf), map[string]any{"id": def}), 204)
	wj := w.launch(t, wf, nil)
	ap := waitApproval(t, a, wj)
	wfURL := fmt.Sprintf("%s/jobs/%d", base, wj)
	if m := srv.next(t); m.subject() != fmt.Sprintf(`The approval node "Gate" needs review. This node can be viewed at: %s`, wfURL) ||
		!strings.HasPrefix(m.Body, fmt.Sprintf(`The approval node "Gate" needs review. This approval node can be viewed at: %s`+"\n\n{", wfURL)) {
		t.Fatalf("approval running: %q\n%s", m.subject(), m.Body)
	}
	must(t, a.post(fmt.Sprintf("workflow_approvals/%d/approve/", int64(ap["id"].(float64))), nil), 204)
	if m := srv.next(t); m.subject() != fmt.Sprintf(`The approval node "Gate" was approved. %s`, wfURL) || !strings.Contains(m.Body, `"status": "approved"`) {
		t.Fatalf("approval approved: %q\n%s", m.subject(), m.Body)
	}
}
