package resources

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/launchpolicy"
)

const (
	policyBacktestDays    = 7
	policyBacktestMaxDays = 365
	// policyBacktestLaunches is the most launches a backtest reads, the latest.
	policyBacktestLaunches = 2000
	policyBacktestListed   = 200
)

// policyTargets are the objects a policy is attached to, by content type,
// with their resource and the column of their organization.
var policyTargets = []struct{ ct, res string }{
	{"organization", "organizations"}, {"inventory", "inventories"},
	{"jobtemplate", "job_templates"}, {"workflowjobtemplate", "workflow_job_templates"},
}

func registerLaunchPolicies() {
	pol := api.Register(&Resource{
		Name:        "launch_policies",
		Type:        "launch_policy",
		Table:       "launch_policies",
		Verbose:     "Launch Policy",
		Ordering:    "name",
		ContentType: "launchpolicy",
		OrgColumn:   "organization_id",
		Versioned:   true,
		Managed:     true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Nullable: true,
				Help: "Its organization. Without one, the policy is global: it checks every launch, and only superusers manage it."},
			{Name: "condition", Type: api.TText, Required: true, MaxLength: launchpolicy.MaxLength,
				Validate: func(c *Ctx, v any, data Obj) error { return launchpolicy.Check(str(v)) },
				Help:     "A CEL expression over launch and now: when it is true, the policy refuses the launch."},
			{Name: "message", Type: api.TText, Default: "", Help: "What a refused launch says: why, and what to do instead."},
			{Name: "mode", Type: api.TChoice, Default: policyShadow, Choices: labelled(policyShadow, "Shadow", policyEnforce, "Enforce"),
				Help: "In shadow mode, the policy records the launches it would refuse and lets them go on; in enforce mode, it refuses them."},
			{Name: "attached", Type: api.TJSON, ReadOnly: true, Virtual: true,
				Help: "The organizations, inventories and templates it is attached to: type, id and name."},
		},
		BeforeSave: saveLaunchPolicy,
		Summary:    policySummary,
		SubLists:   []*SubList{readOnlyChildSub("decisions", "launch_policy_decisions", "launch_policy_id")},
		Actions:    []*api.Action{{Name: "backtest", Methods: []string{"POST"}, Perm: "view", Handler: policyBacktestView}},
	})
	api.Register(&Resource{
		Name:         "launch_policy_decisions",
		Type:         "launch_policy_decision",
		Table:        "launch_policy_decisions",
		Verbose:      "Launch Policy Decision",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "-id",
		SearchFields: []string{"policy_name", "template_name", "message", "error"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "launch_policy", Type: api.TFK, Ref: "launch_policies", ReadOnly: true, Nullable: true},
			{Name: "policy_name", Type: api.TString, ReadOnly: true},
			{Name: "mode", Type: api.TChoice, ReadOnly: true, Choices: labelled(policyShadow, "Shadow", policyEnforce, "Enforce"),
				Help: "The policy's mode when it decided."},
			{Name: "outcome", Type: api.TChoice, ReadOnly: true, Choices: labelled(policyAllowed, "Allowed", policyRefused, "Refused",
				policyWouldRefuse, "Would have been refused")},
			{Name: "message", Type: api.TText, ReadOnly: true},
			{Name: "error", Type: api.TText, ReadOnly: true, Help: "Why the condition failed: a policy whose condition fails refuses."},
			{Name: "job", Column: "unified_job_id", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Nullable: true,
				Help: "The job launched, or failed by the refusal. Empty: the launch was refused at its caller, and no job was made."},
			{Name: "job_type", Type: api.TString, ReadOnly: true},
			{Name: "unified_job_template", Type: api.TFK, Ref: "unified_job_templates", ReadOnly: true, Nullable: true},
			{Name: "template_name", Type: api.TString, ReadOnly: true},
			{Name: "inventory", Type: api.TFK, Ref: "inventories", ReadOnly: true, Nullable: true},
			{Name: "user", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true},
			{Name: "launch_type", Type: api.TString, ReadOnly: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return fmt.Sprintf("(t.launch_policy_id IN (SELECT t.id FROM launch_policies t WHERE %s) OR t.unified_job_id IN (SELECT t.id FROM unified_jobs t WHERE %s))",
				api.ViewPredicate(c, pol, arg), api.ViewPredicate(c, api.Lookup("unified_jobs"), arg))
		},
	})
	for _, t := range policyTargets {
		r := api.Lookup(t.res)
		r.SubLists = append(r.SubLists, launchPolicySub(t.ct))
	}
	for _, name := range []string{"jobs", "workflow_jobs", "ad_hoc_commands"} {
		r := api.Lookup(name)
		r.SubLists = append(r.SubLists, &SubList{Name: "launch_policy_decisions", Target: "launch_policy_decisions", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_id = " + arg(pid) }})
	}
	api.Lookup("job_templates").Actions = append(api.Lookup("job_templates").Actions,
		&api.Action{Name: "launch_check", Methods: []string{"POST"}, Perm: "execute", Handler: launchCheckView})
	api.Lookup("workflow_job_templates").Actions = append(api.Lookup("workflow_job_templates").Actions,
		&api.Action{Name: "launch_check", Methods: []string{"POST"}, Perm: "execute", Handler: launchCheckView})
}

func installLaunchPolicyRoutes(app *api.App) {
	app.Handle("GET", "/api/v2/launch_policies/fields/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, Obj{"fields": launchpolicy.Fields()})
	})
	app.Handle("POST", "/api/v2/launch_policies/check/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		body, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		if err := launchpolicy.Check(str(body["condition"])); err != nil {
			api.WriteJSON(w, 200, Obj{"valid": false, "error": err.Error()})
			return
		}
		api.WriteJSON(w, 200, Obj{"valid": true, "error": ""})
	})
	app.Handle("POST", "/api/v2/launch_policies/backtest/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		runPolicyBacktest(c, w, r, nil)
	})
}

// saveLaunchPolicy drops a policy's attachments when it becomes global:
// it checks every launch then.
func saveLaunchPolicy(c *Ctx, old, data Obj) error {
	if old == nil || old["organization"] == nil {
		return nil
	}
	if v, given := data["organization"]; given && v == nil {
		_, err := c.DB().Exec(c, "DELETE FROM launch_policy_links WHERE launch_policy_id = $1", old["id"])
		return err
	}
	return nil
}

func policySummary(c *Ctx, objs []Obj) error {
	ids := make([]int64, 0, len(objs))
	byID := map[int64]Obj{}
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		ids = append(ids, id)
		byID[id] = o
		o["attached"] = []any{}
	}
	rows, err := c.DB().Query(c, `SELECT l.launch_policy_id, l.content_type, l.object_id, coalesce(o.name, i.name, t.name, '')
		FROM launch_policy_links l
		LEFT JOIN organizations o ON l.content_type = 'organization' AND o.id = l.object_id
		LEFT JOIN inventories i ON l.content_type = 'inventory' AND i.id = l.object_id
		LEFT JOIN unified_job_templates t ON l.content_type IN ('jobtemplate', 'workflowjobtemplate') AND t.id = l.object_id
		WHERE l.launch_policy_id = ANY($1) ORDER BY l.content_type, 4, l.object_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	types := map[string]string{"organization": "organization", "inventory": "inventory", "jobtemplate": "job_template", "workflowjobtemplate": "workflow_job_template"}
	for rows.Next() {
		var pid, oid int64
		var ct, name string
		if err := rows.Scan(&pid, &ct, &oid, &name); err != nil {
			return err
		}
		o := byID[pid]
		o["attached"] = append(o["attached"].([]any), Obj{"type": types[ct], "id": oid, "name": name})
	}
	return rows.Err()
}

// launchPolicySub attaches policies to an object, with the rights of
// notifications: change on both.
func launchPolicySub(ct string) *SubList {
	return &SubList{
		Name:   "launch_policies",
		Target: "launch_policies",
		Filter: func(arg ArgFunc, pid int64) string {
			return fmt.Sprintf("t.id IN (SELECT launch_policy_id FROM launch_policy_links WHERE content_type = %s AND object_id = %s)", arg(ct), arg(pid))
		},
		NoCreate: true,
		CanAttach: func(c *Ctx, parent Obj, cid int64) error {
			var global bool
			if err := c.DB().QueryRow(c, "SELECT organization_id IS NULL FROM launch_policies WHERE id = $1", cid).Scan(&global); err == nil && global {
				return api.BadRequest("A global launch policy checks every launch: it isn't attached.")
			}
			return allowed(api.HasPermID(c, "launch_policies", cid, "change"))
		},
		Associate: func(c *Ctx, pid, cid int64) error {
			_, err := c.DB().Exec(c, "INSERT INTO launch_policy_links (content_type, object_id, launch_policy_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", ct, pid, cid)
			return err
		},
		Disassociate: func(c *Ctx, pid, cid int64) error {
			_, err := c.DB().Exec(c, "DELETE FROM launch_policy_links WHERE content_type = $1 AND object_id = $2 AND launch_policy_id = $3", ct, pid, cid)
			return err
		},
	}
}

// checkTemplateLaunch runs a launch of a job or workflow template only to
// ask its policies.
func checkTemplateLaunch(c *Ctx, res *Resource, tpl Obj, body Obj, ov *LaunchOverrides) (*policyVerdict, error) {
	if ov == nil {
		ov = &LaunchOverrides{}
	}
	var v *policyVerdict
	ov.PolicyCheck = &v
	var err error
	if res.Name == "workflow_job_templates" {
		_, _, err = LaunchWorkflowJobTemplate(c, tpl, body, "manual", ov)
	} else {
		_, _, err = LaunchJobTemplate(c, tpl, body, "manual", ov)
	}
	if errors.Is(err, errLaunchChecked) {
		return v, nil
	}
	if err == nil {
		err = errors.New("the launch wasn't checked")
	}
	return nil, err
}

func verdictJSON(v *policyVerdict) Obj {
	list := []any{}
	refused := false
	if v != nil {
		for _, d := range v.decisions {
			list = append(list, d)
			refused = refused || d.Outcome == policyRefused
		}
	}
	return Obj{"launch_policies": list, "refused": refused}
}

// launchCheckView serves job_templates/N/launch_check/ and
// workflow_job_templates/N/launch_check/: what the launch policies would
// decide of a launch with this body. Missing survey answers and passwords
// are left out.
func launchCheckView(c *Ctx, w http.ResponseWriter, r *http.Request, tpl Obj) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	res := api.Lookup("job_templates")
	if str(tpl["type"]) == "workflow_job_template" {
		res = api.Lookup("workflow_job_templates")
	}
	v, err := checkTemplateLaunch(c, res, tpl, normalizeObj(body), nil)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, verdictJSON(v))
}

func policyBacktestView(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	runPolicyBacktest(c, w, r, obj)
}

// runPolicyBacktest evaluates a condition over the last days' launches the
// user can see, rebuilt from their jobs: the saved policy's, or the body's
// condition, over the launches the policy checks (a draft: of its
// organization, or all of them). Teams are as they are today.
func runPolicyBacktest(c *Ctx, w http.ResponseWriter, r *http.Request, saved Obj) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	days := int64(policyBacktestDays)
	if v, ok := body["days"]; ok && v != nil {
		if days, ok = api.ID(v); !ok || days < 1 || days > policyBacktestMaxDays {
			api.WriteError(w, api.FieldError("days", fmt.Sprintf("Give a number of days from 1 to %d.", policyBacktestMaxDays)))
			return
		}
	}
	cond := str(body["condition"])
	if cond == "" && saved != nil {
		cond = str(saved["condition"])
	}
	if err := launchpolicy.Check(cond); err != nil {
		api.WriteError(w, api.FieldError("condition", err.Error()))
		return
	}
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	where := []string{
		"t.type IN ('job', 'workflow_job', 'ad_hoc_command')", "NOT t.is_sliced_job", "t.job_slice_number <= 1",
		"t.created > now() - " + arg(days) + " * interval '1 day'",
		api.ViewPredicate(c, api.Lookup("unified_jobs"), arg),
	}
	switch {
	case saved != nil && saved["organization"] != nil:
		p := arg(saved["id"])
		where = append(where, `EXISTS (SELECT 1 FROM launch_policy_links l WHERE l.launch_policy_id = `+p+` AND (
			(l.content_type IN ('jobtemplate', 'workflowjobtemplate') AND l.object_id = t.unified_job_template_id)
			OR (l.content_type = 'inventory' AND l.object_id = t.inventory_id)
			OR (l.content_type = 'organization' AND (l.object_id = (SELECT organization_id FROM unified_job_templates WHERE id = t.unified_job_template_id)
				OR l.object_id = (SELECT organization_id FROM inventories WHERE id = t.inventory_id)))))`)
	case saved == nil && body["organization"] != nil:
		org, ok := api.ID(body["organization"])
		if !ok {
			api.WriteError(w, api.FieldError("organization", "Invalid organization."))
			return
		}
		o := arg(org)
		where = append(where, `(t.organization_id = `+o+` OR t.inventory_id IN (SELECT id FROM inventories WHERE organization_id = `+o+`))`)
	}
	rows, err := c.DB().Query(c, "SELECT t.id FROM unified_jobs t WHERE "+strings.Join(where, " AND ")+
		" ORDER BY t.id DESC LIMIT "+fmt.Sprint(policyBacktestLaunches+1), args...)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	truncated := len(ids) > policyBacktestLaunches
	if truncated {
		ids = ids[:policyBacktestLaunches]
	}
	refusedList := []any{}
	refused, failed := 0, 0
	for _, id := range ids {
		row, err := jobs.LoadRow(c, c.App.DB, id)
		if err != nil {
			continue
		}
		f := pastLaunch(c, row)
		in := f.input(c)
		yes, err := launchpolicy.Refuses(c, cond, in, f.at)
		if err != nil {
			failed++
		}
		if !yes && err == nil {
			continue
		}
		refused++
		if len(refusedList) < policyBacktestListed {
			e := ""
			if err != nil {
				e = err.Error()
			}
			user, _ := in["user"].(map[string]any)
			refusedList = append(refusedList, Obj{"id": id, "type": f.typ, "name": row.Str("name"), "created": api.FormatTime(f.at),
				"launch_type": f.text("launch_type"), "status": row.Str("status"), "user": user["username"], "error": e})
		}
	}
	api.WriteJSON(w, 200, Obj{"days": days, "examined": len(ids), "truncated": truncated, "refused": refused, "errors": failed, "launches": refusedList})
}

// pastLaunch rebuilds a launch's facts from its job. The branch given at
// launch is the job's when it differs from its template's.
func pastLaunch(c *Ctx, row jobs.Row) *launchFacts {
	f := &launchFacts{typ: row.Str("type"), cols: row, at: rowTime(row["created"])}
	switch f.typ {
	case "job":
		f.templateCT = "jobtemplate"
	case "workflow_job":
		f.templateCT = "workflowjobtemplate"
	}
	id := row.ID()
	f.credentials = idsQuery(c, "SELECT credential_id FROM unified_job_credentials WHERE unified_job_id = $1 ORDER BY credential_id", id)
	if f.typ == "ad_hoc_command" {
		if cid := row.Int("credential_id"); cid != 0 {
			f.credentials = []int64{cid}
		}
	} else {
		f.labels = idsQuery(c, "SELECT label_id FROM unified_job_labels WHERE unified_job_id = $1", id)
		if g := idsQuery(c, "SELECT instance_group_id FROM instance_group_links WHERE content_type = 'unifiedjob' AND object_id = $1 ORDER BY position", id); len(g) > 0 {
			f.groups = g
		}
	}
	var tplBranch string
	if tpl := row.Int("unified_job_template_id"); tpl != 0 {
		c.DB().QueryRow(c, "SELECT scm_branch FROM unified_job_templates WHERE id = $1", tpl).Scan(&tplBranch)
	}
	if b := row.Str("scm_branch"); b != tplBranch {
		f.branch = b
	}
	c.DB().QueryRow(c, `SELECT n.workflow_job_id FROM workflow_job_nodes n JOIN unified_jobs w ON w.id = n.workflow_job_id
		WHERE n.job_id = $1 AND NOT w.is_sliced_job`, id).Scan(&f.workflow)
	f.user = row.Int("created_by_id")
	if f.user == 0 && f.workflow != 0 {
		c.DB().QueryRow(c, "SELECT coalesce(created_by_id, 0) FROM unified_jobs WHERE id = $1", f.workflow).Scan(&f.user)
	}
	return f
}

func rowTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case *time.Time:
		if t != nil {
			return *t
		}
	}
	return time.Now()
}

func idsQuery(c *Ctx, sql string, args ...any) []int64 {
	out := []int64{}
	rows, err := c.DB().Query(c, sql, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}
