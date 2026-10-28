package resources

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/vars"
)

// workflowPrompts maps workflow launch fields to the template flag allowing them.
var workflowPrompts = map[string]string{
	"inventory": "ask_inventory_on_launch", "limit": "ask_limit_on_launch", "scm_branch": "ask_scm_branch_on_launch",
	"job_tags": "ask_tags_on_launch", "skip_tags": "ask_skip_tags_on_launch", "labels": "ask_labels_on_launch",
}

// LaunchWorkflowJobTemplate creates a workflow job with a copy of the template's nodes.
func LaunchWorkflowJobTemplate(c *Ctx, wf Obj, body Obj, launchType string, ov *LaunchOverrides) (int64, Obj, error) {
	id, _ := api.ID(wf["id"])
	ignored, accepted := Obj{}, Obj{}
	for field, flag := range workflowPrompts {
		v, ok := body[field]
		if !ok {
			continue
		}
		if boolv(wf[flag]) {
			accepted[field] = v
		} else {
			ignored[field] = v
		}
	}
	if ov != nil {
		for k, v := range ov.Trusted {
			accepted[k] = v
		}
	}
	inv := wf["inventory"]
	if v, ok := accepted["inventory"]; ok {
		if invID, ok := api.ID(v); ok {
			if err := api.RequirePermID(c, "inventory", "inventories", invID, "use"); err != nil {
				return 0, nil, err
			}
			inv = invID
		} else if v != nil {
			return 0, nil, api.FieldError("inventory", "Invalid inventory.")
		}
	}

	base, _ := vars.Parse(str(wf["extra_vars"]))
	launchVars := map[string]any{}
	switch x := body["extra_vars"].(type) {
	case map[string]any:
		launchVars = x
	case string:
		p, err := vars.Parse(x)
		if err != nil {
			return 0, nil, api.FieldError("extra_vars", err.Error())
		}
		launchVars = p
	}
	trusted := ov != nil && ov.Trusted != nil
	if ov == nil {
		if err := refuseAnsibleVars(c, "extra_vars", launchVars); err != nil {
			return 0, nil, err
		}
	}
	surveyPasswords := map[string]any{}
	spec := templateSurvey(c, id)
	surveyVars := map[string]bool{}
	if boolv(wf["survey_enabled"]) && len(spec) > 0 {
		invID, _ := api.ID(inv)
		sv, pw, errs := ApplySurvey(c, spec, launchVars, SurveyContext{Template: wf, Inventory: invID})
		if len(errs) > 0 {
			return 0, nil, &api.Error{Status: 400, Body: Obj{"variables_needed_to_start": errs}}
		}
		for k, v := range sv {
			base[k] = v
		}
		for _, p := range pw {
			surveyPasswords[p] = "$encrypted$"
		}
		surveyVars = surveyVariables(spec)
	}
	for k, v := range launchVars {
		if surveyVars[k] {
			continue
		}
		if boolv(wf["ask_variables_on_launch"]) || trusted {
			base[k] = v
		} else {
			ev, _ := ignored["extra_vars"].(map[string]any)
			if ev == nil {
				ev = map[string]any{}
			}
			ev[k] = v
			ignored["extra_vars"] = ev
		}
	}
	if err := refuseUnprompted(c, wf, ignored); err != nil {
		return 0, nil, err
	}
	if err := checkPromptedLabelsAndGroups(c, accepted); err != nil {
		return 0, nil, err
	}
	pick := func(f string) any {
		if v, ok := accepted[f]; ok {
			return v
		}
		return wf[f]
	}
	values := map[string]any{
		"type":                    "workflow_job",
		"name":                    wf["name"],
		"description":             wf["description"],
		"unified_job_template_id": id,
		"organization_id":         wf["organization"],
		"launch_type":             launchType,
		"extra_vars":              vars.PyJSON(base),
		"inventory_id":            inv,
		"limit":                   str(pick("limit")),
		"scm_branch":              str(pick("scm_branch")),
		"job_tags":                str(pick("job_tags")),
		"skip_tags":               str(pick("skip_tags")),
		"allow_simultaneous":      wf["allow_simultaneous"],
		"survey_passwords":        surveyPasswords,
		"webhook_service":         wf["webhook_service"],
	}
	if ov != nil {
		if ov.LaunchType != "" {
			values["launch_type"] = ov.LaunchType
		}
		for k, v := range ov.ExtraJob {
			values[k] = v
		}
	}
	if !c.User.Anonymous() {
		values["created_by_id"] = c.User.UserID
		values["modified_by_id"] = c.User.UserID
	}
	var wfJobID int64
	err := c.WithTx(func(c *Ctx) error {
		var err error
		if wfJobID, err = jobs.Create(c, c.DB(), values); err != nil {
			return err
		}
		if err := copyTemplateNodes(c, id, wfJobID); err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, "INSERT INTO unified_job_labels (unified_job_id, label_id) SELECT $1, label_id FROM unified_job_template_labels WHERE unified_job_template_id = $2", wfJobID, id); err != nil {
			return err
		}
		if v, ok := accepted["labels"].([]any); ok {
			for _, l := range v {
				if lid, ok := api.ID(l); ok {
					c.DB().Exec(c, "INSERT INTO unified_job_labels (unified_job_id, label_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", wfJobID, lid)
				}
			}
		}
		jobs.Wake(c, c.DB())
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return wfJobID, ignored, nil
}

// copyTemplateNodes snapshots a workflow template's DAG into a workflow job.
func copyTemplateNodes(c *Ctx, wfjt, wfJob int64) error {
	_, err := c.DB().Exec(c, `
		WITH src AS (SELECT * FROM workflow_job_template_nodes WHERE workflow_job_template_id = $1),
		ins AS (
			INSERT INTO workflow_job_nodes (workflow_job_id, unified_job_template_id, identifier, all_parents_must_converge, extra_data,
				inventory_id, execution_environment_id, scm_branch, job_type, job_tags, skip_tags, "limit", diff_mode, verbosity, forks,
				job_slice_count, timeout, when_expr, loop_expr, loop_var, loop_concurrency)
			SELECT $2, unified_job_template_id, identifier, all_parents_must_converge, extra_data, inventory_id, execution_environment_id,
				scm_branch, job_type, job_tags, skip_tags, "limit", diff_mode, verbosity, forks, job_slice_count, timeout,
				when_expr, loop_expr, loop_var, loop_concurrency
			FROM src RETURNING id, identifier),
		m AS (SELECT src.id AS old_id, ins.id AS new_id FROM src JOIN ins ON ins.identifier = src.identifier),
		e AS (INSERT INTO workflow_job_node_edges (from_node_id, to_node_id, kind)
			SELECT mf.new_id, mt.new_id, x.kind FROM workflow_job_template_node_edges x
			JOIN m mf ON mf.old_id = x.from_node_id JOIN m mt ON mt.old_id = x.to_node_id),
		cr AS (INSERT INTO workflow_job_node_credentials (node_id, credential_id)
			SELECT m.new_id, x.credential_id FROM workflow_job_template_node_credentials x JOIN m ON m.old_id = x.node_id)
		INSERT INTO workflow_job_node_labels (node_id, label_id)
			SELECT m.new_id, x.label_id FROM workflow_job_template_node_labels x JOIN m ON m.old_id = x.node_id`, wfjt, wfJob)
	return err
}

// copyJobNodes snapshots the DAG of a previous workflow job (relaunch).
func copyJobNodes(c *Ctx, oldJob, newJob int64) error {
	// Identifiers are not unique on job nodes: tag them with the source id to map rows.
	_, err := c.DB().Exec(c, `
		WITH src AS (SELECT * FROM workflow_job_nodes WHERE workflow_job_id = $1),
		ins AS (
			INSERT INTO workflow_job_nodes (workflow_job_id, unified_job_template_id, identifier, all_parents_must_converge, extra_data,
				inventory_id, execution_environment_id, scm_branch, job_type, job_tags, skip_tags, "limit", diff_mode, verbosity, forks,
				job_slice_count, timeout, when_expr, loop_expr, loop_var, loop_concurrency)
			SELECT $2, unified_job_template_id, identifier || chr(31) || id, all_parents_must_converge, extra_data, inventory_id,
				execution_environment_id, scm_branch, job_type, job_tags, skip_tags, "limit", diff_mode, verbosity, forks, job_slice_count, timeout,
				when_expr, loop_expr, loop_var, loop_concurrency
			FROM src RETURNING id, identifier),
		m AS (SELECT src.id AS old_id, ins.id AS new_id FROM src JOIN ins ON ins.identifier = src.identifier || chr(31) || src.id),
		e AS (INSERT INTO workflow_job_node_edges (from_node_id, to_node_id, kind)
			SELECT mf.new_id, mt.new_id, x.kind FROM workflow_job_node_edges x
			JOIN m mf ON mf.old_id = x.from_node_id JOIN m mt ON mt.old_id = x.to_node_id),
		cr AS (INSERT INTO workflow_job_node_credentials (node_id, credential_id)
			SELECT m.new_id, x.credential_id FROM workflow_job_node_credentials x JOIN m ON m.old_id = x.node_id)
		INSERT INTO workflow_job_node_labels (node_id, label_id)
			SELECT m.new_id, x.label_id FROM workflow_job_node_labels x JOIN m ON m.old_id = x.node_id`, oldJob, newJob)
	if err != nil {
		return err
	}
	_, err = c.DB().Exec(c, "UPDATE workflow_job_nodes SET identifier = split_part(identifier, chr(31), 1) WHERE workflow_job_id = $1", newJob)
	return err
}

func workflowLaunchInfo(c *Ctx, wf Obj) Obj {
	id, _ := api.ID(wf["id"])
	spec := templateSurvey(c, id)
	var needed []string
	if boolv(wf["survey_enabled"]) {
		qs, _ := spec["spec"].([]any)
		for _, q := range qs {
			if qm, ok := q.(map[string]any); ok && boolv(qm["required"]) && (qm["default"] == nil || qm["default"] == "") {
				needed = append(needed, str(qm["variable"]))
			}
		}
	}
	var missing []int64
	rows, err := c.DB().Query(c, "SELECT id FROM workflow_job_template_nodes WHERE workflow_job_template_id = $1 AND unified_job_template_id IS NULL ORDER BY id", id)
	if err == nil {
		for rows.Next() {
			var n int64
			rows.Scan(&n)
			missing = append(missing, n)
		}
		rows.Close()
	}
	if missing == nil {
		missing = []int64{}
	}
	ev, _ := vars.Parse(str(wf["extra_vars"]))
	defaults := Obj{"extra_vars": maskSurveyPasswords(vars.PyJSON(ev)), "limit": wf["limit"], "scm_branch": wf["scm_branch"],
		"job_tags": wf["job_tags"], "skip_tags": wf["skip_tags"], "labels": []Obj{}}
	if inv, ok := api.ID(wf["inventory"]); ok {
		var name string
		c.DB().QueryRow(c, "SELECT name FROM inventories WHERE id = $1", inv).Scan(&name)
		defaults["inventory"] = Obj{"id": inv, "name": name}
	} else {
		defaults["inventory"] = Obj{"id": nil, "name": nil}
	}
	out := Obj{
		"can_start_without_user_input": len(needed) == 0 && !boolv(wf["ask_inventory_on_launch"]) && !boolv(wf["ask_variables_on_launch"]) &&
			!boolv(wf["ask_limit_on_launch"]) && !boolv(wf["ask_scm_branch_on_launch"]) && !boolv(wf["ask_labels_on_launch"]) &&
			!boolv(wf["ask_tags_on_launch"]) && !boolv(wf["ask_skip_tags_on_launch"]),
		"variables_needed_to_start":  nonNil(needed),
		"survey_enabled":             boolv(wf["survey_enabled"]),
		"node_templates_missing":     missing,
		"node_prompts_rejected":      []int64{},
		"workflow_job_template_data": Obj{"name": wf["name"], "id": id, "description": wf["description"]},
		"defaults":                   defaults,
	}
	for _, flag := range workflowPrompts {
		out[flag] = boolv(wf[flag])
	}
	out["ask_variables_on_launch"] = boolv(wf["ask_variables_on_launch"])
	return out
}

func handleWorkflowLaunch(c *Ctx, w http.ResponseWriter, r *http.Request, wf Obj) {
	if r.Method == "GET" {
		api.WriteJSON(w, 200, workflowLaunchInfo(c, wf))
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	id, ignored, err := LaunchWorkflowJobTemplate(c, wf, normalizeObj(body), "manual", nil)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	writeLaunched(c, w, id, Obj{"ignored_fields": ignored})
}

// writeLaunched responds with the created job (a job or a workflow job for sliced templates).
func writeLaunched(c *Ctx, w http.ResponseWriter, id int64, extra Obj) {
	var typ string
	c.DB().QueryRow(c, "SELECT type FROM unified_jobs WHERE id = $1", id).Scan(&typ)
	res := api.LookupType(typ)
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
	out[typ] = id
	for k, v := range extra {
		out[k] = v
	}
	api.WriteJSON(w, 201, out)
}

func handleWorkflowRelaunch(c *Ctx, w http.ResponseWriter, r *http.Request, job Obj) {
	jid, _ := api.ID(job["id"])
	if r.Method == "GET" {
		// Only a sliced job replays a template that may ask for passwords.
		needed := []string{}
		if boolv(job["is_sliced_job"]) {
			var first int64
			c.DB().QueryRow(c, "SELECT job_id FROM workflow_job_nodes WHERE workflow_job_id = $1 AND job_id IS NOT NULL ORDER BY id LIMIT 1", jid).Scan(&first)
			needed = nonNil(passwordsNeeded(c, jobCredentialIDs(c, first)))
		}
		api.WriteJSON(w, 200, Obj{"passwords_needed_to_start": needed})
		return
	}
	ujt, ok := api.ID(job["unified_job_template"])
	if !ok {
		api.WriteError(w, api.BadRequest("Cannot relaunch workflow job: its template has been deleted."))
		return
	}
	if ok, err := ujtPerm(c, ujt, "execute"); err != nil || !ok {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	if boolv(job["is_sliced_job"]) {
		jt, err := api.Get(c, api.Lookup("job_templates"), ujt)
		if err != nil {
			api.WriteError(w, api.BadRequest("Job template for this sliced job has been deleted."))
			return
		}
		// Slices share one launch configuration: relaunch the first slice's settings.
		var first int64
		c.DB().QueryRow(c, "SELECT job_id FROM workflow_job_nodes WHERE workflow_job_id = $1 AND job_id IS NOT NULL ORDER BY id LIMIT 1", jid).Scan(&first)
		slice, err := api.Get(c, api.Lookup("jobs"), first)
		if err != nil {
			api.WriteError(w, api.BadRequest("Slices of this job have been deleted."))
			return
		}
		ev, _ := vars.Parse(str(slice["extra_vars"]))
		trusted := Obj{"inventory": slice["inventory"], "limit": slice["limit"], "job_tags": slice["job_tags"], "skip_tags": slice["skip_tags"],
			"job_type": slice["job_type"], "verbosity": slice["verbosity"], "diff_mode": slice["diff_mode"], "scm_branch": slice["scm_branch"],
			"forks": slice["forks"], "timeout": slice["timeout"], "execution_environment": slice["execution_environment"], "job_slice_count": slice["job_slice_count"]}
		jtNoSurvey := Obj{}
		for k, v := range jt {
			jtNoSurvey[k] = v
		}
		jtNoSurvey["survey_enabled"], jtNoSurvey["extra_vars"] = false, ""
		body, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		launchBody := passwordFields(normalizeObj(body))
		launchBody["extra_vars"] = ev
		newID, _, err := LaunchJobTemplate(c, jtNoSurvey, launchBody, "relaunch", &LaunchOverrides{Trusted: trusted, Credentials: jobCredentialIDs(c, first)})
		if err != nil {
			api.WriteError(w, err)
			return
		}
		writeLaunched(c, w, newID, nil)
		return
	}
	values := map[string]any{}
	for _, k := range []string{"name", "description", "extra_vars", "limit", "scm_branch", "job_tags", "skip_tags", "allow_simultaneous", "webhook_service"} {
		values[k] = job[k]
	}
	values["type"], values["unified_job_template_id"], values["organization_id"] = "workflow_job", ujt, job["organization"]
	values["inventory_id"], values["launch_type"] = job["inventory"], "relaunch"
	if !c.User.Anonymous() {
		values["created_by_id"], values["modified_by_id"] = c.User.UserID, c.User.UserID
	}
	var newID int64
	err := c.WithTx(func(c *Ctx) error {
		var err error
		if newID, err = jobs.Create(c, c.DB(), values); err != nil {
			return err
		}
		if err := copyJobNodes(c, jid, newID); err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, "INSERT INTO unified_job_labels (unified_job_id, label_id) SELECT $1, label_id FROM unified_job_labels WHERE unified_job_id = $2", newID, jid); err != nil {
			return err
		}
		jobs.Wake(c, c.DB())
		return nil
	})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	writeLaunched(c, w, newID, nil)
}

func jobCredentialIDs(c *Ctx, job int64) []int64 {
	rows, err := c.DB().Query(c, "SELECT credential_id FROM unified_job_credentials WHERE unified_job_id = $1 ORDER BY credential_id", job)
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

// sliceCount returns the effective number of slices for a launch.
func sliceCount(c *Ctx, requested any, inv any) int {
	n, _ := api.ID(requested)
	if n <= 1 {
		return 1
	}
	var hosts int64
	c.DB().QueryRow(c, `SELECT (SELECT count(*) FROM hosts WHERE inventory_id = $1) + (SELECT count(*) FROM smart_inventory_hosts($1))`, inv).Scan(&hosts)
	if hosts < n {
		n = hosts
	}
	if n < 1 {
		n = 1
	}
	return int(n)
}

// createSlices creates a sliced workflow job running one job per slice in parallel.
func createSlices(c *Ctx, jt Obj, values map[string]any, n int, createJob func(c *Ctx, v map[string]any) (int64, error)) (int64, error) {
	jtID, _ := api.ID(jt["id"])
	wf := map[string]any{
		"type": "workflow_job", "name": values["name"], "description": values["description"], "unified_job_template_id": jtID,
		"job_template_id": jtID, "is_sliced_job": true, "organization_id": values["organization_id"], "launch_type": values["launch_type"],
		"extra_vars": values["extra_vars"], "inventory_id": values["inventory_id"], "limit": values["limit"], "allow_simultaneous": values["allow_simultaneous"],
		"schedule_id": values["schedule_id"], "launched_by": values["launched_by"],
	}
	for _, k := range []string{"created_by_id", "modified_by_id", "webhook_service", "webhook_credential_id", "webhook_guid"} {
		if v, ok := values[k]; ok {
			wf[k] = v
		}
	}
	for k, v := range wf {
		if v == nil {
			delete(wf, k)
		}
	}
	wfID, err := jobs.Create(c, c.DB(), wf)
	if err != nil {
		return 0, err
	}
	for i := 1; i <= n; i++ {
		v := map[string]any{}
		for k, val := range values {
			v[k] = val
		}
		v["job_slice_number"], v["job_slice_count"], v["allow_simultaneous"], v["launch_type"] = i, n, true, "workflow"
		jid, err := createJob(c, v)
		if err != nil {
			return 0, err
		}
		aa, _ := json.Marshal(map[string]any{"job_slice": i})
		if _, err := c.DB().Exec(c, `INSERT INTO workflow_job_nodes (workflow_job_id, unified_job_template_id, job_id, identifier, ancestor_artifacts)
			VALUES ($1, $2, $3, $4, $5)`, wfID, jtID, jid, fmt.Sprintf("slice-%d", i), string(aa)); err != nil {
			return 0, err
		}
	}
	return wfID, nil
}
