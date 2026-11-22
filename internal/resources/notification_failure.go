package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// failureHostNames is how many hosts {{ failure.hosts }} names; the others
// are counted.
const failureHostNames = 10

// failureMessageLen is the length of {{ failure.message }}, as the job
// page's "Why it failed" panel cuts it.
const failureMessageLen = 600

// failureEventWait is how long a notification waits for a finished run's
// events to be stored.
const failureEventWait = 5 * time.Second

// failedEvents are a run's events of a failed task: failed, and not
// ignored, or unreachable. The UI's firstFailure reads them the same way.
const failedEvents = `job_id = $1 AND ((event = 'runner_on_failed' AND coalesce(event_data->>'ignore_errors', '') <> 'true') OR event = 'runner_on_unreachable')`

// emptyFailure is {{ failure }} for a run with no failed task.
func emptyFailure() map[string]any {
	return map[string]any{"task": "", "play": "", "hosts": "", "host_count": float64(0), "message": "", "path": ""}
}

// jobFailure is the first failed task of a failed run, as the job page's
// "Why it failed" panel finds it: its task, play, the hosts that failed on
// it, the first line of its message and its path in the project. It reads
// the stored events, which are masked. A workflow job has no tasks.
func jobFailure(ctx context.Context, db api.DBTX, row jobs.Row) map[string]any {
	out := emptyFailure()
	switch row.Str("status") {
	case "failed", "error":
	default:
		return out
	}
	if row.Str("type") == "workflow_job" {
		return out
	}
	id := row.ID()
	waitForEvents(ctx, db, id)
	var task, play, stdout string
	var data map[string]any
	if err := db.QueryRow(ctx, `SELECT task, play, stdout, event_data FROM job_events WHERE `+failedEvents+` ORDER BY counter LIMIT 1`, id).
		Scan(&task, &play, &stdout, &data); err != nil {
		return out
	}
	hosts, err := strList(ctx, db, `SELECT host_name FROM job_events WHERE `+failedEvents+` AND task = $2 AND play = $3
		GROUP BY host_name ORDER BY min(counter)`, id, task, play)
	if err != nil {
		return out
	}
	names := hosts
	if len(names) > failureHostNames {
		names = names[:failureHostNames]
	}
	text := strings.Join(names, ", ")
	if more := len(hosts) - len(names); more > 0 {
		text += fmt.Sprintf(" and %d more", more)
	}
	out["task"], out["play"], out["hosts"], out["host_count"] = task, play, text, float64(len(hosts))
	out["message"] = failureMessage(data, stdout)
	out["path"] = projectPath(str(data["task_path"]))
	return out
}

// waitForEvents waits, for failureEventWait at most, until a run's events
// are stored. A run that ended through the job manager has them already.
func waitForEvents(ctx context.Context, db api.DBTX, id int64) {
	deadline := time.Now().Add(failureEventWait)
	for {
		var done bool
		if db.QueryRow(ctx, "SELECT event_processing_finished FROM unified_jobs WHERE id = $1", id).Scan(&done) != nil || done || time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// failureMessage is the module's message, else the first line of the
// event's output, cut to failureMessageLen characters.
func failureMessage(data map[string]any, stdout string) string {
	res, _ := data["res"].(map[string]any)
	msg := ""
	for _, k := range []string{"msg", "stderr", "reason", "module_stderr", "stdout"} {
		if s, ok := res[k].(string); ok && strings.TrimSpace(s) != "" {
			msg = s
			break
		}
	}
	if msg == "" {
		msg, _, _ = strings.Cut(strings.ReplaceAll(ansiRe.ReplaceAllString(stdout, ""), "\r", ""), "\n")
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > failureMessageLen {
		msg = string(r[:failureMessageLen])
	}
	return msg
}

// projectPath is a task path relative to the project, with its line, as
// the UI shows it: playbooks/site.yml:20.
func projectPath(path string) string {
	if i := strings.Index(path, "/project/"); i >= 0 {
		return path[i+len("/project/"):]
	}
	parts := strings.Split(path, "/")
	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}
	return strings.Join(parts, "/")
}

// failureFacts are a Teams card's facts about a run's failed task.
func failureFacts(f map[string]any) [][2]string {
	if str(f["task"]) == "" {
		return nil
	}
	return [][2]string{{"Failed task", str(f["task"])}, {"Play", str(f["play"])}, {"Failed on", str(f["hosts"])},
		{"Error", str(f["message"])}, {"Task path", str(f["path"])}}
}

// strList runs a query of one text column.
func strList(ctx context.Context, db api.DBTX, q string, args ...any) ([]string, error) {
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
