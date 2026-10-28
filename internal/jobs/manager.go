// Package jobs runs unified jobs: a leader-elected dispatcher assigns pending
// jobs to nodes by capacity, and every node claims and runs the jobs assigned
// to it, streaming events into PostgreSQL.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/metrics"
	"github.com/damhau/awxnext/internal/tracing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
)

// Row holds the raw columns of a unified_jobs row.
type Row map[string]any

func (r Row) ID() int64           { id, _ := api.ID(r["id"]); return id }
func (r Row) Int(k string) int64  { v, _ := api.ID(r[k]); return v }
func (r Row) Str(k string) string { s, _ := r[k].(string); return s }
func (r Row) Bool(k string) bool  { b, _ := r[k].(bool); return b }
func (r Row) Has(k string) bool   { _, ok := api.ID(r[k]); return ok }
func (r Row) Time(k string) time.Time {
	t, _ := r[k].(time.Time)
	return t
}

// Result is what a runner reports when a job ends.
type Result struct {
	Status      string // successful, failed, error, canceled
	Explanation string
	Traceback   string
	// Columns of unified_jobs to update (e.g. scm_revision, artifacts).
	Update map[string]any
}

// RunContext is passed to runners.
type RunContext struct {
	M   *Manager
	Job Row
	Out *Output
}

// Runner executes a job type.
type Runner interface {
	Run(ctx context.Context, rc *RunContext) Result
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, rc *RunContext) Result

func (f RunnerFunc) Run(ctx context.Context, rc *RunContext) Result { return f(ctx, rc) }

// TypeSpec describes how a unified job type is scheduled and run.
type TypeSpec struct {
	Type string
	// TemplateType is the unified job template type launching it (for status and concurrency).
	TemplateType string
	Runner       Runner
	// Dependencies creates jobs that must finish first (e.g. project updates).
	Dependencies func(ctx context.Context, m *Manager, job Row) ([]int64, error)
	// Impact is the capacity the job consumes (default 1).
	Impact func(ctx context.Context, m *Manager, job Row) int
	// ControlPlane jobs run on control/hybrid nodes (native tasks like git sync).
	ControlPlane bool
	// InDispatcher jobs (workflows) are driven by the dispatcher, not run on a node.
	InDispatcher bool
	// Serial: one job per template at a time unless allow_simultaneous.
	Serial bool
	// Finished runs after the job reached a final status, inside no transaction.
	Finished func(ctx context.Context, m *Manager, job Row)
}

var (
	specsMu sync.RWMutex
	specs   = map[string]*TypeSpec{}
)

// Register adds or replaces a job type.
func Register(s *TypeSpec) {
	specsMu.Lock()
	specs[s.Type] = s
	specsMu.Unlock()
}

func spec(t string) *TypeSpec {
	specsMu.RLock()
	defer specsMu.RUnlock()
	return specs[t]
}

// Manager coordinates dispatching and running on this node.
type Manager struct {
	App  *api.App
	Node string
	Hub  *Hub

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	wg      sync.WaitGroup

	wakeDispatch chan struct{}
	wakeWork     chan struct{}
	// TickHooks run on every dispatcher tick on the leader (schedules, workflows).
	TickHooks []func(ctx context.Context, m *Manager)
	// StartedHooks run when a job starts running on this node; FinishedHooks
	// after it reached a final status (notifications, workflows).
	StartedHooks  []func(ctx context.Context, m *Manager, job Row)
	FinishedHooks []func(ctx context.Context, m *Manager, job Row)
	leader        bool
}

func New(app *api.App) *Manager {
	m := &Manager{
		App:          app,
		Node:         app.Cfg.NodeName,
		Hub:          NewHub(),
		running:      map[int64]context.CancelFunc{},
		wakeDispatch: make(chan struct{}, 1),
		wakeWork:     make(chan struct{}, 1),
	}
	if app.Settings != nil {
		m.Hub.EventRate = func() int { return app.Settings.Int(context.Background(), "MAX_WEBSOCKET_EVENT_RATE") }
	}
	return m
}

func (m *Manager) DB() *pgxpool.Pool { return m.App.DB }

func poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Start runs the listener, dispatcher election, worker and heartbeat until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	m.FailOrphans(ctx)
	go m.listen(ctx)
	go m.leaderLoop(ctx)
	go m.workerLoop(ctx)
	go m.heartbeat(ctx)
}

// Wait blocks until running jobs finish (used on shutdown and in tests).
func (m *Manager) Wait() { m.wg.Wait() }

// --- notifications ---

const (
	chDispatch = "awx_dispatch"
	chWork     = "awx_work"
	chCancel   = "awx_cancel"
	chStatus   = "awx_status"
	chEvents   = "awx_events"
)

// Notify sends a PostgreSQL notification.
func (m *Manager) Notify(ctx context.Context, channel, payload string) {
	if _, err := m.DB().Exec(ctx, "SELECT pg_notify($1, $2)", channel, payload); err != nil {
		slog.Warn("notify failed", "channel", channel, "err", err)
	}
}

func (m *Manager) listen(ctx context.Context) {
	for ctx.Err() == nil {
		if err := m.listenOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("notification listener restarting", "err", err)
			time.Sleep(2 * time.Second)
		}
	}
}

func (m *Manager) listenOnce(ctx context.Context) error {
	pc, err := m.DB().Acquire(ctx)
	if err != nil {
		return err
	}
	conn := pc.Hijack()
	defer conn.Close(context.Background())
	for _, ch := range []string{chDispatch, chWork, chCancel, chStatus, chEvents} {
		if _, err := conn.Exec(ctx, "LISTEN "+ch); err != nil {
			return err
		}
	}
	// Catch up on anything missed while not listening.
	poke(m.wakeDispatch)
	poke(m.wakeWork)
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		switch n.Channel {
		case chDispatch:
			poke(m.wakeDispatch)
		case chWork:
			poke(m.wakeWork)
		case chCancel:
			if id, err := strconv.ParseInt(n.Payload, 10, 64); err == nil {
				m.cancelLocal(id)
			}
		case chStatus:
			m.Hub.fromNotification(n.Channel, n.Payload, m.Node)
		case chEvents:
			m.relayEvents(ctx, n.Payload)
		}
	}
}

// --- leader / dispatcher ---

const leaderLockKey = 727276

func (m *Manager) leaderLoop(ctx context.Context) {
	for ctx.Err() == nil {
		pc, err := m.DB().Acquire(ctx)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		conn := pc.Hijack()
		var got bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", leaderLockKey).Scan(&got); err != nil || !got {
			conn.Close(context.Background())
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		slog.Info("became dispatcher leader", "node", m.Node)
		m.setLeader(true)
		m.dispatchLoop(ctx, conn)
		m.setLeader(false)
		conn.Close(context.Background())
	}
}

func (m *Manager) setLeader(v bool) {
	m.mu.Lock()
	m.leader = v
	m.mu.Unlock()
}

// IsLeader reports whether this node currently runs the dispatcher.
func (m *Manager) IsLeader() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leader
}

func (m *Manager) dispatchLoop(ctx context.Context, lockConn *pgx.Conn) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var lastMaint time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wakeDispatch:
		}
		// Losing the lock connection means losing leadership.
		if err := lockConn.Ping(ctx); err != nil {
			slog.Warn("lost dispatcher leadership", "err", err)
			return
		}
		if time.Since(lastMaint) > 10*time.Minute {
			m.maintain(ctx)
			lastMaint = time.Now()
		}
		m.Tick(ctx)
	}
}

// Tick runs one dispatcher pass (exported for tests).
func (m *Manager) Tick(ctx context.Context) {
	start := time.Now()
	defer func() { tickDuration.With().Observe(time.Since(start).Seconds()) }()
	for _, h := range m.TickHooks {
		h(ctx, m)
	}
	if err := m.dispatchPending(ctx); err != nil && ctx.Err() == nil {
		slog.Error("dispatch failed", "err", err)
	}
	m.reap(ctx)
}

// --- worker ---

func (m *Manager) workerLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		m.claim(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wakeWork:
		}
	}
}

func (m *Manager) claim(ctx context.Context) {
	for ctx.Err() == nil {
		var id int64
		err := m.DB().QueryRow(ctx, `UPDATE unified_jobs SET status = 'running', started = now(), modified = now()
			WHERE id = (SELECT id FROM unified_jobs WHERE status = 'waiting' AND controller_node = $1
				ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1)
			RETURNING id`, m.Node).Scan(&id)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
				slog.Error("claiming job", "err", err)
			}
			return
		}
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.run(ctx, id)
		}()
	}
}

// LoadRow reads all columns of a unified job.
func LoadRow(ctx context.Context, db *pgxpool.Pool, id int64) (Row, error) {
	rows, err := db.Query(ctx, "SELECT * FROM unified_jobs WHERE id = $1", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, api.ErrNotFound
	}
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	row := Row{}
	for i, fd := range rows.FieldDescriptions() {
		v := vals[i]
		if n, ok := v.(int32); ok {
			v = int64(n)
		}
		row[fd.Name] = v
	}
	return row, rows.Err()
}

func (m *Manager) run(parent context.Context, id int64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.mu.Lock()
	m.running[id] = cancel
	m.mu.Unlock()
	jobsRunning.With().Add(1)
	defer func() {
		m.mu.Lock()
		delete(m.running, id)
		m.mu.Unlock()
		jobsRunning.With().Add(-1)
	}()
	// Shutting down the node does not kill jobs abruptly; they are reaped if the node never returns.
	_ = parent

	row, err := LoadRow(ctx, m.DB(), id)
	if err != nil {
		slog.Error("loading job", "job", id, "err", err)
		return
	}
	if row.Bool("cancel_flag") {
		cancel()
	}
	m.publishStatus(ctx, row, "running")
	if ujt := row.Int("unified_job_template_id"); ujt != 0 {
		m.DB().Exec(ctx, "UPDATE unified_job_templates SET current_job_id = $2, status = 'running' WHERE id = $1", ujt, id)
	}
	for _, h := range m.StartedHooks {
		h(ctx, m, row)
	}
	sp := spec(row.Str("type"))
	if sp == nil || sp.Runner == nil {
		m.finish(context.Background(), row, Result{Status: "error", Explanation: "No runner for job type " + row.Str("type")})
		return
	}
	ctx, span := tracing.StartSpan(ctx, "job "+row.Str("type"), attribute.Int64("awx.job.id", id), attribute.String("awx.job.type", row.Str("type")),
		attribute.String("awx.job.name", row.Str("name")), attribute.Int64("awx.unified_job_template.id", row.Int("unified_job_template_id")))
	defer func() {
		final, _ := LoadRow(context.Background(), m.DB(), id)
		span.SetAttributes(attribute.String("awx.job.status", final.Str("status")))
		var err error
		if s := final.Str("status"); s != "successful" {
			err = fmt.Errorf("job %s: %s", s, final.Str("job_explanation"))
		}
		tracing.EndSpan(span, err)
	}()
	out := NewOutput(m.DB(), id, row.Time("created"), m.hostIDs(ctx, row), func(e Event) { m.Hub.publishEvent(row, e) },
		func(maxCounter int) {
			// Viewers connected to other nodes fetch stored events when notified.
			m.Notify(context.Background(), chEvents, fmt.Sprintf("%s|%s|%d|%d", m.Node, row.Str("type"), id, maxCounter))
		})
	var res Result
	func() {
		defer func() {
			if p := recover(); p != nil {
				res = Result{Status: "error", Explanation: fmt.Sprint(p), Traceback: string(debug.Stack())}
			}
		}()
		res = sp.Runner.Run(ctx, &RunContext{M: m, Job: row, Out: out})
	}()
	if err := out.Close(); err != nil && res.Status == "successful" {
		res = Result{Status: "error", Explanation: "Failed to store job events: " + err.Error()}
	}
	if ctx.Err() != nil && res.Status != "successful" {
		res.Status = "canceled"
		if res.Explanation == "" {
			res.Explanation = "Job was canceled."
		}
	}
	m.finish(context.Background(), row, res)
}

func (m *Manager) hostIDs(ctx context.Context, row Row) map[string]int64 {
	out := map[string]int64{}
	inv := row.Int("inventory_id")
	if inv == 0 {
		return out
	}
	rows, err := m.DB().Query(ctx, `SELECT name, id FROM hosts WHERE inventory_id = $1
		UNION ALL SELECT h.name, h.id FROM hosts h WHERE h.id IN (SELECT host_id FROM smart_inventory_hosts($1))`, inv)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var id int64
		rows.Scan(&name, &id)
		out[name] = id
	}
	return out
}

var (
	jobsFinished  = metrics.Default.NewCounter("awxnext_jobs_finished_total", "Jobs reaching a final status, by type and status.", "type", "status")
	jobsRunning   = metrics.Default.NewGauge("awxnext_jobs_running", "Jobs running on this node.")
	tickDuration  = metrics.Default.NewHistogram("awxnext_dispatcher_tick_seconds", "Duration of dispatcher passes on the leader.", nil)
	eventsWritten = metrics.Default.NewCounter("awxnext_job_events_written_total", "Job events stored.")
)

// Complete records the final status of a job that is not run by a node
// (workflow jobs, approvals): template status, hooks and websocket updates.
func (m *Manager) Complete(ctx context.Context, id int64, res Result) error {
	row, err := LoadRow(ctx, m.DB(), id)
	if err != nil {
		return err
	}
	m.finish(ctx, row, res)
	return nil
}

// finish records the final status of a job and updates its template.
func (m *Manager) finish(ctx context.Context, row Row, res Result) {
	id := row.ID()
	if res.Status == "" {
		res.Status = "error"
	}
	failed := res.Status != "successful"
	q := `UPDATE unified_jobs SET status = $2, failed = $3, finished = now(), modified = now(),
		elapsed = coalesce(extract(epoch FROM now() - started), 0), job_explanation = $4, result_traceback = $5`
	args := []any{id, res.Status, failed, res.Explanation, res.Traceback}
	for k, v := range res.Update {
		args = append(args, v)
		q += fmt.Sprintf(", %s = $%d", pgx.Identifier{k}.Sanitize(), len(args))
	}
	q += " WHERE id = $1"
	if _, err := m.DB().Exec(ctx, q, args...); err != nil {
		slog.Error("finishing job", "job", id, "err", err)
	}
	jobsFinished.With(row.Str("type"), res.Status).Inc()
	if ujt := row.Int("unified_job_template_id"); ujt != 0 {
		m.DB().Exec(ctx, `UPDATE unified_job_templates SET last_job_id = $2, last_job_failed = $3, last_job_run = now(), status = $4,
			current_job_id = CASE WHEN current_job_id = $2 THEN NULL ELSE current_job_id END WHERE id = $1`, ujt, id, failed, res.Status)
	}
	final, err := LoadRow(ctx, m.DB(), id)
	if err != nil {
		final = row
	}
	if sp := spec(row.Str("type")); sp != nil && sp.Finished != nil {
		sp.Finished(ctx, m, final)
	}
	for _, h := range m.FinishedHooks {
		h(ctx, m, final)
	}
	m.publishStatus(ctx, final, res.Status)
	m.Notify(ctx, chDispatch, "")
}

func (m *Manager) publishStatus(ctx context.Context, row Row, status string) {
	msg := map[string]any{
		"group_name":              "jobs",
		"type":                    row.Str("type"),
		"unified_job_id":          row.ID(),
		"unified_job_template_id": row["unified_job_template_id"],
		"status":                  status,
		"finished":                row["finished"],
	}
	// Jobs spawned by a workflow also update viewers of that workflow.
	var wfJob, wfNode int64
	if err := m.DB().QueryRow(ctx, "SELECT workflow_job_id, node_id FROM workflow_node_jobs WHERE job_id = $1", row.ID()).Scan(&wfJob, &wfNode); err == nil {
		msg["workflow_job_id"], msg["workflow_node_id"] = wfJob, wfNode
	}
	b, _ := json.Marshal(msg)
	m.Hub.Publish("jobs-status_changed", b)
	m.Hub.publishWorkflow(b)
	m.Notify(ctx, chStatus, m.Node+"|"+string(b))
}

// --- cancel ---

// Cancel requests cancellation. Pending and waiting jobs are canceled immediately.
func (m *Manager) Cancel(ctx context.Context, id int64) error {
	var status string
	err := m.DB().QueryRow(ctx, `UPDATE unified_jobs SET cancel_flag = true WHERE id = $1 RETURNING status`, id).Scan(&status)
	if err != nil {
		return err
	}
	switch status {
	case "new", "pending", "waiting":
		row, err := LoadRow(ctx, m.DB(), id)
		if err != nil {
			return err
		}
		tag, err := m.DB().Exec(ctx, `UPDATE unified_jobs SET status = 'canceled', canceled_on = now(), finished = now(), failed = true
			WHERE id = $1 AND status IN ('new', 'pending', 'waiting')`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if ujt := row.Int("unified_job_template_id"); ujt != 0 {
				m.DB().Exec(ctx, `UPDATE unified_job_templates SET last_job_id = $2, last_job_failed = true, status = 'canceled' WHERE id = $1`, ujt, id)
			}
			if final, err := LoadRow(ctx, m.DB(), id); err == nil {
				row = final
			}
			for _, h := range m.FinishedHooks {
				h(ctx, m, row)
			}
			m.publishStatus(ctx, row, "canceled")
			m.Notify(ctx, chDispatch, "")
			return nil
		}
	}
	m.DB().Exec(ctx, "UPDATE unified_jobs SET canceled_on = now() WHERE id = $1", id)
	m.cancelLocal(id)
	m.Notify(ctx, chCancel, strconv.FormatInt(id, 10))
	return nil
}

func (m *Manager) cancelLocal(id int64) {
	m.mu.Lock()
	cancel := m.running[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// --- liveness ---

func (m *Manager) heartbeat(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		m.DB().Exec(ctx, "UPDATE instances SET last_seen = now(), node_state = 'ready' WHERE hostname = $1", m.Node)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// processless lists job types without a process (workflows): a restart or a
// lost node does not interrupt them, the dispatcher keeps driving them.
func processless() []string {
	specsMu.RLock()
	defer specsMu.RUnlock()
	out := []string{}
	for t, s := range specs {
		if s.InDispatcher {
			out = append(out, t)
		}
	}
	return out
}

// FailOrphans fails jobs this node was running before it restarted.
func (m *Manager) FailOrphans(ctx context.Context) {
	rows, err := m.DB().Query(ctx, `SELECT id FROM unified_jobs WHERE controller_node = $1 AND status IN ('waiting', 'running')
		AND type <> ALL($2)`, m.Node, processless())
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if row, err := LoadRow(ctx, m.DB(), id); err == nil {
			m.finish(ctx, row, Result{Status: "failed", Explanation: "Task was marked as running at system start up. The system must have not shut down properly, so it has been marked as failed."})
		}
	}
}

// reap fails jobs whose controlling node stopped sending heartbeats.
func (m *Manager) reap(ctx context.Context) {
	rows, err := m.DB().Query(ctx, `SELECT j.id FROM unified_jobs j JOIN instances i ON i.hostname = j.controller_node
		WHERE j.status IN ('waiting', 'running') AND i.last_seen < now() - interval '2 minutes' AND j.type <> ALL($1)`, processless())
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if row, err := LoadRow(ctx, m.DB(), id); err == nil {
			m.finish(ctx, row, Result{Status: "failed", Explanation: "Job terminated due to node loss: " + row.Str("controller_node") + " stopped responding."})
		}
	}
}

// maintain creates upcoming daily partitions for job events and archives old ones.
func (m *Manager) maintain(ctx context.Context) {
	now := time.Now().UTC()
	for d := 0; d < 3; d++ {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, d)
		name := "job_events_" + day.Format("20060102")
		_, err := m.DB().Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF job_events FOR VALUES FROM ('%s') TO ('%s')`,
			name, day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339)))
		if err != nil {
			// Rows for this day already landed in the default partition; keep using it.
			slog.Debug("event partition", "name", name, "err", err)
		}
	}
	if err := m.ArchiveEvents(ctx, m.App.Settings.Int(ctx, "JOB_EVENTS_HOT_DAYS")); err != nil && ctx.Err() == nil {
		slog.Error("archiving job events", "err", err)
	}
}
