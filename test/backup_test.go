package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// pgClientTools finds pg_dump and pg_restore: on the PATH, or in a local
// PostgreSQL install of the developer's. CI must have them.
func pgClientTools(t *testing.T) (string, string) {
	t.Helper()
	dump, err1 := exec.LookPath("pg_dump")
	restore, err2 := exec.LookPath("pg_restore")
	if err1 == nil && err2 == nil {
		return dump, restore
	}
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, ".cache", "awxnext-pg", ".venv", "lib", "python3*", "site-packages", "pgserver", "pginstall", "bin"))
	for _, dir := range matches {
		d, r := filepath.Join(dir, "pg_dump"), filepath.Join(dir, "pg_restore")
		if _, err := os.Stat(d); err == nil {
			return d, r
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatal("pg_dump and pg_restore are needed in CI")
	}
	t.Skip("pg_dump and pg_restore not found")
	return "", ""
}

// buildAwxnext builds the awxnext binary once per test run.
func buildAwxnext(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "awxnext")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/awxnext")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// awxnext backup of this install, awxnext restore into an empty database
// under another master key, and a job run by the restored install with a
// secret of the backup.
func TestBackupAndRestore(t *testing.T) {
	requireAnsible(t)
	pgDump, pgRestore := pgClientTools(t)
	f := newLaunchFixture(t)
	a := f.a
	ct := must(t, a.post("credential_types/", map[string]any{"name": uniq("mycloud"), "kind": "cloud",
		"inputs": map[string]any{"fields": []any{map[string]any{"id": "token", "label": "Token", "secret": true}, map[string]any{"id": "region", "label": "Region"}}},
		"injectors": map[string]any{"env": map[string]any{"MYCLOUD_TOKEN": "{{ token }}", "MYCLOUD_CONFIG": "{{ tower.filename.cfg }}"},
			"file": map[string]any{"template.cfg": "{{ region | lower }}"}}}), 201).id()
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("c"), "organization": f.org, "credential_type": ct,
		"inputs": map[string]any{"token": "abcdefghij", "region": "EU-west"}}), 201).id()
	jt := f.template(t, "env.yml", nil)
	must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": cred}), 204)

	bin := buildAwxnext(t)
	dir := t.TempDir()
	passFile := filepath.Join(dir, "passphrase")
	os.WriteFile(passFile, []byte("correct horse battery staple\n"), 0o600)
	run := func(env []string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	tools := []string{"AWXNEXT_PG_DUMP=" + pgDump, "AWXNEXT_PG_RESTORE=" + pgRestore}
	source := append([]string{"AWXNEXT_DATABASE_URL=" + app.Cfg.DatabaseURL, "AWXNEXT_SECRET_KEY=" + app.Cfg.SecretKey,
		"AWXNEXT_NODE_NAME=test-node", "AWXNEXT_PROJECTS_ROOT=" + app.Cfg.ProjectsRoot, "AWXNEXT_JOBS_ROOT=" + app.Cfg.JobsRoot}, tools...)

	file := filepath.Join(dir, "awxnext.tar.gz")
	if out, err := run(source, "backup", "-o", file, "--passphrase-file", passFile); err != nil || !strings.Contains(out, "written to "+file) {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	plain := filepath.Join(dir, "plain.tar.gz")
	if out, err := run(source, "backup", "-o", plain); err != nil || !strings.Contains(out, "restoring it needs this install's master key") {
		t.Fatalf("backup without a passphrase: %v\n%s", err, out)
	}

	// An empty database, another master key.
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, databaseURL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := fmt.Sprintf("awxnext_restore_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	target := append([]string{"AWXNEXT_DATABASE_URL=" + databaseURL(name), "AWXNEXT_SECRET_KEY=another-master-key", "AWXNEXT_NODE_NAME=restored-node",
		"AWXNEXT_PROJECTS_ROOT=" + t.TempDir(), "AWXNEXT_JOBS_ROOT=" + t.TempDir(), "AWXNEXT_CONTAINER_RUNTIME=none",
		"AWXNEXT_LISTEN=" + fmt.Sprintf("127.0.0.1:%d", port), "AWXNEXT_BASE_URL=" + base}, tools...)
	if out, err := run(target, "restore", "--passphrase-file", filepath.Join(dir, "missing"), file); err == nil {
		t.Errorf("missing passphrase file: %s", out)
	}
	os.WriteFile(filepath.Join(dir, "wrong"), []byte("wrong"), 0o600)
	if out, err := run(target, "restore", "--passphrase-file", filepath.Join(dir, "wrong"), file); err == nil || !strings.Contains(out, "wrong passphrase") {
		t.Errorf("wrong passphrase: %v\n%s", err, out)
	}
	if out, err := run(source, "restore", "--passphrase-file", passFile, file); err == nil || !strings.Contains(out, "the database is not empty") {
		t.Errorf("into a database in use: %v\n%s", err, out)
	}
	out, err := run(target, "restore", "--passphrase-file", passFile, file)
	if err != nil || !strings.Contains(out, "re-wrapped") || !strings.Contains(out, "queued") {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	defer admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")

	// The restored install runs the job, with the secret.
	var logs bytes.Buffer
	srvCmd := exec.Command(bin, "serve")
	srvCmd.Env = append(os.Environ(), target...)
	srvCmd.Stdout, srvCmd.Stderr = &logs, &logs
	if err := srvCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		srvCmd.Process.Kill()
		srvCmd.Wait()
		if t.Failed() {
			t.Logf("restored server:\n%s", logs.String())
		}
	}()
	call := func(method, path string, body any) (int, map[string]any, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+"/api/v2/"+path, rd)
		req.SetBasicAuth("admin", "admin")
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
	deadline := time.Now().Add(60 * time.Second)
	for {
		if status, _, _ := call("GET", "ping/", nil); status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the restored server did not start")
		}
		time.Sleep(200 * time.Millisecond)
	}
	status, launched, raw := call("POST", fmt.Sprintf("job_templates/%d/launch/", jt), map[string]any{})
	if status != 201 {
		t.Fatalf("launch on the restored install: %d %s", status, raw)
	}
	id := int64(launched["id"].(float64))
	var job map[string]any
	for deadline = time.Now().Add(120 * time.Second); ; {
		_, job, _ = call("GET", fmt.Sprintf("jobs/%d/", id), nil)
		if s := job["status"]; s == "successful" || s == "failed" || s == "error" || s == "canceled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %v", job["status"])
		}
		time.Sleep(250 * time.Millisecond)
	}
	_, _, stdout := call("GET", fmt.Sprintf("jobs/%d/stdout/?format=txt", id), nil)
	if job["status"] != "successful" || !strings.Contains(stdout, "token length 10 region=eu-west") {
		t.Fatalf("restored job: %v %v\n%s", job["status"], job["job_explanation"], stdout)
	}
	// The checkouts were rebuilt by project updates queued at restore.
	_, p, _ := call("GET", fmt.Sprintf("projects/%d/project_updates/?launch_type=manual&order_by=-id", f.project), nil)
	if n, _ := p["count"].(float64); n < 1 {
		t.Errorf("no project update queued at restore: %v", p)
	}
	// The backed up install's node is not used by the restored one.
	_, inst, _ := call("GET", "instances/?hostname=test-node", nil)
	if r, _ := inst["results"].([]any); len(r) != 1 || r[0].(map[string]any)["node_state"] != "unavailable" {
		t.Errorf("backed up node: %v", inst["results"])
	}
}
