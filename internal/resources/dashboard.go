package resources

import (
	"fmt"
	"net/http"
	"time"

	"github.com/damhau/awxnext/internal/api"
)

// installDashboard adds AWX's /dashboard/ (deprecated in AWX, still called by
// clients) and /dashboard/graphs/jobs/. Every count is of what the user can
// see.
func installDashboard(app *api.App) {
	app.Handle("GET", "/api/v2/dashboard/", dashboard)
	app.Handle("GET", "/api/v2/dashboard/graphs/jobs/", dashboardJobsGraph)
}

// visibleCount counts the rows of a resource the user sees that match the
// conditions (alias t).
func visibleCount(c *Ctx, resName string, conds ...string) (int64, error) {
	var extra []func(api.ArgFunc) string
	for _, cond := range conds {
		cond := cond
		extra = append(extra, func(api.ArgFunc) string { return cond })
	}
	_, n, err := api.List(c, api.Lookup(resName), api.ListParams{Extra: extra, Page: 1, PageSize: 1})
	return n, err
}

func dashboard(c *Ctx, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Warning", `299 awx "This resource has been deprecated and will be removed in a future release."`)
	type count struct {
		res   string
		conds []string
	}
	counts := map[string]count{
		"inventories":         {"inventories", nil},
		"inventories_sourced": {"inventories", []string{"EXISTS (SELECT 1 FROM unified_job_templates s WHERE s.type = 'inventory_source' AND s.inventory_id = t.id)"}},
		"inventories_failed":  {"inventories", []string{"EXISTS (SELECT 1 FROM hosts h JOIN job_host_summaries s ON s.id = h.last_job_host_summary_id WHERE h.inventory_id = t.id AND s.failed)"}},
		"sources_failed":      {"inventory_sources", []string{"t.last_job_failed"}},
		"ec2":                 {"inventory_sources", []string{"t.source = 'ec2'"}},
		"ec2_failed":          {"inventory_sources", []string{"t.source = 'ec2'", "t.status = 'failed'"}},
		"groups":              {"groups", nil},
		"groups_failed":       {"groups", []string{"t.id IN (SELECT gs.group_id FROM group_inventory_sources gs JOIN unified_job_templates s ON s.id = gs.inventory_source_id WHERE s.last_job_failed)"}},
		"hosts":               {"hosts", nil},
		"hosts_failed":        {"hosts", []string{"EXISTS (SELECT 1 FROM job_host_summaries s WHERE s.id = t.last_job_host_summary_id AND s.failed)"}},
		"projects":            {"projects", nil},
		"projects_failed":     {"projects", []string{"t.last_job_failed"}},
		"git":                 {"projects", []string{"t.scm_type = 'git'"}},
		"git_failed":          {"projects", []string{"t.scm_type = 'git'", "t.last_job_failed"}},
		"svn":                 {"projects", []string{"t.scm_type = 'svn'"}},
		"svn_failed":          {"projects", []string{"t.scm_type = 'svn'", "t.last_job_failed"}},
		"archive":             {"projects", []string{"t.scm_type = 'archive'"}},
		"archive_failed":      {"projects", []string{"t.scm_type = 'archive'", "t.last_job_failed"}},
		"users":               {"users", nil},
		"organizations":       {"organizations", nil},
		"teams":               {"teams", nil},
		"credentials":         {"credentials", nil},
		"job_templates":       {"job_templates", nil},
	}
	n := map[string]int64{}
	for k, q := range counts {
		v, err := visibleCount(c, q.res, q.conds...)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		n[k] = v
	}
	scm := func(typ, label string) Obj {
		return Obj{"url": "/api/v2/projects/?scm_type=" + typ, "label": label,
			"failures_url": "/api/v2/projects/?scm_type=" + typ + "&last_job_failed=True", "total": n[typ], "failed": n[typ+"_failed"]}
	}
	api.WriteJSON(w, 200, Obj{
		"related": Obj{"jobs_graph": "/api/v2/dashboard/graphs/jobs/"},
		"inventories": Obj{"url": "/api/v2/inventories/", "total": n["inventories"], "total_with_inventory_source": n["inventories_sourced"],
			"job_failed": n["inventories_failed"], "inventory_failed": n["sources_failed"]},
		"inventory_sources": Obj{"ec2": Obj{"url": "/api/v2/inventory_sources/?source=ec2", "failures_url": "/api/v2/inventory_sources/?source=ec2&status=failed",
			"label": "Amazon EC2", "total": n["ec2"], "failed": n["ec2_failed"]}},
		"groups":        Obj{"url": "/api/v2/groups/", "total": n["groups"], "inventory_failed": n["groups_failed"]},
		"hosts":         Obj{"url": "/api/v2/hosts/", "failures_url": "/api/v2/hosts/?last_job_host_summary__failed=True", "total": n["hosts"], "failed": n["hosts_failed"]},
		"projects":      Obj{"url": "/api/v2/projects/", "failures_url": "/api/v2/projects/?last_job_failed=True", "total": n["projects"], "failed": n["projects_failed"]},
		"scm_types":     Obj{"git": scm("git", "Git"), "svn": scm("svn", "Subversion"), "archive": scm("archive", "Remote Archive")},
		"users":         Obj{"url": "/api/v2/users/", "total": n["users"]},
		"organizations": Obj{"url": "/api/v2/organizations/", "total": n["organizations"]},
		"teams":         Obj{"url": "/api/v2/teams/", "total": n["teams"]},
		"credentials":   Obj{"url": "/api/v2/credentials/", "total": n["credentials"]},
		"job_templates": Obj{"url": "/api/v2/job_templates/", "total": n["job_templates"]},
	})
}

// dashboardJobsGraph counts the finished jobs the user sees per day and
// status, as AWX's DashboardJobsGraphView: one point per day from the start
// of the period, [epoch seconds, count].
func dashboardJobsGraph(c *Ctx, w http.ResponseWriter, r *http.Request) {
	end := time.Now().UTC()
	var start time.Time
	switch period := r.URL.Query().Get("period"); period {
	case "", "month":
		start = end.AddDate(0, -1, 0)
	case "two_weeks":
		start = end.AddDate(0, 0, -14)
	case "week":
		start = end.AddDate(0, 0, -7)
	case "day":
		start = end.AddDate(0, 0, -1)
	default:
		api.WriteJSON(w, 400, Obj{"error": fmt.Sprintf("Unknown period \"%s\"", period)})
		return
	}
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	where := api.ViewPredicate(c, api.Lookup("unified_jobs"), arg) +
		" AND t.launch_type <> 'sync' AND t.status IN ('successful', 'failed', 'canceled', 'error')" +
		" AND t.finished BETWEEN " + arg(start) + " AND " + arg(end)
	if typ, ok := map[string]string{"inv_sync": "inventory_update", "playbook_run": "job", "scm_update": "project_update"}[r.URL.Query().Get("job_type")]; ok {
		where += " AND t.type = " + arg(typ)
	}
	rows, err := c.DB().Query(c, `SELECT date_trunc('day', t.finished AT TIME ZONE 'UTC'), t.status, count(*)
		FROM unified_jobs t WHERE `+where+` GROUP BY 1, 2`, args...)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	perDay := map[string]map[int64]int64{"successful": {}, "failed": {}, "canceled": {}, "error": {}}
	for rows.Next() {
		var day time.Time
		var status string
		var n int64
		if err := rows.Scan(&day, &status, &n); err != nil {
			rows.Close()
			api.WriteError(w, err)
			return
		}
		perDay[status][day.Unix()] = n
	}
	rows.Close()
	out := Obj{}
	first := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	for status, counts := range perDay {
		points := []any{}
		for d := first; !d.After(end); d = d.AddDate(0, 0, 1) {
			points = append(points, []any{float64(d.Unix()), counts[d.Unix()]})
		}
		out[status] = points
	}
	api.WriteJSON(w, 200, Obj{"jobs": out})
}
