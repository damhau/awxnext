// Package system runs management jobs (cleanup of old jobs, activity
// stream entries, sessions, tokens, host metrics, event streams' events and
// host task history) in-process.
package system

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/settings"
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
	db       *pgxpool.Pool
	settings *settings.Store
	out      *jobs.Output
	days     int
	dryRun   bool
	self     int64
}

func run(ctx context.Context, rc *jobs.RunContext) jobs.Result {
	row := rc.Job
	ev, err := vars.Parse(row.Str("extra_vars"))
	if err != nil {
		return jobs.Result{Status: "failed", Explanation: "Invalid extra_vars: " + err.Error()}
	}
	t := &task{db: rc.M.DB(), settings: rc.M.App.Settings, out: rc.Out, self: row.ID()}
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
	case "host_metric_summary_monthly":
		fn = t.hostMetricSummaryMonthly
	case "cleanup_event_streams":
		fn = t.cleanupEventStreams
	case "cleanup_host_history":
		fn = t.cleanupHostHistory
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
		return t.cleanupReportHistory(ctx)
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
	return t.cleanupReportHistory(ctx)
}

// cleanupReportHistory deletes the reports' copies of runs and host results
// older than REPORT_HISTORY_DAYS, whatever days the job was given: they
// outlive the jobs.
func (t *task) cleanupReportHistory(ctx context.Context) error {
	keep := t.settings.Int(ctx, "REPORT_HISTORY_DAYS")
	if keep < 1 {
		keep = 1
	}
	cutoff := time.Now().AddDate(0, 0, -int(keep))
	t.out.Line(fmt.Sprintf("Cleaning up report history older than %d days%s", keep, dry(t.dryRun)))
	for _, q := range []struct{ what, table, where string }{
		{"Report runs", "report_runs", "finished < $1"},
		{"Report host days", "report_host_days", "day < ($1 AT TIME ZONE 'UTC')::date"},
		{"Report rights keys", "report_keys", "day < ($1 AT TIME ZONE 'UTC')::date"},
	} {
		var n int64
		if t.dryRun {
			if err := t.db.QueryRow(ctx, "SELECT count(*) FROM "+q.table+" WHERE "+q.where, cutoff).Scan(&n); err != nil {
				return err
			}
		} else {
			tag, err := t.db.Exec(ctx, "DELETE FROM "+q.table+" WHERE "+q.where, cutoff)
			if err != nil {
				return err
			}
			n = tag.RowsAffected()
		}
		t.out.Line(fmt.Sprintf("%s %s: %d", q.what, t.action(), n))
	}
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

// summaryMonths is how far back the monthly summary is computed: AWX's
// CLEANUP_HOST_METRICS_HARD_THRESHOLD.
const summaryMonths = 36

// hostMetricSummaryMonthly computes host_metric_summary_monthly as AWX's
// HostMetricSummaryMonthlyTask: for each of the last 36 months, the hosts
// first automated that month, those soft-deleted that month, and the
// running total, starting from the hosts counted before. Older months are
// left as they are. The license capacity is 0, as without a subscription.
func (t *task) hostMetricSummaryMonthly(ctx context.Context) error {
	now := time.Now().UTC()
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	first := current.AddDate(0, -(summaryMonths - 1), 0)
	added, deleted := map[time.Time]int64{}, map[time.Time]int64{}
	for _, q := range []struct {
		sql string
		to  map[time.Time]int64
	}{
		{"SELECT date_trunc('month', first_automation AT TIME ZONE 'UTC'), count(*) FROM host_metrics GROUP BY 1", added},
		{"SELECT date_trunc('month', last_deleted AT TIME ZONE 'UTC'), count(*) FROM host_metrics WHERE deleted AND last_deleted IS NOT NULL GROUP BY 1", deleted},
	} {
		rows, err := t.db.Query(ctx, q.sql)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m time.Time
			var n int64
			if err := rows.Scan(&m, &n); err != nil {
				rows.Close()
				return err
			}
			q.to[time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)] = n
		}
		rows.Close()
	}
	var consumed int64
	for m, n := range added {
		if m.Before(first) {
			consumed += n
		}
	}
	for m, n := range deleted {
		if m.Before(first) {
			consumed -= n
		}
	}
	months := 0
	for m := first; !m.After(current); m = m.AddDate(0, 1, 0) {
		consumed += added[m] - deleted[m]
		if _, err := t.db.Exec(ctx, `INSERT INTO host_metric_summary_monthly (date, license_consumed, hosts_added, hosts_deleted)
			VALUES ($1, $2, $3, $4) ON CONFLICT (date) DO UPDATE SET license_consumed = EXCLUDED.license_consumed,
			hosts_added = EXCLUDED.hosts_added, hosts_deleted = EXCLUDED.hosts_deleted`, m, consumed, added[m], deleted[m]); err != nil {
			return err
		}
		months++
	}
	t.out.Line(fmt.Sprintf("Host metric summary computed for %d months, %d hosts counted this month", months, consumed))
	return nil
}

// cleanupEventStreams removes the events each stream keeps no longer, by
// its own retention, and the rules' decisions on them: days takes no part.
func (t *task) cleanupEventStreams(ctx context.Context) error {
	t.out.Line("Cleaning up event stream events older than their stream's retention" + dry(t.dryRun))
	res, err := eventstreams.Cleanup(ctx, t.db, time.Now(), t.dryRun)
	for _, p := range res.Partitions {
		t.out.Line(fmt.Sprintf("Partition %s %s", p, t.dropAction()))
	}
	t.out.Line(fmt.Sprintf("Events %s: %d", t.action(), res.Events))
	t.out.Line(fmt.Sprintf("Event IDs kept to drop duplicates %s: %d", t.action(), res.IDs))
	if err != nil {
		return err
	}
	keep := t.settings.Int(ctx, "EVENT_RULE_DECISION_RETENTION_DAYS")
	t.out.Line(fmt.Sprintf("Cleaning up event rule decisions: those that matched kept %d days at least%s", keep, dry(t.dryRun)))
	dec, err := eventstreams.CleanupDecisions(ctx, t.db, time.Now(), keep, t.dryRun)
	for _, p := range dec.Partitions {
		t.out.Line(fmt.Sprintf("Partition %s %s", p, t.dropAction()))
	}
	t.out.Line(fmt.Sprintf("Decisions %s: %d", t.action(), dec.Decisions))
	t.out.Line(fmt.Sprintf("Expired throttle keys %s: %d", t.action(), dec.Keys))
	t.out.Line(fmt.Sprintf("Pattern windows a day past their deadline %s: %d", t.action(), dec.Patterns))
	t.out.Line(fmt.Sprintf("Settled checks of fixes %s: %d", t.action(), dec.Checks))
	if err != nil {
		return err
	}
	return t.cleanupLaunchDecisions(ctx, keep)
}

// cleanupLaunchDecisions removes the launch policies' decisions older than
// keep days, or with 0, those whose job is gone.
func (t *task) cleanupLaunchDecisions(ctx context.Context, keep int) error {
	where := "created < $1"
	args := []any{time.Now().AddDate(0, 0, -keep)}
	if keep <= 0 {
		t.out.Line("Cleaning up launch policy decisions whose job is gone" + dry(t.dryRun))
		where, args = "unified_job_id IS NULL OR NOT EXISTS (SELECT 1 FROM unified_jobs j WHERE j.id = unified_job_id)", nil
	} else {
		t.out.Line(fmt.Sprintf("Cleaning up launch policy decisions older than %d days%s", keep, dry(t.dryRun)))
	}
	var n int64
	if t.dryRun {
		if err := t.db.QueryRow(ctx, "SELECT count(*) FROM launch_policy_decisions WHERE "+where, args...).Scan(&n); err != nil {
			return err
		}
	} else {
		tag, err := t.db.Exec(ctx, "DELETE FROM launch_policy_decisions WHERE "+where, args...)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
	}
	t.out.Line(fmt.Sprintf("Launch policy decisions %s: %d", t.action(), n))
	return nil
}

// cleanupHostHistory removes the hosts' task results older than
// HOST_HISTORY_DAYS: days takes no part. Open drift stays.
func (t *task) cleanupHostHistory(ctx context.Context) error {
	keep := t.settings.Int(ctx, "HOST_HISTORY_DAYS")
	if keep <= 0 {
		t.out.Line("Cleaning up all host task results: the host history is off" + dry(t.dryRun))
	} else {
		t.out.Line(fmt.Sprintf("Cleaning up host task results older than %d days%s", keep, dry(t.dryRun)))
	}
	cutoff := time.Now().AddDate(0, 0, -max(keep, 0))
	var n int64
	if t.dryRun {
		if err := t.db.QueryRow(ctx, "SELECT count(*) FROM host_task_results WHERE created < $1", cutoff).Scan(&n); err != nil {
			return err
		}
	} else {
		tag, err := t.db.Exec(ctx, "DELETE FROM host_task_results WHERE created < $1", cutoff)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
	}
	t.out.Line(fmt.Sprintf("Host task results %s: %d", t.action(), n))
	return nil
}

func (t *task) dropAction() string {
	if t.dryRun {
		return "would be dropped"
	}
	return "dropped"
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
