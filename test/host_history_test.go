package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// historyPlaybook manages a file per host, stamps another on every run, and
// fails on a host when a file says so.
const historyPlaybook = `- name: Hosts
  hosts: all
  gather_facts: false
  tasks:
    - name: Write config
      ansible.builtin.copy:
        dest: "{{ state_dir }}/{{ inventory_hostname }}.conf"
        content: "managed\n"
    - name: Stamp
      ansible.builtin.copy:
        dest: "{{ state_dir }}/{{ inventory_hostname }}.stamp"
        content: "{{ now().isoformat() }}\n"
      when: not ansible_check_mode
    - name: Check health
      ansible.builtin.assert:
        that: not ((state_dir ~ '/fail-' ~ inventory_hostname) is exists)
`

func TestHostHistoryAndDrift(t *testing.T) {
	requireAnsible(t)
	f := newRepoFixture(t, map[string]string{"history.yml": historyPlaybook})
	a := f.a
	dir := t.TempDir()
	jt := f.template(t, "history.yml", map[string]any{"extra_vars": fmt.Sprintf("state_dir: %s\n", dir),
		"ask_job_type_on_launch": true, "ask_diff_mode_on_launch": true})
	hosts := map[string]int64{}
	for _, h := range must(t, a.get(fmt.Sprintf("hosts/?inventory=%d", f.inv)), 200).results() {
		hosts[h["name"].(string)] = int64(h["id"].(float64))
	}
	edit := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(check bool) int64 {
		t.Helper()
		body := map[string]any{}
		if check {
			body = map[string]any{"job_type": "check", "diff_mode": true}
		}
		j := f.launch(t, a, jt, body)
		want := "successful"
		if _, err := os.Stat(filepath.Join(dir, "fail-h2")); err == nil {
			want = "failed"
		}
		mustStatus(t, f, j, want)
		return j.id()
	}
	task := func(host, name string) map[string]any {
		t.Helper()
		h := must(t, a.get(fmt.Sprintf("hosts/%d/task_history/", hosts[host])), 200)
		for _, tpl := range h.Body["templates"].([]any) {
			tpl := tpl.(map[string]any)
			if int64(tpl["id"].(float64)) != jt {
				continue
			}
			for _, tk := range tpl["tasks"].([]any) {
				if tk := tk.(map[string]any); tk["task"] == name {
					return tk
				}
			}
		}
		t.Fatalf("%s has no task %q: %s", host, name, h.Raw)
		return nil
	}
	flag := func(host, name, kind string) map[string]any {
		t.Helper()
		for _, fl := range task(host, name)["flags"].([]any) {
			if fl := fl.(map[string]any); fl["type"] == kind {
				return fl
			}
		}
		return nil
	}
	statuses := func(host, name string) string {
		t.Helper()
		var s []string
		for _, r := range task(host, name)["results"].([]any) {
			r := r.(map[string]any)
			st := r["status"].(string)
			if r["check_mode"] == true {
				st += "?"
			}
			s = append(s, st)
		}
		return strings.Join(s, " ")
	}
	id := func(v any) int64 {
		if n, ok := v.(float64); ok {
			return int64(n)
		}
		return 0
	}

	run(false)
	r2 := run(false)
	edit("h1.conf", "edited by hand\n")
	r3 := run(false)
	run(false)
	if s := statuses("h1", "Write config"); s != "changed ok changed ok" {
		t.Errorf("h1's Write config: %s", s)
	}
	if fl := flag("h1", "Write config", "drift"); fl == nil || id(fl["job"]) != r3 || id(fl["previous"]) != r2 || fl["open"] != false {
		t.Errorf("h1's drift: %v", task("h1", "Write config"))
	}
	if fl := flag("h2", "Write config", "drift"); fl != nil {
		t.Errorf("h2's Write config drifted: %v", fl)
	}
	for _, h := range []string{"h1", "h2", "h3"} {
		if fl := flag(h, "Stamp", "not_idempotent"); fl == nil || id(fl["runs"]) != 4 {
			t.Errorf("%s's Stamp isn't flagged as not idempotent: %v", h, task(h, "Stamp"))
		}
		if fl := flag(h, "Write config", "not_idempotent"); fl != nil {
			t.Errorf("%s's Write config is flagged as not idempotent: %v", h, fl)
		}
	}

	// A change by hand and an edit to the template: not drift.
	edit("h2.conf", "edited by hand\n")
	edit("fail-h2", "")
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"verbosity": 1}), 200)
	r5 := run(false)
	if fl := flag("h2", "Check health", "new_failure"); fl == nil || id(fl["job"]) != r5 || id(fl["failures"]) != 1 {
		t.Errorf("h2's new failure: %v", task("h2", "Check health"))
	}
	if fl := flag("h1", "Check health", "new_failure"); fl != nil {
		t.Errorf("h1 has a new failure: %v", fl)
	}

	edit("h3.conf", "edited by hand\n")
	r6 := run(true)
	if s := statuses("h3", "Write config"); s != "changed ok ok ok ok changed?" {
		t.Errorf("h3's Write config: %s", s)
	}
	if s := statuses("h3", "Stamp"); s != "changed changed changed changed changed skipped?" {
		t.Errorf("h3's Stamp: %s", s)
	}
	drift := must(t, a.get(fmt.Sprintf("host_drift/?inventory=%d", f.inv)), 200)
	if drift.count() != 1 {
		t.Fatalf("drift after the check run: %s", drift.Raw)
	}
	d := drift.results()[0]
	if id(d["host"]) != hosts["h3"] || d["task"] != "Write config" || id(d["job"]) != r6 || id(d["last_job"]) != r6 || id(d["job_template"]) != jt || id(d["inventory"]) != f.inv {
		t.Errorf("drift: %v", d)
	}
	var diff map[string]any
	if l, _ := d["diff"].(map[string]any)["diff"].([]any); len(l) == 1 {
		diff, _ = l[0].(map[string]any)
	}
	if diff["before"] != "edited by hand\n" || diff["after"] != "managed\n" {
		t.Errorf("drift's diff: %v", d["diff"])
	}
	if fl := flag("h3", "Write config", "drift"); fl == nil || fl["open"] != true || id(fl["job"]) != r6 || fl["diff"] == nil {
		t.Errorf("h3's open drift: %v", task("h3", "Write config"))
	}
	if fl := flag("h2", "Check health", "new_failure"); fl == nil || id(fl["job"]) != r5 || id(fl["failures"]) != 2 {
		t.Errorf("h2's failure in check mode: %v", task("h2", "Check health"))
	}
	bob, _ := newUser(t, a)
	if n := must(t, bob.get("host_drift/"), 200).count(); n != 0 {
		t.Errorf("bob sees %d drifts", n)
	}
	if r := bob.get(fmt.Sprintf("hosts/%d/task_history/", hosts["h3"])); r.Status != 403 && r.Status != 404 {
		t.Errorf("bob's history of h3: %d", r.Status)
	}

	// A run that changes the host leaves the drift open: one that finds
	// nothing to change clears it.
	os.Remove(filepath.Join(dir, "fail-h2"))
	run(false)
	if fl := flag("h2", "Check health", "new_failure"); fl != nil {
		t.Errorf("h2 recovered but still has a new failure: %v", fl)
	}
	if fl := flag("h2", "Write config", "drift"); fl != nil {
		t.Errorf("h2's change came with a template edit, but drifted: %v", fl)
	}
	drift = must(t, a.get(fmt.Sprintf("host_drift/?inventory=%d", f.inv)), 200)
	if drift.count() != 1 || id(drift.results()[0]["last_job"]) != r6 {
		t.Fatalf("drift after a run that changed it: %s", drift.Raw)
	}
	if s := statuses("h3", "Write config"); !strings.HasSuffix(s, "changed? changed") {
		t.Errorf("h3's Write config after the fix: %s", s)
	}

	// The retention: old results go, the open drift stays.
	ctx := context.Background()
	var before, old int
	app.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE job_id <= $2) FROM host_task_results WHERE unified_job_template_id = $1`, jt, r2).Scan(&before, &old)
	app.DB.Exec(ctx, `UPDATE host_task_results SET created = created - interval '40 days' WHERE unified_job_template_id = $1 AND job_id <= $2`, jt, r2)
	if old != 2*3*3 {
		t.Fatalf("results of the first two runs: %d", old)
	}
	var sjt int64
	app.DB.QueryRow(ctx, `SELECT id FROM unified_job_templates WHERE type = 'system_job_template' AND job_type = 'cleanup_host_history'`).Scan(&sjt)
	if r := must(t, a.get(fmt.Sprintf("system_job_templates/%d/schedules/", sjt)), 200); r.count() != 1 {
		t.Errorf("default schedule: %s", r.Raw)
	}
	cleanup := func(extra map[string]any) string {
		t.Helper()
		j := must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", sjt), map[string]any{"extra_vars": extra}), 201)
		if r := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", j.id())); r.Body["status"] != "successful" {
			t.Fatalf("cleanup: %v", r.Body)
		}
		return a.get(fmt.Sprintf("system_jobs/%d/stdout/?format=txt", j.id())).Raw
	}
	count := func() int {
		var n int
		app.DB.QueryRow(ctx, `SELECT count(*) FROM host_task_results WHERE unified_job_template_id = $1`, jt).Scan(&n)
		return n
	}
	if out := cleanup(map[string]any{"dry_run": true}); !strings.Contains(out, "older than 30 days (dry run)") || !strings.Contains(out, "Host task results would delete") {
		t.Errorf("dry run:\n%s", out)
	}
	if n := count(); n != before {
		t.Fatalf("the dry run deleted results: %d of %d", n, before)
	}
	if out := cleanup(map[string]any{}); !strings.Contains(out, "Host task results deleted") {
		t.Errorf("cleanup:\n%s", out)
	}
	if n := count(); n != before-old {
		t.Errorf("after cleanup: %d results, want %d", n, before-old)
	}
	if n := must(t, a.get(fmt.Sprintf("host_drift/?inventory=%d", f.inv)), 200).count(); n != 1 {
		t.Errorf("the cleanup removed the open drift")
	}

	run(true)
	if n := must(t, a.get(fmt.Sprintf("host_drift/?inventory=%d", f.inv)), 200).count(); n != 0 {
		t.Errorf("a check run that changes nothing left drift: %d", n)
	}
	if fl := flag("h3", "Write config", "drift"); fl != nil {
		t.Errorf("h3 still drifted: %v", fl)
	}
	if r := a.get(fmt.Sprintf("hosts/%d/task_history/?days=0", hosts["h1"])); r.Status != 400 {
		t.Errorf("days=0: %d", r.Status)
	}
}
