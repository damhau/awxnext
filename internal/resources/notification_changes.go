package resources

import (
	"context"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// changeMessages are the default messages of the change events.
var changeMessages = map[string]string{
	"first_error": "{{ job_friendly_name }} #{{ job.id }} '{{ job.name }}' {{ job.status }} after a success: {{ url }}",
	"recovered":   "{{ job_friendly_name }} #{{ job.id }} '{{ job.name }}' {{ job.status }} after a failure: {{ url }}",
	"slow":        "{{ job_friendly_name }} #{{ job.id }} '{{ job.name }}' took {{ job.elapsed }} s, more than twice its median of {{ median_elapsed }} s: {{ url }}",
}

// A run is slow when it takes more than slowFactor times the median of the
// template's last slowRuns successful runs, once there are slowMinRuns.
const (
	slowFactor  = 2
	slowRuns    = 10
	slowMinRuns = 5
)

// countedRuns are a template's runs that finished before a given one, the
// slices of a sliced job aside.
const countedRuns = `unified_job_template_id = $1 AND (finished, id) < ($2, $3) AND NOT (type = 'job' AND job_slice_count > 1)`

// notifyChanges sends a finished run's change events.
func notifyChanges(ctx context.Context, m *jobs.Manager, row jobs.Row) {
	if !countsForChanges(row) {
		return
	}
	if len(linkedNotifications(ctx, m.DB(), jobNotificationTargets(ctx, m.DB(), row), changeEvents...)) == 0 {
		return
	}
	events, median := resultChanges(ctx, m.DB(), row)
	for _, ev := range events {
		var extra map[string]any
		if ev == "slow" {
			extra = map[string]any{"median_elapsed": median}
		}
		notifyJob(ctx, m, row, ev, extra)
	}
}

// countsForChanges reports whether a finished run has change events: a run
// of a template that succeeded or failed, and not the slice of a sliced job.
func countsForChanges(row jobs.Row) bool {
	switch row.Str("status") {
	case "successful", "failed", "error":
	default:
		return false
	}
	return row.Int("unified_job_template_id") != 0 && !(row.Str("type") == "job" && row.Int("job_slice_count") > 1)
}

// resultChanges are the change events of a finished run, and the median it
// was slow against. first_error and recovered compare it with the template's
// previous run. slow fires on a slow run when the previous one wasn't slow.
func resultChanges(ctx context.Context, db api.DBTX, row jobs.Row) ([]string, float64) {
	id := row.ID()
	var elapsed float64
	var finished time.Time
	if err := db.QueryRow(ctx, `SELECT elapsed::float8, finished FROM unified_jobs WHERE id = $1 AND finished IS NOT NULL`, id).Scan(&elapsed, &finished); err != nil {
		return nil, 0
	}
	ujt := row.Int("unified_job_template_id")
	var prevStatus string
	var prevElapsed float64
	hasPrev := db.QueryRow(ctx, `SELECT status, elapsed::float8 FROM unified_jobs WHERE `+countedRuns+` AND status IN ('successful', 'failed', 'error')
		ORDER BY finished DESC, id DESC LIMIT 1`, ujt, finished, id).Scan(&prevStatus, &prevElapsed) == nil
	// One more than slowRuns: when the previous run succeeded, its own
	// baseline is the slowRuns before it.
	var succeeded []float64
	rows, err := db.Query(ctx, `SELECT elapsed::float8 FROM unified_jobs WHERE `+countedRuns+` AND status = 'successful'
		ORDER BY finished DESC, id DESC LIMIT `+strconv.Itoa(slowRuns+1), ujt, finished, id)
	if err != nil {
		return nil, 0
	}
	for rows.Next() {
		var e float64
		if rows.Scan(&e) == nil {
			succeeded = append(succeeded, e)
		}
	}
	rows.Close()

	var events []string
	ok := row.Str("status") == "successful"
	if hasPrev {
		switch was := prevStatus == "successful"; {
		case was && !ok:
			events = append(events, "first_error")
		case !was && ok:
			events = append(events, "recovered")
		}
	}
	slow, median := slowAgainst(elapsed, succeeded)
	if slow && hasPrev {
		before := succeeded
		if prevStatus == "successful" {
			before = succeeded[1:]
		}
		if prevSlow, _ := slowAgainst(prevElapsed, before); prevSlow {
			slow = false
		}
	}
	if slow {
		events = append(events, "slow")
	}
	return events, median
}

// slowAgainst reports whether a run that took elapsed seconds is slow
// against the successful runs before it, latest first, and their median.
func slowAgainst(elapsed float64, before []float64) (bool, float64) {
	if len(before) > slowRuns {
		before = before[:slowRuns]
	}
	if len(before) < slowMinRuns {
		return false, 0
	}
	s := slices.Sorted(slices.Values(before))
	median := s[len(s)/2]
	if len(s)%2 == 0 {
		median = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	median = math.Round(median*1000) / 1000
	return median > 0 && elapsed > slowFactor*median, median
}
