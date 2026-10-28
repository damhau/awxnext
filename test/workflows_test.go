package test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/jobs"
)

const showPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "artifact={{ deployed_version | default('none') }} wfvar={{ wfvar | default('none') }}"
      run_once: true
`

// itemPlaybook prints its loop item, publishes it as an artifact and fails on "bad".
const itemPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "region={{ region | default('none') }} index={{ loop_index | default('none') }}"
      run_once: true
    - ansible.builtin.fail:
        msg: bad region
      when: region | default('') == 'bad'
      run_once: true
    - ansible.builtin.set_stats:
        data: {last_region: "{{ region | default('none') }}"}
      run_once: true
`

type wfFixture struct {
	*jobFixture
	site, fail, show, slow, item int64
}

func newWorkflowFixture(t *testing.T) *wfFixture {
	t.Helper()
	f := newJobFixture(t)
	repo := gitRepo(t, map[string]string{"site.yml": sitePlaybook, "fail.yml": failPlaybook, "show.yml": showPlaybook, "slow.yml": slowPlaybook, "item.yml": itemPlaybook})
	f.project = must(t, f.a.post("projects/", map[string]any{"name": uniq("wfproj"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	f.waitProject(t)
	w := &wfFixture{jobFixture: f}
	w.site = f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true})
	w.fail = f.template(t, "fail.yml", nil)
	w.show = f.template(t, "show.yml", nil)
	w.slow = f.template(t, "slow.yml", nil)
	w.item = f.template(t, "item.yml", nil)
	return w
}

func (w *wfFixture) wfjt(t *testing.T, extra map[string]any) int64 {
	t.Helper()
	body := map[string]any{"name": uniq("wf"), "organization": w.org}
	for k, v := range extra {
		body[k] = v
	}
	return must(t, w.a.post("workflow_job_templates/", body), 201).id()
}

func (w *wfFixture) node(t *testing.T, wf int64, ujt int64, extra map[string]any) int64 {
	t.Helper()
	body := map[string]any{"unified_job_template": ujt}
	for k, v := range extra {
		body[k] = v
	}
	return must(t, w.a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), body), 201).id()
}

func (w *wfFixture) link(t *testing.T, from int64, kind string, to int64) {
	t.Helper()
	must(t, w.a.post(fmt.Sprintf("workflow_job_template_nodes/%d/%s_nodes/", from, kind), map[string]any{"id": to}), 204)
}

func (w *wfFixture) approval(t *testing.T, wf int64, name string, timeout int) int64 {
	t.Helper()
	n := must(t, w.a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{}), 201).id()
	must(t, w.a.post(fmt.Sprintf("workflow_job_template_nodes/%d/create_approval_template/", n), map[string]any{"name": name, "timeout": timeout}), 201)
	return n
}

func (w *wfFixture) launch(t *testing.T, wf int64, body map[string]any) int64 {
	t.Helper()
	r := must(t, w.a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), body), 201)
	return int64(r.Body["workflow_job"].(float64))
}

// jobNodes maps template node identifiers' templates to workflow job nodes by unified job template id.
func (w *wfFixture) jobNodes(t *testing.T, wj int64) []map[string]any {
	return w.a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/?page_size=100&order_by=id", wj)).results()
}

func waitApproval(t *testing.T, a *client, wj int64) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/?page_size=100", wj)).results() {
			if job, ok := n["summary_fields"].(map[string]any)["job"].(map[string]any); ok && job["type"] == "workflow_approval" && job["status"] == "pending" {
				return a.get(fmt.Sprintf("workflow_approvals/%d/", int64(job["id"].(float64)))).Body
			}
		}
		if wfj := a.get(fmt.Sprintf("workflow_jobs/%d/", wj)); wfj.Body["status"] != "running" && wfj.Body["status"] != "pending" {
			t.Fatalf("workflow finished without approval: %v", wfj.Body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("approval never became pending")
	return nil
}

func TestWorkflowTemplateValidation(t *testing.T) {
	w := newWorkflowFixture(t)
	a := w.a
	wf := w.wfjt(t, nil)
	n1 := w.node(t, wf, w.site, nil)
	n2 := w.node(t, wf, w.show, nil)
	n3 := w.node(t, wf, w.fail, nil)
	w.link(t, n1, "success", n2)
	w.link(t, n2, "always", n3)
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/failure_nodes/", n3), map[string]any{"id": n1}), 400) // cycle
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/failure_nodes/", n1), map[string]any{"id": n1}), 400) // self
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/failure_nodes/", n1), map[string]any{"id": n2}), 400) // duplicate edge
	other := w.wfjt(t, nil)
	on := w.node(t, other, w.site, nil)
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/success_nodes/", n1), map[string]any{"id": on}), 400)
	// Prompts must be allowed by the node's template.
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": w.show, "limit": "h1"}), 400)
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": w.site, "extra_data": map[string]any{"v": 1}}), 201)
	sjt := int64(a.get("system_job_templates/").results()[0]["id"].(float64))
	// Management jobs are allowed (see TestWorkflowManagementJobNode), with days and dry_run only.
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": sjt, "extra_data": map[string]any{"v": 1}}), 400)
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": wf}), 400)
	// Creating a child through the parent node links it.
	child := must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/failure_nodes/", n3), map[string]any{"unified_job_template": w.show}), 201)
	if p := a.get(fmt.Sprintf("workflow_job_template_nodes/%d/", n3)); fmt.Sprint(p.Body["failure_nodes"]) != fmt.Sprintf("[%d]", child.id()) {
		t.Fatalf("failure_nodes: %v", p.Body["failure_nodes"])
	}
	node := a.get(fmt.Sprintf("workflow_job_template_nodes/%d/", n1)).Body
	if fmt.Sprint(node["success_nodes"]) != fmt.Sprintf("[%d]", n2) || node["summary_fields"].(map[string]any)["unified_job_template"].(map[string]any)["unified_job_type"] != "job" {
		t.Fatalf("node: %v", node)
	}
	// Copy keeps the graph; approval nodes get their own approval template.
	ap := w.approval(t, wf, "Gate", 60)
	cp := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/copy/", wf), map[string]any{"name": uniq("copy")}), 201)
	copied := a.get(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/?order_by=id", cp.id())).results()
	if len(copied) != 6 {
		t.Fatalf("copied nodes: %d", len(copied))
	}
	origAp := a.get(fmt.Sprintf("workflow_job_template_nodes/%d/", ap)).Body["unified_job_template"]
	cpAp := copied[5]["unified_job_template"]
	if cpAp == nil || cpAp == origAp || copied[5]["summary_fields"].(map[string]any)["unified_job_template"].(map[string]any)["name"] != "Gate" {
		t.Fatalf("copied approval node shares or lost its template: %v vs %v", cpAp, origAp)
	}
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/create_approval_template/", int64(copied[5]["id"].(float64))), map[string]any{"name": "Renamed", "timeout": 0}), 201)
	if name := a.get(fmt.Sprintf("workflow_approval_templates/%d/", int64(origAp.(float64)))).Body["name"]; name != "Gate" {
		t.Fatalf("editing the copy changed the original approval: %v", name)
	}
	must(t, a.delete(fmt.Sprintf("workflow_job_template_nodes/%d/", ap)), 204)
	info := must(t, a.get(fmt.Sprintf("workflow_job_templates/%d/launch/", wf)), 200).Body
	if info["can_start_without_user_input"] != true {
		t.Fatalf("launch info: %v", info)
	}

	// RBAC: workflow admins need execute on the templates they add.
	user, uid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "workflow_job_templates", wf, "admin_role")}), 204)
	must(t, user.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": w.site}), 403)
	must(t, user.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{}), 201)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "job_templates", w.site, "execute_role")}), 204)
	must(t, user.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": w.site}), 201)
}

func TestWorkflowRun(t *testing.T) {
	w := newWorkflowFixture(t)
	a := w.a
	wf := w.wfjt(t, map[string]any{"extra_vars": "wfvar: fromwf"})
	nA := w.node(t, wf, w.site, map[string]any{"extra_data": map[string]any{"version": "v1"}})
	nB := w.node(t, wf, w.show, nil)
	nC := w.node(t, wf, w.show, nil)
	nD := w.node(t, wf, w.fail, nil)
	nE := w.approval(t, wf, "Ship it?", 0)
	nF := w.node(t, wf, w.show, nil)
	w.link(t, nA, "success", nB)
	w.link(t, nA, "failure", nC)
	w.link(t, nB, "always", nD)
	w.link(t, nD, "failure", nE)
	w.link(t, nE, "success", nF)

	wj := w.launch(t, wf, nil)
	ap := waitApproval(t, a, wj)
	if ap["can_approve_or_deny"] != true || ap["name"] != "Ship it?" {
		t.Fatalf("approval: %v", ap)
	}
	apID := int64(ap["id"].(float64))
	user, _ := newUser(t, a)
	must(t, user.post(fmt.Sprintf("workflow_approvals/%d/approve/", apID), nil), 403)
	must(t, a.post(fmt.Sprintf("workflow_approvals/%d/approve/", apID), nil), 204)
	must(t, a.post(fmt.Sprintf("workflow_approvals/%d/deny/", apID), nil), 400)
	done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj))
	if done.Body["status"] != "successful" {
		t.Fatalf("workflow: %v (%v)", done.Body["status"], done.Body["job_explanation"])
	}
	status := map[int64]string{}
	dnr := map[int64]bool{}
	var showJobs []int64
	for _, n := range w.jobNodes(t, wj) {
		ujt := int64(n["unified_job_template"].(float64))
		if n["do_not_run"] == true {
			dnr[ujt] = true
		}
		if job, ok := n["summary_fields"].(map[string]any)["job"].(map[string]any); ok {
			status[int64(n["id"].(float64))] = job["status"].(string)
			if ujt == w.show {
				showJobs = append(showJobs, int64(job["id"].(float64)))
			}
		}
	}
	if !dnr[w.show] || len(showJobs) != 2 {
		t.Fatalf("expected the failure path of A to be skipped: dnr=%v show jobs=%v", dnr, showJobs)
	}
	out := w.stdout(t, showJobs[0])
	if !strings.Contains(out, "artifact=v1 wfvar=fromwf") {
		t.Fatalf("artifacts and workflow vars not passed:\n%s", out)
	}
	job := a.get(fmt.Sprintf("jobs/%d/", showJobs[0])).Body
	if job["launch_type"] != "workflow" || job["summary_fields"].(map[string]any)["source_workflow_job"].(map[string]any)["id"] != float64(wj) {
		t.Fatalf("spawned job: %v", job)
	}
	if lb := job["launched_by"].(map[string]any); lb["type"] != "workflow_job" {
		t.Fatalf("launched_by: %v", lb)
	}
	_ = nC
	_ = nF

	// A failure without an error path fails the workflow; a denial follows the failure path.
	wf2 := w.wfjt(t, nil)
	w.node(t, wf2, w.fail, nil)
	res := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", w.launch(t, wf2, nil)))
	if res.Body["status"] != "failed" || !strings.Contains(res.Body["job_explanation"].(string), "No error handling path") {
		t.Fatalf("expected failure: %v", res.Body)
	}
	wf3 := w.wfjt(t, nil)
	gate := w.approval(t, wf3, "gate", 0)
	onDeny := w.node(t, wf3, w.show, nil)
	w.link(t, gate, "failure", onDeny)
	wj3 := w.launch(t, wf3, nil)
	ap3 := waitApproval(t, a, wj3)
	must(t, a.post(fmt.Sprintf("workflow_approvals/%d/deny/", int64(ap3["id"].(float64))), nil), 204)
	if r := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj3)); r.Body["status"] != "successful" {
		t.Fatalf("denied path: %v", r.Body)
	}
	if r := a.get(fmt.Sprintf("workflow_approvals/%d/", int64(ap3["id"].(float64)))); r.Body["status"] != "failed" ||
		r.Body["summary_fields"].(map[string]any)["approved_or_denied_by"].(map[string]any)["username"] != "admin" {
		t.Fatalf("denied approval: %v", r.Body)
	}

	// Approvals expire.
	wf4 := w.wfjt(t, nil)
	w.approval(t, wf4, "quick", 1)
	r4 := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", w.launch(t, wf4, nil)))
	if r4.Body["status"] != "failed" {
		t.Fatalf("expired approval: %v", r4.Body)
	}
	if aps := a.get("workflow_approvals/?timed_out=true&name=quick"); aps.count() == 0 {
		t.Fatal("timed out approval not recorded")
	}

	// Relaunch copies the graph of the original run.
	rl := must(t, a.post(fmt.Sprintf("workflow_jobs/%d/relaunch/", wj3), nil), 201)
	if len(w.jobNodes(t, int64(rl.Body["workflow_job"].(float64)))) != 2 {
		t.Fatal("relaunch nodes")
	}
	a.post(fmt.Sprintf("workflow_jobs/%d/cancel/", int64(rl.Body["workflow_job"].(float64))), nil)
}

func TestWorkflowConvergenceNestingCancel(t *testing.T) {
	w := newWorkflowFixture(t)
	a := w.a
	// all_parents_must_converge: one failing parent stops the child.
	wf := w.wfjt(t, nil)
	p1 := w.node(t, wf, w.site, nil)
	p2 := w.node(t, wf, w.fail, nil)
	child := w.node(t, wf, w.show, map[string]any{"all_parents_must_converge": true})
	after := w.node(t, wf, w.show, nil)
	w.link(t, p1, "success", child)
	w.link(t, p2, "success", child)
	w.link(t, p2, "failure", after)
	wj := w.launch(t, wf, nil)
	waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj))
	ran := 0
	for _, n := range w.jobNodes(t, wj) {
		if n["all_parents_must_converge"] == true && n["do_not_run"] != true {
			t.Fatalf("converging node should not run: %v", n)
		}
		if n["job"] != nil {
			ran++
		}
	}
	if ran != 3 {
		t.Fatalf("expected 3 jobs, got %d", ran)
	}

	// Nested workflows run; recursion is refused.
	inner := w.wfjt(t, nil)
	w.node(t, inner, w.show, nil)
	outer := w.wfjt(t, nil)
	w.node(t, outer, inner, nil)
	ow := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", w.launch(t, outer, nil)))
	if ow.Body["status"] != "successful" {
		t.Fatalf("nested: %v", ow.Body)
	}
	loop := w.wfjt(t, nil)
	mid := w.wfjt(t, nil)
	w.node(t, loop, mid, nil)
	w.node(t, mid, loop, nil)
	lw := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", w.launch(t, loop, nil)))
	if lw.Body["status"] != "failed" {
		t.Fatalf("recursion should fail: %v", lw.Body)
	}

	// Canceling a workflow cancels its running jobs.
	cw := w.wfjt(t, nil)
	w.node(t, cw, w.slow, nil)
	cj := w.launch(t, cw, nil)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ns := w.jobNodes(t, cj)
		if len(ns) > 0 && ns[0]["job"] != nil {
			if s := ns[0]["summary_fields"].(map[string]any)["job"].(map[string]any)["status"]; s == "running" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	must(t, a.post(fmt.Sprintf("workflow_jobs/%d/cancel/", cj), nil), 202)
	if r := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", cj)); r.Body["status"] != "canceled" {
		t.Fatalf("cancel: %v", r.Body)
	}
}

func TestJobSlicing(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", map[string]any{"job_slice_count": 3})
	r := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), nil), 201)
	if r.Body["type"] != "workflow_job" || r.Body["is_sliced_job"] != true || r.Body["job_template"] != float64(jt) {
		t.Fatalf("sliced launch: %v", r.Body)
	}
	wj := int64(r.Body["workflow_job"].(float64))
	done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj))
	if done.Body["status"] != "successful" {
		t.Fatalf("sliced workflow: %v", done.Body)
	}
	slices := a.get(fmt.Sprintf("job_templates/%d/jobs/?order_by=job_slice_number", jt)).results()
	if len(slices) != 3 {
		t.Fatalf("slices: %d", len(slices))
	}
	for i, s := range slices {
		id := int64(s["id"].(float64))
		out := f.stdout(t, id)
		want := fmt.Sprintf("from h%d", i+1)
		if s["job_slice_number"] != float64(i+1) || s["job_slice_count"] != float64(3) || !strings.Contains(out, want) || strings.Count(out, "hello nobody from") != 1 {
			t.Fatalf("slice %d: %v\n%s", i+1, s, out)
		}
	}
	// More slices than hosts: one slice per host.
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"job_slice_count": 10}), 200)
	r = must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), nil), 201)
	wj2 := int64(r.Body["workflow_job"].(float64))
	waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj2))
	if n := len(a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", wj2)).results()); n != 3 {
		t.Fatalf("slices capped by hosts: %d", n)
	}
	rl := must(t, a.post(fmt.Sprintf("workflow_jobs/%d/relaunch/", wj), nil), 201)
	if rl.Body["is_sliced_job"] != true {
		t.Fatalf("sliced relaunch: %v", rl.Body)
	}
	waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", int64(rl.Body["workflow_job"].(float64))))
}

// A restart must not fail workflow jobs: they have no process, the dispatcher
// keeps driving them. Jobs with a process on this node are failed.
func TestRestartKeepsWorkflowJobs(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": org}), 201).id()
	ctx := t.Context()
	var wfJob, job int64
	if err := app.DB.QueryRow(ctx, `INSERT INTO unified_jobs (type, name, status, unified_job_template_id, controller_node, started)
		VALUES ('workflow_job', 'orphan wf', 'running', $1, 'test-node', now()) RETURNING id`, wf).Scan(&wfJob); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.QueryRow(ctx, `INSERT INTO unified_jobs (type, name, status, controller_node, started)
		VALUES ('project_update', 'orphan update', 'running', 'test-node', now()) RETURNING id`).Scan(&job); err != nil {
		t.Fatal(err)
	}
	jobManager.FailOrphans(ctx)
	if s := a.get(fmt.Sprintf("unified_jobs/?id=%d", job)).results()[0]["status"]; s != "failed" {
		t.Fatalf("orphaned update: %v", s)
	}
	// The dispatcher may already have completed the empty workflow; it must not have been failed.
	if s := a.get(fmt.Sprintf("unified_jobs/?id=%d", wfJob)).results()[0]; s["status"] == "failed" {
		t.Fatalf("workflow job failed: %v", s["job_explanation"])
	}
	app.DB.Exec(ctx, "UPDATE unified_jobs SET status = 'canceled', finished = now() WHERE id = $1", wfJob)
}

// Ending a workflow job by any path cancels its pending approvals, and
// links between nodes can be removed and re-added with another kind.
func TestWorkflowApprovalCleanupAndUnlink(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": org}), 201).id()
	n1 := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/create_approval_template/", n1), map[string]any{"name": "gate"}), 201)
	n2 := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/create_approval_template/", n2), map[string]any{"name": "second"}), 201)
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/success_nodes/", n1), map[string]any{"id": n2}), 204)
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/success_nodes/", n1), map[string]any{"id": n2, "disassociate": true}), 204)
	if n := a.get(fmt.Sprintf("workflow_job_template_nodes/%d/success_nodes/", n1)).count(); n != 0 {
		t.Fatalf("link not removed: %d", n)
	}
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/failure_nodes/", n1), map[string]any{"id": n2}), 204)

	wj := int64(must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), nil), 201).Body["workflow_job"].(float64))
	var approval int64
	deadline := time.Now().Add(20 * time.Second)
	for approval == 0 && time.Now().Before(deadline) {
		for _, n := range a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", wj)).results() {
			if id, ok := n["job"].(float64); ok {
				approval = int64(id)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if approval == 0 {
		t.Fatal("approval never started")
	}
	if err := jobManager.Complete(t.Context(), wj, jobs.Result{Status: "failed", Explanation: "test"}); err != nil {
		t.Fatal(err)
	}
	if s := waitJob(t, a, fmt.Sprintf("workflow_approvals/%d/", approval)).Body["status"]; s != "canceled" {
		t.Fatalf("approval after workflow ended: %v", s)
	}
}

// nodeByID indexes a workflow job's nodes by identifier.
func (w *wfFixture) nodeByID(t *testing.T, wj int64) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, n := range w.jobNodes(t, wj) {
		out[n["identifier"].(string)] = n
	}
	return out
}

func nodeJob(n map[string]any) map[string]any {
	job, _ := n["summary_fields"].(map[string]any)["job"].(map[string]any)
	return job
}

func TestWorkflowConditions(t *testing.T) {
	w := newWorkflowFixture(t)
	a := w.a
	wf := w.wfjt(t, nil)
	id := func(name string) map[string]any { return map[string]any{"identifier": name} }
	with := func(name string, extra map[string]any) map[string]any {
		m := id(name)
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	build := w.node(t, wf, w.site, with("build", map[string]any{"extra_data": map[string]any{"version": "v2"}}))
	onV2 := w.node(t, wf, w.show, with("on-v2", map[string]any{"when": "deployed_version == 'v2'"}))
	onV1 := w.node(t, wf, w.show, with("on-v1", map[string]any{"when": "deployed_version == 'v1'"}))
	afterV1 := w.node(t, wf, w.show, id("after-v1"))
	byNode := w.node(t, wf, w.show, with("by-node", map[string]any{"when": "nodes.build.status == 'successful' and nodes.build.artifacts.deployed_version == 'v2'"}))
	broken := w.node(t, wf, w.show, with("broken", map[string]any{"when": "nodes.nope.status == 'x'"}))
	rescue := w.node(t, wf, w.show, id("rescue"))
	for _, to := range []int64{onV2, onV1, byNode, broken} {
		w.link(t, build, "success", to)
	}
	w.link(t, onV1, "success", afterV1)
	w.link(t, broken, "failure", rescue)

	// Invalid expressions are rejected when saved.
	must(t, a.patch(fmt.Sprintf("workflow_job_template_nodes/%d/", onV2), map[string]any{"when": "deployed_version =="}), 400)

	wj := w.launch(t, wf, nil)
	if done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj)); done.Body["status"] != "successful" {
		t.Fatalf("workflow: %v (%v)", done.Body["status"], done.Body["job_explanation"])
	}
	nodes := w.nodeByID(t, wj)
	for _, ran := range []string{"build", "on-v2", "by-node", "rescue"} {
		if job := nodeJob(nodes[ran]); job == nil || job["status"] != "successful" {
			t.Fatalf("%s should have run: %v", ran, nodes[ran])
		}
	}
	if n := nodes["on-v1"]; n["do_not_run"] != true || n["skip_reason"] != "Condition is false: deployed_version == 'v1'" || nodeJob(n) != nil {
		t.Fatalf("on-v1 should be skipped by its condition: %v", n)
	}
	if n := nodes["after-v1"]; n["do_not_run"] != true || n["skip_reason"] != "" {
		t.Fatalf("after-v1 should not run: %v", n)
	}
	job := nodeJob(nodes["broken"])
	if job == nil || job["status"] != "failed" {
		t.Fatalf("broken condition should fail the node: %v", nodes["broken"])
	}
	if exp := a.get(fmt.Sprintf("jobs/%d/", int64(job["id"].(float64)))).Body["job_explanation"].(string); !strings.Contains(exp, "condition could not be evaluated") {
		t.Fatalf("explanation: %s", exp)
	}
}

func TestWorkflowLoops(t *testing.T) {
	w := newWorkflowFixture(t)
	a := w.a
	wf := w.wfjt(t, map[string]any{"extra_vars": "regions: [eu, us, ap]"})
	node := func(ujt int64, ident string, extra map[string]any) int64 {
		body := map[string]any{"identifier": ident}
		for k, v := range extra {
			body[k] = v
		}
		return w.node(t, wf, ujt, body)
	}
	regions := node(w.item, "regions", map[string]any{"loop": "regions", "loop_var": "region", "loop_concurrency": 1})
	after := node(w.show, "after", map[string]any{"when": "last_region == 'ap' and len(nodes.regions.items) == 3"})
	empty := node(w.item, "empty", map[string]any{"loop": "[]", "loop_var": "region"})
	afterEmpty := node(w.show, "after-empty", nil)
	mixed := node(w.item, "mixed", map[string]any{"loop": "['ok', 'bad']", "loop_var": "region"})
	onFail := node(w.show, "on-fail", nil)
	onSuccess := node(w.show, "on-success", nil)
	w.link(t, regions, "success", after)
	w.link(t, empty, "success", afterEmpty)
	w.link(t, mixed, "failure", onFail)
	w.link(t, mixed, "success", onSuccess)

	// Validation.
	ap := w.approval(t, wf, "Gate", 0)
	must(t, a.patch(fmt.Sprintf("workflow_job_template_nodes/%d/", ap), map[string]any{"loop": "regions"}), 400)
	must(t, a.patch(fmt.Sprintf("workflow_job_template_nodes/%d/", regions), map[string]any{"loop_var": "1bad"}), 400)
	must(t, a.patch(fmt.Sprintf("workflow_job_template_nodes/%d/", regions), map[string]any{"loop_concurrency": -1}), 400)
	must(t, a.delete(fmt.Sprintf("workflow_job_template_nodes/%d/", ap)), 204)

	wj := w.launch(t, wf, nil)
	if done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj)); done.Body["status"] != "successful" {
		t.Fatalf("workflow: %v (%v)", done.Body["status"], done.Body["job_explanation"])
	}
	nodes := w.nodeByID(t, wj)

	// One job per item, in order, one at a time.
	n := nodes["regions"]
	iters := n["iterations"].([]any)
	if len(iters) != 3 || fmt.Sprint(n["loop_items"]) != "[eu us ap]" || n["job"] != nil {
		t.Fatalf("regions loop: %v", n)
	}
	var prevFinished string
	for i, it := range iters {
		job := it.(map[string]any)["job"].(map[string]any)
		jid := int64(job["id"].(float64))
		region := []string{"eu", "us", "ap"}[i]
		if job["status"] != "successful" || !strings.HasSuffix(job["name"].(string), "("+region+")") {
			t.Fatalf("iteration %d: %v", i, job)
		}
		if out := w.stdout(t, jid); !strings.Contains(out, fmt.Sprintf("region=%s index=%d", region, i)) {
			t.Fatalf("iteration %d output:\n%s", i, out)
		}
		j := a.get(fmt.Sprintf("jobs/%d/", jid)).Body
		if src, _ := j["summary_fields"].(map[string]any)["source_workflow_job"].(map[string]any); src == nil || src["id"] != float64(wj) {
			t.Fatalf("iteration %d source workflow: %v", i, j["summary_fields"])
		}
		if prevFinished != "" && j["started"].(string) < prevFinished {
			t.Fatalf("iteration %d started at %v before the previous one finished at %v", i, j["started"], prevFinished)
		}
		prevFinished = j["finished"].(string)
	}
	// Artifacts merge in item order, and conditions see the node's items.
	if job := nodeJob(nodes["after"]); job == nil || job["status"] != "successful" {
		t.Fatalf("after: %v", nodes["after"])
	}
	// An empty loop succeeds without jobs.
	if n := nodes["empty"]; len(n["iterations"].([]any)) != 0 || nodeJob(nodes["after-empty"]) == nil {
		t.Fatalf("empty loop: %v / %v", n, nodes["after-empty"])
	}
	// One failed item fails the node.
	if len(nodes["mixed"]["iterations"].([]any)) != 2 || nodeJob(nodes["on-fail"]) == nil || nodes["on-success"]["do_not_run"] != true {
		t.Fatalf("mixed loop: %v / on-fail %v / on-success %v", nodes["mixed"], nodes["on-fail"], nodes["on-success"])
	}

	// Canceling the workflow cancels running iterations.
	wf2 := w.wfjt(t, nil)
	w.node(t, wf2, w.slow, map[string]any{"identifier": "slow", "loop": "['a', 'b']"})
	wj2 := w.launch(t, wf2, nil)
	deadline := time.Now().Add(30 * time.Second)
	for len(w.nodeByID(t, wj2)["slow"]["iterations"].([]any)) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("slow iterations never started")
		}
		time.Sleep(100 * time.Millisecond)
	}
	must(t, a.post(fmt.Sprintf("workflow_jobs/%d/cancel/", wj2), nil), 202)
	if done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj2)); done.Body["status"] != "canceled" {
		t.Fatalf("canceled workflow: %v", done.Body["status"])
	}
	for _, it := range w.nodeByID(t, wj2)["slow"]["iterations"].([]any) {
		job := it.(map[string]any)["job"].(map[string]any)
		if st := waitJob(t, a, fmt.Sprintf("jobs/%d/", int64(job["id"].(float64)))).Body["status"]; st != "canceled" {
			t.Fatalf("iteration not canceled: %v", st)
		}
	}
}

// Management jobs can be workflow steps, with only days and dry_run.
func TestWorkflowManagementJobNode(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	cleanup := int64(must(t, a.get("system_job_templates/?job_type=cleanup_jobs"), 200).results()[0]["id"].(float64))
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": org}), 201).id()
	nodes := fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf)
	must(t, a.post(nodes, map[string]any{"unified_job_template": cleanup, "extra_data": map[string]any{"limit": "x"}}), 400)
	must(t, a.post(nodes, map[string]any{"unified_job_template": cleanup, "extra_data": map[string]any{"days": 90, "dry_run": true}}), 201)
	user, uid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "workflow_job_templates", wf, "admin_role")}), 204)
	must(t, user.post(nodes, map[string]any{"unified_job_template": cleanup}), 403)

	r := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), nil), 201)
	wj := int64(r.Body["workflow_job"].(float64))
	if done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj)); done.Body["status"] != "successful" {
		t.Fatalf("workflow: %v (%v)", done.Body["status"], done.Body["job_explanation"])
	}
	n := a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", wj)).results()[0]
	job := n["summary_fields"].(map[string]any)["job"].(map[string]any)
	if job["type"] != "system_job" || job["status"] != "successful" {
		t.Fatalf("management job node: %v", job)
	}
	if ev := a.get(fmt.Sprintf("system_jobs/%d/", int64(job["id"].(float64)))).Body["extra_vars"]; !strings.Contains(fmt.Sprint(ev), "90") {
		t.Fatalf("days not passed: %v", ev)
	}
}

// A workflow's node links and labels change only for those who may change the
// workflow: viewers could remove links before.
func TestWorkflowNodeSublistsNeedChange(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	jt := f.template(t, "site.yml", nil)
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org}), 201).id()
	nodes := fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf)
	n1 := must(t, a.post(nodes, map[string]any{"unified_job_template": jt}), 201).id()
	n2 := must(t, a.post(nodes, map[string]any{"unified_job_template": jt}), 201).id()
	link := fmt.Sprintf("workflow_job_template_nodes/%d/success_nodes/", n1)
	must(t, a.post(link, map[string]any{"id": n2}), 204)

	viewer, vid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", vid), map[string]any{"id": objectRole(t, a, "workflow_job_templates", wf, "read_role")}), 204)
	must(t, viewer.post(link, map[string]any{"id": n2, "disassociate": true}), 403)
	if a.get(link).count() != 1 {
		t.Fatal("a viewer removed a link")
	}

	editor, eid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", eid), map[string]any{"id": objectRole(t, a, "workflow_job_templates", wf, "admin_role")}), 204)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", eid), map[string]any{"id": objectRole(t, a, "job_templates", jt, "execute_role")}), 204)
	must(t, editor.post(link, map[string]any{"id": n2, "disassociate": true}), 204)
	must(t, editor.post(link, map[string]any{"id": n2}), 204)
}
