package test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func gitopsFiles(user string) map[string]string {
	return map[string]string{
		"site.yml": helloPlaybook,
		"gitops/labels.yml": `kind: Label
name: web
---
kind: Label
name: db
`,
		"gitops/inventory.yml": `kind: Inventory
name: Production
description: prod hosts
spec:
  variables:
    ntp: pool.ntp.org
content:
  all:
    children:
      web:
        vars: {port: 8080}
        hosts:
          web[01:03]:
      db:
        hosts:
          db1: {ansible_host: 10.0.0.5}
`,
		"gitops/templates.yml": `kind: JobTemplate
name: Deploy web
description: v1
spec:
  project: gitops-project
  inventory: Production
  playbook: site.yml
  limit: web
  ask_limit_on_launch: true
  labels: [web]
  survey_enabled: true
  survey_spec:
    name: deploy
    description: ""
    spec:
      - {question_name: Version, variable: version, type: text, required: true, default: "1.0"}
---
kind: WorkflowJobTemplate
name: Release
nodes:
  - id: deploy
    template: {kind: JobTemplate, name: Deploy web}
    limit: web01
    success: [approve]
  - id: approve
    approval: {name: Sign off, timeout: 600}
    always: [again]
  - id: again
    template: {kind: JobTemplate, name: Deploy web}
    when: nodes.deploy.status == 'successful'
    loop: "['web01', 'web02']"
    loop_var: target
    loop_concurrency: 1
---
kind: Schedule
name: Nightly deploy
spec:
  template: {kind: JobTemplate, name: Deploy web}
  rrule: "DTSTART:20300101T020000Z RRULE:FREQ=DAILY;INTERVAL=1"
---
kind: NotificationTemplate
name: Chat hook
spec:
  notification_type: webhook
  notification_configuration:
    url: https://hooks.example.com/x
    http_method: POST
    headers: {}
---
kind: Team
name: Web operators
members: [` + user + `]
`,
	}
}

func TestGitOpsReconcile(t *testing.T) {
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	_, uid := newUser(t, a)
	user := a.get(fmt.Sprintf("users/%d/", uid)).Body["username"].(string)
	repo := gitRepo(t, gitopsFiles(user))
	proj := must(t, a.post("projects/", map[string]any{"name": "gitops-project", "organization": org, "scm_type": "git", "scm_url": "file://" + repo}), 201).id()
	waitProjectSynced(t, a, proj, "")

	src := must(t, a.post("gitops_sources/", map[string]any{"name": uniq("src"), "organization": org, "project": proj, "path": "gitops",
		"sync_on_project_update": false}), 201)
	sid := src.id()
	syncURL := fmt.Sprintf("gitops_sources/%d/sync/", sid)

	// A dry run plans every object without writing anything.
	plan := must(t, a.post(syncURL+"?dry_run=true", nil), 200)
	if plan.Body["dry_run"] != true || summary(plan, "create") != 8 {
		t.Fatalf("plan: %s", plan.Raw)
	}
	if a.get(fmt.Sprintf("job_templates/?organization=%d&name=Deploy+web", org)).count() != 0 {
		t.Fatal("dry run wrote objects")
	}

	rep := must(t, a.post(syncURL, nil), 202)
	if summary(rep, "create") != 8 || summary(rep, "error") != 0 {
		t.Fatalf("sync: %s", rep.Raw)
	}
	tag := fmt.Sprintf("gitops:%d", sid)
	jt := one(t, a, fmt.Sprintf("job_templates/?organization=%d&name=Deploy+web", org))
	if jt["managed_by"] != tag || jt["limit"] != "web" || jt["survey_enabled"] != true {
		t.Fatalf("job template: %v", jt)
	}
	jtID := int64(jt["id"].(float64))
	if l := names(a.get(fmt.Sprintf("job_templates/%d/labels/", jtID))); len(l) != 1 || l[0] != "web" {
		t.Fatalf("labels %v", l)
	}
	if q := a.get(fmt.Sprintf("job_templates/%d/survey_spec/", jtID)).Body["spec"].([]any); len(q) != 1 {
		t.Fatalf("survey %v", q)
	}
	inv := one(t, a, fmt.Sprintf("inventories/?organization=%d&name=Production", org))
	invID := int64(inv["id"].(float64))
	hosts := names(a.get(fmt.Sprintf("inventories/%d/hosts/?order_by=name", invID)))
	if fmt.Sprint(hosts) != "[db1 web01 web02 web03]" {
		t.Fatalf("hosts %v", hosts)
	}
	web := one(t, a, fmt.Sprintf("groups/?inventory=%d&name=web", invID))
	if n := a.get(fmt.Sprintf("groups/%d/hosts/", int64(web["id"].(float64)))).count(); n != 3 {
		t.Fatalf("web group has %d hosts", n)
	}
	wf := one(t, a, fmt.Sprintf("workflow_job_templates/?organization=%d&name=Release", org))
	wfID := int64(wf["id"].(float64))
	nodes := a.get(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/?order_by=id", wfID)).results()
	if len(nodes) != 3 || nodes[0]["limit"] != "web01" {
		t.Fatalf("nodes %v", nodes)
	}
	if s := nodes[0]["success_nodes"].([]any); len(s) != 1 {
		t.Fatalf("edges %v", nodes[0])
	}
	if n := nodes[2]; n["when"] != "nodes.deploy.status == 'successful'" || n["loop"] != "['web01', 'web02']" || n["loop_var"] != "target" || n["loop_concurrency"] != float64(1) {
		t.Fatalf("condition and loop %v", n)
	}
	sched := one(t, a, "schedules/?name=Nightly+deploy")
	if int64(sched["unified_job_template"].(float64)) != jtID || sched["managed_by"] != tag {
		t.Fatalf("schedule %v", sched)
	}
	team := one(t, a, fmt.Sprintf("teams/?organization=%d&name=Web+operators", org))
	teamID := int64(team["id"].(float64))
	if n := a.get(fmt.Sprintf("teams/%d/users/", teamID)).count(); n != 1 {
		t.Fatalf("team members %d", n)
	}

	// Managed objects (and their children) are locked against API edits.
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jtID), map[string]any{"limit": "db"}), 409)
	must(t, a.post(fmt.Sprintf("inventories/%d/hosts/", invID), map[string]any{"name": "rogue"}), 409)
	must(t, a.post(fmt.Sprintf("job_templates/%d/survey_spec/", jtID), map[string]any{}), 409)
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wfID), map[string]any{"unified_job_template": jtID}), 409)
	must(t, a.delete(fmt.Sprintf("schedules/%d/", int64(sched["id"].(float64)))), 409)

	// Syncing an unchanged revision changes nothing.
	again := must(t, a.post(syncURL, nil), 202)
	if summary(again, "unchanged") != 8 {
		t.Fatalf("resync: %s", again.Raw)
	}

	// A new commit updates, prunes and is applied after the project update.
	must(t, a.patch(fmt.Sprintf("gitops_sources/%d/", sid), map[string]any{"sync_on_project_update": true}), 200)
	files := gitopsFiles(user)
	os.Remove(filepath.Join(repo, "gitops/labels.yml"))
	files["gitops/labels.yml"] = "kind: Label\nname: db\n"
	files["gitops/inventory.yml"] = `kind: Inventory
name: Production
description: prod hosts
content:
  all:
    children:
      web:
        hosts:
          web01:
`
	files["gitops/templates.yml"] = `kind: JobTemplate
name: Deploy web
description: v2
spec:
  project: gitops-project
  inventory: Production
  playbook: site.yml
  labels: [db]
---
kind: Team
name: Web operators
members: []
`
	sha := commitFiles(t, repo, files, "second")
	must(t, a.post(fmt.Sprintf("projects/%d/update/", proj), nil), 202)
	waitSource(t, a, sid, sha)
	jt = a.get(fmt.Sprintf("job_templates/%d/", jtID)).Body
	if jt["description"] != "v2" || jt["limit"] != "" || jt["survey_enabled"] != false {
		t.Fatalf("updated template: %v", jt)
	}
	if l := names(a.get(fmt.Sprintf("job_templates/%d/labels/", jtID))); fmt.Sprint(l) != "[db]" {
		t.Fatalf("labels %v", l)
	}
	if hosts := names(a.get(fmt.Sprintf("inventories/%d/hosts/", invID))); fmt.Sprint(hosts) != "[web01]" {
		t.Fatalf("hosts after prune %v", hosts)
	}
	if v := a.get(fmt.Sprintf("inventories/%d/", invID)).Body["variables"]; v != "" {
		t.Fatalf("variables not reset: %q", v)
	}
	for _, gone := range []string{
		fmt.Sprintf("workflow_job_templates/?organization=%d&name=Release", org),
		"schedules/?name=Nightly+deploy",
		fmt.Sprintf("labels/?organization=%d&name=web", org),
		fmt.Sprintf("notification_templates/?organization=%d&name=Chat+hook", org),
	} {
		if n := a.get(gone).count(); n != 0 {
			t.Fatalf("%s not pruned", gone)
		}
	}
	if n := a.get(fmt.Sprintf("teams/%d/users/", teamID)).count(); n != 0 {
		t.Fatalf("team members %d", n)
	}
	report := a.get(fmt.Sprintf("gitops_sources/%d/", sid)).Body
	if report["status"] != "successful" || report["last_revision"] != sha {
		t.Fatalf("source %v", report)
	}

	// Unmanaged objects with a declared name are a conflict, and a failed sync changes nothing.
	staging := must(t, a.post("inventories/", map[string]any{"name": "Staging", "organization": org}), 201).id()
	files["gitops/staging.yml"] = "kind: Inventory\nname: Staging\ndescription: from git\n"
	files["gitops/labels.yml"] = "kind: Label\nname: db\n---\nkind: Label\nname: cache\n"
	commitFiles(t, repo, files, "third")
	must(t, a.patch(fmt.Sprintf("gitops_sources/%d/", sid), map[string]any{"sync_on_project_update": false}), 200)
	must(t, a.post(fmt.Sprintf("projects/%d/update/", proj), nil), 202)
	sha3 := waitProjectSynced(t, a, proj, sha)
	fail := must(t, a.post(syncURL, nil), 400)
	if fail.Body["error"] == nil || summary(fail, "error") != 1 {
		t.Fatalf("conflict: %s", fail.Raw)
	}
	if a.get(fmt.Sprintf("labels/?organization=%d&name=cache", org)).count() != 0 {
		t.Fatal("failed sync was partially applied")
	}
	if s := a.get(fmt.Sprintf("gitops_sources/%d/", sid)).Body; s["status"] != "failed" || s["last_revision"] != sha {
		t.Fatalf("failed source %v", s)
	}
	must(t, a.patch(fmt.Sprintf("gitops_sources/%d/", sid), map[string]any{"adopt_existing": true}), 200)
	must(t, a.post(syncURL, nil), 202)
	st := a.get(fmt.Sprintf("inventories/%d/", staging)).Body
	if st["managed_by"] != tag || st["description"] != "from git" {
		t.Fatalf("adopted %v", st)
	}
	if s := a.get(fmt.Sprintf("gitops_sources/%d/", sid)).Body; s["last_revision"] != sha3 {
		t.Fatalf("revision %v", s["last_revision"])
	}

	// Non-admins cannot see or run sources; deleting a source releases its objects.
	u, _ := newUser(t, a)
	must(t, u.get(fmt.Sprintf("gitops_sources/%d/", sid)), 403)
	must(t, u.post(syncURL, nil), 403)
	must(t, a.delete(fmt.Sprintf("gitops_sources/%d/", sid)), 204)
	if mb := a.get(fmt.Sprintf("inventories/%d/", staging)).Body["managed_by"]; mb != "" {
		t.Fatalf("not released: %v", mb)
	}
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jtID), map[string]any{"limit": "db"}), 200)
}

func TestGitOpsValidation(t *testing.T) {
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	repo := gitRepo(t, map[string]string{
		"site.yml": helloPlaybook,
		"bad.yml":  "kind: JobTemplate\nname: Broken\nspec:\n  project: nope\n  playbook: site.yml\n---\nkind: Label\nname: fine\n",
	})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("p"), "organization": org, "scm_type": "git", "scm_url": "file://" + repo}), 201).id()
	waitProjectSynced(t, a, proj, "")
	other := newOrg(t, a)
	must(t, a.post("gitops_sources/", map[string]any{"name": uniq("s"), "organization": other, "project": proj}), 400)
	sid := must(t, a.post("gitops_sources/", map[string]any{"name": uniq("s"), "organization": org, "project": proj}), 201).id()
	r := must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), map[string]any{"dry_run": true}), 400)
	changes, _ := r.Body["changes"].([]any)
	if changes == nil {
		t.Fatalf("plan: %s", r.Raw)
	}
	var errs []string
	for _, c := range changes {
		m := c.(map[string]any)
		if m["action"] == "error" {
			errs = append(errs, fmt.Sprint(m["name"], ": ", m["error"]))
		}
	}
	sort.Strings(errs)
	if len(errs) != 1 || errs[0] != `Broken: project "nope" not found` {
		t.Fatalf("errors %v", errs)
	}
	commitFiles(t, repo, map[string]string{"bad.yml": "kind: Gadget\nname: x\n"}, "unknown kind")
	must(t, a.post(fmt.Sprintf("projects/%d/update/", proj), nil), 202)
	waitProjectSynced(t, a, proj, r.Body["revision"].(string))
	r = must(t, a.post(fmt.Sprintf("gitops_sources/%d/sync/", sid), nil), 400)
	if r.Body["error"] != `bad.yml#1: unsupported kind "Gadget"` {
		t.Fatalf("unknown kind: %v", r.Body["error"])
	}
}

func summary(r resp, action string) int {
	s, _ := r.Body["summary"].(map[string]any)
	n, _ := s[action].(float64)
	return int(n)
}

func one(t *testing.T, a *client, url string) map[string]any {
	t.Helper()
	r := must(t, a.get(url), 200)
	if r.count() != 1 {
		t.Fatalf("%s: %d results", url, r.count())
	}
	return r.results()[0]
}

// waitProjectSynced waits for a successful sync to a revision other than prev.
func waitProjectSynced(t *testing.T, a *client, id int64, prev string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		p := a.get(fmt.Sprintf("projects/%d/", id)).Body
		if rev, _ := p["scm_revision"].(string); rev != "" && rev != prev && p["status"] == "successful" {
			return rev
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("project never synced")
	return ""
}

func waitSource(t *testing.T, a *client, id int64, rev string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s := a.get(fmt.Sprintf("gitops_sources/%d/", id)).Body
		if s["last_revision"] == rev {
			return
		}
		if s["status"] == "failed" && s["last_sync"] != nil {
			t.Fatalf("sync failed: %v", s["last_error"])
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("source never applied %s", rev)
}
