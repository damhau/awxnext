package test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const diffPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - name: render the config
      ansible.builtin.template:
        src: app.conf.j2
        dest: "{{ out_dir }}/{{ inventory_hostname }}.conf"
`

// A job run with diff_mode (roadmap M7-12) keeps what the job output's diff
// view shows: a changed template task's before and after in its result
// (event_data.res.diff, named by the destination in before_header), and the
// runner's unified diff in a runner_on_file_diff event under the task.
func TestJobDiffMode(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	out := t.TempDir()
	os.Chmod(out, 0o777)
	f.repo = gitRepo(t, map[string]string{"diff.yml": diffPlaybook, "app.conf.j2": "port = {{ app_port }}\nworkers = 4\n"})
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("proj"), "organization": f.org, "scm_type": "git", "scm_url": f.repo}), 201).id()
	f.waitProject(t)
	jt := f.template(t, "diff.yml", map[string]any{"diff_mode": true, "limit": "h1", "ask_variables_on_launch": true})

	run := func(port int) int64 {
		t.Helper()
		job := f.launch(t, a, jt, map[string]any{"extra_vars": map[string]any{"out_dir": out, "app_port": port}})
		if job.Body["status"] != "successful" {
			t.Fatalf("job %v: %v", job.id(), job.Body["status"])
		}
		return job.id()
	}
	run(8080)
	job := run(9090)

	evs := must(t, a.get(fmt.Sprintf("jobs/%d/job_events/?event=runner_on_ok&page_size=50", job)), 200).results()
	if len(evs) != 1 || evs[0]["changed"] != true {
		t.Fatalf("results: %v", evs)
	}
	res, _ := evs[0]["event_data"].(map[string]any)["res"].(map[string]any)
	diffs, _ := res["diff"].([]any)
	if len(diffs) != 1 {
		t.Fatalf("no diff in the result: %v", res)
	}
	d := diffs[0].(map[string]any)
	if d["before"] != "port = 8080\nworkers = 4\n" || d["after"] != "port = 9090\nworkers = 4\n" || d["before_header"] != out+"/h1.conf" {
		t.Fatalf("diff: %v", d)
	}

	fd := must(t, a.get(fmt.Sprintf("jobs/%d/job_events/?event=runner_on_file_diff", job)), 200).results()
	if len(fd) != 1 || fd[0]["host_name"] != "h1" || fd[0]["task"] != "render the config" ||
		!strings.Contains(fmt.Sprint(fd[0]["stdout"]), "-port = 8080") || !strings.Contains(fmt.Sprint(fd[0]["stdout"]), "+port = 9090") {
		t.Fatalf("the runner's diff: %v", fd)
	}

	// Without diff mode, no diff.
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"diff_mode": false}), 200)
	plain := run(7070)
	if n := a.get(fmt.Sprintf("jobs/%d/job_events/?event=runner_on_file_diff", plain)).count(); n != 0 {
		t.Errorf("a job without diff mode has %d diffs", n)
	}
}
