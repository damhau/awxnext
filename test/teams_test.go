package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const teamsSig = "Zm9vYmFyYmF6cXV4c2lnbmF0dXJl"

// teamsPost is one post a fake Teams workflow received.
type teamsPost struct {
	ContentType string
	Body        map[string]any
}

// card is the Adaptive Card of a post.
func (p teamsPost) card(t *testing.T) teamsCardView {
	t.Helper()
	atts, _ := p.Body["attachments"].([]any)
	if p.Body["type"] != "message" || len(atts) != 1 {
		t.Fatalf("not a card message: %v", p.Body)
	}
	att := atts[0].(map[string]any)
	content, _ := att["content"].(map[string]any)
	if att["contentType"] != "application/vnd.microsoft.card.adaptive" || content["type"] != "AdaptiveCard" || content["version"] != "1.4" {
		t.Fatalf("attachment: %v", att)
	}
	var v teamsCardView
	body := content["body"].([]any)
	head := body[0].(map[string]any)
	v.title, v.color = fmt.Sprint(head["text"]), fmt.Sprint(head["color"])
	v.facts = map[string]string{}
	if len(body) > 1 {
		for _, f := range body[1].(map[string]any)["facts"].([]any) {
			f := f.(map[string]any)
			v.facts[fmt.Sprint(f["title"])] = fmt.Sprint(f["value"])
		}
	}
	if acts, ok := content["actions"].([]any); ok && len(acts) == 1 {
		act := acts[0].(map[string]any)
		if act["type"] != "Action.OpenUrl" {
			t.Fatalf("action: %v", act)
		}
		v.action, v.link = fmt.Sprint(act["title"]), fmt.Sprint(act["url"])
	}
	return v
}

type teamsCardView struct {
	title, color, action, link string
	facts                      map[string]string
}

// fakeTeams answers as a Teams workflow's webhook trigger: 202 at /ok, and
// Power Automate's error at /gone, quoting the signature.
type fakeTeams struct {
	srv   *httptest.Server
	mu    sync.Mutex
	posts []teamsPost
}

func newFakeTeams(t *testing.T) *fakeTeams {
	t.Helper()
	f := &fakeTeams{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if r.Method != http.MethodPost || json.Unmarshal(b, &body) != nil || r.URL.Query().Get("sig") != teamsSig {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.posts = append(f.posts, teamsPost{ContentType: r.Header.Get("Content-Type"), Body: body})
		f.mu.Unlock()
		if r.URL.Path == "/proxy" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "<html><body>Bad gateway</body></html>")
			return
		}
		if r.URL.Path == "/throttled" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "Rate limit is exceeded. Try again in 1 seconds.\n")
			return
		}
		if r.URL.Path == "/gone" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":{"code":"WorkflowNotFound","message":"The workflow with signature '%s' could not be found."}}`, teamsSig)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTeams) url(path string) string {
	return f.srv.URL + path + "?api-version=1&sp=%2Ftriggers%2Fmanual%2Frun&sv=1.0&sig=" + teamsSig
}

// take waits for n more posts and returns them in the order received.
func (f *fakeTeams) take(t *testing.T, n int) []teamsPost {
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
	t.Fatalf("expected %d Teams posts", n)
	return nil
}

func newTeamsTemplate(t *testing.T, a *client, org int64, url string, extra map[string]any) int64 {
	t.Helper()
	body := map[string]any{"name": uniq("teams"), "organization": org, "notification_type": "teams", "notification_configuration": map[string]any{"url": url}}
	for k, v := range extra {
		body[k] = v
	}
	return must(t, a.post("notification_templates/", body), 201).id()
}

func storedURL(t *testing.T, nt int64) string {
	t.Helper()
	var stored string
	app.DB.QueryRow(t.Context(), "SELECT notification_configuration->>'url' FROM notification_templates WHERE id = $1", nt).Scan(&stored)
	plain, err := app.Crypto.Decrypt(stored)
	if err != nil {
		t.Fatalf("stored url %q: %v", stored, err)
	}
	return plain
}

func TestTeamsNotificationValidation(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	opts := must(t, a.do("OPTIONS", "notification_templates/", nil), 200).Body
	choices := fmt.Sprint(opts["actions"].(map[string]any)["POST"].(map[string]any)["notification_type"].(map[string]any)["choices"])
	if !strings.Contains(choices, "[teams Microsoft Teams]") {
		t.Errorf("type choices: %s", choices)
	}
	for _, c := range []struct {
		cfg  map[string]any
		want string
	}{
		{map[string]any{}, "Missing required fields for Notification Configuration: ['url']."},
		{map[string]any{"url": 5}, "Configuration field 'url' incorrect type, expected password."},
		{map[string]any{"url": ""}, "url must be the http(s) URL of the Teams workflow."},
		{map[string]any{"url": "ftp://example.com/x"}, "url must be the http(s) URL of the Teams workflow."},
		{map[string]any{"url": "https://example.com/x", "headers": map[string]any{}}, `Unexpected field "headers" for teams notifications.`},
	} {
		r := must(t, a.post("notification_templates/", map[string]any{"name": uniq("teams"), "organization": org, "notification_type": "teams",
			"notification_configuration": c.cfg}), 400)
		if fmt.Sprint(r.Body["notification_configuration"]) != "["+c.want+"]" {
			t.Errorf("%v: %s", c.cfg, r.Raw)
		}
	}

	// The URL is a secret: stored encrypted, shown as $encrypted$, kept by
	// the placeholder.
	u := "https://prod-01.westeurope.logic.azure.com/workflows/abc/triggers/manual/paths/invoke?sig=" + teamsSig
	nt := newTeamsTemplate(t, a, org, u, nil)
	r := a.get(fmt.Sprintf("notification_templates/%d/", nt))
	if fmt.Sprint(r.Body["notification_configuration"]) != "map[url:$encrypted$]" || strings.Contains(r.Raw, teamsSig) {
		t.Fatalf("config: %s", r.Raw)
	}
	if storedURL(t, nt) != u {
		t.Fatal("stored url")
	}
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", nt), map[string]any{"notification_configuration": map[string]any{"url": "$encrypted$"},
		"messages": map[string]any{"started": map[string]any{"message": "{{ job.name }} started", "body": "ignored"}}}), 200)
	if storedURL(t, nt) != u {
		t.Fatal("url after saving the placeholder")
	}

	// A placeholder takes the old value of the same field, as in AWX, and a
	// secret is stored encrypted whatever type it came from.
	hook := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": "https://example.com/hook"}}), 201).id()
	must(t, a.patch(fmt.Sprintf("notification_templates/%d/", hook), map[string]any{"notification_type": "teams",
		"notification_configuration": map[string]any{"url": "$encrypted$"}}), 200)
	if storedURL(t, hook) != "https://example.com/hook" {
		t.Fatal("url after the type change")
	}
}

// Teams notifications against a fake workflow (roadmap M11-03).
func TestTeamsNotificationSend(t *testing.T) {
	teams := newFakeTeams(t)
	a := adminClient(t)
	org := newOrg(t, a)

	nt := newTeamsTemplate(t, a, org, teams.url("/ok"), nil)
	n := sendTest(t, a, nt)
	subject := fmt.Sprintf("Tower Notification Test %d %s", nt, app.Cfg.BaseURL)
	if n["status"] != "successful" || n["notifications_sent"] != float64(1) || n["notification_type"] != "teams" || n["recipients"] != "" || n["subject"] != subject {
		t.Fatalf("notification: %v", n)
	}
	p := teams.take(t, 1)[0]
	if c := p.card(t); p.ContentType != "application/json" || c.title != subject || len(c.facts) != 0 || c.link != "" {
		t.Errorf("test card: %+v %+v", p, c)
	}
	if body, _ := json.Marshal(n["body"]); !strings.Contains(string(body), `"AdaptiveCard"`) {
		t.Errorf("stored body: %s", body)
	}

	// An answer other than 2xx fails the notification with the status and
	// the workflow's message, which never shows the signature.
	nt = newTeamsTemplate(t, a, org, teams.url("/gone"), nil)
	n = sendTest(t, a, nt)
	if n["status"] != "failed" || n["notifications_sent"] != float64(0) ||
		n["error"] != "Teams answered 404 Not Found: WorkflowNotFound: The workflow with signature 'REDACTED' could not be found." {
		t.Fatalf("refused: %v", n)
	}
	teams.take(t, 1)

	for path, want := range map[string]string{"/proxy": "Teams answered 502 Bad Gateway", "/throttled": "Teams answered 429 Too Many Requests: Rate limit is exceeded. Try again in 1 seconds."} {
		nt = newTeamsTemplate(t, a, org, teams.url(path), nil)
		if n := sendTest(t, a, nt); n["status"] != "failed" || n["error"] != want {
			t.Errorf("%s: %v", path, n)
		}
		teams.take(t, 1)
	}

	// An unreachable workflow fails without the URL in the error.
	gone := httptest.NewServer(http.NotFoundHandler())
	addr := gone.URL
	gone.Close()
	nt = newTeamsTemplate(t, a, org, addr+"/x?sig="+teamsSig, nil)
	n = sendTest(t, a, nt)
	if e := fmt.Sprint(n["error"]); n["status"] != "failed" || !strings.HasPrefix(e, "Teams notification failed: ") || strings.Contains(e, teamsSig) || strings.Contains(e, "/x") {
		t.Fatalf("unreachable: %v", n)
	}
}

func TestTeamsJobNotifications(t *testing.T) {
	teams := newFakeTeams(t)
	w := newWorkflowFixture(t)
	a := w.a
	base := strings.TrimRight(app.Cfg.BaseURL, "/")
	custom := newTeamsTemplate(t, a, w.org, teams.url("/ok"), map[string]any{"messages": map[string]any{
		"error": map[string]any{"message": "{{ job.name }} failed, see {{ url }}"},
	}})
	for _, ev := range []string{"started", "success", "error"} {
		must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_%s/", w.site, ev), map[string]any{"id": custom}), 204)
	}
	must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_error/", w.fail), map[string]any{"id": custom}), 204)

	ok := w.jobFixture.launch(t, a, w.site, nil)
	mustStatus(t, w.jobFixture, ok, "successful")
	url := fmt.Sprintf("%s/jobs/%d", base, ok.id())
	cards := map[string]teamsCardView{}
	for _, p := range teams.take(t, 2) {
		c := p.card(t)
		cards[c.facts["Status"]] = c
	}
	tpl := fmt.Sprint(ok.Body["name"])
	started, done := cards["running"], cards["successful"]
	if started.title != fmt.Sprintf("Job #%d '%s' running: %s", ok.id(), tpl, url) || started.facts["Template"] != tpl ||
		started.facts["Launched by"] != "admin" || started.facts["Started"] == "" || started.facts["Finished"] != "" || started.color != "<nil>" ||
		started.action != "Open the job" || started.link != url {
		t.Errorf("started card: %+v", started)
	}
	if done.title != fmt.Sprintf("Job #%d '%s' successful: %s", ok.id(), tpl, url) || done.color != "Good" ||
		!strings.HasSuffix(done.facts["Finished"], " UTC") || done.facts["Elapsed"] == "" || done.link != url {
		t.Errorf("success card: %+v", done)
	}

	failed := w.jobFixture.launch(t, a, w.fail, nil)
	mustStatus(t, w.jobFixture, failed, "failed")
	url = fmt.Sprintf("%s/jobs/%d", base, failed.id())
	if c := teams.take(t, 1)[0].card(t); c.title != fmt.Sprintf("%s failed, see %s", failed.Body["name"], url) || c.color != "Attention" ||
		c.facts["Status"] != "failed" || c.link != url {
		t.Errorf("failure card: %+v", c)
	}

	// An approval's card opens the approvals while it waits, and the
	// workflow job once decided.
	def := newTeamsTemplate(t, a, w.org, teams.url("/ok"), nil)
	wf := w.wfjt(t, nil)
	w.approval(t, wf, "Gate", 0)
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/notification_templates_approvals/", wf), map[string]any{"id": def}), 204)
	wj := w.launch(t, wf, nil)
	ap := waitApproval(t, a, wj)
	wfURL := fmt.Sprintf("%s/jobs/%d", base, wj)
	wfName := fmt.Sprint(a.get(fmt.Sprintf("workflow_jobs/%d/", wj)).Body["name"])
	c := teams.take(t, 1)[0].card(t)
	if c.title != fmt.Sprintf(`The approval node "Gate" needs review. This node can be viewed at: %s`, wfURL) || c.color != "Warning" ||
		c.facts["Status"] != "Waiting for approval" || c.facts["Approval"] != "Gate" || c.facts["Workflow"] != wfName ||
		c.action != "Review the approval" || c.link != base+"/approvals" {
		t.Errorf("approval running card: %+v", c)
	}
	must(t, a.post(fmt.Sprintf("workflow_approvals/%d/approve/", int64(ap["id"].(float64))), nil), 204)
	c = teams.take(t, 1)[0].card(t)
	if c.title != fmt.Sprintf(`The approval node "Gate" was approved. %s`, wfURL) || c.color != "Good" || c.facts["Status"] != "Approved" ||
		c.action != "Open the workflow job" || c.link != wfURL {
		t.Errorf("approval approved card: %+v", c)
	}
	ns := a.get(fmt.Sprintf("notification_templates/%d/notifications/", def)).results()
	var statuses []string
	for _, n := range ns {
		statuses = append(statuses, fmt.Sprint(n["status"]))
	}
	if !slices.Equal(statuses, []string{"successful", "successful"}) {
		t.Fatalf("notifications: %v", ns)
	}
}
