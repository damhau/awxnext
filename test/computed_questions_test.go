package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const computedPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "hostname={{ hostname }} gateway={{ gateway }} vlan_id={{ vlan_id }} ({{ vlan_id | type_debug }}) owner={{ owner_tag }} ref={{ request_ref }} expires={{ expires }} pw={{ db_password }} pwlen={{ db_password | length }}"
      run_once: true
    - ansible.builtin.copy:
        content: "{{ db_password }}"
        dest: "OUT/pw-{{ awx_job_id }}"
      run_once: true
`

// secretOutput is what publish.yml publishes as the secret db_password.
const secretOutput = "S3cret-0utput-Value"

const publishPlaybook = `- hosts: all
  gather_facts: false
  vars:
    made: "` + secretOutput + `"
  tasks:
    - ansible.builtin.set_stats:
        data:
          db_password: "{{ made }}"
          db_host: db1.example.com
      run_once: true
    - ansible.builtin.debug:
        msg: "after set_stats: {{ made }}"
      run_once: true
`

const consumePlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "who={{ who }} got={{ db_password }} len={{ db_password | length }} host={{ db_host }}"
      run_once: true
    - ansible.builtin.copy:
        content: "{{ db_password }}"
        dest: "OUT/got-{{ awx_job_id }}"
      run_once: true
`

// newComputedFixture: a project of the playbooks above, writing what they
// are handed in clear to a directory the test reads.
func newComputedFixture(t *testing.T) (*wfFixture, string) {
	t.Helper()
	f := newJobFixture(t)
	out := t.TempDir()
	repo := gitRepo(t, map[string]string{
		"computed.yml": strings.ReplaceAll(computedPlaybook, "OUT", out),
		"publish.yml":  publishPlaybook,
		"consume.yml":  strings.ReplaceAll(consumePlaybook, "OUT", out),
	})
	f.project = must(t, f.a.post("projects/", map[string]any{"name": uniq("computed"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	f.waitProject(t)
	return &wfFixture{jobFixture: f}, out
}

func readOut(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// allEvents is the raw JSON of a job's events.
func allEvents(t *testing.T, a *client, job int64) string {
	t.Helper()
	return must(t, a.get(fmt.Sprintf("jobs/%d/job_events/?page_size=200", job)), 200).Raw
}

// A survey's computed questions are never asked: the server computes them
// at launch from the item picked in a list, the earlier answers, the user,
// their team and the portal request, and refuses a launch, a schedule or a
// catalog item that sets them. A generated password reaches the job
// encrypted and masked.
func TestComputedQuestions(t *testing.T) {
	w, out := newComputedFixture(t)
	a := w.a
	vlans := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode([]any{
			map[string]any{"vid": 120, "name": "app-120", "gateway": "10.1.20.1"},
			map[string]any{"vid": 130, "name": "db-130", "gateway": "10.1.30.1"},
		})
	}))
	t.Cleanup(vlans.Close)
	setJobsSettings(t, a, map[string]any{"CHOICE_SOURCE_HOSTS": []any{vlans.URL}, "CHOICE_SOURCE_ALLOWED_LOCAL_ADDRESSES": []any{"127.0.0.1"}})
	src := must(t, a.post("choice_sources/", map[string]any{"name": uniq("vlans"), "organization": w.org, "kind": "http", "url": vlans.URL,
		"value": "string(item.vid)", "label": "item.name"}), 201).id()

	jt := w.template(t, "computed.yml", map[string]any{"survey_enabled": true})
	spec := fmt.Sprintf("job_templates/%d/survey_spec/", jt)
	question := func(variable, typ, valueFrom string, required bool) map[string]any {
		return map[string]any{"question_name": variable, "variable": variable, "type": typ, "required": required, "value_from": valueFrom}
	}
	questions := []any{
		map[string]any{"question_name": "VLAN", "variable": "vlan", "type": "multiplechoice", "required": true,
			"choices_from": map[string]any{"type": "source", "source": src}},
		map[string]any{"question_name": "Stage", "variable": "env", "type": "multiplechoice", "choices": []any{"dev", "prod"}, "default": "dev", "required": true},
		question("hostname", "text", `answers.env + "-" + user.username + "-" + item("vlan").name`, true),
		question("gateway", "text", `item("vlan").gateway`, true),
		question("vlan_id", "integer", `item("vlan").vid`, true),
		question("owner_tag", "text", `team?.name ?? user.username`, true),
		question("request_ref", "text", `request == nil ? "launch" : "req-" + string(request.id)`, true),
		question("expires", "text", `request?.expires_at ?? "never"`, true),
		question("db_password", "password", "password(24)", true),
	}

	// Checked when the survey is saved.
	for name, bad := range map[string]map[string]any{
		"on a list":               {"question_name": "x", "variable": "x", "type": "multiplechoice", "choices": []any{"a"}, "required": false, "value_from": `"a"`},
		"with a default":          {"question_name": "x", "variable": "x", "type": "text", "required": false, "value_from": `"a"`, "default": "b"},
		"item of a text question": question("x", "text", `item("hostname")`, false),
		"a later question":        question("x", "text", `answers.later`, false),
		"a password answer":       question("x", "text", `answers.db_password`, false),
		"password() on text":      question("x", "text", `password(12)`, false),
		"a password too short":    question("x", "password", `password(4)`, false),
		"not an expression":       question("x", "text", `1 +`, false),
	} {
		r := must(t, a.post(spec, map[string]any{"name": "s", "description": "", "spec": append(append([]any{}, questions...), bad)}), 400)
		if !strings.Contains(strings.ToLower(r.Raw), "survey question 9") {
			t.Errorf("%s: %s", name, r.Raw)
		}
	}
	must(t, a.post(spec, map[string]any{"name": "s", "description": "", "spec": questions}), 200)

	// Never asked: the launch asks only for the VLAN.
	info := must(t, a.get(fmt.Sprintf("job_templates/%d/launch/", jt)), 200).Body
	if fmt.Sprint(info["variables_needed_to_start"]) != "[vlan]" {
		t.Errorf("variables needed: %v", info["variables_needed_to_start"])
	}

	user, uid := newUser(t, a)
	grant(t, a, "JobTemplate Execute", uid, jt)
	username := must(t, user.get("me/"), 200).results()[0]["username"]

	// A launch that sets a computed variable is refused, a schedule too.
	r := must(t, user.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": map[string]any{"vlan": "120", "hostname": "mine"}}), 400)
	if !strings.Contains(r.Raw, `'hostname' is computed by the server and can't be set at launch.`) {
		t.Errorf("launch setting hostname: %s", r.Raw)
	}
	r = must(t, a.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": uniq("s"), "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY;COUNT=1",
		"extra_data": map[string]any{"vlan": "120", "db_password": "mine"}}), 400)
	if !strings.Contains(r.Raw, `'db_password' is computed by the server`) {
		t.Errorf("schedule setting db_password: %s", r.Raw)
	}

	// A launch: from the picked VLAN, the answers and the user.
	job := w.jobFixture.launch(t, user, jt, map[string]any{"extra_vars": map[string]any{"vlan": "120", "env": "prod"}})
	mustStatus(t, w.jobFixture, job, "successful")
	stdout := w.stdout(t, job.id())
	want := fmt.Sprintf("hostname=prod-%s-app-120 gateway=10.1.20.1 vlan_id=120 (int) owner=%s ref=launch expires=never pw=$encrypted$ pwlen=24", username, username)
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout, want %q:\n%s", want, stdout)
	}
	pw := readOut(t, out, fmt.Sprintf("pw-%d", job.id()))
	if len(pw) != 24 || strings.Trim(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("generated password %q", pw)
	}
	seen := a.get(fmt.Sprintf("jobs/%d/", job.id())).Raw
	for where, text := range map[string]string{"stdout": stdout, "events": allEvents(t, a, job.id()), "job": seen} {
		if strings.Contains(text, pw) {
			t.Errorf("the generated password is in the %s", where)
		}
	}
	if !strings.Contains(seen, `\"db_password\": \"$encrypted$\"`) || !strings.Contains(seen, `\"hostname\": \"prod-`) {
		t.Errorf("job extra_vars: %s", seen)
	}
	var stored string
	app.DB.QueryRow(context.Background(), "SELECT extra_vars FROM unified_jobs WHERE id = $1", job.id()).Scan(&stored)
	if !strings.Contains(stored, `"db_password": "$encrypted$v1$`) {
		t.Errorf("stored extra_vars: %s", stored)
	}

	// A portal request: its id, expiry and team.
	must(t, a.post("catalog_items/", map[string]any{"name": uniq("vm"), "unified_job_template": jt,
		"questions": map[string]any{"hostname": map[string]any{"value": "fixed"}}}), 400)
	item := must(t, a.post("catalog_items/", map[string]any{"name": uniq("vm"), "unified_job_template": jt, "kind": "deployment",
		"default_lifetime_days": 3}), 201).id()
	requester, rid := newUser(t, a)
	grant(t, a, "CatalogItem Requester", rid, item)
	team := must(t, a.post("teams/", map[string]any{"name": uniq("team"), "organization": w.org}), 201)
	must(t, a.post(fmt.Sprintf("teams/%d/users/", team.id()), map[string]any{"id": rid}), 204)
	path := fmt.Sprintf("catalog_items/%d/request/", item)
	if got := variables(must(t, requester.get(path), 200).Body["questions"]); fmt.Sprint(got) != "[vlan env]" {
		t.Errorf("the item asks %v", got)
	}
	r = must(t, requester.post(path, map[string]any{"answers": map[string]any{"vlan": "130", "expires": "never"}}), 400)
	if !strings.Contains(r.Raw, `'expires' is computed by the server`) {
		t.Errorf("request setting expires: %s", r.Raw)
	}
	req := must(t, requester.post(path, map[string]any{"answers": map[string]any{"vlan": "130"}}), 201).Body
	rjob := waitJob(t, a, fmt.Sprintf("jobs/%d/", int64(req["job"].(float64))))
	mustStatus(t, w.jobFixture, rjob, "successful")
	created, err := time.Parse(time.RFC3339Nano, req["created"].(string))
	if err != nil {
		t.Fatal(err)
	}
	stdout = w.stdout(t, rjob.id())
	want = fmt.Sprintf("gateway=10.1.30.1 vlan_id=130 (int) owner=%s ref=req-%v expires=%s", team.Body["name"], req["id"], created.Add(72*time.Hour).UTC().Format("2006-01-02T15:04:05"))
	if !strings.Contains(stdout, want) {
		t.Errorf("request stdout, want %q:\n%s", want, stdout)
	}
	if pw2 := readOut(t, out, fmt.Sprintf("pw-%d", rjob.id())); len(pw2) != 24 || pw2 == pw || strings.Contains(stdout, pw2) {
		t.Errorf("the request's password %q (launch's %q)", pw2, pw)
	}
}

// A secret output is hidden from the set_stats event on, stored encrypted,
// shown as $encrypted$, and handed to the next workflow node as a survey
// password is. The node computes its questions as the workflow's launcher.
func TestSecretOutputs(t *testing.T) {
	w, out := newComputedFixture(t)
	a := w.a
	outputs := []any{
		map[string]any{"name": "db_password", "type": "string", "secret": true},
		map[string]any{"name": "db_host", "type": "string"},
	}
	must(t, a.post("job_templates/", map[string]any{"name": uniq("jt"), "project": w.project, "inventory": w.inv, "playbook": "publish.yml",
		"outputs": []any{map[string]any{"name": "hosts", "type": "list", "secret": true}}}), 400)
	publish := w.template(t, "publish.yml", map[string]any{"outputs": outputs, "verbosity": 1})
	if got := fmt.Sprint(a.get(fmt.Sprintf("job_templates/%d/", publish)).Body["outputs"]); got != "[map[name:db_password required:false secret:true type:string] map[name:db_host required:false type:string]]" {
		t.Errorf("outputs: %s", got)
	}

	job := w.jobFixture.launch(t, a, publish, map[string]any{})
	mustStatus(t, w.jobFixture, job, "successful")
	stdout := w.stdout(t, job.id())
	events := allEvents(t, a, job.id())
	for where, text := range map[string]string{"stdout": stdout, "events": events, "job": a.get(fmt.Sprintf("jobs/%d/", job.id())).Raw} {
		if strings.Contains(text, secretOutput) {
			t.Errorf("the secret output is in the %s", where)
		}
	}
	if !strings.Contains(stdout, `"db_password": "$encrypted$"`) || !strings.Contains(stdout, "after set_stats: $encrypted$") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if art := fmt.Sprint(job.Body["artifacts"]); art != "map[db_host:db1.example.com db_password:$encrypted$]" {
		t.Errorf("artifacts: %s", art)
	}
	var stored, host string
	app.DB.QueryRow(context.Background(), "SELECT artifacts->>'db_password', artifacts->>'db_host' FROM unified_jobs WHERE id = $1", job.id()).Scan(&stored, &host)
	if !strings.HasPrefix(stored, "$encrypted$v1$") || host != "db1.example.com" {
		t.Errorf("stored artifacts: %q %q", stored, host)
	}
	stats := must(t, a.get(fmt.Sprintf("jobs/%d/job_events/?event=playbook_on_stats", job.id())), 200).results()
	if len(stats) != 1 || fmt.Sprint(stats[0]["event_data"].(map[string]any)["artifact_data"]) != "map[db_host:db1.example.com db_password:$encrypted$]" {
		t.Errorf("stats event: %v", stats)
	}

	// The next node gets the value, and computes who launched the workflow.
	consume := w.template(t, "consume.yml", map[string]any{"survey_enabled": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", consume), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Who", "variable": "who", "type": "text", "required": true, "value_from": "user.username"},
	}}), 200)
	wf := w.wfjt(t, nil)
	first := w.node(t, wf, publish, map[string]any{"identifier": "publish"})
	second := w.node(t, wf, consume, map[string]any{"identifier": "consume"})
	w.link(t, first, "success", second)
	wj := w.launch(t, wf, nil)
	if done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj)); done.Body["status"] != "successful" {
		t.Fatalf("workflow: %v (%v)", done.Body["status"], done.Body["job_explanation"])
	}
	nodes := w.nodeByID(t, wj)
	next := int64(nodeJob(nodes["consume"])["id"].(float64))
	stdout = w.stdout(t, next)
	if !strings.Contains(stdout, fmt.Sprintf("who=admin got=$encrypted$ len=%d host=db1.example.com", len(secretOutput))) {
		t.Errorf("next node's stdout:\n%s", stdout)
	}
	if got := readOut(t, out, fmt.Sprintf("got-%d", next)); got != secretOutput {
		t.Errorf("the next node got %q", got)
	}
	nextJob := a.get(fmt.Sprintf("jobs/%d/", next)).Raw
	if strings.Contains(nextJob, secretOutput) || strings.Contains(allEvents(t, a, next), secretOutput) || !strings.Contains(nextJob, `\"db_password\": \"$encrypted$\"`) {
		t.Errorf("next job: %s", nextJob)
	}
	wfNodes := must(t, a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", wj)), 200).Raw
	if strings.Contains(wfNodes, secretOutput) || strings.Contains(wfNodes, "$encrypted$v1$") {
		t.Errorf("workflow nodes: %s", wfNodes)
	}
}
