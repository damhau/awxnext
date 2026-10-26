package jobs

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job events of finished jobs leave the database after JOB_EVENTS_HOT_DAYS:
// each job's events become one gzip'd JSON-lines archive and the day's
// partition is dropped. Viewing an archived job's output restores its events
// into the default partition for a day.

var eventColumns = []string{"id", "job_id", "job_created", "created", "modified", "counter", "event", "event_data", "failed", "changed",
	"uuid", "parent_uuid", "host_id", "host_name", "playbook", "play", "role", "task", "stdout", "start_line", "end_line", "verbosity"}

type archivedEvent struct {
	ID         int64          `json:"id"`
	JobID      int64          `json:"job_id"`
	JobCreated time.Time      `json:"job_created"`
	Created    time.Time      `json:"created"`
	Modified   time.Time      `json:"modified"`
	Counter    int            `json:"counter"`
	Event      string         `json:"event"`
	EventData  map[string]any `json:"event_data"`
	Failed     bool           `json:"failed"`
	Changed    bool           `json:"changed"`
	UUID       string         `json:"uuid"`
	ParentUUID string         `json:"parent_uuid"`
	HostID     *int64         `json:"host_id"`
	HostName   string         `json:"host_name"`
	Playbook   string         `json:"playbook"`
	Play       string         `json:"play"`
	Role       string         `json:"role"`
	Task       string         `json:"task"`
	Stdout     string         `json:"stdout"`
	StartLine  int            `json:"start_line"`
	EndLine    int            `json:"end_line"`
	Verbosity  int            `json:"verbosity"`
}

func (e *archivedEvent) values() []any {
	return []any{e.ID, e.JobID, e.JobCreated, e.Created, e.Modified, e.Counter, e.Event, e.EventData, e.Failed, e.Changed,
		e.UUID, e.ParentUUID, e.HostID, e.HostName, e.Playbook, e.Play, e.Role, e.Task, e.Stdout, e.StartLine, e.EndLine, e.Verbosity}
}

const finalStatuses = "('successful', 'failed', 'error', 'canceled')"

var partitionRe = regexp.MustCompile(`^job_events_(\d{8})$`)

// ArchiveEvents archives events older than the hot window (exported for tests and the CLI).
func (m *Manager) ArchiveEvents(ctx context.Context, hotDays int) error {
	if hotDays <= 0 {
		return nil
	}
	now := time.Now().UTC()
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -hotDays)
	rows, err := m.DB().Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent WHERE p.relname = 'job_events' ORDER BY c.relname`)
	if err != nil {
		return err
	}
	var parts []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		parts = append(parts, name)
	}
	rows.Close()
	for _, name := range parts {
		match := partitionRe.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		day, err := time.Parse("20060102", match[1])
		if err != nil || day.AddDate(0, 0, 1).After(cutoff) {
			continue
		}
		if err := m.archivePartition(ctx, name, day, day.AddDate(0, 0, 1)); err != nil {
			return fmt.Errorf("archiving %s: %w", name, err)
		}
	}
	if err := m.archiveDefault(ctx, cutoff); err != nil {
		return err
	}
	// Restored copies are dropped again after a day.
	_, err = m.DB().Exec(ctx, `WITH r AS (UPDATE job_event_archives SET restored = NULL WHERE restored < now() - interval '1 day' RETURNING job_id)
		DELETE FROM job_events WHERE job_id IN (SELECT job_id FROM r)`)
	return err
}

func (m *Manager) archivePartition(ctx context.Context, name string, from, to time.Time) error {
	tx, err := m.DB().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM unified_jobs WHERE created >= $1 AND created < $2
		AND status NOT IN `+finalStatuses+`)`, from, to).Scan(&active); err != nil {
		return err
	}
	if active {
		return nil
	}
	ids, err := collectIDs(ctx, tx, fmt.Sprintf("SELECT DISTINCT job_id FROM %s", pgx.Identifier{name}.Sanitize()))
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := archiveJob(ctx, tx, name, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "DROP TABLE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return err
	}
	slog.Info("archived job events", "partition", name, "jobs", len(ids))
	return tx.Commit(ctx)
}

// archiveDefault archives old events that landed in the default partition.
func (m *Manager) archiveDefault(ctx context.Context, cutoff time.Time) error {
	ids, err := collectIDs(ctx, m.DB(), `SELECT DISTINCT e.job_id FROM job_events_default e JOIN unified_jobs j ON j.id = e.job_id
		WHERE e.job_created < $1 AND j.status IN `+finalStatuses+`
		AND NOT EXISTS (SELECT 1 FROM job_event_archives a WHERE a.job_id = e.job_id) LIMIT 1000`, cutoff)
	if err != nil {
		return err
	}
	for _, id := range ids {
		tx, err := m.DB().Begin(ctx)
		if err != nil {
			return err
		}
		if err := archiveJob(ctx, tx, "job_events_default", id); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM job_events_default WHERE job_id = $1", id); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func collectIDs(ctx context.Context, q querier, sql string, args ...any) ([]int64, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// archiveJob stores one job's events from table as a compressed archive.
func archiveJob(ctx context.Context, tx pgx.Tx, table string, jobID int64) error {
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id, job_id, job_created, created, modified, counter, event, event_data, failed, changed,
		uuid, parent_uuid, host_id, host_name, playbook, play, role, task, stdout, start_line, end_line, verbosity
		FROM %s WHERE job_id = $1 ORDER BY counter, id`, pgx.Identifier{table}.Sanitize()), jobID)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	n := 0
	var jobCreated time.Time
	for rows.Next() {
		var e archivedEvent
		if err := rows.Scan(&e.ID, &e.JobID, &e.JobCreated, &e.Created, &e.Modified, &e.Counter, &e.Event, &e.EventData, &e.Failed, &e.Changed,
			&e.UUID, &e.ParentUUID, &e.HostID, &e.HostName, &e.Playbook, &e.Play, &e.Role, &e.Task, &e.Stdout, &e.StartLine, &e.EndLine, &e.Verbosity); err != nil {
			rows.Close()
			return err
		}
		jobCreated = e.JobCreated
		if err := enc.Encode(&e); err != nil {
			rows.Close()
			return err
		}
		n++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	// Events of deleted jobs are dropped with the partition.
	_, err = tx.Exec(ctx, `INSERT INTO job_event_archives (job_id, job_created, event_count, data)
		SELECT $1, $2, $3, $4 WHERE EXISTS (SELECT 1 FROM unified_jobs WHERE id = $1) ON CONFLICT (job_id) DO NOTHING`,
		jobID, jobCreated, n, buf.Bytes())
	return err
}

// RestoreEvents makes an archived job's events readable again. It is a no-op
// for jobs whose events are in the database.
func RestoreEvents(ctx context.Context, db *pgxpool.Pool, jobID int64) error {
	var pending bool
	if err := db.QueryRow(ctx, "SELECT restored IS NULL FROM job_event_archives WHERE job_id = $1", jobID).Scan(&pending); err != nil || !pending {
		return nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var data []byte
	if err := tx.QueryRow(ctx, "SELECT data FROM job_event_archives WHERE job_id = $1 AND restored IS NULL FOR UPDATE", jobID).Scan(&data); err != nil {
		return nil // restored concurrently
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	var rows [][]any
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		var e archivedEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return err
		}
		if e.EventData == nil {
			e.EventData = map[string]any{}
		}
		rows = append(rows, e.values())
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"job_events"}, eventColumns, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE job_event_archives SET restored = now() WHERE job_id = $1", jobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
