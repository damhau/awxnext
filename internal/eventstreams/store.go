package eventstreams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is what the log needs of a pool, a connection or a transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Stored is an event of the log.
type Stored struct {
	Event
	Stream   int64
	Sequence int64
	Received time.Time
	TestMode bool
}

// Result is what Ingest made of an event: its place in the log, or the
// place of the event with the same ID received before.
type Result struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Sequence  int64  `json:"sequence"`
	Duplicate bool   `json:"duplicate"`
}

// ErrNoStream is Ingest's error when the stream no longer exists.
var ErrNoStream = errors.New("the event stream does not exist")

// Ingest appends events to a stream's log and returns once they are
// committed. An event whose source and ID the stream already has is
// dropped. Events of a stream in test mode are kept apart from rules.
func Ingest(ctx context.Context, db DB, stream int64, testMode bool, events []Event) ([]Result, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// The stream's row lock orders its writers: sequences are given, and
	// become visible, in order, and two senders of the same ID cannot both
	// pass the duplicate check.
	var last int64
	if err := tx.QueryRow(ctx, `SELECT last_sequence FROM event_streams WHERE id = $1 FOR NO KEY UPDATE`, stream).Scan(&last); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoStream
		}
		return nil, err
	}
	results := make([]Result, 0, len(events))
	stored := 0
	for _, ev := range events {
		res := Result{ID: ev.ID, Source: ev.Source, Sequence: last + 1}
		var seq int64
		err := tx.QueryRow(ctx, `INSERT INTO event_stream_dedup (stream_id, source, event_id, sequence) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING RETURNING sequence`, stream, ev.Source, ev.ID, res.Sequence).Scan(&seq)
		if errors.Is(err, pgx.ErrNoRows) {
			res.Duplicate = true
			if err := tx.QueryRow(ctx, `SELECT sequence FROM event_stream_dedup WHERE stream_id = $1 AND source = $2 AND event_id = $3`,
				stream, ev.Source, ev.ID).Scan(&res.Sequence); err != nil {
				return nil, err
			}
			results = append(results, res)
			continue
		}
		if err != nil {
			return nil, err
		}
		ext := ev.Extensions
		if ext == nil {
			ext = map[string]any{}
		}
		var d any
		if len(ev.Data) > 0 {
			d = string(ev.Data)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stream_events (stream_id, sequence, event_id, source, event_type, subject, event_time,
			data_content_type, data_schema, data, extensions, test_mode) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12)`,
			stream, res.Sequence, ev.ID, ev.Source, ev.Type, ev.Subject, ev.Time, ev.DataContentType, ev.DataSchema, d, ext, testMode); err != nil {
			return nil, err
		}
		last = res.Sequence
		stored++
		results = append(results, res)
	}
	if stored > 0 {
		if _, err := tx.Exec(ctx, `UPDATE event_streams SET last_sequence = $2, last_event_at = now() WHERE id = $1`, stream, last); err != nil {
			return nil, err
		}
	}
	return results, tx.Commit(ctx)
}

const storedColumns = `stream_id, sequence, received, event_id, source, event_type, subject, event_time, data_content_type, data_schema, data, extensions, test_mode`

func scanStored(rows pgx.Rows) (Stored, error) {
	var s Stored
	var data []byte
	err := rows.Scan(&s.Stream, &s.Sequence, &s.Received, &s.ID, &s.Source, &s.Type, &s.Subject, &s.Time,
		&s.DataContentType, &s.DataSchema, &data, &s.Extensions, &s.TestMode)
	if len(data) > 0 {
		s.Data = json.RawMessage(data)
	}
	return s, err
}

// Read returns up to limit events of a stream that come after position
// after, in order. Events received in test mode are not given: rules do
// not see them.
func Read(ctx context.Context, db DB, stream, after int64, limit int) ([]Stored, error) {
	rows, err := db.Query(ctx, `SELECT `+storedColumns+` FROM stream_events
		WHERE stream_id = $1 AND sequence > $2 AND NOT test_mode ORDER BY sequence LIMIT $3`, stream, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stored
	for rows.Next() {
		s, err := scanStored(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Get returns the event of a stream at sequence, test mode or not.
func Get(ctx context.Context, db DB, stream, sequence int64) (Stored, error) {
	rows, err := db.Query(ctx, `SELECT `+storedColumns+` FROM stream_events WHERE stream_id = $1 AND sequence = $2`, stream, sequence)
	if err != nil {
		return Stored{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Stored{}, err
		}
		return Stored{}, pgx.ErrNoRows
	}
	return scanStored(rows)
}

// Recent returns up to limit of the latest events of streams, test mode
// included: what rules are checked against.
func Recent(ctx context.Context, db DB, streams []int64, limit int) ([]Stored, error) {
	var out []Stored
	for _, s := range streams {
		rows, err := db.Query(ctx, `SELECT `+storedColumns+` FROM stream_events WHERE stream_id = $1 ORDER BY sequence DESC LIMIT $2`, s, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			ev, err := scanStored(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, ev)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Position is where consumer has read a stream to: 0, its start, when it
// never saved a position.
func Position(ctx context.Context, db DB, consumer string, stream int64) (int64, error) {
	var pos int64
	err := db.QueryRow(ctx, `SELECT position FROM event_consumer_positions WHERE consumer = $1 AND stream_id = $2`, consumer, stream).Scan(&pos)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return pos, err
}

// SavePosition records that consumer has read a stream to position.
func SavePosition(ctx context.Context, db DB, consumer string, stream, position int64) error {
	_, err := db.Exec(ctx, `INSERT INTO event_consumer_positions (consumer, stream_id, position) VALUES ($1, $2, $3)
		ON CONFLICT (consumer, stream_id) DO UPDATE SET position = EXCLUDED.position, modified = now()`, consumer, stream, position)
	return err
}

const partitionPrefix = "stream_events_"

var partitionRe = regexp.MustCompile(`^stream_events_(\d{8})$`)

// MaintainPartitions creates the log's partitions of today and the next
// two days.
func MaintainPartitions(ctx context.Context, db DB) {
	now := time.Now().UTC()
	for d := 0; d < 3; d++ {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, d)
		name := partitionPrefix + day.Format("20060102")
		_, err := db.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF stream_events FOR VALUES FROM ('%s') TO ('%s')`,
			pgx.Identifier{name}.Sanitize(), day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339)))
		if err != nil {
			// The day's events already landed in the default partition; it keeps them.
			slog.Debug("stream event partition", "name", name, "err", err)
		}
	}
}

// CleanupResult is what Cleanup removed, or would remove.
type CleanupResult struct {
	Partitions []string
	Events     int64
	IDs        int64
}

const cleanupBatch = 10000

// Cleanup removes what streams keep no longer: the days older than every
// stream's retention, whole, then each stream's expired events, the events
// of deleted streams, and the IDs kept to drop duplicates, as long as
// their stream's events. With dryRun it only counts.
func Cleanup(ctx context.Context, db DB, now time.Time, dryRun bool) (CleanupResult, error) {
	var res CleanupResult
	var longest int
	if err := db.QueryRow(ctx, `SELECT coalesce(max(retention_days), 0) FROM event_streams`).Scan(&longest); err != nil {
		return res, err
	}
	cutoff := now.UTC().AddDate(0, 0, -longest)
	rows, err := db.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'stream_events'::regclass ORDER BY c.relname`)
	if err != nil {
		return res, err
	}
	var parts []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return res, err
		}
		parts = append(parts, name)
	}
	rows.Close()
	for _, name := range parts {
		m := partitionRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		day, err := time.Parse("20060102", m[1])
		if err != nil || day.AddDate(0, 0, 1).After(cutoff) {
			continue
		}
		var n int64
		if err := db.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{name}.Sanitize()).Scan(&n); err != nil {
			return res, err
		}
		if !dryRun {
			if _, err := db.Exec(ctx, `DROP TABLE `+pgx.Identifier{name}.Sanitize()); err != nil {
				return res, err
			}
		}
		res.Partitions = append(res.Partitions, name)
		res.Events += n
	}

	type stream struct {
		id   int64
		days int
	}
	var streams []stream
	rows, err = db.Query(ctx, `SELECT id, retention_days FROM event_streams ORDER BY id`)
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
	limit := fmt.Sprint(cleanupBatch)
	for _, s := range streams {
		before := now.UTC().AddDate(0, 0, -s.days)
		n, err := remove(ctx, db, dryRun, res.Partitions, `stream_events`, `t.stream_id = $1 AND t.received < $2`,
			`DELETE FROM stream_events WHERE stream_id = $1 AND received < $2 AND id IN
				(SELECT id FROM stream_events WHERE stream_id = $1 AND received < $2 LIMIT `+limit+`)`, s.id, before)
		if err != nil {
			return res, err
		}
		res.Events += n
		if n, err = remove(ctx, db, dryRun, nil, `event_stream_dedup`, `t.stream_id = $1 AND t.received < $2`,
			`DELETE FROM event_stream_dedup WHERE stream_id = $1 AND (source, event_id) IN
				(SELECT source, event_id FROM event_stream_dedup WHERE stream_id = $1 AND received < $2 LIMIT `+limit+`)`, s.id, before); err != nil {
			return res, err
		}
		res.IDs += n
	}
	orphan := `NOT EXISTS (SELECT 1 FROM event_streams s WHERE s.id = t.stream_id)`
	n, err := remove(ctx, db, dryRun, res.Partitions, `stream_events`, orphan,
		`DELETE FROM stream_events WHERE id IN (SELECT t.id FROM stream_events t WHERE `+orphan+` LIMIT `+limit+`)`)
	if err != nil {
		return res, err
	}
	res.Events += n
	if n, err = remove(ctx, db, dryRun, nil, `event_stream_dedup`, orphan,
		`DELETE FROM event_stream_dedup WHERE (stream_id, source, event_id) IN
			(SELECT t.stream_id, t.source, t.event_id FROM event_stream_dedup t WHERE `+orphan+` LIMIT `+limit+`)`); err != nil {
		return res, err
	}
	res.IDs += n
	return res, nil
}

// remove runs del until it deletes fewer rows than a batch, and returns
// how many it deleted. With dryRun it counts the rows of table (alias t)
// that match where instead, outside the partitions dropped, which were
// counted whole.
func remove(ctx context.Context, db DB, dryRun bool, dropped []string, table, where, del string, args ...any) (int64, error) {
	if dryRun {
		q := `SELECT count(*) FROM ` + table + ` t WHERE ` + where
		if dropped != nil {
			args = append(args, dropped)
			q += fmt.Sprintf(` AND t.tableoid::regclass::text <> ALL($%d::text[])`, len(args))
		}
		var n int64
		err := db.QueryRow(ctx, q, args...).Scan(&n)
		return n, err
	}
	var total int64
	for {
		tag, err := db.Exec(ctx, del, args...)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < cleanupBatch {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}
