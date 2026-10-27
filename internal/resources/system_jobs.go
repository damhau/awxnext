package resources

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/vars"
)

// SystemJobTypes are the built-in management jobs, with their default schedule.
var SystemJobTypes = []struct {
	JobType, Name, Description string
	Rule                       string // RRULE part of the default schedule
	ExtraData                  map[string]any
}{
	{"cleanup_jobs", "Cleanup Job Details", "Remove job history", "FREQ=WEEKLY;INTERVAL=1;BYDAY=SU", map[string]any{"days": "90"}},
	{"cleanup_activitystream", "Cleanup Activity Stream", "Remove activity stream history", "FREQ=WEEKLY;INTERVAL=1;BYDAY=TU", map[string]any{"days": "355"}},
	{"cleanup_sessions", "Cleanup Expired Sessions", "Cleans out expired browser sessions", "FREQ=WEEKLY;INTERVAL=1", map[string]any{}},
	{"cleanup_tokens", "Cleanup Expired OAuth 2 Tokens", "Cleanup expired OAuth 2 access and refresh tokens", "FREQ=WEEKLY;INTERVAL=1", map[string]any{}},
	{"cleanup_host_metrics", "Cleanup Host Metrics", "Run cleanup host metrics", "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=1", map[string]any{"days": "365"}},
}

var systemJobTypeChoices = labelled("cleanup_jobs", "Remove jobs older than a certain number of days",
	"cleanup_activitystream", "Remove activity stream entries older than a certain number of days",
	"cleanup_sessions", "Removes expired browser sessions from the database",
	"cleanup_tokens", "Removes expired OAuth 2 access tokens and refresh tokens",
	"cleanup_host_metrics", "Removes soft-deleted host metrics older than a certain number of days")

// SeedSystemJobTemplates creates the management job templates and their default schedules.
func SeedSystemJobTemplates(ctx context.Context, app *api.App) error {
	for _, s := range SystemJobTypes {
		var id int64
		err := app.DB.QueryRow(ctx, `SELECT id FROM unified_job_templates WHERE type = 'system_job_template' AND job_type = $1`, s.JobType).Scan(&id)
		if err == nil {
			continue
		}
		if err := app.DB.QueryRow(ctx, `INSERT INTO unified_job_templates (type, name, description, job_type, status)
			VALUES ('system_job_template', $1, $2, $3, 'ok') RETURNING id`, s.Name, s.Description, s.JobType).Scan(&id); err != nil {
			return err
		}
		if err := seedSchedule(ctx, app, id, s.Name+" Schedule", s.Rule, s.ExtraData); err != nil {
			return err
		}
	}
	return nil
}

// LaunchSystemJob starts a management job with the given extra vars.
func LaunchSystemJob(c *Ctx, sjt Obj, extra map[string]any, launchType string) (int64, error) {
	id, _ := api.ID(sjt["id"])
	values := map[string]any{
		"type":                    "system_job",
		"name":                    str(sjt["name"]),
		"description":             str(sjt["description"]),
		"unified_job_template_id": id,
		"job_type":                sjt["job_type"],
		"extra_vars":              vars.MustJSON(extra),
		"launch_type":             launchType,
	}
	if !c.User.Anonymous() {
		values["created_by_id"] = c.User.UserID
		values["modified_by_id"] = c.User.UserID
	}
	jid, err := jobs.Create(c, c.DB(), values)
	if err != nil {
		return 0, err
	}
	jobs.Wake(c, c.DB())
	return jid, nil
}

// validateSystemJobVars checks the days/dry_run variables of management jobs,
// reporting errors on field.
func validateSystemJobVars(field string, extra map[string]any) error {
	for k, v := range extra {
		switch k {
		case "days":
			n, err := strconv.Atoi(fmt.Sprint(v))
			if f, ok := v.(float64); ok {
				n, err = int(f), nil
				if float64(n) != f {
					err = fmt.Errorf("not an integer")
				}
			}
			if err != nil || n < 0 {
				return api.FieldError(field, "days must be a non-negative integer.")
			}
		case "dry_run":
		default:
			return api.FieldError(field, fmt.Sprintf("Unsupported variable %q for management jobs.", k))
		}
	}
	return nil
}

func registerSystemJobs() {
	superOnly := func(c *Ctx, arg ArgFunc) string {
		if c.User.Superuser || c.User.Auditor {
			return "TRUE"
		}
		return "FALSE"
	}
	sjt := api.Register(&Resource{
		Name:          "system_job_templates",
		Type:          "system_job_template",
		Table:         "unified_job_templates",
		Discriminator: true,
		Verbose:       "System Job Template",
		Ordering:      "name",
		NoCreate:      true,
		NoDelete:      true,
		ReadOnly:      true,
		Fields: append([]*Field{
			{Name: "job_type", Type: api.TChoice, ReadOnly: true, Choices: systemJobTypeChoices},
		}, ujtFields()...),
		View:    superOnly,
		Summary: ujtSummary,
	})
	sjt.SubLists = append([]*SubList{
		{Name: "jobs", Target: "system_jobs", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) }},
	}, notificationSubs("systemjobtemplate", false)...)
	for _, s := range sjt.SubLists {
		if s.Associate != nil {
			s.AttachPerm = "none"
			s.CanAttach = func(c *Ctx, parent Obj, child int64) error {
				if !c.User.Superuser {
					return api.ErrForbidden
				}
				return nil
			}
		}
	}
	sjt.Actions = []*api.Action{{Name: "launch", Methods: []string{"GET", "POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		if r.Method == "GET" {
			api.WriteJSON(w, 200, Obj{"ask_variables_on_launch": true, "variables_needed_to_start": []string{}, "survey_enabled": false,
				"can_start_without_user_input": true})
			return
		}
		if !c.User.Superuser {
			api.WriteError(w, api.ErrForbidden)
			return
		}
		body, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		extra := map[string]any{}
		switch v := body["extra_vars"].(type) {
		case map[string]any:
			extra = v
		case string:
			if extra, err = vars.Parse(v); err != nil {
				api.WriteError(w, api.FieldError("extra_vars", err.Error()))
				return
			}
		}
		if err := validateSystemJobVars("extra_vars", extra); err != nil {
			api.WriteError(w, err)
			return
		}
		id, err := LaunchSystemJob(c, obj, extra, "manual")
		if err != nil {
			api.WriteError(w, err)
			return
		}
		job, _ := api.Get(c, api.Lookup("system_jobs"), id)
		out, err := api.SerializeOne(c, api.Lookup("system_jobs"), job)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out["system_job"] = id
		api.WriteJSON(w, 201, out)
	}}}

	registerJobResource(jobSpec{
		name: "system_jobs", typ: "system_job", verbose: "System Job",
		templateField: "system_job_template", templateRes: "system_job_templates", eventsName: "events", eventsRes: "system_job_events",
		visible:   superOnly,
		canManage: func(c *Ctx, job Obj) (bool, error) { return c.User.Superuser, nil },
		extra: []*Field{
			{Name: "job_type", Type: api.TChoice, ReadOnly: true, Choices: systemJobTypeChoices},
			{Name: "extra_vars", Type: api.TText, ReadOnly: true},
		},
	})
	registerEventResource("system_job_events", "system_job_event", "system_job", "system_job")
}
