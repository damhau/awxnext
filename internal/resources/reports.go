package resources

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/jackc/pgx/v5"
)

// Reports, awxnext's /api/v2/reports/: how the runs of job templates did over
// a period, read from report_runs and report_host_days (migration 0055),
// which outlive cleanup_jobs. Each report counts only the runs the user may
// read, by the jobs resource's own rule. Days are UTC days.

var reportLaunchTypes = []string{"manual", "relaunch", "callback", "scheduled", "dependency", "workflow", "webhook", "sync", "scm", "portal", "event_rule"}

var reportFilters = []struct{ key, column string }{
	{"organization", "organization_id"},
	{"template", "unified_job_template_id"},
	{"inventory", "inventory_id"},
	{"project", "project_id"},
}

const (
	reportSucceeded = "status = 'successful'"
	reportFailed    = "status IN ('failed', 'error')"
	reportCanceled  = "status = 'canceled'"
	reportMaxDays   = 3650
)

func installReports(app *api.App) {
	reports := []struct {
		name string
		fn   func(*Ctx, *reportQuery) (Obj, error)
	}{
		{"totals", totalsReport},
		{"templates", templatesReport},
		{"trends", trendsReport},
		{"hosts", hostsReport},
		{"launches", launchesReport},
	}
	app.Handle("GET", "/api/v2/reports/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		out := Obj{}
		for _, rep := range reports {
			out[rep.name] = "/api/v2/reports/" + rep.name + "/"
		}
		api.WriteJSON(w, 200, out)
	})
	for _, rep := range reports {
		app.Handle("GET", "/api/v2/reports/"+rep.name+"/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
			q, err := parseReportQuery(c, r)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			out, err := runReport(c, q, rep.fn)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			out["period"] = q.period()
			api.WriteJSON(w, 200, out)
		})
	}
}

// runReport runs a report in a read-only transaction of its own, which
// plans each query for its parameters (a cached generic plan doesn't see how
// many days and keys it reads, and runs on millions of rows without parallel
// workers) and gives it the memory to aggregate them in hashes rather than
// in sorts on disk. benchmarks.md has the numbers.
func runReport(c *Ctx, q *reportQuery, fn func(*Ctx, *reportQuery) (Obj, error)) (Obj, error) {
	tx, err := c.App.DB.BeginTx(c, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(c, `SELECT set_config('plan_cache_mode', 'force_custom_plan', true), set_config('work_mem', '32MB', true)`); err != nil {
		return nil, err
	}
	rc := *c
	rc.Tx = tx
	if err := q.loadVisible(&rc); err != nil {
		return nil, err
	}
	return fn(&rc, q)
}

// reportQuery is a report's period and filters.
type reportQuery struct {
	fromDay, toDay time.Time // both included
	keptSince      time.Time
	interval       string
	order          string
	filters        map[string]int64
	launchType     string
	// visible holds the keys of the runs the user may read, nil when they
	// may read every run.
	visible *reportKeys
}

// reportKeys are report_keys rows, one per index, with 0 for NULL.
type reportKeys struct {
	templates, orgs, users []int64
	portal                 []bool
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func parseReportQuery(c *Ctx, r *http.Request) (*reportQuery, error) {
	v := r.URL.Query()
	fe := api.FieldErrors{}
	today := utcDay(time.Now())
	q := &reportQuery{filters: map[string]int64{}, toDay: today}
	if v.Get("from") != "" || v.Get("to") != "" {
		if v.Get("period") != "" {
			fe.Add("period", "Give a period, or from and to, not both.")
		}
		from, err := time.Parse(time.DateOnly, v.Get("from"))
		if err != nil {
			fe.Add("from", "Enter a date as YYYY-MM-DD.")
		}
		if s := v.Get("to"); s != "" {
			to, err := time.Parse(time.DateOnly, s)
			if err != nil {
				fe.Add("to", "Enter a date as YYYY-MM-DD.")
			}
			q.toDay = to
		}
		q.fromDay = from
		if len(fe) == 0 && q.fromDay.After(q.toDay) {
			fe.Add("to", "Ensure this date is on or after from.")
		}
		if len(fe) == 0 && q.toDay.Sub(q.fromDay) >= reportMaxDays*24*time.Hour {
			fe.Add("from", fmt.Sprintf("Ensure the period is at most %d days.", reportMaxDays))
		}
	} else {
		days := 30
		if s := v.Get("period"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || (n != 7 && n != 30 && n != 90) {
				fe.Add("period", "Choose 7, 30 or 90 days, or give from and to.")
			}
			days = n
		}
		q.fromDay = today.AddDate(0, 0, 1-days)
	}
	for _, key := range []string{"organization", "template", "inventory", "project", "label"} {
		if s := v.Get(key); s != "" {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil || id < 1 {
				fe.Add(key, "Enter the ID of one object.")
			}
			q.filters[key] = id
		}
	}
	if s := v.Get("launch_type"); s != "" {
		if !slices.Contains(reportLaunchTypes, s) {
			fe.Add("launch_type", "Choose one of "+strings.Join(reportLaunchTypes, ", ")+".")
		}
		q.launchType = s
	}
	switch q.interval = v.Get("interval"); q.interval {
	case "":
		q.interval = "day"
		if q.days() > 31 {
			q.interval = "week"
		}
	case "day", "week":
	default:
		fe.Add("interval", "Choose day or week.")
	}
	q.order = v.Get("order_by")
	if q.order != "" && !slices.Contains(templateOrders, strings.TrimPrefix(q.order, "-")) {
		fe.Add("order_by", "Choose one of "+strings.Join(templateOrders, ", ")+", with - to reverse.")
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	keep := c.App.Settings.Int(c, "REPORT_HISTORY_DAYS")
	q.keptSince = today.AddDate(0, 0, 1-max(keep, 1))
	return q, nil
}

func (q *reportQuery) to() time.Time { return q.toDay.AddDate(0, 0, 1) }
func (q *reportQuery) days() int     { return int(math.Round(q.to().Sub(q.fromDay).Hours() / 24)) }

func (q *reportQuery) period() Obj {
	prevFrom := q.fromDay.AddDate(0, 0, -q.days())
	return Obj{
		"from": q.fromDay.Format(time.DateOnly), "to": q.toDay.Format(time.DateOnly), "days": q.days(),
		"previous_from": prevFrom.Format(time.DateOnly), "previous_to": q.fromDay.AddDate(0, 0, -1).Format(time.DateOnly),
		"interval": q.interval, "kept_since": q.keptSince.Format(time.DateOnly), "cut_short": q.fromDay.Before(q.keptSince),
	}
}

type sqlArgs struct{ args []any }

func (s *sqlArgs) arg(v any) string {
	s.args = append(s.args, v)
	return fmt.Sprintf("$%d", len(s.args))
}

// cond holds the filters and the user's rights on alias t, a report_runs or
// a report_host_days row.
func (q *reportQuery) cond(c *Ctx, a *sqlArgs) string {
	parts := []string{q.visibleRows(a)}
	for _, f := range reportFilters {
		if id, ok := q.filters[f.key]; ok {
			parts = append(parts, "t."+f.column+" = "+a.arg(id))
		}
	}
	if id, ok := q.filters["label"]; ok {
		parts = append(parts, "t.unified_job_template_id IN (SELECT l.unified_job_template_id FROM unified_job_template_labels l WHERE l.label_id = "+a.arg(id)+")")
	}
	if q.launchType != "" {
		parts = append(parts, "t.launch_type = "+a.arg(q.launchType))
	}
	return strings.Join(parts, " AND ")
}

// loadVisible finds the keys (report_keys) of the period and of the one
// before it whose runs the user may read as jobs. The jobs' view runs once
// per key, a few per template and launcher, instead of on each row: there,
// its role lookups take seconds on a large history, and keep the reports'
// queries from running in parallel.
func (q *reportQuery) loadVisible(c *Ctx) error {
	a := &sqlArgs{}
	view := api.ViewPredicate(c, api.Lookup("jobs"), a.arg)
	if view == "TRUE" {
		return nil
	}
	rows, err := c.DB().Query(c, `SELECT coalesce(t.unified_job_template_id, 0), coalesce(t.organization_id, 0), coalesce(t.created_by_id, 0), t.portal
		FROM (SELECT DISTINCT k.unified_job_template_id, k.organization_id, k.created_by_id, k.portal,
			CASE WHEN k.portal THEN 0 END::bigint AS catalog_request_id
			FROM report_keys k WHERE k.day BETWEEN `+a.arg(q.fromDay.AddDate(0, 0, -q.days()))+` AND `+a.arg(q.toDay)+`) t
		WHERE `+view, a.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	k := &reportKeys{templates: []int64{}, orgs: []int64{}, users: []int64{}, portal: []bool{}}
	for rows.Next() {
		var tmpl, org, user int64
		var portal bool
		if err := rows.Scan(&tmpl, &org, &user, &portal); err != nil {
			return err
		}
		k.templates, k.orgs, k.users, k.portal = append(k.templates, tmpl), append(k.orgs, org), append(k.users, user), append(k.portal, portal)
	}
	q.visible = k
	return rows.Err()
}

// visibleRows keeps the rows (alias t) whose key loadVisible found.
func (q *reportQuery) visibleRows(a *sqlArgs) string {
	k := q.visible
	if k == nil {
		return "TRUE"
	}
	return `(coalesce(t.unified_job_template_id, 0), coalesce(t.organization_id, 0), coalesce(t.created_by_id, 0), t.catalog_request_id IS NOT NULL) IN (
		SELECT * FROM unnest(` + a.arg(k.templates) + `::bigint[], ` + a.arg(k.orgs) + `::bigint[], ` + a.arg(k.users) + `::bigint[], ` + a.arg(k.portal) + `::boolean[]))`
}

// runs selects the runs that finished between from and to (excluded).
func (q *reportQuery) runs(c *Ctx, a *sqlArgs, from, to time.Time) string {
	return "SELECT t.* FROM report_runs t WHERE " + q.cond(c, a) + " AND t.finished >= " + a.arg(from) + " AND t.finished < " + a.arg(to)
}

// hostDays holds the conditions on the host results (alias t) of the
// period's days.
func (q *reportQuery) hostDays(c *Ctx, a *sqlArgs) string {
	return q.cond(c, a) + " AND t.day BETWEEN " + a.arg(q.fromDay) + " AND " + a.arg(q.toDay)
}

// outcomes counts runs, successes, failures and cancellations, in this order.
const outcomes = `count(*), count(*) FILTER (WHERE ` + reportSucceeded + `), count(*) FILTER (WHERE ` + reportFailed + `), count(*) FILTER (WHERE ` + reportCanceled + `)`

type outcomeCounts struct {
	Runs       int64 `json:"runs"`
	Successful int64 `json:"successful"`
	Failed     int64 `json:"failed"`
	Canceled   int64 `json:"canceled"`
}

func (o *outcomeCounts) dest() []any { return []any{&o.Runs, &o.Successful, &o.Failed, &o.Canceled} }

// successRate leaves canceled runs out; nil without a finished run.
func successRate(ok, failed int64) *float64 {
	if ok+failed == 0 {
		return nil
	}
	return round(float64(ok)/float64(ok+failed), 4)
}

func round(v float64, places int) *float64 {
	p := math.Pow(10, float64(places))
	r := math.Round(v*p) / p
	return &r
}

func roundPtr(v *float64, places int) *float64 {
	if v == nil {
		return nil
	}
	return round(*v, places)
}

func totalsReport(c *Ctx, q *reportQuery) (Obj, error) {
	var o outcomeCounts
	var seconds float64
	a := &sqlArgs{}
	if err := c.DB().QueryRow(c, `SELECT `+outcomes+`, coalesce(sum(elapsed), 0)::float8 FROM (`+q.runs(c, a, q.fromDay, q.to())+`) r`, a.args...).
		Scan(append(o.dest(), &seconds)...); err != nil {
		return nil, err
	}
	var hostRuns, hosts int64
	a = &sqlArgs{}
	if err := c.DB().QueryRow(c, `SELECT coalesce(sum(h.runs), 0), count(*)
		FROM (SELECT t.host_name, sum(t.runs) AS runs FROM report_host_days t WHERE `+q.hostDays(c, a)+` GROUP BY 1) h`, a.args...).Scan(&hostRuns, &hosts); err != nil {
		return nil, err
	}
	a = &sqlArgs{}
	rows, err := c.DB().Query(c, `SELECT r.project_id, p.name, count(*) FROM (`+q.runs(c, a, q.fromDay, q.to())+`) r
		LEFT JOIN unified_job_templates p ON p.id = r.project_id WHERE r.project_id IS NOT NULL GROUP BY 1, 2 ORDER BY 3 DESC, 1 LIMIT 5`, a.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []Obj{}
	for rows.Next() {
		var id *int64
		var name *string
		var n int64
		if err := rows.Scan(&id, &name, &n); err != nil {
			return nil, err
		}
		projects = append(projects, Obj{"project": id, "name": name, "runs": n})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return Obj{
		"runs": o.Runs, "successful": o.Successful, "failed": o.Failed, "canceled": o.Canceled,
		"success_rate": successRate(o.Successful, o.Failed), "hours": *round(seconds/3600, 2),
		"host_runs": hostRuns, "hosts": hosts, "top_projects": projects,
	}, nil
}

type templateReportRow struct {
	Template *int64 `json:"template"`
	Name     string `json:"name"`
	outcomeCounts
	SuccessRate         *float64     `json:"success_rate"`
	PreviousSuccessRate *float64     `json:"previous_success_rate"`
	SuccessRateChange   *float64     `json:"success_rate_change"`
	MedianDuration      *float64     `json:"median_duration"`
	P95Duration         *float64     `json:"p95_duration"`
	Flips               int64        `json:"flips"`
	LastFailure         *lastFailure `json:"last_failure"`
}

type lastFailure struct {
	Job      int64     `json:"job"`
	Finished time.Time `json:"finished"`
	// Exists says whether the job is still kept: cleanup_jobs deletes it
	// before the report forgets it.
	Exists bool `json:"exists"`
}

var templateOrders = []string{"name", "runs", "failed", "success_rate", "success_rate_change", "median_duration", "p95_duration", "flips", "last_failure"}

// templatesReport lists each template's runs, worst first by default. A flip
// is a run whose result (success or failure) differs from the previous one's.
func templatesReport(c *Ctx, q *reportQuery) (Obj, error) {
	a := &sqlArgs{}
	runs := q.runs(c, a, q.fromDay, q.to())
	prev := q.runs(c, a, q.fromDay.AddDate(0, 0, -q.days()), q.fromDay)
	rows, err := c.DB().Query(c, `WITH runs AS (`+runs+`),
		prev AS (SELECT unified_job_template_id, count(*) FILTER (WHERE `+reportSucceeded+`) ok, count(*) FILTER (WHERE `+reportFailed+`) failed
			FROM (`+prev+`) p GROUP BY 1),
		ordered AS (SELECT unified_job_template_id, `+reportFailed+` AS failed,
			lag(`+reportFailed+`) OVER (PARTITION BY unified_job_template_id ORDER BY finished, job_id) AS before
			FROM runs WHERE NOT `+reportCanceled+`),
		flips AS (SELECT unified_job_template_id, count(*) FILTER (WHERE failed <> before) n FROM ordered GROUP BY 1),
		agg AS (SELECT unified_job_template_id, count(*) runs, count(*) FILTER (WHERE `+reportSucceeded+`) ok,
			count(*) FILTER (WHERE `+reportFailed+`) failed, count(*) FILTER (WHERE `+reportCanceled+`) canceled,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY elapsed) FILTER (WHERE NOT `+reportCanceled+`) median,
			percentile_cont(0.95) WITHIN GROUP (ORDER BY elapsed) FILTER (WHERE NOT `+reportCanceled+`) p95,
			(array_agg(name ORDER BY finished DESC))[1] AS name,
			(array_agg(job_id ORDER BY finished DESC, job_id DESC) FILTER (WHERE `+reportFailed+`))[1] AS last_failure,
			max(finished) FILTER (WHERE `+reportFailed+`) AS last_failure_at
			FROM runs GROUP BY 1)
		SELECT a.unified_job_template_id, coalesce(u.name, a.name), a.runs, a.ok, a.failed, a.canceled, a.median, a.p95,
			coalesce(f.n, 0), p.ok, p.failed, a.last_failure, a.last_failure_at,
			EXISTS (SELECT 1 FROM unified_jobs j WHERE j.id = a.last_failure)
		FROM agg a
		LEFT JOIN flips f ON f.unified_job_template_id IS NOT DISTINCT FROM a.unified_job_template_id
		LEFT JOIN prev p ON p.unified_job_template_id IS NOT DISTINCT FROM a.unified_job_template_id
		LEFT JOIN unified_job_templates u ON u.id = a.unified_job_template_id`, a.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*templateReportRow{}
	for rows.Next() {
		t := &templateReportRow{}
		var prevOK, prevFailed *int64
		var failJob *int64
		var failAt *time.Time
		var exists bool
		if err := rows.Scan(append([]any{&t.Template, &t.Name}, append(t.outcomeCounts.dest(),
			&t.MedianDuration, &t.P95Duration, &t.Flips, &prevOK, &prevFailed, &failJob, &failAt, &exists)...)...); err != nil {
			return nil, err
		}
		t.SuccessRate = successRate(t.Successful, t.Failed)
		if prevOK != nil {
			t.PreviousSuccessRate = successRate(*prevOK, *prevFailed)
		}
		if t.SuccessRate != nil && t.PreviousSuccessRate != nil {
			t.SuccessRateChange = round(*t.SuccessRate-*t.PreviousSuccessRate, 4)
		}
		t.MedianDuration, t.P95Duration = roundPtr(t.MedianDuration, 3), roundPtr(t.P95Duration, 3)
		if failJob != nil {
			t.LastFailure = &lastFailure{Job: *failJob, Finished: *failAt, Exists: exists}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortTemplateRows(out, cmp.Or(q.order, "success_rate"))
	return Obj{"count": len(out), "order_by": cmp.Or(q.order, "success_rate"), "results": out}, nil
}

// sortTemplateRows sorts by one column, empty values last either way, then
// by most failures, most runs and name.
func sortTemplateRows(rows []*templateReportRow, order string) {
	desc := strings.HasPrefix(order, "-")
	key := strings.TrimPrefix(order, "-")
	value := func(t *templateReportRow) *float64 {
		f := func(n int64) *float64 { v := float64(n); return &v }
		switch key {
		case "runs":
			return f(t.Runs)
		case "failed":
			return f(t.Failed)
		case "success_rate":
			return t.SuccessRate
		case "success_rate_change":
			return t.SuccessRateChange
		case "median_duration":
			return t.MedianDuration
		case "p95_duration":
			return t.P95Duration
		case "flips":
			return f(t.Flips)
		case "last_failure":
			if t.LastFailure == nil {
				return nil
			}
			return f(t.LastFailure.Finished.UnixMicro())
		}
		return nil
	}
	slices.SortStableFunc(rows, func(a, b *templateReportRow) int {
		var c int
		if key == "name" {
			c = cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		} else {
			va, vb := value(a), value(b)
			switch {
			case va == nil && vb == nil:
			case va == nil:
				return 1
			case vb == nil:
				return -1
			default:
				c = cmp.Compare(*va, *vb)
			}
		}
		if desc {
			c = -c
		}
		return cmp.Or(c, -cmp.Compare(a.Failed, b.Failed), -cmp.Compare(a.Runs, b.Runs), cmp.Compare(a.Name, b.Name))
	})
}

// trendsReport counts runs per day or week (weeks start on Monday), with
// their median duration: a template's, with the template filter.
func trendsReport(c *Ctx, q *reportQuery) (Obj, error) {
	a := &sqlArgs{}
	rows, err := c.DB().Query(c, `SELECT date_trunc('`+q.interval+`', r.finished AT TIME ZONE 'UTC')::date, `+outcomes+`,
		percentile_cont(0.5) WITHIN GROUP (ORDER BY elapsed) FILTER (WHERE NOT `+reportCanceled+`)
		FROM (`+q.runs(c, a, q.fromDay, q.to())+`) r GROUP BY 1`, a.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type bucket struct {
		Start string `json:"start"`
		outcomeCounts
		FailureRate    *float64 `json:"failure_rate"`
		MedianDuration *float64 `json:"median_duration"`
	}
	found := map[string]*bucket{}
	for rows.Next() {
		var day time.Time
		b := &bucket{}
		if err := rows.Scan(append(append([]any{&day}, b.dest()...), &b.MedianDuration)...); err != nil {
			return nil, err
		}
		b.Start = day.Format(time.DateOnly)
		b.MedianDuration = roundPtr(b.MedianDuration, 3)
		if b.Successful+b.Failed > 0 {
			b.FailureRate = round(float64(b.Failed)/float64(b.Successful+b.Failed), 4)
		}
		found[b.Start] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	start, step := q.fromDay, 1
	if q.interval == "week" {
		start = start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))
		step = 7
	}
	out := []*bucket{}
	for d := start; !d.After(q.toDay); d = d.AddDate(0, 0, step) {
		b := found[d.Format(time.DateOnly)]
		if b == nil {
			b = &bucket{Start: d.Format(time.DateOnly)}
		}
		out = append(out, b)
	}
	return Obj{"results": out}, nil
}

// hostsReport counts the distinct hosts (by name, as host metrics do)
// automated per organization and month, and lists the hosts that failed or
// were unreachable most often.
func hostsReport(c *Ctx, q *reportQuery) (Obj, error) {
	a := &sqlArgs{}
	rows, err := c.DB().Query(c, `SELECT h.organization_id, o.name, h.month, count(*)
		FROM (SELECT DISTINCT t.organization_id, to_char(t.day, 'YYYY-MM') AS month, t.host_name FROM report_host_days t WHERE `+q.hostDays(c, a)+`) h
		LEFT JOIN organizations o ON o.id = h.organization_id
		GROUP BY 1, 2, 3 ORDER BY 3, 2 NULLS LAST, 1`, a.args...)
	if err != nil {
		return nil, err
	}
	automated := []Obj{}
	for rows.Next() {
		var org *int64
		var name *string
		var month string
		var n int64
		if err := rows.Scan(&org, &name, &month, &n); err != nil {
			rows.Close()
			return nil, err
		}
		automated = append(automated, Obj{"organization": org, "name": name, "month": month, "hosts": n})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a = &sqlArgs{}
	rows, err = c.DB().Query(c, `SELECT f.host_name, h.id, i.id, i.name, f.runs, f.failed, f.unreachable
		FROM (SELECT t.host_name, t.host_id, sum(t.runs) AS runs, sum(t.failed) AS failed, sum(t.unreachable) AS unreachable
			FROM report_host_days t WHERE `+q.hostDays(c, a)+`
			GROUP BY 1, 2 HAVING sum(t.failed) + sum(t.unreachable) > 0
			ORDER BY sum(t.failed) + sum(t.unreachable) DESC, sum(t.runs) DESC, 1, 2 LIMIT 50) f
		LEFT JOIN hosts h ON h.id = f.host_id LEFT JOIN inventories i ON i.id = h.inventory_id
		ORDER BY f.failed + f.unreachable DESC, f.runs DESC, f.host_name, f.host_id`, a.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	failing := []Obj{}
	for rows.Next() {
		var name string
		var host, inv *int64
		var invName *string
		var runs, failed, unreachable int64
		if err := rows.Scan(&name, &host, &inv, &invName, &runs, &failed, &unreachable); err != nil {
			return nil, err
		}
		failing = append(failing, Obj{"host_name": name, "host": host, "inventory": inv, "inventory_name": invName,
			"runs": runs, "failed": failed, "unreachable": unreachable})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return Obj{"automated": automated, "most_failed": failing}, nil
}

// launchesReport counts runs by who launched them (a team counts its
// members' runs) and how.
func launchesReport(c *Ctx, q *reportQuery) (Obj, error) {
	query := func(idField, nameField, sql string, a *sqlArgs) ([]Obj, error) {
		rows, err := c.DB().Query(c, sql, a.args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Obj{}
		for rows.Next() {
			var id *int64
			var name *string
			var o outcomeCounts
			if err := rows.Scan(append([]any{&id, &name}, o.dest()...)...); err != nil {
				return nil, err
			}
			row := Obj{nameField: name, "runs": o.Runs, "successful": o.Successful, "failed": o.Failed, "canceled": o.Canceled,
				"success_rate": successRate(o.Successful, o.Failed)}
			if idField != "" {
				row[idField] = id
			}
			out = append(out, row)
		}
		return out, rows.Err()
	}
	a := &sqlArgs{}
	users, err := query("user", "username", `SELECT r.created_by_id, u.username, `+outcomes+` FROM (`+q.runs(c, a, q.fromDay, q.to())+`) r
		LEFT JOIN users u ON u.id = r.created_by_id GROUP BY 1, 2 ORDER BY 3 DESC, 2 NULLS LAST`, a)
	if err != nil {
		return nil, err
	}
	a = &sqlArgs{}
	runs := q.runs(c, a, q.fromDay, q.to())
	teams, err := query("team", "name", `SELECT tm.id, tm.name, `+outcomes+` FROM (`+runs+`) r
		JOIN (SELECT DISTINCT ra.user_id, ra.object_id AS team_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
			WHERE ra.content_type = 'team' AND ra.user_id IS NOT NULL AND 'member_team' = ANY(rd.permissions)) m ON m.user_id = r.created_by_id
		JOIN (SELECT t.id, t.name FROM teams t WHERE `+api.ViewPredicate(c, api.Lookup("teams"), a.arg)+`) tm ON tm.id = m.team_id
		GROUP BY 1, 2 ORDER BY 3 DESC, 2`, a)
	if err != nil {
		return nil, err
	}
	a = &sqlArgs{}
	types, err := query("", "launch_type", `SELECT NULL::bigint, r.launch_type, `+outcomes+` FROM (`+q.runs(c, a, q.fromDay, q.to())+`) r
		GROUP BY 2 ORDER BY 3 DESC, 2`, a)
	if err != nil {
		return nil, err
	}
	return Obj{"by_user": users, "by_team": teams, "by_launch_type": types}, nil
}
