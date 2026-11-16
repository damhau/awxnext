package resources

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/connectors"
	"github.com/damhau/awxnext/internal/credtypes"
	"github.com/damhau/awxnext/internal/jobexec"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/runnercmd"
)

// How event connectors run. The leader places each enabled connector on one
// node (ConnectorTick): a hybrid node runs it itself, an execution node's
// agent runs it for a control node, a container group runs it in a pod that
// a control node starts. The controller node's supervisor starts the run
// with a new token, streams its log, and when it stops applies the
// connector's restart policy. The leader moves the connectors of a lost
// node: their token is revoked first, so that a runner left on a lost node
// can no longer post.

const (
	connectorLogTail  = 64 << 10
	connectorRunsKept = 20
	// A run this long resets the restart backoff.
	connectorStableRun = 10 * time.Minute
	connectorBatch     = 100
	connectorLostAfter = "2 minutes"
)

// ConnectorTick moves connectors off lost nodes and places those due to
// start. It runs on the leader.
func ConnectorTick(ctx context.Context, m *jobs.Manager) {
	if err := moveLostConnectors(ctx, m.App); err != nil {
		slog.Warn("event connectors: moving off lost nodes", "err", err)
	}
	if err := placeConnectors(ctx, m.App); err != nil {
		slog.Warn("event connectors: placing", "err", err)
	}
}

func moveLostConnectors(ctx context.Context, app *api.App) error {
	alive := `SELECT 1 FROM instances i WHERE i.hostname = %s AND i.enabled AND i.last_seen > now() - interval '` + connectorLostAfter + `'`
	_, err := app.DB.Exec(ctx, `WITH lost AS (
			SELECT c.id, c.generation, CASE WHEN NOT EXISTS (`+fmt.Sprintf(alive, "c.controller_node")+`) THEN c.controller_node ELSE c.execution_node END AS node
			FROM event_connectors c
			WHERE c.controller_node <> '' AND c.status IN ('starting', 'running') AND (NOT EXISTS (`+fmt.Sprintf(alive, "c.controller_node")+`)
				OR (c.execution_node NOT IN ('', c.controller_node) AND NOT EXISTS (`+fmt.Sprintf(alive, "c.execution_node")+` AND i.node_state = 'ready')))
			FOR UPDATE OF c SKIP LOCKED
		), moved AS (
			UPDATE event_connectors c SET status = 'pending', status_explanation = 'Node ' || l.node || ' was lost: moving to another node.',
				controller_node = '', execution_node = '', token_hash = '', next_start = NULL
			FROM lost l WHERE c.id = l.id RETURNING c.id, l.generation, l.node
		)
		UPDATE event_connector_runs r SET status = 'lost', finished = now(), modified = now(), explanation = 'Node ' || m.node || ' was lost.'
		FROM moved m WHERE r.connector_id = m.id AND r.generation = m.generation AND r.finished IS NULL`)
	return err
}

func placeConnectors(ctx context.Context, app *api.App) error {
	rows, err := app.DB.Query(ctx, `SELECT id, instance_group_id, released_by, released_at FROM event_connectors
		WHERE enabled AND status IN ('pending', 'restarting') AND controller_node = '' AND (next_start IS NULL OR next_start <= now())
		ORDER BY next_start NULLS FIRST, id LIMIT 50`)
	if err != nil {
		return err
	}
	type due struct {
		id         int64
		group      *int64
		avoid      string
		releasedAt *time.Time
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.group, &d.avoid, &d.releasedAt); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		ctl, exe, why, err := connectorNode(ctx, app, d.group, d.avoid, d.releasedAt)
		if err != nil {
			return err
		}
		if ctl == "" {
			if _, err := app.DB.Exec(ctx, `UPDATE event_connectors SET status_explanation = $2, next_start = now() + interval '10 seconds'
				WHERE id = $1 AND controller_node = ''`, d.id, why); err != nil {
				return err
			}
			continue
		}
		if _, err := app.DB.Exec(ctx, `UPDATE event_connectors SET status = 'starting', controller_node = $2, execution_node = $3,
			generation = generation + 1, next_start = NULL, token_hash = '', released_by = '', released_at = NULL
			WHERE id = $1 AND enabled AND status IN ('pending', 'restarting') AND controller_node = ''`, d.id, ctl, exe); err != nil {
			return err
		}
	}
	return nil
}

// connectorNode picks where a connector runs in its instance group: the
// controller node, and the node it runs on (empty in a container group).
// It passes over avoid, the node that gave the connector back as it stopped,
// unless that node was seen since.
func connectorNode(ctx context.Context, app *api.App, group *int64, avoid string, releasedAt *time.Time) (ctl, exe, why string, err error) {
	if avoid == "" || releasedAt == nil {
		avoid, releasedAt = "", &time.Time{}
	}
	var gid int64
	var name string
	var container bool
	q := "SELECT id, name, is_container_group FROM instance_groups WHERE name = 'default'"
	args := []any{}
	if group != nil {
		q, args = "SELECT id, name, is_container_group FROM instance_groups WHERE id = $1", []any{*group}
	}
	if err := app.DB.QueryRow(ctx, q, args...).Scan(&gid, &name, &container); err != nil {
		return "", "", "Its instance group no longer exists.", nil
	}
	controlNode := func() (string, error) {
		var host string
		err := app.DB.QueryRow(ctx, `SELECT i.hostname FROM instances i
			WHERE i.node_type IN ('control', 'hybrid') AND i.enabled AND i.last_seen > now() - interval '`+connectorLostAfter+`'
				AND (i.hostname <> $1 OR i.last_seen > $2)
			ORDER BY (SELECT count(*) FROM event_connectors c WHERE c.controller_node = i.hostname), i.hostname LIMIT 1`, avoid, releasedAt).Scan(&host)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return host, err
	}
	if container {
		ctl, err := controlNode()
		if ctl == "" && err == nil {
			why = "No control node is up to start its pod."
		}
		return ctl, "", why, err
	}
	var host, nodeType string
	err = app.DB.QueryRow(ctx, `SELECT i.hostname, i.node_type FROM instances i JOIN instance_group_instances gi ON gi.instance_id = i.id
		WHERE gi.instance_group_id = $1 AND i.enabled AND i.node_state = 'ready' AND i.node_type IN ('execution', 'hybrid')
			AND i.last_seen > now() - interval '`+connectorLostAfter+`' AND (i.hostname <> $2 OR i.last_seen > $3)
		ORDER BY (SELECT count(*) FROM event_connectors c WHERE c.execution_node = i.hostname), i.hostname LIMIT 1`, gid, avoid, releasedAt).Scan(&host, &nodeType)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Sprintf("No node of instance group %s is up to run it.", name), nil
	}
	if err != nil {
		return "", "", "", err
	}
	if nodeType == "hybrid" {
		return host, host, "", nil
	}
	ctl, err = controlNode()
	if ctl == "" && err == nil {
		why = "No control node is up to start it on " + host + "."
	}
	return ctl, host, why, err
}

// ConnectorSupervisor runs the connectors this node controls.
type ConnectorSupervisor struct {
	app  *api.App
	node string
	// Poll is how often it reads which connectors it controls.
	Poll time.Duration

	mu   sync.Mutex
	runs map[int64]*connectorRun
	wg   sync.WaitGroup
	done chan struct{}
}

// Connectors is this node's supervisor, set when the node starts.
var Connectors *ConnectorSupervisor

func NewConnectorSupervisor(app *api.App, node string) *ConnectorSupervisor {
	return &ConnectorSupervisor{app: app, node: node, Poll: time.Second, runs: map[int64]*connectorRun{}, done: make(chan struct{})}
}

// Wait returns once Run has handed this node's connectors back.
func (s *ConnectorSupervisor) Wait() {
	if s != nil {
		<-s.done
	}
}

type connectorRun struct {
	id, gen  int64
	cancel   context.CancelFunc
	shutdown atomic.Bool
}

// Run supervises until ctx ends; then it stops its runs and gives their
// connectors back for another node.
func (s *ConnectorSupervisor) Run(ctx context.Context) {
	defer close(s.done)
	t := time.NewTicker(s.Poll)
	defer t.Stop()
	for {
		if err := s.reconcile(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("event connectors: reading this node's", "err", err)
		}
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for _, r := range s.runs {
				r.shutdown.Store(true)
				r.cancel()
			}
			s.mu.Unlock()
			s.wg.Wait()
			return
		case <-t.C:
		}
	}
}

func (s *ConnectorSupervisor) reconcile(ctx context.Context) error {
	rows, err := s.app.DB.Query(ctx, `SELECT id, generation FROM event_connectors
		WHERE controller_node = $1 AND enabled AND status IN ('starting', 'running')`, s.node)
	if err != nil {
		return err
	}
	want := map[int64]int64{}
	for rows.Next() {
		var id, gen int64
		if err := rows.Scan(&id, &gen); err != nil {
			rows.Close()
			return err
		}
		want[id] = gen
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.runs {
		if want[id] != r.gen {
			r.cancel()
		}
	}
	for id, gen := range want {
		if _, running := s.runs[id]; running {
			continue
		}
		rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		r := &connectorRun{id: id, gen: gen, cancel: cancel}
		s.runs[id] = r
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer cancel()
			s.run(rctx, r)
			s.mu.Lock()
			if s.runs[id] == r {
				delete(s.runs, id)
			}
			s.mu.Unlock()
		}()
	}
	return nil
}

// connectorWorker is a running connector: an agent's work unit, a pod, or
// a process on this node.
type connectorWorker interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Wait() (string, error)
	Kill()
	ExitCode() int
}

type connectorLaunch struct {
	cfg       connectors.Config
	spec      agent.WorkSpec
	exe       string
	pod       *agent.PodRequest
	runtime   string
	policy    string
	delay     int
	max       int
	restarts  int
	streamURL string
}

// run is one run of a connector, from its token to its restart policy.
func (s *ConnectorSupervisor) run(ctx context.Context, r *connectorRun) {
	bg := context.WithoutCancel(ctx)
	l, err := s.prepare(bg, r.id, r.gen)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	tok := agent.NewToken()
	tag, terr := s.app.DB.Exec(bg, `UPDATE event_connectors SET token_hash = $4, status = 'running', status_explanation = '', started = now(),
		stats = '{}' WHERE id = $1 AND generation = $2 AND controller_node = $3`, r.id, r.gen, s.node, agent.HashToken(tok))
	if terr != nil || tag.RowsAffected() == 0 {
		return
	}
	exe := s.node
	if l != nil {
		exe = l.exe
		if exe == "" {
			exe = "pod"
		}
	}
	var runID int64
	if err := s.app.DB.QueryRow(bg, `INSERT INTO event_connector_runs (connector_id, generation, controller_node, execution_node)
		VALUES ($1, $2, $3, $4) RETURNING id`, r.id, r.gen, s.node, exe).Scan(&runID); err != nil {
		slog.Warn("event connectors: recording a run", "connector", r.id, "err", err)
		return
	}
	s.app.DB.Exec(bg, `DELETE FROM event_connector_runs WHERE connector_id = $1 AND id <= (SELECT id FROM event_connector_runs
		WHERE connector_id = $1 ORDER BY id DESC OFFSET $2 LIMIT 1)`, r.id, connectorRunsKept)
	started := time.Now()
	out := &connectorOutput{}
	rc, lost := -1, false
	if err == nil {
		l.cfg.Token = tok
		var w connectorWorker
		w, err = s.start(ctx, l)
		if err == nil {
			rc, lost, err = s.watch(ctx, r, runID, w, l, out)
		}
	}
	if ctx.Err() != nil {
		if r.shutdown.Load() {
			s.finishRun(bg, runID, "stopped", nil, fmt.Sprintf("Node %s stopped.", s.node), out)
			s.app.DB.Exec(bg, `UPDATE event_connectors SET status = 'pending', status_explanation = 'Node ' || $3::text || ' stopped: moving to another node.',
				controller_node = '', execution_node = '', token_hash = '', next_start = NULL, released_by = $3, released_at = now()
				WHERE id = $1 AND generation = $2 AND controller_node = $3`,
				r.id, r.gen, s.node)
		} else {
			s.finishRun(bg, runID, "stopped", nil, "The connector was changed, stopped or moved.", out)
		}
		return
	}
	s.finished(bg, r, l, runID, started, rc, lost, err, out)
}

// prepare reads what the run needs: its configuration with the
// credentials' injections, its image, where it runs.
func (s *ConnectorSupervisor) prepare(ctx context.Context, id, gen int64) (*connectorLaunch, error) {
	var name, uuid, source, python, exe, policy string
	var args map[string]any
	var filters []map[string]any
	var creds []int64
	var ee, ig *int64
	var org int64
	var buffer, delay, max, restarts int
	err := s.app.DB.QueryRow(ctx, `SELECT c.name, s.uuid, c.source, c.args, c.filters, c.credentials, c.execution_environment_id,
			c.instance_group_id, c.python, c.buffer_size, c.execution_node, c.organization_id, c.restart_policy, c.restart_delay,
			c.max_restarts, c.restart_count
		FROM event_connectors c JOIN event_streams s ON s.id = c.event_stream_id
		WHERE c.id = $1 AND c.generation = $2 AND c.controller_node = $3`, id, gen, s.node).
		Scan(&name, &uuid, &source, &args, &filters, &creds, &ee, &ig, &python, &buffer, &exe, &org, &policy, &delay, &max, &restarts)
	if err != nil {
		return nil, err
	}
	l := &connectorLaunch{exe: exe, policy: policy, delay: delay, max: max, restarts: restarts, runtime: s.app.Cfg.ContainerRuntime}
	if exe == s.node {
		l.exe = s.node
	}
	if args == nil {
		args = map[string]any{}
	}
	l.cfg = connectors.Config{Runner: connectors.Runner, Connector: id, Name: name, PostPath: ReceiverPath(uuid), Source: source,
		Args: args, Filters: filters, Buffer: buffer, Batch: connectorBatch, Env: map[string]any{}}
	for _, cid := range creds {
		rc, err := ResolveCredential(ctx, s.app, cid)
		if err != nil {
			return l, fmt.Errorf("credential %d: %w", cid, err)
		}
		if err := injectConnectorCredential(&l.cfg, rc); err != nil {
			return l, fmt.Errorf("credential %q: %w", rc.Name, err)
		}
	}
	l.spec = agent.WorkSpec{Ident: fmt.Sprintf("connector_%d_%d", id, gen), Kind: agent.KindConnector, Python: python}
	if err := s.connectorImage(ctx, l, ee, org); err != nil {
		return l, err
	}
	if exe == "" {
		pod, err := s.connectorPod(ctx, id, ig)
		if err != nil {
			return l, err
		}
		pod.Spec = l.spec
		l.pod = pod
	}
	return l, nil
}

func (s *ConnectorSupervisor) connectorImage(ctx context.Context, l *connectorLaunch, ee *int64, org int64) error {
	if ee == nil {
		s.app.DB.QueryRow(ctx, "SELECT default_environment_id FROM organizations WHERE id = $1", org).Scan(&ee)
	}
	l.spec.Image = s.app.Cfg.DefaultEEImage
	if ee == nil {
		return nil
	}
	var credID *int64
	if err := s.app.DB.QueryRow(ctx, "SELECT image, pull, credential_id FROM execution_environments WHERE id = $1", *ee).
		Scan(&l.spec.Image, &l.spec.Pull, &credID); err != nil {
		return errors.New("The connector's execution environment no longer exists.")
	}
	if credID != nil {
		rc, err := ResolveCredential(ctx, s.app, *credID)
		if err != nil {
			return err
		}
		l.spec.RegistryHost, _ = rc.Inputs["host"].(string)
		l.spec.RegistryUser, _ = rc.Inputs["username"].(string)
		l.spec.RegistryPassword, _ = rc.Inputs["password"].(string)
	}
	return nil
}

func (s *ConnectorSupervisor) connectorPod(ctx context.Context, id int64, ig *int64) (*agent.PodRequest, error) {
	if ig == nil {
		return nil, errors.New("The connector has no container group.")
	}
	var podSpec string
	var credID *int64
	if err := s.app.DB.QueryRow(ctx, "SELECT pod_spec_override, credential_id FROM instance_groups WHERE id = $1", *ig).Scan(&podSpec, &credID); err != nil {
		return nil, errors.New("The connector's container group no longer exists.")
	}
	req := &agent.PodRequest{ConnectorID: id, PodSpec: podSpec, ServiceURL: s.app.Cfg.ServiceURL, ShimImage: s.app.Cfg.ShimImage}
	s.app.DB.QueryRow(ctx, "SELECT value #>> '{}' FROM settings WHERE key = 'INSTALL_UUID'").Scan(&req.InstallID)
	if credID != nil {
		cred, err := ResolveCredential(ctx, s.app, *credID)
		if err != nil {
			return nil, err
		}
		in := func(k string) string { v, _ := cred.Inputs[k].(string); return v }
		verify, ok := cred.Inputs["verify_ssl"].(bool)
		req.Credential = &agent.KubeCredential{Host: in("host"), BearerToken: in("bearer_token"), CACert: in("ssl_ca_cert"), VerifySSL: verify || !ok}
	}
	return req, nil
}

// injectConnectorCredential gives a credential to the source as its
// type's injectors say: env to its environment, extra_vars to its
// arguments, files to files the runner writes.
func injectConnectorCredential(cfg *connectors.Config, rc *ResolvedCredential) error {
	inj := rc.Injectors
	if inj == nil {
		return nil
	}
	vars := credtypes.Vars(rc.Schema, rc.Inputs)
	files := map[string]string{}
	if fm, ok := inj["file"].(map[string]any); ok {
		for k, v := range fm {
			name := strings.TrimPrefix(strings.TrimPrefix(k, "template"), ".")
			content, err := credtypes.Render(str(v), vars, files)
			if err != nil {
				return err
			}
			ph := fmt.Sprintf("@@AWXNEXT_FILE_%d_%s@@", rc.ID, name)
			cfg.Files = append(cfg.Files, connectors.File{Placeholder: ph, Content: content})
			files[name] = ph
		}
	}
	if em, ok := inj["env"].(map[string]any); ok {
		for k, v := range em {
			val, err := credtypes.Render(str(v), vars, files)
			if err != nil {
				return err
			}
			cfg.Env[k] = val
		}
	}
	if xm, ok := inj["extra_vars"].(map[string]any); ok {
		for k, v := range xm {
			if _, set := cfg.Args[k]; set {
				continue
			}
			val, err := credtypes.RenderAny(v, vars, files)
			if err != nil {
				return err
			}
			cfg.Args[k] = val
		}
	}
	return nil
}

func (s *ConnectorSupervisor) start(ctx context.Context, l *connectorLaunch) (connectorWorker, error) {
	switch {
	case l.pod != nil:
		if agent.Default == nil {
			return nil, errors.New("container groups are not available on this node")
		}
		w, err := agent.Default.StartPod(ctx, *l.pod)
		if err != nil {
			return nil, err
		}
		return asConnectorWorker(w)
	case l.exe != s.node:
		if agent.Default == nil {
			return nil, errors.New("remote execution is not available on this node")
		}
		w, err := agent.Default.StartWorker(ctx, l.exe, l.spec)
		if err != nil {
			return nil, err
		}
		return asConnectorWorker(w)
	}
	return s.startLocal(ctx, l)
}

func asConnectorWorker(w agent.Worker) (connectorWorker, error) {
	cw, ok := w.(connectorWorker)
	if !ok {
		w.Kill()
		return nil, errors.New("the worker gives no exit code")
	}
	return cw, nil
}

// connectorSiteURL is how a connector on this node reaches awxnext: the
// node itself for a process, the service URL from a container, which
// loopback does not reach.
func (s *ConnectorSupervisor) connectorSiteURL(runtime string) string {
	cfg := s.app.Cfg
	if runtime == "none" || runtime == "process" || runtime == "" {
		if cfg.InternalURL != "" {
			return cfg.InternalURL
		}
		if u := loopbackURL(cfg.Listen); u != "" {
			return u
		}
	}
	if cfg.ServiceURL != "" {
		return cfg.ServiceURL
	}
	return cfg.BaseURL
}

// loopbackURL is the plain HTTP URL of a listen address, on loopback when
// it listens on every address: awxnext ends no TLS itself.
func loopbackURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func (s *ConnectorSupervisor) startLocal(ctx context.Context, l *connectorLaunch) (connectorWorker, error) {
	spec := runnercmd.Spec{Runtime: l.runtime, Image: l.spec.Image, Pull: l.spec.Pull, RegistryHost: l.spec.RegistryHost,
		RegistryUser: l.spec.RegistryUser, RegistryPassword: l.spec.RegistryPassword, Name: "awxnext_" + l.spec.Ident}
	args, cleanup, err := runnercmd.Run(spec, connectors.Argv(l.spec.Python))
	if err != nil {
		return nil, err
	}
	env := connectors.Env()
	w := &localConnector{spec: spec}
	if iso := jobexec.Default; iso != nil && (l.runtime == "none" || l.runtime == "process" || l.runtime == "") {
		u, err := iso.Acquire(ctx)
		if err != nil {
			cleanup()
			return nil, err
		}
		dir := iso.Dir(l.spec.Ident)
		args, env, w.jobUser = iso.Command(u, dir, args), jobexec.Env(env, u, dir), true
		done := cleanup
		cleanup = func() { done(); iso.Release(u) }
	}
	w.cleanup = cleanup
	w.cmd = exec.Command(args[0], args[1:]...)
	w.cmd.Env = env
	if w.stdin, err = w.cmd.StdinPipe(); err != nil {
		cleanup()
		return nil, err
	}
	if w.stdout, err = w.cmd.StdoutPipe(); err != nil {
		cleanup()
		return nil, err
	}
	w.stderr = &limitedBuffer{max: connectorLogTail}
	w.cmd.Stderr = w.stderr
	if err := w.cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("starting %s: %w", args[0], err)
	}
	w.stdin.Write(connectors.Site{BaseURL: s.connectorSiteURL(l.runtime)}.Line())
	return w, nil
}

type localConnector struct {
	cmd     *exec.Cmd
	spec    runnercmd.Spec
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  *limitedBuffer
	cleanup func()
	jobUser bool
}

func (l *localConnector) Stdin() io.WriteCloser { return l.stdin }
func (l *localConnector) Stdout() io.Reader     { return l.stdout }
func (l *localConnector) Kill() {
	if l.jobUser && l.cmd.Process != nil {
		// The helper stops the job user's processes on SIGTERM.
		l.cmd.Process.Signal(syscall.SIGTERM)
		p := l.cmd.Process
		time.AfterFunc(30*time.Second, func() { p.Kill() })
		return
	}
	runnercmd.Kill(l.spec, l.cmd)
}
func (l *localConnector) Wait() (string, error) {
	err := l.cmd.Wait()
	l.cleanup()
	return strings.TrimSpace(l.stderr.String()), err
}
func (l *localConnector) ExitCode() int {
	if l.cmd.ProcessState == nil {
		return -1
	}
	return l.cmd.ProcessState.ExitCode()
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// connectorOutput is a run's log tail and last status.
type connectorOutput struct {
	mu     sync.Mutex
	log    []byte
	status *connectors.Status
	dirty  bool
}

func (o *connectorOutput) line(l string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dirty = true
	if st, ok := connectors.ParseStatus(l); ok {
		o.status = &st
		return
	}
	o.log = append(o.log, l...)
	o.log = append(o.log, '\n')
	if len(o.log) > connectorLogTail {
		cut := len(o.log) - connectorLogTail
		if i := bytes.IndexByte(o.log[cut:], '\n'); i >= 0 {
			cut += i + 1
		}
		o.log = append([]byte(nil), o.log[cut:]...)
	}
}

func (o *connectorOutput) snapshot() (string, []byte, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	dirty := o.dirty
	o.dirty = false
	var stats []byte
	if o.status != nil {
		stats, _ = json.Marshal(o.status)
	}
	return string(o.log), stats, dirty
}

// lastLine is the log's last line, for the run's explanation.
func (o *connectorOutput) lastLine() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(o.log)), "\n")
	return logStamp.ReplaceAllString(lines[len(lines)-1], "")
}

// logStamp is the time the runner puts before its own lines.
var logStamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ `)

// watch sends the configuration, streams the log until the run ends, and
// returns its exit code; lost is set when the connection to its node was.
func (s *ConnectorSupervisor) watch(ctx context.Context, r *connectorRun, runID int64, w connectorWorker, l *connectorLaunch, out *connectorOutput) (int, bool, error) {
	line, err := l.cfg.Line()
	if err == nil {
		_, err = w.Stdin().Write(line)
	}
	w.Stdin().Close()
	if err != nil {
		w.Kill()
		w.Wait()
		return -1, false, err
	}
	stop := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				w.Kill()
				<-stop
				return
			case <-stop:
				return
			case <-t.C:
				s.flush(context.WithoutCancel(ctx), r, runID, out)
			}
		}
	}()
	readLines(w.Stdout(), out.line)
	diag, werr := w.Wait()
	close(stop)
	<-flushed
	rc := w.ExitCode()
	if diag != "" {
		out.line(diag)
	}
	if rc < 0 && werr != nil {
		return rc, strings.Contains(werr.Error(), "lost connection") || strings.Contains(werr.Error(), "acknowledge"), werr
	}
	return rc, false, nil
}

// readLines calls fn with each line of r, a line cut at 16 KiB.
func readLines(r io.Reader, fn func(string)) {
	br := bufio.NewReaderSize(r, 16<<10)
	var long bool
	for {
		b, err := br.ReadSlice('\n')
		if len(b) > 0 && !long {
			fn(strings.TrimRight(string(b), "\r\n"))
		}
		long = errors.Is(err, bufio.ErrBufferFull)
		if err != nil && !long {
			return
		}
	}
}

func (s *ConnectorSupervisor) flush(ctx context.Context, r *connectorRun, runID int64, out *connectorOutput) {
	log, stats, dirty := out.snapshot()
	if !dirty {
		return
	}
	s.app.DB.Exec(ctx, "UPDATE event_connector_runs SET log = $2, stats = coalesce($3::jsonb, stats), modified = now() WHERE id = $1", runID, log, nullableJSON(stats))
	if stats != nil {
		s.app.DB.Exec(ctx, "UPDATE event_connectors SET stats = $4 WHERE id = $1 AND generation = $2 AND controller_node = $3", r.id, r.gen, s.node, stats)
	}
}

func nullableJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

func (s *ConnectorSupervisor) finishRun(ctx context.Context, runID int64, status string, rc *int, why string, out *connectorOutput) {
	log, stats, _ := out.snapshot()
	// The leader may have marked the run lost already: its verdict stays.
	s.app.DB.Exec(ctx, `UPDATE event_connector_runs SET status = CASE WHEN finished IS NULL THEN $2 ELSE status END,
		explanation = CASE WHEN finished IS NULL THEN $4 ELSE explanation END, rc = $3, log = $5, stats = coalesce($6::jsonb, stats),
		finished = coalesce(finished, now()), modified = now() WHERE id = $1`, runID, status, rc, why, log, nullableJSON(stats))
}

// finished records how the run ended and applies the restart policy.
func (s *ConnectorSupervisor) finished(ctx context.Context, r *connectorRun, l *connectorLaunch, runID int64, started time.Time, rc int, lost bool, err error, out *connectorOutput) {
	s.flush(ctx, r, runID, out)
	var rcp *int
	if rc >= 0 {
		rcp = &rc
	}
	fence := " WHERE id = $1 AND generation = $2 AND controller_node = $3"
	var current bool
	s.app.DB.QueryRow(ctx, "SELECT true FROM event_connectors"+fence, r.id, r.gen, s.node).Scan(&current)
	if !current {
		s.finishRun(ctx, runID, "stopped", rcp, "The connector was changed, stopped or moved.", out)
		return
	}
	if lost {
		why := fmt.Sprintf("Lost the connection to node %s: moving to another node.", l.exe)
		s.finishRun(ctx, runID, "lost", rcp, why, out)
		s.app.DB.Exec(ctx, `UPDATE event_connectors SET status = 'pending', status_explanation = $4, controller_node = '', execution_node = '',
			token_hash = '', next_start = NULL`+fence, r.id, r.gen, s.node, why)
		return
	}
	failed := err != nil || rc != 0
	var why string
	switch {
	case err != nil:
		why = err.Error()
	case rc == connectors.ExitConfig:
		why = "The connector could not start: " + out.lastLine()
	case rc == connectors.ExitRefused:
		why = "awxnext refused the connector's token."
	case rc != 0:
		why = fmt.Sprintf("The source failed (exit code %d): %s", rc, out.lastLine())
	default:
		why = "The source ended."
	}
	status := "completed"
	if failed {
		status = "failed"
	}
	s.finishRun(ctx, runID, status, rcp, why, out)
	if l == nil {
		l = &connectorLaunch{policy: restartOnFailure, delay: 5}
		s.app.DB.QueryRow(ctx, "SELECT restart_policy, restart_delay, max_restarts, restart_count FROM event_connectors WHERE id = $1", r.id).
			Scan(&l.policy, &l.delay, &l.max, &l.restarts)
	}
	restart := l.policy == restartAlways || (l.policy == restartOnFailure && failed)
	if !restart {
		s.app.DB.Exec(ctx, `UPDATE event_connectors SET status = $4, status_explanation = $5, controller_node = '', execution_node = '',
			token_hash = '', next_start = NULL`+fence, r.id, r.gen, s.node, status, why)
		return
	}
	count := l.restarts
	if time.Since(started) > connectorStableRun {
		count = 0
	}
	count++
	if l.max > 0 && count > l.max {
		s.app.DB.Exec(ctx, `UPDATE event_connectors SET status = 'failed', status_explanation = $4, controller_node = '', execution_node = '',
			token_hash = '', next_start = NULL`+fence, r.id, r.gen, s.node, fmt.Sprintf("%s Stopped after %d restarts in a row.", sentence(why), l.max))
		return
	}
	delay := time.Duration(l.delay) * time.Second << min(count-1, 6)
	s.app.DB.Exec(ctx, `UPDATE event_connectors SET status = 'restarting', status_explanation = $4, controller_node = '', execution_node = '',
		token_hash = '', next_start = now() + $5::float8 * interval '1 millisecond', restart_count = $6`+fence,
		r.id, r.gen, s.node, fmt.Sprintf("%s Restarting in %s.", sentence(why), delay), delay.Milliseconds(), count)
}

// sentence ends s with a full stop when it has none, as the source's last
// line often has not.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsRune(".!?", rune(s[len(s)-1])) {
		return s
	}
	return s + "."
}
