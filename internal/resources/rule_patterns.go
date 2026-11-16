package resources

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/rules"
	"github.com/damhau/awxnext/internal/tracing"
)

// The patterns of event rules (roadmap M14-07): a rule waits, for each key,
// for a count of events, all of several conditions, a sequence of them, or
// one not followed by another, within a window, and acts once with every
// event that matched. Each key's window is a row of event_rule_patterns,
// locked by the batch that changes it: the rule's streams may be decided
// on different nodes. The batch that adds an event to a window gives the
// events it lets go of, or fires for, their outcome in the transaction
// that saves the stream's position; FirePatterns moves the windows that
// time alone changes.

func (r *eventRule) pattern() rules.Pattern {
	return rules.Pattern{Kind: r.Pattern, Count: int(r.PatternCount), Conditions: len(r.Conditions),
		Window: time.Duration(r.PatternWindow) * time.Second}
}

// hasConditions says whether the rule's pattern matches events against
// its conditions.
func (r *eventRule) hasConditions() bool {
	return r.Pattern != "" && r.Pattern != rules.PatternCount
}

func checkRulePattern(r *eventRule, fe api.FieldErrors) {
	n := len(r.Conditions)
	if r.Pattern == "" {
		if n > 0 {
			fe.Add("conditions", "Only a rule with an all, sequence or absence pattern has conditions.")
		}
		return
	}
	if r.PatternWindow < 1 {
		fe.Add("pattern_window", "A pattern needs a window of at least one second.")
	}
	if r.Throttle != "" {
		fe.Add("throttle", "A rule with a pattern acts once per window: it takes no throttle.")
	}
	switch r.Pattern {
	case rules.PatternCount:
		if r.PatternCount < 2 {
			fe.Add("pattern_count", "A count pattern waits for 2 events at least.")
		}
		if n > 0 {
			fe.Add("conditions", "A count pattern counts the events that pass the filter: it has no conditions.")
		}
	case rules.PatternAbsence:
		if n != 2 {
			fe.Add("conditions", "An absence pattern has 2 conditions: the event that opens the window, and the event that must follow within it.")
		}
	default:
		if n < 2 {
			fe.Add("conditions", "An all or sequence pattern has 2 conditions at least.")
		}
	}
}

// patternChanged says whether a rule's save changes what its windows
// hold: they are dropped then.
func patternChanged(old, obj Obj) bool {
	for _, f := range []string{"pattern", "conditions", "pattern_count", "pattern_window", "key"} {
		if !sameValue(old[f], obj[f]) {
			return true
		}
	}
	return false
}

// dropPatterns drops a rule's windows and the decisions waiting in them.
func dropPatterns(ctx context.Context, db api.DBTX, rule int64, reason string) error {
	_, err := db.Exec(ctx, `WITH w AS (DELETE FROM event_rule_patterns WHERE rule_id = $1 RETURNING events),
		e AS (SELECT (x->>'decision')::bigint AS id, (x->>'received')::timestamptz AS received FROM w, jsonb_array_elements(w.events) x)
		UPDATE event_rule_decisions d SET outcome = 'dropped', reason = $2, modified = now()
		FROM e WHERE d.id = e.id AND d.received = e.received AND d.outcome = 'waiting'`, rule, reason)
	return err
}

// keyWindow is a key's window, locked.
type keyWindow struct {
	id    int64
	key   string
	state rules.PatternState
}

const windowQuery = `SELECT id, opened, clock, events FROM event_rule_patterns WHERE rule_id = $1 AND shadow = $2 AND key = $3 FOR UPDATE`

// lockWindow locks a key's window, or makes it.
func lockWindow(ctx context.Context, db pgx.Tx, r *eventRule, key string, at time.Time) (*keyWindow, error) {
	w := &keyWindow{key: key}
	scan := func() error {
		return db.QueryRow(ctx, windowQuery, r.ID, r.shadow(), key).Scan(&w.id, &w.state.Opened, &w.state.Clock, &w.state.Events)
	}
	err := scan()
	if !errors.Is(err, pgx.ErrNoRows) {
		return w, err
	}
	// Another node, deciding another of the rule's streams, may make the
	// window first: the insert waits for its batch, and this one then
	// locks the window it made.
	err = db.QueryRow(ctx, `INSERT INTO event_rule_patterns (rule_id, shadow, key, opened, clock, deadline) VALUES ($1, $2, $3, $4, $4, $4)
		ON CONFLICT (rule_id, shadow, key) DO NOTHING RETURNING id`, r.ID, r.shadow(), key, at).Scan(&w.id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = scan()
	}
	return w, err
}

// save saves the window, or deletes it once empty.
func (w *keyWindow) save(ctx context.Context, db pgx.Tx, p rules.Pattern) error {
	if len(w.state.Events) == 0 {
		_, err := db.Exec(ctx, `DELETE FROM event_rule_patterns WHERE id = $1`, w.id)
		return err
	}
	_, err := db.Exec(ctx, `UPDATE event_rule_patterns SET opened = $2, clock = $3, deadline = $4, events = $5, progress = $6, modified = now()
		WHERE id = $1`, w.id, w.state.Opened, w.state.Clock, p.Deadline(&w.state), w.state.Events, p.Progress(&w.state))
	return err
}

// addToPattern adds an event to its key's window and returns its outcome.
func (e *RuleEngine) addToPattern(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, m member, matched []int) (outcome, error) {
	w, err := lockWindow(ctx, db, r, m.key, m.ev.Received)
	if err != nil {
		return outcome{}, err
	}
	p := r.pattern()
	step := p.Add(&w.state, rules.PatternEvent{ID: m.decision, Stream: m.ev.Stream, Sequence: m.ev.Sequence, Received: m.ev.Received, Matched: matched})
	self, err := e.settle(ctx, db, after, r, w, step, &m)
	if err != nil {
		return outcome{}, err
	}
	if r.stopped && step.Outcome != rules.Fired {
		return outcome{name: "refused", key: m.key, reason: "The rule stopped: see the decision of its window."}, nil
	}
	if err := w.save(ctx, db, p); err != nil {
		return outcome{}, err
	}
	if step.Outcome == rules.Fired {
		return self, nil
	}
	return outcome{name: step.Outcome, key: m.key, reason: step.Reason, details: map[string]any{"pattern": p.Progress(&w.state)}}, nil
}

// settle gives the events a window let go of their outcome, and fires for
// those it completed with. It returns the outcome of cur, the event added,
// when the window fired with it.
func (e *RuleEngine) settle(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, w *keyWindow, step rules.PatternStep, cur *member) (outcome, error) {
	p := r.pattern()
	for _, x := range step.Expired {
		out := outcome{name: rules.Expired, key: w.key, reason: p.ExpiredReason(&w.state), details: map[string]any{"pattern": p.Progress(&w.state)}}
		if err := saveOutcome(ctx, db, after, r.ID, x.ID, x.Received, r.Mode, out); err != nil {
			return outcome{}, err
		}
	}
	for _, x := range step.Cleared {
		out := outcome{name: rules.Cleared, key: w.key, reason: "An event of condition 2 came before the window ended.",
			details: map[string]any{"cleared_by": map[string]any{"stream": cur.ev.Stream, "sequence": cur.ev.Sequence}}}
		if err := saveOutcome(ctx, db, after, r.ID, x.ID, x.Received, r.Mode, out); err != nil {
			return outcome{}, err
		}
	}
	var self outcome
	for _, fire := range [][]rules.PatternEvent{step.Due, step.Fire} {
		if len(fire) == 0 {
			continue
		}
		members, outs, err := e.firePattern(ctx, db, after, r, w.key, fire, step.At, cur)
		if err != nil {
			return outcome{}, err
		}
		for i, m := range members {
			if cur != nil && m.decision == cur.decision {
				self = outs[i]
				continue
			}
			if err := saveOutcome(ctx, db, after, r.ID, m.decision, m.ev.Received, r.Mode, outs[i]); err != nil {
				return outcome{}, err
			}
		}
	}
	return self, nil
}

// firePattern runs the action once for the events a window completed with,
// at the time it did, unless the key's circuit breaker is open. The
// firing counts once for the breaker: on the decision of the last event.
func (e *RuleEngine) firePattern(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, key string, fire []rules.PatternEvent, at time.Time, cur *member) ([]member, []outcome, error) {
	members := make([]member, 0, len(fire))
	for _, x := range fire {
		if cur != nil && x.ID == cur.decision {
			members = append(members, *cur)
			continue
		}
		ev, err := eventstreams.Get(ctx, db, x.Stream, x.Sequence)
		if errors.Is(err, pgx.ErrNoRows) {
			out := outcome{name: "dropped", key: key, reason: "The event is no longer in the log."}
			if err := saveOutcome(ctx, db, after, r.ID, x.ID, x.Received, r.Mode, out); err != nil {
				return nil, nil, err
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		members = append(members, member{ev: ev, val: rules.Value(ev), decision: x.ID, key: key})
	}
	if len(members) == 0 {
		return nil, nil, nil
	}
	sctx, span := ruleSpan(ctx, r, members[0].ev)
	out, err := e.firePatternAction(sctx, db, after, r, key, members, at)
	tracing.EndSpan(span, err)
	if err != nil {
		return nil, nil, err
	}
	p := r.pattern()
	if out.details == nil {
		out.details = map[string]any{}
	}
	done := &rules.PatternState{Events: fire, Opened: fire[0].Received, Clock: at}
	out.details["pattern"] = p.Progress(done)
	outs := make([]outcome, len(members))
	for i := range members {
		outs[i] = out
		outs[i].key = key
		outs[i].extra = i < len(members)-1
	}
	return members, outs, nil
}

func (e *RuleEngine) firePatternAction(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, key string, members []member, at time.Time) (outcome, error) {
	open, n, err := breakerState(ctx, db, r, r.shadow(), key, at)
	if err != nil {
		return outcome{}, err
	}
	if open {
		return breakerHold(r, key), tripBreaker(ctx, db, after, r, r.shadow(), key, at, n)
	}
	out, err := e.act(ctx, db, after, r, members, false, at)
	if err != nil {
		return outcome{}, err
	}
	if out.fired() && r.BreakerFirings > 0 && n+1 >= r.BreakerFirings {
		err = tripBreaker(ctx, db, after, r, r.shadow(), key, at, n+1)
	}
	return out, err
}

// FirePatterns moves the windows that time alone changes: their old events
// expire, and an absence that ended fires. A window moves to now, or to
// the first unread event of its rule's streams when that came before: the
// events received before a window's deadline are decided first.
func (e *RuleEngine) FirePatterns(ctx context.Context) error {
	tx, err := e.app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT w.id, w.rule_id, w.key, w.opened, w.clock, w.events, h.horizon
		FROM event_rule_patterns w JOIN event_rules r ON r.id = w.rule_id
		CROSS JOIN LATERAL (SELECT least(now(), min(f.received)) AS horizon FROM jsonb_array_elements(r.streams) s
			JOIN event_consumer_positions p ON p.consumer = $1 AND p.stream_id = (s #>> '{}')::bigint
			CROSS JOIN LATERAL (SELECT ev.received FROM stream_events ev WHERE ev.stream_id = p.stream_id AND ev.sequence > p.position
				AND NOT ev.test_mode ORDER BY ev.sequence LIMIT 1) f) h
		WHERE w.deadline <= h.horizon AND r.enabled AND r.pattern <> '' AND w.shadow = (r.mode = 'shadow')
		ORDER BY w.deadline LIMIT 50 FOR UPDATE OF w SKIP LOCKED`, rulesConsumer)
	if err != nil {
		return err
	}
	type due struct {
		w       *keyWindow
		rule    int64
		horizon time.Time
	}
	var list []due
	for rows.Next() {
		d := due{w: &keyWindow{}}
		if err := rows.Scan(&d.w.id, &d.rule, &d.w.key, &d.w.state.Opened, &d.w.state.Clock, &d.w.state.Events, &d.horizon); err != nil {
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
	stopped := map[int64]bool{}
	for _, d := range list {
		if stopped[d.rule] {
			continue
		}
		r, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleColumns+` FROM event_rules WHERE id = $1`, d.rule))
		if err != nil {
			return err
		}
		p := r.pattern()
		if _, err := e.settle(ctx, tx, after, r, d.w, p.Advance(&d.w.state, d.horizon), nil); err != nil {
			return err
		}
		if r.stopped {
			// Stopping the rule dropped its windows.
			stopped[r.ID] = true
			continue
		}
		if err := d.w.save(ctx, tx, p); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	after.run(ctx, e.app)
	return nil
}
