package test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/resources"
)

func policyOutcomes(t *testing.T, a *client, url string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, d := range must(t, a.get(url), 200).results() {
		out[d["policy_name"].(string)] = d
	}
	return out
}

func wantPolicyOutcome(t *testing.T, got map[string]map[string]any, policy, outcome string) {
	t.Helper()
	if d := got[policy]; d == nil || d["outcome"] != outcome {
		t.Errorf("%s: %v, want %s", policy, got[policy], outcome)
	}
}

func refusedBy(t *testing.T, r resp, names ...string) {
	t.Helper()
	detail := fmt.Sprint(r.Body["detail"])
	list, _ := r.Body["launch_policies"].([]any)
	if r.Status != 400 || len(list) != len(names) {
		t.Fatalf("refusal: %d %v", r.Status, r.Body)
	}
	for _, n := range names {
		if !strings.Contains(detail, fmt.Sprintf("Refused by launch policy %q", n)) {
			t.Errorf("refusal doesn't name %s: %s", n, detail)
		}
	}
}

// TestLaunchPolicies checks launch policies on each launch path: refused
// at the caller with a 400 and no job, or, without a caller, as a job that
// fails at once and notifies; recorded only in shadow mode; with the check
// endpoint, the portal's preview, backtests, rights and retention.
func TestLaunchPolicies(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	ctx := context.Background()
	rec, hook := newHookServer(t)

	jt := f.template(t, "site.yml", map[string]any{"name": uniq("deploy"), "ask_limit_on_launch": true, "ask_verbosity_on_launch": true, "ask_variables_on_launch": true})
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": hook.URL}}), 201).id()
	must(t, a.post(fmt.Sprintf("job_templates/%d/notification_templates_error/", jt), map[string]any{"id": nt}), 204)
	if h := a.get(fmt.Sprintf("inventories/%d/", f.inv)).Body["total_hosts"]; h != float64(3) {
		t.Fatalf("total hosts: %v", h)
	}

	// The condition is type-checked on save.
	for cond, want := range map[string]string{`launch.inventroy.name == "x"`: "undefined field", `launch.limit`: "true or false",
		`window(now, "Mon-Funday")`: "unknown day"} {
		r := must(t, a.post("launch_policies/", map[string]any{"name": uniq("bad"), "organization": f.org, "condition": cond}), 400)
		if !strings.Contains(fmt.Sprint(r.Body["condition"]), want) {
			t.Errorf("%s: %v", cond, r.Body)
		}
	}
	must(t, a.post("launch_policies/", map[string]any{"name": uniq("bad"), "organization": f.org, "condition": "true", "mode": "loud"}), 400)
	if r := must(t, a.post("launch_policies/check/", map[string]any{"condition": `launch.limit == ""`}), 200); r.Body["valid"] != true {
		t.Errorf("check: %v", r.Body)
	}
	if r := must(t, a.post("launch_policies/check/", map[string]any{"condition": `launch.limt == ""`}), 200); r.Body["valid"] != false {
		t.Errorf("check: %v", r.Body)
	}
	if fields := must(t, a.get("launch_policies/fields/"), 200).Body["fields"].([]any); len(fields) < 40 {
		t.Errorf("fields: %d", len(fields))
	}

	// Rights: global policies are the superusers'; an organization's admin
	// makes its policies and attaches them to what they may change.
	orgAdmin, orgAdminID := newUser(t, a)
	grant(t, a, "Organization Admin", orgAdminID, f.org)
	must(t, orgAdmin.post("launch_policies/", map[string]any{"name": uniq("global"), "condition": "true"}), 403)
	limitName := uniq("limit-required")
	pLimit := must(t, orgAdmin.post("launch_policies/", map[string]any{"name": limitName, "organization": f.org, "mode": "enforce",
		"condition": `launch.limit == "" && launch.inventory.hosts >= 3`, "message": "Give a limit: this inventory is large."}), 201)
	if pLimit.Body["mode"] != "enforce" {
		t.Fatalf("policy: %v", pLimit.Body)
	}
	must(t, orgAdmin.patch(fmt.Sprintf("launch_policies/%d/", pLimit.id()), map[string]any{"organization": nil}), 403)
	stranger, _ := newUser(t, a)
	must(t, stranger.post(fmt.Sprintf("inventories/%d/launch_policies/", f.inv), map[string]any{"id": pLimit.id()}), 403)
	must(t, orgAdmin.post(fmt.Sprintf("inventories/%d/launch_policies/", f.inv), map[string]any{"id": pLimit.id()}), 204)
	if att := must(t, a.get(fmt.Sprintf("launch_policies/%d/", pLimit.id())), 200).Body["attached"].([]any); len(att) != 1 ||
		att[0].(map[string]any)["type"] != "inventory" {
		t.Errorf("attached: %v", att)
	}
	verbName := uniq("no-debug")
	pVerb := must(t, a.post("launch_policies/", map[string]any{"name": verbName, "organization": f.org, "condition": "launch.verbosity >= 3"}), 201)
	if pVerb.Body["mode"] != "shadow" {
		t.Fatalf("default mode: %v", pVerb.Body)
	}
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch_policies/", jt), map[string]any{"id": pVerb.id()}), 204)

	// A refused API launch: a 400 that says why, and no job.
	r := a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{})
	refusedBy(t, r, limitName)
	if d := fmt.Sprint(r.Body["detail"]); !strings.HasSuffix(d, "Give a limit: this inventory is large.") {
		t.Errorf("detail: %v", r.Body)
	}
	if n := must(t, a.get(fmt.Sprintf("job_templates/%d/jobs/", jt)), 200).count(); n != 0 {
		t.Fatalf("a refused launch made %d jobs", n)
	}
	if d := must(t, a.get(fmt.Sprintf("launch_policies/%d/decisions/", pLimit.id())), 200).results(); len(d) != 1 || d[0]["outcome"] != "refused" || d[0]["job"] != nil {
		t.Errorf("decisions: %v", d)
	}

	// The check answers the same, without launching.
	if c := must(t, a.post(fmt.Sprintf("job_templates/%d/launch_check/", jt), map[string]any{}), 200).Body; c["refused"] != true {
		t.Errorf("check: %v", c)
	}
	if c := must(t, a.post(fmt.Sprintf("job_templates/%d/launch_check/", jt), map[string]any{"limit": "h1", "verbosity": 3}), 200).Body; c["refused"] != false || len(c["launch_policies"].([]any)) != 2 {
		t.Errorf("check: %v", c)
	}
	if n := must(t, a.get(fmt.Sprintf("launch_policies/%d/decisions/", pLimit.id())), 200).count(); n != 1 {
		t.Errorf("the check recorded decisions: %d", n)
	}

	// Allowed, and recorded in shadow mode.
	j1 := f.launch(t, a, jt, map[string]any{"limit": "h1", "verbosity": 3})
	mustStatus(t, f, j1, "successful")
	got := policyOutcomes(t, a, fmt.Sprintf("jobs/%d/launch_policy_decisions/", j1.id()))
	wantPolicyOutcome(t, got, limitName, "allowed")
	wantPolicyOutcome(t, got, verbName, "would_refuse")
	// Who sees the job sees its decisions, not the policies.
	viewer, viewerID := newUser(t, a)
	grant(t, a, "JobTemplate Read", viewerID, jt)
	if n := must(t, viewer.get(fmt.Sprintf("jobs/%d/launch_policy_decisions/", j1.id())), 200).count(); n != 2 {
		t.Errorf("the job's viewer sees %d decisions", n)
	}
	if n := must(t, viewer.get("launch_policies/"), 200).count(); n != 0 {
		t.Errorf("the job's viewer sees %d policies", n)
	}

	// A relaunch is checked: in enforce mode, the verbosity refuses it.
	must(t, a.patch(fmt.Sprintf("launch_policies/%d/", pVerb.id()), map[string]any{"mode": "enforce"}), 200)
	refusedBy(t, a.post(fmt.Sprintf("jobs/%d/relaunch/", j1.id()), map[string]any{}), verbName)

	// A schedule has no caller: its job fails at once, names the policy and notifies.
	s := must(t, a.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": uniq("nightly"),
		"rrule": "DTSTART:20200101T000000Z RRULE:FREQ=HOURLY;INTERVAL=1"}), 201)
	app.DB.Exec(ctx, "UPDATE schedules SET next_run = now() - interval '1 second' WHERE id = $1", s.id())
	resources.ScheduleTick(ctx, jobManager)
	sj := must(t, a.get(fmt.Sprintf("schedules/%d/jobs/", s.id())), 200).results()
	if len(sj) != 1 {
		t.Fatalf("scheduled jobs: %v", sj)
	}
	sjID := int64(sj[0]["id"].(float64))
	job := waitJob(t, a, fmt.Sprintf("jobs/%d/", sjID))
	if job.Body["status"] != "failed" || !strings.Contains(fmt.Sprint(job.Body["job_explanation"]), fmt.Sprintf("Refused by launch policy %q: Give a limit", limitName)) {
		t.Fatalf("refused scheduled job: %v %v", job.Body["status"], job.Body["job_explanation"])
	}
	if job.Body["started"] != nil {
		t.Errorf("the refused job started: %v", job.Body["started"])
	}
	wantPolicyOutcome(t, policyOutcomes(t, a, fmt.Sprintf("jobs/%d/launch_policy_decisions/", sjID)), limitName, "refused")
	if reqs := rec.wait(t, 1); !strings.Contains(fmt.Sprint(reqs[0].Body), fmt.Sprint(sjID)) {
		t.Errorf("notification: %v", reqs[0].Body)
	}

	// A webhook: the same.
	gen := f.template(t, "site.yml", map[string]any{"webhook_service": "generic"})
	key := a.get(fmt.Sprintf("job_templates/%d/webhook_key/", gen)).Body["webhook_key"].(string)
	code, body, _ := postRaw(t, fmt.Sprintf("job_templates/%d/webhook/", gen), []byte(`{}`), map[string]string{"Authorization": "Bearer " + key})
	if code != 202 {
		t.Fatalf("webhook: %d %v", code, body)
	}
	wj := waitJob(t, a, fmt.Sprintf("jobs/%d/", int64(body["job"].(float64))))
	if wj.Body["status"] != "failed" || !strings.Contains(fmt.Sprint(wj.Body["job_explanation"]), limitName) {
		t.Errorf("webhook job: %v %v", wj.Body["status"], wj.Body["job_explanation"])
	}

	// A workflow is let through; its node's job is refused, without a caller.
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": jt}), 201)
	wr := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), map[string]any{}), 201)
	wfJob := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wr.id()))
	nodes := must(t, a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", wr.id())), 200).results()
	if wfJob.Body["status"] != "failed" || len(nodes) != 1 || nodes[0]["job"] == nil {
		t.Fatalf("workflow: %v %v", wfJob.Body["status"], nodes)
	}
	nodeJob := int64(nodes[0]["job"].(float64))
	nj := a.get(fmt.Sprintf("jobs/%d/", nodeJob))
	if nj.Body["status"] != "failed" || nj.Body["launch_type"] != "workflow" || !strings.Contains(fmt.Sprint(nj.Body["job_explanation"]), limitName) {
		t.Errorf("node job: %v %v %v", nj.Body["status"], nj.Body["launch_type"], nj.Body["job_explanation"])
	}

	// Ad hoc commands, by an organization's policy.
	shellName := uniq("no-shell")
	pShell := must(t, a.post("launch_policies/", map[string]any{"name": shellName, "organization": f.org, "mode": "enforce",
		"condition": `launch.type == "ad_hoc_command" && launch.module.name == "shell"`}), 201)
	must(t, a.post(fmt.Sprintf("organizations/%d/launch_policies/", f.org), map[string]any{"id": pShell.id()}), 204)
	refusedBy(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "shell", "module_args": "echo hi", "limit": "h1"}), shellName)
	ah := must(t, a.post("ad_hoc_commands/", map[string]any{"inventory": f.inv, "module_name": "command", "module_args": "echo hi", "limit": "h1"}), 201)
	got = policyOutcomes(t, a, fmt.Sprintf("ad_hoc_commands/%d/launch_policy_decisions/", ah.id()))
	wantPolicyOutcome(t, got, shellName, "allowed")
	wantPolicyOutcome(t, got, limitName, "allowed")
	waitJob(t, a, fmt.Sprintf("ad_hoc_commands/%d/", ah.id()))

	// The portal: its preview lists the policies, and a request is refused.
	item := must(t, a.post("catalog_items/", map[string]any{"name": uniq("vm"), "unified_job_template": jt}), 201).id()
	pv := must(t, a.post(fmt.Sprintf("catalog_items/%d/preview/", item), map[string]any{}), 200).Body
	if !slices.ContainsFunc(pv["launch_policies"].([]any), func(d any) bool {
		m := d.(map[string]any)
		return m["name"] == limitName && m["outcome"] == "refused"
	}) {
		t.Errorf("preview: %v", pv["launch_policies"])
	}
	refusedBy(t, a.post(fmt.Sprintf("catalog_items/%d/request/", item), map[string]any{}), limitName)

	// A global policy checks every launch, and isn't attached.
	jt2 := f.template(t, "site.yml", map[string]any{"name": uniq("retired")})
	globalName := uniq("retired-templates")
	pGlobal := must(t, a.post("launch_policies/", map[string]any{"name": globalName, "mode": "enforce", "message": "It is retired.",
		"condition": fmt.Sprintf("launch.template.id == %d", jt2)}), 201)
	t.Cleanup(func() { a.delete(fmt.Sprintf("launch_policies/%d/", pGlobal.id())) })
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch_policies/", jt2), map[string]any{"id": pGlobal.id()}), 400)
	refusedBy(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt2), map[string]any{}), globalName, limitName)

	// A condition that fails refuses in enforce mode, and its error is recorded.
	inv2 := must(t, a.post("inventories/", map[string]any{"name": uniq("small"), "organization": f.org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"}), 201).id()
	must(t, a.post("hosts/", map[string]any{"name": "h1", "inventory": inv2}), 201)
	jt3 := must(t, a.post("job_templates/", map[string]any{"name": uniq("small"), "project": f.project, "inventory": inv2, "playbook": "site.yml"}), 201).id()
	errName := uniq("broken")
	pErr := must(t, a.post("launch_policies/", map[string]any{"name": errName, "organization": f.org, "mode": "enforce",
		"condition": `launch.extra_vars.missing == "x"`}), 201)
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch_policies/", jt3), map[string]any{"id": pErr.id()}), 204)
	r = a.post(fmt.Sprintf("job_templates/%d/launch/", jt3), map[string]any{})
	refusedBy(t, r, errName)
	if !strings.Contains(fmt.Sprint(r.Body["detail"]), "its condition failed") {
		t.Errorf("error refusal: %v", r.Body)
	}
	must(t, a.patch(fmt.Sprintf("launch_policies/%d/", pErr.id()), map[string]any{"mode": "shadow"}), 200)
	j3 := f.launch(t, a, jt3, nil)
	if d := policyOutcomes(t, a, fmt.Sprintf("jobs/%d/launch_policy_decisions/", j3.id()))[errName]; d == nil || d["outcome"] != "would_refuse" || d["error"] == "" {
		t.Errorf("shadow error: %v", d)
	}

	// Project updates aren't checked.
	allName := uniq("everything")
	pAll := must(t, a.post("launch_policies/", map[string]any{"name": allName, "organization": f.org, "mode": "enforce", "condition": "true"}), 201)
	must(t, a.post(fmt.Sprintf("organizations/%d/launch_policies/", f.org), map[string]any{"id": pAll.id()}), 204)
	must(t, a.post(fmt.Sprintf("projects/%d/update/", f.project), nil), 202)
	refusedBy(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt3), map[string]any{}), allName)
	must(t, a.delete(fmt.Sprintf("launch_policies/%d/", pAll.id())), 204)

	// Backtests: the saved condition over the launches it checks, and a draft's.
	bt := must(t, a.post(fmt.Sprintf("launch_policies/%d/backtest/", pLimit.id()), map[string]any{}), 200).Body
	listed := map[int64]bool{}
	for _, l := range bt["launches"].([]any) {
		listed[int64(l.(map[string]any)["id"].(float64))] = true
	}
	if !listed[sjID] || !listed[nodeJob] || listed[j1.id()] || listed[j3.id()] || bt["refused"] != float64(len(listed)) {
		t.Errorf("backtest: %v", bt)
	}
	draft := must(t, a.post("launch_policies/backtest/", map[string]any{"condition": "launch.verbosity >= 3", "organization": f.org, "days": 1}), 200).Body
	if l := draft["launches"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != float64(j1.id()) {
		t.Errorf("draft backtest: %v", draft)
	}
	must(t, a.post("launch_policies/backtest/", map[string]any{"condition": "launch.limt"}), 400)
	must(t, a.post(fmt.Sprintf("launch_policies/%d/backtest/", pLimit.id()), map[string]any{"days": 0}), 400)
	if n := must(t, stranger.post("launch_policies/backtest/", map[string]any{"condition": "true"}), 200).Body["examined"]; n != float64(0) {
		t.Errorf("a user without rights backtests %v launches", n)
	}

	// Deleting a policy keeps its decisions; they are cleaned up with event rules'.
	must(t, a.delete(fmt.Sprintf("launch_policies/%d/", pVerb.id())), 204)
	if d := policyOutcomes(t, a, fmt.Sprintf("jobs/%d/launch_policy_decisions/", j1.id()))[verbName]; d == nil || d["launch_policy"] != nil {
		t.Errorf("a deleted policy's decision: %v", d)
	}
	app.DB.Exec(ctx, `INSERT INTO launch_policy_decisions (created, policy_name, mode, outcome, job_type) VALUES (now() - interval '100 days', 'old', 'enforce', 'refused', 'job')`)
	var sjt int64
	app.DB.QueryRow(ctx, `SELECT id FROM unified_job_templates WHERE type = 'system_job_template' AND job_type = 'cleanup_event_streams'`).Scan(&sjt)
	cj := must(t, a.post(fmt.Sprintf("system_job_templates/%d/launch/", sjt), map[string]any{}), 201)
	if r := waitJob(t, a, fmt.Sprintf("system_jobs/%d/", cj.id())); r.Body["status"] != "successful" {
		t.Fatalf("cleanup: %v", r.Body)
	}
	var old, kept int
	app.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE policy_name = 'old'), count(*) FILTER (WHERE unified_job_id = $1) FROM launch_policy_decisions`, j1.id()).Scan(&old, &kept)
	if old != 0 || kept != 2 {
		t.Errorf("after cleanup: %d old, %d of the job", old, kept)
	}
}
