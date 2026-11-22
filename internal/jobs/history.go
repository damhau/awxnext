package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// HostHistory, set on a job's Output, keeps each host's task results for
// the host's history, and the drift its check-mode runs find.
type HostHistory struct {
	Template int64
	Check    bool
}

// maxDriftDiff bounds the diff a drift keeps; a larger one says only that
// it was too large.
const maxDriftDiff = 256 << 10

type hostResult struct {
	host             int64
	play, role, task string
	status           string
	counter          int
	created          time.Time
	diff             []byte
}

// resultRank orders a task's results on a host, as the job page's matrix
// does: a task that ran more than once keeps the worst.
var resultRank = map[string]int{"skipped": 1, "ok": 2, "changed": 3, "failed": 4, "unreachable": 5}

// rankSQL is resultRank in SQL, for a status column.
const rankSQL = "array_position(ARRAY['skipped', 'ok', 'changed', 'failed', 'unreachable'], %s)"

// taskResult is the status of a host's task result event, or "" for other
// events. A failure whose errors were ignored counts as ok.
func taskResult(e Event) string {
	switch e.Event {
	case "runner_on_unreachable":
		return "unreachable"
	case "runner_on_failed":
		if ignored, _ := e.EventData["ignore_errors"].(bool); ignored {
			return "ok"
		}
		return "failed"
	case "runner_on_skipped":
		return "skipped"
	case "runner_on_ok":
		if e.Changed {
			return "changed"
		}
		return "ok"
	}
	return ""
}

// taskDiff is the part of a result's event data that shows what it changed
// with diff mode: res.diff and each loop item's, with the paths they name.
func taskDiff(data map[string]any) []byte {
	res, _ := data["res"].(map[string]any)
	if res == nil {
		return nil
	}
	pick := func(r map[string]any) map[string]any {
		if r["diff"] == nil {
			return nil
		}
		out := map[string]any{"diff": r["diff"]}
		for _, k := range []string{"dest", "path", "item"} {
			if v, ok := r[k]; ok {
				out[k] = v
			}
		}
		return out
	}
	out := pick(res)
	var items []any
	if list, ok := res["results"].([]any); ok {
		for _, it := range list {
			if m, ok := it.(map[string]any); ok {
				if p := pick(m); p != nil {
					items = append(items, p)
				}
			}
		}
	}
	if items != nil {
		if out == nil {
			out = map[string]any{}
		}
		out["results"] = items
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	if len(b) > maxDriftDiff {
		return []byte(`{"too_large": true}`)
	}
	return b
}

// recordHistory stores a batch's task results on hosts, and opens or
// clears their drift. Its errors are logged: the events are stored.
func (o *Output) recordHistory(ctx context.Context, batch []Event) {
	h := o.History
	type key struct {
		host             int64
		play, role, task string
	}
	byKey := map[key]int{}
	var results []hostResult
	for _, e := range batch {
		s := taskResult(e)
		if s == "" || e.HostID == nil {
			continue
		}
		r := hostResult{host: *e.HostID, play: e.Play, role: e.Role, task: e.Task, status: s, counter: e.Counter, created: e.Created}
		if h.Check && s == "changed" {
			r.diff = taskDiff(e.EventData)
		}
		k := key{r.host, r.play, r.role, r.task}
		if i, ok := byKey[k]; ok {
			if resultRank[s] > resultRank[results[i].status] {
				results[i] = r
			}
			continue
		}
		byKey[k] = len(results)
		results = append(results, r)
	}
	if len(results) == 0 {
		return
	}
	n := len(results)
	hosts, counters := make([]int64, n), make([]int32, n)
	plays, roles, tasks, statuses := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	created := make([]time.Time, n)
	for i, r := range results {
		hosts[i], plays[i], roles[i], tasks[i], statuses[i], counters[i], created[i] = r.host, r.play, r.role, r.task, r.status, int32(r.counter), r.created
	}
	rank := func(col string) string { return fmt.Sprintf(rankSQL, col) }
	if _, err := o.db.Exec(ctx, `INSERT INTO host_task_results (job_id, host_id, unified_job_template_id, play, role, task, status, check_mode, counter, created)
		SELECT $1, u.host, $2, u.play, u.role, u.task, u.status, $3, u.counter, u.created
		FROM unnest($4::bigint[], $5::text[], $6::text[], $7::text[], $8::text[], $9::int[], $10::timestamptz[]) AS u(host, play, role, task, status, counter, created)
		WHERE EXISTS (SELECT 1 FROM hosts WHERE id = u.host)
		ON CONFLICT (job_id, host_id, play, role, task) DO UPDATE SET status = EXCLUDED.status, counter = EXCLUDED.counter, created = EXCLUDED.created
		WHERE `+rank("EXCLUDED.status")+` > `+rank("host_task_results.status"),
		o.jobID, h.Template, h.Check, hosts, plays, roles, tasks, statuses, counters, created); err != nil {
		slog.Error("recording host task results", "job", o.jobID, "err", err)
		return
	}
	var ok, drifted []hostResult
	for _, r := range results {
		switch {
		case r.status == "ok":
			ok = append(ok, r)
		case r.status == "changed" && h.Check:
			drifted = append(drifted, r)
		}
	}
	if len(ok) > 0 {
		hosts, plays, roles, tasks, created := columns(ok)
		if _, err := o.db.Exec(ctx, `DELETE FROM host_drift d USING unnest($3::bigint[], $4::text[], $5::text[], $6::text[], $7::timestamptz[]) AS u(host, play, role, task, created)
			WHERE d.host_id = u.host AND d.unified_job_template_id = $2 AND d.play = u.play AND d.role = u.role AND d.task = u.task
			AND d.last_job_id IS DISTINCT FROM $1 AND d.last_seen <= u.created`, o.jobID, h.Template, hosts, plays, roles, tasks, created); err != nil {
			slog.Error("clearing host drift", "job", o.jobID, "err", err)
		}
	}
	if len(drifted) > 0 {
		hosts, plays, roles, tasks, created := columns(drifted)
		diffs := make([]*string, len(drifted))
		for i, r := range drifted {
			if r.diff != nil {
				s := string(r.diff)
				diffs[i] = &s
			}
		}
		if _, err := o.db.Exec(ctx, `INSERT INTO host_drift (host_id, inventory_id, unified_job_template_id, play, role, task, since, job_id, last_job_id, last_seen, diff)
			SELECT u.host, h.inventory_id, $2, u.play, u.role, u.task, u.created, $1, $1, u.created, u.diff::jsonb
			FROM unnest($3::bigint[], $4::text[], $5::text[], $6::text[], $7::timestamptz[], $8::text[]) AS u(host, play, role, task, created, diff)
			JOIN hosts h ON h.id = u.host
			WHERE EXISTS (SELECT 1 FROM unified_job_templates WHERE id = $2)
			ON CONFLICT (host_id, unified_job_template_id, play, role, task) DO UPDATE SET last_job_id = EXCLUDED.last_job_id,
				last_seen = EXCLUDED.last_seen, diff = EXCLUDED.diff
			WHERE host_drift.last_seen <= EXCLUDED.last_seen`, o.jobID, h.Template, hosts, plays, roles, tasks, created, diffs); err != nil {
			slog.Error("recording host drift", "job", o.jobID, "err", err)
		}
	}
}

func columns(rs []hostResult) (hosts []int64, plays, roles, tasks []string, created []time.Time) {
	for _, r := range rs {
		hosts, plays, roles, tasks, created = append(hosts, r.host), append(plays, r.play), append(roles, r.role), append(tasks, r.task), append(created, r.created)
	}
	return
}
