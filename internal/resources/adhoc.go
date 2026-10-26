package resources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/vars"
)

// adHocPasswords collects credential passwords supplied with an ad hoc command request.
func adHocPasswords(c *Ctx, cred any) (map[string]string, error) {
	body, _ := c.Extra["body"].(Obj)
	cp := map[string]string{}
	if m, ok := body["credential_passwords"].(map[string]any); ok {
		for k, v := range m {
			cp[k] = fmt.Sprint(v)
		}
	}
	for k, v := range body {
		if contains(passwordKeys, k) {
			cp[k] = fmt.Sprint(v)
		}
	}
	var creds []int64
	if id, ok := api.ID(cred); ok {
		creds = append(creds, id)
	}
	var missing []string
	for _, k := range passwordsNeeded(c, creds) {
		if cp[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, &api.Error{Status: 400, Body: Obj{"passwords_needed_to_start": missing}}
	}
	return cp, nil
}

func registerAdHocCommands() {
	invRes := api.Lookup("inventories")
	res := registerJobResource(jobSpec{
		name: "ad_hoc_commands", typ: "ad_hoc_command", verbose: "Ad Hoc Command",
		eventsName: "events", eventsRes: "ad_hoc_command_events",
		visible: func(c *Ctx, arg ArgFunc) string {
			return "t.inventory_id IN (SELECT t.id FROM inventories t WHERE " + viewOf(c, invRes, arg) + ")"
		},
		canManage: func(c *Ctx, job Obj) (bool, error) {
			inv, ok := api.ID(job["inventory"])
			if !ok {
				return false, nil
			}
			return api.HasPermID(c, "inventories", inv, "change")
		},
		extra: []*Field{
			{Name: "job_type", Type: api.TChoice, Default: "run", Choices: labelled("run", "Run", "check", "Check"), CreateOnly: true},
			{Name: "inventory", Type: api.TFK, Ref: "inventories", Required: true, Nullable: true, CreateOnly: true},
			{Name: "limit", Type: api.TText, Default: "", CreateOnly: true},
			{Name: "credential", Type: api.TFK, Ref: "credentials", Nullable: true, CreateOnly: true},
			{Name: "module_name", Type: api.TString, Default: "command", CreateOnly: true, ChoicesFunc: func(c *Ctx) []api.Choice {
				var allowed []string
				c.App.Settings.Decode(c, "AD_HOC_COMMANDS", &allowed)
				out := make([]api.Choice, len(allowed))
				for i, m := range allowed {
					out[i] = api.Choice{Value: m, Label: m}
				}
				return out
			}, Validate: func(c *Ctx, v any, data Obj) error {
				var allowed []string
				c.App.Settings.Decode(c, "AD_HOC_COMMANDS", &allowed)
				if !contains(allowed, str(v)) {
					return fmt.Errorf("Unsupported module for ad hoc commands.")
				}
				return nil
			}},
			{Name: "module_args", Type: api.TText, Default: "", CreateOnly: true},
			{Name: "forks", Type: api.TInt, Default: 0, CreateOnly: true},
			{Name: "verbosity", Type: api.TChoice, Default: 0, CreateOnly: true, Choices: verbosityChoices},
			{Name: "extra_vars", Type: api.TText, Default: "", CreateOnly: true, Validate: validateVars},
			{Name: "become_enabled", Type: api.TBool, Default: false, CreateOnly: true},
			{Name: "diff_mode", Type: api.TBool, Default: false, CreateOnly: true},
			{Name: "name", Type: api.TString, ReadOnly: true},
			{Name: "start_args", Type: api.TSecret, ReadOnly: true, Hidden: true, NoFilter: true},
		},
	})
	for _, f := range res.Fields {
		if f.Name == "execution_environment" {
			f.ReadOnly, f.CreateOnly = false, true
		}
	}
	res.NoCreate = false
	res.CanAdd = func(c *Ctx, data Obj) error {
		if err := requireInventoryPerm(c, data["inventory"], "adhoc"); err != nil {
			return err
		}
		if cid, ok := api.ID(data["credential"]); ok {
			if err := api.RequirePermID(c, "credential", "credentials", cid, "use"); err != nil {
				return err
			}
		}
		if eid, ok := api.ID(data["execution_environment"]); ok {
			if visible, _ := api.CanView(c, api.Lookup("execution_environments"), eid); !visible {
				return api.FieldError("execution_environment", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", eid))
			}
		}
		return nil
	}
	res.BeforeSave = func(c *Ctx, old, data Obj) error {
		if old != nil {
			return nil
		}
		if err := requireCredentialKind(c, "credential", data["credential"], "ssh"); err != nil {
			return err
		}
		if str(data["job_type"]) == "" {
			data["job_type"] = "run"
		}
		module := str(data["module_name"])
		if module == "" {
			module = "command"
			data["module_name"] = module
		}
		args := str(data["module_args"])
		if (module == "command" || module == "shell") && strings.TrimSpace(args) == "" {
			return api.FieldError("module_args", fmt.Sprintf("No argument passed to %s module.", module))
		}
		if c.App.Settings.String(c, "ALLOW_JINJA_IN_EXTRA_VARS") != "always" && (strings.Contains(args, "{{") || strings.Contains(args, "{%")) {
			return api.FieldError("module_args", "Jinja templates are not allowed in module arguments (setting ALLOW_JINJA_IN_EXTRA_VARS).")
		}
		cp, err := adHocPasswords(c, data["credential"])
		if err != nil {
			return err
		}
		ev, _ := vars.Parse(str(data["extra_vars"]))
		names := make([]string, 0, len(ev))
		for k := range ev {
			names = append(names, k)
		}
		sa, _ := json.Marshal(StartArgs{CredentialPasswords: cp, LaunchVars: names})
		inv, _ := api.ID(data["inventory"])
		var org *int64
		c.DB().QueryRow(c, "SELECT organization_id FROM inventories WHERE id = $1", inv).Scan(&org)
		data["organization"] = org
		data["name"] = module
		data["status"] = "pending"
		data["launch_type"] = "manual"
		data["start_args"] = string(sa)
		data["execution_environment"] = resolveEE(c, data["execution_environment"], Obj{"organization": org})
		return nil
	}
	res.AfterCreate = func(c *Ctx, obj Obj) error {
		jobs.Wake(c, c.DB())
		return nil
	}
	res.Actions = append(res.Actions, &api.Action{Name: "relaunch", Methods: []string{"GET", "POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, cmd Obj) {
		var creds []int64
		if id, ok := api.ID(cmd["credential"]); ok {
			creds = append(creds, id)
		}
		if r.Method == "GET" {
			api.WriteJSON(w, 200, Obj{"passwords_needed_to_start": nonNil(passwordsNeeded(c, creds))})
			return
		}
		body, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		c.Extra["body"] = body
		data := Obj{}
		for _, k := range []string{"job_type", "inventory", "limit", "credential", "module_name", "module_args", "forks", "verbosity",
			"extra_vars", "become_enabled", "diff_mode", "execution_environment"} {
			data[k] = cmd[k]
		}
		if _, ok := api.ID(data["inventory"]); !ok {
			api.WriteError(w, api.BadRequest("Inventory for this ad hoc command has been deleted."))
			return
		}
		obj, err := api.Create(c, res, data, nil)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out, err := api.SerializeOne(c, res, obj)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out["ad_hoc_command"] = obj["id"]
		api.WriteJSON(w, 201, out)
	}})
	registerEventResource("ad_hoc_command_events", "ad_hoc_command_event", "ad_hoc_command", "ad_hoc_command")

	invRes.SubLists = append(invRes.SubLists, &SubList{Name: "ad_hoc_commands", Target: "ad_hoc_commands", ParentField: "inventory",
		Filter: func(arg ArgFunc, pid int64) string { return "t.inventory_id = " + arg(pid) }})
	groups := api.Lookup("groups")
	groups.SubLists = append(groups.SubLists, &SubList{Name: "ad_hoc_commands", Target: "ad_hoc_commands",
		Filter: func(arg ArgFunc, pid int64) string {
			return fmt.Sprintf(`t.inventory_id = (SELECT inventory_id FROM groups WHERE id = %[1]s) AND t."limit" = (SELECT name FROM groups WHERE id = %[1]s)`, arg(pid))
		},
		CreateDefaults: func(c *Ctx, parent Obj) Obj { return Obj{"inventory": parent["inventory"], "limit": parent["name"]} }})
	hosts := api.Lookup("hosts")
	hosts.SubLists = append(hosts.SubLists,
		&SubList{Name: "ad_hoc_commands", Target: "ad_hoc_commands",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT job_id FROM job_events WHERE host_id = %s)", arg(pid))
			},
			CreateDefaults: func(c *Ctx, parent Obj) Obj { return Obj{"inventory": parent["inventory"], "limit": parent["name"]} }},
		&SubList{Name: "ad_hoc_command_events", Target: "ad_hoc_command_events", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.host_id = " + arg(pid) }})
}
