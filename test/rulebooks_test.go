package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/eventstreams"
)

// syncProject updates a project and waits for the update to end.
func syncProject(t *testing.T, a *client, project int64) resp {
	t.Helper()
	up := must(t, a.post(fmt.Sprintf("projects/%d/update/", project), nil), 202)
	pu := waitJob(t, a, fmt.Sprintf("project_updates/%v/", up.Body["project_update"]))
	if pu.Body["status"] != "successful" {
		t.Fatalf("project update: %s\n%s", pu.Raw, a.get(fmt.Sprintf("project_updates/%d/stdout/?format=txt", pu.id())).Raw)
	}
	return pu
}

// rulebookObjects lists what a rulebook's imports made of a kind:
// event_streams, event_connectors or event_rules, by name.
func rulebookObjects(t *testing.T, a *client, rulebook int64, kind string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, o := range must(t, a.get(fmt.Sprintf("rulebooks/%d/%s/?page_size=200", rulebook, kind)), 200).results() {
		name := o["name"].(string)
		if _, rest, ok := strings.Cut(name, ": "); ok {
			name = rest
		}
		out[name] = o
	}
	return out
}

// reportRule finds a rule's report in a rulebook's.
func reportRule(t *testing.T, report map[string]any, ruleset, rule string) map[string]any {
	t.Helper()
	for _, rs := range report["rulesets"].([]any) {
		rs := rs.(map[string]any)
		if rs["name"] != ruleset {
			continue
		}
		for _, r := range rs["rules"].([]any) {
			if r := r.(map[string]any); r["name"] == rule {
				return r
			}
		}
	}
	t.Fatalf("no rule %s / %s in the report %v", ruleset, rule, report)
	return nil
}

// Every example rulebook of ansible-rulebook and ansible.eda imports
// through the API: each rule is in the report, translated into objects
// tied to the rulebook, or not with a reason, as the translation's golden
// file says; the templates the examples name don't exist here, which the
// report says too.
func TestRulebookImportExamples(t *testing.T) {
	slow(t)
	f := newLaunchFixture(t)
	a := f.a
	files, err := filepath.Glob("../internal/rulebooks/testdata/examples/*/*.yml")
	if err != nil || len(files) < 100 {
		t.Fatalf("%d example rulebooks: %v", len(files), err)
	}
	var golden map[string]struct {
		Report struct {
			Rulesets []struct {
				Name  string
				Rules []struct {
					Name    string
					Status  string
					Actions []struct{ Action, Status, As string }
				}
			}
		}
	}
	b, err := os.ReadFile("../internal/rulebooks/testdata/examples.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	vars := map[string]any{"repo": "https://github.com/example/app.git", "src_path": "/tmp/app", "ansible_eda": nil}
	var rules, made, missing int
	for _, file := range files {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(filepath.Dir(file)) + "/" + filepath.Base(file)
		rb := must(t, a.post("rulebooks/", map[string]any{"name": name, "project": f.project, "rulesets": string(text), "variables": vars}), 201)
		if rb.Body["import_error"] != "" || rb.Body["imported"] == nil || rb.Body["organization"] != float64(f.org) {
			t.Errorf("%s: %s", name, rb.Raw)
			continue
		}
		var sets []map[string]any
		if err := yaml.Unmarshal(text, &sets); err != nil {
			t.Fatal(err)
		}
		inYAML := 0
		for _, rs := range sets {
			list, _ := rs["rules"].([]any)
			inYAML += len(list)
		}
		report := rb.Body["report"].(map[string]any)
		if n := report["summary"].(map[string]any)["rules"]; n != float64(inYAML) {
			t.Errorf("%s: the report has %v rules, the rulebook %d", name, n, inYAML)
		}
		rules += inYAML
		g := golden[name].Report
		objects := 0
		for i, rs := range report["rulesets"].([]any) {
			rs := rs.(map[string]any)
			for j, rr := range rs["rules"].([]any) {
				rr := rr.(map[string]any)
				gr := g.Rulesets[i].Rules[j]
				for k, ar := range rr["actions"].([]any) {
					ar := ar.(map[string]any)
					ga := gr.Actions[k]
					where := fmt.Sprintf("%s: %s / %s: %v", name, rs["name"], rr["name"], ar["action"])
					switch {
					case ar["status"] == ga.Status && ga.Status == "translated" && ga.As != "decisions":
						obj, _ := ar["object"].(map[string]any)
						if obj == nil {
							t.Errorf("%s: translated without an object", where)
							continue
						}
						got := must(t, a.get(fmt.Sprintf("%ss/%v/", obj["type"], obj["id"])), 200)
						if got.Body["rulebook"] != rb.Body["id"] || got.Body["enabled"] != false || got.Body["mode"] != "shadow" || got.Body["owner"] != nil {
							t.Errorf("%s: %s", where, got.Raw)
						}
						objects++
					case ar["status"] == ga.Status:
						if ga.Status != "translated" && ar["reason"] == "" {
							t.Errorf("%s: not translated without a reason", where)
						}
					case ga.Status == "translated" && ga.As == "launch":
						if r := fmt.Sprint(ar["reason"]); !strings.HasPrefix(r, "No job template ") && !strings.HasPrefix(r, "No workflow template ") {
							t.Errorf("%s: %v %q", where, ar["status"], r)
						}
						missing++
					default:
						t.Errorf("%s: %v (%v), the translation says %s", where, ar["status"], ar["reason"], ga.Status)
					}
				}
			}
		}
		made += objects
		id := rb.id()
		if n := len(rulebookObjects(t, a, id, "event_rules")); n != objects {
			t.Errorf("%s: %d rules, %d in the report", name, n, objects)
		}
	}
	t.Logf("%d rulebooks, %d rules: %d rules made, %d launches whose template is missing", len(files), rules, made, missing)
	if made == 0 || missing == 0 {
		t.Errorf("made %d, missing %d", made, missing)
	}
}

// rulebookFiles are a project's files with rulebooks in both places EDA
// looks for them, a YAML file that is no rulebook, one too deep, and a
// playbook.
func rulebookFiles(web string) map[string]string {
	return map[string]string{
		"site.yml":                           sitePlaybook,
		"extensions/eda/rulebooks/web.yml":   web,
		"rulebooks/ops.yml":                  opsRulebook,
		"rulebooks/vars.yml":                 "app: web\nport: 8080\n",
		"rulebooks/nested/deeper.yml":        opsRulebook,
		"extensions/eda/rulebooks/README.md": "Rulebooks.\n",
	}
}

const opsRulebook = `- name: Ops
  hosts: all
  sources:
    - ansible.eda.range:
        limit: 3
  rules:
    - name: Count
      condition: event.i == 2
      action:
        print_event:
`

func webRulebook(org, jt, condition string, extra string) string {
	return fmt.Sprintf(`- name: Web
  hosts: all
  sources:
    - ansible.eda.webhook:
        host: 0.0.0.0
        port: 5000
  rules:
    - name: Deploy
      condition: %s
      action:
        run_job_template:
          name: %s
          organization: %s
          job_args:
            extra_vars:
              repo: "{{ event.payload.repo }}"
            limit: web
    - name: Stop
      condition: event.payload.action == "stop"
      action:
        shutdown:
%s`, condition, jt, org, extra)
}

// A project's rulebooks are imported at each sync: a rule's translation
// changes with its rulebook only, what an operator decides stays, a rule
// gone from the rulebook goes, and a rulebook gone from the project goes
// with its objects. Whoever sees the project sees its rulebooks.
func TestRulebookProjectSync(t *testing.T) {
	slow(t)
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	orgName := must(t, a.get(fmt.Sprintf("organizations/%d/", org)), 200).Body["name"].(string)
	jtName := uniq("deploy")
	repo := gitRepo(t, rulebookFiles(webRulebook(orgName, jtName, `event.payload.repo == "app"`, "")))
	project := must(t, a.post("projects/", map[string]any{"name": uniq("rb"), "organization": org, "scm_type": "git", "scm_url": repo}), 201).id()
	f := &jobFixture{a: a, org: org, project: project}
	f.waitProject(t)
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	jt := must(t, a.post("job_templates/", map[string]any{"name": jtName, "project": project, "inventory": inv, "playbook": "site.yml",
		"ask_variables_on_launch": true}), 201).id()
	must(t, a.post(fmt.Sprintf("projects/%d/import_rulebooks/", project), nil), 200)

	list := must(t, a.get(fmt.Sprintf("projects/%d/rulebooks/?order_by=name", project)), 200).results()
	if len(list) != 2 || list[0]["name"] != "ops.yml" || list[0]["path"] != "rulebooks/ops.yml" || list[1]["name"] != "web.yml" ||
		list[1]["path"] != "extensions/eda/rulebooks/web.yml" || list[1]["revision"] == "" {
		t.Fatalf("rulebooks: %v", list)
	}
	ops, web := int64(list[0]["id"].(float64)), int64(list[1]["id"].(float64))
	webRB := list[1]
	if s := webRB["report"].(map[string]any)["summary"].(map[string]any); s["rules"] != float64(2) || s["translated"] != float64(1) || s["not_translated"] != float64(1) {
		t.Errorf("summary %v", s)
	}
	stop := reportRule(t, webRB["report"].(map[string]any), "Web", "Stop")
	if a := stop["actions"].([]any)[0].(map[string]any); a["status"] != "not_translated" || !strings.Contains(fmt.Sprint(a["reason"]), "shutdown") {
		t.Errorf("stop: %v", stop)
	}
	deploy := reportRule(t, webRB["report"].(map[string]any), "Web", "Deploy")
	if !strings.Contains(fmt.Sprint(deploy["notes"]), "doesn't prompt for a limit") {
		t.Errorf("deploy notes: %v", deploy["notes"])
	}

	streams := rulebookObjects(t, a, web, "event_streams")
	stream := streams["Web / ansible.eda.webhook"]
	if len(streams) != 1 || stream == nil || stream["stream_type"] != eventstreams.TypeGeneric || stream["credential"] != nil || stream["organization"] != float64(org) {
		t.Fatalf("streams: %v", streams)
	}
	rules := rulebookObjects(t, a, web, "event_rules")
	rule := rules["Web / Deploy"]
	if len(rules) != 1 || rule["enabled"] != false || rule["mode"] != "shadow" || rule["owner"] != nil ||
		rule["unified_job_template"] != float64(jt) || rule["limit"] != "" || rule["rulebook"] != float64(web) {
		t.Fatalf("rules: %v", rules)
	}
	if v := rule["extra_vars"].(map[string]any); v["repo"] != "event.data.repo" || v["ansible_eda"] == nil {
		t.Errorf("extra_vars %v", v)
	}
	if c := rulebookObjects(t, a, ops, "event_connectors"); len(c) != 1 || c["Ops / ansible.eda.range"]["enabled"] != false ||
		c["Ops / ansible.eda.range"]["source"] != "ansible.eda.range" {
		t.Errorf("connectors: %v", c)
	}
	if r := rulebookObjects(t, a, ops, "event_rules")["Ops / Count"]; r == nil || r["action"] != "log" || r["message"] != "event.data" {
		t.Errorf("ops rule: %v", r)
	}

	// What the rulebook says is the rulebook's; what an operator decides
	// is theirs.
	rid := int64(rule["id"].(float64))
	ruleURL := fmt.Sprintf("event_rules/%d/", rid)
	if r := must(t, a.patch(ruleURL, map[string]any{"filter": "true"}), 409); !strings.Contains(r.Raw, "change filter in the rulebook") {
		t.Errorf("filter: %s", r.Raw)
	}
	must(t, a.delete(ruleURL), 409)
	must(t, a.delete(fmt.Sprintf("event_streams/%v/", stream["id"])), 409)
	must(t, a.patch(fmt.Sprintf("rulebooks/%d/", web), map[string]any{"rulesets": "[]"}), 409)
	must(t, a.delete(fmt.Sprintf("rulebooks/%d/", web)), 409)
	cred := streamCredential(t, a, org, eventstreams.CredToken, map[string]any{"token": "tok"})
	must(t, a.patch(fmt.Sprintf("event_streams/%v/", stream["id"]), map[string]any{"credential": cred}), 200)
	on := must(t, a.patch(ruleURL, map[string]any{"enabled": true, "mode": "auto", "description": "on call", "max_concurrent_runs": 2}), 200)
	if on.Body["owner"] == nil {
		t.Errorf("enabled without an owner: %s", on.Raw)
	}

	// The rulebook changes: the rule follows, keeping the operator's
	// choices; a new rule starts disabled; a removed file takes its
	// rulebook and objects with it.
	commitFiles(t, repo, map[string]string{"extensions/eda/rulebooks/web.yml": webRulebook(orgName, jtName, `event.payload.repo == "api"`, `    - name: Tag
      condition: event.payload.tag is defined
      action:
        post_event:
          event:
            tagged: "{{ event.payload.tag }}"
`)}, "change web")
	if err := os.Remove(filepath.Join(repo, "rulebooks/ops.yml")); err != nil {
		t.Fatal(err)
	}
	commitFiles(t, repo, map[string]string{}, "remove ops")
	opsRules := rulebookObjects(t, a, ops, "event_rules")
	syncProject(t, a, project)
	rules = rulebookObjects(t, a, web, "event_rules")
	rule = rules["Web / Deploy"]
	if len(rules) != 2 || rule == nil || rule["id"] != float64(rid) || !strings.Contains(rule["filter"].(string), `"api"`) || rule["enabled"] != true ||
		rule["mode"] != "auto" || rule["description"] != "on call" || rule["max_concurrent_runs"] != float64(2) || rule["owner"] != on.Body["owner"] {
		t.Errorf("rule after the change: %v", rule)
	}
	if tag := rules["Web / Tag"]; tag == nil || tag["enabled"] != false || tag["action"] != "post_event" || tag["target_stream"] != stream["id"] {
		t.Errorf("new rule: %v", tag)
	}
	must(t, a.get(fmt.Sprintf("rulebooks/%d/", ops)), 404)
	must(t, a.get(fmt.Sprintf("event_rules/%v/", opsRules["Ops / Count"]["id"])), 404)
	if n := must(t, a.get(fmt.Sprintf("projects/%d/rulebooks/", project)), 200).count(); n != 1 {
		t.Errorf("%d rulebooks after removing one", n)
	}

	// A rulebook that can't be read keeps the objects of the import before.
	commitFiles(t, repo, map[string]string{"extensions/eda/rulebooks/web.yml": "- name: Web\n  rules: [\n"}, "break web")
	syncProject(t, a, project)
	rb := must(t, a.get(fmt.Sprintf("rulebooks/%d/", web)), 200)
	if !strings.Contains(fmt.Sprint(rb.Body["import_error"]), "YAML") {
		t.Errorf("import_error %v", rb.Body["import_error"])
	}
	if n := len(rulebookObjects(t, a, web, "event_rules")); n != 2 {
		t.Errorf("%d rules after a broken import", n)
	}

	// Whoever sees the project sees its rulebooks; changing them takes the
	// right to change the project.
	viewer, vid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Project Use"), "user": vid, "object_id": project}), 201)
	if r := must(t, viewer.get(fmt.Sprintf("rulebooks/%d/", web)), 200); r.Body["report"] == nil {
		t.Errorf("viewer: %s", r.Raw)
	}
	upload := map[string]any{"name": "mine.yml", "project": project, "rulesets": opsRulebook}
	must(t, viewer.post("rulebooks/", upload), 403)
	must(t, viewer.post(fmt.Sprintf("rulebooks/%d/import/", web), nil), 403)
	stranger, _ := newUser(t, a)
	must(t, stranger.get(fmt.Sprintf("rulebooks/%d/", web)), 403)
	if n := must(t, stranger.get("rulebooks/"), 200).count(); n != 0 {
		t.Errorf("a stranger sees %d rulebooks", n)
	}

	// An uploaded rulebook is the API's: changed, it is imported again;
	// deleted, it takes its objects with it.
	up := must(t, a.post("rulebooks/", upload), 201)
	upID := up.id()
	if up.Body["path"] != "" || up.Body["report"].(map[string]any)["summary"].(map[string]any)["rules"] != float64(1) {
		t.Errorf("upload: %s", up.Raw)
	}
	must(t, a.patch(fmt.Sprintf("rulebooks/%d/", upID), map[string]any{"rulesets": strings.Replace(opsRulebook, "event.i == 2", "event.i == 3", 1)}), 200)
	r := rulebookObjects(t, a, upID, "event_rules")["Ops / Count"]
	if r == nil || !strings.Contains(r["filter"].(string), "== 3") {
		t.Errorf("rule after the upload changed: %v", r)
	}
	if bad := must(t, a.post("rulebooks/", map[string]any{"name": "bad.yml", "project": project, "rulesets": "- name: [\n"}), 400); !strings.Contains(bad.Raw, "rulesets") {
		t.Errorf("bad upload: %s", bad.Raw)
	}
	conn := rulebookObjects(t, a, upID, "event_connectors")["Ops / ansible.eda.range"]
	must(t, a.delete(fmt.Sprintf("rulebooks/%d/", upID)), 204)
	must(t, a.get(fmt.Sprintf("event_rules/%v/", r["id"])), 404)
	must(t, a.get(fmt.Sprintf("event_connectors/%v/", conn["id"])), 404)
}

// What follows a launch: a failed job is launched again after the
// rulebook's delay, and the artifacts of a job are posted to the ruleset,
// where another rule takes them.
func TestRulebookFollowups(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	orgName := must(t, a.get(fmt.Sprintf("organizations/%d/", f.org)), 200).Body["name"].(string)
	prompts := map[string]any{"ask_variables_on_launch": true, "ask_limit_on_launch": true}
	failing := must(t, a.get(fmt.Sprintf("job_templates/%d/", f.template(t, "fail.yml", prompts))), 200).Body["name"]
	site := must(t, a.get(fmt.Sprintf("job_templates/%d/", f.template(t, "site.yml", prompts))), 200).Body["name"]
	text := fmt.Sprintf(`- name: Follow
  hosts: all
  sources:
    - ansible.eda.range:
        limit: 1
  rules:
    - name: Retry
      condition: event.kind == "retry"
      action:
        run_job_template:
          name: %[1]s
          organization: %[3]s
          retries: 1
          delay: 1
          include_events: false
          job_args:
            limit: h2
    - name: Deploy
      condition: event.kind == "deploy"
      action:
        run_job_template:
          name: %[2]s
          organization: %[3]s
          post_events: true
          job_args:
            extra_vars:
              version: v2
            limit: h1
    - name: Deployed
      condition: event.deployed_version == "v2"
      action:
        debug:
          msg: "deployed {{ event.deployed_version }}"
`, failing, site, orgName)
	rb := must(t, a.post("rulebooks/", map[string]any{"name": "follow.yml", "project": f.project, "rulesets": text}), 201)
	if s := rb.Body["report"].(map[string]any)["summary"].(map[string]any); s["translated"] != float64(3) {
		t.Fatalf("report: %s", rb.Raw)
	}
	rbID := rb.id()
	streams := rulebookObjects(t, a, rbID, "event_streams")
	ce := streams["Follow"]
	if ce == nil || ce["stream_type"] != eventstreams.TypeCloudEvents {
		t.Fatalf("streams %v", streams)
	}
	cred := streamCredential(t, a, f.org, eventstreams.CredToken, map[string]any{"token": "tok"})
	must(t, a.patch(fmt.Sprintf("event_streams/%v/", ce["id"]), map[string]any{"credential": cred}), 200)
	rules := rulebookObjects(t, a, rbID, "event_rules")
	for _, r := range rules {
		must(t, a.patch(fmt.Sprintf("event_rules/%v/", r["id"]), map[string]any{"enabled": true, "mode": "auto"}), 200)
	}
	retry, deploy, deployed := rules["Follow / Retry"], rules["Follow / Deploy"], rules["Follow / Deployed"]
	if retry["retries"] != float64(1) || retry["retry_delay"] != float64(1) || deploy["post_artifacts"] != "event" || deployed["action"] != "log" {
		t.Fatalf("rules %v", rules)
	}
	s := eventStream{id: int64(ce["id"].(float64)), path: "event_streams/" + ce["uuid"].(string) + "/post/"}
	send := func(kind string) {
		code, b, _ := s.post(t, fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":"/test","type":"generic","data":{"kind":%q}}`, uniq(kind), kind),
			headers("Authorization", "Bearer tok", "Content-Type", "application/cloudevents+json"))
		if code != 202 {
			t.Fatalf("posting %s: %d %v", kind, code, b)
		}
	}
	send("retry")
	send("deploy")

	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %s", what)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	retryID := int64(retry["id"].(float64))
	waitFor("the retry", func() bool { return len(ruleJobs(t, a, retryID)) == 2 })
	jobs := ruleJobs(t, a, retryID)
	first, second := waitJob(t, a, fmt.Sprintf("jobs/%v/", jobs[0]["id"])), waitJob(t, a, fmt.Sprintf("jobs/%v/", jobs[1]["id"]))
	mustStatus(t, f, first, "failed")
	mustStatus(t, f, second, "failed")
	if second.Body["limit"] != "h2" || jobVars(t, second.Body)["ansible_eda"] != nil {
		t.Errorf("retry: limit %v, vars %v", second.Body["limit"], second.Body["extra_vars"])
	}
	created, _ := time.Parse(time.RFC3339Nano, second.Body["created"].(string))
	finished, _ := time.Parse(time.RFC3339Nano, first.Body["finished"].(string))
	if created.Sub(finished) < time.Second {
		t.Errorf("the retry was created at %v, less than its delay after the job it retries finished at %v", created, finished)
	}
	deployedID := int64(deployed["id"].(float64))
	waitFor("the posted artifacts", func() bool {
		return must(t, a.get(fmt.Sprintf("event_rules/%d/decisions/?outcome=logged", deployedID)), 200).count() == 1
	})
	d := must(t, a.get(fmt.Sprintf("event_rules/%d/decisions/?outcome=logged", deployedID)), 200).results()[0]
	if d["reason"] != "deployed v2" || d["details"].(map[string]any)["message"] != "deployed v2" {
		t.Errorf("logged: %v", d)
	}
	evs := s.events(t, a)
	posted := evs[len(evs)-1]
	if posted["event_type"] != "post_event" || !strings.HasPrefix(fmt.Sprint(posted["source"]), fmt.Sprintf("/api/v2/event_rules/%v/", deploy["id"])) {
		t.Errorf("posted %v", posted)
	}
	// Nothing more: a second retry, or a post of the failed jobs' artifacts.
	time.Sleep(2 * time.Second)
	if n := len(ruleJobs(t, a, retryID)); n != 2 {
		t.Errorf("%d jobs of the retried rule", n)
	}
	var open int
	if err := app.DB.QueryRow(t.Context(), "SELECT count(*) FROM event_rule_followups WHERE rule_id = ANY($1) AND state <> 'done'",
		[]int64{retryID, int64(deploy["id"].(float64))}).Scan(&open); err != nil || open != 0 {
		t.Errorf("%d follow-ups open: %v", open, err)
	}
	if job := ruleJobs(t, a, int64(deploy["id"].(float64))); len(job) != 1 || job[0]["status"] != "successful" {
		t.Errorf("deploy jobs: %v", job)
	}
}
