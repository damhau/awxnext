package test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// maskingPlaybook prints every secret it is handed, as given, JSON-escaped
// once and twice, and line by line; runDir/env/passwords holds the passwords
// given at launch.
const maskingPlaybook = `- hosts: all
  gather_facts: false
  vars:
    run_dir: "{{ lookup('env', 'ANSIBLE_SSH_CONTROL_PATH_DIR') | dirname }}"
  tasks:
    - name: "a task named after {{ lookup('env', 'MASK_TOKEN') }}"
      ansible.builtin.debug:
        msg: "given {{ lookup('env', 'MASK_TOKEN') }} looked {{ lookup('env', 'MASK_LOOKED') }} survey {{ survey_pw }} pin {{ lookup('env', 'MASK_PIN') }} region {{ lookup('env', 'MASK_REGION') }}"
      run_once: true
    - ansible.builtin.debug:
        msg: "{{ {'token': lookup('env', 'MASK_TOKEN'), 'survey': survey_pw} | to_json }}"
      run_once: true
    - ansible.builtin.debug:
        msg: "{{ {'token': lookup('env', 'MASK_TOKEN')} | to_json(ensure_ascii=False) }}"
      run_once: true
    - ansible.builtin.debug:
        msg: "{{ lookup('env', 'MASK_KEY') }}"
      run_once: true
    - ansible.builtin.debug:
        msg: "{{ lookup('env', 'MASK_KEY').splitlines() }}"
      run_once: true
    - ansible.builtin.command: "cat {{ run_dir }}/env/passwords"
      changed_when: false
      run_once: true
    - ansible.builtin.debug:
        msg: "{{ lookup('file', run_dir ~ '/env/passwords') | from_json }}"
      run_once: true
`

// The secrets: each holds a core that no masked form leaves behind.
const (
	maskToken  = `Tk"\ü-qZ7tokencore`
	maskLooked = "from-vault-v2"
	maskSurvey = "surv3y-Pass!core"
	maskSSH    = "prompt-ssh-core1"
	maskBecome = "prompt-become-core2"
	maskVault  = "prompt-vault-core3"
)

var (
	maskKey   = "kEyLine1-core\nkEyLine2-core\nkEyLine3-core\n"
	maskCores = []string{"qZ7tokencore", "from-vault-v2", "surv3y-Pass!core", "prompt-ssh-core1", "prompt-become-core2", "prompt-vault-core3",
		"kEyLine1-core", "kEyLine2-core", "kEyLine3-core"}
)

func leakedCores(text string) []string {
	var out []string
	for _, c := range maskCores {
		if strings.Contains(text, c) {
			out = append(out, c)
		}
	}
	return out
}

// wsCollector keeps the raw job_events messages of a job.
type wsCollector struct {
	mu   sync.Mutex
	msgs []string
}

func (w *wsCollector) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.msgs, "\n")
}

func collectJobEvents(t *testing.T, job int64) *wsCollector {
	t.Helper()
	conn, err := wsDial(t, basicHeader("admin", "admin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if m := wsRead(t, conn, 5*time.Second); m["accept"] != true {
		t.Fatalf("hello: %v", m)
	}
	wsSend(t, conn, map[string]any{"groups": map[string]any{"job_events": []any{job}}})
	w := &wsCollector{}
	go func() {
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			if bytes.Contains(b, []byte(`"group_name":"job_events"`)) {
				w.mu.Lock()
				w.msgs = append(w.msgs, string(b))
				w.mu.Unlock()
			}
		}
	}()
	return w
}

// TestSecretMasking: a job prints each kind of secret it is handed in each
// form; its stored output and events, websocket messages, log aggregator
// records and archive hold none of them.
func TestSecretMasking(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	ctx := context.Background()
	commitFiles(t, f.repo, map[string]string{"masking.yml": maskingPlaybook}, "masking")
	must(t, a.post(fmt.Sprintf("projects/%d/update/", f.project), nil), 202)
	time.Sleep(300 * time.Millisecond)
	f.waitProject(t)

	logs, lsrv := httpReceiver(t, false)
	t.Cleanup(func() { a.delete("settings/logging/") })
	must(t, a.patch("settings/logging/", map[string]any{"LOG_AGGREGATOR_ENABLED": true, "LOG_AGGREGATOR_LEVEL": "INFO", "LOG_AGGREGATOR_HOST": lsrv.URL + "/logs",
		"LOG_AGGREGATOR_TYPE": "other", "LOG_AGGREGATOR_PROTOCOL": "https", "LOG_AGGREGATOR_LOGGERS": []string{"job_events"}}), 200)

	vault := fakeVaultKV(t)
	lookup := must(t, a.post("credentials/", map[string]any{"name": uniq("kv"), "organization": f.org, "credential_type": credType(t, a, "hashivault_kv"),
		"inputs": map[string]any{"url": vault.URL, "token": "root", "api_version": "v2"}}), 201).id()
	ct := must(t, a.post("credential_types/", map[string]any{"name": uniq("masked"), "kind": "cloud",
		"inputs": map[string]any{"fields": []any{
			map[string]any{"id": "token", "label": "Token", "secret": true},
			map[string]any{"id": "key", "label": "Key", "secret": true, "multiline": true},
			map[string]any{"id": "looked", "label": "Looked up", "secret": true},
			map[string]any{"id": "pin", "label": "PIN", "secret": true},
			map[string]any{"id": "region", "label": "Region"}}},
		"injectors": map[string]any{"env": map[string]any{"MASK_TOKEN": "{{ token }}", "MASK_KEY": "{{ key }}", "MASK_LOOKED": "{{ looked }}",
			"MASK_PIN": "{{ pin }}", "MASK_REGION": "{{ region }}"}}}), 201).id()
	cloud := must(t, a.post("credentials/", map[string]any{"name": uniq("masked"), "organization": f.org, "credential_type": ct,
		"inputs": map[string]any{"token": maskToken, "key": maskKey, "pin": "x9z", "region": "eu-west-9"}}), 201).id()
	must(t, a.post(fmt.Sprintf("credentials/%d/input_sources/", cloud), map[string]any{"source_credential": lookup, "input_field_name": "looked",
		"metadata": map[string]any{"secret_path": "kv/apps/checkout", "secret_key": "db_password"}}), 201)
	machine := must(t, a.post("credentials/", map[string]any{"name": uniq("m"), "organization": f.org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "deploy", "password": "ASK", "become_password": "ASK"}}), 201).id()
	vaultCred := must(t, a.post("credentials/", map[string]any{"name": uniq("v"), "organization": f.org, "credential_type": credType(t, a, "vault"),
		"inputs": map[string]any{"vault_password": "ASK", "vault_id": "prod"}}), 201).id()

	jt := f.template(t, "masking.yml", map[string]any{"survey_enabled": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), map[string]any{"name": "s", "description": "", "spec": []any{
		map[string]any{"question_name": "Password", "variable": "survey_pw", "type": "password", "required": true}}}), 200)
	for _, c := range []int64{cloud, machine, vaultCred} {
		must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": c}), 204)
	}
	r := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"extra_vars": map[string]any{"survey_pw": maskSurvey},
		"credential_passwords": map[string]any{"ssh_password": maskSSH, "become_password": maskBecome, "vault_password.prod": maskVault}}), 201)
	ws := collectJobEvents(t, r.id())
	job := waitJob(t, a, fmt.Sprintf("jobs/%d/", r.id()))
	mustStatus(t, f, job, "successful")

	out := f.stdout(t, job.id())
	for _, want := range []string{"given $encrypted$ looked $encrypted$ survey $encrypted$ pin x9z region eu-west-9", "a task named after $encrypted$",
		`"msg": "$encrypted$"`, `"$encrypted$",`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if leaks := leakedCores(out); len(leaks) > 0 {
		t.Errorf("stdout shows %v:\n%s", leaks, out)
	}
	events := a.get(fmt.Sprintf("jobs/%d/job_events/?page_size=200", job.id()))
	if leaks := leakedCores(events.Raw); len(leaks) > 0 {
		t.Errorf("events show %v", leaks)
	}
	if !strings.Contains(events.Raw, `a task named after $encrypted$`) {
		t.Errorf("task name not masked in events")
	}
	// The last events are published as the job ends.
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(ws.text(), "playbook_on_stats") && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if text := ws.text(); !strings.Contains(text, "$encrypted$") {
		t.Errorf("no masked websocket message among:\n%s", text)
	} else if leaks := leakedCores(text); len(leaks) > 0 {
		t.Errorf("websocket messages show %v", leaks)
	}
	logs.wait(t, func(m map[string]any) bool { return m["job"] == float64(job.id()) && m["event"] == "playbook_on_stats" })
	logs.mu.Lock()
	raw, _ := json.Marshal(logs.msgs)
	logs.mu.Unlock()
	if !bytes.Contains(raw, []byte("$encrypted$")) {
		t.Errorf("no masked log record")
	}
	if leaks := leakedCores(string(raw)); len(leaks) > 0 {
		t.Errorf("log records show %v", leaks)
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := app.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("UPDATE unified_jobs SET created = now() - interval '30 days' WHERE id = $1", job.id())
	exec("UPDATE job_events SET job_created = now() - interval '30 days' WHERE job_id = $1", job.id())
	if err := jobManager.ArchiveEvents(ctx, 14); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := app.DB.QueryRow(ctx, "SELECT data FROM job_event_archives WHERE job_id = $1", job.id()).Scan(&data); err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	archived, _ := io.ReadAll(zr)
	if !bytes.Contains(archived, []byte("$encrypted$")) {
		t.Errorf("archive holds no masked event")
	}
	if leaks := leakedCores(string(archived)); len(leaks) > 0 {
		t.Errorf("archive shows %v", leaks)
	}
}

const failingInventoryScript = `#!/bin/sh
echo "cannot reach the CMDB with token $MASK_TOKEN" >&2
exit 1
`

// TestSecretMaskingOtherRuns: ad hoc commands, inventory updates and project
// updates mask the secrets they are handed too.
func TestSecretMaskingOtherRuns(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = f.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	noLeak := func(what, text, core string, want ...string) {
		t.Helper()
		if strings.Contains(text, core) {
			t.Errorf("%s shows %s:\n%s", what, core, text)
		}
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Errorf("%s lacks %q:\n%s", what, w, text)
			}
		}
	}

	machine := must(t, a.post("credentials/", map[string]any{"name": uniq("m"), "organization": f.org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "deploy", "password": "adhoc-pass-core4"}}), 201).id()
	r := must(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "credential": machine, "limit": "h1", "module_name": "shell",
		"module_args": `cat "$(dirname "$ANSIBLE_SSH_CONTROL_PATH_DIR")/env/passwords"`}), 201)
	cmd := waitJob(t, a, fmt.Sprintf("ad_hoc_commands/%d/", r.id()))
	out := a.get(fmt.Sprintf("ad_hoc_commands/%d/stdout/?format=txt", cmd.id())).Raw
	if cmd.Body["status"] != "successful" {
		t.Fatalf("ad hoc command %v:\n%s", cmd.Body["status"], out)
	}
	noLeak("ad hoc stdout", out, "adhoc-pass-core4", `"SSH password:\\s*?$":"$encrypted$"`)
	noLeak("ad hoc events", a.get(fmt.Sprintf("ad_hoc_commands/%d/events/", cmd.id())).Raw, "adhoc-pass-core4", "$encrypted$")

	commitFiles(t, f.repo, map[string]string{"inventory/fail.sh": failingInventoryScript}, "inventory script")
	os.Chmod(filepath.Join(f.repo, "inventory/fail.sh"), 0o755)
	git("add", "inventory/fail.sh")
	git("commit", "-qm", "executable")
	must(t, a.post(fmt.Sprintf("projects/%d/update/", f.project), nil), 202)
	time.Sleep(300 * time.Millisecond)
	f.waitProject(t)
	ct := must(t, a.post("credential_types/", map[string]any{"name": uniq("cmdb"), "kind": "cloud",
		"inputs":    map[string]any{"fields": []any{map[string]any{"id": "token", "label": "Token", "secret": true}}},
		"injectors": map[string]any{"env": map[string]any{"MASK_TOKEN": "{{ token }}"}}}), 201).id()
	cmdb := must(t, a.post("credentials/", map[string]any{"name": uniq("cmdb"), "organization": f.org, "credential_type": ct,
		"inputs": map[string]any{"token": "inv-token-core6"}}), 201).id()
	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": f.org}), 201).id()
	src := must(t, a.post("inventory_sources/", map[string]any{"name": "cmdb", "inventory": inv, "source": "scm", "source_project": f.project,
		"source_path": "inventory/fail.sh", "credential": cmdb}), 201).id()
	started := must(t, a.post(fmt.Sprintf("inventory_sources/%d/update/", src), nil), 202)
	u := waitJob(t, a, fmt.Sprintf("inventory_updates/%v/", started.Body["inventory_update"]))
	out = a.get(fmt.Sprintf("inventory_updates/%v/stdout/?format=txt", u.Body["id"])).Raw
	if u.Body["status"] != "failed" {
		t.Fatalf("inventory update %v:\n%s", u.Body["status"], out)
	}
	noLeak("inventory update stdout", out, "inv-token-core6", "token $encrypted$")

	// A project update prints its branch: here, the credential's password.
	git("branch", "branch-core7-x")
	scm := must(t, a.post("credentials/", map[string]any{"name": uniq("scm"), "organization": f.org, "credential_type": credType(t, a, "scm"),
		"inputs": map[string]any{"username": "git", "password": "branch-core7-x"}}), 201).id()
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": f.org, "scm_type": "git", "scm_url": f.repo,
		"scm_branch": "branch-core7-x", "credential": scm}), 201).id()
	var update map[string]any
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if l := a.get(fmt.Sprintf("projects/%d/project_updates/?order_by=-id", proj)).results(); len(l) > 0 && l[0]["finished"] != nil {
			update = l[0]
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if update == nil {
		t.Fatal("project update never finished")
	}
	out = a.get(fmt.Sprintf("project_updates/%v/stdout/?format=txt", update["id"])).Raw
	if update["status"] != "successful" {
		t.Fatalf("project update %v:\n%s", update["status"], out)
	}
	noLeak("project update stdout", out, "branch-core7-x", "($encrypted$)")
}
