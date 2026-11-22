package resources

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/damhau/awxnext/internal/api"
)

// A host's task history (M12-04): each task's results on the host across
// jobs, from host_task_results, with the flags they raise.

// historyDays is the history the host's page shows by default.
const historyDays = 5

// notIdempotentRuns is how many runs, all changed, flag a task as likely
// not idempotent.
const notIdempotentRuns = 3

type historyResult struct {
	Job     int64     `json:"job"`
	Status  string    `json:"status"`
	Check   bool      `json:"check_mode"`
	Counter int       `json:"counter"`
	Created time.Time `json:"created"`
}

type historyTask struct {
	Play    string          `json:"play"`
	Role    string          `json:"role"`
	Task    string          `json:"task"`
	Results []historyResult `json:"results"`
	Flags   []Obj           `json:"flags"`
}

type historyTemplate struct {
	ID    int64          `json:"id"`
	Name  string         `json:"name"`
	Tasks []*historyTask `json:"tasks"`
	last  time.Time
}

type historyJob struct {
	revision, extraVars string
	created             time.Time
}

type taskKey struct {
	template         int64
	play, role, task string
}

func registerHostHistory() {
	api.Lookup("hosts").Actions = append(api.Lookup("hosts").Actions, &api.Action{Name: "task_history", Methods: []string{"GET"}, Handler: handleHostHistory})
	api.Register(&Resource{
		Name:         "host_drift",
		Type:         "host_drift",
		Table:        "host_drift",
		Verbose:      "Host Drift",
		NoName:       true,
		NoTimestamps: true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "-since",
		SearchFields: []string{"task", "play", "role"},
		Fields: []*Field{
			{Name: "host", Type: api.TFK, Ref: "hosts", ReadOnly: true},
			{Name: "inventory", Type: api.TFK, Ref: "inventories", ReadOnly: true},
			{Name: "job_template", Column: "unified_job_template_id", Type: api.TFK, Ref: "job_templates", ReadOnly: true},
			{Name: "play", Type: api.TString, ReadOnly: true},
			{Name: "role", Type: api.TString, ReadOnly: true},
			{Name: "task", Type: api.TString, ReadOnly: true},
			{Name: "since", Type: api.TDateTime, ReadOnly: true, Help: "When a check-mode run first found the task would change the host."},
			{Name: "job", Type: api.TFK, Ref: "jobs", ReadOnly: true, Nullable: true, Help: "The run that found it."},
			{Name: "last_job", Type: api.TFK, Ref: "jobs", ReadOnly: true, Nullable: true, Help: "The latest run that still found it."},
			{Name: "last_seen", Type: api.TDateTime, ReadOnly: true},
			{Name: "diff", Type: api.TJSON, ReadOnly: true, NoFilter: true, Help: "What the latest check-mode run would change, with diff mode: the result's diff, and each loop item's."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.host_id IN (SELECT t.id FROM hosts t WHERE " + api.ViewPredicate(c, api.Lookup("hosts"), arg) + ")"
		},
	})
}

// handleHostHistory serves /hosts/N/task_history/: the host's task results
// over the last ?days= (5 by default, at most HOST_HISTORY_DAYS), from the
// jobs the user can see, by template and task, with their flags.
func handleHostHistory(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	id, _ := api.ID(obj["id"])
	keep := c.App.Settings.Int(c, "HOST_HISTORY_DAYS")
	days := historyDays
	if s := r.URL.Query().Get("days"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			api.WriteError(w, api.FieldError("days", "A positive number of days."))
			return
		}
		days = n
	}
	days = min(days, max(keep, 1))
	until := time.Now()
	since := until.Add(-time.Duration(days) * 24 * time.Hour)
	templates, err := hostHistory(c, id, since)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, Obj{"days": days, "since": since, "until": until, "retention_days": keep, "templates": templates})
}

func hostHistory(c *Ctx, host int64, since time.Time) ([]*historyTemplate, error) {
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	where := "r.host_id = " + arg(host) + " AND r.created >= " + arg(since)
	if !c.User.Superuser && !c.User.Auditor {
		where += " AND r.job_id IN (SELECT t.id FROM unified_jobs t WHERE t.type = 'job' AND (" + api.ViewPredicate(c, api.Lookup("jobs"), arg) + "))"
	}
	rows, err := c.DB().Query(c, `SELECT r.job_id, r.unified_job_template_id, coalesce(t.name, ''), r.play, r.role, r.task, r.status, r.check_mode,
		r.counter, r.created, j.scm_revision, j.extra_vars, j.created
		FROM host_task_results r JOIN unified_jobs j ON j.id = r.job_id LEFT JOIN unified_job_templates t ON t.id = r.unified_job_template_id
		WHERE `+where+` ORDER BY r.created, r.job_id`, args...)
	if err != nil {
		return nil, err
	}
	byTemplate := map[int64]*historyTemplate{}
	byTask := map[taskKey]*historyTask{}
	jobs := map[int64]historyJob{}
	for rows.Next() {
		var res historyResult
		var k taskKey
		var name string
		var j historyJob
		if err := rows.Scan(&res.Job, &k.template, &name, &k.play, &k.role, &k.task, &res.Status, &res.Check, &res.Counter, &res.Created,
			&j.revision, &j.extraVars, &j.created); err != nil {
			rows.Close()
			return nil, err
		}
		jobs[res.Job] = j
		tpl := byTemplate[k.template]
		if tpl == nil {
			tpl = &historyTemplate{ID: k.template, Name: name}
			byTemplate[k.template] = tpl
		}
		tpl.last = res.Created
		t := byTask[k]
		if t == nil {
			t = &historyTask{Play: k.play, Role: k.role, Task: k.task, Flags: []Obj{}}
			byTask[k] = t
			tpl.Tasks = append(tpl.Tasks, t)
		}
		t.Results = append(t.Results, res)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k, t := range byTask {
		flags, err := taskFlags(c, k.template, t.Results, jobs)
		if err != nil {
			return nil, err
		}
		t.Flags = flags
	}
	if err := openDrift(c, host, byTemplate, byTask); err != nil {
		return nil, err
	}
	out := make([]*historyTemplate, 0, len(byTemplate))
	for _, tpl := range byTemplate {
		sortTasks(tpl)
		out = append(out, tpl)
	}
	slices.SortFunc(out, func(a, b *historyTemplate) int { return cmp.Or(b.last.Compare(a.last), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}

// sortTasks orders a template's tasks as its latest run ran them, then
// those it didn't run, latest first.
func sortTasks(tpl *historyTemplate) {
	latest := func(t *historyTask) historyResult {
		if len(t.Results) == 0 {
			return historyResult{}
		}
		return t.Results[len(t.Results)-1]
	}
	slices.SortStableFunc(tpl.Tasks, func(a, b *historyTask) int {
		la, lb := latest(a), latest(b)
		return cmp.Or(cmp.Compare(lb.Job, la.Job), cmp.Compare(la.Counter, lb.Counter))
	})
}

// taskFlags are what a task's results on a host tell, oldest first:
//   - not_idempotent: it reported changed on every run, notIdempotentRuns
//     at least, check-mode runs aside;
//   - new_failure: its latest result failed after a success;
//   - drift: a changed between two ok runs, check-mode runs aside, with no
//     change to the template, its project's revision or the extra variables
//     between the ok run and the changed one. Ansible put back what someone
//     changed on the host.
func taskFlags(c *Ctx, template int64, results []historyResult, jobs map[int64]historyJob) ([]Obj, error) {
	flags := []Obj{}
	var runs, all []historyResult
	for _, r := range results {
		if r.Status == "skipped" {
			continue
		}
		all = append(all, r)
		if !r.Check {
			runs = append(runs, r)
		}
	}
	if len(runs) >= notIdempotentRuns && !slices.ContainsFunc(runs, func(r historyResult) bool { return r.Status != "changed" }) {
		flags = append(flags, Obj{"type": "not_idempotent", "runs": len(runs), "job": runs[len(runs)-1].Job})
	}
	bad := func(r historyResult) bool { return r.Status == "failed" || r.Status == "unreachable" }
	if n := len(all); n > 0 && bad(all[n-1]) {
		s := n - 1
		for s > 0 && bad(all[s-1]) {
			s--
		}
		if s > 0 {
			flags = append(flags, Obj{"type": "new_failure", "job": all[s].Job, "since": all[s].Created, "last_success": all[s-1].Job, "failures": n - s})
		}
	}
	for i := len(runs) - 2; i >= 1; i-- {
		before, changed, after := runs[i-1], runs[i], runs[i+1]
		if changed.Status != "changed" || before.Status != "ok" || after.Status != "ok" {
			continue
		}
		same, err := sameRun(c, template, jobs[before.Job], jobs[changed.Job])
		if err != nil {
			return nil, err
		}
		if same {
			flags = append(flags, Obj{"type": "drift", "open": false, "check_mode": false, "job": changed.Job, "at": changed.Created,
				"previous": before.Job})
			break
		}
	}
	return flags, nil
}

// sameRun reports whether two runs of a template ran the same thing: the
// same project revision and extra variables, and no edit to the template
// between their launches.
func sameRun(c *Ctx, template int64, a, b historyJob) (bool, error) {
	if a.revision != b.revision || a.extraVars != b.extraVars {
		return false, nil
	}
	var edited bool
	err := c.DB().QueryRow(c, `SELECT EXISTS (SELECT 1 FROM object_versions WHERE resource = 'job_templates' AND object_id = $1 AND created > $2 AND created <= $3)`,
		template, a.created, b.created).Scan(&edited)
	return !edited, err
}

// openDrift adds the host's open drift to its tasks' flags, and the tasks
// it names that have no result in the window.
func openDrift(c *Ctx, host int64, byTemplate map[int64]*historyTemplate, byTask map[taskKey]*historyTask) error {
	rows, err := c.DB().Query(c, `SELECT d.id, d.unified_job_template_id, coalesce(t.name, ''), d.play, d.role, d.task, d.since, d.job_id, d.last_job_id, d.last_seen, d.diff
		FROM host_drift d LEFT JOIN unified_job_templates t ON t.id = d.unified_job_template_id WHERE d.host_id = $1 ORDER BY d.since`, host)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var k taskKey
		var name string
		var since, seen time.Time
		var job, last *int64
		var diff []byte
		if err := rows.Scan(&id, &k.template, &name, &k.play, &k.role, &k.task, &since, &job, &last, &seen, &diff); err != nil {
			return err
		}
		tpl := byTemplate[k.template]
		if tpl == nil {
			tpl = &historyTemplate{ID: k.template, Name: name, last: seen}
			byTemplate[k.template] = tpl
		}
		t := byTask[k]
		if t == nil {
			t = &historyTask{Play: k.play, Role: k.role, Task: k.task, Results: []historyResult{}, Flags: []Obj{}}
			byTask[k] = t
			tpl.Tasks = append(tpl.Tasks, t)
		}
		var d any
		if diff != nil {
			json.Unmarshal(diff, &d)
		}
		t.Flags = append(t.Flags, Obj{"type": "drift", "open": true, "check_mode": true, "id": id, "since": since, "job": job, "last_job": last,
			"last_seen": seen, "diff": d})
	}
	return rows.Err()
}
