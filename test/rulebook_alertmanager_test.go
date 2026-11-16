package test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/eventstreams"
)

// The Alertmanager rulebook of testdata/rulebooks, run by ansible-rulebook
// and imported into awxnext, given the same notifications: the actions
// each runs, for which alerts, with which variables and limits, and how
// many times, are lines of testdata/rulebooks/alertmanager.expected.json.
// That file is ansible-rulebook 1.3.2's outcome as its source says:
//   - ansible.eda 2.13.0's alertmanager source puts each alert as an event,
//     its meta.hosts the instance label without its port, and the
//     notification as an event no rule here matches;
//   - match_multiple_rules false gives an event to the first rule it
//     matches only (Drools' RegisterOnlyAgendaFilter);
//   - once_within drops an event of a group that acted within the window;
//   - is match anchors its pattern at the start, is search doesn't;
//   - run_job_template's limit is job_args.limit, else the event's hosts;
//   - debug prints its msg on a line of its own.
// No alert the throttle drops matches a later rule: where Drools orders the
// throttle's cleanup rule among the others is not something to rely on.
// TestRulebookAlertmanagerDifferential checks the file against
// ansible-rulebook itself.

const (
	alertmanagerRulebook = "testdata/rulebooks/alertmanager.yml"
	alertmanagerExpected = "testdata/rulebooks/alertmanager.expected.json"
)

type amAlert struct {
	status, fingerprint, startsAt string
	labels, annotations           map[string]any
}

func amNotification(status string, alerts ...amAlert) []byte {
	var list []map[string]any
	for _, a := range alerts {
		ends := "0001-01-01T00:00:00Z"
		if a.status == "resolved" {
			ends = "2026-11-16T09:00:00Z"
		}
		list = append(list, map[string]any{"status": a.status, "fingerprint": a.fingerprint, "startsAt": a.startsAt, "endsAt": ends,
			"labels": a.labels, "annotations": a.annotations, "generatorURL": "http://prometheus:9090/graph"})
	}
	b, _ := json.Marshal(map[string]any{"version": "4", "groupKey": "{}:{}", "status": status, "receiver": "eda",
		"externalURL": "http://alertmanager:9093", "groupLabels": map[string]any{}, "commonLabels": map[string]any{}, "alerts": list})
	return b
}

func alertOn(name, host, fp, start string, labels map[string]any, summary string) amAlert {
	l := map[string]any{"alertname": name, "instance": host + ":9100", "host": host}
	for k, v := range labels {
		l[k] = v
	}
	return amAlert{status: "firing", fingerprint: fp, startsAt: start, labels: l, annotations: map[string]any{"summary": summary}}
}

func resolved(a amAlert) amAlert {
	a.status = "resolved"
	return a
}

// alertmanagerNotifications are what Alertmanager sends, in order: a group
// sent again with an alert added, a flapping alert, alerts no rule takes,
// an alert two rules match, two alerts resolved, and the sentinel that ends
// the scenario.
func alertmanagerNotifications() [][]byte {
	nginx := alertOn("ServiceDown", "h1", "a1", "2026-11-16T08:00:00Z", map[string]any{"service": "nginx", "severity": "critical"}, "nginx is down on h1")
	disk := alertOn("DiskFull", "h2", "a2", "2026-11-16T08:01:00Z", map[string]any{"mountpoint": "/var/log", "severity": "warning"}, "/var/log is full")
	postgres := alertOn("ServiceDown", "h1", "a3", "2026-11-16T08:02:00Z", map[string]any{"service": "postgres", "severity": "warning"}, "postgres is down")
	flap := nginx
	flap.startsAt = "2026-11-16T08:10:00Z"
	unreachable := alertOn("HostUnreachable", "h3", "a4", "2026-11-16T08:11:00Z", map[string]any{"severity": "critical"}, "h3 is unreachable")
	home := alertOn("DiskAlmostFull", "h3", "a5", "2026-11-16T08:12:00Z", map[string]any{"mountpoint": "/home", "severity": "warning"}, "/home is almost full")
	noService := alertOn("ServiceDown", "h2", "a6", "2026-11-16T08:13:00Z", map[string]any{"severity": "warning"}, "a service is down")
	// Both Clean a full disk and Escalate an unreachable host match it: the
	// first rule takes it.
	lib := alertOn("DiskFull", "h3", "a7", "2026-11-16T08:14:00Z", map[string]any{"mountpoint": "/var/lib", "severity": "critical"}, "/var/lib is full: the database is unreachable")
	sentinel := alertOn("Sentinel", "h1", "a9", "2026-11-16T08:30:00Z", nil, "the end")
	return [][]byte{
		amNotification("firing", nginx, disk),
		amNotification("firing", nginx, disk, postgres),
		amNotification("firing", flap, unreachable, home, noService, lib),
		amNotification("resolved", resolved(nginx), resolved(disk)),
		amNotification("firing", sentinel),
	}
}

type amFixture struct {
	f                  *jobFixture
	orgName            string
	templates          map[int64]string
	restart, clean, pg string
}

func newAMFixture(t *testing.T, f *jobFixture) *amFixture {
	t.Helper()
	x := &amFixture{f: f, templates: map[int64]string{}}
	x.orgName = must(t, f.a.get(fmt.Sprintf("organizations/%d/", f.org)), 200).Body["name"].(string)
	prompts := map[string]any{"ask_variables_on_launch": true, "ask_limit_on_launch": true}
	for _, role := range []string{"restart", "clean", "page"} {
		id := f.template(t, "site.yml", prompts)
		x.templates[id] = role
	}
	return x
}

func (x *amFixture) templateName(t *testing.T, role string) string {
	for id, r := range x.templates {
		if r == role {
			return must(t, x.f.a.get(fmt.Sprintf("job_templates/%d/", id)), 200).Body["name"].(string)
		}
	}
	t.Fatalf("no %s template", role)
	return ""
}

func (x *amFixture) variables(t *testing.T, port int) map[string]any {
	return map[string]any{"alertmanager_port": port, "organization": x.orgName,
		"restart_template": x.templateName(t, "restart"), "clean_template": x.templateName(t, "clean"), "page_template": x.templateName(t, "page")}
}

// launches are the jobs of the fixture's templates, as outcome lines:
// those of awxnext's rules (fromRules), or the others, ansible-rulebook's.
func (x *amFixture) launches(t *testing.T, fromRules bool) []string {
	t.Helper()
	var out []string
	for id, role := range x.templates {
		for _, j := range must(t, x.f.a.get(fmt.Sprintf("jobs/?job_template=%d&page_size=200", id)), 200).results() {
			if _, byRule := j["summary_fields"].(map[string]any)["event_rule"]; byRule != fromRules {
				continue
			}
			out = append(out, launchLine(t, role, j))
		}
	}
	sort.Strings(out)
	return out
}

// launchLine is what a job says of the action that launched it: the rule,
// the template, the limit, the variables, and the event.
func launchLine(t *testing.T, role string, job map[string]any) string {
	t.Helper()
	vars := jobVars(t, job)
	eda, _ := vars["ansible_eda"].(map[string]any)
	delete(vars, "ansible_eda")
	ev, _ := eda["event"].(map[string]any)
	what := "event " + compactJSON(t, withoutMeta(ev))
	if alert, ok := ev["alert"].(map[string]any); ok {
		labels := alert["labels"].(map[string]any)
		what = fmt.Sprintf("alert %v %v %v %v, hosts %v", labels["alertname"], alert["fingerprint"], alert["startsAt"], alert["status"],
			compactJSON(t, ev["meta"].(map[string]any)["hosts"]))
	}
	return fmt.Sprintf("launch %s: %v / %v, limit %q, vars %s, %s", role, eda["ruleset"], eda["rule"], job["limit"], compactJSON(t, vars), what)
}

func withoutMeta(ev map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range ev {
		if k != "meta" {
			out[k] = v
		}
	}
	return out
}

func compactJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// awxnextOutcome imports the rulebook, turns its rules on, posts the
// notifications to its stream, and returns what the rules did.
func (x *amFixture) awxnextOutcome(t *testing.T) []string {
	t.Helper()
	a := x.f.a
	text, err := os.ReadFile(alertmanagerRulebook)
	if err != nil {
		t.Fatal(err)
	}
	rb := must(t, a.post("rulebooks/", map[string]any{"name": uniq("alertmanager") + ".yml", "project": x.f.project, "rulesets": string(text),
		"variables": x.variables(t, 5050)}), 201)
	report := rb.Body["report"].(map[string]any)
	if s := report["summary"].(map[string]any); s["rules"] != float64(6) || s["translated"] != float64(6) {
		t.Fatalf("report: %s", compactJSON(t, report))
	}
	id := rb.id()
	streams := rulebookObjects(t, a, id, "event_streams")
	st := streams["Alertmanager / alerts"]
	if len(streams) != 1 || st["stream_type"] != eventstreams.TypeAlertmanager {
		t.Fatalf("streams: %v", streams)
	}
	cred := streamCredential(t, a, x.f.org, eventstreams.CredToken, map[string]any{"token": "tok"})
	must(t, a.patch(fmt.Sprintf("event_streams/%v/", st["id"]), map[string]any{"credential": cred}), 200)
	rules := rulebookObjects(t, a, id, "event_rules")
	for _, r := range rules {
		must(t, a.patch(fmt.Sprintf("event_rules/%v/", r["id"]), map[string]any{"enabled": true, "mode": "auto"}), 200)
	}
	s := eventStream{id: int64(st["id"].(float64)), path: "event_streams/" + st["uuid"].(string) + "/post/"}
	for _, n := range alertmanagerNotifications() {
		if code, b, _ := s.post(t, string(n), headers("Authorization", "Bearer tok", "Content-Type", "application/json")); code != 202 {
			t.Fatalf("posting alerts: %d %v", code, b)
		}
	}
	waitRules(t, s.id)
	out := x.launches(t, true)
	var all []string
	for name, r := range rules {
		for _, d := range must(t, a.get(fmt.Sprintf("event_rules/%v/decisions/?not__outcome=not_matched&order_by=sequence&page_size=200", r["id"])), 200).results() {
			all = append(all, fmt.Sprintf("%s: event %v %v: %v", name, d["sequence"], d["outcome"], d["reason"]))
			if d["outcome"] == "logged" {
				out = append(out, fmt.Sprintf("log: %s", d["details"].(map[string]any)["message"]))
			}
		}
	}
	t.Cleanup(func() {
		if t.Failed() {
			sort.Strings(all)
			t.Logf("awxnext's decisions:\n  %s", strings.Join(all, "\n  "))
		}
	})
	sort.Strings(out)
	return out
}

func readExpected(t *testing.T) []string {
	t.Helper()
	var want []string
	b, err := os.ReadFile(alertmanagerExpected)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	return want
}

// diffLines prints both outcomes as JSON on a mismatch, ready to replace
// the expected file's lines or to compare with them.
func diffLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	j := func(v []string) string {
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	}
	t.Errorf("%s differs from %s.\ngot:\n%s\nwant:\n%s", what, alertmanagerExpected, j(got), j(want))
}

// onceAnAlert is ansible-rulebook's outcome as awxnext's differs from it
// on purpose: an Alertmanager stream takes an alert again only when its
// status or start changes, as the import's report says, so the action
// ansible-rulebook runs again for an alert Alertmanager sends again,
// awxnext runs once. Each launch line names its alert's fingerprint, start
// and status, so a repeated line is such an action.
func onceAnAlert(lines []string) []string {
	var out []string
	for i, l := range lines {
		if i > 0 && l == lines[i-1] && strings.Contains(l, ", alert ") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// The imported Alertmanager rulebook does what ansible-rulebook does with
// it, the expected file, but for the alerts Alertmanager sends again.
func TestRulebookAlertmanager(t *testing.T) {
	slow(t)
	x := newAMFixture(t, newLaunchFixture(t))
	want := readExpected(t)
	if len(onceAnAlert(want)) == len(want) {
		t.Errorf("the expected outcome has no alert Alertmanager sent again: %v", want)
	}
	diffLines(t, "awxnext's outcome, but for alerts sent again,", x.awxnextOutcome(t), onceAnAlert(want))
}

// requireRulebook returns the ansible-rulebook command. full.yml sets
// AWXNEXT_TEST_ANSIBLE_RULEBOOK and installs it with Java, so there a
// missing piece fails the test.
func requireRulebook(t *testing.T) string {
	t.Helper()
	missing := t.Skipf
	cmd := os.Getenv("AWXNEXT_TEST_ANSIBLE_RULEBOOK")
	if cmd != "" {
		missing = t.Fatalf
	} else {
		cmd = "ansible-rulebook"
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		missing("ansible-rulebook is not installed: %v", err)
		return ""
	}
	if _, err := exec.LookPath("java"); err != nil && os.Getenv("JAVA_HOME") == "" {
		missing("ansible-rulebook needs Java 17 or later: %v", err)
	}
	return path
}

// syncBuffer is ansible-rulebook's output, written and read at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rulebookOutcome runs ansible-rulebook with awxnext as its controller,
// posts the notifications to its Alertmanager source, and returns what it
// did: the jobs it launched, and what its debug actions printed.
func (x *amFixture) rulebookOutcome(t *testing.T, bin string) []string {
	t.Helper()
	a := x.f.a
	dir := t.TempDir()
	port := freePort(t)
	varsFile := filepath.Join(dir, "vars.json")
	b, _ := json.Marshal(x.variables(t, port))
	if err := os.WriteFile(varsFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(alertmanagerRulebook)
	if err != nil {
		t.Fatal(err)
	}
	token := must(t, a.post("tokens/", map[string]any{"description": "ansible-rulebook"}), 201).Body["token"].(string)
	home, _ := os.UserHomeDir()
	collections := strings.Join(append([]string{filepath.Join(home, ".cache", "awxnext", "collections")},
		filepath.SplitList(os.Getenv("ANSIBLE_COLLECTIONS_PATH"))...), string(os.PathListSeparator))
	cmd := exec.Command(bin, "--rulebook", abs, "--vars", varsFile, "--controller-url", srv.URL, "--controller-token", token,
		"--controller-ssl-verify", "no", "-v")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ANSIBLE_COLLECTIONS_PATH="+collections, "ANSIBLE_LOCAL_TEMP="+filepath.Join(dir, "tmp"),
		"EDA_JOB_TEMPLATE_REFRESH_DELAY=0.2", "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("ansible-rulebook:\n%s", out.String())
		}
	})
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(90 * time.Second)
	for {
		if res, err := http.Get(url); err == nil {
			res.Body.Close()
			break
		}
		select {
		case <-exited:
			t.Fatalf("ansible-rulebook exited: %v\n%s", waitErr, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("ansible-rulebook's Alertmanager source never listened on %d:\n%s", port, out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, n := range alertmanagerNotifications() {
		res, err := http.Post(url+"alerts", "application/json", bytes.NewReader(n))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("ansible-rulebook took the alerts with %d", res.StatusCode)
		}
	}
	// ansible-rulebook runs the actions of a ruleset one after the other,
	// each job to its end: it is done once the sentinel's job and every
	// one before it ended, and no job follows them. The page that
	// post_event leads to can come after the sentinel's: the posted event
	// is matched only when its action's turn comes.
	var launched []string
	stable := time.Time{}
	deadline = time.Now().Add(3 * time.Minute)
	for {
		select {
		case <-exited:
			t.Fatalf("ansible-rulebook exited: %v\n%s", waitErr, out.String())
		default:
		}
		lines, running := x.rulebookJobs(t)
		done := !running && slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, `"alert":"sentinel"`) })
		switch {
		case !done || !slices.Equal(lines, launched):
			launched, stable = lines, time.Time{}
		case stable.IsZero():
			stable = time.Now()
		case time.Since(stable) > 10*time.Second:
			return append(launched, rulebookLogs(out.String())...)
		}
		if time.Now().After(deadline) {
			t.Fatalf("ansible-rulebook never finished: %v\n%s", launched, out.String())
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// rulebookJobs are the launches ansible-rulebook made, and whether one of
// their jobs still runs.
func (x *amFixture) rulebookJobs(t *testing.T) ([]string, bool) {
	t.Helper()
	running := false
	for id := range x.templates {
		for _, j := range must(t, x.f.a.get(fmt.Sprintf("jobs/?job_template=%d&page_size=200", id)), 200).results() {
			if _, byRule := j["summary_fields"].(map[string]any)["event_rule"]; !byRule && !slices.Contains(finalStatuses, j["status"]) {
				running = true
			}
		}
	}
	return x.launches(t, false), running
}

var finalStatuses = []any{"successful", "failed", "error", "canceled"}

// rulebookLogs are the messages ansible-rulebook's debug actions printed.
func rulebookLogs(out string) []string {
	var logs []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); strings.HasPrefix(line, "LOG ") {
			logs = append(logs, "log: "+line)
		}
	}
	return logs
}

// The differential test: ansible-rulebook 1.3.2 itself, with awxnext as
// its controller, does what the expected file says, and so does the same
// rulebook imported into awxnext, but for the alerts Alertmanager sends
// again (onceAnAlert).
func TestRulebookAlertmanagerDifferential(t *testing.T) {
	bin := requireRulebook(t)
	x := newAMFixture(t, newJobFixture(t))
	want := readExpected(t)
	theirs := x.rulebookOutcome(t, bin)
	sort.Strings(theirs)
	diffLines(t, "ansible-rulebook's outcome", theirs, want)
	ours := x.awxnextOutcome(t)
	diffLines(t, "awxnext's outcome, but for alerts sent again,", ours, onceAnAlert(want))
	b, _ := json.MarshalIndent(map[string][]string{"ansible-rulebook": theirs, "awxnext": ours}, "", "  ")
	t.Logf("outcomes:\n%s", b)
}
