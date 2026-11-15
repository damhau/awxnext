package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/rules"
	"github.com/damhau/awxnext/internal/tracing"
)

// The rule engine evaluates each stream's events in order, on one node at
// a time: a node holds a stream while it holds the stream's advisory lock,
// on a connection of its own, as the dispatcher leader holds its lock.
// Each batch is one transaction: it locks the stream's position, decides
// each event for each rule, acts, and saves the position. A batch that
// doesn't commit leaves nothing behind, and the next holder starts where
// the last commit stopped; a decision is unique per rule and event, so an
// event read again never acts twice.

const (
	rulesConsumer = "rules"
	rulesChannel  = "awxnext_rules"
	// ruleLockClass is the first key of the streams' advisory locks; the
	// second is the stream id.
	ruleLockClass = 727277
	// maxPostDepth bounds how many rules an event can go through, as one
	// rule's posted event matches another's filter.
	maxPostDepth   = 8
	depthExtension = "awxnextdepth"
	// passBatches bounds the batches of one stream in a pass, so that one
	// busy stream doesn't hold back the others.
	passBatches = 20
)

// Rules is the rule engine of this node, once StartJobs has started it.
var Rules *RuleEngine

// RuleEngine evaluates event rules on this node.
type RuleEngine struct {
	app  *api.App
	node string
	// BatchSize is how many events a batch reads.
	BatchSize int

	mu   sync.Mutex
	conn *pgx.Conn
	held map[int64]bool

	beforeCommit atomic.Pointer[func(stream int64) error]
}

// NewRuleEngine makes an engine that decides as node.
func NewRuleEngine(app *api.App, node string) *RuleEngine {
	return &RuleEngine{app: app, node: node, BatchSize: 200, held: map[int64]bool{}}
}

// SetBeforeCommit sets a function each batch calls before it commits; its
// error abandons the batch, as a node failing then would (tests).
func (e *RuleEngine) SetBeforeCommit(fn func(stream int64) error) {
	if fn == nil {
		e.beforeCommit.Store(nil)
		return
	}
	e.beforeCommit.Store(&fn)
}

// Run evaluates rules until ctx ends.
func (e *RuleEngine) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := e.run(ctx)
		e.Close()
		if err != nil && ctx.Err() == nil {
			slog.Warn("event rules: restarting", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (e *RuleEngine) run(ctx context.Context) error {
	if err := e.connect(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	conn := e.conn
	e.mu.Unlock()
	if _, err := conn.Exec(ctx, "LISTEN "+rulesChannel); err != nil {
		return err
	}
	for {
		more, err := e.Pass(ctx)
		if err != nil {
			return err
		}
		if more {
			continue
		}
		// A timeout leaves the connection open: the pass runs every second
		// even without notifications, for windows that end. One pass reads
		// the events of every notification already received.
		for timeout := time.Second; ; timeout = time.Millisecond {
			wait, cancel := context.WithTimeout(ctx, timeout)
			_, err := conn.WaitForNotification(wait)
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil && conn.IsClosed() {
				return err
			}
			if err != nil {
				break
			}
		}
	}
}

func (e *RuleEngine) connect(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn != nil && !e.conn.IsClosed() {
		return nil
	}
	pc, err := e.app.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	e.conn = pc.Hijack()
	e.held = map[int64]bool{}
	return nil
}

// Close lets go of the streams this engine holds.
func (e *RuleEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn != nil {
		e.conn.Close(context.Background())
	}
	e.conn = nil
	e.held = map[int64]bool{}
}

func lockKey(stream int64) int32 { return int32(stream & 0x7fffffff) }

// Acquire takes the locks of the streams enabled rules read that no other
// node holds, lets go of those no rule reads any more, and returns the
// streams this engine holds.
func (e *RuleEngine) Acquire(ctx context.Context) ([]int64, error) {
	if err := e.connect(ctx); err != nil {
		return nil, err
	}
	rows, err := e.app.DB.Query(ctx, `SELECT DISTINCT (s #>> '{}')::bigint FROM event_rules r, jsonb_array_elements(r.streams) s WHERE r.enabled`)
	if err != nil {
		return nil, err
	}
	want, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.conn.Ping(ctx); err != nil {
		return nil, err
	}
	wanted := map[int64]bool{}
	for _, s := range want {
		wanted[s] = true
		if e.held[s] {
			continue
		}
		var got bool
		if err := e.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", ruleLockClass, lockKey(s)).Scan(&got); err != nil {
			return nil, err
		}
		if got {
			e.held[s] = true
		}
	}
	for s := range e.held {
		if !wanted[s] {
			e.unlock(ctx, s)
		}
	}
	out := make([]int64, 0, len(e.held))
	for s := range e.held {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (e *RuleEngine) unlock(ctx context.Context, s int64) {
	delete(e.held, s)
	if e.conn != nil {
		e.conn.Exec(ctx, "SELECT pg_advisory_unlock($1, $2)", ruleLockClass, lockKey(s))
	}
}

// Release lets go of a stream, for another node to take.
func (e *RuleEngine) Release(ctx context.Context, stream int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.held[stream] {
		e.unlock(ctx, stream)
	}
}

// Pass runs the batches of the streams this engine holds, fires the
// once_after windows that are due, and acts on the approvals decided. It
// says whether a stream has more events to read.
func (e *RuleEngine) Pass(ctx context.Context) (bool, error) {
	streams, err := e.Acquire(ctx)
	if err != nil {
		return false, err
	}
	more := false
	for _, s := range streams {
		for i := 0; ; i++ {
			n, err := e.Step(ctx, s)
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("event rules: batch failed", "stream", s, "err", err)
				}
				break
			}
			if n < e.BatchSize {
				break
			}
			if i == passBatches-1 {
				more = true
				break
			}
		}
	}
	if err := e.FireDue(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("event rules: firing windows failed", "err", err)
	}
	if err := e.ResolveApprovals(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("event rules: resolving approvals failed", "err", err)
	}
	return more, nil
}

// afterCommit is what a batch does once committed.
type afterCommit struct {
	notifications []int64
	approvals     []int64
	counts        ruleCounts
}

func (a *afterCommit) run(ctx context.Context, app *api.App) {
	a.counts.publish()
	for _, id := range a.notifications {
		go deliver(ctx, app, id)
	}
	for _, id := range a.approvals {
		notifyApproval(&Ctx{Context: ctx, App: app, User: rbac.System(), Extra: map[string]any{}}, id, "running")
	}
}

const ruleColumns = `id, name, organization_id, streams, owner_id, filter, key, throttle, throttle_window, action,
	unified_job_template_id, extra_vars, job_limit, notification_template_id, message, target_stream_id, event_type, event_data,
	mode, max_concurrent_runs, host_lock, max_group_share, breaker_firings, breaker_window, breaker_notification_template_id,
	window_action, approval_timeout`

func scanRule(row pgx.Row) (*eventRule, error) {
	r := &eventRule{}
	err := row.Scan(&r.ID, &r.Name, &r.Org, &r.Streams, &r.Owner, &r.Filter, &r.Key, &r.Throttle, &r.Window, &r.Action,
		&r.Template, &r.ExtraVars, &r.Limit, &r.Notification, &r.Message, &r.Target, &r.EventType, &r.EventData,
		&r.Mode, &r.MaxRuns, &r.HostLock, &r.GroupShare, &r.BreakerFirings, &r.BreakerWindow, &r.BreakerNotification,
		&r.WindowAction, &r.ApprovalTimeout)
	return r, err
}

func streamRules(ctx context.Context, db api.DBTX, stream int64) ([]*eventRule, error) {
	rows, err := db.Query(ctx, `SELECT `+ruleColumns+` FROM event_rules WHERE enabled AND streams @> jsonb_build_array($1::bigint) ORDER BY id`, stream)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*eventRule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Step decides the next batch of a stream's events and returns how many
// it read.
func (e *RuleEngine) Step(ctx context.Context, stream int64) (int, error) {
	tx, err := e.app.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `INSERT INTO event_consumer_positions (consumer, stream_id, position)
		SELECT $1, id, last_sequence FROM event_streams WHERE id = $2 ON CONFLICT DO NOTHING`, rulesConsumer, stream); err != nil {
		return 0, err
	}
	// The position's row lock fences the batches of a stream: a node that
	// lost the stream's lock without noticing waits here for the new
	// holder's batch and reads on from where it committed.
	var pos int64
	err = tx.QueryRow(ctx, `SELECT position FROM event_consumer_positions WHERE consumer = $1 AND stream_id = $2 FOR UPDATE`, rulesConsumer, stream).Scan(&pos)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	events, err := eventstreams.Read(ctx, tx, stream, pos, e.BatchSize)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	rs, err := streamRules(ctx, tx, stream)
	if err != nil {
		return 0, err
	}
	after := &afterCommit{}
	var misses []miss
	for _, ev := range events {
		val := rules.Value(ev)
		ectx, span := eventSpan(ctx, ev)
		trace := tracing.TraceID(ectx)
		n := len(misses)
		for _, r := range rs {
			if r.stopped {
				continue
			}
			ok, m := rules.Filter(ctx, r.Filter, rules.Vars{Event: val})
			if !ok {
				misses = append(misses, miss{rule: r, ev: ev, miss: m, trace: trace})
				continue
			}
			if err := e.decide(ectx, tx, after, r, ev, val); err != nil {
				tracing.EndSpan(span, err)
				return 0, err
			}
		}
		span.SetAttributes(attribute.Int("awxnext.event_rules.not_matched", len(misses)-n))
		span.End()
	}
	if err := e.recordMisses(ctx, tx, after, stream, misses); err != nil {
		return 0, err
	}
	last := events[len(events)-1].Sequence
	if err := eventstreams.SavePosition(ctx, tx, rulesConsumer, stream, last); err != nil {
		return 0, err
	}
	var newest int64
	if err := tx.QueryRow(ctx, `SELECT last_sequence FROM event_streams WHERE id = $1`, stream).Scan(&newest); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if fn := e.beforeCommit.Load(); fn != nil {
		if err := (*fn)(stream); err != nil {
			tx.Rollback(context.Background())
			e.Release(ctx, stream)
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	after.run(ctx, e.app)
	label := idLabel(stream)
	now := time.Now()
	for _, ev := range events {
		streamLag.With(label).Observe(now.Sub(ev.Received).Seconds())
	}
	streamBacklog.With(label).Set(float64(max(newest-last, 0)))
	return len(events), nil
}

// eventSpan starts the span of an event's decisions, in the trace the
// event names in its traceparent extension, or in a new one.
func eventSpan(ctx context.Context, ev eventstreams.Stored) (context.Context, trace.Span) {
	ctx = tracing.WithTraceparent(ctx, extension(ev, "traceparent"), extension(ev, "tracestate"))
	return tracing.StartSpan(ctx, "event_stream event", attribute.Int64("awxnext.event_stream.id", ev.Stream),
		attribute.Int64("awxnext.event_stream.sequence", ev.Sequence), attribute.String("cloudevents.event_id", ev.ID),
		attribute.String("cloudevents.event_source", ev.Source), attribute.String("cloudevents.event_type", ev.Type))
}

// ruleSpan starts the span of a rule's decision. Without an event span in
// ctx, as when a window ends or an approval is granted later, it joins the
// event's trace.
func ruleSpan(ctx context.Context, r *eventRule, ev eventstreams.Stored) (context.Context, trace.Span) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		ctx = tracing.WithTraceparent(ctx, extension(ev, "traceparent"), extension(ev, "tracestate"))
	}
	return tracing.StartSpan(ctx, "event_rule decide", attribute.Int64("awxnext.event_rule.id", r.ID),
		attribute.String("awxnext.event_rule.name", r.Name), attribute.String("awxnext.event_rule.mode", r.Mode))
}

func extension(ev eventstreams.Stored, name string) string {
	v, _ := ev.Extensions[name].(string)
	return v
}

// miss is an event a rule's filter didn't match.
type miss struct {
	rule  *eventRule
	ev    eventstreams.Stored
	miss  *rules.Miss
	trace string
}

// recordMisses records the batch's events that rules' filters didn't
// match, at once: most events match no rule.
func (e *RuleEngine) recordMisses(ctx context.Context, tx pgx.Tx, after *afterCommit, stream int64, misses []miss) error {
	if len(misses) == 0 {
		return nil
	}
	n := len(misses)
	ruleIDs, seqs := make([]int64, n), make([]int64, n)
	ids, sources, types, reasons, modes, details, traces := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	received := make([]time.Time, n)
	for i, m := range misses {
		ruleIDs[i], seqs[i], received[i] = m.rule.ID, m.ev.Sequence, m.ev.Received
		ids[i], sources[i], types[i] = m.ev.ID, m.ev.Source, m.ev.Type
		reasons[i], modes[i], traces[i] = m.miss.Reason(), m.rule.Mode, m.trace
		b, err := json.Marshal(map[string]any{"filter": m.miss.Details()})
		if err != nil {
			return err
		}
		details[i] = string(b)
	}
	rows, err := tx.Query(ctx, `INSERT INTO event_rule_decisions (rule_id, stream_id, sequence, event_id, event_source, event_type, received, node,
			outcome, reason, mode, details, trace_id)
		SELECT x.rule, $2, x.seq, x.id, x.source, x.type, x.received, $3, 'not_matched', x.reason, x.mode, x.details::jsonb, x.trace
		FROM unnest($1::bigint[], $4::bigint[], $5::text[], $6::text[], $7::text[], $8::timestamptz[], $9::text[], $10::text[], $11::text[], $12::text[])
			AS x(rule, seq, id, source, type, received, reason, mode, details, trace)
		ON CONFLICT (rule_id, stream_id, sequence, received) DO NOTHING RETURNING rule_id`,
		ruleIDs, stream, e.node, seqs, ids, sources, types, received, reasons, modes, details, traces)
	if err != nil {
		return err
	}
	counted := map[int64]int{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		counted[id]++
	}
	rows.Close()
	for id, k := range counted {
		after.outcome(id, outcome{name: "not_matched"}, k)
	}
	return rows.Err()
}

// member is an event an action fires for, with its decision and key.
type member struct {
	ev       eventstreams.Stored
	val      map[string]any
	decision int64
	key      string
}

// outcome is what a rule did with an event.
type outcome struct {
	name, reason string
	key          string
	job          *int64
	window       *int64
	approval     *int64
	guardrail    string
	details      map[string]any
}

// fired says whether an outcome counts as a firing for the circuit
// breaker: the action ran, or was tried, or would have run in shadow mode,
// or asked for approval.
func (o outcome) fired() bool {
	switch o.name {
	case "launched", "notified", "posted", "failed", "shadow", "awaiting_approval":
		return true
	}
	return false
}

func (r *eventRule) shadow() bool { return r.Mode == modeShadow }

// decide records what a rule does with an event it matches, and does it.
func (e *RuleEngine) decide(ctx context.Context, tx pgx.Tx, after *afterCommit, r *eventRule, ev eventstreams.Stored, val map[string]any) (err error) {
	ctx, span := ruleSpan(ctx, r, ev)
	defer func() { tracing.EndSpan(span, err) }()
	vars := rules.Vars{Event: val}
	key := ""
	if r.Key != "" {
		k, err := rules.Text(ctx, r.Key, vars)
		if err != nil {
			return e.record(ctx, tx, after, r, ev, outcome{name: "error", reason: "The key could not be evaluated: " + err.Error()})
		}
		key = k
	}
	var until time.Time
	if r.Throttle == rules.OnceWithin {
		// Holding the key first saves a write per event held back. An
		// event read again finds its key held, as it was when decided.
		err := tx.QueryRow(ctx, `INSERT INTO event_rule_keys AS k (rule_id, shadow, key, held_until) VALUES ($1, $2, $3, $4)
			ON CONFLICT (rule_id, shadow, key) DO UPDATE SET held_until = EXCLUDED.held_until WHERE k.held_until <= $5 RETURNING held_until`,
			r.ID, r.shadow(), key, ev.Received.Add(time.Duration(r.Window)*time.Second), ev.Received).Scan(&until)
		if errors.Is(err, pgx.ErrNoRows) {
			return e.record(ctx, tx, after, r, ev, outcome{name: "throttled", key: key, reason: "The key acted less than the window ago."})
		}
		if err != nil {
			return err
		}
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO event_rule_decisions (rule_id, stream_id, sequence, event_id, event_source, event_type, received, node, outcome, mode, trace_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9, $10) ON CONFLICT (rule_id, stream_id, sequence, received) DO NOTHING RETURNING id`,
		r.ID, ev.Stream, ev.Sequence, ev.ID, ev.Source, ev.Type, ev.Received, e.node, r.Mode, tracing.TraceID(ctx)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Decided before: the event was read again.
		return nil
	}
	if err != nil {
		return err
	}
	after.matched(r.ID)
	out, err := e.throttle(ctx, tx, after, r, member{ev: ev, val: val, decision: id, key: key})
	if err != nil {
		return err
	}
	if out.name == "held" && r.Throttle == rules.OnceWithin {
		// An action held back doesn't hold its key.
		if _, err := tx.Exec(ctx, `DELETE FROM event_rule_keys WHERE rule_id = $1 AND shadow = $2 AND key = $3 AND held_until = $4`,
			r.ID, r.shadow(), key, until); err != nil {
			return err
		}
	}
	return saveOutcome(ctx, tx, after, r.ID, id, ev.Received, r.Mode, out)
}

// saveOutcome gives a decision its outcome. Details add to those it has.
// received is the decision's, which finds its partition.
func saveOutcome(ctx context.Context, db pgx.Tx, after *afterCommit, rule, id int64, received time.Time, mode string, out outcome) error {
	details := out.details
	if details == nil {
		details = map[string]any{}
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("awxnext.event_rule.outcome", out.name))
	_, err := db.Exec(ctx, `UPDATE event_rule_decisions SET outcome = $3, reason = $4, key = $5, job_id = $6, window_id = coalesce($7, window_id),
		mode = $8, guardrail = coalesce(nullif($9, ''), guardrail), fired = fired OR $10, approval_id = coalesce($11, approval_id), details = details || $12::jsonb, modified = now()
		WHERE id = $1 AND received = $2`, id, received, out.name, out.reason, out.key, out.job, out.window, mode, out.guardrail, out.fired(), out.approval, details)
	if err == nil {
		after.outcome(rule, out, 1)
	}
	return err
}

// record records a decision that doesn't act, unless the event was decided
// before.
func (e *RuleEngine) record(ctx context.Context, tx pgx.Tx, after *afterCommit, r *eventRule, ev eventstreams.Stored, out outcome) error {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("awxnext.event_rule.outcome", out.name))
	tag, err := tx.Exec(ctx, `INSERT INTO event_rule_decisions (rule_id, stream_id, sequence, event_id, event_source, event_type, received, node, outcome, reason, key, mode, trace_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT (rule_id, stream_id, sequence, received) DO NOTHING`,
		r.ID, ev.Stream, ev.Sequence, ev.ID, ev.Source, ev.Type, ev.Received, e.node, out.name, out.reason, out.key, r.Mode, tracing.TraceID(ctx))
	if err == nil && tag.RowsAffected() == 1 {
		after.matched(r.ID)
		after.outcome(r.ID, out, 1)
	}
	return err
}

// throttle applies a once_after throttle to an event and its key, or acts.
func (e *RuleEngine) throttle(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, m member) (outcome, error) {
	key := m.key
	window := time.Duration(r.Window) * time.Second
	switch r.Throttle {
	case rules.OnceAfter:
		var wid int64
		var fireAt time.Time
		err := db.QueryRow(ctx, `SELECT id, fire_at FROM event_rule_windows WHERE rule_id = $1 FOR UPDATE`, r.ID).Scan(&wid, &fireAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return outcome{}, err
		}
		if err == nil && !m.ev.Received.Before(fireAt) {
			// The window ended before this event: it fires first.
			if err := e.fireWindow(ctx, db, after, r, wid); err != nil {
				return outcome{}, err
			}
			wid = 0
		}
		if r.stopped {
			return outcome{name: "refused", key: key, reason: "The rule stopped: see the decision of its window."}, nil
		}
		if wid == 0 {
			// Another node, deciding another of the rule's streams, may open
			// the window first: then this event joins it.
			fireAt = m.ev.Received.Add(window)
			err := db.QueryRow(ctx, `INSERT INTO event_rule_windows (rule_id, opened, fire_at) VALUES ($1, $2, $3) ON CONFLICT (rule_id) DO NOTHING RETURNING id`,
				r.ID, m.ev.Received, fireAt).Scan(&wid)
			if errors.Is(err, pgx.ErrNoRows) {
				err = db.QueryRow(ctx, `SELECT id, fire_at FROM event_rule_windows WHERE rule_id = $1 FOR UPDATE`, r.ID).Scan(&wid, &fireAt)
			}
			if err != nil {
				return outcome{}, err
			}
		}
		var dup bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM event_rule_decisions WHERE window_id = $1 AND key = $2 AND outcome = 'waiting')`, wid, key).Scan(&dup); err != nil {
			return outcome{}, err
		}
		if dup {
			return outcome{name: "deduplicated", key: key, window: &wid, reason: "The window already holds an event with this key."}, nil
		}
		return outcome{name: "waiting", key: key, window: &wid, reason: "Waiting for the window to end at " + fireAt.UTC().Format(time.RFC3339) + "."}, nil
	}
	outs, err := e.fire(ctx, db, after, r, []member{m})
	if err != nil {
		return outcome{}, err
	}
	return outs[0], nil
}

// fire runs the action once for members, but for those whose key's circuit
// breaker is open, and returns each member's outcome. A key reaching the
// breaker's firings trips it.
func (e *RuleEngine) fire(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, members []member) ([]outcome, error) {
	outs := make([]outcome, len(members))
	var acting []member
	var idx []int
	prior := map[string]int64{}
	for i, m := range members {
		open, n, err := breakerState(ctx, db, r, r.shadow(), m.key, m.ev.Received)
		if err != nil {
			return nil, err
		}
		if open {
			if err := tripBreaker(ctx, db, after, r, r.shadow(), m.key, m.ev.Received, n); err != nil {
				return nil, err
			}
			outs[i] = breakerHold(r, m.key)
			outs[i].key = m.key
			continue
		}
		prior[m.key] = n
		acting = append(acting, m)
		idx = append(idx, i)
	}
	if len(acting) == 0 {
		return outs, nil
	}
	out, err := e.act(ctx, db, after, r, acting, false)
	if err != nil {
		return nil, err
	}
	for j, i := range idx {
		outs[i] = out
		outs[i].key = acting[j].key
	}
	if out.fired() && r.BreakerFirings > 0 {
		for _, m := range acting {
			if n := prior[m.key] + 1; n >= r.BreakerFirings {
				if err := tripBreaker(ctx, db, after, r, r.shadow(), m.key, m.ev.Received, n); err != nil {
					return nil, err
				}
			}
		}
	}
	return outs, nil
}

// fireWindow acts once for the events a once_after window holds, the first
// of each key, and gives their decisions the outcome.
func (e *RuleEngine) fireWindow(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, wid int64) error {
	if _, err := db.Exec(ctx, "DELETE FROM event_rule_windows WHERE id = $1", wid); err != nil {
		return err
	}
	rows, err := db.Query(ctx, `SELECT id, stream_id, sequence, key FROM event_rule_decisions WHERE window_id = $1 AND outcome = 'waiting'
		ORDER BY received, stream_id, sequence`, wid)
	if err != nil {
		return err
	}
	type ref struct {
		id, stream, seq int64
		key             string
	}
	var refs []ref
	for rows.Next() {
		var x ref
		if err := rows.Scan(&x.id, &x.stream, &x.seq, &x.key); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var members []member
	for _, x := range refs {
		ev, err := eventstreams.Get(ctx, db, x.stream, x.seq)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		members = append(members, member{ev: ev, val: rules.Value(ev), decision: x.id, key: x.key})
	}
	if len(members) > 0 {
		sctx, span := ruleSpan(ctx, r, members[0].ev)
		outs, err := e.fire(sctx, db, after, r, members)
		if err != nil {
			tracing.EndSpan(span, err)
			return err
		}
		for i, m := range members {
			if err := saveOutcome(sctx, db, after, r.ID, m.decision, m.ev.Received, r.Mode, outs[i]); err != nil {
				tracing.EndSpan(span, err)
				return err
			}
		}
		span.End()
	}
	tag, err := db.Exec(ctx, `UPDATE event_rule_decisions SET outcome = 'dropped', reason = $2, modified = now() WHERE window_id = $1 AND outcome = 'waiting'`,
		wid, "The window's events are no longer in the log.")
	if err == nil && tag.RowsAffected() > 0 {
		after.outcome(r.ID, outcome{name: "dropped"}, int(tag.RowsAffected()))
	}
	return err
}

// FireDue fires the once_after windows that have ended, once their rule's
// streams have no unread event received before the end.
func (e *RuleEngine) FireDue(ctx context.Context) error {
	tx, err := e.app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT w.id, w.rule_id FROM event_rule_windows w JOIN event_rules r ON r.id = w.rule_id
		WHERE w.fire_at <= now() AND r.enabled AND NOT EXISTS (
			SELECT 1 FROM jsonb_array_elements(r.streams) s
			JOIN event_consumer_positions p ON p.consumer = $1 AND p.stream_id = (s #>> '{}')::bigint
			JOIN stream_events ev ON ev.stream_id = p.stream_id AND ev.sequence > p.position
			WHERE NOT ev.test_mode AND ev.received < w.fire_at)
		ORDER BY w.fire_at LIMIT 50 FOR UPDATE OF w SKIP LOCKED`, rulesConsumer)
	if err != nil {
		return err
	}
	type due struct{ window, rule int64 }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.window, &d.rule); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(list) == 0 {
		return err
	}
	after := &afterCommit{}
	for _, d := range list {
		r, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleColumns+` FROM event_rules WHERE id = $1`, d.rule))
		if err != nil {
			return err
		}
		if err := e.fireWindow(ctx, tx, after, r, d.window); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	after.run(ctx, e.app)
	return nil
}

// stopRule disables a rule its owner may no longer run.
func stopRule(ctx context.Context, db pgx.Tx, r *eventRule, reason string) error {
	r.stopped = true
	slog.Warn("event rule stopped", "rule", r.ID, "reason", reason)
	if _, err := db.Exec(ctx, `UPDATE event_rules SET enabled = false, stopped_reason = $2, modified = now() WHERE id = $1`, r.ID, reason); err != nil {
		return err
	}
	return dropWindows(ctx, db, r.ID, "The rule stopped before its window ended.")
}

// ownerCtx is the context the rule's action runs in: as its owner, when
// the owner is still an active user.
func (e *RuleEngine) ownerCtx(ctx context.Context, db pgx.Tx, r *eventRule) (*Ctx, string, error) {
	if r.Owner == nil {
		return nil, "The rule's owner was deleted.", nil
	}
	p, err := auth.LoadPrincipal(ctx, e.app, *r.Owner)
	if err != nil {
		return nil, "", err
	}
	if p == nil {
		return nil, fmt.Sprintf("The rule's owner (user %d) is no longer active.", *r.Owner), nil
	}
	return &Ctx{Context: ctx, App: e.app, User: p, Tx: db, Extra: map[string]any{}}, "", nil
}

// act runs the rule's action for members, the first as event: as the
// rule's owner, after checking the owner still may. A refusal stops the
// rule. Its guardrails, windows and mode apply, but, with approved, those
// that applied when the approval was asked for: the blast radius only.
func (e *RuleEngine) act(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, members []member, approved bool) (outcome, error) {
	c, reason, err := e.ownerCtx(ctx, db, r)
	if err != nil {
		return outcome{}, err
	}
	if c != nil {
		if reason, err = ruleRefusal(c, r); err != nil {
			return outcome{}, err
		}
	}
	if reason != "" {
		return outcome{name: "refused", reason: reason}, stopRule(ctx, db, r, reason)
	}
	vars := rules.Vars{Event: members[0].val}
	for _, m := range members {
		vars.Events = append(vars.Events, m.val)
	}
	sp, err := db.Begin(ctx)
	if err != nil {
		return outcome{}, err
	}
	defer sp.Rollback(context.Background())
	c.Tx = sp
	out, err := e.guard(c, after, r, members, vars, approved)
	if err != nil {
		if ctx.Err() != nil {
			return outcome{}, err
		}
		// The action's error is its decision's: an event whose action
		// keeps failing must not hold back the stream.
		if err := sp.Rollback(ctx); err != nil {
			return outcome{}, err
		}
		name := "failed"
		if errors.Is(err, errRuleEval) {
			name = "error"
		}
		return outcome{name: name, reason: describeError(err)}, nil
	}
	if err := sp.Commit(ctx); err != nil {
		return outcome{}, err
	}
	switch out.name {
	case "launched", "notified", "posted":
		_, err = db.Exec(ctx, "UPDATE event_rules SET last_fired = now() WHERE id = $1", r.ID)
	}
	return out, err
}

// guard plans the action, applies the rule's guardrails and mode, and does
// what is left to do: hold the action back, record it in shadow mode, ask
// for approval, or act.
func (e *RuleEngine) guard(c *Ctx, after *afterCommit, r *eventRule, members []member, vars rules.Vars, approved bool) (outcome, error) {
	p, err := e.plan(c, r, members, vars)
	if err != nil {
		return outcome{}, err
	}
	g := newGuards(c, c.DB())
	approval := !approved && r.Mode == modeApproval
	var window *hold
	if !approved {
		if window, err = g.windows(r, p, members[0].ev.Received); err != nil {
			return outcome{}, err
		}
		if window != nil && !window.approve {
			return window.outcome(), nil
		}
		approval = approval || window != nil
	}
	h, err := g.blastRadius(r, p)
	if err != nil {
		return outcome{}, err
	}
	if h != nil {
		return h.outcome(), nil
	}
	details := map[string]any{"action": p.describe()}
	guardrail := ""
	if window != nil {
		guardrail = window.guardrail
		for k, v := range window.details {
			details[k] = v
		}
	}
	if r.shadow() {
		what := "would have " + p.done()
		if approval {
			what = "would have asked for approval to " + p.summary()
		}
		return outcome{name: "shadow", guardrail: guardrail, details: details, reason: "Shadow mode: the rule " + what + "."}, nil
	}
	if approval {
		why := "The rule is in approval mode."
		if window != nil {
			why = window.reason
		}
		return e.requestApproval(c, after, r, members, p, why, guardrail, details)
	}
	return e.execute(c, after, r, members, p)
}

// requestApproval asks for a workflow approval of the action: the rule's
// decisions wait for it, and ResolveApprovals acts once it is approved.
func (e *RuleEngine) requestApproval(c *Ctx, after *afterCommit, r *eventRule, members []member, p *plan, why, guardrail string, details map[string]any) (outcome, error) {
	ev := members[0].ev
	name := r.Name + ": " + p.summary()
	if len(name) > 512 {
		name = name[:509] + "..."
	}
	desc := fmt.Sprintf("Event %s (%s) from %s, %d of stream %d", ev.ID, ev.Type, ev.Source, ev.Sequence, ev.Stream)
	if members[0].key != "" {
		desc += ", key " + members[0].key
	}
	if len(members) > 1 {
		desc += fmt.Sprintf(", and %d more events", len(members)-1)
	}
	desc += ". " + why
	id, err := jobs.Create(c, c.DB(), map[string]any{"type": "workflow_approval", "name": name, "description": desc,
		"approval_timeout": r.ApprovalTimeout, "status": "pending", "launch_type": "manual", "event_rule_id": r.ID,
		"stream_event": eventRef(members), "organization_id": r.Org, "created_by_id": r.Owner})
	if err != nil {
		return outcome{}, err
	}
	if err := jobLaunched(c, "workflow_approvals", id); err != nil {
		return outcome{}, err
	}
	after.approvals = append(after.approvals, id)
	return outcome{name: "awaiting_approval", approval: &id, guardrail: guardrail, details: details,
		reason: fmt.Sprintf("Waiting for workflow approval %d. %s", id, why)}, nil
}

// ResolveApprovals gives the decisions waiting for an approval its outcome,
// and acts on an approval granted: exactly once, as the transaction that
// acts also gives the decisions their outcome. It cancels the approvals of
// rules disabled or put in shadow mode.
func (e *RuleEngine) ResolveApprovals(ctx context.Context) error {
	rows, err := e.app.DB.Query(ctx, `SELECT DISTINCT d.approval_id, coalesce(a.status, ''), r.enabled AND r.mode <> 'shadow'
		FROM event_rule_decisions d JOIN event_rules r ON r.id = d.rule_id LEFT JOIN unified_jobs a ON a.id = d.approval_id
		WHERE d.outcome = 'awaiting_approval' AND (a.id IS NULL OR a.status <> 'pending' OR NOT r.enabled OR r.mode = 'shadow')
		LIMIT 50`)
	if err != nil {
		return err
	}
	type due struct {
		id     int64
		status string
		live   bool
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.status, &d.live); err != nil {
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
		if d.status == "pending" && !d.live && Jobs != nil {
			if err := Jobs.Cancel(ctx, d.id); err != nil {
				return err
			}
		}
		if err := e.resolveApproval(ctx, d.id); err != nil {
			return err
		}
	}
	return nil
}

func (e *RuleEngine) resolveApproval(ctx context.Context, approval int64) error {
	tx, err := e.app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var status string
	var timedOut bool
	var by *string
	err = tx.QueryRow(ctx, `SELECT a.status, a.timed_out, u.username FROM unified_jobs a LEFT JOIN users u ON u.id = a.approved_or_denied_by_id
		WHERE a.id = $1 FOR UPDATE OF a`, approval).Scan(&status, &timedOut, &by)
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		return err
	}
	if status == "pending" {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id, received, rule_id, stream_id, sequence, key, mode FROM event_rule_decisions
		WHERE approval_id = $1 AND outcome = 'awaiting_approval' ORDER BY received, stream_id, sequence FOR UPDATE`, approval)
	if err != nil {
		return err
	}
	type ref struct {
		id, rule, stream, seq int64
		received              time.Time
		key, mode             string
	}
	var refs []ref
	for rows.Next() {
		var x ref
		if err := rows.Scan(&x.id, &x.received, &x.rule, &x.stream, &x.seq, &x.key, &x.mode); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(refs) == 0 {
		return err
	}
	var enabled bool
	if err := tx.QueryRow(ctx, "SELECT enabled FROM event_rules WHERE id = $1", refs[0].rule).Scan(&enabled); err != nil {
		return err
	}
	r, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleColumns+` FROM event_rules WHERE id = $1`, refs[0].rule))
	if err != nil {
		return err
	}
	who := ""
	if by != nil {
		who = *by
	}
	after := &afterCommit{}
	var out outcome
	switch {
	case missing:
		out = outcome{name: "canceled", reason: "The approval was deleted."}
	case status == "successful" && (!enabled || r.shadow()):
		out = outcome{name: "canceled", reason: "The rule was disabled or put in shadow mode before the action ran."}
	case status == "successful":
		var members []member
		for _, x := range refs {
			ev, err := eventstreams.Get(ctx, tx, x.stream, x.seq)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			members = append(members, member{ev: ev, val: rules.Value(ev), decision: x.id, key: x.key})
		}
		if len(members) == 0 {
			out = outcome{name: "dropped", reason: "The approved events are no longer in the log."}
		} else {
			sctx, span := ruleSpan(ctx, r, members[0].ev)
			out, err = e.act(sctx, tx, after, r, members, true)
			span.SetAttributes(attribute.String("awxnext.event_rule.outcome", out.name))
			tracing.EndSpan(span, err)
			if err != nil {
				return err
			}
		}
	case timedOut:
		out = outcome{name: "timed_out", reason: "The approval timed out."}
	case status == "failed":
		out = outcome{name: "denied", reason: "Denied by " + who + "."}
	default:
		out = outcome{name: "canceled", reason: "The approval was canceled."}
	}
	if out.details == nil {
		out.details = map[string]any{}
	}
	out.details["approval"] = Obj{"id": approval, "status": status, "by": who}
	for _, x := range refs {
		o := out
		o.key = x.key
		if err := saveOutcome(ctx, tx, after, x.rule, x.id, x.received, x.mode, o); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	after.run(ctx, e.app)
	return nil
}

var errRuleEval = errors.New("evaluation")

type evalError struct {
	what string
	err  error
}

func (e *evalError) Error() string   { return e.what + " could not be evaluated: " + e.err.Error() }
func (e *evalError) Is(t error) bool { return t == errRuleEval }

// describeError is an action's error for its decision: an API error's
// message, without the payload of the request.
func describeError(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		b, _ := json.Marshal(ae.Body)
		return fmt.Sprintf("The action failed (%d): %s", ae.Status, b)
	}
	return err.Error()
}

// eventRef is how a job, or a posted event, names the event it came from.
func eventRef(members []member) map[string]any {
	ev := members[0].ev
	return map[string]any{"stream": ev.Stream, "sequence": ev.Sequence, "id": ev.ID, "source": ev.Source, "type": ev.Type,
		"subject": ev.Subject, "events": len(members)}
}

// plan is a rule's action with its expressions evaluated: what the
// guardrails check and shadow mode records, before anything is done.
type plan struct {
	action string

	tpl     Obj
	tplType string
	body    Obj
	runs    []runTarget

	ntType  string
	subject string
	message any

	event  eventstreams.Event
	target int64
	test   bool
}

// runTarget is an inventory a launch runs on, and its limit.
type runTarget struct {
	inventory int64
	limit     string
}

func (e *RuleEngine) plan(c *Ctx, r *eventRule, members []member, vars rules.Vars) (*plan, error) {
	switch r.Action {
	case actionLaunch:
		return e.planLaunch(c, r, vars)
	case actionNotify:
		return e.planNotify(c, r, members, vars)
	case actionPost:
		return e.planPost(c, r, members, vars)
	}
	return nil, api.Detail(400, "Unknown action "+r.Action+".")
}

// summary is what the action does, for an approval's name.
func (p *plan) summary() string {
	switch p.action {
	case actionLaunch:
		what := "job template"
		if p.tplType == "workflow_job_template" {
			what = "workflow"
		}
		s := fmt.Sprintf("launch %s %s", what, str(p.tpl["name"]))
		if l, ok := p.body["limit"].(string); ok && l != "" {
			s += " on " + l
		}
		return s
	case actionNotify:
		return "send " + p.subject
	}
	return fmt.Sprintf("post a %s event to stream %d", p.event.Type, p.target)
}

// done is what the action did, for shadow mode's reason.
func (p *plan) done() string {
	return strings.Replace(strings.Replace(strings.Replace(p.summary(), "launch ", "launched ", 1), "send ", "sent ", 1), "post ", "posted ", 1)
}

// describe is what the action would do, for its decision.
func (p *plan) describe() map[string]any {
	switch p.action {
	case actionLaunch:
		d := map[string]any{"launch": Obj{"id": p.tpl["id"], "name": p.tpl["name"], "type": p.tplType}}
		for k, v := range p.body {
			d[k] = v
		}
		return d
	case actionNotify:
		return map[string]any{"notify": p.tpl, "subject": p.subject, "message": p.message}
	}
	return map[string]any{"post": p.target, "event_type": p.event.Type, "event_id": p.event.ID, "data": json.RawMessage(p.event.Data)}
}

func (e *RuleEngine) planLaunch(c *Ctx, r *eventRule, vars rules.Vars) (*plan, error) {
	body := Obj{}
	names := make([]string, 0, len(r.ExtraVars))
	for k := range r.ExtraVars {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) > 0 {
		ev := map[string]any{}
		for _, k := range names {
			v, err := rules.Eval(c, r.ExtraVars[k], vars)
			if err != nil {
				return nil, &evalError{"extra_vars." + k, err}
			}
			ev[k] = v
		}
		if err := refuseAnsibleVars(c, "extra_vars", ev); err != nil {
			return nil, err
		}
		body["extra_vars"] = ev
	}
	var limit *string
	if r.Limit != "" {
		l, err := rules.Text(c, r.Limit, vars)
		if err != nil {
			return nil, &evalError{"The limit", err}
		}
		body["limit"], limit = l, &l
	}
	typ := ujtType(c, *r.Template)
	res := api.LookupType(typ)
	if res == nil || (typ != "job_template" && typ != "workflow_job_template") {
		return nil, api.Detail(400, "The rule's template is not a job or workflow template.")
	}
	tpl, err := api.Get(c, res, *r.Template)
	if err != nil {
		return nil, err
	}
	runs, err := launchRuns(c, typ, tpl, limit)
	if err != nil {
		return nil, err
	}
	return &plan{action: actionLaunch, tpl: tpl, tplType: typ, body: body, runs: runs}, nil
}

func (e *RuleEngine) planNotify(c *Ctx, r *eventRule, members []member, vars rules.Vars) (*plan, error) {
	var typ, name string
	if err := c.DB().QueryRow(c, "SELECT notification_type, name FROM notification_templates WHERE id = $1", *r.Notification).Scan(&typ, &name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, api.Detail(400, "The notification template no longer exists.")
		}
		return nil, err
	}
	about := map[string]any{"rule": Obj{"id": r.ID, "name": r.Name}, "event": eventRef(members)}
	subject := fmt.Sprintf("Event rule %s: %s", r.Name, members[0].ev.Type)
	var msg any
	if r.Message != "" {
		v, err := rules.Eval(c, r.Message, vars)
		if err != nil {
			return nil, &evalError{"The message", err}
		}
		msg = v
	}
	// An email's body is text; a webhook's is the message when it is an
	// object, else the message with the rule and the event.
	var body any = about
	text, isText := msg.(string)
	switch {
	case typ == "email" && isText:
		subject, body = text, text
	case typ == "email":
		b, _ := json.MarshalIndent(about, "", "  ")
		if msg != nil {
			b, _ = json.MarshalIndent(msg, "", "  ")
		}
		body = string(b)
	case isText:
		about["message"] = text
	case msg != nil:
		body = msg
	}
	return &plan{action: actionNotify, tpl: Obj{"id": *r.Notification, "name": name}, ntType: typ, subject: subject, message: body}, nil
}

func (e *RuleEngine) planPost(c *Ctx, r *eventRule, members []member, vars rules.Vars) (*plan, error) {
	first := members[0].ev
	depth, _ := api.ID(first.Extensions[depthExtension])
	if depth >= maxPostDepth {
		return nil, api.Detail(400, fmt.Sprintf("The event went through %d rules already: rules posting to each other loop.", depth))
	}
	typ, err := rules.Text(c, r.EventType, vars)
	if err != nil {
		return nil, &evalError{"The event type", err}
	}
	if typ == "" {
		return nil, &evalError{"The event type", errors.New("it is empty")}
	}
	data := json.RawMessage(first.Data)
	if r.EventData != "" {
		v, err := rules.Eval(c, r.EventData, vars)
		if err != nil {
			return nil, &evalError{"The event data", err}
		}
		if data, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	var test bool
	if err := c.DB().QueryRow(c, "SELECT test_mode FROM event_streams WHERE id = $1", *r.Target).Scan(&test); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, api.Detail(400, "The target stream no longer exists.")
		}
		return nil, err
	}
	now := time.Now().UTC()
	ev := eventstreams.Event{
		ID:              fmt.Sprintf("rule-%d-%d-%d", r.ID, first.Stream, first.Sequence),
		Source:          api.Lookup("event_rules").URL(r.ID),
		Type:            typ,
		Subject:         first.Subject,
		Time:            &now,
		DataContentType: "application/json",
		Data:            data,
		Extensions:      map[string]any{depthExtension: depth + 1},
	}
	if tp := tracing.Traceparent(c); tp != "" {
		ev.Extensions["traceparent"] = tp
	} else if tp, ok := first.Extensions["traceparent"]; ok {
		ev.Extensions["traceparent"] = tp
	}
	return &plan{action: actionPost, event: ev, target: *r.Target, test: test}, nil
}

// execute does what a plan says.
func (e *RuleEngine) execute(c *Ctx, after *afterCommit, r *eventRule, members []member, p *plan) (outcome, error) {
	switch p.action {
	case actionLaunch:
		ov := &LaunchOverrides{ExtraJob: map[string]any{"event_rule_id": r.ID, "stream_event": eventRef(members)}}
		var job int64
		var err error
		if p.tplType == "job_template" {
			job, _, err = LaunchJobTemplate(c, p.tpl, p.body, "manual", ov)
		} else {
			job, _, err = LaunchWorkflowJobTemplate(c, p.tpl, p.body, "manual", ov)
		}
		if err != nil {
			return outcome{}, err
		}
		return outcome{name: "launched", job: &job}, nil
	case actionNotify:
		id, err := createNotification(c, c.DB(), *r.Notification, nil, p.subject, p.message)
		if err != nil {
			return outcome{}, err
		}
		after.notifications = append(after.notifications, id)
		return outcome{name: "notified"}, nil
	}
	res, err := eventstreams.Ingest(c, logDB(c), p.target, p.test, []eventstreams.Event{p.event})
	if err != nil {
		return outcome{}, err
	}
	if _, err := c.DB().Exec(c, "SELECT pg_notify($1, $2)", rulesChannel, strconv.FormatInt(p.target, 10)); err != nil {
		return outcome{}, err
	}
	return outcome{name: "posted", reason: fmt.Sprintf("Event %d of stream %d.", res[0].Sequence, p.target)}, nil
}

// WakeRules tells the rule engines that a stream has new events.
func WakeRules(ctx context.Context, db api.DBTX, stream int64) {
	db.Exec(ctx, "SELECT pg_notify($1, $2)", rulesChannel, strconv.FormatInt(stream, 10))
}
