package test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// outputsPlaybook publishes the publish variable with set_stats.
const outputsPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.set_stats:
        data: "{{ publish | default({}) }}"
      run_once: true
`

var regionsOutputs = []any{
	map[string]any{"name": "regions", "type": "list", "required": true},
	map[string]any{"name": "replicas", "type": "integer"},
}

func newOutputsFixture(t *testing.T) *wfFixture {
	t.Helper()
	f := newJobFixture(t)
	repo := gitRepo(t, map[string]string{"outputs.yml": outputsPlaybook, "item.yml": itemPlaybook})
	f.project = must(t, f.a.post("projects/", map[string]any{"name": uniq("outproj"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	f.waitProject(t)
	return &wfFixture{jobFixture: f}
}

func TestTemplateOutputs(t *testing.T) {
	f := newLaunchFixture(t)
	a := f.a
	plain := f.template(t, "site.yml", nil)
	if r := a.get(fmt.Sprintf("job_templates/%d/", plain)); fmt.Sprint(r.Body["outputs"]) != "[]" {
		t.Fatalf("default outputs: %v", r.Body["outputs"])
	}
	jt := f.template(t, "site.yml", map[string]any{"outputs": regionsOutputs})
	r := a.get(fmt.Sprintf("job_templates/%d/", jt))
	if got := fmt.Sprint(r.Body["outputs"]); got != "[map[name:regions required:true type:list] map[name:replicas required:false type:integer]]" {
		t.Fatalf("outputs: %s", got)
	}
	for body, want := range map[string]string{
		`{"outputs": {"regions": "list"}}`:                                              "Expected a list of outputs.",
		`{"outputs": [{"name": "regions", "type": "array"}]}`:                           `Output "regions": type must be one of string, integer, float, boolean, list, object.`,
		`{"outputs": [{"name": "my-var", "type": "list"}]}`:                             `Output 1: "my-var" is not a valid variable name.`,
		`{"outputs": [{"name": "a", "type": "list"}, {"name": "a", "type": "string"}]}`: `Output "a" is declared twice.`,
	} {
		var data map[string]any
		json.Unmarshal([]byte(body), &data)
		r := must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), data), 400)
		if fmt.Sprint(r.Body["outputs"]) != "["+want+"]" {
			t.Errorf("%s: %s", body, r.Raw)
		}
	}
	// An empty form value clears them.
	if r := must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"outputs": ""}), 200); fmt.Sprint(r.Body["outputs"]) != "[]" {
		t.Fatalf("cleared outputs: %v", r.Body["outputs"])
	}
	// Copies keep them.
	a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"outputs": regionsOutputs})
	cp := must(t, a.post(fmt.Sprintf("job_templates/%d/copy/", jt), map[string]any{"name": uniq("copy")}), 201).id()
	if r := a.get(fmt.Sprintf("job_templates/%d/", cp)); len(r.Body["outputs"].([]any)) != 2 {
		t.Fatalf("copy outputs: %v", r.Body["outputs"])
	}
}

func TestJobOutputsChecked(t *testing.T) {
	w := newOutputsFixture(t)
	a := w.a
	jt := w.template(t, "outputs.yml", map[string]any{"ask_variables_on_launch": true, "outputs": regionsOutputs})
	launch := func(jt int64, publish map[string]any) resp {
		return w.jobFixture.launch(t, a, jt, map[string]any{"extra_vars": map[string]any{"publish": publish}})
	}

	// Matching outputs are converted to their types; others pass unchanged.
	j := launch(jt, map[string]any{"regions": []any{"eu", "us"}, "replicas": "3", "note": "free"})
	mustStatus(t, w.jobFixture, j, "successful")
	art := j.Body["artifacts"].(map[string]any)
	if fmt.Sprint(art["regions"]) != "[eu us]" || art["replicas"] != float64(3) || art["note"] != "free" {
		t.Fatalf("artifacts: %#v", art)
	}

	// A wrong type fails the job, in its explanation and its output.
	j = launch(jt, map[string]any{"regions": "eu", "replicas": 2})
	mustStatus(t, w.jobFixture, j, "failed")
	want := `The playbook succeeded but its outputs do not match the template: "regions" must be a list, not a string ("eu").`
	if j.Body["job_explanation"] != want {
		t.Fatalf("explanation: %v", j.Body["job_explanation"])
	}
	if out := w.stdout(t, j.id()); !strings.Contains(out, want) {
		t.Fatalf("output:\n%s", out)
	}

	// So does a missing required output.
	j = launch(jt, map[string]any{})
	mustStatus(t, w.jobFixture, j, "failed")
	if !strings.Contains(fmt.Sprint(j.Body["job_explanation"]), `"regions" is required but was not set`) {
		t.Fatalf("explanation: %v", j.Body["job_explanation"])
	}

	// Without declarations nothing is checked or converted.
	plain := w.template(t, "outputs.yml", map[string]any{"ask_variables_on_launch": true})
	j = launch(plain, map[string]any{"regions": "eu", "replicas": "3"})
	mustStatus(t, w.jobFixture, j, "successful")
	if art := j.Body["artifacts"].(map[string]any); art["regions"] != "eu" || art["replicas"] != "3" {
		t.Fatalf("artifacts: %#v", art)
	}
}

// A workflow loops over a declared list output; a wrong type fails the node
// that produced it, and the loop does not run.
func TestWorkflowTypedOutputs(t *testing.T) {
	w := newOutputsFixture(t)
	a := w.a
	discoverJT := w.template(t, "outputs.yml", map[string]any{"outputs": regionsOutputs})
	itemJT := w.template(t, "item.yml", nil)
	wf := w.wfjt(t, map[string]any{"ask_variables_on_launch": true})
	discover := w.node(t, wf, discoverJT, map[string]any{"identifier": "discover"})

	// Expressions are checked against the declared outputs when saved.
	nodes := fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf)
	r := must(t, a.post(nodes, map[string]any{"unified_job_template": itemJT, "when": "nodes.discover.artifacts.region != nil"}), 400)
	if fmt.Sprint(r.Body["when"]) != `[Step "discover" has no output "region"; its template declares regions, replicas.]` {
		t.Fatalf("when: %s", r.Raw)
	}
	r = must(t, a.post(nodes, map[string]any{"unified_job_template": itemJT, "loop": "nodes['discover']?.artifacts?.replicas"}), 400)
	if fmt.Sprint(r.Body["loop"]) != `[Output "replicas" of step "discover" is an integer; a loop needs a list.]` {
		t.Fatalf("loop: %s", r.Raw)
	}
	deploy := w.node(t, wf, itemJT, map[string]any{"identifier": "deploy", "loop": "nodes.discover.artifacts.regions", "loop_var": "region",
		"when": "nodes.discover.artifacts.replicas > 1"})
	must(t, a.patch(fmt.Sprintf("workflow_job_template_nodes/%d/", deploy), map[string]any{"when": "nodes.discover.artifacts.replica > 1"}), 400)
	// Steps whose template declares nothing, and plain variables, are not checked.
	must(t, a.patch(fmt.Sprintf("workflow_job_template_nodes/%d/", discover), map[string]any{"when": "nodes.deploy.artifacts.anything == nil and anything == nil"}), 200)
	must(t, a.patch(fmt.Sprintf("workflow_job_template_nodes/%d/", discover), map[string]any{"when": ""}), 200)
	w.link(t, discover, "success", deploy)

	// A list: one deploy per region. replicas, published as the string "2",
	// reaches the condition as a number.
	wj := w.launch(t, wf, map[string]any{"extra_vars": map[string]any{"publish": map[string]any{"regions": []any{"eu", "us"}, "replicas": "2"}}})
	if done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj)); done.Body["status"] != "successful" {
		t.Fatalf("workflow: %v (%v)", done.Body["status"], done.Body["job_explanation"])
	}
	n := w.nodeByID(t, wj)["deploy"]
	if iters, _ := n["iterations"].([]any); len(iters) != 2 || fmt.Sprint(n["loop_items"]) != "[eu us]" {
		t.Fatalf("deploy: %v", n)
	}

	// A string: discover fails with the type error and deploy never runs.
	wj = w.launch(t, wf, map[string]any{"extra_vars": map[string]any{"publish": map[string]any{"regions": "eu,us"}}})
	if done := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", wj)); done.Body["status"] != "failed" {
		t.Fatalf("workflow: %v", done.Body["status"])
	}
	byID := w.nodeByID(t, wj)
	job := a.get(fmt.Sprintf("jobs/%d/", int64(nodeJob(byID["discover"])["id"].(float64)))).Body
	if job["status"] != "failed" || !strings.Contains(fmt.Sprint(job["job_explanation"]), `"regions" must be a list, not a string ("eu,us")`) {
		t.Fatalf("discover job: %v %v", job["status"], job["job_explanation"])
	}
	if d := byID["deploy"]; d["do_not_run"] != true || d["job"] != nil {
		t.Fatalf("deploy ran: %v", d)
	}
}
