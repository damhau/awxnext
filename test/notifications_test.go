package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type hookRecorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
	fail bool
}

type recordedRequest struct {
	Method, Auth, Header string
	Body                 map[string]any
}

func newHookServer(t *testing.T) (*hookRecorder, *httptest.Server) {
	rec := &hookRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		u, p, _ := r.BasicAuth()
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, recordedRequest{Method: r.Method, Auth: u + ":" + p, Header: r.Header.Get("X-Custom"), Body: body})
		fail := rec.fail
		rec.mu.Unlock()
		if fail {
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

func (h *hookRecorder) wait(t *testing.T, n int) []recordedRequest {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		if len(h.reqs) >= n {
			out := append([]recordedRequest{}, h.reqs...)
			h.mu.Unlock()
			return out
		}
		h.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected %d webhook requests", n)
	return nil
}

func TestWebhookNotifications(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	rec, hook := newHookServer(t)

	must(t, a.post("notification_templates/", map[string]any{"name": "x", "organization": f.org, "notification_type": "slack",
		"notification_configuration": map[string]any{"url": hook.URL}}), 400)
	must(t, a.post("notification_templates/", map[string]any{"name": "x", "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": "ftp://x"}}), 400)
	must(t, a.post("notification_templates/", map[string]any{"name": "x", "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL, "bogus": 1}}), 400)
	must(t, a.post("notification_templates/", map[string]any{"name": "x", "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}, "messages": map[string]any{"success": map[string]any{"body": "{{ not json"}}}), 400)
	must(t, a.post("notification_templates/", map[string]any{"name": "x", "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}, "messages": map[string]any{"success": map[string]any{"body": "not json"}}}), 400)

	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL, "username": "bot", "password": "hunter2", "headers": map[string]any{"X-Custom": "yes"}},
		"messages":                   map[string]any{"error": map[string]any{"message": "{{ job.name }} broke", "body": `{"text": "{{ job_friendly_name }} {{ job.id }} {{ job.status }}", "inv": {{ job.summary_fields.inventory.name | tojson }}}`}}}), 201)
	if cfg := nt.Body["notification_configuration"].(map[string]any); cfg["password"] != "$encrypted$" || strings.Contains(nt.Raw, "hunter2") {
		t.Fatalf("password not hidden: %v", cfg)
	}
	ntID := nt.id()
	// Keeping the placeholder keeps the password.
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", ntID), map[string]any{"notification_configuration": map[string]any{
		"url": hook.URL, "username": "bot", "password": "$encrypted$", "headers": map[string]any{"X-Custom": "yes"}}}), 200)

	test := must(t, a.post(fmt.Sprintf("notification_templates/%d/test/", ntID), nil), 202)
	reqs := rec.wait(t, 1)
	if reqs[0].Auth != "bot:hunter2" || reqs[0].Header != "yes" || !strings.HasPrefix(reqs[0].Body["body"].(string), "Ansible Tower Test Notification") {
		t.Fatalf("test request: %+v", reqs[0])
	}
	nid := int64(test.Body["notification"].(float64))
	waitNotification := func(id int64) resp {
		t.Helper()
		for i := 0; i < 100; i++ {
			r := a.get(fmt.Sprintf("notifications/%d/", id))
			if r.Body["status"] != "pending" {
				return r
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("notification still pending")
		return resp{}
	}
	if n := waitNotification(nid); n.Body["status"] != "successful" || n.Body["notifications_sent"] != float64(1) {
		t.Fatalf("notification: %v", n.Body)
	}

	jt := f.template(t, "fail.yml", nil)
	for _, ev := range []string{"started", "success", "error"} {
		must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_%s/", jt, ev), map[string]any{"id": ntID}), 204)
	}
	if r := a.get(fmt.Sprintf("job_templates/%d/notification_templates_error/", jt)); r.count() != 1 {
		t.Fatalf("attached: %v", r.Body)
	}
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "failed")
	reqs = rec.wait(t, 3)
	started, failed := reqs[1].Body, reqs[2].Body
	if started["id"] != float64(job.id()) || started["status"] != "running" || started["friendly_name"] != "Job" {
		t.Fatalf("started body: %v", started)
	}
	if failed["text"] != fmt.Sprintf("Job %d failed", job.id()) || failed["inv"] == "" {
		t.Fatalf("error body: %v", failed)
	}
	jn := a.get(fmt.Sprintf("jobs/%d/notifications/", job.id()))
	if jn.count() != 2 {
		t.Fatalf("job notifications: %v", jn.Body)
	}
	for _, n := range jn.results() {
		if n["subject"] == fmt.Sprintf("%s broke", job.Body["name"]) {
			goto subjectOK
		}
	}
	t.Fatalf("custom subject missing: %v", jn.Body)
subjectOK:

	// Organization-level templates apply to all the organization's jobs; failures are recorded.
	must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_started/", jt), map[string]any{"id": ntID, "disassociate": true}), 204)
	must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_error/", jt), map[string]any{"id": ntID, "disassociate": true}), 204)
	must(t, a.post(fmt.Sprintf("organizations/%d/notification_templates_success/", f.org), map[string]any{"id": ntID}), 204)
	rec.mu.Lock()
	rec.fail = true
	rec.mu.Unlock()
	ok := f.launch(t, a, f.template(t, "site.yml", nil), nil)
	mustStatus(t, f, ok, "successful")
	reqs = rec.wait(t, 4)
	if reqs[3].Body["status"] != "successful" || reqs[3].Body["hosts"] == nil {
		t.Fatalf("default body: %v", reqs[3].Body)
	}
	on := a.get(fmt.Sprintf("jobs/%d/notifications/", ok.id()))
	if on.count() != 1 {
		t.Fatalf("org notifications: %v", on.Body)
	}
	if n := waitNotification(int64(on.results()[0]["id"].(float64))); n.Body["status"] != "failed" || !strings.Contains(n.Body["error"].(string), "500") {
		t.Fatalf("failed delivery: %v", n.Body)
	}
	if r := a.get(fmt.Sprintf("notification_templates/%d/", ntID)); len(r.Body["summary_fields"].(map[string]any)["recent_notifications"].([]any)) == 0 {
		t.Fatal("recent notifications missing")
	}

	// Attaching requires change on the template.
	user, uid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "job_templates", jt, "admin_role")}), 204)
	must(t, user.post(fmt.Sprintf("job_templates/%d/notification_templates_success/", jt), map[string]any{"id": ntID}), 403)
}
