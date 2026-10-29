package test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/resources"
)

// Labels and instance groups saved on schedules and workflow nodes: attached
// by whoever may see or use them, and given to the jobs launched when their
// template prompts for them (as AWX, which ignores them otherwise).
func TestSavedLabelsAndInstanceGroups(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	label := func() int64 {
		return must(t, a.post("labels/", map[string]any{"name": uniq("l"), "organization": f.org}), 201).id()
	}
	tplLabel, nodeLabel, schedLabel, wfLabel := label(), label(), label(), label()
	ig1 := must(t, a.post("instance_groups/", map[string]any{"name": uniq("ig")}), 201).id()
	ig2 := must(t, a.post("instance_groups/", map[string]any{"name": uniq("ig")}), 201).id()
	ids := func(path string) string {
		t.Helper()
		var out []int64
		for _, o := range must(t, a.get(path), 200).results() {
			out = append(out, int64(o["id"].(float64)))
		}
		return fmt.Sprint(out)
	}
	jobGroups := func(job int64) string {
		t.Helper()
		var out []int64
		rows, err := app.DB.Query(t.Context(), "SELECT instance_group_id FROM instance_group_links WHERE content_type = 'unifiedjob' AND object_id = $1 ORDER BY position", job)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			out = append(out, id)
		}
		return fmt.Sprint(out)
	}
	attach := func(path string, id int64, status int) resp {
		t.Helper()
		return must(t, a.post(path, map[string]any{"id": id}), status)
	}
	fire := func(sched int64) {
		t.Helper()
		app.DB.Exec(context.Background(), "UPDATE schedules SET next_run = now() - interval '1 second' WHERE id = $1", sched)
		resources.ScheduleTick(context.Background(), jobManager)
	}
	rule := "DTSTART:20200101T000000Z RRULE:FREQ=HOURLY;INTERVAL=1"

	jt := f.template(t, "site.yml", map[string]any{"ask_labels_on_launch": true, "ask_instance_groups_on_launch": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/labels/", jt), map[string]any{"id": tplLabel}), 204)
	plain := f.template(t, "site.yml", nil)

	// A job template's schedule: labels and ordered instance groups.
	s := must(t, a.post(fmt.Sprintf("job_templates/%d/schedules/", jt), map[string]any{"name": uniq("s"), "rrule": rule}), 201).id()
	attach(fmt.Sprintf("schedules/%d/labels/", s), schedLabel, 204)
	attach(fmt.Sprintf("schedules/%d/instance_groups/", s), ig2, 204)
	attach(fmt.Sprintf("schedules/%d/instance_groups/", s), ig1, 204)
	if got := ids(fmt.Sprintf("schedules/%d/instance_groups/", s)); got != fmt.Sprint([]int64{ig2, ig1}) {
		t.Fatalf("schedule instance groups: %s", got)
	}
	// On a template that does not prompt for them, they are saved but not used.
	ps := must(t, a.post(fmt.Sprintf("job_templates/%d/schedules/", plain), map[string]any{"name": uniq("s"), "rrule": rule}), 201).id()
	attach(fmt.Sprintf("schedules/%d/labels/", ps), schedLabel, 204)
	attach(fmt.Sprintf("schedules/%d/instance_groups/", ps), ig1, 204)
	fire(ps)
	pjobs := must(t, a.get(fmt.Sprintf("schedules/%d/jobs/", ps)), 200).results()
	if len(pjobs) != 1 {
		t.Fatalf("plain scheduled jobs: %v", pjobs)
	}
	if pj := int64(pjobs[0]["id"].(float64)); ids(fmt.Sprintf("jobs/%d/labels/", pj)) != "[]" || jobGroups(pj) != "[]" {
		t.Fatalf("unprompted labels or instance groups used: %s %s", ids(fmt.Sprintf("jobs/%d/labels/", pj)), jobGroups(pj))
	}
	fire(s)
	jobs := must(t, a.get(fmt.Sprintf("schedules/%d/jobs/", s)), 200).results()
	if len(jobs) != 1 {
		t.Fatalf("scheduled jobs: %v", jobs)
	}
	job := int64(jobs[0]["id"].(float64))
	if got := ids(fmt.Sprintf("jobs/%d/labels/?order_by=id", job)); got != fmt.Sprint([]int64{tplLabel, schedLabel}) {
		t.Fatalf("scheduled job labels: %s", got)
	}
	if got := jobGroups(job); got != fmt.Sprint([]int64{ig2, ig1}) {
		t.Fatalf("scheduled job instance groups: %s", got)
	}
	must(t, a.post(fmt.Sprintf("schedules/%d/instance_groups/", s), map[string]any{"id": ig2, "disassociate": true}), 204)
	if got := ids(fmt.Sprintf("schedules/%d/instance_groups/", s)); got != fmt.Sprint([]int64{ig1}) {
		t.Fatalf("after detach: %s", got)
	}

	// A job's create_schedule saves the labels and instance groups it was launched with.
	launched := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"labels": []int64{nodeLabel}, "instance_groups": []int64{ig2, ig1}}), 201).id()
	cs := must(t, a.get(fmt.Sprintf("jobs/%d/create_schedule/", launched)), 200).Body["prompts"].(map[string]any)
	if fmt.Sprint(cs["labels"]) == "<nil>" || len(cs["instance_groups"].([]any)) != 2 {
		t.Fatalf("create_schedule prompts: %v", cs)
	}
	auto := must(t, a.post(fmt.Sprintf("jobs/%d/create_schedule/", launched), nil), 201).id()
	if ids(fmt.Sprintf("schedules/%d/labels/", auto)) != fmt.Sprint([]int64{nodeLabel}) || ids(fmt.Sprintf("schedules/%d/instance_groups/", auto)) != fmt.Sprint([]int64{ig2, ig1}) {
		t.Fatalf("auto schedule: %s %s", ids(fmt.Sprintf("schedules/%d/labels/", auto)), ids(fmt.Sprintf("schedules/%d/instance_groups/", auto)))
	}

	// A workflow: its node saves labels and instance groups; its schedule
	// saves labels (workflows do not prompt for instance groups).
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org, "ask_labels_on_launch": true}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/labels/", wf), map[string]any{"id": wfLabel}), 204)
	node := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": jt, "identifier": "deploy"}), 201).id()
	attach(fmt.Sprintf("workflow_job_template_nodes/%d/labels/", node), nodeLabel, 204)
	attach(fmt.Sprintf("workflow_job_template_nodes/%d/instance_groups/", node), ig1, 204)
	ws := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/schedules/", wf), map[string]any{"name": uniq("s"), "rrule": rule}), 201).id()
	attach(fmt.Sprintf("schedules/%d/labels/", ws), schedLabel, 204)

	// Using an instance group takes use on it.
	u, uid := newUser(t, a)
	must(t, a.post(fmt.Sprintf("organizations/%d/users/", f.org), map[string]any{"id": uid}), 204)
	for _, r := range []struct {
		res  string
		id   int64
		role string
	}{{"workflow_job_templates", wf, "admin_role"}, {"job_templates", jt, "execute_role"}} {
		must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, r.res, r.id, r.role)}), 204)
	}
	must(t, u.post(fmt.Sprintf("workflow_job_template_nodes/%d/instance_groups/", node), map[string]any{"id": ig2}), 403)
	must(t, a.post(fmt.Sprintf("users/%d/roles/", uid), map[string]any{"id": objectRole(t, a, "instance_groups", ig2, "use_role")}), 204)
	must(t, u.post(fmt.Sprintf("workflow_job_template_nodes/%d/instance_groups/", node), map[string]any{"id": ig2}), 204)
	must(t, u.post(fmt.Sprintf("workflow_job_template_nodes/%d/instance_groups/", node), map[string]any{"id": ig2, "disassociate": true}), 204)

	// The scheduled workflow runs its node with the saved labels and instance
	// groups; the workflow's labels reach the node's job, whose template prompts for labels.
	fire(ws)
	wjobs := must(t, a.get(fmt.Sprintf("schedules/%d/jobs/", ws)), 200).results()
	if len(wjobs) != 1 {
		t.Fatalf("scheduled workflow jobs: %v", wjobs)
	}
	wj := int64(wjobs[0]["id"].(float64))
	if got := ids(fmt.Sprintf("workflow_jobs/%d/labels/?order_by=id", wj)); got != fmt.Sprint([]int64{schedLabel, wfLabel}) {
		t.Fatalf("workflow job labels: %s", got)
	}
	nodeJob := func(wj int64) (map[string]any, int64) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			for _, n := range a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", wj)).results() {
				if id, ok := n["job"].(float64); ok {
					return n, int64(id)
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("the node never got a job")
		return nil, 0
	}
	jn, nj := nodeJob(wj)
	if got := ids(fmt.Sprintf("jobs/%d/labels/?order_by=id", nj)); got != fmt.Sprint([]int64{tplLabel, nodeLabel, schedLabel, wfLabel}) {
		t.Fatalf("node job labels: %s", got)
	}
	if got := jobGroups(nj); got != fmt.Sprint([]int64{ig1}) {
		t.Fatalf("node job instance groups: %s", got)
	}
	jnID := int64(jn["id"].(float64))
	if ids(fmt.Sprintf("workflow_job_nodes/%d/labels/", jnID)) != fmt.Sprint([]int64{nodeLabel}) || ids(fmt.Sprintf("workflow_job_nodes/%d/instance_groups/", jnID)) != fmt.Sprint([]int64{ig1}) {
		t.Fatalf("workflow job node prompts: %s %s", ids(fmt.Sprintf("workflow_job_nodes/%d/labels/", jnID)), ids(fmt.Sprintf("workflow_job_nodes/%d/instance_groups/", jnID)))
	}

	// A relaunch and a copy keep the nodes' instance groups. (The relaunch
	// waits for the first run, whose job has no instance to run on.)
	rewj := must(t, a.post(fmt.Sprintf("workflow_jobs/%d/relaunch/", wj), map[string]any{}), 201).id()
	rn := a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", rewj)).results()
	if len(rn) != 1 || ids(fmt.Sprintf("workflow_job_nodes/%v/instance_groups/", rn[0]["id"])) != fmt.Sprint([]int64{ig1}) ||
		ids(fmt.Sprintf("workflow_job_nodes/%v/labels/", rn[0]["id"])) != fmt.Sprint([]int64{nodeLabel}) {
		t.Fatalf("relaunched nodes: %v", rn)
	}
	cp := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/copy/", wf), map[string]any{"name": uniq("copy")}), 201).id()
	cn := a.get(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", cp)).results()
	if len(cn) != 1 || ids(fmt.Sprintf("workflow_job_template_nodes/%v/instance_groups/", cn[0]["id"])) != fmt.Sprint([]int64{ig1}) {
		t.Fatalf("copied nodes: %v", cn)
	}

	// Bulk jobs take instance groups too, under the same rules.
	r := must(t, a.post("bulk/job_launch/", map[string]any{"name": "bulk", "jobs": []any{
		map[string]any{"unified_job_template": jt, "instance_groups": []any{ig2, ig1}, "labels": []any{nodeLabel}},
	}}), 201)
	bn := must(t, a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", r.id())), 200).results()
	if len(bn) != 1 || ids(fmt.Sprintf("workflow_job_nodes/%v/instance_groups/", bn[0]["id"])) != fmt.Sprint([]int64{ig2, ig1}) {
		t.Fatalf("bulk node: %v", bn)
	}
	r = must(t, a.post("bulk/job_launch/", map[string]any{"name": "bulk", "jobs": []any{
		map[string]any{"unified_job_template": plain, "instance_groups": []any{ig1}},
	}}), 400)
	if !strings.Contains(r.Raw, "Field is not configured to prompt on launch.") {
		t.Fatalf("bulk on a template that does not prompt: %s", r.Raw)
	}
}

// As in AWX (DRF's related fields), "" clears a reference: the awx.awx
// modules send it to reset one.
func TestEmptyStringClearsReference(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	ee := must(t, a.post("execution_environments/", map[string]any{"name": uniq("ee"), "image": "quay.io/x/y:1"}), 201).id()
	jt := f.template(t, "site.yml", map[string]any{"execution_environment": ee})
	if r := must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"execution_environment": ""}), 200); r.Body["execution_environment"] != nil {
		t.Fatalf("execution_environment: %v", r.Body["execution_environment"])
	}
	// A required reference still cannot be emptied.
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"project": ""}), 400)
}

// A credential saved on a workflow node goes to its job whatever its kind,
// as in AWX: only credentials given at launch are refused by kind.
func TestNodeCredentialOfAnyKind(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	scm := must(t, a.post("credentials/", map[string]any{"name": uniq("scm"), "organization": f.org, "credential_type": credType(t, a, "scm"),
		"inputs": map[string]any{"username": "git"}}), 201).id()
	jt := f.template(t, "site.yml", map[string]any{"ask_credential_on_launch": true})
	must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{"credentials": []int64{scm}}), 400)
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org}), 201).id()
	node := must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": jt}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_template_nodes/%d/credentials/", node), map[string]any{"id": scm}), 204)
	wj := int64(must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/launch/", wf), nil), 201).Body["workflow_job"].(float64))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ns := a.get(fmt.Sprintf("workflow_jobs/%d/workflow_nodes/", wj)).results()
		if len(ns) == 1 && ns[0]["job"] != nil {
			job := a.get(fmt.Sprintf("jobs/%v/", ns[0]["job"])).Body
			if strings.Contains(fmt.Sprint(job["job_explanation"]), "Cannot assign") {
				t.Fatalf("node job refused: %v", job["job_explanation"])
			}
			creds := a.get(fmt.Sprintf("jobs/%v/credentials/", ns[0]["job"])).results()
			if len(creds) != 1 || creds[0]["id"] != float64(scm) {
				t.Fatalf("node job credentials: %v", creds)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the node never got a job")
}
