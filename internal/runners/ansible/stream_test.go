package ansible

import (
	"archive/zip"
	"bytes"
	"io"
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

// Keepalives (AWX_RUNNER_KEEPALIVE_SECONDS) keep the stream open and are not events.
func TestProcessDropsKeepalives(t *testing.T) {
	in := `{"event": "keepalive", "counter": 0, "uuid": 0}` + "\n" + `{"event": "verbose", "counter": 1, "stdout": "hi"}` + "\n" + `{"eof": true}` + "\n"
	var got []StreamMessage
	if err := Process(strings.NewReader(in), t.TempDir(), func(m StreamMessage) { got = append(got, m) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Event["counter"] != float64(1) {
		t.Fatalf("messages: %+v", got)
	}
}

func readZip(t *testing.T, b []byte) map[string]*zip.File {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	return files
}

func zipContent(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestZipDirsLayout(t *testing.T) {
	pdd := t.TempDir()
	os.MkdirAll(filepath.Join(pdd, "env"), 0o755)
	os.WriteFile(filepath.Join(pdd, "env", "extravars"), []byte(`{"a": 1}`), 0o600)
	os.Symlink("env/extravars", filepath.Join(pdd, "vars"))
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "site.yml"), []byte("- hosts: all\n"), 0o644)
	os.MkdirAll(filepath.Join(project, ".git"), 0o755)
	os.WriteFile(filepath.Join(project, ".git", "config"), []byte("[core]\n"), 0o644)
	// A manual project may be a symlink under the projects root; its files are
	// sent, as AWX copies them.
	linked := filepath.Join(t.TempDir(), "linked")
	os.Symlink(project, linked)

	var buf bytes.Buffer
	if err := zipDirs(&buf, []Mount{{Path: pdd}, {Prefix: "project", Path: linked}}); err != nil {
		t.Fatal(err)
	}
	files := readZip(t, buf.Bytes())
	for _, name := range []string{"env/", "env/extravars", "vars", "project/", "project/site.yml"} {
		if files[name] == nil {
			t.Fatalf("no %s in %v", name, files)
		}
	}
	if got := zipContent(t, files["env/extravars"]); got != `{"a": 1}` {
		t.Fatalf("env/extravars: %q", got)
	}
	if files["vars"].Mode()&os.ModeSymlink == 0 || zipContent(t, files["vars"]) != "env/extravars" {
		t.Fatalf("vars is not stored as a link: %v %q", files["vars"].Mode(), zipContent(t, files["vars"]))
	}
	for name := range files {
		if strings.Contains(name, ".git") {
			t.Fatalf("%s was sent", name)
		}
	}
}

// A file replaced by a symlink while the directory is zipped must not bring
// in what the link points to outside the directory (#142).
func TestZipDirsSymlinkSwap(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("OUTSIDE-SECRET"), 0o600)
	pdd := t.TempDir()
	slot := filepath.Join(pdd, "slot")
	os.WriteFile(slot, []byte("inside"), 0o600)
	scratch := t.TempDir()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		link, file := filepath.Join(scratch, "link"), filepath.Join(scratch, "file")
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.Symlink(secret, link)
			os.Rename(link, slot)
			os.WriteFile(file, []byte("inside"), 0o600)
			os.Rename(file, slot)
		}
	}()
	defer func() { close(stop); <-done }()

	for range 3000 {
		var buf bytes.Buffer
		if err := zipDirs(&buf, []Mount{{Path: pdd}}); err != nil {
			continue
		}
		f := readZip(t, buf.Bytes())["slot"]
		if f != nil && f.Mode()&os.ModeSymlink == 0 && zipContent(t, f) == "OUTSIDE-SECRET" {
			t.Fatal("the file outside the directory was sent")
		}
	}
}
