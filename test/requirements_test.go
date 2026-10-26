package test

import (
	"fmt"
	"strings"
	"testing"
)

func TestProjectRequirements(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	role := gitRepo(t, map[string]string{
		"meta/main.yml":  "galaxy_info:\n  author: test\n  description: test role\n  license: MIT\n  min_ansible_version: '2.9'\n  platforms: []\ndependencies: []\n",
		"tasks/main.yml": "- ansible.builtin.debug:\n    msg: from-git-role\n",
	})
	coll := gitRepo(t, map[string]string{
		"galaxy.yml":                 "namespace: acme\nname: tools\nversion: 1.0.0\nreadme: README.md\nauthors: [test]\n",
		"README.md":                  "# acme.tools\n",
		"meta/runtime.yml":           "requires_ansible: '>=2.9'\n",
		"roles/hello/tasks/main.yml": "- ansible.builtin.debug:\n    msg: from-collection-role\n",
	})
	repo := gitRepo(t, map[string]string{
		"roles/requirements.yml":       fmt.Sprintf("- src: git+file://%s\n  name: gitrole\n", role),
		"collections/requirements.yml": fmt.Sprintf("collections:\n  - name: git+file://%s\n    type: git\n", coll),
		"site.yml": "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.include_role:\n        name: gitrole\n" +
			"    - ansible.builtin.include_role:\n        name: acme.tools.hello\n",
	})
	proj := must(t, a.post("projects/", map[string]any{"name": uniq("reqs"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	f.project = proj
	f.waitProject(t)
	jt := f.template(t, "site.yml", map[string]any{"limit": "h1"})
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	out := f.stdout(t, job.id())
	for _, want := range []string{"Installing project requirements", "from-git-role", "from-collection-role"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in stdout:\n%s", want, out)
		}
	}
	// Event counters stay unique across the requirements run and the playbook.
	ev := a.get(fmt.Sprintf("jobs/%d/job_events/?page_size=200&order_by=counter", job.id())).results()
	seen := map[float64]bool{}
	for _, e := range ev {
		c := e["counter"].(float64)
		if seen[c] {
			t.Fatalf("duplicate event counter %v", c)
		}
		seen[c] = true
	}

	// The second run reuses the cached requirements.
	job = f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	out = f.stdout(t, job.id())
	if !strings.Contains(out, "Using cached project requirements") || !strings.Contains(out, "from-collection-role") {
		t.Fatalf("cache not used:\n%s", out)
	}
}
