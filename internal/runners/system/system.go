// Package system runs management jobs (cleanup of old jobs, activity
// stream entries, sessions, tokens and host metrics) in-process.
package system

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/vars"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Register installs the system_job type.
func Register() {
	jobs.Register(&jobs.TypeSpec{
		Type:         "system_job",
		TemplateType: "system_job_template",
		Runner:       jobs.RunnerFunc(run),
		ControlPlane: true,
		Serial:       true,
	})
}

type task struct {
	db     *pgxpool.Pool
	out    *jobs.Output
	days   int
	dryRun bool
	self   int64
}

func run(ctx context.Context, rc *jobs.RunContext) jobs.Result {
	row := rc.Job
	ev, err := vars.Parse(row.Str("extra_vars"))
	if err != nil {
		return jobs.Result{Status: "failed", Explanation: "Invalid extra_vars: " + err.Error()}
	}
	t := &task{db: rc.M.DB(), out: rc.Out, self: row.ID()}
	if v, ok := ev["days"]; ok {
		n, err := strconv.Atoi(fmt.Sprint(v))
		if f, isF := v.(float64); isF {
			n, err = int(f), nil
		}
		if err != nil || n < 0 {
			return jobs.Result{Status: "failed", Explanation: "days must be a non-negative integer"}
		}
		t.days = n
	}
	switch x := ev["dry_run"].(type) {
	case bool:
		t.dryRun = x
	case string:
		t.dryRun = x == "true" || x == "True" || x == "1"
	}
	var fn func(context.Context) error
	switch row.Str("job_type") {
	case "cleanup_jobs":
		fn = t.cleanupJobs
	case "cleanup_activitystream":
		fn = t.cleanupActivity
	case "cleanup_sessions":
		fn = t.simple("DELETE FROM sessions WHERE expires < now()", "Expired Sessions deleted")
	case "cleanup_tokens":
		fn = t.simple("DELETE FROM auth_tokens WHERE expires < now()", "Expired OAuth 2 Access Tokens deleted")
	case "cleanup_host_metrics":
		fn = t.cleanupHostMetrics
	default:
		return jobs.Result{Status: "error", Explanation: "Unknown management job type " + row.Str("job_type")}
	}
	start := time.Now()
	if err := fn(ctx); err != nil {
		t.out.Line("Error: " + err.Error())
		return jobs.Result{Status: "failed", Explanation: err.Error()}
	}
	t.out.Line(fmt.Sprintf("Completed in %.2fs", time.Since(start).Seconds()))
	return jobs.Result{Status: "successful"}
}

func (t *task) cutoff() time.Time { return time.Now().AddDate(0, 0, -t.days) }

func (t *task) action() string {
	if t.dryRun {
		return "would delete"
	}
	return "deleted"
}

// cleanupJobs removes finished jobs of every type created before the cutoff, with their events.
func (t *task) cleanupJobs(ctx context.Context) error {
	cutoff := t.cutoff()
	t.out.Line(fmt.Sprintf("Cleaning up jobs older than %d days (before %s)%s", t.days, cutoff.UTC().Format(time.RFC3339), dry(t.dryRun)))
	rows, err := t.db.Query(ctx, `SELECT type, count(*) FROM unified_jobs WHERE created < $1 AND id <> $2
		AND status IN ('successful', 'failed', 'error', 'canceled') GROUP BY type ORDER BY type`, cutoff, t.self)
	if err != nil {
		return err
	}
	counts := map[string]int64{}
	var order []string
	for rows.Next() {
		var typ string
		var n int64
		rows.Scan(&typ, &n)
		counts[typ] = n
		order = append(order, typ)
	}
	rows.Close()
	if t.dryRun {
		for _, typ := range order {
			t.out.Line(fmt.Sprintf("%s: %d %s", typ, counts[typ], t.action()))
		}
		return nil
	}
	total := int64(0)
	for {
		// Delete in batches to keep transactions short.
		var ids []int64
		rows, err := t.db.Query(ctx, `SELECT id FROM unified_jobs WHERE created < $1 AND id <> $2
			AND status IN ('successful', 'failed', 'error', 'canceled') ORDER BY id LIMIT 500`, cutoff, t.self)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 0 {
			break
		}
		tx, err := t.db.Begin(ctx)
		if err != nil {
			return err
		}
		for _, q := range []string{
			"DELETE FROM job_events WHERE job_id = ANY($1)",
			"DELETE FROM instance_group_links WHERE content_type = 'unifiedjob' AND object_id = ANY($1)",
			"DELETE FROM unified_jobs WHERE id = ANY($1)",
		} {
			if _, err := tx.Exec(ctx, q, ids); err != nil {
				tx.Rollback(ctx)
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		total += int64(len(ids))
	}
	for _, typ := range order {
		t.out.Line(fmt.Sprintf("%s: %d deleted", typ, counts[typ]))
	}
	t.out.Line(fmt.Sprintf("Total: %d jobs deleted", total))
	return nil
}

func (t *task) cleanupActivity(ctx context.Context) error {
	cutoff := t.cutoff()
	t.out.Line(fmt.Sprintf("Cleaning up activity stream entries older than %d days%s", t.days, dry(t.dryRun)))
	var n int64
	if t.dryRun {
		if err := t.db.QueryRow(ctx, "SELECT count(*) FROM activity_stream WHERE timestamp < $1", cutoff).Scan(&n); err != nil {
			return err
		}
	} else {
		tag, err := t.db.Exec(ctx, "DELETE FROM activity_stream WHERE timestamp < $1", cutoff)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
	}
	t.out.Line(fmt.Sprintf("Activity stream entries %s: %d", t.action(), n))
	return nil
}

func (t *task) cleanupHostMetrics(ctx context.Context) error {
	cutoff := t.cutoff()
	t.out.Line(fmt.Sprintf("Cleaning up host metrics soft-deleted more than %d days ago%s", t.days, dry(t.dryRun)))
	q := "FROM host_metrics WHERE deleted AND last_deleted < $1"
	var n int64
	if t.dryRun {
		if err := t.db.QueryRow(ctx, "SELECT count(*) "+q, cutoff).Scan(&n); err != nil {
			return err
		}
	} else {
		tag, err := t.db.Exec(ctx, "DELETE "+q, cutoff)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
	}
	t.out.Line(fmt.Sprintf("Host metrics %s: %d", t.action(), n))
	return nil
}

func (t *task) simple(sql, label string) func(context.Context) error {
	return func(ctx context.Context) error {
		if t.dryRun {
			t.out.Line(label + ": dry run, nothing deleted")
			return nil
		}
		tag, err := t.db.Exec(ctx, sql)
		if err != nil {
			return err
		}
		t.out.Line(fmt.Sprintf("%s: %d", label, tag.RowsAffected()))
		return nil
	}
}

func dry(b bool) string {
	if b {
		return " (dry run)"
	}
	return ""
}
