package ansible

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runnerPath finds ansible-runner (PATH or the dev venv).
func runnerPath(t *testing.T) string {
	if p, err := exec.LookPath("ansible-runner"); err == nil {
		return p
	}
	if _, err := os.Stat("/opt/ansible-venv/bin/ansible-runner"); err == nil {
		return "/opt/ansible-venv/bin/ansible-runner"
	}
	t.Skip("ansible-runner not installed")
	return ""
}

func TestTransmitWorkerProcess(t *testing.T) {
	runner := runnerPath(t)
	pdd := t.TempDir()
	os.MkdirAll(filepath.Join(pdd, "project"), 0o755)
	os.MkdirAll(filepath.Join(pdd, "inventory"), 0o755)
	os.MkdirAll(filepath.Join(pdd, "env"), 0o755)
	os.WriteFile(filepath.Join(pdd, "project", "site.yml"), []byte(`- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "hello {{ who }}"
    - ansible.builtin.set_stats:
        data: {deployed: "{{ who }}"}
`), 0o644)
	os.WriteFile(filepath.Join(pdd, "inventory", "hosts"), []byte("#!/bin/sh\ncat <<'EOF'\n{\"all\": {\"hosts\": [\"localhost\"]}, \"_meta\": {\"hostvars\": {\"localhost\": {\"ansible_connection\": \"local\", \"ansible_python_interpreter\": \"{{ ansible_playbook_python }}\"}}}}\nEOF\n"), 0o755)
	os.WriteFile(filepath.Join(pdd, "env", "extravars"), []byte(`{"who": "awx-next"}`), 0o600)

	var in bytes.Buffer
	if err := Transmit(&in, map[string]any{"ident": "42", "playbook": "site.yml"}, pdd); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	cmd := exec.Command(runner, "worker", "--private-data-dir", work)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(runner)+":"+os.Getenv("PATH"))
	cmd.Stdin = &in
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	artifacts := t.TempDir()
	var statuses []string
	var stdout strings.Builder
	var stats map[string]any
	err = Process(out, artifacts, func(m StreamMessage) {
		if m.Status != "" {
			statuses = append(statuses, m.Status)
		}
		if m.Event != nil {
			if s, ok := m.Event["stdout"].(string); ok {
				stdout.WriteString(s + "\n")
			}
			if m.Event["event"] == "playbook_on_stats" {
				stats, _ = m.Event["event_data"].(map[string]any)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("worker failed: %v\n%s", err, stderr.String())
	}
	if len(statuses) == 0 || statuses[len(statuses)-1] != "successful" {
		t.Fatalf("statuses: %v\nstdout:\n%s\nstderr:\n%s", statuses, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello awx-next") {
		t.Fatalf("stdout missing debug output:\n%s", stdout.String())
	}
	if stats == nil || !strings.Contains(stdout.String(), "PLAY RECAP") {
		t.Fatal("no playbook_on_stats event")
	}
	if ad, _ := stats["artifact_data"].(map[string]any); ad["deployed"] != "awx-next" {
		t.Fatalf("set_stats artifact_data: %v", stats["artifact_data"])
	}
	if _, err := os.Stat(filepath.Join(artifacts, "rc")); err != nil {
		entries, _ := os.ReadDir(artifacts)
		t.Fatalf("artifacts not unpacked: %v", entries)
	}
}
