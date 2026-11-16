package eventstreams

import (
	"context"
	"fmt"
	"time"
)

// DecisionCleanup is what CleanupDecisions removed, or would remove.
type DecisionCleanup struct {
	Partitions []string
	Decisions  int64
	Keys       int64
	Patterns   int64
	Checks     int64
}

// CleanupDecisions removes the rules' decisions their log keeps no longer.
// A filter that didn't match is kept as long as its event; any other
// decision as long as its event, and keepDays at least. Decisions on
// deleted streams go the same way, as if their events were gone. It also
// removes the throttle keys that expired a day ago, and the patterns'
// windows still there a day after their deadline, whose waiting decisions
// it drops, and the settled checks of fixes as old as the oldest decisions
// kept. With dryRun it only counts.
func CleanupDecisions(ctx context.Context, db DB, now time.Time, keepDays int, dryRun bool) (DecisionCleanup, error) {
	var res DecisionCleanup
	var longest int
	if err := db.QueryRow(ctx, `SELECT coalesce(max(retention_days), 0) FROM event_streams`).Scan(&longest); err != nil {
		return res, err
	}
	now = now.UTC()
	var err error
	res.Partitions, res.Decisions, err = dropDays(ctx, db, "event_rule_decisions", now.AddDate(0, 0, -max(longest, keepDays)), dryRun)
	if err != nil {
		return res, err
	}
	type stream struct {
		id   int64
		days int
	}
	var streams []stream
	rows, err := db.Query(ctx, `SELECT id, retention_days FROM event_streams ORDER BY id`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var s stream
		if err := rows.Scan(&s.id, &s.days); err != nil {
			rows.Close()
			return res, err
		}
		streams = append(streams, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	limit := fmt.Sprint(cleanupBatch)
	del := func(where string, args ...any) error {
		n, err := remove(ctx, db, dryRun, res.Partitions, `event_rule_decisions`, where,
			`DELETE FROM event_rule_decisions WHERE (id, received) IN
				(SELECT t.id, t.received FROM event_rule_decisions t WHERE `+where+` LIMIT `+limit+`)`, args...)
		res.Decisions += n
		return err
	}
	for _, s := range streams {
		if err := del(`t.stream_id = $1 AND t.received < $2 AND t.outcome = 'not_matched'`, s.id, now.AddDate(0, 0, -s.days)); err != nil {
			return res, err
		}
		if err := del(`t.stream_id = $1 AND t.received < $2`, s.id, now.AddDate(0, 0, -max(s.days, keepDays))); err != nil {
			return res, err
		}
	}
	orphan := `NOT EXISTS (SELECT 1 FROM event_streams s WHERE s.id = t.stream_id)`
	if err := del(orphan + ` AND t.outcome = 'not_matched'`); err != nil {
		return res, err
	}
	if err := del(orphan+` AND t.received < $1`, now.AddDate(0, 0, -keepDays)); err != nil {
		return res, err
	}
	res.Keys, err = remove(ctx, db, dryRun, nil, `event_rule_keys`, `t.held_until < $1`,
		`DELETE FROM event_rule_keys WHERE (rule_id, shadow, key) IN
			(SELECT t.rule_id, t.shadow, t.key FROM event_rule_keys t WHERE t.held_until < $1 LIMIT `+limit+`)`, now.AddDate(0, 0, -1))
	if err != nil {
		return res, err
	}
	res.Patterns, err = remove(ctx, db, dryRun, nil, `event_rule_patterns`, `t.deadline < $1`,
		`WITH t AS (SELECT id, events FROM event_rule_patterns WHERE deadline < $1 LIMIT `+limit+` FOR UPDATE SKIP LOCKED),
			d AS (UPDATE event_rule_decisions x SET outcome = 'dropped', reason = 'The window was still there a day after its deadline.', modified = now()
				FROM t, jsonb_array_elements(t.events) e
				WHERE x.id = (e->>'decision')::bigint AND x.received = (e->>'received')::timestamptz AND x.outcome = 'waiting')
		DELETE FROM event_rule_patterns WHERE id IN (SELECT id FROM t)`, now.AddDate(0, 0, -1))
	if err != nil {
		return res, err
	}
	res.Checks, err = remove(ctx, db, dryRun, nil, `event_rule_checks`, `t.state = 'settled' AND t.settled < $1`,
		`DELETE FROM event_rule_checks WHERE id IN
			(SELECT t.id FROM event_rule_checks t WHERE t.state = 'settled' AND t.settled < $1 LIMIT `+limit+`)`, now.AddDate(0, 0, -max(longest, keepDays)))
	return res, err
}
