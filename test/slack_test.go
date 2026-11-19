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

	"github.com/damhau/awxnext/internal/resources"
)

const slackToken = "xoxb-test-token"

// slackPost is one chat.postMessage the fake Slack API received.
type slackPost struct {
	Auth, ContentType string
	Body              map[string]any
}

// fakeSlack answers chat.postMessage as Slack does: ok, or ok false with
// invalid_auth for another token and channel_not_found for "missing".
type fakeSlack struct {
	mu    sync.Mutex
	posts []slackPost
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		err := json.Unmarshal(b, &body)
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat.postMessage" || err != nil {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.posts = append(f.posts, slackPost{Auth: r.Header.Get("Authorization"), ContentType: r.Header.Get("Content-Type"), Body: body})
		f.mu.Unlock()
		switch {
		case r.Header.Get("Authorization") != "Bearer "+slackToken:
			fmt.Fprint(w, `{"ok":false,"error":"invalid_auth"}`)
		case body["channel"] == "missing":
			fmt.Fprint(w, `{"ok":false,"error":"channel_not_found"}`)
		default:
			fmt.Fprint(w, `{"ok":true,"channel":"C0123","ts":"1700000000.000200"}`)
		}
	}))
	t.Cleanup(srv.Close)
	resources.SlackAPIURL = srv.URL + "/api/"
	t.Cleanup(func() { resources.SlackAPIURL = "https://slack.com/api/" })
	return f
}

// take waits for n more posts and returns them in the order received.
func (f *fakeSlack) take(t *testing.T, n int) []slackPost {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.posts) >= n {
			out := f.posts[:n:n]
			f.posts = f.posts[n:]
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected %d Slack posts", n)
	return nil
}

func newSlackTemplate(t *testing.T, a *client, org int64, cfg map[string]any, extra map[string]any) int64 {
	t.Helper()
	body := map[string]any{"name": uniq("slack"), "organization": org, "notification_type": "slack", "notification_configuration": cfg}
	for k, v := range extra {
		body[k] = v
	}
	return must(t, a.post("notification_templates/", body), 201).id()
}

func TestSlackNotificationValidation(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	for _, c := range []struct {
		cfg  map[string]any
		want string
	}{
		{map[string]any{}, "Missing required fields for Notification Configuration: ['token', 'channels']."},
		{map[string]any{"token": 1, "channels": []any{"#ops"}}, "Configuration field 'token' incorrect type, expected password."},
		{map[string]any{"token": "t", "channels": "#ops"}, "Configuration field 'channels' incorrect type, expected list."},
		{map[string]any{"token": "t", "channels": []any{}}, "No values specified for field 'channels'"},
		{map[string]any{"token": "t", "channels": []any{"#ops", "#"}}, `"#" is not a channel: write #name, or #name, <message ts> to post in that message's thread.`},
		{map[string]any{"token": "t", "channels": []any{"#ops, 1.2, 3"}}, `"#ops, 1.2, 3" is not a channel: write #name, or #name, <message ts> to post in that message's thread.`},
		{map[string]any{"token": "t", "channels": []any{"#ops"}, "hex_color": 5}, "Configuration field 'hex_color' incorrect type, expected string."},
		{map[string]any{"token": "", "channels": []any{"#ops"}}, "token must not be empty."},
		{map[string]any{"token": "t", "channels": []any{"#ops"}, "url": "https://x"}, `Unexpected field "url" for slack notifications.`},
	} {
		r := must(t, a.post("notification_templates/", map[string]any{"name": uniq("slack"), "organization": org, "notification_type": "slack",
			"notification_configuration": c.cfg}), 400)
		if fmt.Sprint(r.Body["notification_configuration"]) != "["+c.want+"]" {
			t.Errorf("%v: %s", c.cfg, r.Raw)
		}
	}

	// The token is stored encrypted and shown as $encrypted$; hex_color is
	// stored only when given, as in AWX.
	nt := newSlackTemplate(t, a, org, map[string]any{"token": "s3cret", "channels": []any{"#ops"}}, nil)
	r := a.get(fmt.Sprintf("notification_templates/%d/", nt))
	if fmt.Sprint(r.Body["notification_configuration"]) != "map[channels:[#ops] token:$encrypted$]" {
		t.Fatalf("config: %v", r.Body["notification_configuration"])
	}
	token := func() string {
		var stored string
		app.DB.QueryRow(t.Context(), "SELECT notification_configuration->>'token' FROM notification_templates WHERE id = $1", nt).Scan(&stored)
		plain, err := app.Crypto.Decrypt(stored)
		if err != nil {
			t.Fatalf("stored token %q: %v", stored, err)
		}
		return plain
	}
	if token() != "s3cret" {
		t.Fatal("stored token")
	}
	cfg := r.Body["notification_configuration"].(map[string]any)
	cfg["hex_color"] = "#36a64f"
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", nt), map[string]any{"notification_configuration": cfg}), 200)
	if token() != "s3cret" {
		t.Fatal("token after saving the placeholder")
	}

	// A Slack message has no body: one sent is accepted, as AWX does, and ignored.
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", nt), map[string]any{"messages": map[string]any{
		"started": map[string]any{"message": "{{ job.name }} started", "body": "not JSON"}}}), 200)
	// Another type's configuration doesn't become Slack's.
	hook := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": "https://example.com/hook", "password": "pw"}}), 201).id()
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", hook), map[string]any{"notification_type": "slack",
		"notification_configuration": map[string]any{"token": "$encrypted$", "channels": []any{"#ops"}}}), 400)
}

// Slack notifications against a fake Slack API (roadmap M11-02).
func TestSlackNotificationSend(t *testing.T) {
	slack := newFakeSlack(t)
	a := adminClient(t)
	org := newOrg(t, a)
	base := app.Cfg.BaseURL

	nt := newSlackTemplate(t, a, org, map[string]any{"token": slackToken, "channels": []any{"#ops", "dev", " #ops, 1700000000.000100 "}}, nil)
	n := sendTest(t, a, nt)
	subject := fmt.Sprintf("Tower Notification Test %d %s", nt, base)
	if n["status"] != "successful" || n["notifications_sent"] != float64(1) || n["notification_type"] != "slack" ||
		n["recipients"] != "['#ops', 'dev', ' #ops, 1700000000.000100 ']" || n["subject"] != subject {
		t.Fatalf("notification: %v", n)
	}
	posts := slack.take(t, 3)
	for i, want := range []string{
		`map[as_user:true channel:ops text:%s]`,
		`map[as_user:true channel:dev text:%s]`,
		`map[as_user:true channel:ops text:%s thread_ts:1700000000.000100]`,
	} {
		p := posts[i]
		if p.Auth != "Bearer "+slackToken || p.ContentType != "application/json; charset=utf-8" || fmt.Sprint(p.Body) != fmt.Sprintf(want, subject) {
			t.Errorf("post %d: %+v", i, p)
		}
	}

	// A valid hex_color sends the message as a colored attachment; another
	// sends it as text, as AWX does.
	for color, attached := range map[string]bool{"#36a64f": true, "#f00": true, "36a64f": false, "#36a64": false} {
		nt := newSlackTemplate(t, a, org, map[string]any{"token": slackToken, "channels": []any{"#ops"}, "hex_color": color}, nil)
		if n := sendTest(t, a, nt); n["status"] != "successful" {
			t.Fatalf("%s: %v", color, n)
		}
		p := slack.take(t, 1)[0]
		subject := fmt.Sprintf("Tower Notification Test %d %s", nt, base)
		want := fmt.Sprintf("map[as_user:true channel:ops text:%s]", subject)
		if attached {
			want = fmt.Sprintf("map[as_user:true attachments:[map[color:%s text:%s]] channel:ops]", color, subject)
		}
		if fmt.Sprint(p.Body) != want {
			t.Errorf("%s: %v", color, p.Body)
		}
	}

	// A channel Slack refuses fails the notification with Slack's error; the
	// other channels still get the message.
	nt = newSlackTemplate(t, a, org, map[string]any{"token": slackToken, "channels": []any{"#missing", "#ops"}}, nil)
	n = sendTest(t, a, nt)
	subject = fmt.Sprintf("Tower Notification Test %d %s", nt, base)
	if n["status"] != "failed" || n["notifications_sent"] != float64(0) ||
		n["error"] != fmt.Sprintf("Slack Notification unable to send missing: %s (channel_not_found)", subject) {
		t.Fatalf("refused channel: %v", n)
	}
	if posts := slack.take(t, 2); posts[1].Body["channel"] != "ops" {
		t.Fatalf("posts: %v", posts)
	}
	nt = newSlackTemplate(t, a, org, map[string]any{"token": "xoxb-revoked", "channels": []any{"#ops"}}, nil)
	if n := sendTest(t, a, nt); n["status"] != "failed" || !strings.HasSuffix(fmt.Sprint(n["error"]), "(invalid_auth)") {
		t.Fatalf("wrong token: %v", n)
	}
	slack.take(t, 1)
}

func TestSlackJobNotifications(t *testing.T) {
	slack := newFakeSlack(t)
	w := newWorkflowFixture(t)
	a := w.a
	base := strings.TrimRight(app.Cfg.BaseURL, "/")
	cfg := map[string]any{"token": slackToken, "channels": []any{"#ops"}}

	// The collection's custom messages, and AWX's default ones.
	custom := newSlackTemplate(t, a, w.org, cfg, map[string]any{"messages": map[string]any{
		"started": map[string]any{"message": "{{ job_friendly_name }} {{ job.id }} started"},
		"success": map[string]any{"message": "{{ job_friendly_name }} completed in {{ job.elapsed }} seconds"},
		"error":   map[string]any{"message": "{{ job_friendly_name }} FAILED! Please look at {{ job.url }}"},
	}})
	def := newSlackTemplate(t, a, w.org, cfg, nil)
	for _, jt := range []int64{w.site, w.fail} {
		for _, ev := range []string{"started", "success", "error"} {
			must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_%s/", jt, ev), map[string]any{"id": custom}), 204)
		}
		must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_success/", jt), map[string]any{"id": def}), 204)
	}
	ok := w.jobFixture.launch(t, a, w.site, nil)
	mustStatus(t, w.jobFixture, ok, "successful")
	texts := map[string]bool{}
	for _, p := range slack.take(t, 3) {
		texts[fmt.Sprint(p.Body["text"])] = true
	}
	url := fmt.Sprintf("%s/jobs/%d", base, ok.id())
	for _, want := range []string{fmt.Sprintf("Job %d started", ok.id()), fmt.Sprintf("Job #%d '%s' successful: %s", ok.id(), ok.Body["name"], url)} {
		if !texts[want] {
			t.Errorf("no message %q in %v", want, texts)
		}
	}
	elapsed := false
	for text := range texts {
		elapsed = elapsed || strings.HasPrefix(text, "Job completed in ") && strings.HasSuffix(text, " seconds") && text != "Job completed in  seconds"
	}
	if !elapsed {
		t.Errorf("no success message with the elapsed time in %v", texts)
	}
	failed := w.jobFixture.launch(t, a, w.fail, nil)
	mustStatus(t, w.jobFixture, failed, "failed")
	texts = map[string]bool{}
	for _, p := range slack.take(t, 2) {
		texts[fmt.Sprint(p.Body["text"])] = true
	}
	if want := fmt.Sprintf("Job FAILED! Please look at /api/v2/jobs/%d/", failed.id()); !texts[want] || !texts[fmt.Sprintf("Job %d started", failed.id())] {
		t.Errorf("failed job: want %q in %v", want, texts)
	}
	ns := a.get(fmt.Sprintf("notification_templates/%d/notifications/", def)).results()
	if len(ns) != 1 || ns[0]["status"] != "successful" || ns[0]["body"] != "" || ns[0]["recipients"] != "['#ops']" {
		t.Fatalf("notifications: %v", ns)
	}

	// Approvals, with AWX's default messages.
	wf := w.wfjt(t, nil)
	w.approval(t, wf, "Gate", 0)
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/notification_templates_approvals/", wf), map[string]any{"id": def}), 204)
	wj := w.launch(t, wf, nil)
	ap := waitApproval(t, a, wj)
	wfURL := fmt.Sprintf("%s/jobs/%d", base, wj)
	if p := slack.take(t, 1)[0]; p.Body["text"] != fmt.Sprintf(`The approval node "Gate" needs review. This node can be viewed at: %s`, wfURL) {
		t.Fatalf("approval running: %v", p.Body)
	}
	must(t, a.post(fmt.Sprintf("workflow_approvals/%d/approve/", int64(ap["id"].(float64))), nil), 204)
	if p := slack.take(t, 1)[0]; p.Body["text"] != fmt.Sprintf(`The approval node "Gate" was approved. %s`, wfURL) {
		t.Fatalf("approval approved: %v", p.Body)
	}
}
