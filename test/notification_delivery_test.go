package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/resources"
)

// deployPlaybook fails its first task on every host but h1 when broken is
// set, with a message that quotes the survey's password.
const deployPlaybook = `- name: Web servers
  hosts: all
  gather_facts: false
  tasks:
    - name: Install packages
      ansible.builtin.fail:
        msg: "repository refused {{ token }} on {{ inventory_hostname }}"
      when: (broken | default(false) | bool) and inventory_hostname != 'h1'
    - ansible.builtin.debug: msg=after
`

// syntaxPlaybook doesn't parse: its run fails with no failed task.
const syntaxPlaybook = "- hosts: all\n  tasks: [\n"

const deployToken = "tok-c0re-9f2a77"

// newDeployFixture makes a template of deployPlaybook, one host at a time
// so that h2 fails before h3, with the token as a survey password.
func newDeployFixture(t *testing.T) (*jobFixture, int64) {
	t.Helper()
	requireAnsible(t)
	f := newRepoFixture(t, map[string]string{"deploy.yml": deployPlaybook, "syntax.yml": syntaxPlaybook})
	jt := f.template(t, "deploy.yml", map[string]any{"ask_variables_on_launch": true, "survey_enabled": true, "forks": 1})
	must(t, f.a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), map[string]any{"name": "", "description": "", "spec": []any{
		map[string]any{"question_name": "Token", "variable": "token", "type": "password", "required": false}}}), 200)
	return f, jt
}

func deploy(t *testing.T, f *jobFixture, jt int64, broken bool) resp {
	t.Helper()
	job := f.launch(t, f.a, jt, map[string]any{"extra_vars": map[string]any{"token": deployToken, "broken": broken}})
	want := "successful"
	if broken {
		want = "failed"
	}
	mustStatus(t, f, job, want)
	return job
}

func attachNotification(t *testing.T, a *client, jt, nt int64, events ...string) {
	t.Helper()
	for _, ev := range events {
		must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_%s/", jt, ev), map[string]any{"id": nt}), 204)
	}
}

// apiPost is a chat.postMessage the fake Slack API received: the channel's
// name, whether given by name or by ID, and the ts it answered.
type apiPost struct {
	channel, ts string
	body        map[string]any
}

// slackAPI is a fake Slack Web API. chat.postMessage answers each message
// with its own ts and the channel's ID, C- and its name, after the
// channel's scripted answers: a Slack error, such as ratelimited, or an
// HTTP status, which comes with Retry-After: 1. chat.update edits.
type slackAPI struct {
	mu      sync.Mutex
	seq     int
	posts   []apiPost
	updates []map[string]any
	script  map[string][]string
}

func newSlackAPI(t *testing.T) *slackAPI {
	t.Helper()
	f := &slackAPI{script: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Header.Get("Authorization") != "Bearer "+slackToken {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		name := strings.TrimPrefix(fmt.Sprint(body["channel"]), "C-")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/chat.postMessage":
			if s := f.script[name]; len(s) > 0 {
				f.script[name] = s[1:]
				f.posts = append(f.posts, apiPost{channel: name, body: body})
				if code, err := strconv.Atoi(s[0]); err == nil {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(code)
					return
				}
				fmt.Fprintf(w, `{"ok":false,"error":%q}`, s[0])
				return
			}
			f.seq++
			ts := fmt.Sprintf("1700000100.%06d", f.seq)
			f.posts = append(f.posts, apiPost{channel: name, ts: ts, body: body})
			fmt.Fprintf(w, `{"ok":true,"channel":"C-%s","ts":%q}`, name, ts)
		case "/api/chat.update":
			f.updates = append(f.updates, body)
			fmt.Fprintf(w, `{"ok":true,"channel":%q,"ts":%q}`, body["channel"], body["ts"])
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	resources.SlackAPIURL = srv.URL + "/api/"
	t.Cleanup(func() { resources.SlackAPIURL = "https://slack.com/api/" })
	return f
}

// take waits for n more posts and returns them by channel.
func (f *slackAPI) take(t *testing.T, n int) map[string][]apiPost {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.posts) >= n {
			out := map[string][]apiPost{}
			for _, p := range f.posts[:n] {
				out[p.channel] = append(out[p.channel], p)
			}
			f.posts = f.posts[n:]
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("expected %d Slack posts, got %v", n, f.posts)
	return nil
}

// takeUpdates waits for n more edits and returns them by channel ID.
func (f *slackAPI) takeUpdates(t *testing.T, n int) map[string]map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.updates) >= n {
			out := map[string]map[string]any{}
			for _, u := range f.updates[:n] {
				out[fmt.Sprint(u["channel"])] = u
			}
			f.updates = f.updates[n:]
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected %d Slack edits", n)
	return nil
}

func (f *slackAPI) quiet(t *testing.T) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts) > 0 || len(f.updates) > 0 {
		t.Fatalf("unexpected Slack calls: %v %v", f.posts, f.updates)
	}
}

// scriptedHook answers each path with its statuses in turn, the last one
// from then on, 200 without one, and records when each request came. A
// 429 comes with Retry-After: 1.
type scriptedHook struct {
	srv    *httptest.Server
	mu     sync.Mutex
	codes  map[string][]int
	times  map[string][]time.Time
	bodies map[string][]string
}

func newScriptedHook(t *testing.T) *scriptedHook {
	t.Helper()
	h := &scriptedHook{codes: map[string][]int{}, times: map[string][]time.Time{}, bodies: map[string][]string{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.times[r.URL.Path] = append(h.times[r.URL.Path], time.Now())
		h.bodies[r.URL.Path] = append(h.bodies[r.URL.Path], string(b))
		code := 200
		if c := h.codes[r.URL.Path]; len(c) > 0 {
			code = c[0]
			if len(c) > 1 {
				h.codes[r.URL.Path] = c[1:]
			}
		}
		h.mu.Unlock()
		if code == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *scriptedHook) script(path string, codes ...int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.codes[path] = codes
}

func (h *scriptedHook) hits(path string) []time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Time{}, h.times[path]...)
}

func failingOf(t *testing.T, a *client, nt int64) map[string]any {
	t.Helper()
	f, _ := a.get(fmt.Sprintf("notification_templates/%d/", nt)).Body["summary_fields"].(map[string]any)["failing"].(map[string]any)
	return f
}

// TestNotificationFailureContext: a failed run's notifications name its
// first failed task, play, hosts, message and path, masked, in custom
// messages, on Teams cards and in first_error's default message; a run
// that failed with no failed task leaves them empty.
func TestNotificationFailureContext(t *testing.T) {
	f, jt := newDeployFixture(t)
	a := f.a
	slack := newSlackAPI(t)
	teams := newFakeTeams(t)
	slackCfg := func(ch string) map[string]any { return map[string]any{"token": slackToken, "channels": []any{ch}} }
	custom := newSlackTemplate(t, a, f.org, slackCfg("#custom"), map[string]any{"messages": map[string]any{"error": map[string]any{
		"message": "task={{ failure.task }}|play={{ failure.play }}|hosts={{ failure.hosts }}|n={{ failure.host_count }}|msg={{ failure.message }}|path={{ failure.path }}"}}})
	def := newSlackTemplate(t, a, f.org, slackCfg("#default"), nil)
	card := newTeamsTemplate(t, a, f.org, teams.url("/ok"), nil)
	attachNotification(t, a, jt, custom, "error")
	attachNotification(t, a, jt, def, "first_error")
	attachNotification(t, a, jt, card, "error")

	deploy(t, f, jt, false)
	failed := deploy(t, f, jt, true)
	posts := slack.take(t, 2)
	if got, want := fmt.Sprint(posts["custom"][0].body["text"]),
		"task=Install packages|play=Web servers|hosts=h2, h3|n=2|msg=repository refused $encrypted$ on h2|path=deploy.yml:5"; got != want {
		t.Errorf("custom message:\n got %s\nwant %s", got, want)
	}
	url := fmt.Sprintf("%s/jobs/%d", strings.TrimRight(app.Cfg.BaseURL, "/"), failed.id())
	if got, want := fmt.Sprint(posts["default"][0].body["text"]),
		fmt.Sprintf("Job #%d '%s' failed after a success, at task 'Install packages' on 2 hosts: %s", failed.id(), failed.Body["name"], url); got != want {
		t.Errorf("first_error:\n got %s\nwant %s", got, want)
	}
	tp := teams.take(t, 1)[0]
	c := tp.card(t)
	for k, want := range map[string]string{"Failed task": "Install packages", "Play": "Web servers", "Failed on": "h2, h3",
		"Error": "repository refused $encrypted$ on h2", "Task path": "deploy.yml:5", "Status": "failed"} {
		if c.facts[k] != want {
			t.Errorf("card fact %s: %q, want %q (%v)", k, c.facts[k], want, c.facts)
		}
	}
	raw, _ := json.Marshal(tp.Body)
	stored := a.get(fmt.Sprintf("jobs/%d/notifications/", failed.id())).Raw
	for _, text := range []string{string(raw), stored, fmt.Sprint(posts)} {
		if strings.Contains(text, deployToken) {
			t.Errorf("the token leaked: %s", text)
		}
	}

	// A run that fails before any task has no failure to name.
	broken := f.template(t, "syntax.yml", nil)
	attachNotification(t, a, broken, custom, "error")
	mustStatus(t, f, f.launch(t, a, broken, nil), "failed")
	if got := fmt.Sprint(slack.take(t, 1)["custom"][0].body["text"]); got != "task=|play=|hosts=|n=0|msg=|path=" {
		t.Errorf("no failed task: %s", got)
	}
}

// TestNotificationRetries: a 429, a 5xx, Slack's ratelimited and a network
// error are retried, Retry-After honored; a 4xx and Slack's other errors
// fail at once. Resend sends a failed notification again, three failures
// in a row mark the template failing until a success, and the leader
// resumes a notification a stopped server left pending.
func TestNotificationRetries(t *testing.T) {
	slow(t)
	a := adminClient(t)
	org := newOrg(t, a)
	hook := newScriptedHook(t)
	webhook := func(path string) int64 {
		t.Helper()
		return must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
			"notification_configuration": map[string]any{"url": hook.srv.URL + path}}), 201).id()
	}

	hook.script("/flaky", 503, 429, 200)
	if n := sendTest(t, a, webhook("/flaky")); n["status"] != "successful" || n["attempts"] != float64(3) || n["error"] != "" {
		t.Fatalf("flaky webhook: %v", n)
	}
	if hits := hook.hits("/flaky"); len(hits) != 3 || hits[2].Sub(hits[1]) < time.Second {
		t.Errorf("retries: %v, the last one before Retry-After", hits)
	}

	hook.script("/gone", 404)
	gone := webhook("/gone")
	n := sendTest(t, a, gone)
	if n["status"] != "failed" || n["attempts"] != float64(1) || !strings.Contains(fmt.Sprint(n["error"]), "404") || len(hook.hits("/gone")) != 1 {
		t.Fatalf("a 404 is final: %v", n)
	}
	sendTest(t, a, gone)
	if f := failingOf(t, a, gone); f != nil {
		t.Fatalf("failing after 2: %v", f)
	}
	sendTest(t, a, gone)
	if f := failingOf(t, a, gone); f == nil || f["failures"] != float64(3) || !strings.Contains(fmt.Sprint(f["error"]), "404 Not Found") || f["since"] == nil {
		t.Fatalf("failing after 3: %v", f)
	}

	// Resend needs change on the template, and a failed notification.
	auditor, auditorID := newUser(t, a)
	must(t, a.patch(fmt.Sprintf("users/%d/", auditorID), map[string]any{"is_system_auditor": true}), 200)
	failedID := int64(n["id"].(float64))
	must(t, auditor.post(fmt.Sprintf("notifications/%d/resend/", failedID), nil), 403)
	hook.script("/gone", 200)
	resent := int64(must(t, a.post(fmt.Sprintf("notifications/%d/resend/", failedID), nil), 202).Body["notification"].(float64))
	if r := waitNotification(t, a, resent); r["status"] != "successful" || r["subject"] != n["subject"] || resent == failedID {
		t.Fatalf("resent: %v", r)
	}
	if f := failingOf(t, a, gone); f != nil {
		t.Fatalf("failing after a success: %v", f)
	}
	must(t, a.post(fmt.Sprintf("notifications/%d/resend/", resent), nil), 400)

	// A network error is retried, and fails once the retries are spent.
	closed := httptest.NewServer(http.NotFoundHandler())
	addr := closed.URL
	closed.Close()
	down := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": addr + "/x"}}), 201).id()
	if n := sendTest(t, a, down); n["status"] != "failed" || n["attempts"] != float64(4) || !strings.Contains(fmt.Sprint(n["error"]), "connection refused") {
		t.Fatalf("unreachable: %v", n)
	}

	// Teams: a 500 is retried, a 400 isn't.
	hook.script("/teams", 500, 202)
	teamsOK := newTeamsTemplate(t, a, org, hook.srv.URL+"/teams", nil)
	if n := sendTest(t, a, teamsOK); n["status"] != "successful" || n["attempts"] != float64(2) {
		t.Fatalf("teams 500: %v", n)
	}
	hook.script("/teams-bad", 400)
	if n := sendTest(t, a, newTeamsTemplate(t, a, org, hook.srv.URL+"/teams-bad", nil)); n["status"] != "failed" || n["attempts"] != float64(1) {
		t.Fatalf("teams 400: %v", n)
	}

	// Slack: ratelimited and a 429 are retried, on those channels only;
	// channel_not_found is final.
	slack := newSlackAPI(t)
	slack.script["busy"] = []string{"ratelimited"}
	slack.script["throttled"] = []string{"429"}
	nt := newSlackTemplate(t, a, org, map[string]any{"token": slackToken, "channels": []any{"#ops", "#busy", "#throttled"}}, nil)
	if n := sendTest(t, a, nt); n["status"] != "successful" || n["attempts"] != float64(2) {
		t.Fatalf("slack retries: %v", n)
	}
	if posts := slack.take(t, 5); len(posts["ops"]) != 1 || len(posts["busy"]) != 2 || len(posts["throttled"]) != 2 {
		t.Fatalf("slack posts: %v", posts)
	}
	slack.script["missing"] = []string{"channel_not_found"}
	slack.script["busy"] = []string{"ratelimited"}
	nt = newSlackTemplate(t, a, org, map[string]any{"token": slackToken, "channels": []any{"#missing", "#busy"}}, nil)
	n = sendTest(t, a, nt)
	if want := fmt.Sprintf("Slack Notification unable to send missing: %s (channel_not_found)", n["subject"]); n["status"] != "failed" ||
		n["attempts"] != float64(2) || n["error"] != want {
		t.Fatalf("slack refusal: %v, want %s", n, want)
	}
	if posts := slack.take(t, 3); len(posts["missing"]) != 1 || len(posts["busy"]) != 2 {
		t.Fatalf("slack posts: %v", posts)
	}

	// A notification a stopped server left pending is resumed by the
	// leader; one left for days fails.
	ok := webhook("/resumed")
	var left, stale int64
	for _, row := range []struct {
		id  *int64
		ago string
	}{{&left, "2 minutes"}, {&stale, "2 days"}} {
		if err := app.DB.QueryRow(t.Context(), `INSERT INTO notifications (notification_template_id, notification_type, subject, body, attempts, next_attempt)
			VALUES ($1, 'webhook', 'left pending', '{"body": "left pending"}', 1, now() - $2::interval) RETURNING id`, ok, row.ago).Scan(row.id); err != nil {
			t.Fatal(err)
		}
	}
	if n := waitNotification(t, a, left); n["status"] != "successful" || n["attempts"] != float64(2) || len(hook.hits("/resumed")) != 1 {
		t.Fatalf("resumed: %v", n)
	}
	if n := waitNotification(t, a, stale); n["status"] != "failed" || n["error"] != "Not sent: the server stopped while sending it." {
		t.Fatalf("stale: %v", n)
	}
}

// TestSlackFailureThreads: with thread_failures, a template's first failure
// starts a thread in each channel, its later failures and the run that
// recovers reply in it, and the first message is edited to say which run
// recovered. A channel given with a thread keeps it, and a notification
// attached to errors only still ends its threads with the recovery.
func TestSlackFailureThreads(t *testing.T) {
	f, jt := newDeployFixture(t)
	a := f.a
	slack := newSlackAPI(t)
	threaded := newSlackTemplate(t, a, f.org, map[string]any{"token": slackToken, "channels": []any{"#ops", "#dev", "#pinned, 1700000000.000001"},
		"thread_failures": true}, nil)
	if cfg := a.get(fmt.Sprintf("notification_templates/%d/", threaded)).Body["notification_configuration"].(map[string]any); cfg["thread_failures"] != true {
		t.Fatalf("configuration: %v", cfg)
	}
	attachNotification(t, a, jt, threaded, "success", "error")
	errorsOnly := newSlackTemplate(t, a, f.org, map[string]any{"token": slackToken, "channels": []any{"#oncall"}, "thread_failures": true}, nil)
	attachNotification(t, a, jt, errorsOnly, "error")
	thread := func(p apiPost) string { return fmt.Sprint(p.body["thread_ts"]) }

	deploy(t, f, jt, false)
	posts := slack.take(t, 3)
	for _, ch := range []string{"ops", "dev"} {
		if p := posts[ch]; len(p) != 1 || p[0].body["thread_ts"] != nil || p[0].body["channel"] != ch {
			t.Fatalf("a success goes to %s: %v", ch, p)
		}
	}
	if thread(posts["pinned"][0]) != "1700000000.000001" {
		t.Fatalf("pinned: %v", posts["pinned"])
	}

	first := deploy(t, f, jt, true)
	posts = slack.take(t, 4)
	started := map[string]apiPost{}
	for _, ch := range []string{"ops", "dev", "oncall"} {
		if p := posts[ch]; len(p) != 1 || p[0].body["thread_ts"] != nil {
			t.Fatalf("the first failure starts a thread in %s: %v", ch, p)
		}
		started[ch] = posts[ch][0]
	}
	if thread(posts["pinned"][0]) != "1700000000.000001" {
		t.Fatalf("pinned: %v", posts["pinned"])
	}

	deploy(t, f, jt, true)
	posts = slack.take(t, 4)
	for _, ch := range []string{"ops", "dev", "oncall"} {
		if p := posts[ch]; len(p) != 1 || thread(p[0]) != started[ch].ts || p[0].body["channel"] != "C-"+ch {
			t.Fatalf("a later failure replies in %s's thread %s: %v", ch, started[ch].ts, p)
		}
	}

	recovery := deploy(t, f, jt, false)
	posts = slack.take(t, 4)
	for _, ch := range []string{"ops", "dev", "oncall"} {
		if p := posts[ch]; len(p) != 1 || thread(p[0]) != started[ch].ts {
			t.Fatalf("the recovery replies in %s's thread: %v", ch, p)
		}
	}
	url := fmt.Sprintf("%s/jobs/%d", strings.TrimRight(app.Cfg.BaseURL, "/"), recovery.id())
	if got, want := fmt.Sprint(posts["oncall"][0].body["text"]), fmt.Sprintf("Job #%d '%s' successful after a failure: %s", recovery.id(), recovery.Body["name"], url); got != want {
		t.Errorf("errors-only recovery:\n got %s\nwant %s", got, want)
	}
	edits := slack.takeUpdates(t, 3)
	for _, ch := range []string{"ops", "dev", "oncall"} {
		e := edits["C-"+ch]
		want := fmt.Sprintf("%s\nRecovered by Job #%d: %s", started[ch].body["text"], recovery.id(), url)
		if e == nil || e["ts"] != started[ch].ts || e["text"] != want {
			t.Errorf("%s's first message, edited: %v, want %q", ch, e, want)
		}
	}
	if !strings.Contains(fmt.Sprint(started["ops"].body["text"]), fmt.Sprintf("#%d", first.id())) {
		t.Errorf("the thread starts with the first failure: %v", started["ops"].body)
	}

	deploy(t, f, jt, false)
	posts = slack.take(t, 3)
	for _, ch := range []string{"ops", "dev"} {
		if p := posts[ch]; len(p) != 1 || p[0].body["thread_ts"] != nil {
			t.Fatalf("after the recovery, a success goes to %s: %v", ch, p)
		}
	}
	slack.quiet(t)
	var open int
	app.DB.QueryRow(t.Context(), "SELECT count(*) FROM slack_threads WHERE notification_template_id IN ($1, $2)", threaded, errorsOnly).Scan(&open)
	if open != 0 {
		t.Errorf("%d threads still open", open)
	}
}
