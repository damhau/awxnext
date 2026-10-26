package ansible

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// Run describes one ansible-runner execution.
type Run struct {
	Ident string
	// Kwargs for ansible-runner (playbook, module, ...).
	Kwargs map[string]any
	// Dir is the local private data directory being prepared.
	Dir string
	// Root is the private data directory path as seen by ansible-runner
	// (inside the container, or the worker's directory for local runs).
	Root string
	// Mounts are extra directories transmitted into the private data dir.
	Mounts []Mount
	// Image and pull policy of the execution environment (container runtimes).
	Image string
	Pull  string
	// Registry credentials for pulling the image.
	RegistryHost, RegistryUser, RegistryPassword string
	// WorkDir is the worker's private data dir for local runs.
	WorkDir string
	Timeout time.Duration
	runtime string
	base    string
}

// NewRun prepares directories for a run under the jobs root.
func NewRun(app *api.App, ident string) (*Run, error) {
	base := filepath.Join(app.Cfg.JobsRoot, ident)
	os.RemoveAll(base)
	r := &Run{Ident: ident, Dir: filepath.Join(base, "pdd"), runtime: app.Cfg.ContainerRuntime, base: base}
	for _, d := range []string{r.Dir, filepath.Join(r.Dir, "env"), filepath.Join(r.Dir, "inventory")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	switch r.runtime {
	case "none", "process", "":
		r.WorkDir = filepath.Join(base, "work")
		if err := os.MkdirAll(r.WorkDir, 0o700); err != nil {
			return nil, err
		}
		r.Root = r.WorkDir
	default:
		r.Root = "/runner"
	}
	return r, nil
}

// Cleanup removes the run's local files.
func (r *Run) Cleanup() { os.RemoveAll(r.base) }

// WriteFile writes a file into the private data dir (path relative to it).
func (r *Run) WriteFile(rel string, data []byte, mode os.FileMode) error {
	p := filepath.Join(r.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, mode)
}

// WriteJSON writes a JSON file into the private data dir.
func (r *Run) WriteJSON(rel string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.WriteFile(rel, b, 0o600)
}

// Path returns the runtime path of a file in the private data dir.
func (r *Run) Path(rel string) string { return r.Root + "/" + strings.TrimPrefix(rel, "/") }

// Outcome is the result of an execution.
type Outcome struct {
	Status    string // successful, failed, canceled, timeout, error
	RC        int
	Stats     map[string]any
	Artifacts string // local directory holding returned artifacts
	Args      []string
}

// Execute transmits the private data dir to ansible-runner worker and
// streams events into out.
func (r *Run) Execute(ctx context.Context, app *api.App, out *jobs.Output) (*Outcome, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	args, cleanup, err := r.command(app)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	oc := &Outcome{Status: "error", Artifacts: filepath.Join(r.base, "artifacts"), Args: args}
	os.MkdirAll(oc.Artifacts, 0o700)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = r.env()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", args[0], err)
	}
	go func() {
		Transmit(stdin, r.Kwargs, r.Dir, r.Mounts...)
		stdin.Close()
	}()
	// Events of a later execution in the same job continue the numbering.
	baseCounter, baseLine := out.Position()
	done := make(chan error, 1)
	go func() {
		done <- Process(stdout, oc.Artifacts, func(m StreamMessage) {
			if m.Status != "" {
				oc.Status = m.Status
				if m.RC != nil {
					oc.RC = *m.RC
				}
				return
			}
			if m.Event != nil {
				ev := MapEvent(m.Event)
				if ev.Counter > 0 {
					ev.Counter += baseCounter
				}
				if ev.StartLine > 0 || ev.EndLine > 0 {
					ev.StartLine += baseLine
					ev.EndLine += baseLine
				}
				if ev.Event == "playbook_on_stats" {
					oc.Stats = ev.EventData
				}
				out.Emit(ev)
			}
		})
	}()
	var perr error
	select {
	case perr = <-done:
	case <-ctx.Done():
		r.kill(cmd)
		<-done
	}
	werr := cmd.Wait()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		oc.Status = "timeout"
	case ctx.Err() != nil:
		oc.Status = "canceled"
	case perr != nil:
		return oc, perr
	case oc.Status == "error" || oc.Status == "starting" || oc.Status == "running":
		msg := strings.TrimSpace(stderr.String())
		if werr != nil && msg == "" {
			msg = werr.Error()
		}
		if msg != "" {
			out.Emit(jobs.Event{Event: "error", Stdout: msg, Failed: true})
		}
		return oc, fmt.Errorf("ansible-runner did not complete: %s", lastLine(msg))
	}
	return oc, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func (r *Run) kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
	if r.runtime == "podman" || r.runtime == "docker" {
		exec.Command(r.runtime, "rm", "-f", r.containerName()).Run()
	}
}

func (r *Run) containerName() string { return "awxnext_job_" + r.Ident }

func (r *Run) env() []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8"}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "XDG_RUNTIME_DIR", "CONTAINER_HOST", "DOCKER_HOST", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// command builds the worker command line for the configured runtime.
func (r *Run) command(app *api.App) ([]string, func(), error) {
	noop := func() {}
	switch r.runtime {
	case "none", "process", "":
		bin := app.Cfg.RunnerCommand
		return []string{bin, "worker", "--private-data-dir", r.WorkDir}, noop, nil
	case "podman", "docker":
		if r.Image == "" {
			return nil, noop, errors.New("no execution environment image")
		}
		args := []string{r.runtime}
		var runArgs []string
		cleanup := noop
		if r.RegistryUser != "" {
			dir, err := os.MkdirTemp("", "awxnext-auth-")
			if err != nil {
				return nil, noop, err
			}
			cleanup = func() { os.RemoveAll(dir) }
			auth := base64.StdEncoding.EncodeToString([]byte(r.RegistryUser + ":" + r.RegistryPassword))
			cfg, _ := json.Marshal(map[string]any{"auths": map[string]any{r.RegistryHost: map[string]string{"auth": auth}}})
			if r.runtime == "docker" {
				// docker reads registry auth from <config dir>/config.json.
				if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600); err != nil {
					cleanup()
					return nil, noop, err
				}
				args = append(args, "--config", dir)
			} else {
				if err := os.WriteFile(filepath.Join(dir, "auth.json"), cfg, 0o600); err != nil {
					cleanup()
					return nil, noop, err
				}
				runArgs = append(runArgs, "--authfile", filepath.Join(dir, "auth.json"))
			}
		}
		args = append(args, "run")
		args = append(args, runArgs...)
		pull := r.Pull
		if pull == "" {
			pull = "missing"
		}
		args = append(args, "--rm", "-i", "--pull="+pull, "--name", r.containerName(), "--env", "HOME=/runner")
		if r.runtime == "podman" {
			args = append(args, "--userns=keep-id")
		}
		args = append(args, r.Image, "ansible-runner", "worker", "--private-data-dir=/runner")
		return args, cleanup, nil
	case "kubernetes":
		return nil, noop, errors.New("container groups require the job agent (not yet available on this node)")
	}
	return nil, noop, fmt.Errorf("unknown container runtime %q", r.runtime)
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		l.w.Write(p[:l.n])
		l.n = 0
		return len(p), nil
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// MapEvent converts an ansible-runner event into a job event.
func MapEvent(m map[string]any) jobs.Event {
	s := func(k string) string { v, _ := m[k].(string); return v }
	i := func(k string) int { v, _ := m[k].(float64); return int(v) }
	data, _ := m["event_data"].(map[string]any)
	ds := func(k string) string { v, _ := data[k].(string); return v }
	e := jobs.Event{
		Counter:    i("counter"),
		Event:      s("event"),
		UUID:       s("uuid"),
		ParentUUID: s("parent_uuid"),
		Stdout:     s("stdout"),
		StartLine:  i("start_line"),
		EndLine:    i("end_line"),
		Verbosity:  i("verbosity"),
		EventData:  data,
		HostName:   ds("host"),
		Playbook:   ds("playbook"),
		Play:       ds("play"),
		Role:       ds("role"),
		Task:       ds("task"),
	}
	if c := s("created"); c != "" {
		if t, err := time.Parse("2006-01-02T15:04:05.999999", c); err == nil {
			e.Created = t
		} else if t, err := time.Parse(time.RFC3339Nano, c); err == nil {
			e.Created = t
		}
	}
	switch e.Event {
	case "runner_on_failed", "runner_on_unreachable", "runner_item_on_failed", "runner_on_async_failed":
		if ignore, _ := data["ignore_errors"].(bool); !ignore {
			e.Failed = true
		}
	}
	if res, ok := data["res"].(map[string]any); ok {
		if ch, _ := res["changed"].(bool); ch {
			e.Changed = true
		}
	}
	if e.Event == "playbook_on_stats" {
		if f, ok := data["failures"].(map[string]any); ok && len(f) > 0 {
			e.Failed = true
		}
		if d, ok := data["dark"].(map[string]any); ok && len(d) > 0 {
			e.Failed = true
		}
		if c, ok := data["changed"].(map[string]any); ok && len(c) > 0 {
			e.Changed = true
		}
	}
	return e
}
