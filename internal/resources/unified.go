package resources

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// Jobs is the node's job manager, set by the server at startup.
var Jobs *jobs.Manager

var jobStatusChoices = labelled("new", "New", "pending", "Pending", "waiting", "Waiting", "running", "Running",
	"successful", "Successful", "failed", "Failed", "error", "Error", "canceled", "Canceled")

var launchTypeChoices = labelled("manual", "Manual", "relaunch", "Relaunch", "callback", "Callback", "scheduled", "Scheduled",
	"dependency", "Dependency", "workflow", "Workflow", "webhook", "Webhook", "sync", "Sync", "scm", "SCM Update")

var ujtStatusChoices = labelled("new", "New", "pending", "Pending", "waiting", "Waiting", "running", "Running",
	"successful", "Successful", "failed", "Failed", "error", "Error", "canceled", "Canceled", "never updated", "Never Updated",
	"ok", "OK", "missing", "Missing", "none", "No External Source", "updating", "Updating")

// ujtFields are the fields shared by all unified job templates.
func ujtFields() []*Field {
	return []*Field{
		{Name: "status", Type: api.TChoice, ReadOnly: true, Choices: ujtStatusChoices},
		{Name: "last_job", Column: "last_job_id", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Hidden: true},
		{Name: "last_job_failed", Type: api.TBool, ReadOnly: true},
		{Name: "last_job_run", Type: api.TDateTime, ReadOnly: true, Nullable: true},
		{Name: "next_job_run", Type: api.TDateTime, ReadOnly: true, Nullable: true},
		{Name: "current_job", Column: "current_job_id", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Hidden: true},
		{Name: "execution_environment", Type: api.TFK, Ref: "execution_environments", Nullable: true, Help: "The container image to be used for execution."},
	}
}

// ujtSummary adds last_job / current_job / recent_jobs summaries like AWX,
// plus what lists of mixed templates show: inventory, playbook, survey and
// workflow size (summary_fields.template).
func ujtSummary(c *Ctx, objs []Obj) error {
	ids := make([]int64, 0, len(objs))
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		ids = append(ids, id)
	}
	extra := map[int64]Obj{}
	inv := map[int64]Obj{}
	rows, err := c.DB().Query(c, `SELECT u.id, u.playbook, u.survey_enabled, i.id, i.name,
		(SELECT count(*) FROM workflow_job_template_nodes n WHERE n.workflow_job_template_id = u.id)
		FROM unified_job_templates u LEFT JOIN inventories i ON i.id = u.inventory_id WHERE u.id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var playbook *string
		var survey *bool
		var invID *int64
		var invName *string
		var nodes int64
		if err := rows.Scan(&id, &playbook, &survey, &invID, &invName, &nodes); err != nil {
			rows.Close()
			return err
		}
		e := Obj{"workflow_nodes": nodes}
		if playbook != nil {
			e["playbook"] = *playbook
		}
		if survey != nil {
			e["survey_enabled"] = *survey
		}
		extra[id] = e
		if invID != nil {
			inv[id] = Obj{"id": *invID, "name": *invName}
		}
	}
	rows.Close()
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		sf := o["summary_fields"].(Obj)
		switch o["type"] {
		case "job_template":
			sf["template"] = Obj{"playbook": extra[id]["playbook"], "survey_enabled": extra[id]["survey_enabled"] == true}
		case "workflow_job_template":
			sf["template"] = Obj{"workflow_nodes": extra[id]["workflow_nodes"], "survey_enabled": extra[id]["survey_enabled"] == true}
		}
		if i, ok := inv[id]; ok {
			sf["inventory"] = i
		}
	}
	// Capabilities as each template's own endpoint reports them.
	byType := map[string][]int{}
	for i, o := range objs {
		byType[str(o["type"])] = append(byType[str(o["type"])], i)
	}
	for typ, idx := range byType {
		r := api.LookupType(typ)
		if r == nil {
			continue
		}
		raws := make([]Obj, len(idx))
		tids := make([]int64, len(idx))
		for j, i := range idx {
			raws[j], tids[j] = objs[i], ids[i]
		}
		caps, err := api.UserCapabilities(c, r, raws, tids)
		if err != nil {
			return err
		}
		for j, i := range idx {
			objs[i]["summary_fields"].(Obj)["user_capabilities"] = caps[j]
		}
	}
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		sf := o["summary_fields"].(Obj)
		rel := o["related"].(Obj)
		recent := []Obj{}
		rows, err := c.DB().Query(c, `SELECT id, type, status, finished, canceled_on FROM unified_jobs
			WHERE unified_job_template_id = $1 ORDER BY id DESC LIMIT 10`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var jid int64
			var typ, status string
			var finished, canceled any
			rows.Scan(&jid, &typ, &status, &finished, &canceled)
			recent = append(recent, Obj{"id": jid, "status": status, "finished": fmtTime(finished), "canceled_on": fmtTime(canceled), "type": typ})
		}
		rows.Close()
		sf["recent_jobs"] = recent
		for _, k := range []string{"last_job", "current_job"} {
			delete(rel, k)
			delete(sf, k)
			col := k + "_id"
			var jid int64
			var typ, name, desc, status string
			var failed bool
			var finished any
			err := c.DB().QueryRow(c, `SELECT j.id, j.type, j.name, j.description, j.status, j.failed, j.finished FROM unified_job_templates u
				JOIN unified_jobs j ON j.id = u.`+col+` WHERE u.id = $1`, id).Scan(&jid, &typ, &name, &desc, &status, &failed, &finished)
			if err != nil {
				continue
			}
			sf[k] = Obj{"id": jid, "name": name, "description": desc, "finished": fmtTime(finished), "status": status, "failed": failed}
			if r := api.LookupType(typ); r != nil {
				rel[k] = r.URL(jid)
			}
		}
	}
	return nil
}

func fmtTime(v any) any {
	if t, ok := v.(time.Time); ok && !t.IsZero() {
		return api.FormatTime(t)
	}
	return nil
}

// jobFields are the fields shared by all unified jobs.
func jobFields(templateField, templateRef string) []*Field {
	var fields []*Field
	if templateField != "" {
		fields = append(fields, &Field{Name: templateField, Column: "unified_job_template_id", Type: api.TFK, Ref: templateRef, ReadOnly: true, Nullable: true})
	}
	return append(fields, []*Field{
		{Name: "unified_job_template", Type: api.TFK, Ref: "unified_job_templates", ReadOnly: true, Nullable: true},
		{Name: "launch_type", Type: api.TChoice, ReadOnly: true, Choices: launchTypeChoices},
		{Name: "status", Type: api.TChoice, ReadOnly: true, Choices: jobStatusChoices},
		{Name: "execution_environment", Type: api.TFK, Ref: "execution_environments", ReadOnly: true, Nullable: true},
		{Name: "failed", Type: api.TBool, ReadOnly: true},
		{Name: "started", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "The date and time the job was queued for starting."},
		{Name: "finished", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "The date and time the job finished execution."},
		{Name: "canceled_on", Type: api.TDateTime, ReadOnly: true, Nullable: true},
		{Name: "elapsed", Type: api.TFloat, ReadOnly: true, Help: "Elapsed time in seconds that the job ran."},
		{Name: "job_args", Type: api.TText, ReadOnly: true},
		{Name: "job_cwd", Type: api.TString, ReadOnly: true},
		{Name: "job_env", Type: api.TJSON, ReadOnly: true},
		{Name: "job_explanation", Type: api.TText, ReadOnly: true, Help: "A status field to indicate the state of the job if it wasn't able to run and capture stdout"},
		{Name: "execution_node", Type: api.TString, ReadOnly: true, Help: "The node the job executed on."},
		{Name: "controller_node", Type: api.TString, ReadOnly: true, Help: "The instance that managed the execution environment."},
		{Name: "result_traceback", Type: api.TText, ReadOnly: true},
		{Name: "event_processing_finished", Type: api.TBool, ReadOnly: true, Help: "Indicates whether all of the events generated by this unified job have been saved to the database."},
		{Name: "work_unit_id", Type: api.TString, ReadOnly: true, Nullable: true},
		{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true, Nullable: true},
		{Name: "instance_group", Type: api.TFK, Ref: "instance_groups", ReadOnly: true, Nullable: true},
		{Name: "launched_by", Type: api.TJSON, ReadOnly: true, NoFilter: true},
		{Name: "cancel_flag", Type: api.TBool, ReadOnly: true, Hidden: true},
		{Name: "emitted_events", Type: api.TInt, ReadOnly: true, Hidden: true},
		{Name: "task_impact", Type: api.TInt, ReadOnly: true, Hidden: true},
	}...)
}

var finalStatus = map[string]bool{"successful": true, "failed": true, "error": true, "canceled": true}

// jobSpec describes a unified job resource.
type jobSpec struct {
	name, typ, verbose string
	templateField      string // e.g. "job_template", "project"
	templateRes        string // e.g. "job_templates"
	eventsName         string // sublist name: "job_events" or "events"
	eventsRes          string // resource: "job_events", "project_update_events"
	// visible returns a predicate (alias t) for jobs the principal can see beyond launched-by-me.
	visible func(c *Ctx, arg ArgFunc) string
	// canManage reports whether the principal administers the job (delete, and cancel others' jobs).
	canManage func(c *Ctx, job Obj) (bool, error)
	// canCancel, when set, grants cancel more broadly than canManage (e.g. project update role).
	canCancel func(c *Ctx, job Obj) (bool, error)
	extra     []*Field
	// omit lists shared fields this job type does not have in AWX.
	omit []string
}

func registerJobResource(js jobSpec) *Resource {
	var fields []*Field
	for _, f := range jobFields(js.templateField, js.templateRes) {
		if !slices.Contains(js.omit, f.Name) {
			fields = append(fields, f)
		}
	}
	fields = append(fields, js.extra...)
	res := api.Register(&Resource{
		Name:          js.name,
		Type:          js.typ,
		Table:         "unified_jobs",
		Discriminator: true,
		Verbose:       js.verbose,
		Ordering:      "-id",
		NoCreate:      true,
		NoUpdate:      true,
		Fields:        fields,
		// Like AWX: references to a job show how it ended.
		SummaryFields: []string{"id", "name", "description", "status", "failed", "elapsed", "type", "finished"},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return fmt.Sprintf("t.created_by_id = %s OR (%s)", arg(c.User.UserID), js.visible(c, arg))
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			return api.Detail(405, "Jobs cannot be modified.")
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if !finalStatus[str(obj["status"])] {
				return api.Detail(403, "Cannot delete running job resource.")
			}
			if c.User.Superuser {
				return nil
			}
			ok, err := js.canManage(c, obj)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		AfterDelete: func(c *Ctx, obj Obj) error {
			// job_events is partitioned and has no foreign key to cascade.
			_, err := c.DB().Exec(c, "DELETE FROM job_events WHERE job_id = $1", obj["id"])
			return err
		},
		Serialize: func(c *Ctx, o Obj) {
			if !c.User.Superuser {
				o["job_env"] = Obj{}
			} else if env, ok := o["job_env"].(map[string]any); ok {
				o["job_env"] = redactEnv(env)
			}
			if lb, ok := o["launched_by"].(map[string]any); ok && len(lb) > 0 {
				o["launched_by"] = lb
			} else {
				o["launched_by"] = Obj{}
			}
		},
		Summary: jobSummary,
		Capabilities: func(c *Ctx, obj Obj, p map[string]bool) map[string]bool {
			manage := c.User.Superuser
			if !manage {
				manage, _ = js.canManage(c, obj)
			}
			return map[string]bool{"delete": manage && finalStatus[str(obj["status"])], "start": manage}
		},
	})
	res.SubLists = []*SubList{
		{
			Name: "notifications", Target: "notifications", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_id = " + arg(pid) },
		},
	}
	if js.eventsName != "" {
		res.SubLists = append(res.SubLists, &SubList{
			Name: js.eventsName, Target: js.eventsRes, ReadOnly: true, SkipView: true,
			Filter:     func(arg ArgFunc, pid int64) string { return "t.job_id = " + arg(pid) },
			BeforeList: func(c *Ctx, pid int64) error { return jobs.RestoreEvents(c, c.App.DB, pid) },
		})
	}
	res.Actions = []*api.Action{
		{Name: "cancel", Methods: []string{"GET", "POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			can := !finalStatus[str(obj["status"])]
			if r.Method == "GET" {
				api.WriteJSON(w, 200, Obj{"can_cancel": can})
				return
			}
			manage := c.User.Superuser
			if uid, _ := api.ID(obj["created_by"]); uid == c.User.UserID && !c.User.Anonymous() {
				manage = true
			}
			if !manage {
				check := js.canManage
				if js.canCancel != nil {
					check = js.canCancel
				}
				var err error
				if manage, err = check(c, obj); err != nil {
					api.WriteError(w, err)
					return
				}
			}
			if !manage {
				api.WriteError(w, api.ErrForbidden)
				return
			}
			if !can {
				api.WriteError(w, api.Detail(405, "Method \"POST\" not allowed."))
				return
			}
			id, _ := api.ID(obj["id"])
			if err := Jobs.Cancel(c, id); err != nil {
				api.WriteError(w, err)
				return
			}
			w.WriteHeader(202)
		}},
		{Name: "stdout", Methods: []string{"GET"}, Handler: handleStdout},
	}
	return res
}

func redactEnv(env map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range env {
		u := strings.ToUpper(k)
		if strings.Contains(u, "PASSWORD") || strings.Contains(u, "SECRET") || strings.Contains(u, "TOKEN") || strings.Contains(u, "KEY") && !strings.HasSuffix(u, "_KEY_FILE") {
			out[k] = "**********"
		} else {
			out[k] = v
		}
	}
	return out
}

func jobSummary(c *Ctx, objs []Obj) error {
	for _, o := range objs {
		sf := o["summary_fields"].(Obj)
		rel := o["related"].(Obj)
		if lb, ok := o["launched_by"].(Obj); ok && len(lb) == 0 {
			if u, ok := sf["created_by"].(Obj); ok {
				uid, _ := api.ID(u["id"])
				o["launched_by"] = Obj{"id": uid, "name": u["username"], "type": "user", "url": api.Lookup("users").URL(uid)}
			}
		}
		// The unified_job_template link points at the type-specific template.
		if ujt, ok := api.ID(o["unified_job_template"]); ok {
			var typ string
			c.DB().QueryRow(c, "SELECT type FROM unified_job_templates WHERE id = $1", ujt).Scan(&typ)
			if r := api.LookupType(typ); r != nil {
				rel["unified_job_template"] = r.URL(ujt)
				if s, ok := sf["unified_job_template"].(Obj); ok {
					s["unified_job_type"] = typ
				}
			}
		}
		if _, ok := sf["user_capabilities"]; !ok {
			sf["user_capabilities"] = Obj{}
		}
	}
	return nil
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// handleStdout serves /<jobs>/<id>/stdout/ in AWX's formats.
func handleStdout(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	id, _ := api.ID(obj["id"])
	q := r.URL.Query()
	format := q.Get("format")
	if format == "" {
		format = "json"
		if strings.Contains(r.Header.Get("Accept"), "text/plain") {
			format = "txt"
		}
	}
	if err := jobs.RestoreEvents(c, c.App.DB, id); err != nil {
		api.WriteError(w, err)
		return
	}
	startLine, _ := strconv.Atoi(q.Get("start_line"))
	endLine, _ := strconv.Atoi(q.Get("end_line"))
	rows, err := c.DB().Query(c, "SELECT stdout, start_line FROM job_events WHERE job_id = $1 AND stdout <> '' ORDER BY counter", id)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	var b strings.Builder
	for rows.Next() {
		var s string
		var sl int
		rows.Scan(&s, &sl)
		b.WriteString(s)
		b.WriteString("\n")
	}
	rows.Close()
	content := b.String()
	if content == "" {
		if expl := str(obj["result_traceback"]); expl != "" {
			content = expl + "\n"
		} else if expl := str(obj["job_explanation"]); expl != "" {
			content = expl + "\n"
		}
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if startLine > 0 || endLine > 0 {
		if endLine <= 0 || endLine > total {
			endLine = total
		}
		if startLine > endLine {
			startLine = endLine
		}
		content = strings.Join(lines[startLine:endLine], "")
	} else {
		endLine = total
	}
	max := c.App.Settings.Int(c, "STDOUT_MAX_BYTES_DISPLAY")
	download := strings.HasSuffix(format, "_download")
	if !download && max > 0 && len(content) > max {
		msg := fmt.Sprintf("Standard Output too large to display (%d bytes), only download supported for sizes over %d bytes.", len(content), max)
		if format == "json" {
			api.WriteJSON(w, 200, Obj{"range": Obj{"start": 0, "end": 1, "absolute_end": 1}, "content": msg})
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte(msg))
		}
		return
	}
	switch strings.TrimSuffix(format, "_download") {
	case "txt":
		content = ansiRe.ReplaceAllString(content, "")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if download {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s_%d.txt"`, str(obj["type"]), id))
		}
		w.Write([]byte(content))
	case "ansi":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if download {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s_%d.txt"`, str(obj["type"]), id))
		}
		w.Write([]byte(content))
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>%s #%d</title></head><body><pre>%s</pre></body></html>",
			html.EscapeString(str(obj["name"])), id, html.EscapeString(ansiRe.ReplaceAllString(content, "")))
	default:
		api.WriteJSON(w, 200, Obj{"range": Obj{"start": startLine, "end": endLine, "absolute_end": total}, "content": content})
	}
}

// eventResource registers a job events resource for one job type.
func registerEventResource(name, typ, jobType, jobField string) {
	api.Register(&Resource{
		Name:         name,
		Type:         typ,
		Table:        "job_events",
		Verbose:      title2(typ),
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "counter",
		SearchFields: []string{"stdout"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: jobField, Column: "job_id", Type: api.TFK, Ref: jobResName(jobType), ReadOnly: true},
			{Name: "event", Type: api.TString, ReadOnly: true},
			{Name: "counter", Type: api.TInt, ReadOnly: true},
			{Name: "event_data", Type: api.TJSON, ReadOnly: true},
			{Name: "failed", Type: api.TBool, ReadOnly: true},
			{Name: "changed", Type: api.TBool, ReadOnly: true},
			{Name: "uuid", Type: api.TString, ReadOnly: true},
			{Name: "parent_uuid", Type: api.TString, ReadOnly: true},
			{Name: "host", Type: api.TFK, Ref: "hosts", ReadOnly: true, Nullable: true},
			{Name: "host_name", Type: api.TString, ReadOnly: true},
			{Name: "playbook", Type: api.TString, ReadOnly: true},
			{Name: "play", Type: api.TString, ReadOnly: true},
			{Name: "role", Type: api.TString, ReadOnly: true},
			{Name: "task", Type: api.TString, ReadOnly: true},
			{Name: "stdout", Type: api.TText, ReadOnly: true},
			{Name: "start_line", Type: api.TInt, ReadOnly: true},
			{Name: "end_line", Type: api.TInt, ReadOnly: true},
			{Name: "verbosity", Type: api.TInt, ReadOnly: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			jr := api.Lookup(jobResName(jobType))
			pred := "TRUE"
			if !c.User.Superuser && !c.User.Auditor {
				pred = jr.View(c, arg)
			}
			return fmt.Sprintf("t.job_id IN (SELECT t.id FROM unified_jobs t WHERE t.type = %s AND (%s))", arg(jobType), pred)
		},
		Serialize: func(c *Ctx, o Obj) {
			o["event_display"] = eventDisplay(str(o["event"]))
			o["event_level"] = eventLevel(str(o["event"]))
		},
	})
}

func jobResName(jobType string) string {
	return map[string]string{
		"job": "jobs", "project_update": "project_updates", "inventory_update": "inventory_updates",
		"ad_hoc_command": "ad_hoc_commands", "system_job": "system_jobs", "workflow_job": "workflow_jobs",
	}[jobType]
}

var eventDisplays = map[string]string{
	"playbook_on_start": "Playbook Started", "playbook_on_play_start": "Play Started", "playbook_on_task_start": "Task Started",
	"playbook_on_stats": "Playbook Complete", "runner_on_ok": "Host OK", "runner_on_failed": "Host Failed",
	"runner_on_skipped": "Host Skipped", "runner_on_unreachable": "Host Unreachable", "runner_on_start": "Host Started",
	"runner_item_on_ok": "Item OK", "runner_item_on_failed": "Item Failed", "runner_item_on_skipped": "Item Skipped",
	"runner_retry": "Host Retry", "runner_on_async_ok": "Host Async OK", "runner_on_async_failed": "Host Async Failure",
	"playbook_on_include": "Including File", "playbook_on_no_hosts_matched": "No Hosts Matched",
	"playbook_on_no_hosts_remaining": "No Hosts Remaining", "playbook_on_handler_task_start": "Handler Started",
	"playbook_on_notify": "Host Notified", "verbose": "Verbose", "debug": "Debug", "warning": "Warning", "error": "Error",
	"system_warning": "System Warning", "deprecated": "Deprecated",
}

func eventDisplay(e string) string {
	if d, ok := eventDisplays[e]; ok {
		return d
	}
	return title2(e)
}

func eventLevel(e string) int {
	switch {
	case e == "playbook_on_start" || e == "playbook_on_stats":
		return 0
	case e == "playbook_on_play_start" || strings.HasPrefix(e, "playbook_on_no_hosts"):
		return 1
	case strings.HasPrefix(e, "playbook_on_task") || strings.HasPrefix(e, "playbook_on_handler") || e == "playbook_on_include" || e == "playbook_on_notify":
		return 2
	case strings.HasPrefix(e, "runner_"):
		return 3
	}
	return 0
}

func registerUnified() {
	ujt := api.Register(&Resource{
		Name:     "unified_job_templates",
		Type:     "unified_job_template",
		Table:    "unified_job_templates",
		Verbose:  "Unified Job Template",
		Ordering: "name",
		ReadOnly: true,
		Fields: append([]*Field{
			{Name: "type", Type: api.TChoice, ReadOnly: true, Choices: choices("job_template", "project", "inventory_source", "workflow_job_template", "system_job_template", "workflow_approval_template")},
			{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true, Nullable: true},
			{Name: "folder", Type: api.TFK, Ref: "folders", ReadOnly: true, Nullable: true},
			// Filter-only columns of job templates (e.g. inventory__name__istartswith=prod).
			{Name: "inventory", Type: api.TFK, Ref: "inventories", ReadOnly: true, Nullable: true, Hidden: true},
			{Name: "project", Type: api.TFK, Ref: "projects", ReadOnly: true, Nullable: true, Hidden: true},
			{Name: "playbook", Type: api.TString, ReadOnly: true, Hidden: true},
		}, ujtFields()...),
		Relations: map[string]api.Relation{
			"labels": {Target: "labels", Join: func(a, b string) string {
				return fmt.Sprintf("EXISTS (SELECT 1 FROM unified_job_template_labels x WHERE x.unified_job_template_id = %s.id AND x.label_id = %s.id)", a, b)
			}},
		},
		SummaryFields: []string{"id", "name", "description", "type"},
		View:          ujtView,
		Summary:       ujtSummary,
	})
	ujt.Serialize = func(c *Ctx, o Obj) {
		if r := api.LookupType(str(o["type"])); r != nil {
			id, _ := api.ID(o["id"])
			o["url"] = r.URL(id)
		}
	}
	uj := api.Register(&Resource{
		Name:     "unified_jobs",
		Type:     "unified_job",
		Table:    "unified_jobs",
		Verbose:  "Unified Job",
		Ordering: "-id",
		ReadOnly: true,
		Fields: append([]*Field{
			{Name: "type", Type: api.TChoice, ReadOnly: true, Choices: choices("job", "project_update", "inventory_update", "ad_hoc_command", "workflow_job", "system_job", "workflow_approval")},
		}, jobFields("unified_job_template_x", "unified_job_templates")[1:]...),
		SummaryFields: []string{"id", "name", "description", "status", "failed", "type", "finished"},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			var parts []string
			for _, r := range api.Resources() {
				if r.Table == "unified_jobs" && r.Discriminator {
					parts = append(parts, fmt.Sprintf("(t.type = %s AND (%s))", arg(r.Type), r.View(c, arg)))
				}
			}
			if len(parts) == 0 {
				return "FALSE"
			}
			return strings.Join(parts, " OR ")
		},
		Summary: jobSummary,
	})
	uj.Serialize = func(c *Ctx, o Obj) {
		if r := api.LookupType(str(o["type"])); r != nil {
			id, _ := api.ID(o["id"])
			o["url"] = r.URL(id)
		}
		o["job_env"] = Obj{}
	}
}

// ujtView is the union of the type-specific template visibility rules.
func ujtView(c *Ctx, arg ArgFunc) string {
	if c.User.Superuser || c.User.Auditor {
		return "TRUE"
	}
	var parts []string
	for _, r := range api.Resources() {
		if r.Table == "unified_job_templates" && r.Discriminator {
			parts = append(parts, fmt.Sprintf("(t.type = %s AND (%s))", arg(r.Type), viewOf(c, r, arg)))
		}
	}
	if len(parts) == 0 {
		return "FALSE"
	}
	return strings.Join(parts, " OR ")
}
