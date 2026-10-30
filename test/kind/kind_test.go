// Package kind tests the Helm chart installed in a kind cluster (CI's helm
// job): AWXNEXT_TEST_KIND_URL is the installed awxnext, AWXNEXT_TEST_KIND_PASSWORD
// its admin password.
package kind

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type api struct {
	t    *testing.T
	base string
	pass string
}

func (a *api) call(method, path string, body any) (int, map[string]any, string) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.base+"/api/v2/"+path, rd)
	req.SetBasicAuth("admin", a.pass)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err.Error()
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return res.StatusCode, m, string(raw)
}

func (a *api) create(path string, body any) int64 {
	a.t.Helper()
	status, m, raw := a.call("POST", path, body)
	if status != 201 {
		a.t.Fatalf("POST %s: %d %s", path, status, raw)
	}
	return int64(m["id"].(float64))
}

// wait polls a unified job until it finishes.
func (a *api) wait(path string, timeout time.Duration) map[string]any {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, m, _ := a.call("GET", path, nil)
		switch m["status"] {
		case "successful", "failed", "error", "canceled":
			return m
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("%s did not finish: %v", path, m["status"])
		}
		time.Sleep(time.Second)
	}
}

// The chart's install runs a job in a container group: a pod per job, in
// the release's namespace, created with the control plane's service account.
func TestContainerGroupJob(t *testing.T) {
	base := os.Getenv("AWXNEXT_TEST_KIND_URL")
	if base == "" {
		t.Skip("AWXNEXT_TEST_KIND_URL is not set (CI's helm job)")
	}
	a := &api{t: t, base: strings.TrimRight(base, "/"), pass: os.Getenv("AWXNEXT_TEST_KIND_PASSWORD")}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		if status, _, _ := a.call("GET", "ping/", nil); status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("awxnext is not answering")
		}
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	org := a.create("organizations/", map[string]any{"name": "kind-" + suffix})
	inv := a.create("inventories/", map[string]any{"name": "kind-" + suffix, "organization": org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"})
	a.create("hosts/", map[string]any{"name": "localhost", "inventory": inv})
	project := a.create("projects/", map[string]any{"name": "kind-" + suffix, "organization": org, "scm_type": "git",
		"scm_url": "https://github.com/ansible/ansible-tower-samples"})
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(2 * time.Second) {
		_, p, _ := a.call("GET", fmt.Sprintf("projects/%d/", project), nil)
		if p["status"] == "successful" {
			break
		}
		if p["status"] == "failed" || p["status"] == "error" || time.Now().After(deadline) {
			t.Fatalf("project sync: %v", p["status"])
		}
	}
	group := a.create("instance_groups/", map[string]any{"name": "pods-" + suffix, "is_container_group": true})
	jt := a.create("job_templates/", map[string]any{"name": "kind-" + suffix, "inventory": inv, "project": project, "playbook": "hello_world.yml"})
	if status, _, raw := a.call("POST", fmt.Sprintf("job_templates/%d/instance_groups/", jt), map[string]any{"id": group}); status != 204 {
		t.Fatalf("container group on the template: %d %s", status, raw)
	}
	id := a.create(fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{})
	job := a.wait(fmt.Sprintf("jobs/%d/", id), 10*time.Minute)
	_, _, stdout := a.call("GET", fmt.Sprintf("jobs/%d/stdout/?format=txt", id), nil)
	if job["status"] != "successful" || !strings.Contains(stdout, "Hello World!") {
		t.Fatalf("container group job: %v %v\n%s", job["status"], job["job_explanation"], stdout)
	}
	if job["instance_group"] != float64(group) || job["execution_node"] != "" {
		t.Errorf("not run in the container group: group %v, node %v", job["instance_group"], job["execution_node"])
	}
}

// A backup of the release restored into another (docs/operations.md, Backup
// and restore): the templates of the backed up install launch, their
// projects checked out again by the restore's project updates.
func TestRestoredInstall(t *testing.T) {
	base := os.Getenv("AWXNEXT_TEST_KIND_RESTORED_URL")
	if base == "" {
		t.Skip("AWXNEXT_TEST_KIND_RESTORED_URL is not set (CI's helm job)")
	}
	a := &api{t: t, base: strings.TrimRight(base, "/"), pass: os.Getenv("AWXNEXT_TEST_KIND_PASSWORD")}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		if status, _, _ := a.call("GET", "ping/", nil); status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("awxnext is not answering")
		}
	}
	_, list, raw := a.call("GET", "job_templates/?name__startswith=kind-&order_by=id", nil)
	results, _ := list["results"].([]any)
	if len(results) == 0 {
		t.Fatalf("no template of the backed up install: %s", raw)
	}
	jt := results[0].(map[string]any)
	// The project keeps its status from the backup, which has its first
	// update only: wait for the second, the one the restore queued.
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(2 * time.Second) {
		_, u, _ := a.call("GET", fmt.Sprintf("project_updates/?project=%v&order_by=-id&page_size=1", jt["project"]), nil)
		latest := map[string]any{}
		if r, _ := u["results"].([]any); len(r) > 0 {
			latest = r[0].(map[string]any)
		}
		if count, _ := u["count"].(float64); count >= 2 && latest["status"] == "successful" {
			break
		}
		if latest["status"] == "failed" || latest["status"] == "error" || time.Now().After(deadline) {
			t.Fatalf("project update after the restore: %v", latest)
		}
	}
	id := a.create(fmt.Sprintf("job_templates/%v/launch/", jt["id"]), map[string]any{})
	job := a.wait(fmt.Sprintf("jobs/%d/", id), 10*time.Minute)
	_, _, stdout := a.call("GET", fmt.Sprintf("jobs/%d/stdout/?format=txt", id), nil)
	if job["status"] != "successful" || !strings.Contains(stdout, "Hello World!") {
		t.Fatalf("job of a restored template: %v %v\n%s", job["status"], job["job_explanation"], stdout)
	}
}
