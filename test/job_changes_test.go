package test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// brokenSitePlaybook is site.yml after a commit that breaks h2.
const brokenSitePlaybook = `- name: Site
  hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "hello {{ who | default('nobody') }} from {{ inventory_hostname }}"
    - ansible.builtin.fail:
        msg: "no subscription on {{ inventory_hostname }}"
      when: inventory_hostname == 'h2'
`

func changesOf(t *testing.T, c *client, path string, want int) resp {
	t.Helper()
	return must(t, c.get(path), want)
}

func section(t *testing.T, r resp, name string) map[string]any {
	t.Helper()
	m, _ := r.Body[name].(map[string]any)
	if m == nil {
		t.Fatalf("no %s in the changes: %s", name, r.Raw)
	}
	return m
}

func list(r resp, name string) []map[string]any {
	var out []map[string]any
	l, _ := r.Body[name].([]any)
	for _, x := range l {
		out = append(out, x.(map[string]any))
	}
	return out
}

func fieldNames(o map[string]any) string {
	var out []string
	l, _ := o["fields"].([]any)
	for _, x := range l {
		f := x.(map[string]any)
		name := fmt.Sprint(f["field"])
		if k, ok := f["key"]; ok {
			name += "." + fmt.Sprint(k)
		}
		if f["secret"] == true {
			name += "(secret)"
		}
		out = append(out, name)
	}
	return strings.Join(out, ",")
}

func TestJobChanges(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	const digest1, digest2 = "sha256:4a1f00000000000000000000000000000000000000000000000000000000beef", "sha256:9c0e00000000000000000000000000000000000000000000000000000000cafe"
	k := &fakeKube{t: t, pods: map[string]map[string]any{}, dir: t.TempDir(), imageID: "docker-pullable://quay.io/ansible/awx-ee@" + digest1}
	kube := httptest.NewServer(k)
	defer kube.Close()
	kcred := must(t, a.post("credentials/", map[string]any{"name": uniq("k8s"), "organization": f.org, "credential_type": credType(t, a, "kubernetes_bearer_token"),
		"inputs": map[string]any{"host": kube.URL, "bearer_token": "kube-token", "verify_ssl": false}}), 201).id()
	ig := must(t, a.post("instance_groups/", map[string]any{"name": uniq("pods"), "is_container_group": true, "credential": kcred}), 201).id()

	ct := must(t, a.post("credential_types/", map[string]any{"name": uniq("cloud"), "kind": "cloud",
		"inputs":    map[string]any{"fields": []any{map[string]any{"id": "api_token", "label": "API Token", "secret": true}}},
		"injectors": map[string]any{"env": map[string]any{"MYCLOUD_TOKEN": "{{ api_token }}"}}}), 201).id()
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("cloud"), "organization": f.org, "credential_type": ct,
		"inputs": map[string]any{"api_token": "token-value-one"}}), 201).id()
	jt := f.template(t, "site.yml", map[string]any{"ask_limit_on_launch": true, "ask_variables_on_launch": true, "extra_vars": "who: alice\n", "survey_enabled": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jt), map[string]any{"name": "", "description": "", "spec": []any{
		map[string]any{"question_name": "Secret", "variable": "secret", "type": "password", "required": false, "default": ""}}}), 200)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": cred}), 204)
	must(t, a.post(fmt.Sprintf("job_templates/%d/instance_groups/", jt), map[string]any{"id": ig}), 204)

	good := f.launch(t, a, jt, map[string]any{"extra_vars": map[string]any{"secret": "survey-pass-one"}})
	mustStatus(t, f, good, "successful")
	first := changesOf(t, a, fmt.Sprintf("jobs/%d/changes/", good.id()), 200)
	if first.Body["compared_with"] != nil || first.Body["count"] != float64(0) || first.Body["last_success"] != nil {
		t.Fatalf("a template's first run has nothing to compare with: %s", first.Raw)
	}

	// Between the two runs: a commit, edits to the template, inventory and
	// credential, a new host, a new image behind the same tag, and other
	// launch inputs.
	rev := fmt.Sprint(a.get(fmt.Sprintf("projects/%d/", f.project)).Body["scm_revision"])
	commitFiles(t, f.repo, map[string]string{"site.yml": brokenSitePlaybook}, "Read the subscription from the vault")
	must(t, a.post(fmt.Sprintf("projects/%d/update/", f.project), nil), 202)
	deadline := time.Now().Add(30 * time.Second)
	for p := a.get(fmt.Sprintf("projects/%d/", f.project)); p.Body["scm_revision"] == rev || p.Body["status"] != "successful"; p = a.get(fmt.Sprintf("projects/%d/", f.project)) {
		if time.Now().After(deadline) {
			t.Fatalf("the project never synced the new commit: %s", p.Raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"extra_vars": "who: bob\n"}), 200)
	must(t, a.patch(fmt.Sprintf("inventories/%d/", f.inv), map[string]any{
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\nregion: eu\n"}), 200)
	must(t, a.post("hosts/", map[string]any{"name": "h4", "inventory": f.inv}), 201)
	must(t, a.patch(fmt.Sprintf("credentials/%d/", cred), map[string]any{"inputs": map[string]any{"api_token": "token-value-two"}}), 200)
	k.mu.Lock()
	k.imageID = "quay.io/ansible/awx-ee@" + digest2
	k.mu.Unlock()

	bad := f.launch(t, a, jt, map[string]any{"extra_vars": map[string]any{"secret": "survey-pass-two"}, "limit": "h1,h2,h4"})
	mustStatus(t, f, bad, "failed")
	ch := changesOf(t, a, fmt.Sprintf("jobs/%d/changes/", bad.id()), 200)
	cw := section(t, ch, "compared_with")
	if cw["id"] != float64(good.id()) || cw["reason"] != "last_success" || section(t, ch, "last_success")["id"] != float64(good.id()) {
		t.Fatalf("compared with: %s", ch.Raw)
	}
	tpl := section(t, ch, "template")
	if tpl["readable"] != true || fieldNames(tpl) != "extra_vars" || len(tpl["versions"].([]any)) != 1 ||
		tpl["versions"].([]any)[0].(map[string]any)["actor"].(map[string]any)["username"] != "admin" {
		t.Errorf("template: %v", tpl)
	}
	if inv := section(t, ch, "inventory"); fieldNames(inv) != "variables" {
		t.Errorf("inventory: %v", inv)
	}
	if creds := list(ch, "credentials"); len(creds) != 1 || fieldNames(creds[0]) != "inputs.api_token(secret)" {
		t.Errorf("credentials: %v", creds)
	}
	p := section(t, ch, "project")
	commits, _ := p["commits"].([]any)
	if p["commits_available"] != true || p["total_commits"] != float64(1) || len(commits) != 1 ||
		commits[0].(map[string]any)["message"] != "Read the subscription from the vault" || commits[0].(map[string]any)["author"] != "Test" ||
		section(t, resp{Body: p}, "before")["revision"] != rev {
		t.Errorf("project: %v", p)
	}
	ee := section(t, ch, "execution_environment")
	if ee["same_tag"] != true || ee["before"].(map[string]any)["digest"] != digest1 || ee["after"].(map[string]any)["digest"] != digest2 {
		t.Errorf("execution environment: %v", ee)
	}
	var inputs []string
	for _, in := range list(ch, "inputs") {
		name := fmt.Sprint(in["field"])
		if key, ok := in["key"]; ok {
			name += "." + fmt.Sprint(key)
		}
		if in["secret"] == true {
			name += "(secret)"
		}
		inputs = append(inputs, name)
	}
	if strings.Join(inputs, ",") != "limit,extra_vars.secret(secret)" {
		t.Errorf("inputs (the template's own edit of who shows with the template): %v", inputs)
	}
	h := section(t, ch, "hosts")
	if fmt.Sprint(h["added"]) != "[h4]" || fmt.Sprint(h["removed"]) != "[h3]" {
		t.Errorf("hosts: %v", h)
	}
	if ch.Body["count"] != float64(8) {
		t.Errorf("count %v, want 8: %s", ch.Body["count"], ch.Raw)
	}
	for _, secret := range []string{"token-value-one", "token-value-two", "survey-pass-one", "survey-pass-two", "$encrypted$v1$"} {
		if strings.Contains(ch.Raw, secret) {
			t.Errorf("the changes show %q", secret)
		}
	}

	// Someone who can read the template, and so its jobs, but not the
	// inventory, credential or project, sees that they changed, not how.
	reader, uid := newUser(t, a)
	grant(t, a, "JobTemplate Read", uid, jt)
	rc := changesOf(t, reader, fmt.Sprintf("jobs/%d/changes/", bad.id()), 200)
	if rc.Body["count"] != float64(8) || section(t, rc, "template")["readable"] != true {
		t.Errorf("reader: %s", rc.Raw)
	}
	for _, o := range []map[string]any{section(t, rc, "inventory"), list(rc, "credentials")[0], section(t, rc, "project")} {
		if o["readable"] != false || o["fields"] != nil || o["versions"] != nil || o["commits"] != nil && len(o["commits"].([]any)) > 0 {
			t.Errorf("an object the reader can't read: %v", o)
		}
	}
	other, _ := newUser(t, a)
	changesOf(t, other, fmt.Sprintf("jobs/%d/changes/", bad.id()), 403)

	// ?since= compares with a chosen run of the same template.
	chosen := changesOf(t, a, fmt.Sprintf("jobs/%d/changes/?since=%d", bad.id(), good.id()), 200)
	if section(t, chosen, "compared_with")["reason"] != "chosen" || chosen.Body["count"] != float64(8) {
		t.Errorf("since: %s", chosen.Raw)
	}
	failing := f.template(t, "fail.yml", nil)
	a1 := f.launch(t, a, failing, nil)
	for _, since := range []string{fmt.Sprint(bad.id()), fmt.Sprint(a1.id()), "nope", "999999999"} {
		changesOf(t, a, fmt.Sprintf("jobs/%d/changes/?since=%s", bad.id(), since), 400)
	}

	// A rerun with nothing changed lists nothing.
	again := must(t, a.post(fmt.Sprintf("jobs/%d/relaunch/", bad.id()), nil), 201).id()
	rerun := waitJob(t, a, fmt.Sprintf("jobs/%d/", again))
	mustStatus(t, f, rerun, "failed")
	same := changesOf(t, a, fmt.Sprintf("jobs/%d/changes/?since=%d", again, bad.id()), 200)
	if same.Body["count"] != float64(0) || same.Body["template"] != nil || same.Body["project"] != nil || same.Body["hosts"] != nil ||
		same.Body["execution_environment"] != nil || len(list(same, "inputs")) != 0 || len(list(same, "credentials")) != 0 {
		t.Errorf("a rerun: %s", same.Raw)
	}

	// A template that never succeeded compares with its previous run.
	mustStatus(t, f, a1, "failed")
	a2 := f.launch(t, a, failing, nil)
	prev := changesOf(t, a, fmt.Sprintf("jobs/%d/changes/", a2.id()), 200)
	if c := section(t, prev, "compared_with"); c["id"] != float64(a1.id()) || c["reason"] != "previous" || prev.Body["last_success"] != nil || prev.Body["count"] != float64(0) {
		t.Errorf("never succeeded: %s", prev.Raw)
	}
}

func TestWorkflowJobChanges(t *testing.T) {
	w := newWorkflowFixture(t)
	a := w.a
	wf := w.wfjt(t, map[string]any{"extra_vars": "who: alice\n"})
	w.node(t, wf, w.site, nil)
	wj1 := w.launch(t, wf, nil)
	if r := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj1)); r.Body["status"] != "successful" {
		t.Fatalf("first workflow run: %s", r.Raw)
	}
	must(t, a.patch(fmt.Sprintf("workflow_job_templates/%d/", wf), map[string]any{"extra_vars": "who: bob\n"}), 200)
	w.node(t, wf, w.show, nil)
	wj2 := w.launch(t, wf, nil)
	if r := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj2)); r.Body["status"] != "successful" {
		t.Fatalf("second workflow run: %s", r.Raw)
	}
	ch := changesOf(t, a, fmt.Sprintf("workflow_jobs/%d/changes/", wj2), 200)
	if section(t, ch, "compared_with")["id"] != float64(wj1) || fieldNames(section(t, ch, "template")) != "extra_vars" ||
		len(list(ch, "inputs")) != 0 || ch.Body["project"] != nil || ch.Body["hosts"] != nil {
		t.Fatalf("workflow changes: %s", ch.Raw)
	}
	nodes := list(ch, "nodes")
	if len(nodes) != 2 || nodes[0]["change"] != "" || nodes[1]["change"] != "added" || nodes[0]["before_job"] == nil || nodes[0]["after_job"] == nil ||
		nodes[1]["before_job"] != nil || ch.Body["count"] != float64(2) {
		t.Fatalf("nodes: %v (count %v)", nodes, ch.Body["count"])
	}
	// Each node's job has its own changes against the earlier run's: here,
	// the variable the workflow passed it.
	nj := nodes[0]["after_job"].(map[string]any)
	since := nodes[0]["before_job"].(map[string]any)["id"]
	r := changesOf(t, a, fmt.Sprintf("jobs/%v/changes/?since=%v", nj["id"], since), 200)
	if in := list(r, "inputs"); r.Body["count"] != float64(1) || len(in) != 1 || in[0]["key"] != "who" || in[0]["before"] != "alice" || in[0]["after"] != "bob" {
		t.Errorf("the node's job: %s", r.Raw)
	}
}
