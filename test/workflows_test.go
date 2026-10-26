package test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const showPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "artifact={{ deployed_version | default('none') }} wfvar={{ wfvar | default('none') }}"
      run_once: true
`

type wfFixture struct {
	*jobFixture
	site, fail, show, slow int64
}

func newWorkflowFixture(t *testing.T) *wfFixture {
	t.Helper()
	f := newJobFixture(t)
	repo := gitRepo(t, map[string]string{"site.yml": sitePlaybook, "fail.yml": failPlaybook, "show.yml": showPlaybook, "slow.yml": slowPlaybook})
	f.project = must(t, f.a.post("projects/", map[string]any{"name": uniq("wfproj"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	f.waitProject(t)
	w := &wfFixture{jobFixture: f}
	w.site = f.template(t, "site.yml", map[string]any{"ask_variables_on_launch": true})
	w.fail = f.template(t, "fail.yml", nil)
	w.show = f.template(t, "show.yml", nil)
	w.slow = f.template(t, "slow.yml", nil)
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
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": sjt}), 400)
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
	// Copy keeps the graph.
	cp := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/copy/", wf), map[string]any{"name": uniq("copy")}), 201)
	if n := a.get(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", cp.id())).count(); n != 5 {
		t.Fatalf("copied nodes: %d", n)
	}
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
