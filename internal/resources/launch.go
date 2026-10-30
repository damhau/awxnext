package resources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/vars"
)

var encryptedValueRe = regexp.MustCompile(`"\$encrypted\$v1\$[^"]*"`)

// StartArgs are launch-time inputs kept encrypted on the job until it runs.
type StartArgs struct {
	CredentialPasswords map[string]string `json:"credential_passwords,omitempty"`
	// LaunchVars are extra var names supplied at launch (prompt or survey).
	LaunchVars []string `json:"launch_vars,omitempty"`
}

// promptable maps launch fields to the template flag that allows them.
var promptable = map[string]string{
	"inventory": "ask_inventory_on_launch", "credentials": "ask_credential_on_launch", "limit": "ask_limit_on_launch",
	"job_tags": "ask_tags_on_launch", "skip_tags": "ask_skip_tags_on_launch", "job_type": "ask_job_type_on_launch",
	"verbosity": "ask_verbosity_on_launch", "diff_mode": "ask_diff_mode_on_launch", "scm_branch": "ask_scm_branch_on_launch",
	"execution_environment": "ask_execution_environment_on_launch", "labels": "ask_labels_on_launch", "forks": "ask_forks_on_launch",
	"job_slice_count": "ask_job_slice_count_on_launch", "timeout": "ask_timeout_on_launch", "instance_groups": "ask_instance_groups_on_launch",
}

var passwordKeys = []string{"ssh_password", "ssh_key_unlock", "become_password"}

// passwordsNeeded lists credential_passwords keys required by ASK inputs.
func passwordsNeeded(c *Ctx, creds []int64) []string {
	var out []string
	for _, id := range creds {
		var inputs map[string]any
		var kind string
		c.DB().QueryRow(c, `SELECT cr.inputs, ct.kind FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1`, id).Scan(&inputs, &kind)
		out = append(out, askedPasswords(kind, inputs)...)
	}
	sort.Strings(out)
	return out
}

// askedPasswords lists the credential_passwords keys a credential asks for at launch.
func askedPasswords(kind string, inputs map[string]any) []string {
	var out []string
	switch kind {
	case "ssh":
		for _, f := range []struct{ field, key string }{{"password", "ssh_password"}, {"ssh_key_unlock", "ssh_key_unlock"}, {"become_password", "become_password"}} {
			if inputs[f.field] == "ASK" {
				out = append(out, f.key)
			}
		}
	case "vault":
		if inputs["vault_password"] == "ASK" {
			if id := str(inputs["vault_id"]); id != "" {
				out = append(out, "vault_password."+id)
			} else {
				out = append(out, "vault_password")
			}
		}
	}
	return out
}

// passwordFields keeps the credential passwords of a request body.
func passwordFields(body Obj) Obj {
	out := Obj{}
	for k, v := range body {
		if k == "credential_passwords" || contains(passwordKeys, k) || strings.HasPrefix(k, "vault_password") {
			out[k] = v
		}
	}
	return out
}

// resolveEE picks the execution environment like AWX: prompt, template, project default, organization default, global default.
func resolveEE(c *Ctx, prompt any, tpl Obj) any {
	if id, ok := api.ID(prompt); ok {
		return id
	}
	if id, ok := api.ID(tpl["execution_environment"]); ok {
		return id
	}
	var id *int64
	if pid, ok := api.ID(tpl["project"]); ok {
		c.DB().QueryRow(c, "SELECT default_environment_id FROM unified_job_templates WHERE id = $1", pid).Scan(&id)
		if id != nil {
			return *id
		}
	}
	if org, ok := api.ID(tpl["organization"]); ok {
		c.DB().QueryRow(c, "SELECT default_environment_id FROM organizations WHERE id = $1", org).Scan(&id)
		if id != nil {
			return *id
		}
	}
	if gid, ok := api.ID(c.App.Settings.Get(c, "DEFAULT_EXECUTION_ENVIRONMENT")); ok {
		return gid
	}
	var def int64
	if err := c.DB().QueryRow(c, "SELECT id FROM execution_environments WHERE managed ORDER BY id LIMIT 1").Scan(&def); err == nil {
		return def
	}
	return nil
}

func templateSurvey(c *Ctx, id int64) map[string]any {
	var spec map[string]any
	c.DB().QueryRow(c, "SELECT survey_spec FROM unified_job_templates WHERE id = $1", id).Scan(&spec)
	return spec
}

func handleLaunch(c *Ctx, w http.ResponseWriter, r *http.Request, jt Obj) {
	id, _ := api.ID(jt["id"])
	if r.Method == "GET" {
		api.WriteJSON(w, 200, launchInfo(c, jt))
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	jobID, ignored, err := LaunchJobTemplate(c, jt, normalizeObj(body), "manual", nil)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	_ = id
	writeLaunched(c, w, jobID, Obj{"ignored_fields": ignored})
}

// checkPromptedLabelsAndGroups requires the user to see each label and to be
// allowed to use each instance group a launch asks for.
func checkPromptedLabelsAndGroups(c *Ctx, accepted Obj) error {
	if v, ok := accepted["labels"].([]any); ok {
		for _, l := range v {
			if err := canUseLabel(c, l); err != nil {
				return err
			}
		}
	}
	if v, ok := accepted["instance_groups"].([]any); ok {
		for _, g := range v {
			gid, ok := api.ID(g)
			if !ok {
				return api.FieldError("instance_groups", "Invalid instance group.")
			}
			if err := api.RequirePermID(c, "instance_groups", "instance_groups", gid, "use"); err != nil {
				return err
			}
		}
	}
	return nil
}

// canUseLabel requires the label to be visible to the user.
func canUseLabel(c *Ctx, v any) error {
	lid, ok := api.ID(v)
	if !ok {
		return api.FieldError("labels", "Invalid label.")
	}
	return allowed(api.CanView(c, api.Lookup("labels"), lid))
}

// validForks enforces MAX_FORKS (0: no limit) wherever forks are saved or
// used to launch.
func validForks(c *Ctx, v any, _ Obj) error {
	n, _ := api.ID(v)
	if limit := int64(c.App.Settings.Int(c, "MAX_FORKS")); limit > 0 && n > limit {
		return fmt.Errorf("Maximum number of forks (%d) exceeded.", limit)
	}
	return nil
}

// refuseAnsibleVars fails launch-time variables named ansible_*. Ansible reads
// them as connection and execution settings, and extra variables override
// every other variable, so they would let whoever launches choose a command
// run where the job connects from (ansible_ssh_common_args) or the host the
// job's credentials go to (ansible_host). Variables on templates, inventories
// and hosts are set by those who may edit them and are not checked here.
// ALLOW_ANSIBLE_VARS_AT_LAUNCH restores AWX's behaviour.
func refuseAnsibleVars(c *Ctx, field string, vars map[string]any) error {
	var names []string
	for k := range vars {
		if isAnsibleVar(k) {
			names = append(names, k)
		}
	}
	if len(names) == 0 || c.App.Settings.Bool(c, "ALLOW_ANSIBLE_VARS_AT_LAUNCH") {
		return nil
	}
	sort.Strings(names)
	return api.FieldError(field, fmt.Sprintf("Variables %s cannot be set at launch: ansible_* variables control how Ansible connects and runs. Set them on the template, inventory or host instead.", strings.Join(names, ", ")))
}

func isAnsibleVar(name string) bool { return strings.HasPrefix(strings.ToLower(name), "ansible_") }

// refuseUnprompted fails a launch that sends fields the template does not
// prompt for. AWX ignores them and starts the job anyway, so a limit meant for
// one host runs on the whole inventory; IGNORE_UNPROMPTED_LAUNCH_FIELDS brings
// that behaviour back. Empty values and the template's own values change
// nothing and are let through (still listed in ignored_fields): the Terraform
// provider sends a job's values again when it updates the job.
func refuseUnprompted(c *Ctx, tpl Obj, ignored Obj) error {
	if len(ignored) == 0 || c.App.Settings.Bool(c, "IGNORE_UNPROMPTED_LAUNCH_FIELDS") {
		return nil
	}
	fe := api.FieldErrors{}
	for field, v := range ignored {
		if field != "extra_vars" && sameAsTemplate(c, tpl, field, v) {
			continue
		}
		switch x := v.(type) {
		case nil:
			continue
		case string:
			if x == "" {
				continue
			}
		case []any:
			if len(x) == 0 {
				continue
			}
		case map[string]any:
			if len(x) == 0 {
				continue
			}
			if field == "extra_vars" {
				own, _ := vars.Parse(str(tpl["extra_vars"]))
				names := make([]string, 0, len(x))
				for k, val := range x {
					if prev, ok := own[k]; !ok || vars.MustJSON(prev) != vars.MustJSON(val) {
						names = append(names, k)
					}
				}
				if len(names) == 0 {
					continue
				}
				sort.Strings(names)
				fe.Add(field, fmt.Sprintf("Variables %s are not allowed on launch. Check the Prompt on launch setting on the template to include Extra Variables.", strings.Join(names, ", ")))
				continue
			}
		}
		fe.Add(field, "Field is not configured to prompt on launch.")
	}
	return fe.Err()
}

// sameAsTemplate reports whether an unprompted launch field only repeats the
// template's own value.
func sameAsTemplate(c *Ctx, tpl Obj, field string, v any) bool {
	id, _ := api.ID(tpl["id"])
	switch field {
	case "credentials":
		return sameIDs(v, templateCredentials(c, id))
	case "labels":
		return sameIDs(v, templateLabels(c, id))
	case "instance_groups":
		var have []int64
		rows, err := c.DB().Query(c, "SELECT instance_group_id FROM instance_group_links WHERE content_type = $1 AND object_id = $2",
			strings.ReplaceAll(str(tpl["type"]), "_", ""), id)
		if err != nil {
			return false
		}
		for rows.Next() {
			var ig int64
			rows.Scan(&ig)
			have = append(have, ig)
		}
		rows.Close()
		return sameIDs(v, have)
	case "inventory", "execution_environment":
		a, aok := api.ID(v)
		b, bok := api.ID(tpl[field])
		return aok && bok && a == b
	}
	return vars.MustJSON(v) == vars.MustJSON(tpl[field])
}

// sameIDs compares a launch list (ids or objects with an id) with ids, as sets.
func sameIDs(v any, have []int64) bool {
	list, ok := v.([]any)
	if !ok || len(list) != len(have) {
		return false
	}
	set := map[int64]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			x = m["id"]
		}
		if id, ok := api.ID(x); !ok || !set[id] {
			return false
		}
	}
	return true
}

// normalizeObj turns the json.Number values of a launch body into int64 for
// integers and float64 otherwise, as Python reads JSON: a job's extra_vars
// are written back as AWX writes them, 1 and not 1.0.
func normalizeObj(o Obj) Obj {
	m, _ := plainNumbers(o).(map[string]any)
	if m == nil {
		return Obj{}
	}
	return m
}

func plainNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = plainNumbers(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plainNumbers(e)
		}
		return out
	}
	return v
}

func launchInfo(c *Ctx, jt Obj) Obj {
	id, _ := api.ID(jt["id"])
	creds := templateCredentials(c, id)
	needed := passwordsNeeded(c, creds)
	spec := templateSurvey(c, id)
	var varsNeeded []string
	if boolv(jt["survey_enabled"]) {
		qs, _ := spec["spec"].([]any)
		for _, q := range qs {
			if qm, ok := q.(map[string]any); ok && qm["required"] == true && (qm["default"] == nil || qm["default"] == "") {
				varsNeeded = append(varsNeeded, str(qm["variable"]))
			}
		}
	}
	_, hasInv := api.ID(jt["inventory"])
	credObjs := []Obj{}
	for _, cid := range creds {
		var name string
		var typeID int64
		c.DB().QueryRow(c, "SELECT name, credential_type_id FROM credentials WHERE id = $1", cid).Scan(&name, &typeID)
		credObjs = append(credObjs, Obj{"id": cid, "name": name, "credential_type": typeID, "passwords_needed": passwordsNeeded(c, []int64{cid})})
	}
	defaults := Obj{
		"extra_vars": jt["extra_vars"], "diff_mode": jt["diff_mode"], "limit": jt["limit"], "job_tags": jt["job_tags"],
		"skip_tags": jt["skip_tags"], "job_type": jt["job_type"], "verbosity": jt["verbosity"], "scm_branch": jt["scm_branch"],
		"forks": jt["forks"], "job_slice_count": jt["job_slice_count"], "timeout": jt["timeout"], "credentials": credObjs,
		"labels": []Obj{}, "instance_groups": []Obj{},
	}
	if inv, ok := api.ID(jt["inventory"]); ok {
		var name string
		c.DB().QueryRow(c, "SELECT name FROM inventories WHERE id = $1", inv).Scan(&name)
		defaults["inventory"] = Obj{"id": inv, "name": name}
	} else {
		defaults["inventory"] = Obj{"id": nil, "name": nil}
	}
	if ee, ok := api.ID(jt["execution_environment"]); ok {
		var name string
		c.DB().QueryRow(c, "SELECT name FROM execution_environments WHERE id = $1", ee).Scan(&name)
		defaults["execution_environment"] = Obj{"id": ee, "name": name}
	}
	info := Obj{
		"can_start_without_user_input": len(needed) == 0 && len(varsNeeded) == 0 && hasInv,
		"passwords_needed_to_start":    nonNil(needed),
		"variables_needed_to_start":    nonNil(varsNeeded),
		"credential_needed_to_start":   false,
		"inventory_needed_to_start":    !hasInv,
		"survey_enabled":               jt["survey_enabled"],
		"job_template_data":            Obj{"name": jt["name"], "id": id, "description": jt["description"]},
		"defaults":                     defaults,
	}
	for _, f := range askFields {
		info[f] = jt[f]
	}
	return info
}

// LaunchOverrides lets callers (schedules, workflows, relaunch) bypass prompt checks.
type LaunchOverrides struct {
	LaunchType string
	// Trusted prompts are applied regardless of ask_*_on_launch (saved prompts of schedules/workflow nodes).
	Trusted Obj
	// ExtraJob sets columns on the created job (e.g. schedule_id, workflow references).
	ExtraJob map[string]any
	// Credentials replaces the credential set entirely (relaunch).
	Credentials []int64
}

// LaunchJobTemplate validates launch input and creates a pending job.
func LaunchJobTemplate(c *Ctx, jt Obj, body Obj, launchType string, ov *LaunchOverrides) (int64, Obj, error) {
	id, _ := api.ID(jt["id"])
	ignored := Obj{}
	accepted := Obj{}
	for field, flag := range promptable {
		v, ok := body[field]
		if !ok {
			continue
		}
		if boolv(jt[flag]) {
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
	fe := api.FieldErrors{}

	// Inventory.
	inv := jt["inventory"]
	if v, ok := accepted["inventory"]; ok {
		invID, ok := api.ID(v)
		own, _ := api.ID(jt["inventory"])
		if !ok {
			fe.Add("inventory", "Invalid inventory.")
		} else if invID != own {
			// Executing the template allows its own inventory (a relaunch passes it back).
			if err := api.RequirePermID(c, "inventory", "inventories", invID, "use"); err != nil {
				return 0, nil, err
			}
			inv = invID
		}
	}
	if invID, ok := api.ID(inv); !ok {
		fe.Add("inventory", "Job Template 'inventory' is missing or undefined.")
	} else {
		var pending bool
		c.DB().QueryRow(c, "SELECT pending_deletion FROM inventories WHERE id = $1", invID).Scan(&pending)
		if pending {
			fe.Add("inventory", "The inventory associated with this Job Template is being deleted.")
		}
	}

	// Credentials: launch credentials replace template credentials of the same slot.
	creds := templateCredentials(c, id)
	if v, ok := accepted["credentials"]; ok {
		list, _ := v.([]any)
		var launch []int64
		for _, x := range list {
			cid, ok := api.ID(x)
			if !ok {
				fe.Add("credentials", "Invalid credential.")
				continue
			}
			if err := api.RequirePermID(c, "credentials", "credentials", cid, "use"); err != nil {
				return 0, nil, err
			}
			launch = append(launch, cid)
		}
		launchSlots := map[string]bool{}
		for _, cid := range launch {
			s, _, err := credentialSlot(c, cid)
			if err != nil {
				return 0, nil, err
			}
			if launchSlots[s] {
				fe.Add("credentials", "Cannot assign multiple credentials of the same credential type.")
			}
			launchSlots[s] = true
		}
		for _, cid := range creds {
			s, _, _ := credentialSlot(c, cid)
			if !launchSlots[s] {
				var name string
				c.DB().QueryRow(c, "SELECT name FROM credentials WHERE id = $1", cid).Scan(&name)
				fe.Add("credentials", fmt.Sprintf("Removing %s credential at launch time without replacement is not supported. Provided list lacked credential(s): %s.", name, name))
			}
		}
		// As AWX, only credentials given at launch are refused by kind: those
		// saved on a node or schedule were checked when attached and go to
		// the job as they are (the runner skips kinds a job cannot use).
		if ov == nil || ov.Trusted["credentials"] == nil {
			for _, cid := range launch {
				if err := checkTemplateCredential(c, nil, cid); err != nil {
					fe.Add("credentials", fmt.Sprint(err.(*api.Error).Body.(Obj)["detail"]))
				}
			}
		}
		creds = launch
	}
	if ov != nil && ov.Credentials != nil {
		creds = ov.Credentials
	}

	// Extra vars and survey.
	base, _ := vars.Parse(str(jt["extra_vars"]))
	launchVars := map[string]any{}
	if v, ok := body["extra_vars"]; ok {
		switch x := v.(type) {
		case map[string]any:
			launchVars = x
		case string:
			p, err := vars.Parse(x)
			if err != nil {
				fe.Add("extra_vars", err.Error())
			}
			launchVars = p
		}
	}
	if ov == nil {
		if err := refuseAnsibleVars(c, "extra_vars", launchVars); err != nil {
			return 0, nil, err
		}
	}
	var launchVarNames []string
	surveyPasswords := map[string]any{}
	spec := templateSurvey(c, id)
	if boolv(jt["survey_enabled"]) && len(spec) > 0 {
		invID, _ := api.ID(inv)
		sv, pw, errs := ApplySurvey(c, spec, launchVars, SurveyContext{Template: jt, Inventory: invID})
		if len(errs) > 0 {
			return 0, nil, &api.Error{Status: 400, Body: Obj{"variables_needed_to_start": errs}}
		}
		for k, v := range sv {
			base[k] = v
			launchVarNames = append(launchVarNames, k)
		}
		for _, p := range pw {
			surveyPasswords[p] = "$encrypted$"
		}
		surveyVars := surveyVariables(spec)
		for k, v := range launchVars {
			if surveyVars[k] {
				continue
			}
			if boolv(jt["ask_variables_on_launch"]) || (ov != nil && ov.Trusted != nil) {
				base[k] = v
				launchVarNames = append(launchVarNames, k)
			} else {
				ev, _ := ignored["extra_vars"].(map[string]any)
				if ev == nil {
					ev = map[string]any{}
				}
				ev[k] = v
				ignored["extra_vars"] = ev
			}
		}
	} else if len(launchVars) > 0 {
		if boolv(jt["ask_variables_on_launch"]) || (ov != nil && ov.Trusted != nil) {
			for k, v := range launchVars {
				base[k] = v
				launchVarNames = append(launchVarNames, k)
			}
		} else {
			ignored["extra_vars"] = launchVars
		}
	}

	if err := refuseUnprompted(c, jt, ignored); err != nil {
		return 0, nil, err
	}
	if err := checkPromptedLabelsAndGroups(c, accepted); err != nil {
		return 0, nil, err
	}

	// Branch override.
	branch := jt["scm_branch"]
	if v, ok := accepted["scm_branch"]; ok {
		branch = v
	}

	// Passwords for ASK credential inputs.
	cp := map[string]string{}
	if m, ok := body["credential_passwords"].(map[string]any); ok {
		for k, v := range m {
			cp[k] = fmt.Sprint(v)
		}
	}
	for k, v := range body {
		if contains(passwordKeys, k) || strings.HasPrefix(k, "vault_password") {
			cp[k] = fmt.Sprint(v)
		}
	}
	var missing []string
	for _, k := range passwordsNeeded(c, creds) {
		if cp[k] == "" && !(strings.HasPrefix(k, "vault_password.") && cp["vault_password"] != "") {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return 0, nil, &api.Error{Status: 400, Body: Obj{"passwords_needed_to_start": missing}}
	}
	if err := fe.Err(); err != nil {
		return 0, nil, err
	}

	pick := func(field string) any {
		if v, ok := accepted[field]; ok {
			return v
		}
		return jt[field]
	}
	// MAX_FORKS also stops templates saved before it was lowered.
	if err := validForks(c, pick("forks"), nil); err != nil {
		return 0, nil, api.FieldError("forks", err.Error())
	}
	ee := resolveEE(c, accepted["execution_environment"], jt)
	if v, ok := accepted["execution_environment"]; ok {
		if eid, ok := api.ID(v); ok {
			if visible, _ := api.CanView(c, api.Lookup("execution_environments"), eid); !visible {
				return 0, nil, api.ErrForbidden
			}
		}
	}
	sa, _ := json.Marshal(StartArgs{CredentialPasswords: cp, LaunchVars: launchVarNames})
	encSA, err := c.App.Crypto.Encrypt(string(sa))
	if err != nil {
		return 0, nil, err
	}
	values := map[string]any{
		"type":                     "job",
		"name":                     jt["name"],
		"description":              jt["description"],
		"unified_job_template_id":  id,
		"organization_id":          jt["organization"],
		"launch_type":              launchType,
		"job_type":                 pick("job_type"),
		"inventory_id":             inv,
		"project_id":               jt["project"],
		"playbook":                 jt["playbook"],
		"scm_branch":               branch,
		"forks":                    pick("forks"),
		"limit":                    pick("limit"),
		"verbosity":                pick("verbosity"),
		"extra_vars":               vars.PyJSON(base),
		"job_tags":                 pick("job_tags"),
		"skip_tags":                pick("skip_tags"),
		"start_at_task":            jt["start_at_task"],
		"timeout":                  pick("timeout"),
		"use_fact_cache":           jt["use_fact_cache"],
		"force_handlers":           jt["force_handlers"],
		"become_enabled":           jt["become_enabled"],
		"diff_mode":                pick("diff_mode"),
		"allow_simultaneous":       jt["allow_simultaneous"],
		"job_slice_count":          pick("job_slice_count"),
		"execution_environment_id": ee,
		"survey_passwords":         surveyPasswords,
		"start_args":               encSA,
		"webhook_service":          jt["webhook_service"],
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
	createJob := func(c *Ctx, values map[string]any) (int64, error) {
		jobID, err := jobs.Create(c, c.DB(), values)
		if err != nil {
			return 0, err
		}
		for _, cid := range creds {
			if _, err := c.DB().Exec(c, "INSERT INTO unified_job_credentials (unified_job_id, credential_id) VALUES ($1, $2)", jobID, cid); err != nil {
				return 0, err
			}
		}
		if _, err := c.DB().Exec(c, "INSERT INTO unified_job_labels (unified_job_id, label_id) SELECT $1, label_id FROM unified_job_template_labels WHERE unified_job_template_id = $2", jobID, id); err != nil {
			return 0, err
		}
		if v, ok := accepted["labels"].([]any); ok {
			for _, l := range v {
				if lid, ok := api.ID(l); ok {
					c.DB().Exec(c, "INSERT INTO unified_job_labels (unified_job_id, label_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", jobID, lid)
				}
			}
		}
		if v, ok := accepted["instance_groups"].([]any); ok {
			for i, g := range v {
				if gid, ok := api.ID(g); ok {
					c.DB().Exec(c, "INSERT INTO instance_group_links (content_type, object_id, instance_group_id, position) VALUES ('unifiedjob', $1, $2, $3) ON CONFLICT DO NOTHING", jobID, gid, i)
				}
			}
		}
		return jobID, jobLaunched(c, "jobs", jobID)
	}
	// A sliced template launches a workflow job running one job per slice of the inventory.
	slices := 1
	if ov == nil || ov.ExtraJob["job_slice_number"] == nil {
		slices = sliceCount(c, values["job_slice_count"], inv)
	}
	var jobID int64
	err = c.WithTx(func(c *Ctx) error {
		var err error
		if slices > 1 {
			jobID, err = createSlices(c, jt, values, slices, createJob)
		} else {
			if n, _ := api.ID(values["job_slice_number"]); n == 0 {
				values["job_slice_count"] = 1
			}
			jobID, err = createJob(c, values)
		}
		if err != nil {
			return err
		}
		jobs.Wake(c, c.DB())
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return jobID, ignored, nil
}

// addLaunchChoices adds to a relaunch's prompts the labels and instance
// groups its original job was launched with: the job's labels the template
// does not have (now), and the instance groups chosen at launch, in order.
// As prompts, they are checked like a launch's (see checkPromptedLabelsAndGroups).
func addLaunchChoices(c *Ctx, trusted Obj, job, template int64) {
	ids := func(sql string, args ...any) []any {
		rows, err := c.DB().Query(c, sql, args...)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var out []any
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				out = append(out, id)
			}
		}
		return out
	}
	if l := ids(`SELECT label_id FROM unified_job_labels WHERE unified_job_id = $1 AND label_id NOT IN
		(SELECT label_id FROM unified_job_template_labels WHERE unified_job_template_id = $2) ORDER BY label_id`, job, template); len(l) > 0 {
		trusted["labels"] = l
	}
	if g := ids("SELECT instance_group_id FROM instance_group_links WHERE content_type = 'unifiedjob' AND object_id = $1 ORDER BY position", job); len(g) > 0 {
		trusted["instance_groups"] = g
	}
}

// handleRelaunch implements /jobs/N/relaunch/.
func handleRelaunch(c *Ctx, w http.ResponseWriter, r *http.Request, job Obj) {
	jid, _ := api.ID(job["id"])
	var creds []int64
	rows, err := c.DB().Query(c, "SELECT credential_id FROM unified_job_credentials WHERE unified_job_id = $1", jid)
	if err == nil {
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			creds = append(creds, id)
		}
		rows.Close()
	}
	var failedHosts []string
	rows, err = c.DB().Query(c, "SELECT host_name FROM job_host_summaries WHERE job_id = $1 AND failed ORDER BY host_name", jid)
	if err == nil {
		for rows.Next() {
			var h string
			rows.Scan(&h)
			failedHosts = append(failedHosts, h)
		}
		rows.Close()
	}
	total := countQuery(c, "SELECT count(*) FROM job_host_summaries WHERE job_id = $1", jid)
	if r.Method == "GET" {
		api.WriteJSON(w, 200, Obj{"passwords_needed_to_start": nonNil(passwordsNeeded(c, creds)),
			"retry_counts": Obj{"all": total, "failed": len(failedHosts)}})
		return
	}
	jtID, ok := api.ID(job["job_template"])
	if !ok {
		api.WriteError(w, api.BadRequest("Job Template for this job has been deleted."))
		return
	}
	jt, err := api.Get(c, api.Lookup("job_templates"), jtID)
	if err != nil {
		api.WriteError(w, api.BadRequest("Job Template for this job has been deleted."))
		return
	}
	can, err := api.HasPerm(c, api.Lookup("job_templates"), jt, "execute")
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if !can {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body = normalizeObj(body)
	limit := job["limit"]
	if str(body["hosts"]) == "failed" {
		if len(failedHosts) == 0 {
			api.WriteError(w, api.FieldError("hosts", "No failed hosts to relaunch on."))
			return
		}
		limit = strings.Join(failedHosts, ",")
	}
	// Relaunch reproduces the original job's settings.
	ev, _ := vars.Parse(str(job["extra_vars"]))
	trusted := Obj{"inventory": job["inventory"], "limit": limit, "job_tags": job["job_tags"], "skip_tags": job["skip_tags"],
		"job_type": job["job_type"], "verbosity": job["verbosity"], "diff_mode": job["diff_mode"], "scm_branch": job["scm_branch"],
		"forks": job["forks"], "timeout": job["timeout"], "execution_environment": job["execution_environment"]}
	addLaunchChoices(c, trusted, jid, jtID)
	// The original launch's variables are replayed, so those it got at launch
	// are checked again; template variables are the template's own.
	tplVars, _ := vars.Parse(str(jt["extra_vars"]))
	launched := map[string]any{}
	for k, v := range ev {
		if _, own := tplVars[k]; !own {
			launched[k] = v
		}
	}
	if err := refuseAnsibleVars(c, "extra_vars", launched); err != nil {
		api.WriteError(w, err)
		return
	}
	// The body only brings passwords: everything else is the original job's.
	launchBody := passwordFields(body)
	launchBody["extra_vars"] = ev
	jtNoSurvey := Obj{}
	for k, v := range jt {
		jtNoSurvey[k] = v
	}
	jtNoSurvey["survey_enabled"] = false
	jtNoSurvey["extra_vars"] = ""
	newID, _, err := LaunchJobTemplate(c, jtNoSurvey, launchBody, "relaunch", &LaunchOverrides{Trusted: trusted, Credentials: creds,
		ExtraJob: map[string]any{"job_slice_number": job["job_slice_number"], "job_slice_count": job["job_slice_count"]}})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	writeLaunched(c, w, newID, nil)
}
