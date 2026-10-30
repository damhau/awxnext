// Package isolation tests job users on the all-in-one image (CI's compose
// job): AWXNEXT_TEST_ISOLATION_URL is an awxnext started from
// deploy/compose, AWXNEXT_TEST_ISOLATION_PASSWORD its admin password.
package isolation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

func (a *api) stdout(path string) string {
	_, _, out := a.call("GET", path+"stdout/?format=txt", nil)
	return out
}

// A job, while another runs, cannot find the other's private data (its
// extra vars, in its directory and its processes'), cannot list the jobs'
// directories, and cannot write to a project checkout nor list them. Both
// run as job users, each its own.
func TestJobsIsolated(t *testing.T) {
	base := os.Getenv("AWXNEXT_TEST_ISOLATION_URL")
	if base == "" {
		t.Skip("AWXNEXT_TEST_ISOLATION_URL is not set (CI's compose job)")
	}
	a := &api{t: t, base: strings.TrimRight(base, "/"), pass: os.Getenv("AWXNEXT_TEST_ISOLATION_PASSWORD")}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		if status, _, _ := a.call("GET", "ping/", nil); status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("awxnext is not answering")
		}
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	org := a.create("organizations/", map[string]any{"name": "isolation-" + suffix})
	inv := a.create("inventories/", map[string]any{"name": "isolation-" + suffix, "organization": org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"})
	a.create("hosts/", map[string]any{"name": "localhost", "inventory": inv})
	project := a.create("projects/", map[string]any{"name": "isolation-" + suffix, "organization": org, "scm_type": "git",
		"scm_url": "https://github.com/ansible/ansible-tower-samples"})
	var checkout string
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(2 * time.Second) {
		_, p, _ := a.call("GET", fmt.Sprintf("projects/%d/", project), nil)
		if p["status"] == "successful" {
			checkout = filepath.Join("/var/lib/awxnext/projects", fmt.Sprint(p["local_path"]))
			break
		}
		if p["status"] == "failed" || p["status"] == "error" || time.Now().After(deadline) {
			t.Fatalf("project sync: %v", p["status"])
		}
	}

	// The holder's extra vars are in its private data directory while it
	// sleeps.
	holder := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell",
		"module_args": "id -un; sleep 40", "extra_vars": fmt.Sprintf(`{"isolation_secret": "holder-secret-%s"}`, suffix)})
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		_, h, _ := a.call("GET", fmt.Sprintf("ad_hoc_commands/%d/", holder), nil)
		_, ev, _ := a.call("GET", fmt.Sprintf("ad_hoc_commands/%d/events/?event=runner_on_start", holder), nil)
		if h["status"] == "running" && ev["count"] != nil && ev["count"].(float64) > 0 {
			break
		}
		if h["status"] != "pending" && h["status"] != "waiting" && h["status"] != "running" || time.Now().After(deadline) {
			t.Fatalf("holder: %v", h["status"])
		}
	}
	// The secret is put together at run time: the prober's own command
	// holds its two halves only (and the objects' names, in every job's
	// extra vars, are isolation-<suffix>).
	script := strings.Join([]string{
		"id -un",
		"m=holder-secret-; m=${m}" + suffix,
		`echo "found: $(grep -rl "$m" /var/lib/awxnext /proc/[0-9]*/environ /proc/[0-9]*/cwd/ /tmp 2>/dev/null | head -3 | tr '\n' ' ')"`,
		`echo "list jobs: $(ls /var/lib/awxnext/jobs 2>&1 | head -1)"`,
		`echo "list work: $(ls /var/lib/awxnext/jobs/work 2>&1 | head -1)"`,
		`echo "list projects: $(ls /var/lib/awxnext/projects 2>&1 | head -1)"`,
		fmt.Sprintf(`echo "write checkout: $(touch %s/written 2>&1 && echo written)"`, checkout),
		fmt.Sprintf(`echo "read checkout: $(cat %s/hello_world.yml 2>&1 | head -1)"`, checkout),
	}, "\n")
	prober := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell", "module_args": script})
	p := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", prober), 3*time.Minute)
	out := a.stdout(fmt.Sprintf("ad_hoc_commands/%d/", prober))
	if p["status"] != "successful" {
		t.Fatalf("prober: %v\n%s", p["status"], out)
	}
	line := func(prefix string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(l, prefix))
			}
		}
		t.Fatalf("no %q in:\n%s", prefix, out)
		return ""
	}
	if found := line("found:"); found != "" {
		t.Errorf("the prober found the holder's extra vars: %s", found)
	}
	for _, l := range []string{"list jobs:", "list work:", "list projects:", "write checkout:", "read checkout:"} {
		if got := line(l); !strings.Contains(got, "Permission denied") {
			t.Errorf("%s %s (want Permission denied)", l, got)
		}
	}
	h := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", holder), 3*time.Minute)
	hout := a.stdout(fmt.Sprintf("ad_hoc_commands/%d/", holder))
	if h["status"] != "successful" {
		t.Fatalf("holder: %v\n%s", h["status"], hout)
	}
	user := regexp.MustCompile(`(?m)^(awxjob\d+)\s*$`)
	jobUser := func(s string) string {
		if m := user.FindStringSubmatch(s); m != nil {
			return m[1]
		}
		return ""
	}
	hu, pu := jobUser(hout), jobUser(out)
	if hu == "" || pu == "" || hu == pu {
		t.Errorf("job users: holder %q, prober %q\n%s\n%s", hu, pu, hout, out)
	}
}

// A canceled job leaves nothing running, not even a process it moved out
// of its session: the helper kills all of the job user's processes.
func TestCanceledJobLeavesNoProcess(t *testing.T) {
	base := os.Getenv("AWXNEXT_TEST_ISOLATION_URL")
	if base == "" {
		t.Skip("AWXNEXT_TEST_ISOLATION_URL is not set (CI's compose job)")
	}
	a := &api{t: t, base: strings.TrimRight(base, "/"), pass: os.Getenv("AWXNEXT_TEST_ISOLATION_PASSWORD")}
	suffix := fmt.Sprint(time.Now().UnixNano())
	org := a.create("organizations/", map[string]any{"name": "cancel-" + suffix})
	inv := a.create("inventories/", map[string]any{"name": "cancel-" + suffix, "organization": org,
		"variables": "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"})
	a.create("hosts/", map[string]any{"name": "localhost", "inventory": inv})
	sleeper := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell",
		"module_args": "(setsid sleep 300 > /dev/null 2>&1 &); sleep 300"})
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		_, s, _ := a.call("GET", fmt.Sprintf("ad_hoc_commands/%d/", sleeper), nil)
		_, ev, _ := a.call("GET", fmt.Sprintf("ad_hoc_commands/%d/events/?event=runner_on_start", sleeper), nil)
		if s["status"] == "running" && ev["count"] != nil && ev["count"].(float64) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sleeper: %v", s["status"])
		}
	}
	time.Sleep(2 * time.Second)
	if status, _, raw := a.call("POST", fmt.Sprintf("ad_hoc_commands/%d/cancel/", sleeper), nil); status != 202 {
		t.Fatalf("cancel: %d %s", status, raw)
	}
	if s := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", sleeper), 2*time.Minute); s["status"] != "canceled" {
		t.Fatalf("sleeper: %v", s["status"])
	}
	// The other job users' processes, seen from a job (status is readable);
	// counted again for a while, the cancel finishing in the background.
	count := `me=$(id -u); n=0; for s in /proc/[0-9]*/status; do u=$(awk '/^Uid:/ {print $2}' "$s" 2>/dev/null); ` +
		`if [ -n "$u" ] && [ "$u" -ge 50000 ] && [ "$u" -lt 50128 ] && [ "$u" != "$me" ]; then n=$((n+1)); fi; done; echo "others: $n"`
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) {
		counter := a.create("ad_hoc_commands/", map[string]any{"inventory": inv, "module_name": "shell", "module_args": count})
		c := a.wait(fmt.Sprintf("ad_hoc_commands/%d/", counter), 2*time.Minute)
		out := a.stdout(fmt.Sprintf("ad_hoc_commands/%d/", counter))
		if c["status"] != "successful" {
			t.Fatalf("counter: %v\n%s", c["status"], out)
		}
		if strings.Contains(out, "others: 0") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes of other job users after the cancel:\n%s", out)
		}
	}
}
