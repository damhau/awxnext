package resources

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/rbac"
)

var askFields = []string{
	"ask_scm_branch_on_launch", "ask_diff_mode_on_launch", "ask_variables_on_launch", "ask_limit_on_launch",
	"ask_tags_on_launch", "ask_skip_tags_on_launch", "ask_job_type_on_launch", "ask_verbosity_on_launch",
	"ask_inventory_on_launch", "ask_credential_on_launch", "ask_execution_environment_on_launch",
	"ask_labels_on_launch", "ask_forks_on_launch", "ask_job_slice_count_on_launch", "ask_timeout_on_launch",
	"ask_instance_groups_on_launch",
}

func askFieldDefs() []*Field {
	var out []*Field
	for _, n := range askFields {
		out = append(out, &Field{Name: n, Type: api.TBool, Default: false})
	}
	return out
}

var webhookServices = labelled("", "", "github", "GitHub", "gitlab", "GitLab", "bitbucket_dc", "BitBucket DataCenter", "generic", "Generic (JSONPath mapping)")

// credentialSlot identifies the "one per" rule for template credentials:
// one per credential type, and one per vault id for vault credentials.
func credentialSlot(c *Ctx, credID int64) (slot string, kind string, err error) {
	var typeID int64
	var vaultID string
	err = c.DB().QueryRow(c, `SELECT ct.id, ct.kind, coalesce(cr.inputs->>'vault_id', '') FROM credentials cr
		JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1`, credID).Scan(&typeID, &kind, &vaultID)
	if err != nil {
		return "", "", api.FieldError("id", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", credID))
	}
	slot = fmt.Sprint(typeID)
	if kind == "vault" {
		slot += "/" + vaultID
	}
	return slot, kind, nil
}

var nonJobKinds = map[string]bool{"scm": true, "registry": true, "galaxy": true, "external": true, "token": true, "cryptography": true, "insights": true}

// checkTemplateCredential validates adding a credential to a template's set.
func checkTemplateCredential(c *Ctx, existing []int64, cred int64) error {
	slot, kind, err := credentialSlot(c, cred)
	if err != nil {
		return err
	}
	if nonJobKinds[kind] {
		return api.BadRequest(fmt.Sprintf("Cannot assign a Credential of kind `%s`.", kind))
	}
	for _, e := range existing {
		if e == cred {
			continue
		}
		s, _, _ := credentialSlot(c, e)
		if s == slot {
			if kind == "vault" {
				return api.BadRequest("Cannot assign multiple vault credentials with the same vault id.")
			}
			return api.BadRequest("Cannot assign multiple credentials of the same credential type.")
		}
	}
	return nil
}

// templateLabels returns the label ids of a template, sorted.
func templateLabels(c *Ctx, ujt int64) []int64 {
	out := []int64{}
	rows, err := c.DB().Query(c, "SELECT label_id FROM unified_job_template_labels WHERE unified_job_template_id = $1 ORDER BY label_id", ujt)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// restoreLabels makes a template's labels match a snapshot.
func restoreLabels(c *Ctx, ujt int64, snap Obj) {
	labels, ok := snap["labels"].([]any)
	if !ok {
		return
	}
	c.DB().Exec(c, "DELETE FROM unified_job_template_labels WHERE unified_job_template_id = $1", ujt)
	for _, l := range labels {
		if lid, ok := api.ID(l); ok {
			c.DB().Exec(c, "INSERT INTO unified_job_template_labels (unified_job_template_id, label_id) SELECT $1, id FROM labels WHERE id = $2 ON CONFLICT DO NOTHING", ujt, lid)
		}
	}
}

func templateCredentials(c *Ctx, ujt int64) []int64 {
	rows, err := c.DB().Query(c, "SELECT credential_id FROM unified_job_template_credentials WHERE unified_job_template_id = $1 ORDER BY credential_id", ujt)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// checkPlaybook verifies the playbook exists in the project.
func checkPlaybook(c *Ctx, projectID int64, playbook string) error {
	proj, err := api.Get(c, api.Lookup("projects"), projectID)
	if err != nil {
		return api.FieldError("project", "Invalid project.")
	}
	files, _ := projectFiles(c, proj)
	if str(proj["scm_type"]) != "" && str(proj["scm_revision"]) == "" {
		// Not synced yet: accept and verify at run time.
		return nil
	}
	for _, p := range files.Playbooks {
		if p == playbook {
			return nil
		}
	}
	return api.FieldError("playbook", "Playbook not found for project.")
}

func registerJobTemplates() {
	jt := api.Register(&Resource{
		Name:          "job_templates",
		Type:          "job_template",
		Table:         "unified_job_templates",
		Discriminator: true,
		Verbose:       "Job Template",
		Ordering:      "name",
		ContentType:   "jobtemplate",
		OrgColumn:     "organization_id",
		FolderColumn:  "folder_id",
		Versioned:     true,
		Managed:       true,
		Copyable:      true,
		Fields: append(append([]*Field{
			{Name: "job_type", Type: api.TChoice, Default: "run", Choices: labelled("run", "Run", "check", "Check")},
			{Name: "inventory", Type: api.TFK, Ref: "inventories", Nullable: true},
			{Name: "project", Type: api.TFK, Ref: "projects", Nullable: true},
			{Name: "playbook", Type: api.TString, Default: "", MaxLength: 1024},
			{Name: "scm_branch", Type: api.TString, Default: "", MaxLength: 1024, Help: "Branch to use in job run. Project default used if blank. Only allowed if project allow_override field is set to true."},
			{Name: "forks", Type: api.TInt, Default: 0, Validate: validForks},
			{Name: "limit", Type: api.TText, Default: ""},
			{Name: "verbosity", Type: api.TChoice, Default: 0, Choices: verbosityChoices},
			{Name: "extra_vars", Type: api.TText, Default: "", Validate: validateVars},
			{Name: "job_tags", Type: api.TText, Default: ""},
			{Name: "force_handlers", Type: api.TBool, Default: false},
			{Name: "skip_tags", Type: api.TText, Default: ""},
			{Name: "start_at_task", Type: api.TText, Default: ""},
			{Name: "timeout", Type: api.TInt, Default: 0, Help: "The amount of time (in seconds) to run before the task is canceled."},
			{Name: "use_fact_cache", Type: api.TBool, Default: false, Help: "If enabled, the service will act as an Ansible Fact Cache Plugin; persisting facts at the end of a playbook run to the database and caching facts for use by Ansible."},
			{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true, Nullable: true, Help: "The organization used to determine access to this template (from the project)."},
			{Name: "host_config_key", Type: api.TString, Default: "", MaxLength: 1024},
			{Name: "survey_enabled", Type: api.TBool, Default: false},
			{Name: "become_enabled", Type: api.TBool, Default: false},
			{Name: "diff_mode", Type: api.TBool, Default: false, Help: "If enabled, textual changes made to any templated files on the host are shown in the standard output"},
			{Name: "allow_simultaneous", Type: api.TBool, Default: false},
			{Name: "job_slice_count", Type: api.TInt, Default: 1, Help: "The number of jobs to slice into at runtime. Will cause the Job Template to launch a workflow if value is greater than 1."},
			{Name: "webhook_service", Type: api.TChoice, Default: "", Choices: webhookServices},
			{Name: "webhook_credential", Type: api.TFK, Ref: "credentials", Nullable: true},
			{Name: "prevent_instance_group_fallback", Type: api.TBool, Default: false},
			{Name: "survey_spec", Type: api.TJSON, ReadOnly: true, Hidden: true, NoFilter: true},
			folderField(),
		}, askFieldDefs()...), ujtFields()...),
		Relations: map[string]api.Relation{
			"credentials": {Target: "credentials", Join: func(a, b string) string {
				return fmt.Sprintf("EXISTS (SELECT 1 FROM unified_job_template_credentials x WHERE x.unified_job_template_id = %s.id AND x.credential_id = %s.id)", a, b)
			}},
			"labels": {Target: "labels", Join: func(a, b string) string {
				return fmt.Sprintf("EXISTS (SELECT 1 FROM unified_job_template_labels x WHERE x.unified_job_template_id = %s.id AND x.label_id = %s.id)", a, b)
			}},
		},
		CanAdd: func(c *Ctx, data Obj) error {
			if c.User.Superuser {
				return nil
			}
			if err := api.RequirePermID(c, "project", "projects", data["project"], "use"); err != nil {
				return err
			}
			if err := api.RequirePermID(c, "inventory", "inventories", data["inventory"], "use"); err != nil {
				return err
			}
			if f, ok := api.ID(data["folder"]); ok {
				org, _ := api.ID(data["organization"])
				ok, err := c.User.HasFolder(c, c.App.DB, "add_jobtemplate", f, org)
				if err != nil {
					return err
				}
				if !ok {
					return api.ErrForbidden
				}
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			ok, err := api.HasPerm(c, api.Lookup("job_templates"), old, "change")
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			if c.User.Superuser {
				return nil
			}
			for _, f := range []struct{ field, res string }{{"project", "projects"}, {"inventory", "inventories"}} {
				if fmt.Sprint(old[f.field]) != fmt.Sprint(data[f.field]) {
					if err := api.RequirePermID(c, f.field, f.res, data[f.field], "use"); err != nil {
						return err
					}
				}
			}
			return nil
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			merged := Obj{}
			for k, v := range old {
				merged[k] = v
			}
			for k, v := range data {
				merged[k] = v
			}
			pid, ok := api.ID(merged["project"])
			if !ok {
				return api.FieldError("project", "Job Templates must have a project assigned.")
			}
			var org *int64
			var allowOverride bool
			c.DB().QueryRow(c, "SELECT organization_id, allow_override FROM unified_job_templates WHERE id = $1", pid).Scan(&org, &allowOverride)
			data["organization"] = org
			if _, ok := api.ID(merged["inventory"]); !ok && !boolv(merged["ask_inventory_on_launch"]) {
				return api.FieldError("inventory", "Job Template must provide 'inventory' or allow prompting for it.")
			}
			if str(merged["playbook"]) == "" {
				return api.FieldError("playbook", "This field is required.")
			}
			if _, given := data["playbook"]; given || old == nil || fmt.Sprint(old["project"]) != fmt.Sprint(merged["project"]) {
				if err := checkPlaybook(c, pid, str(merged["playbook"])); err != nil {
					return err
				}
			}
			if str(merged["scm_branch"]) != "" && !allowOverride {
				return api.FieldError("scm_branch", "Project does not allow overriding branch.")
			}
			if boolv(merged["ask_scm_branch_on_launch"]) && !allowOverride {
				return api.FieldError("ask_scm_branch_on_launch", "Project does not allow overriding branch.")
			}
			if n, ok := api.ID(merged["job_slice_count"]); ok && n < 1 {
				return api.FieldError("job_slice_count", "Ensure this value is greater than or equal to 1.")
			}
			if old == nil {
				data["status"] = "never updated"
			}
			return nil
		},
		Summary: func(c *Ctx, objs []Obj) error {
			if err := ujtSummary(c, objs); err != nil {
				return err
			}
			for _, o := range objs {
				id, _ := api.ID(o["id"])
				sf := o["summary_fields"].(Obj)
				creds := []Obj{}
				rows, err := c.DB().Query(c, `SELECT cr.id, cr.name, cr.description, ct.kind, coalesce(ct.namespace, ct.kind), ct.id
					FROM unified_job_template_credentials x JOIN credentials cr ON cr.id = x.credential_id
					JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE x.unified_job_template_id = $1 ORDER BY cr.name`, id)
				if err != nil {
					return err
				}
				for rows.Next() {
					var cid, tid int64
					var name, desc, kind, ns string
					rows.Scan(&cid, &name, &desc, &kind, &ns, &tid)
					creds = append(creds, Obj{"id": cid, "name": name, "description": desc, "kind": ns, "cloud": kind == "cloud", "credential_type_id": tid})
				}
				rows.Close()
				sf["credentials"] = creds
				labels := []Obj{}
				rows, err = c.DB().Query(c, `SELECT l.id, l.name FROM unified_job_template_labels x JOIN labels l ON l.id = x.label_id
					WHERE x.unified_job_template_id = $1 ORDER BY l.name`, id)
				if err != nil {
					return err
				}
				for rows.Next() {
					var lid int64
					var name string
					rows.Scan(&lid, &name)
					labels = append(labels, Obj{"id": lid, "name": name})
				}
				rows.Close()
				sf["labels"] = Obj{"count": len(labels), "results": labels}
				var spec map[string]any
				c.DB().QueryRow(c, "SELECT survey_spec FROM unified_job_templates WHERE id = $1", id).Scan(&spec)
				if len(spec) > 0 {
					sf["survey"] = Obj{"title": spec["name"], "description": spec["description"]}
				}
				o["custom_virtualenv"] = nil
			}
			return nil
		},
		Capabilities: func(c *Ctx, obj Obj, p map[string]bool) map[string]bool {
			su := c.User.Superuser
			edit := su || p["change_jobtemplate"]
			return map[string]bool{"edit": edit, "delete": su || p["delete_jobtemplate"], "start": su || p["execute_jobtemplate"],
				"schedule": su || p["execute_jobtemplate"], "copy": edit}
		},
		AfterCopy: func(c *Ctx, src, dst Obj) error {
			for _, q := range []string{
				"INSERT INTO unified_job_template_credentials (unified_job_template_id, credential_id) SELECT $2, credential_id FROM unified_job_template_credentials WHERE unified_job_template_id = $1",
				"INSERT INTO unified_job_template_labels (unified_job_template_id, label_id) SELECT $2, label_id FROM unified_job_template_labels WHERE unified_job_template_id = $1",
				"UPDATE unified_job_templates SET survey_spec = (SELECT survey_spec FROM unified_job_templates WHERE id = $1) WHERE id = $2",
				"INSERT INTO instance_group_links (content_type, object_id, instance_group_id, position) SELECT content_type, $2, instance_group_id, position FROM instance_group_links WHERE content_type = 'jobtemplate' AND object_id = $1",
			} {
				if _, err := c.DB().Exec(c, q, src["id"], dst["id"]); err != nil {
					return err
				}
			}
			return nil
		},
		VersionExtra: func(c *Ctx, obj Obj) (Obj, error) {
			id, _ := api.ID(obj["id"])
			var spec any
			c.DB().QueryRow(c, "SELECT survey_spec FROM unified_job_templates WHERE id = $1", id).Scan(&spec)
			creds := templateCredentials(c, id)
			if creds == nil {
				creds = []int64{}
			}
			return Obj{"credentials": creds, "labels": templateLabels(c, id), "survey_spec": redactSurvey(spec)}, nil
		},
		RevertExtra: func(c *Ctx, obj Obj, snap Obj) error {
			id, _ := api.ID(obj["id"])
			restoreLabels(c, id, snap)
			if creds, ok := snap["credentials"].([]any); ok {
				c.DB().Exec(c, "DELETE FROM unified_job_template_credentials WHERE unified_job_template_id = $1", id)
				for _, cr := range creds {
					if cid, ok := api.ID(cr); ok {
						c.DB().Exec(c, "INSERT INTO unified_job_template_credentials (unified_job_template_id, credential_id) SELECT $1, id FROM credentials WHERE id = $2 ON CONFLICT DO NOTHING", id, cid)
					}
				}
			}
			return nil
		},
	})
	jt.SubLists = append([]*SubList{
		{
			Name: "jobs", Target: "jobs", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) },
		},
		{
			Name: "credentials", Target: "credentials", NoCreate: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT credential_id FROM unified_job_template_credentials WHERE unified_job_template_id = %s)", arg(pid))
			},
			CanAttach: func(c *Ctx, parent Obj, cred int64) error {
				if err := api.RequirePermID(c, "id", "credentials", cred, "use"); err != nil {
					return err
				}
				pid, _ := api.ID(parent["id"])
				return checkTemplateCredential(c, templateCredentials(c, pid), cred)
			},
			Associate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "INSERT INTO unified_job_template_credentials (unified_job_template_id, credential_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", pid, cid)
				return err
			},
			Disassociate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM unified_job_template_credentials WHERE unified_job_template_id = $1 AND credential_id = $2", pid, cid)
				return err
			},
		},
		labelSub("unified_job_template_labels", "unified_job_template_id"),
		orderedLinkSub("instance_groups", "instance_groups", "instance_group_links", "instance_group_id", "jobtemplate"),
	}, notificationSubs("jobtemplate", false)...)
	jt.Actions = []*api.Action{
		{Name: "launch", Methods: []string{"GET", "POST"}, Perm: "execute", Handler: handleLaunch},
		{Name: "survey_spec", Methods: []string{"GET", "POST", "DELETE"}, Handler: handleSurveySpec},
		{Name: "survey_choices", Methods: []string{"GET"}, Perm: "execute", Handler: handleSurveyChoices},
	}

	registerJobResource(jobSpec{
		name: "jobs", typ: "job", verbose: "Job",
		templateField: "job_template", templateRes: "job_templates", eventsName: "job_events", eventsRes: "job_events",
		visible: func(c *Ctx, arg ArgFunc) string {
			// Visible with the template, or through organization roles (also after the template was deleted).
			return "t.unified_job_template_id IN (SELECT t.id FROM unified_job_templates t WHERE t.type = 'job_template' AND (" + viewOf(c, jt, arg) + "))" +
				" OR " + c.User.Predicate(rbacArg(arg), "jobtemplate", "view_jobtemplate", rbac.Scope{ID: "t.unified_job_template_id", Org: "t.organization_id"})
		},
		canManage: func(c *Ctx, job Obj) (bool, error) {
			if id, ok := api.ID(job["job_template"]); ok {
				if ok, err := api.HasPermID(c, "job_templates", id, "change"); err == nil && ok {
					return true, nil
				}
			}
			if org, ok := api.ID(job["organization"]); ok {
				return c.User.HasOrg(c, c.App.DB, "change_organization", org)
			}
			return false, nil
		},
		canCancel: func(c *Ctx, job Obj) (bool, error) {
			if id, ok := api.ID(job["job_template"]); ok {
				return api.HasPermID(c, "job_templates", id, "change")
			}
			return false, nil
		},
		extra: []*Field{
			{Name: "job_type", Type: api.TChoice, ReadOnly: true, Choices: labelled("run", "Run", "check", "Check", "scan", "Scan")},
			{Name: "inventory", Type: api.TFK, Ref: "inventories", ReadOnly: true, Nullable: true},
			{Name: "project", Type: api.TFK, Ref: "projects", ReadOnly: true, Nullable: true},
			{Name: "playbook", Type: api.TString, ReadOnly: true},
			{Name: "scm_branch", Type: api.TString, ReadOnly: true},
			{Name: "forks", Type: api.TInt, ReadOnly: true},
			{Name: "limit", Type: api.TText, ReadOnly: true},
			{Name: "verbosity", Type: api.TChoice, ReadOnly: true, Choices: verbosityChoices},
			{Name: "extra_vars", Type: api.TText, ReadOnly: true},
			{Name: "job_tags", Type: api.TText, ReadOnly: true},
			{Name: "force_handlers", Type: api.TBool, ReadOnly: true},
			{Name: "skip_tags", Type: api.TText, ReadOnly: true},
			{Name: "start_at_task", Type: api.TText, ReadOnly: true},
			{Name: "timeout", Type: api.TInt, ReadOnly: true},
			{Name: "use_fact_cache", Type: api.TBool, ReadOnly: true},
			{Name: "scm_revision", Type: api.TString, ReadOnly: true, Help: "The SCM Revision from the Project used for this job, if available"},
			{Name: "artifacts", Type: api.TJSON, ReadOnly: true},
			{Name: "job_slice_number", Type: api.TInt, ReadOnly: true},
			{Name: "job_slice_count", Type: api.TInt, ReadOnly: true},
			{Name: "diff_mode", Type: api.TBool, ReadOnly: true},
			{Name: "become_enabled", Type: api.TBool, ReadOnly: true},
			{Name: "allow_simultaneous", Type: api.TBool, ReadOnly: true},
			{Name: "webhook_service", Type: api.TString, ReadOnly: true},
			{Name: "webhook_credential", Type: api.TFK, Ref: "credentials", ReadOnly: true, Nullable: true},
			{Name: "webhook_guid", Type: api.TString, ReadOnly: true},
			{Name: "survey_passwords", Type: api.TJSON, ReadOnly: true, Hidden: true},
			{Name: "start_args", Type: api.TText, ReadOnly: true, Hidden: true, NoFilter: true},
		},
	})
	jobsRes := api.Lookup("jobs")
	prev := jobsRes.Serialize
	jobsRes.Serialize = func(c *Ctx, o Obj) {
		prev(c, o)
		o["extra_vars"] = maskSurveyPasswords(str(o["extra_vars"]))
		o["passwords_needed_to_start"] = []string{}
	}
	prevSummary := jobsRes.Summary
	jobsRes.Summary = func(c *Ctx, objs []Obj) error {
		if err := prevSummary(c, objs); err != nil {
			return err
		}
		// The passwords a relaunch asks for, for every job of the page at once.
		ids := make([]int64, len(objs))
		for i, o := range objs {
			ids[i], _ = api.ID(o["id"])
		}
		needs := map[int64][]string{}
		rows, err := c.DB().Query(c, `SELECT j.unified_job_id, cr.inputs, ct.kind FROM unified_job_credentials j
			JOIN credentials cr ON cr.id = j.credential_id JOIN credential_types ct ON ct.id = cr.credential_type_id
			WHERE j.unified_job_id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var jid int64
			var inputs map[string]any
			var kind string
			if err := rows.Scan(&jid, &inputs, &kind); err == nil {
				needs[jid] = append(needs[jid], askedPasswords(kind, inputs)...)
			}
		}
		rows.Close()
		for i, o := range objs {
			sort.Strings(needs[ids[i]])
			o["passwords_needed_to_start"] = nonNil(needs[ids[i]])
		}
		for _, o := range objs {
			id, _ := api.ID(o["id"])
			counts := Obj{}
			rows, err := c.DB().Query(c, `SELECT CASE WHEN dark > 0 THEN 'dark' WHEN failures > 0 THEN 'failures' WHEN changed > 0 THEN 'changed'
				WHEN ok > 0 THEN 'ok' WHEN rescued > 0 THEN 'rescued' WHEN ignored > 0 THEN 'ignored' ELSE 'skipped' END, count(*)
				FROM job_host_summaries WHERE job_id = $1 GROUP BY 1`, id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var k string
				var n int64
				rows.Scan(&k, &n)
				counts[k] = n
			}
			rows.Close()
			o["host_status_counts"] = counts
			creds := []Obj{}
			rows, err = c.DB().Query(c, `SELECT cr.id, cr.name, cr.description, coalesce(ct.namespace, ct.kind), ct.kind = 'cloud'
				FROM unified_job_credentials x JOIN credentials cr ON cr.id = x.credential_id JOIN credential_types ct ON ct.id = cr.credential_type_id
				WHERE x.unified_job_id = $1 ORDER BY cr.name`, id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var cid int64
				var name, desc, kind string
				var cloud bool
				rows.Scan(&cid, &name, &desc, &kind, &cloud)
				creds = append(creds, Obj{"id": cid, "name": name, "description": desc, "kind": kind, "cloud": cloud})
			}
			rows.Close()
			o["summary_fields"].(Obj)["credentials"] = creds
		}
		return nil
	}
	jobsRes.SubLists = append(jobsRes.SubLists,
		&SubList{Name: "job_host_summaries", Target: "job_host_summaries", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.job_id = " + arg(pid) }},
		&SubList{Name: "credentials", Target: "credentials", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT credential_id FROM unified_job_credentials WHERE unified_job_id = %s)", arg(pid))
			}},
		&SubList{Name: "labels", Target: "labels", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT label_id FROM unified_job_labels WHERE unified_job_id = %s)", arg(pid))
			}},
	)
	jobsRes.Actions = append(jobsRes.Actions, &api.Action{Name: "relaunch", Methods: []string{"GET", "POST"}, Handler: handleRelaunch})
	registerEventResource("job_events", "job_event", "job", "job")

	api.Register(&Resource{
		Name:         "job_host_summaries",
		Type:         "job_host_summary",
		Table:        "job_host_summaries",
		Verbose:      "Job Host Summary",
		NoName:       true,
		NoTimestamps: true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "host_name",
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: "job", Type: api.TFK, Ref: "jobs", ReadOnly: true},
			{Name: "host", Type: api.TFK, Ref: "hosts", ReadOnly: true, Nullable: true},
			{Name: "host_name", Type: api.TString, ReadOnly: true},
			{Name: "changed", Type: api.TInt, ReadOnly: true},
			{Name: "dark", Type: api.TInt, ReadOnly: true},
			{Name: "failures", Type: api.TInt, ReadOnly: true},
			{Name: "ok", Type: api.TInt, ReadOnly: true},
			{Name: "processed", Type: api.TInt, ReadOnly: true},
			{Name: "skipped", Type: api.TInt, ReadOnly: true},
			{Name: "failed", Type: api.TBool, ReadOnly: true},
			{Name: "ignored", Type: api.TInt, ReadOnly: true},
			{Name: "rescued", Type: api.TInt, ReadOnly: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.job_id IN (SELECT t.id FROM unified_jobs t WHERE t.type = 'job' AND (" + api.Lookup("jobs").View(c, arg) + "))"
		},
	})
	hosts := api.Lookup("hosts")
	hosts.SubLists = append(hosts.SubLists, &SubList{Name: "job_host_summaries", Target: "job_host_summaries", ReadOnly: true,
		Filter: func(arg ArgFunc, pid int64) string { return "t.host_id = " + arg(pid) }})
	hosts.SubLists = append(hosts.SubLists, &SubList{Name: "job_events", Target: "job_events", ReadOnly: true,
		Filter: func(arg ArgFunc, pid int64) string { return "t.host_id = " + arg(pid) }})
}

// labelSub is the labels sublist of templates: associate by id, or by name (created in the template's organization).
func labelSub(table, column string) *SubList {
	return &SubList{
		Name: "labels", Target: "labels",
		Filter: func(arg ArgFunc, pid int64) string {
			return fmt.Sprintf("t.id IN (SELECT label_id FROM %s WHERE %s = %s)", table, column, arg(pid))
		},
		CreateDefaults: func(c *Ctx, parent Obj) Obj { return Obj{"organization": parent["organization"]} },
		FindExisting: func(c *Ctx, parent Obj, data Obj) (int64, bool, error) {
			name := str(data["name"])
			org, ok := api.ID(data["organization"])
			if !ok {
				org, _ = api.ID(parent["organization"])
			}
			var id int64
			if err := c.DB().QueryRow(c, "SELECT id FROM labels WHERE organization_id = $1 AND name = $2", org, name).Scan(&id); err == nil {
				return id, true, nil
			}
			return 0, false, nil
		},
		CanAttach: func(c *Ctx, parent Obj, lid int64) error { return canUseLabel(c, lid) },
		Associate: func(c *Ctx, pid, lid int64) error {
			_, err := c.DB().Exec(c, fmt.Sprintf("INSERT INTO %s (%s, label_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", table, column), pid, lid)
			return err
		},
		Disassociate: func(c *Ctx, pid, lid int64) error {
			_, err := c.DB().Exec(c, fmt.Sprintf("DELETE FROM %s WHERE %s = $1 AND label_id = $2", table, column), pid, lid)
			return err
		},
	}
}

// maskSurveyPasswords hides encrypted survey answers in job extra_vars.
func maskSurveyPasswords(ev string) string {
	if !strings.Contains(ev, "$encrypted$") {
		return ev
	}
	return encryptedValueRe.ReplaceAllString(ev, `"$$encrypted$$"`)
}

func redactSurvey(spec any) any {
	m, ok := spec.(map[string]any)
	if !ok {
		return spec
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	if qs, ok := m["spec"].([]any); ok {
		var nq []any
		for _, q := range qs {
			qm, ok := q.(map[string]any)
			if !ok {
				continue
			}
			c := map[string]any{}
			for k, v := range qm {
				c[k] = v
			}
			if c["type"] == "password" {
				if s, _ := c["default"].(string); s != "" {
					c["default"] = crypto.Placeholder
				}
			}
			nq = append(nq, c)
		}
		out["spec"] = nq
	}
	return out
}

func handleSurveySpec(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	res := api.Lookup(str(obj["type"]) + "s")
	if str(obj["type"]) == "workflow_job_template" {
		res = api.Lookup("workflow_job_templates")
	}
	id, _ := api.ID(obj["id"])
	var spec map[string]any
	c.DB().QueryRow(c, "SELECT survey_spec FROM unified_job_templates WHERE id = $1", id).Scan(&spec)
	if r.Method == "GET" {
		if spec == nil {
			spec = map[string]any{}
		}
		api.WriteJSON(w, 200, redactSurvey(spec))
		return
	}
	if err := api.CheckManaged(c, res, obj); err != nil {
		api.WriteError(w, err)
		return
	}
	ok, err := api.HasPerm(c, res, obj, "change")
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if !ok {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	if r.Method == "DELETE" {
		c.DB().Exec(c, "UPDATE unified_job_templates SET survey_spec = '{}' WHERE id = $1", id)
		api.RecordVersion(c, res, id, "update", Obj{"survey_spec": "deleted"})
		w.WriteHeader(204)
		return
	}
	data, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	newSpec, err := ValidateSurveySpec(c, normalize(data), spec)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if _, err := c.DB().Exec(c, "UPDATE unified_job_templates SET survey_spec = $2, modified = now() WHERE id = $1", id, newSpec); err != nil {
		api.WriteError(w, err)
		return
	}
	api.RecordVersion(c, res, id, "update", Obj{"survey_spec": "updated"})
	api.WriteJSON(w, 200, redactSurvey(newSpec))
}
