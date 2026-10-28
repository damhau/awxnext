package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/schedule"
)

// scheduleLaunchers start a job for a schedule, by unified job template type.
var scheduleLaunchers = map[string]func(c *Ctx, ujt Obj, sched Obj) (int64, error){}

// schedulePrompts are the launch fields a schedule can set.
var schedulePrompts = []string{"inventory", "scm_branch", "job_type", "job_tags", "skip_tags", "limit", "diff_mode", "verbosity",
	"execution_environment", "forks", "job_slice_count", "timeout"}

// ujtType returns the type of a unified job template.
func ujtType(c *Ctx, id int64) string {
	var t string
	c.DB().QueryRow(c, "SELECT type FROM unified_job_templates WHERE id = $1", id).Scan(&t)
	return t
}

// ujtPerm checks launch ("execute") or admin ("change") rights on any unified job template.
func ujtPerm(c *Ctx, id int64, perm string) (bool, error) {
	if c.User.Superuser {
		return true, nil
	}
	switch ujtType(c, id) {
	case "job_template":
		return api.HasPermID(c, "job_templates", id, perm)
	case "workflow_job_template":
		if api.Lookup("workflow_job_templates") == nil {
			return false, nil
		}
		return api.HasPermID(c, "workflow_job_templates", id, perm)
	case "project":
		if perm == "execute" {
			perm = "update"
		}
		return api.HasPermID(c, "projects", id, perm)
	case "inventory_source":
		if perm == "execute" {
			perm = "update"
		}
		var inv int64
		c.DB().QueryRow(c, "SELECT inventory_id FROM unified_job_templates WHERE id = $1", id).Scan(&inv)
		return api.HasPermID(c, "inventories", inv, perm)
	case "":
		return false, api.ErrNotFound
	}
	return false, nil
}

func requireUJTPerm(c *Ctx, v any, perm string) error {
	id, ok := api.ID(v)
	if !ok {
		return api.FieldError("unified_job_template", "This field is required.")
	}
	ok, err := ujtPerm(c, id, perm)
	if err == api.ErrNotFound {
		return api.FieldError("unified_job_template", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
	}
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	return nil
}

// scheduleTimes computes dtstart, dtend and next_run for a rule.
func scheduleTimes(rule string, enabled bool) (dtstart, dtend, next any, err error) {
	r, err := schedule.Parse(rule)
	if err != nil {
		return nil, nil, nil, err
	}
	dtstart = r.Start.UTC()
	if last, ok := r.Last(); ok {
		dtend = last.UTC()
	}
	first, ok := r.After(r.Start.Add(-time.Second))
	if ok {
		dtstart = first.UTC()
	}
	if enabled {
		if n, ok := r.After(time.Now()); ok {
			next = n.UTC()
		}
	}
	return dtstart, dtend, next, nil
}

// refreshNextJobRun stores a template's next scheduled run.
func refreshNextJobRun(c *Ctx, ujt any) error {
	_, err := c.DB().Exec(c, `UPDATE unified_job_templates u SET (next_job_run, next_schedule_id) = (
		SELECT next_run, id FROM schedules WHERE unified_job_template_id = u.id AND enabled AND next_run IS NOT NULL ORDER BY next_run LIMIT 1)
		WHERE u.id = $1`, ujt)
	return err
}

// seedSchedule creates a schedule starting now (used for built-in templates).
func seedSchedule(ctx context.Context, app *api.App, ujt int64, name, rule string, extra map[string]any) error {
	text := "DTSTART:" + time.Now().UTC().Format("20060102T150405") + "Z RRULE:" + rule
	dtstart, dtend, next, err := scheduleTimes(text, true)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(extra)
	_, err = app.DB.Exec(ctx, `INSERT INTO schedules (name, unified_job_template_id, rrule, dtstart, dtend, next_run, extra_data)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`, name, ujt, text, dtstart, dtend, next, string(b))
	if err != nil {
		return err
	}
	_, err = app.DB.Exec(ctx, `UPDATE unified_job_templates u SET (next_job_run, next_schedule_id) = (
		SELECT next_run, id FROM schedules WHERE unified_job_template_id = u.id AND enabled AND next_run IS NOT NULL ORDER BY next_run LIMIT 1)
		WHERE u.id = $1`, ujt)
	return err
}

// validateSchedulePrompts checks prompts against the template's ask_* flags and survey.
func validateSchedulePrompts(c *Ctx, ujt int64, merged Obj, data Obj, old Obj) error {
	typ := ujtType(c, ujt)
	fe := api.FieldErrors{}
	var tpl Obj
	if typ == "job_template" || typ == "workflow_job_template" {
		res := api.Lookup(map[string]string{"job_template": "job_templates", "workflow_job_template": "workflow_job_templates"}[typ])
		if res != nil {
			tpl, _ = api.Get(c, res, ujt)
		}
	}
	for _, f := range schedulePrompts {
		v, ok := data[f]
		if !ok || v == nil {
			continue
		}
		if tpl == nil || !boolv(tpl[promptable[f]]) {
			fe.Add(f, "Field is not configured to prompt on launch.")
		}
	}
	if err := fe.Err(); err != nil {
		return err
	}
	if inv, ok := api.ID(data["inventory"]); ok {
		if err := api.RequirePermID(c, "inventory", "inventories", inv, "use"); err != nil {
			return err
		}
	}
	extra, _ := merged["extra_data"].(map[string]any)
	if len(extra) == 0 {
		return nil
	}
	switch typ {
	case "system_job_template":
		return validateSystemJobVars("extra_data", extra)
	case "job_template", "workflow_job_template":
	default:
		return api.FieldError("extra_data", "Extra variables are not supported for this template type.")
	}
	if err := refuseAnsibleVars(c, "extra_data", extra); err != nil {
		return err
	}
	if tpl == nil {
		return nil
	}
	spec := templateSurvey(c, ujt)
	surveyVars := map[string]bool{}
	if boolv(tpl["survey_enabled"]) {
		surveyVars = surveyVariables(spec)
	}
	if !boolv(tpl["ask_variables_on_launch"]) {
		for k := range extra {
			if !surveyVars[k] {
				return api.FieldError("extra_data", fmt.Sprintf("Variables %s provided, but the template cannot accept them on launch.", k))
			}
		}
	}
	// Survey passwords are stored encrypted; "$encrypted$" keeps the stored value.
	var oldExtra map[string]any
	if old != nil {
		oldExtra, _ = old["extra_data"].(map[string]any)
	}
	qs, _ := spec["spec"].([]any)
	for _, q := range qs {
		qm, _ := q.(map[string]any)
		if str(qm["type"]) != "password" {
			continue
		}
		v := str(qm["variable"])
		s, ok := extra[v].(string)
		if !ok {
			continue
		}
		if s == crypto.Placeholder {
			if prev, ok := oldExtra[v]; ok {
				extra[v] = prev
			} else {
				delete(extra, v)
			}
			continue
		}
		if !crypto.IsEncrypted(s) {
			enc, err := c.App.Crypto.Encrypt(s)
			if err != nil {
				return err
			}
			extra[v] = enc
		}
	}
	data["extra_data"] = extra
	return nil
}

func registerSchedules() {
	ujtRes := api.Lookup("unified_job_templates")
	res := api.Register(&Resource{
		Name:     "schedules",
		Type:     "schedule",
		Table:    "schedules",
		Verbose:  "Schedule",
		Ordering: "id",
		Managed:  true,
		Fields: []*Field{
			{Name: "unified_job_template", Type: api.TFK, Ref: "unified_job_templates", Required: true, CreateOnly: true},
			{Name: "enabled", Type: api.TBool, Default: true, Help: "Enables processing of this schedule."},
			{Name: "rrule", Type: api.TString, Required: true, Help: "A value representing the schedules iCal recurrence rule.",
				Validate: func(c *Ctx, v any, data Obj) error { _, err := schedule.Parse(str(v)); return err }},
			{Name: "dtstart", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "The first occurrence of the schedule occurs on or after this time."},
			{Name: "dtend", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "The last occurrence of the schedule occurs before this time, aftewards the schedule expires."},
			{Name: "next_run", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "The next time that the scheduled action will run."},
			{Name: "timezone", Type: api.TString, ReadOnly: true, Virtual: true},
			{Name: "until", Type: api.TString, ReadOnly: true, Virtual: true},
			{Name: "extra_data", Type: api.TJSON, Default: map[string]any{}},
			{Name: "inventory", Type: api.TFK, Ref: "inventories", Nullable: true, Help: "Inventory applied as a prompt, assuming job template prompts for inventory"},
			{Name: "scm_branch", Type: api.TString, Nullable: true},
			{Name: "job_type", Type: api.TChoice, Nullable: true, Choices: labelled("", "---------", "run", "Run", "check", "Check")},
			{Name: "job_tags", Type: api.TString, Nullable: true},
			{Name: "skip_tags", Type: api.TString, Nullable: true},
			{Name: "limit", Type: api.TString, Nullable: true},
			{Name: "diff_mode", Type: api.TBool, Nullable: true},
			{Name: "verbosity", Type: api.TChoice, Nullable: true, Choices: verbosityChoices},
			{Name: "execution_environment", Type: api.TFK, Ref: "execution_environments", Nullable: true},
			{Name: "forks", Type: api.TInt, Nullable: true},
			{Name: "job_slice_count", Type: api.TInt, Nullable: true},
			{Name: "timeout", Type: api.TInt, Nullable: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.unified_job_template_id IN (SELECT t.id FROM unified_job_templates t WHERE " + ujtRes.View(c, arg) + ")"
		},
		CanAdd: func(c *Ctx, data Obj) error {
			return requireUJTPerm(c, data["unified_job_template"], "execute")
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			if err := requireUJTPerm(c, old["unified_job_template"], "change"); err == nil {
				return nil
			}
			if uid, _ := api.ID(old["created_by"]); uid == c.User.UserID && !c.User.Anonymous() {
				return requireUJTPerm(c, old["unified_job_template"], "execute")
			}
			return api.ErrForbidden
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if err := requireUJTPerm(c, obj["unified_job_template"], "change"); err == nil {
				return nil
			}
			if uid, _ := api.ID(obj["created_by"]); uid == c.User.UserID && !c.User.Anonymous() {
				return requireUJTPerm(c, obj["unified_job_template"], "execute")
			}
			return api.ErrForbidden
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			merged := Obj{}
			for k, v := range old {
				merged[k] = v
			}
			for k, v := range data {
				merged[k] = v
			}
			ujt, _ := api.ID(merged["unified_job_template"])
			if err := validateSchedulePrompts(c, ujt, merged, data, old); err != nil {
				return err
			}
			enabled := merged["enabled"] != false
			dtstart, dtend, next, err := scheduleTimes(str(merged["rrule"]), enabled)
			if err != nil {
				return api.FieldError("rrule", err.Error())
			}
			data["dtstart"], data["dtend"], data["next_run"] = dtstart, dtend, next
			return nil
		},
		AfterCreate: func(c *Ctx, obj Obj) error { return refreshNextJobRun(c, obj["unified_job_template"]) },
		AfterUpdate: func(c *Ctx, old, obj Obj) error { return refreshNextJobRun(c, obj["unified_job_template"]) },
		AfterDelete: func(c *Ctx, obj Obj) error { return refreshNextJobRun(c, obj["unified_job_template"]) },
		Serialize: func(c *Ctx, o Obj) {
			o["timezone"], o["until"] = "", ""
			if r, err := schedule.Parse(str(o["rrule"])); err == nil {
				o["timezone"] = r.TZID
				if !r.Until.IsZero() {
					o["until"] = r.Until.In(r.Loc).Format("2006-01-02T15:04:05")
				}
			}
			if extra, ok := o["extra_data"].(map[string]any); ok {
				for k, v := range extra {
					if s, ok := v.(string); ok && crypto.IsEncrypted(s) {
						extra[k] = crypto.Placeholder
					}
				}
			}
		},
		Summary: func(c *Ctx, objs []Obj) error {
			for _, o := range objs {
				id, _ := api.ID(o["unified_job_template"])
				var typ string
				c.DB().QueryRow(c, "SELECT type FROM unified_job_templates WHERE id = $1", id).Scan(&typ)
				if sf, ok := o["summary_fields"].(Obj)["unified_job_template"].(Obj); ok {
					sf["unified_job_type"] = map[string]string{"job_template": "job", "project": "project_update", "inventory_source": "inventory_update",
						"workflow_job_template": "workflow_job", "system_job_template": "system_job"}[typ]
				}
				if r := api.LookupType(typ); r != nil {
					o["related"].(Obj)["unified_job_template"] = r.URL(id)
				}
				sid, _ := api.ID(o["id"])
				creds := []Obj{}
				rows, err := c.DB().Query(c, `SELECT c.id, c.name, ct.kind, ct.namespace FROM schedule_credentials sc JOIN credentials c ON c.id = sc.credential_id
					JOIN credential_types ct ON ct.id = c.credential_type_id WHERE sc.schedule_id = $1 ORDER BY c.id`, sid)
				if err != nil {
					return err
				}
				for rows.Next() {
					var cid int64
					var name, kind string
					var ns *string
					rows.Scan(&cid, &name, &kind, &ns)
					creds = append(creds, Obj{"id": cid, "name": name, "kind": kind})
				}
				rows.Close()
				o["summary_fields"].(Obj)["credentials"] = creds
			}
			return nil
		},
	})
	res.SubLists = []*SubList{
		{
			Name: "credentials", Target: "credentials",
			Filter: func(arg ArgFunc, pid int64) string {
				return "t.id IN (SELECT credential_id FROM schedule_credentials WHERE schedule_id = " + arg(pid) + ")"
			},
			NoCreate: true,
			CanAttach: func(c *Ctx, parent Obj, child int64) error {
				ujt, _ := api.ID(parent["unified_job_template"])
				if ujtType(c, ujt) != "job_template" {
					return api.BadRequest("Credentials can only be prompted for job templates.")
				}
				jt, err := api.Get(c, api.Lookup("job_templates"), ujt)
				if err != nil {
					return err
				}
				if !boolv(jt["ask_credential_on_launch"]) {
					return api.FieldError("credentials", "Field is not configured to prompt on launch.")
				}
				if len(passwordsNeeded(c, []int64{child})) > 0 {
					return api.FieldError("credentials", "Saved launch configurations cannot provide passwords needed to start.")
				}
				return api.RequirePermID(c, "credentials", "credentials", child, "use")
			},
			Associate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "INSERT INTO schedule_credentials (schedule_id, credential_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", pid, cid)
				return err
			},
			Disassociate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM schedule_credentials WHERE schedule_id = $1 AND credential_id = $2", pid, cid)
				return err
			},
		},
		{
			Name: "jobs", Target: "unified_jobs", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.schedule_id = " + arg(pid) },
		},
	}
	for _, name := range []string{"job_templates", "projects", "inventory_sources", "system_job_templates", "workflow_job_templates"} {
		r := api.Lookup(name)
		r.SubLists = append(r.SubLists, &SubList{Name: "schedules", Target: "schedules", ParentField: "unified_job_template",
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) }})
	}

	scheduleLaunchers["job_template"] = func(c *Ctx, jt Obj, s Obj) (int64, error) {
		// Schedules saved before ansible_* variables were refused at launch.
		if extra, _ := s["extra_data"].(map[string]any); extra != nil {
			if err := refuseAnsibleVars(c, "extra_data", extra); err != nil {
				return 0, err
			}
		}
		body := Obj{}
		trusted := Obj{}
		for _, f := range schedulePrompts {
			if v := s[f]; v != nil {
				trusted[f] = v
			}
		}
		if extra, ok := s["extra_data"].(map[string]any); ok && len(extra) > 0 {
			plain := map[string]any{}
			for k, v := range extra {
				if str, ok := v.(string); ok && crypto.IsEncrypted(str) {
					if p, err := c.App.Crypto.Decrypt(str); err == nil {
						v = p
					}
				}
				plain[k] = v
			}
			body["extra_vars"] = plain
		}
		sid, _ := api.ID(s["id"])
		var creds []int64
		rows, err := c.DB().Query(c, "SELECT credential_id FROM schedule_credentials WHERE schedule_id = $1 ORDER BY credential_id", sid)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			creds = append(creds, id)
		}
		rows.Close()
		if len(creds) > 0 {
			list := make([]any, len(creds))
			for i, id := range creds {
				list[i] = id
			}
			trusted["credentials"] = list
		}
		id, _, err := LaunchJobTemplate(c, jt, body, "scheduled", &LaunchOverrides{Trusted: trusted, ExtraJob: map[string]any{"schedule_id": sid}})
		return id, err
	}
	scheduleLaunchers["workflow_job_template"] = func(c *Ctx, wf Obj, s Obj) (int64, error) {
		// Schedules saved before ansible_* variables were refused at launch.
		if extra, _ := s["extra_data"].(map[string]any); extra != nil {
			if err := refuseAnsibleVars(c, "extra_data", extra); err != nil {
				return 0, err
			}
		}
		trusted := Obj{}
		for _, f := range []string{"inventory", "limit", "scm_branch", "job_tags", "skip_tags"} {
			if v := s[f]; v != nil {
				trusted[f] = v
			}
		}
		body := Obj{}
		if extra, ok := s["extra_data"].(map[string]any); ok && len(extra) > 0 {
			plain := map[string]any{}
			for k, v := range extra {
				if str, ok := v.(string); ok && crypto.IsEncrypted(str) {
					if p, err := c.App.Crypto.Decrypt(str); err == nil {
						v = p
					}
				}
				plain[k] = v
			}
			body["extra_vars"] = plain
		}
		sid, _ := api.ID(s["id"])
		id, _, err := LaunchWorkflowJobTemplate(c, wf, body, "scheduled", &LaunchOverrides{Trusted: trusted, ExtraJob: map[string]any{"schedule_id": sid}})
		return id, err
	}
	scheduleLaunchers["project"] = func(c *Ctx, p Obj, s Obj) (int64, error) {
		return LaunchProjectUpdate(c, p, "scheduled", "check", "")
	}
	scheduleLaunchers["inventory_source"] = func(c *Ctx, src Obj, s Obj) (int64, error) {
		return LaunchInventoryUpdate(c, src, "scheduled")
	}
	scheduleLaunchers["system_job_template"] = func(c *Ctx, sjt Obj, s Obj) (int64, error) {
		extra, _ := s["extra_data"].(map[string]any)
		return LaunchSystemJob(c, sjt, extra, "scheduled")
	}
}

// installScheduleRoutes adds /schedules/preview/ and /schedules/zoneinfo/.
func installScheduleRoutes(app *api.App) {
	app.Handle("POST", "/api/v2/schedules/preview/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		body, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		rule, err := schedule.Parse(str(body["rrule"]))
		if err != nil {
			api.WriteError(w, api.FieldError("rrule", err.Error()))
			return
		}
		local, utc := []string{}, []string{}
		for _, t := range rule.Next(time.Now(), 10) {
			local = append(local, t.In(rule.Loc).Format(time.RFC3339))
			utc = append(utc, t.UTC().Format("2006-01-02T15:04:05Z"))
		}
		api.WriteJSON(w, 200, Obj{"local": local, "utc": utc})
	})
	app.Handle("GET", "/api/v2/schedules/zoneinfo/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, schedule.Zones())
	})
}

// ScheduleTick launches jobs for due schedules. It runs on the dispatcher leader.
func ScheduleTick(ctx context.Context, m *jobs.Manager) {
	rows, err := m.DB().Query(ctx, "SELECT id FROM schedules WHERE enabled AND next_run <= now() ORDER BY next_run LIMIT 50")
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	c := &api.Ctx{Context: ctx, App: m.App, User: rbac.System(), Extra: map[string]any{}}
	for _, id := range ids {
		if err := runSchedule(c, id); err != nil {
			slog.Error("scheduled launch failed", "schedule", id, "err", err)
		}
	}
}

func runSchedule(c *Ctx, id int64) error {
	// Advance next_run first so a failing launch is not retried every tick.
	var rule string
	var ujt int64
	err := c.WithTx(func(c *Ctx) error {
		if err := c.DB().QueryRow(c, `SELECT rrule, unified_job_template_id FROM schedules
			WHERE id = $1 AND enabled AND next_run <= now() FOR UPDATE SKIP LOCKED`, id).Scan(&rule, &ujt); err != nil {
			rule = ""
			return nil
		}
		var next any
		if r, err := schedule.Parse(rule); err == nil {
			if n, ok := r.After(time.Now()); ok {
				next = n.UTC()
			}
		}
		if _, err := c.DB().Exec(c, "UPDATE schedules SET next_run = $2 WHERE id = $1", id, next); err != nil {
			return err
		}
		return refreshNextJobRun(c, ujt)
	})
	if err != nil || rule == "" {
		return err
	}
	sched, err := api.Get(c, api.Lookup("schedules"), id)
	if err != nil {
		return err
	}
	typ := ujtType(c, ujt)
	launch := scheduleLaunchers[typ]
	tplRes := api.LookupType(typ)
	if launch == nil || tplRes == nil {
		return fmt.Errorf("schedules are not supported for %s", typ)
	}
	tpl, err := api.Get(c, tplRes, ujt)
	if err != nil {
		return err
	}
	return c.WithTx(func(c *Ctx) error {
		jid, err := launch(c, tpl, sched)
		if err != nil {
			return err
		}
		lb, _ := json.Marshal(Obj{"id": id, "name": sched["name"], "type": "schedule", "url": api.Lookup("schedules").URL(id)})
		_, err = c.DB().Exec(c, "UPDATE unified_jobs SET schedule_id = $2, launched_by = $3 WHERE id = $1", jid, id, string(lb))
		return err
	})
}
