package ansible

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/runnercmd"
	"github.com/damhau/awxnext/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
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
	// agentNode is the remote execution node running the worker; pod a container group job pod.
	agentNode string
	pod       *agent.PodRequest
}

// NewRunFor prepares a run placed where the dispatcher put the job: locally,
// on a remote execution node (agent) or in a container group (job pod).
func NewRunFor(ctx context.Context, app *api.App, ident string, row jobs.Row) (*Run, error) {
	r, err := NewRun(app, ident)
	if err != nil {
		return nil, err
	}
	if ig := row.Int("instance_group_id"); ig != 0 {
		var container bool
		var podSpec string
		var credID *int64
		app.DB.QueryRow(ctx, "SELECT is_container_group, pod_spec_override, credential_id FROM instance_groups WHERE id = $1", ig).Scan(&container, &podSpec, &credID)
		if container {
			req := &agent.PodRequest{JobID: row.ID(), PodSpec: podSpec, ServiceURL: app.Cfg.ServiceURL, ShimImage: app.Cfg.ShimImage}
			app.DB.QueryRow(ctx, "SELECT value #>> '{}' FROM settings WHERE key = 'INSTALL_UUID'").Scan(&req.InstallID)
			if credID != nil {
				cred, err := resources.ResolveCredential(ctx, app, *credID)
				if err != nil {
					return nil, err
				}
				in := func(k string) string { v, _ := cred.Inputs[k].(string); return v }
				verify, ok := cred.Inputs["verify_ssl"].(bool)
				req.Credential = &agent.KubeCredential{Host: in("host"), BearerToken: in("bearer_token"), CACert: in("ssl_ca_cert"), VerifySSL: verify || !ok}
			}
			r.useRemote("kubernetes", "/runner")
			r.pod = req
			return r, nil
		}
	}
	node := row.Str("execution_node")
	if node == "" || node == app.Cfg.NodeName {
		return r, nil
	}
	var nodeType, runtime, workDir string
	if err := app.DB.QueryRow(ctx, "SELECT node_type, agent_runtime, agent_work_dir FROM instances WHERE hostname = $1", node).Scan(&nodeType, &runtime, &workDir); err != nil {
		return nil, fmt.Errorf("execution node %s no longer exists", node)
	}
	if nodeType != "execution" {
		return r, nil
	}
	root := "/runner"
	if runtime == "none" || runtime == "" {
		runtime = "none"
		root = strings.TrimRight(workDir, "/") + "/" + ident
	}
	r.useRemote(runtime, root)
	r.agentNode = node
	return r, nil
}

func (r *Run) useRemote(runtime, root string) {
	if r.WorkDir != "" {
		os.RemoveAll(r.WorkDir)
		r.WorkDir = ""
	}
	r.runtime, r.Root = runtime, root
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

// worker is a running `ansible-runner worker` (local process, agent or job pod).
type worker interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Wait() (string, error)
	Kill()
}

type localWorker struct {
	cmd     *exec.Cmd
	spec    runnercmd.Spec
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  bytes.Buffer
	cleanup func()
}

func (l *localWorker) Stdin() io.WriteCloser { return l.stdin }
func (l *localWorker) Stdout() io.Reader     { return l.stdout }
func (l *localWorker) Kill()                 { runnercmd.Kill(l.spec, l.cmd) }
func (l *localWorker) Wait() (string, error) {
	err := l.cmd.Wait()
	l.cleanup()
	return strings.TrimSpace(l.stderr.String()), err
}

func (r *Run) spec(app *api.App) runnercmd.Spec {
	return runnercmd.Spec{Runtime: r.runtime, RunnerCommand: app.Cfg.RunnerCommand, WorkDir: r.WorkDir, Image: r.Image, Pull: r.Pull,
		RegistryHost: r.RegistryHost, RegistryUser: r.RegistryUser, RegistryPassword: r.RegistryPassword, Name: r.containerName()}
}

// startWorker starts the worker where the run is placed.
func (r *Run) startWorker(ctx context.Context, app *api.App) (worker, []string, error) {
	ws := agent.WorkSpec{Ident: r.Ident, Image: r.Image, Pull: r.Pull, RegistryHost: r.RegistryHost, RegistryUser: r.RegistryUser, RegistryPassword: r.RegistryPassword}
	switch {
	case r.agentNode != "":
		if agent.Default == nil {
			return nil, nil, errors.New("remote execution is not available on this node")
		}
		w, err := agent.Default.StartWorker(ctx, r.agentNode, ws)
		return w, []string{"agent:" + r.agentNode, "ansible-runner", "worker"}, err
	case r.pod != nil:
		if agent.Default == nil {
			return nil, nil, errors.New("container groups are not available on this node")
		}
		req := *r.pod
		req.Spec = ws
		w, err := agent.Default.StartPod(ctx, req)
		return w, []string{"pod", "ansible-runner", "worker"}, err
	}
	spec := r.spec(app)
	args, cleanup, err := runnercmd.Command(spec)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = runnercmd.Env()
	lw := &localWorker{cmd: cmd, spec: spec, cleanup: cleanup}
	if lw.stdin, err = cmd.StdinPipe(); err != nil {
		cleanup()
		return nil, nil, err
	}
	if lw.stdout, err = cmd.StdoutPipe(); err != nil {
		cleanup()
		return nil, nil, err
	}
	cmd.Stderr = &limitedWriter{w: &lw.stderr, n: 64 << 10}
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("starting %s: %w", args[0], err)
	}
	return lw, args, nil
}

// Execute transmits the private data dir to ansible-runner worker and
// streams events into out.
func (r *Run) Execute(ctx context.Context, app *api.App, out *jobs.Output) (oc *Outcome, err error) {
	ctx, span := tracing.StartSpan(ctx, "ansible-runner worker", attribute.String("awx.runner.ident", r.Ident), attribute.String("awx.runner.runtime", r.runtime),
		attribute.String("awx.runner.execution_node", r.agentNode))
	defer func() {
		if oc != nil {
			span.SetAttributes(attribute.String("awx.runner.status", oc.Status), attribute.Int("awx.runner.rc", oc.RC))
		}
		tracing.EndSpan(span, err)
	}()
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	w, args, err := r.startWorker(ctx, app)
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return &Outcome{Status: "timeout"}, nil
		case ctx.Err() != nil:
			return &Outcome{Status: "canceled"}, nil
		}
		return nil, err
	}
	oc = &Outcome{Status: "error", Artifacts: filepath.Join(r.base, "artifacts"), Args: args}
	os.MkdirAll(oc.Artifacts, 0o700)
	go func() {
		Transmit(w.Stdin(), r.Kwargs, r.Dir, r.Mounts...)
		w.Stdin().Close()
	}()
	// Events of a later execution in the same job continue the numbering.
	baseCounter, baseLine := out.Position()
	done := make(chan error, 1)
	go func() {
		done <- Process(w.Stdout(), oc.Artifacts, func(m StreamMessage) {
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
		w.Kill()
		<-done
	}
	diag, werr := w.Wait()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		oc.Status = "timeout"
	case ctx.Err() != nil:
		oc.Status = "canceled"
	case perr != nil:
		return oc, perr
	case oc.Status == "error" || oc.Status == "starting" || oc.Status == "running":
		msg := diag
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

func (r *Run) containerName() string { return "awxnext_job_" + r.Ident }

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
