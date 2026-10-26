package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitRepo creates a local git repository with the given files and returns its path.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	commitFiles(t, dir, files, "initial")
	return dir
}

func commitFiles(t *testing.T, dir string, files map[string]string, msg string) string {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out))
}

// waitJob polls a job URL until it reaches a final status.
func waitJob(t *testing.T, a *client, url string) resp {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		r := a.get(url)
		switch r.Body["status"] {
		case "successful", "failed", "error", "canceled":
			return r
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", url)
	return resp{}
}

func allowFileURLs(t *testing.T, a *client) {
	must(t, a.patch("settings/jobs/", map[string]any{"PROJECT_ALLOW_FILE_URLS": true}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"PROJECT_ALLOW_FILE_URLS": false}) })
}

const helloPlaybook = "- name: Hello\n  hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.debug:\n        msg: hello\n"

func TestProjectGitSync(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	repo := gitRepo(t, map[string]string{
		"site.yml":                   helloPlaybook,
		"playbooks/deploy.yml":       "- hosts: web\n  tasks: []\n",
		"roles/web/tasks/main.yml":   "- debug: msg=x\n",
		"inventories/prod/hosts.ini": "[web]\nweb1\n",
		"inventories/prod.yml":       "all:\n  hosts:\n    a: {}\n",
		"README.md":                  "# demo\n",
	})
	must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": "file://" + repo}), 400)
	allowFileURLs(t, a)
	r := must(t, a.post("projects/", map[string]any{"name": uniq("checkout"), "organization": org, "scm_type": "git", "scm_url": "file://" + repo}), 201)
	pid := r.id()
	if r.Body["status"] != "never updated" && r.Body["status"] != "pending" && r.Body["status"] != "running" {
		t.Logf("initial status %v", r.Body["status"])
	}
	ups := must(t, a.get(fmt.Sprintf("projects/%d/project_updates/", pid)), 200)
	if ups.count() != 1 {
		t.Fatalf("project create should launch an update: %s", ups.Raw)
	}
	pu := waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(ups.results()[0]["id"].(float64))))
	if pu.Body["status"] != "successful" {
		out := a.get(fmt.Sprintf("project_updates/%d/stdout/?format=txt", pu.id()))
		t.Fatalf("project update failed: %v\n%s", pu.Body["job_explanation"], out.Raw)
	}
	p := must(t, a.get(fmt.Sprintf("projects/%d/", pid)), 200)
	sha := p.Body["scm_revision"].(string)
	if len(sha) != 40 || p.Body["status"] != "successful" || p.Body["last_update_failed"] != false {
		t.Fatalf("project after sync: rev=%v status=%v", sha, p.Body["status"])
	}
	pbs := must(t, a.get(fmt.Sprintf("projects/%d/playbooks/", pid)), 200)
	if pbs.Raw != "[\"playbooks/deploy.yml\",\"site.yml\"]\n" {
		t.Fatalf("playbooks: %s", pbs.Raw)
	}
	invs := must(t, a.get(fmt.Sprintf("projects/%d/inventories/", pid)), 200)
	if !strings.Contains(invs.Raw, "inventories/prod/hosts.ini") || !strings.Contains(invs.Raw, "inventories/prod.yml") || strings.Contains(invs.Raw, "site.yml") {
		t.Fatalf("inventories: %s", invs.Raw)
	}
	// Output and events.
	out := must(t, a.get(fmt.Sprintf("project_updates/%d/stdout/?format=txt", pu.id())), 200)
	if !strings.Contains(out.Raw, "Revision "+sha) || !strings.Contains(out.Raw, "Project update successful") {
		t.Fatalf("stdout: %s", out.Raw)
	}
	evs := must(t, a.get(fmt.Sprintf("project_updates/%d/events/", pu.id())), 200)
	if evs.count() < 3 || evs.Body["results"].([]any)[0].(map[string]any)["counter"].(float64) != 1 {
		t.Fatalf("events: %d", evs.count())
	}
	js := must(t, a.get(fmt.Sprintf("project_updates/%d/stdout/", pu.id())), 200)
	if js.Body["range"].(map[string]any)["absolute_end"].(float64) < 3 {
		t.Fatalf("json stdout: %s", js.Raw)
	}
	// The project's local_path points at the checked-out revision.
	local := filepath.Join(app.Cfg.ProjectsRoot, p.Body["local_path"].(string), "site.yml")
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("local path not populated: %v", err)
	}

	// New commit: update picks it up; old revision dir stays.
	sha2 := commitFiles(t, repo, map[string]string{"site.yml": helloPlaybook + "# v2\n"}, "v2")
	up := must(t, a.post(fmt.Sprintf("projects/%d/update/", pid), nil), 202)
	pu2 := waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(up.Body["project_update"].(float64))))
	if pu2.Body["status"] != "successful" || pu2.Body["scm_revision"] != sha2 {
		t.Fatalf("second update: %v %v", pu2.Body["status"], pu2.Body["scm_revision"])
	}
	b, _ := os.ReadFile(local)
	if !strings.Contains(string(b), "# v2") {
		t.Fatal("local path not switched to new revision")
	}

	// Tags and branches.
	exec.Command("git", "-C", repo, "tag", "v1.0", sha).Run()
	r = must(t, a.patch(fmt.Sprintf("projects/%d/", pid), map[string]any{"scm_branch": "v1.0"}), 200)
	time.Sleep(200 * time.Millisecond)
	latest := must(t, a.get(fmt.Sprintf("project_updates/?unified_job_template=%d&order_by=-id&page_size=1", pid)), 200).results()[0]
	done := waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(latest["id"].(float64))))
	if done.Body["scm_revision"] != sha {
		t.Fatalf("tag checkout: %v want %v", done.Body["scm_revision"], sha)
	}

	// A bad branch fails the update with a clear message.
	must(t, a.patch(fmt.Sprintf("projects/%d/", pid), map[string]any{"scm_branch": "does-not-exist"}), 200)
	time.Sleep(200 * time.Millisecond)
	latest = must(t, a.get(fmt.Sprintf("project_updates/?unified_job_template=%d&order_by=-id&page_size=1", pid)), 200).results()[0]
	done = waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(latest["id"].(float64))))
	if done.Body["status"] != "failed" || !strings.Contains(done.Body["job_explanation"].(string), "does-not-exist") {
		t.Fatalf("bad branch: %v %v", done.Body["status"], done.Body["job_explanation"])
	}
	p = must(t, a.get(fmt.Sprintf("projects/%d/", pid)), 200)
	if p.Body["last_update_failed"] != true || p.Body["status"] != "failed" {
		t.Fatalf("project status after failure: %v", p.Body["status"])
	}
	// Unified listings include project updates and the project.
	if n := must(t, a.get(fmt.Sprintf("unified_jobs/?unified_job_template=%d", pid)), 200).count(); n < 4 {
		t.Fatalf("unified_jobs: %d", n)
	}
	ujt := must(t, a.get(fmt.Sprintf("unified_job_templates/?id=%d", pid)), 200).results()[0]
	if ujt["url"] != fmt.Sprintf("/api/v2/projects/%d/", pid) || ujt["type"] != "project" {
		t.Fatalf("unified_job_templates: %v", ujt)
	}
}

func TestProjectAccessAndManual(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	allowFileURLs(t, a)
	repo := gitRepo(t, map[string]string{"site.yml": helloPlaybook})
	pid := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": repo}), 201).id()
	time.Sleep(100 * time.Millisecond)

	u, uid := newUser(t, a)
	must(t, u.get(fmt.Sprintf("projects/%d/", pid)), 403)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Project Use"), "user": uid, "object_id": pid}), 201)
	r := must(t, u.get(fmt.Sprintf("projects/%d/update/", pid)), 200)
	if r.Body["can_update"] != false {
		t.Fatal("use role should not update")
	}
	must(t, u.post(fmt.Sprintf("projects/%d/update/", pid), nil), 403)
	if n := must(t, u.get(fmt.Sprintf("projects/%d/project_updates/", pid)), 200).count(); n < 1 {
		t.Fatal("use role should see updates")
	}
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Project Update"), "user": uid, "object_id": pid}), 201)
	up := must(t, u.post(fmt.Sprintf("projects/%d/update/", pid), nil), 202)
	waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(up.Body["project_update"].(float64))))
	// Finished updates can be deleted by project admins; running ones cannot be deleted at all.
	must(t, u.delete(fmt.Sprintf("project_updates/%d/", int64(up.Body["project_update"].(float64)))), 403)
	must(t, a.delete(fmt.Sprintf("project_updates/%d/", int64(up.Body["project_update"].(float64)))), 204)

	// Credential kind is enforced.
	ssh := must(t, a.post("credentials/", map[string]any{"name": uniq("c"), "organization": org, "credential_type": credType(t, a, "ssh")}), 201).id()
	must(t, a.patch(fmt.Sprintf("projects/%d/", pid), map[string]any{"credential": ssh}), 400)

	// Manual projects use a directory under the projects root.
	must(t, a.post("projects/", map[string]any{"name": uniq("m"), "organization": org, "local_path": "nope"}), 400)
	must(t, a.post("projects/", map[string]any{"name": uniq("m"), "organization": org, "local_path": "../etc"}), 400)
	os.MkdirAll(filepath.Join(app.Cfg.ProjectsRoot, "manual_demo"), 0o755)
	os.WriteFile(filepath.Join(app.Cfg.ProjectsRoot, "manual_demo", "ping.yml"), []byte(helloPlaybook), 0o644)
	m := must(t, a.post("projects/", map[string]any{"name": uniq("m"), "organization": org, "local_path": "manual_demo"}), 201)
	if m.Body["status"] != "ok" {
		t.Fatalf("manual status: %v", m.Body["status"])
	}
	pbs := must(t, a.get(fmt.Sprintf("projects/%d/playbooks/", m.id())), 200)
	if pbs.Raw != "[\"ping.yml\"]\n" {
		t.Fatalf("manual playbooks: %s", pbs.Raw)
	}
	must(t, a.post(fmt.Sprintf("projects/%d/update/", m.id()), nil), 405)
}
