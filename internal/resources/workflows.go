package resources

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/jackc/pgx/v5"
)

var edgeKinds = []string{"success", "failure", "always"}

// nodeTypes are the template types a workflow node may run.
var nodeTypes = map[string]bool{"job_template": true, "project": true, "inventory_source": true, "workflow_job_template": true, "workflow_approval_template": true,
	"system_job_template": true}

var unifiedJobTypes = map[string]string{"job_template": "job", "project": "project_update", "inventory_source": "inventory_update",
	"workflow_job_template": "workflow_job", "system_job_template": "system_job", "workflow_approval_template": "workflow_approval"}

// nodePromptFields are the launch prompts saved on workflow nodes.
func nodePromptFields() []*Field {
	return []*Field{
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
		{Name: "forks", Type: api.TInt, Nullable: true, Validate: validForks},
		{Name: "job_slice_count", Type: api.TInt, Nullable: true},
		{Name: "timeout", Type: api.TInt, Nullable: true},
		{Name: "success_nodes", Type: api.TJSON, ReadOnly: true, Virtual: true},
		{Name: "failure_nodes", Type: api.TJSON, ReadOnly: true, Virtual: true},
		{Name: "always_nodes", Type: api.TJSON, ReadOnly: true, Virtual: true},
	}
}

// nodeFlowFields are a node's condition and loop (see wfexpr.go).
func nodeFlowFields() []*Field {
	expression := func(c *Ctx, v any, data Obj) error {
		if src := str(v); src != "" {
			if err := checkExpr(src); err != nil {
				return fmt.Errorf("Invalid expression: %v", err)
			}
		}
		return nil
	}
	return []*Field{
		{Name: "when", Column: "when_expr", Type: api.TText, Default: "", Validate: expression,
			Help: "Run the node only when this expression is true, e.g. deploy_env == 'prod' or nodes.build.status == 'successful'. Otherwise the node is skipped, like a node on a path not taken."},
		{Name: "loop", Column: "loop_expr", Type: api.TText, Default: "", Validate: expression,
			Help: "Run the node's template once per item of the list this expression gives, e.g. regions or ['eu', 'us']. The node fails if any item fails."},
		{Name: "loop_var", Type: api.TString, Default: "", MaxLength: 64,
			Help: "Variable holding the current item (default item). loop_index holds its position, from 0.",
			Validate: func(c *Ctx, v any, data Obj) error {
				if s := str(v); s != "" && !varNameRe.MatchString(s) {
					return fmt.Errorf("%q is not a valid variable name.", s)
				}
				return nil
			}},
		{Name: "loop_concurrency", Type: api.TInt, Default: 0, Help: "How many items run at once; 0 runs them all at once.",
			Validate: func(c *Ctx, v any, data Obj) error {
				if n, _ := num(v); n < 0 {
					return errors.New("Must be 0 or more.")
				}
				return nil
			}},
	}
}

// nodeSummary adds child node ids and the template summary to workflow nodes.
func nodeSummary(edgeTable string) func(c *Ctx, objs []Obj) error {
	return func(c *Ctx, objs []Obj) error {
		for _, o := range objs {
			id, _ := api.ID(o["id"])
			for _, k := range edgeKinds {
				o[k+"_nodes"] = []int64{}
			}
			rows, err := c.DB().Query(c, fmt.Sprintf("SELECT to_node_id, kind FROM %s WHERE from_node_id = $1 ORDER BY to_node_id", edgeTable), id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var to int64
				var kind string
				rows.Scan(&to, &kind)
				o[kind+"_nodes"] = append(o[kind+"_nodes"].([]int64), to)
			}
			rows.Close()
			if ujt, ok := api.ID(o["unified_job_template"]); ok {
				typ := ujtType(c, ujt)
				if sf, ok := o["summary_fields"].(Obj)["unified_job_template"].(Obj); ok {
					sf["unified_job_type"] = unifiedJobTypes[typ]
					if typ == "workflow_approval_template" {
						var timeout int64
						c.DB().QueryRow(c, "SELECT timeout FROM unified_job_templates WHERE id = $1", ujt).Scan(&timeout)
						sf["timeout"] = timeout
					}
				}
				if r := api.LookupType(typ); r != nil {
					o["related"].(Obj)["unified_job_template"] = r.URL(ujt)
				}
			}
		}
		return nil
	}
}

// reachable reports whether to can reach from through edges (adding from->to would form a cycle).
func reachable(c *Ctx, edgeTable string, from, to int64) bool {
	var cycle bool
	c.DB().QueryRow(c, fmt.Sprintf(`WITH RECURSIVE r(id) AS (SELECT $1::bigint UNION SELECT e.to_node_id FROM %s e JOIN r ON e.from_node_id = r.id)
		SELECT EXISTS (SELECT 1 FROM r WHERE id = $2)`, edgeTable), to, from).Scan(&cycle)
	return cycle
}

func wfjtPerm(c *Ctx, wfjt any, perm string) error {
	id, ok := api.ID(wfjt)
	if !ok {
		return api.FieldError("workflow_job_template", "This field is required.")
	}
	if perm == "change" {
		if err := requireUnmanaged(c, "workflow_job_templates", id); err != nil {
			return err
		}
	}
	return api.RequirePermID(c, "workflow_job_template", "workflow_job_templates", id, perm)
}

func registerWorkflows() {
	var ujt []*Field
	for _, f := range ujtFields() {
		if f.Name != "execution_environment" {
			ujt = append(ujt, f)
		}
	}
	wfAsk := []string{"ask_variables_on_launch", "ask_inventory_on_launch", "ask_scm_branch_on_launch", "ask_limit_on_launch",
		"ask_labels_on_launch", "ask_skip_tags_on_launch", "ask_tags_on_launch"}
	fields := []*Field{
		{Name: "organization", Type: api.TFK, Ref: "organizations", Nullable: true, Help: "The organization used to determine access to this template."},
		{Name: "extra_vars", Type: api.TText, Default: "", Validate: validateVars},
		{Name: "survey_enabled", Type: api.TBool, Default: false},
		{Name: "allow_simultaneous", Type: api.TBool, Default: false},
		{Name: "inventory", Type: api.TFK, Ref: "inventories", Nullable: true, Help: "Inventory applied as a prompt, assuming job template prompts for inventory"},
		{Name: "limit", Type: api.TString, Default: ""},
		{Name: "scm_branch", Type: api.TString, Default: ""},
		{Name: "job_tags", Type: api.TString, Default: ""},
		{Name: "skip_tags", Type: api.TString, Default: ""},
		{Name: "webhook_service", Type: api.TChoice, Default: "", Choices: webhookServices},
		{Name: "webhook_credential", Type: api.TFK, Ref: "credentials", Nullable: true},
		{Name: "webhook_mapping", Type: api.TJSON, Default: map[string]any{}, Validate: validateWebhookMapping},
		{Name: "survey_spec", Type: api.TJSON, ReadOnly: true, Hidden: true, NoFilter: true},
		folderField(),
	}
	for _, a := range wfAsk {
		fields = append(fields, &Field{Name: a, Type: api.TBool, Default: false})
	}
	wf := api.Register(&Resource{
		Name:          "workflow_job_templates",
		Type:          "workflow_job_template",
		Table:         "unified_job_templates",
		Discriminator: true,
		Verbose:       "Workflow Job Template",
		Ordering:      "name",
		ContentType:   "workflowjobtemplate",
		OrgColumn:     "organization_id",
		FolderColumn:  "folder_id",
		Versioned:     true,
		Managed:       true,
		Copyable:      true,
		Fields:        append(fields, ujt...),
		VersionExtra: func(c *Ctx, obj Obj) (Obj, error) {
			id, _ := api.ID(obj["id"])
			var spec any
			c.DB().QueryRow(c, "SELECT survey_spec FROM unified_job_templates WHERE id = $1", id).Scan(&spec)
			return Obj{"labels": templateLabels(c, id), "survey_spec": redactSurvey(spec)}, nil
		},
		RevertExtra: func(c *Ctx, obj Obj, snap Obj) error {
			id, _ := api.ID(obj["id"])
			restoreLabels(c, id, snap)
			return nil
		},
		CanAdd: func(c *Ctx, data Obj) error {
			if c.User.Superuser {
				return nil
			}
			ok, err := api.CanAddIn(c, api.Lookup("workflow_job_templates"), data)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			if inv, ok := api.ID(data["inventory"]); ok {
				return api.RequirePermID(c, "inventory", "inventories", inv, "use")
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			if err := wfjtPerm(c, old["id"], "change"); err != nil {
				return err
			}
			if inv, ok := api.ID(data["inventory"]); ok && fmt.Sprint(old["inventory"]) != fmt.Sprint(inv) {
				return api.RequirePermID(c, "inventory", "inventories", inv, "use")
			}
			return nil
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
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
				labels := []Obj{}
				rows, err := c.DB().Query(c, `SELECT l.id, l.name FROM unified_job_template_labels x JOIN labels l ON l.id = x.label_id
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
				if spec := templateSurvey(c, id); len(spec) > 0 {
					sf["survey"] = Obj{"title": spec["name"], "description": spec["description"]}
				}
			}
			return nil
		},
		Capabilities: func(c *Ctx, obj Obj, p map[string]bool) map[string]bool {
			su := c.User.Superuser
			edit := su || p["change_workflowjobtemplate"]
			return map[string]bool{"edit": edit, "delete": su || p["delete_workflowjobtemplate"], "start": su || p["execute_workflowjobtemplate"],
				"schedule": su || p["execute_workflowjobtemplate"], "copy": edit}
		},
		AfterCopy: copyWorkflowNodes,
	})
	wf.SubLists = append([]*SubList{
		{Name: "workflow_nodes", Target: "workflow_job_template_nodes", ParentField: "workflow_job_template",
			Filter: func(arg ArgFunc, pid int64) string { return "t.workflow_job_template_id = " + arg(pid) }},
		{Name: "workflow_jobs", Target: "workflow_jobs", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) }},
		labelSub("unified_job_template_labels", "unified_job_template_id"),
	}, notificationSubs("workflowjobtemplate", true)...)
	wf.Actions = []*api.Action{
		{Name: "launch", Methods: []string{"GET", "POST"}, Perm: "execute", Handler: handleWorkflowLaunch},
		{Name: "survey_spec", Methods: []string{"GET", "POST", "DELETE"}, Handler: handleSurveySpec},
		{Name: "survey_choices", Methods: []string{"GET"}, Perm: "execute", Handler: handleSurveyChoices},
		{Name: "webhook_key", Methods: []string{"GET", "POST"}, Perm: "change", Handler: handleWebhookKey},
	}

	// Template nodes.
	tn := api.Register(&Resource{
		Name:     "workflow_job_template_nodes",
		Type:     "workflow_job_template_node",
		Table:    "workflow_job_template_nodes",
		Verbose:  "Workflow Job Template Node",
		Ordering: "id",
		NoName:   true,
		Fields: append([]*Field{
			{Name: "workflow_job_template", Type: api.TFK, Ref: "workflow_job_templates", Required: true, CreateOnly: true},
			{Name: "unified_job_template", Type: api.TFK, Ref: "unified_job_templates", Nullable: true},
			{Name: "identifier", Type: api.TString, MaxLength: 512, Help: "An identifier for this node that is unique within its workflow. It is copied to workflow job nodes corresponding to this node."},
			{Name: "all_parents_must_converge", Type: api.TBool, Default: false, Help: "If enabled then the node will only run if all of the parent nodes have met the criteria to reach this node"},
		}, append(nodePromptFields(), nodeFlowFields()...)...),
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.workflow_job_template_id IN (SELECT t.id FROM unified_job_templates t WHERE t.type = 'workflow_job_template' AND (" + viewOf(c, wf, arg) + "))"
		},
		CanAdd: func(c *Ctx, data Obj) error {
			if err := wfjtPerm(c, data["workflow_job_template"], "change"); err != nil {
				return err
			}
			if id, ok := api.ID(data["unified_job_template"]); ok {
				return requireUJTPerm(c, id, "execute")
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			if err := wfjtPerm(c, old["workflow_job_template"], "change"); err != nil {
				return err
			}
			if id, ok := api.ID(data["unified_job_template"]); ok && fmt.Sprint(old["unified_job_template"]) != fmt.Sprint(id) {
				return requireUJTPerm(c, id, "execute")
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error { return wfjtPerm(c, obj["workflow_job_template"], "change") },
		// A node is changed by whoever may change its workflow.
		Perm: func(c *Ctx, obj Obj, perm string) (bool, error) {
			if perm == "view" {
				id, _ := api.ID(obj["workflow_job_template"])
				return api.CanView(c, wf, id)
			}
			err := wfjtPerm(c, obj["workflow_job_template"], "change")
			if err == api.ErrForbidden {
				return false, nil
			}
			return err == nil, err
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			merged := Obj{}
			for k, v := range old {
				merged[k] = v
			}
			for k, v := range data {
				merged[k] = v
			}
			ujt, ok := api.ID(merged["unified_job_template"])
			if !ok {
				for _, f := range schedulePrompts {
					if data[f] != nil {
						return api.FieldError(f, "Field is not configured to prompt on launch.")
					}
				}
				return nil
			}
			t := ujtType(c, ujt)
			if !nodeTypes[t] {
				return api.FieldError("unified_job_template", fmt.Sprintf("Cannot nest a %s inside a WorkflowJobTemplate", t))
			}
			if t == "workflow_approval_template" && str(merged["loop"]) != "" {
				return api.FieldError("loop", "Approval nodes cannot loop.")
			}
			if t == "system_job_template" {
				// Management jobs take only days and dry_run, from the node.
				ed, _ := merged["extra_data"].(map[string]any)
				if err := validateSystemJobVars("extra_data", ed); err != nil {
					return err
				}
			}
			if wf, _ := api.ID(merged["workflow_job_template"]); wf == ujt {
				return api.FieldError("unified_job_template", "Workflow job template cannot include itself.")
			}
			return validateSchedulePrompts(c, ujt, merged, data, old)
		},
		Summary: nodeSummary("workflow_job_template_node_edges"),
	})
	for _, kind := range edgeKinds {
		kind := kind
		tn.SubLists = append(tn.SubLists, &SubList{
			Name: kind + "_nodes", Target: "workflow_job_template_nodes",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT to_node_id FROM workflow_job_template_node_edges WHERE from_node_id = %s AND kind = %s)", arg(pid), arg(kind))
			},
			CreateDefaults: func(c *Ctx, parent Obj) Obj { return Obj{"workflow_job_template": parent["workflow_job_template"]} },
			CanAttach: func(c *Ctx, parent Obj, child int64) error {
				if err := wfjtPerm(c, parent["workflow_job_template"], "change"); err != nil {
					return err
				}
				pid, _ := api.ID(parent["id"])
				var childWF int64
				c.DB().QueryRow(c, "SELECT workflow_job_template_id FROM workflow_job_template_nodes WHERE id = $1", child).Scan(&childWF)
				if wf, _ := api.ID(parent["workflow_job_template"]); wf != childWF {
					return api.BadRequest("Nodes must be in the same workflow.")
				}
				if pid == child || reachable(c, "workflow_job_template_node_edges", pid, child) {
					return api.BadRequest("Cycle detected.")
				}
				var exists bool
				c.DB().QueryRow(c, "SELECT EXISTS (SELECT 1 FROM workflow_job_template_node_edges WHERE from_node_id = $1 AND to_node_id = $2)", pid, child).Scan(&exists)
				if exists {
					return api.BadRequest("Relationship not allowed.")
				}
				return nil
			},
			Associate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "INSERT INTO workflow_job_template_node_edges (from_node_id, to_node_id, kind) VALUES ($1, $2, $3)", pid, cid, kind)
				return err
			},
			Disassociate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM workflow_job_template_node_edges WHERE from_node_id = $1 AND to_node_id = $2 AND kind = $3", pid, cid, kind)
				return err
			},
		})
	}
	tn.SubLists = append(tn.SubLists,
		&SubList{
			Name: "credentials", Target: "credentials", NoCreate: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return "t.id IN (SELECT credential_id FROM workflow_job_template_node_credentials WHERE node_id = " + arg(pid) + ")"
			},
			CanAttach: func(c *Ctx, parent Obj, child int64) error {
				if err := wfjtPerm(c, parent["workflow_job_template"], "change"); err != nil {
					return err
				}
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
				_, err := c.DB().Exec(c, "INSERT INTO workflow_job_template_node_credentials (node_id, credential_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", pid, cid)
				return err
			},
			Disassociate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM workflow_job_template_node_credentials WHERE node_id = $1 AND credential_id = $2", pid, cid)
				return err
			},
		},
		labelSub("workflow_job_template_node_labels", "node_id"),
	)
	tn.Actions = []*api.Action{{Name: "create_approval_template", Methods: []string{"POST"}, Handler: handleCreateApprovalTemplate}}

	// Approval templates.
	at := api.Register(&Resource{
		Name:          "workflow_approval_templates",
		Type:          "workflow_approval_template",
		Table:         "unified_job_templates",
		Discriminator: true,
		Verbose:       "Workflow Approval Template",
		Ordering:      "name",
		NoCreate:      true,
		Fields: append([]*Field{
			{Name: "timeout", Type: api.TInt, Default: 0, Help: "The amount of time (in seconds) before the approval node expires and fails."},
		}, ujt...),
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.id IN (SELECT n.unified_job_template_id FROM workflow_job_template_nodes n JOIN unified_job_templates t ON t.id = n.workflow_job_template_id WHERE " + viewOf(c, wf, arg) + ")"
		},
		CanChange: func(c *Ctx, old, data Obj) error { return approvalTemplateAdmin(c, old) },
		CanDelete: func(c *Ctx, obj Obj) error { return approvalTemplateAdmin(c, obj) },
		Summary:   ujtSummary,
	})
	at.SubLists = []*SubList{{Name: "approvals", Target: "workflow_approvals", ReadOnly: true,
		Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) }}}

	// Workflow jobs.
	wjVisible := func(c *Ctx, arg ArgFunc) string {
		return "t.unified_job_template_id IN (SELECT t.id FROM unified_job_templates t WHERE t.type IN ('workflow_job_template', 'job_template') AND (" + ujtView(c, arg) + "))" +
			" OR " + c.User.Predicate(rbacArg(arg), "workflowjobtemplate", "view_workflowjobtemplate", rbac.Scope{ID: "t.unified_job_template_id", Org: "t.organization_id"})
	}
	wj := registerJobResource(jobSpec{
		name: "workflow_jobs", typ: "workflow_job", verbose: "Workflow Job",
		templateField: "workflow_job_template", templateRes: "workflow_job_templates",
		visible: wjVisible,
		// AWX leaves it off workflow jobs, and the collection waits on it when present.
		omit: []string{"event_processing_finished"},
		canManage: func(c *Ctx, job Obj) (bool, error) {
			ujt, ok := api.ID(job["unified_job_template"])
			if !ok {
				return false, nil
			}
			return ujtPerm(c, ujt, "change")
		},
		extra: []*Field{
			{Name: "extra_vars", Type: api.TText, ReadOnly: true},
			{Name: "allow_simultaneous", Type: api.TBool, ReadOnly: true},
			{Name: "job_template", Type: api.TFK, Ref: "job_templates", ReadOnly: true, Nullable: true, Help: "If automatically created for a sliced job run, the job template the workflow job was created from."},
			{Name: "is_sliced_job", Type: api.TBool, ReadOnly: true},
			{Name: "inventory", Type: api.TFK, Ref: "inventories", ReadOnly: true, Nullable: true},
			{Name: "limit", Type: api.TString, ReadOnly: true},
			{Name: "scm_branch", Type: api.TString, ReadOnly: true},
			{Name: "job_tags", Type: api.TString, ReadOnly: true},
			{Name: "skip_tags", Type: api.TString, ReadOnly: true},
			{Name: "webhook_service", Type: api.TString, ReadOnly: true},
			{Name: "webhook_credential", Type: api.TFK, Ref: "credentials", ReadOnly: true, Nullable: true},
			{Name: "webhook_guid", Type: api.TString, ReadOnly: true},
		},
	})
	for _, f := range wj.Fields {
		if f.Name == "workflow_job_template" {
			// Sliced jobs point at their job template instead.
			f.Hidden = true
		}
	}
	prevSummary := wj.Summary
	wj.Summary = func(c *Ctx, objs []Obj) error {
		if err := prevSummary(c, objs); err != nil {
			return err
		}
		for _, o := range objs {
			if !boolv(o["is_sliced_job"]) {
				if id, ok := api.ID(o["unified_job_template"]); ok {
					o["workflow_job_template"] = id
					o["related"].(Obj)["workflow_job_template"] = wf.URL(id)
				} else {
					o["workflow_job_template"] = nil
				}
			} else {
				o["workflow_job_template"] = nil
			}
		}
		return nil
	}
	wj.SubLists = append(wj.SubLists,
		&SubList{Name: "workflow_nodes", Target: "workflow_job_nodes", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.workflow_job_id = " + arg(pid) }},
		&SubList{Name: "labels", Target: "labels", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return "t.id IN (SELECT label_id FROM unified_job_labels WHERE unified_job_id = " + arg(pid) + ")"
			}},
	)
	wj.Actions = append(wj.Actions, &api.Action{Name: "relaunch", Methods: []string{"GET", "POST"}, Handler: handleWorkflowRelaunch})

	// Workflow job nodes.
	jn := api.Register(&Resource{
		Name:     "workflow_job_nodes",
		Type:     "workflow_job_node",
		Table:    "workflow_job_nodes",
		Verbose:  "Workflow Job Node",
		Ordering: "id",
		NoName:   true,
		ReadOnly: true,
		Fields: append([]*Field{
			{Name: "workflow_job", Type: api.TFK, Ref: "workflow_jobs", ReadOnly: true},
			{Name: "unified_job_template", Type: api.TFK, Ref: "unified_job_templates", ReadOnly: true, Nullable: true},
			{Name: "job", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Nullable: true},
			{Name: "do_not_run", Type: api.TBool, ReadOnly: true, Help: "Indicates that a job will not be created when True. Workflow runtime semantics will mark this True if the node is in a path that will decidedly not be ran. A value of False means the node may not run."},
			{Name: "identifier", Type: api.TString, ReadOnly: true},
			{Name: "all_parents_must_converge", Type: api.TBool, ReadOnly: true},
			{Name: "skip_reason", Type: api.TString, ReadOnly: true, Help: "Why the node did not run, when its condition decided it."},
			{Name: "loop_items", Type: api.TJSON, ReadOnly: true, Nullable: true, Help: "The items of a loop node, once its loop has started."},
			{Name: "iterations", Type: api.TJSON, ReadOnly: true, Virtual: true, Help: "The job of each loop item."},
		}, append(nodePromptFields(), nodeFlowFields()...)...),
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.workflow_job_id IN (SELECT t.id FROM unified_jobs t WHERE t.type = 'workflow_job' AND (" + api.Lookup("workflow_jobs").View(c, arg) + "))"
		},
		Summary: func(c *Ctx, objs []Obj) error {
			if err := nodeSummary("workflow_job_node_edges")(c, objs); err != nil {
				return err
			}
			for _, o := range objs {
				o["iterations"] = []Obj{}
				if o["loop_items"] != nil {
					id, _ := api.ID(o["id"])
					rows, err := c.DB().Query(c, `SELECT i.idx, j.id, j.type, j.name, j.status, j.failed, j.elapsed FROM workflow_job_node_iterations i
						JOIN unified_jobs j ON j.id = i.job_id WHERE i.node_id = $1 ORDER BY i.idx`, id)
					if err != nil {
						return err
					}
					var iters []Obj
					for rows.Next() {
						var idx int
						var jid int64
						var typ, name, status string
						var failed bool
						var elapsed float64
						rows.Scan(&idx, &jid, &typ, &name, &status, &failed, &elapsed)
						iters = append(iters, Obj{"index": idx, "job": Obj{"id": jid, "type": typ, "name": name, "status": status, "failed": failed, "elapsed": elapsed}})
					}
					rows.Close()
					if iters != nil {
						o["iterations"] = iters
					}
				}
				if jid, ok := api.ID(o["job"]); ok {
					var typ, name, status string
					var failed bool
					var elapsed float64
					if err := c.DB().QueryRow(c, "SELECT type, name, status, failed, elapsed FROM unified_jobs WHERE id = $1", jid).
						Scan(&typ, &name, &status, &failed, &elapsed); err == nil {
						o["summary_fields"].(Obj)["job"] = Obj{"id": jid, "name": name, "status": status, "failed": failed, "elapsed": elapsed, "type": typ}
						if r := api.LookupType(typ); r != nil {
							o["related"].(Obj)["job"] = r.URL(jid)
						}
					}
				}
			}
			return nil
		},
	})
	for _, kind := range edgeKinds {
		kind := kind
		jn.SubLists = append(jn.SubLists, &SubList{
			Name: kind + "_nodes", Target: "workflow_job_nodes", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT to_node_id FROM workflow_job_node_edges WHERE from_node_id = %s AND kind = %s)", arg(pid), arg(kind))
			},
		})
	}
	jn.SubLists = append(jn.SubLists, &SubList{Name: "credentials", Target: "credentials", ReadOnly: true,
		Filter: func(arg ArgFunc, pid int64) string {
			return "t.id IN (SELECT credential_id FROM workflow_job_node_credentials WHERE node_id = " + arg(pid) + ")"
		}})

	// Approvals.
	ap := registerJobResource(jobSpec{
		name: "workflow_approvals", typ: "workflow_approval", verbose: "Workflow Approval",
		templateField: "workflow_approval_template", templateRes: "workflow_approval_templates",
		visible: func(c *Ctx, arg ArgFunc) string {
			return "t.id IN (SELECT n.job_id FROM workflow_job_nodes n JOIN unified_jobs t ON t.id = n.workflow_job_id WHERE " + wjVisible(c, arg) + ")"
		},
		canManage: func(c *Ctx, job Obj) (bool, error) {
			wf := approvalWorkflowTemplate(c, job)
			if wf == 0 {
				return false, nil
			}
			return ujtPerm(c, wf, "change")
		},
		extra: []*Field{
			{Name: "timeout", Column: "approval_timeout", Type: api.TInt, ReadOnly: true},
			{Name: "timed_out", Type: api.TBool, ReadOnly: true},
			{Name: "approved_or_denied_by", Type: api.TFK, Ref: "users", ReadOnly: true, Nullable: true, Hidden: true},
			{Name: "can_approve_or_deny", Type: api.TBool, ReadOnly: true, Virtual: true},
			{Name: "approval_expiration", Type: api.TDateTime, ReadOnly: true, Virtual: true, Nullable: true},
		},
	})
	prevAp := ap.Summary
	ap.Summary = func(c *Ctx, objs []Obj) error {
		if err := prevAp(c, objs); err != nil {
			return err
		}
		for _, o := range objs {
			o["can_approve_or_deny"] = str(o["status"]) == "pending" && canApprove(c, o) == nil
			o["approval_expiration"] = nil
			if t, ok := api.ID(o["timeout"]); ok && t > 0 {
				if created, err := time.Parse(time.RFC3339Nano, str(o["created"])); err == nil {
					o["approval_expiration"] = api.FormatTime(created.Add(time.Duration(t) * time.Second))
				}
			}
			if uid, ok := api.ID(o["approved_or_denied_by"]); ok {
				var name string
				c.DB().QueryRow(c, "SELECT username FROM users WHERE id = $1", uid).Scan(&name)
				o["summary_fields"].(Obj)["approved_or_denied_by"] = Obj{"id": uid, "username": name}
			}
			if wf := approvalWorkflowJob(c, o); wf != 0 {
				var name string
				c.DB().QueryRow(c, "SELECT name FROM unified_jobs WHERE id = $1", wf).Scan(&name)
				o["summary_fields"].(Obj)["source_workflow_job"] = Obj{"id": wf, "name": name}
			}
		}
		return nil
	}
	ap.Actions = append(ap.Actions,
		&api.Action{Name: "approve", Methods: []string{"POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			decideApproval(c, w, obj, true)
		}},
		&api.Action{Name: "deny", Methods: []string{"POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			decideApproval(c, w, obj, false)
		}},
	)

	// Launched jobs know the workflow that spawned them.
	for _, name := range []string{"jobs", "project_updates", "inventory_updates", "workflow_jobs"} {
		r := api.Lookup(name)
		prev := r.Summary
		r.Summary = func(c *Ctx, objs []Obj) error {
			if prev != nil {
				if err := prev(c, objs); err != nil {
					return err
				}
			}
			for _, o := range objs {
				var wfID int64
				var wfName, status string
				err := c.DB().QueryRow(c, `SELECT w.id, w.name, w.status FROM workflow_node_jobs n JOIN unified_jobs w ON w.id = n.workflow_job_id
					WHERE n.job_id = $1`, o["id"]).Scan(&wfID, &wfName, &status)
				if err == nil {
					o["summary_fields"].(Obj)["source_workflow_job"] = Obj{"id": wfID, "name": wfName, "status": status}
					o["related"].(Obj)["source_workflow_job"] = api.Lookup("workflow_jobs").URL(wfID)
				}
			}
			return nil
		}
	}
}

func approvalTemplateAdmin(c *Ctx, obj Obj) error {
	var wf int64
	c.DB().QueryRow(c, "SELECT workflow_job_template_id FROM workflow_job_template_nodes WHERE unified_job_template_id = $1 LIMIT 1", obj["id"]).Scan(&wf)
	if wf == 0 {
		if c.User.Superuser {
			return nil
		}
		return api.ErrForbidden
	}
	return wfjtPerm(c, wf, "change")
}

// approvalWorkflowJob returns the workflow job an approval belongs to.
func approvalWorkflowJob(c *Ctx, job Obj) int64 {
	var wf int64
	c.DB().QueryRow(c, "SELECT workflow_job_id FROM workflow_job_nodes WHERE job_id = $1", job["id"]).Scan(&wf)
	return wf
}

// approvalWorkflowTemplate returns the workflow job template governing an approval.
func approvalWorkflowTemplate(c *Ctx, job Obj) int64 {
	var wf int64
	c.DB().QueryRow(c, `SELECT coalesce(w.unified_job_template_id, 0) FROM workflow_job_nodes n JOIN unified_jobs w ON w.id = n.workflow_job_id
		WHERE n.job_id = $1`, job["id"]).Scan(&wf)
	return wf
}

func canApprove(c *Ctx, job Obj) error {
	if c.User.Superuser {
		return nil
	}
	wf := approvalWorkflowTemplate(c, job)
	if wf == 0 {
		return api.ErrForbidden
	}
	ok, err := ujtPerm(c, wf, "approve")
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	return nil
}

func decideApproval(c *Ctx, w http.ResponseWriter, obj Obj, approve bool) {
	if err := canApprove(c, obj); err != nil {
		api.WriteError(w, err)
		return
	}
	if str(obj["status"]) != "pending" {
		api.WriteError(w, api.BadRequest("This workflow step has already been approved or denied."))
		return
	}
	status := "successful"
	if !approve {
		status = "failed"
	}
	id, _ := api.ID(obj["id"])
	// Claim the decision first so concurrent approvers cannot both decide.
	tag, err := c.DB().Exec(c, `UPDATE unified_jobs SET approved_or_denied_by_id = $2, started = created
		WHERE id = $1 AND status = 'pending' AND approved_or_denied_by_id IS NULL`, id, nullID(c.User.UserID))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		api.WriteError(w, api.BadRequest("This workflow step has already been approved or denied."))
		return
	}
	if err := Jobs.Complete(c, id, jobs.Result{Status: status}); err != nil {
		api.WriteError(w, err)
		return
	}
	notifyApproval(c, id, map[bool]string{true: "approved", false: "denied"}[approve])
	w.WriteHeader(204)
}

// copyWorkflowNodes copies nodes, edges, credentials, labels and the survey of a workflow template.
func copyWorkflowNodes(c *Ctx, src, dst Obj) error {
	srcID, _ := api.ID(src["id"])
	dstID, _ := api.ID(dst["id"])
	ids := map[int64]int64{}
	rows, err := c.DB().Query(c, "SELECT id FROM workflow_job_template_nodes WHERE workflow_job_template_id = $1 ORDER BY id", srcID)
	if err != nil {
		return err
	}
	var old []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		old = append(old, id)
	}
	rows.Close()
	for _, id := range old {
		var nid int64
		if err := c.DB().QueryRow(c, `INSERT INTO workflow_job_template_nodes (workflow_job_template_id, unified_job_template_id, identifier,
			all_parents_must_converge, extra_data, inventory_id, execution_environment_id, scm_branch, job_type, job_tags, skip_tags, "limit",
			diff_mode, verbosity, forks, job_slice_count, timeout, when_expr, loop_expr, loop_var, loop_concurrency)
			SELECT $2, unified_job_template_id, identifier, all_parents_must_converge, extra_data, inventory_id, execution_environment_id,
			scm_branch, job_type, job_tags, skip_tags, "limit", diff_mode, verbosity, forks, job_slice_count, timeout,
			when_expr, loop_expr, loop_var, loop_concurrency
			FROM workflow_job_template_nodes WHERE id = $1 RETURNING id`, id, dstID).Scan(&nid); err != nil {
			return err
		}
		ids[id] = nid
		// An approval template belongs to its node: the copy gets its own.
		var approval int64
		err := c.DB().QueryRow(c, `INSERT INTO unified_job_templates (type, name, description, timeout, organization_id, status, created_by_id, modified_by_id)
			SELECT 'workflow_approval_template', u.name, u.description, u.timeout, u.organization_id, 'never updated', $2, $2
			FROM workflow_job_template_nodes n JOIN unified_job_templates u ON u.id = n.unified_job_template_id
			WHERE n.id = $1 AND u.type = 'workflow_approval_template' RETURNING id`, id, nullID(c.User.UserID)).Scan(&approval)
		switch {
		case err == nil:
			if _, err := c.DB().Exec(c, "UPDATE workflow_job_template_nodes SET unified_job_template_id = $2 WHERE id = $1", nid, approval); err != nil {
				return err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		for _, q := range []string{
			"INSERT INTO workflow_job_template_node_credentials (node_id, credential_id) SELECT $2, credential_id FROM workflow_job_template_node_credentials WHERE node_id = $1",
			"INSERT INTO workflow_job_template_node_labels (node_id, label_id) SELECT $2, label_id FROM workflow_job_template_node_labels WHERE node_id = $1",
		} {
			if _, err := c.DB().Exec(c, q, id, nid); err != nil {
				return err
			}
		}
	}
	for from, nfrom := range ids {
		rows, err := c.DB().Query(c, "SELECT to_node_id, kind FROM workflow_job_template_node_edges WHERE from_node_id = $1", from)
		if err != nil {
			return err
		}
		type edge struct {
			to   int64
			kind string
		}
		var edges []edge
		for rows.Next() {
			var e edge
			rows.Scan(&e.to, &e.kind)
			edges = append(edges, e)
		}
		rows.Close()
		for _, e := range edges {
			if _, err := c.DB().Exec(c, "INSERT INTO workflow_job_template_node_edges (from_node_id, to_node_id, kind) VALUES ($1, $2, $3)", nfrom, ids[e.to], e.kind); err != nil {
				return err
			}
		}
	}
	for _, q := range []string{
		"INSERT INTO unified_job_template_labels (unified_job_template_id, label_id) SELECT $2, label_id FROM unified_job_template_labels WHERE unified_job_template_id = $1",
		"UPDATE unified_job_templates SET survey_spec = (SELECT survey_spec FROM unified_job_templates WHERE id = $1) WHERE id = $2",
	} {
		if _, err := c.DB().Exec(c, q, srcID, dstID); err != nil {
			return err
		}
	}
	return nil
}

func handleCreateApprovalTemplate(c *Ctx, w http.ResponseWriter, r *http.Request, node Obj) {
	if err := wfjtPerm(c, node["workflow_job_template"], "change"); err != nil {
		api.WriteError(w, err)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	name := str(body["name"])
	if name == "" {
		api.WriteError(w, api.FieldError("name", "This field is required."))
		return
	}
	timeout, _ := api.ID(body["timeout"])
	if timeout < 0 {
		api.WriteError(w, api.FieldError("timeout", "Ensure this value is greater than or equal to 0."))
		return
	}
	nid, _ := api.ID(node["id"])
	var id int64
	err = c.WithTx(func(c *Ctx) error {
		// Reuse the node's approval template when it already has one.
		if cur, ok := api.ID(node["unified_job_template"]); ok && ujtType(c, cur) == "workflow_approval_template" {
			id = cur
			_, err := c.DB().Exec(c, "UPDATE unified_job_templates SET name = $2, description = $3, timeout = $4, modified = now() WHERE id = $1",
				id, name, str(body["description"]), timeout)
			return err
		}
		var org *int64
		c.DB().QueryRow(c, "SELECT organization_id FROM unified_job_templates WHERE id = $1", node["workflow_job_template"]).Scan(&org)
		if err := c.DB().QueryRow(c, `INSERT INTO unified_job_templates (type, name, description, timeout, organization_id, status, created_by_id, modified_by_id)
			VALUES ('workflow_approval_template', $1, $2, $3, $4, 'never updated', $5, $5) RETURNING id`,
			name, str(body["description"]), timeout, org, nullID(c.User.UserID)).Scan(&id); err != nil {
			return err
		}
		_, err := c.DB().Exec(c, "UPDATE workflow_job_template_nodes SET unified_job_template_id = $2, modified = now() WHERE id = $1", nid, id)
		return err
	})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	res := api.Lookup("workflow_approval_templates")
	obj, err := api.Get(c, res, id)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out, err := api.SerializeOne(c, res, obj)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 201, out)
}
