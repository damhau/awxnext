package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/rules"
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

// Pass runs the batches of the streams this engine holds, then fires the
// once_after windows that are due. It says whether a stream has more events
// to read.
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
	return more, nil
}

// afterCommit is what a batch does once committed.
type afterCommit struct {
	notifications []int64
}

func (a *afterCommit) run(ctx context.Context, app *api.App) {
	for _, id := range a.notifications {
		go deliver(ctx, app, id)
	}
}

const ruleColumns = `id, name, organization_id, streams, owner_id, filter, key, throttle, throttle_window, action,
	unified_job_template_id, extra_vars, job_limit, notification_template_id, message, target_stream_id, event_type, event_data`

func scanRule(row pgx.Row) (*eventRule, error) {
	r := &eventRule{}
	err := row.Scan(&r.ID, &r.Name, &r.Org, &r.Streams, &r.Owner, &r.Filter, &r.Key, &r.Throttle, &r.Window, &r.Action,
		&r.Template, &r.ExtraVars, &r.Limit, &r.Notification, &r.Message, &r.Target, &r.EventType, &r.EventData)
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
	for _, ev := range events {
		val := rules.Value(ev)
		for _, r := range rs {
			if r.stopped {
				continue
			}
			if err := e.decide(ctx, tx, after, r, ev, val); err != nil {
				return 0, err
			}
		}
	}
	if err := eventstreams.SavePosition(ctx, tx, rulesConsumer, stream, events[len(events)-1].Sequence); err != nil {
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
	return len(events), nil
}

// member is an event an action fires for, with its decision.
type member struct {
	ev       eventstreams.Stored
	val      map[string]any
	decision int64
}

// outcome is what a rule did with an event.
type outcome struct {
	name, reason string
	key          string
	job          *int64
	window       *int64
}

// decide records what a rule does with an event it matches, and does it.
func (e *RuleEngine) decide(ctx context.Context, tx pgx.Tx, after *afterCommit, r *eventRule, ev eventstreams.Stored, val map[string]any) error {
	vars := rules.Vars{Event: val}
	if ok, err := rules.Match(ctx, r.Filter, vars); err != nil || !ok {
		return nil
	}
	key := ""
	if r.Key != "" {
		k, err := rules.Text(ctx, r.Key, vars)
		if err != nil {
			return e.record(ctx, tx, r, ev, outcome{name: "error", reason: "The key could not be evaluated: " + err.Error()})
		}
		key = k
	}
	if r.Throttle == rules.OnceWithin {
		// Holding the key first saves a write per event held back. An
		// event read again finds its key held, as it was when decided.
		var until time.Time
		err := tx.QueryRow(ctx, `INSERT INTO event_rule_keys AS k (rule_id, key, held_until) VALUES ($1, $2, $3)
			ON CONFLICT (rule_id, key) DO UPDATE SET held_until = EXCLUDED.held_until WHERE k.held_until <= $4 RETURNING held_until`,
			r.ID, key, ev.Received.Add(time.Duration(r.Window)*time.Second), ev.Received).Scan(&until)
		if errors.Is(err, pgx.ErrNoRows) {
			return e.record(ctx, tx, r, ev, outcome{name: "throttled", key: key, reason: "The key acted less than the window ago."})
		}
		if err != nil {
			return err
		}
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO event_rule_decisions (rule_id, stream_id, sequence, event_id, event_source, event_type, received, node, outcome)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending') ON CONFLICT (rule_id, stream_id, sequence) DO NOTHING RETURNING id`,
		r.ID, ev.Stream, ev.Sequence, ev.ID, ev.Source, ev.Type, ev.Received, e.node).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Decided before: the event was read again.
		return nil
	}
	if err != nil {
		return err
	}
	out, err := e.throttle(ctx, tx, after, r, member{ev: ev, val: val, decision: id}, key)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE event_rule_decisions SET outcome = $2, reason = $3, key = $4, job_id = $5, window_id = $6, modified = now() WHERE id = $1`,
		id, out.name, out.reason, out.key, out.job, out.window)
	return err
}

// record records a decision that doesn't act, unless the event was decided
// before.
func (e *RuleEngine) record(ctx context.Context, tx pgx.Tx, r *eventRule, ev eventstreams.Stored, out outcome) error {
	_, err := tx.Exec(ctx, `INSERT INTO event_rule_decisions (rule_id, stream_id, sequence, event_id, event_source, event_type, received, node, outcome, reason, key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (rule_id, stream_id, sequence) DO NOTHING`,
		r.ID, ev.Stream, ev.Sequence, ev.ID, ev.Source, ev.Type, ev.Received, e.node, out.name, out.reason, out.key)
	return err
}

// throttle applies a once_after throttle to an event and its key, or acts.
func (e *RuleEngine) throttle(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, m member, key string) (outcome, error) {
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
	out, err := e.act(ctx, db, after, r, []member{m})
	out.key = key
	return out, err
}

// fireWindow acts once for the events a once_after window holds, the first
// of each key, and gives their decisions the outcome.
func (e *RuleEngine) fireWindow(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, wid int64) error {
	if _, err := db.Exec(ctx, "DELETE FROM event_rule_windows WHERE id = $1", wid); err != nil {
		return err
	}
	rows, err := db.Query(ctx, `SELECT id, stream_id, sequence FROM event_rule_decisions WHERE window_id = $1 AND outcome = 'waiting' ORDER BY received, stream_id, sequence`, wid)
	if err != nil {
		return err
	}
	type ref struct{ id, stream, seq int64 }
	var refs []ref
	for rows.Next() {
		var x ref
		if err := rows.Scan(&x.id, &x.stream, &x.seq); err != nil {
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
		members = append(members, member{ev: ev, val: rules.Value(ev), decision: x.id})
	}
	out := outcome{name: "dropped", reason: "The window's events are no longer in the log."}
	if len(members) > 0 {
		if out, err = e.act(ctx, db, after, r, members); err != nil {
			return err
		}
	}
	_, err = db.Exec(ctx, `UPDATE event_rule_decisions SET outcome = $2, reason = $3, job_id = $4, modified = now() WHERE window_id = $1 AND outcome = 'waiting'`,
		wid, out.name, out.reason, out.job)
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
// rule.
func (e *RuleEngine) act(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, members []member) (outcome, error) {
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
	var out outcome
	switch r.Action {
	case actionLaunch:
		out, err = e.launch(c, r, members, vars)
	case actionNotify:
		out, err = e.notify(c, after, r, members, vars)
	case actionPost:
		out, err = e.post(c, r, members, vars)
	default:
		out = outcome{name: "error", reason: "Unknown action " + r.Action + "."}
	}
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
	_, err = db.Exec(ctx, "UPDATE event_rules SET last_fired = now() WHERE id = $1", r.ID)
	return out, err
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

func (e *RuleEngine) launch(c *Ctx, r *eventRule, members []member, vars rules.Vars) (outcome, error) {
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
				return outcome{}, &evalError{"extra_vars." + k, err}
			}
			ev[k] = v
		}
		if err := refuseAnsibleVars(c, "extra_vars", ev); err != nil {
			return outcome{}, err
		}
		body["extra_vars"] = ev
	}
	if r.Limit != "" {
		limit, err := rules.Text(c, r.Limit, vars)
		if err != nil {
			return outcome{}, &evalError{"The limit", err}
		}
		body["limit"] = limit
	}
	typ := ujtType(c, *r.Template)
	res := api.LookupType(typ)
	if res == nil || (typ != "job_template" && typ != "workflow_job_template") {
		return outcome{}, api.Detail(400, "The rule's template is not a job or workflow template.")
	}
	tpl, err := api.Get(c, res, *r.Template)
	if err != nil {
		return outcome{}, err
	}
	ov := &LaunchOverrides{ExtraJob: map[string]any{"event_rule_id": r.ID, "stream_event": eventRef(members)}}
	var job int64
	if typ == "job_template" {
		job, _, err = LaunchJobTemplate(c, tpl, body, "manual", ov)
	} else {
		job, _, err = LaunchWorkflowJobTemplate(c, tpl, body, "manual", ov)
	}
	if err != nil {
		return outcome{}, err
	}
	return outcome{name: "launched", job: &job}, nil
}

func (e *RuleEngine) notify(c *Ctx, after *afterCommit, r *eventRule, members []member, vars rules.Vars) (outcome, error) {
	var typ string
	if err := c.DB().QueryRow(c, "SELECT notification_type FROM notification_templates WHERE id = $1", *r.Notification).Scan(&typ); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return outcome{}, api.Detail(400, "The notification template no longer exists.")
		}
		return outcome{}, err
	}
	about := map[string]any{"rule": Obj{"id": r.ID, "name": r.Name}, "event": eventRef(members)}
	subject := fmt.Sprintf("Event rule %s: %s", r.Name, members[0].ev.Type)
	var msg any
	if r.Message != "" {
		v, err := rules.Eval(c, r.Message, vars)
		if err != nil {
			return outcome{}, &evalError{"The message", err}
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
	id, err := createNotification(c, c.DB(), *r.Notification, nil, subject, body)
	if err != nil {
		return outcome{}, err
	}
	after.notifications = append(after.notifications, id)
	return outcome{name: "notified"}, nil
}

func (e *RuleEngine) post(c *Ctx, r *eventRule, members []member, vars rules.Vars) (outcome, error) {
	first := members[0].ev
	depth, _ := api.ID(first.Extensions[depthExtension])
	if depth >= maxPostDepth {
		return outcome{}, api.Detail(400, fmt.Sprintf("The event went through %d rules already: rules posting to each other loop.", depth))
	}
	typ, err := rules.Text(c, r.EventType, vars)
	if err != nil {
		return outcome{}, &evalError{"The event type", err}
	}
	if typ == "" {
		return outcome{}, &evalError{"The event type", errors.New("it is empty")}
	}
	data := json.RawMessage(first.Data)
	if r.EventData != "" {
		v, err := rules.Eval(c, r.EventData, vars)
		if err != nil {
			return outcome{}, &evalError{"The event data", err}
		}
		if data, err = json.Marshal(v); err != nil {
			return outcome{}, err
		}
	}
	var test bool
	if err := c.DB().QueryRow(c, "SELECT test_mode FROM event_streams WHERE id = $1", *r.Target).Scan(&test); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return outcome{}, api.Detail(400, "The target stream no longer exists.")
		}
		return outcome{}, err
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
	if tp, ok := first.Extensions["traceparent"]; ok {
		ev.Extensions["traceparent"] = tp
	}
	res, err := eventstreams.Ingest(c, logDB(c), *r.Target, test, []eventstreams.Event{ev})
	if err != nil {
		return outcome{}, err
	}
	if _, err := c.DB().Exec(c, "SELECT pg_notify($1, $2)", rulesChannel, strconv.FormatInt(*r.Target, 10)); err != nil {
		return outcome{}, err
	}
	return outcome{name: "posted", reason: fmt.Sprintf("Event %d of stream %d.", res[0].Sequence, *r.Target)}, nil
}

// WakeRules tells the rule engines that a stream has new events.
func WakeRules(ctx context.Context, db api.DBTX, stream int64) {
	db.Exec(ctx, "SELECT pg_notify($1, $2)", rulesChannel, strconv.FormatInt(stream, 10))
}
