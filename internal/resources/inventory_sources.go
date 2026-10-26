package resources

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// InventoryPlugins maps AWX source names to the inventory plugin and the file
// name suffix the plugin requires.
var InventoryPlugins = map[string]struct{ Plugin, File, CredNamespace string }{
	"ec2":                      {"amazon.aws.aws_ec2", "aws_ec2.yml", "aws"},
	"gce":                      {"google.cloud.gcp_compute", "gcp_compute.yml", "gce"},
	"azure_rm":                 {"azure.azcollection.azure_rm", "azure_rm.yml", "azure_rm"},
	"vmware":                   {"community.vmware.vmware_vm_inventory", "vmware.yml", "vmware"},
	"openstack":                {"openstack.cloud.openstack", "openstack.yml", "openstack"},
	"satellite6":               {"theforeman.foreman.foreman", "foreman.yml", "satellite6"},
	"controller":               {"awx.awx.tower", "tower.yml", "controller"},
	"rhv":                      {"ovirt.ovirt.ovirt", "ovirt.yml", "rhv"},
	"insights":                 {"redhatinsights.insights.insights", "insights.yml", "insights"},
	"terraform":                {"cloud.terraform.terraform_state", "terraform_state.yml", "terraform"},
	"openshift_virtualization": {"kubevirt.core.kubevirt", "kubevirt.yml", "kubernetes_bearer_token"},
}

var inventorySourceChoices = labelled("scm", "Sourced from a Project", "constructed", "Template additional groups and hostvars at runtime",
	"ec2", "Amazon EC2", "gce", "Google Compute Engine", "azure_rm", "Microsoft Azure Resource Manager", "vmware", "VMware vCenter",
	"satellite6", "Red Hat Satellite 6", "openstack", "OpenStack", "rhv", "Red Hat Virtualization", "controller", "Red Hat Ansible Automation Platform",
	"insights", "Red Hat Insights", "terraform", "Terraform State", "openshift_virtualization", "OpenShift Virtualization")

// LaunchInventoryUpdate creates an inventory update job for a source.
func LaunchInventoryUpdate(c *Ctx, src Obj, launchType string) (int64, error) {
	sid, _ := api.ID(src["id"])
	values := map[string]any{
		"type":                     "inventory_update",
		"name":                     str(src["name"]),
		"unified_job_template_id":  sid,
		"organization_id":          src["organization"],
		"inventory_id":             src["inventory"],
		"source":                   src["source"],
		"source_path":              src["source_path"],
		"source_vars":              src["source_vars"],
		"credential_id":            src["credential"],
		"enabled_var":              src["enabled_var"],
		"enabled_value":            src["enabled_value"],
		"host_filter":              src["host_filter"],
		"overwrite":                src["overwrite"],
		"overwrite_vars":           src["overwrite_vars"],
		"timeout":                  src["timeout"],
		"verbosity":                src["verbosity"],
		"limit":                    src["limit"],
		"scm_branch":               src["scm_branch"],
		"project_id":               src["source_project"],
		"execution_environment_id": resolveEE(c, nil, Obj{"execution_environment": src["execution_environment"], "project": src["source_project"], "organization": src["organization"]}),
		"launch_type":              launchType,
	}
	if !c.User.Anonymous() {
		values["created_by_id"] = c.User.UserID
		values["modified_by_id"] = c.User.UserID
	}
	id, err := jobs.Create(c, c.DB(), values)
	if err != nil {
		return 0, err
	}
	jobs.Wake(c, c.DB())
	return id, nil
}

func registerInventorySources() {
	invRes := api.Lookup("inventories")
	srcView := func(c *Ctx, arg ArgFunc) string {
		if c.User.Superuser || c.User.Auditor {
			return "TRUE"
		}
		return "t.inventory_id IN (SELECT t.id FROM inventories t WHERE " + viewOf(c, invRes, arg) + ")"
	}
	src := api.Register(&Resource{
		Name:          "inventory_sources",
		Type:          "inventory_source",
		Table:         "unified_job_templates",
		Discriminator: true,
		Verbose:       "Inventory Source",
		Ordering:      "name",
		Versioned:     true,
		Managed:       true,
		Fields: append([]*Field{
			{Name: "inventory", Type: api.TFK, Ref: "inventories", Required: true, CreateOnly: true},
			{Name: "source", Type: api.TChoice, Required: true, Choices: inventorySourceChoices},
			{Name: "source_path", Type: api.TString, Default: "", MaxLength: 1024},
			{Name: "source_vars", Type: api.TText, Default: "", Validate: validateVars, Help: "Inventory source variables in YAML or JSON format."},
			{Name: "scm_branch", Type: api.TString, Default: "", MaxLength: 1024, Help: "Inventory source SCM branch. Project default used if blank. Only allowed if project allow_override field is set to true."},
			{Name: "credential", Type: api.TFK, Ref: "credentials", Nullable: true, Help: "Cloud credential to use for inventory updates."},
			{Name: "enabled_var", Type: api.TString, Default: "", Help: "Retrieve the enabled state from the given dict of host variables. The enabled variable may be specified as \"foo.bar\", in which case the lookup will traverse into nested dicts."},
			{Name: "enabled_value", Type: api.TString, Default: "", Help: "Only used when enabled_var is set. Value when the host is considered enabled."},
			{Name: "host_filter", Type: api.TString, Default: "", Help: "This field is deprecated and will be removed in a future release. Regex where only matching hosts will be imported."},
			{Name: "overwrite", Type: api.TBool, Default: false, Help: "Overwrite local groups and hosts from remote inventory source."},
			{Name: "overwrite_vars", Type: api.TBool, Default: false, Help: "Overwrite local variables from remote inventory source."},
			{Name: "timeout", Type: api.TInt, Default: 0},
			{Name: "verbosity", Type: api.TChoice, Default: 1, Choices: intChoices(0, "0 (WARNING)", 1, "1 (INFO)", 2, "2 (DEBUG)")},
			{Name: "limit", Type: api.TText, Default: "", Help: "Enter host, group or pattern match"},
			{Name: "update_on_launch", Type: api.TBool, Default: false},
			{Name: "update_cache_timeout", Type: api.TInt, Default: 0},
			{Name: "source_project", Type: api.TFK, Ref: "projects", Nullable: true, Help: "Project containing inventory file used as source."},
			{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true, Nullable: true, Hidden: true},
		}, ujtFields()...),
		View: srcView,
		CanAdd: func(c *Ctx, data Obj) error {
			if err := requireInventoryPerm(c, data["inventory"], "change"); err != nil {
				return err
			}
			if p, ok := api.ID(data["source_project"]); ok {
				return api.RequirePermID(c, "source_project", "projects", p, "use")
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			if err := requireInventoryPerm(c, old["inventory"], "change"); err != nil {
				return err
			}
			if fmt.Sprint(old["source_project"]) != fmt.Sprint(data["source_project"]) {
				if p, ok := api.ID(data["source_project"]); ok {
					return api.RequirePermID(c, "source_project", "projects", p, "use")
				}
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if countQuery(c, "SELECT count(*) FROM unified_jobs WHERE unified_job_template_id = $1 AND status IN ('pending','waiting','running')", obj["id"]) > 0 {
				return api.Detail(409, "Resource is being used by running jobs.")
			}
			return requireInventoryPerm(c, obj["inventory"], "change")
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			merged := Obj{}
			for k, v := range old {
				merged[k] = v
			}
			for k, v := range data {
				merged[k] = v
			}
			inv, _ := api.ID(merged["inventory"])
			var org *int64
			var kind string
			c.DB().QueryRow(c, "SELECT organization_id, kind FROM inventories WHERE id = $1", inv).Scan(&org, &kind)
			data["organization"] = org
			source := str(merged["source"])
			if kind == "smart" {
				return api.FieldError("inventory", "Cannot create Inventory Source for Smart Inventory")
			}
			if (source == "constructed") != (kind == "constructed") {
				return api.FieldError("source", "Constructed sources can only be used with constructed inventories (and vice versa).")
			}
			switch source {
			case "scm":
				pid, ok := api.ID(merged["source_project"])
				if !ok {
					return api.FieldError("source_project", "Project required for scm type sources.")
				}
				if strings.Contains(str(merged["source_path"]), "..") {
					return api.FieldError("source_path", "Invalid source path.")
				}
				if str(merged["scm_branch"]) != "" {
					var allow bool
					c.DB().QueryRow(c, "SELECT allow_override FROM unified_job_templates WHERE id = $1", pid).Scan(&allow)
					if !allow {
						return api.FieldError("scm_branch", "Project does not allow overriding branch.")
					}
				}
			default:
				if str(merged["scm_branch"]) != "" {
					return api.FieldError("scm_branch", "Cannot set scm_branch if not SCM type.")
				}
				if _, ok := api.ID(merged["source_project"]); ok {
					return api.FieldError("source_project", "Cannot set source_project if not SCM type.")
				}
			}
			if cid, ok := api.ID(merged["credential"]); ok {
				if p, ok := InventoryPlugins[source]; ok {
					var ns string
					c.DB().QueryRow(c, "SELECT coalesce(ct.namespace, '') FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1", cid).Scan(&ns)
					var kind string
					c.DB().QueryRow(c, "SELECT ct.kind FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1", cid).Scan(&kind)
					if ns != p.CredNamespace && kind != "cloud" && kind != "kubernetes" {
						return api.FieldError("credential", fmt.Sprintf("Cloud-based inventory sources (such as %s) require credentials for the matching cloud service.", source))
					}
				}
				if err := api.RequirePermID(c, "credential", "credentials", cid, "use"); err != nil {
					return err
				}
			}
			if hf := str(merged["host_filter"]); hf != "" {
				if _, err := regexp.Compile(hf); err != nil {
					return api.FieldError("host_filter", "Invalid regular expression: "+err.Error())
				}
			}
			if old == nil {
				data["status"] = "never updated"
			}
			return nil
		},
		Summary: ujtSummary,
		Serialize: func(c *Ctx, o Obj) {
			o["last_update_failed"] = o["last_job_failed"]
			o["last_updated"] = o["last_job_run"]
			o["custom_virtualenv"] = nil
		},
	})
	src.SubLists = append([]*SubList{
		{Name: "inventory_updates", Target: "inventory_updates", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) }},
		{Name: "hosts", Target: "hosts", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT host_id FROM host_inventory_sources WHERE inventory_source_id = %s)", arg(pid))
			}},
		{Name: "groups", Target: "groups", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT group_id FROM group_inventory_sources WHERE inventory_source_id = %s)", arg(pid))
			}},
	}, notificationSubs("inventorysource", false)...)
	src.Actions = []*api.Action{
		{Name: "update", Methods: []string{"GET", "POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			err := requireInventoryPerm(c, obj["inventory"], "update")
			if r.Method == "GET" {
				api.WriteJSON(w, 200, Obj{"can_update": err == nil})
				return
			}
			if err != nil {
				api.WriteError(w, err)
				return
			}
			id, err := LaunchInventoryUpdate(c, obj, "manual")
			if err != nil {
				api.WriteError(w, err)
				return
			}
			iu, _ := api.Get(c, api.Lookup("inventory_updates"), id)
			out, err := api.SerializeOne(c, api.Lookup("inventory_updates"), iu)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			out["inventory_update"] = id
			api.WriteJSON(w, 202, out)
		}},
	}
	invRes.SubLists = append(invRes.SubLists,
		&SubList{Name: "inventory_sources", Target: "inventory_sources", ParentField: "inventory",
			Filter: func(arg ArgFunc, pid int64) string { return "t.inventory_id = " + arg(pid) }})
	invRes.Actions = append(invRes.Actions, &api.Action{Name: "update_inventory_sources", Methods: []string{"GET", "POST"},
		Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, inv Obj) {
			id, _ := api.ID(inv["id"])
			srcs, _, err := api.List(c, src, api.ListParams{SkipView: true, Extra: []func(ArgFunc) string{func(arg ArgFunc) string { return "t.inventory_id = " + arg(id) }}})
			if err != nil {
				api.WriteError(w, err)
				return
			}
			perr := requireInventoryPerm(c, inv["id"], "update")
			var out []Obj
			for _, s := range srcs {
				sid, _ := api.ID(s["id"])
				entry := Obj{"inventory_source": sid, "can_update": perr == nil}
				if r.Method == "POST" && perr == nil {
					jid, err := LaunchInventoryUpdate(c, s, "manual")
					if err != nil {
						api.WriteError(w, err)
						return
					}
					entry["inventory_update"] = jid
					entry["status"] = "started"
				}
				out = append(out, entry)
			}
			if r.Method == "POST" {
				if perr != nil {
					api.WriteError(w, perr)
					return
				}
				if len(out) == 0 {
					api.WriteError(w, api.BadRequest("No inventory sources to update."))
					return
				}
				api.WriteJSON(w, 202, out)
				return
			}
			if out == nil {
				out = []Obj{}
			}
			api.WriteJSON(w, 200, out)
		}})
	for _, n := range []string{"hosts", "groups"} {
		r := api.Lookup(n)
		r.SubLists = append(r.SubLists, &SubList{Name: "inventory_sources", Target: "inventory_sources", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT inventory_source_id FROM %s_inventory_sources WHERE %s_id = %s)", n[:len(n)-1], n[:len(n)-1], arg(pid))
			}})
	}

	registerJobResource(jobSpec{
		name: "inventory_updates", typ: "inventory_update", verbose: "Inventory Update",
		templateField: "inventory_source", templateRes: "inventory_sources", eventsName: "events", eventsRes: "inventory_update_events",
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
		canCancel: func(c *Ctx, job Obj) (bool, error) {
			inv, ok := api.ID(job["inventory"])
			if !ok {
				return false, nil
			}
			return api.HasPermID(c, "inventories", inv, "update")
		},
		extra: []*Field{
			{Name: "inventory", Type: api.TFK, Ref: "inventories", ReadOnly: true, Nullable: true},
			{Name: "source", Type: api.TString, ReadOnly: true},
			{Name: "source_path", Type: api.TString, ReadOnly: true},
			{Name: "source_vars", Type: api.TText, ReadOnly: true},
			{Name: "scm_branch", Type: api.TString, ReadOnly: true},
			{Name: "credential", Type: api.TFK, Ref: "credentials", ReadOnly: true, Nullable: true},
			{Name: "enabled_var", Type: api.TString, ReadOnly: true},
			{Name: "enabled_value", Type: api.TString, ReadOnly: true},
			{Name: "host_filter", Type: api.TString, ReadOnly: true},
			{Name: "overwrite", Type: api.TBool, ReadOnly: true},
			{Name: "overwrite_vars", Type: api.TBool, ReadOnly: true},
			{Name: "timeout", Type: api.TInt, ReadOnly: true},
			{Name: "verbosity", Type: api.TInt, ReadOnly: true},
			{Name: "limit", Type: api.TText, ReadOnly: true},
			{Name: "scm_revision", Type: api.TString, ReadOnly: true},
			{Name: "source_project_update", Type: api.TFK, Ref: "project_updates", ReadOnly: true, Nullable: true},
		},
	})
	registerEventResource("inventory_update_events", "inventory_update_event", "inventory_update", "inventory_update")
	registerConstructedInventories()
}

// registerConstructedInventories exposes /constructed_inventories/: inventories
// of kind "constructed" whose source settings live on an auto-created source.
func registerConstructedInventories() {
	inv := api.Lookup("inventories")
	ci := api.Register(&Resource{
		Name:         "constructed_inventories",
		Type:         "constructed_inventory",
		Table:        "inventories",
		Verbose:      "Constructed Inventory",
		Ordering:     "name",
		ContentType:  "inventory",
		OrgColumn:    "organization_id",
		FolderColumn: "folder_id",
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true},
			{Name: "kind", Type: api.TString, ReadOnly: true},
			varsField(),
			folderField(),
			{Name: "source_vars", Type: api.TText, Virtual: true, Default: "", Validate: validateVars, Help: "The source_vars for the related auto-created inventory source, special to constructed inventory."},
			{Name: "update_cache_timeout", Type: api.TInt, Virtual: true, Help: "The cache timeout for the related auto-created inventory source, special to constructed inventory"},
			{Name: "limit", Type: api.TText, Virtual: true, Help: "The limit to restrict the returned hosts for the related auto-created inventory source, special to constructed inventory."},
			{Name: "verbosity", Type: api.TInt, Virtual: true, Help: "The verbosity level for the related auto-created inventory source, special to constructed inventory"},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return "t.kind = 'constructed' AND (" + viewOf(c, inv, arg) + ")"
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			if old == nil {
				data["kind"] = "constructed"
			}
			c.Extra["constructed_source"] = Obj{"source_vars": data["source_vars"], "update_cache_timeout": data["update_cache_timeout"],
				"limit": data["limit"], "verbosity": data["verbosity"]}
			return nil
		},
		AfterCreate: func(c *Ctx, obj Obj) error {
			id, _ := api.ID(obj["id"])
			srcVals := c.Extra["constructed_source"].(Obj)
			values := map[string]any{"type": "inventory_source", "name": "Auto-created source for: " + str(obj["name"]),
				"inventory_id": id, "organization_id": obj["organization"], "source": "constructed", "overwrite": true, "overwrite_vars": true,
				"update_on_launch": true, "status": "never updated"}
			for k, v := range srcVals {
				if v != nil {
					values[k] = v
				}
			}
			_, err := insertRaw(c, "unified_job_templates", values)
			return err
		},
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			id, _ := api.ID(obj["id"])
			for k, v := range c.Extra["constructed_source"].(Obj) {
				if v != nil {
					if _, err := c.DB().Exec(c, fmt.Sprintf(`UPDATE unified_job_templates SET "%s" = $2 WHERE type = 'inventory_source' AND source = 'constructed' AND inventory_id = $1`, k), id, v); err != nil {
						return err
					}
				}
			}
			return nil
		},
		Summary: func(c *Ctx, objs []Obj) error {
			if err := summarizeInventories(c, objs); err != nil {
				return err
			}
			for _, o := range objs {
				var sv, limit string
				var cache, verbosity int64
				c.DB().QueryRow(c, `SELECT source_vars, update_cache_timeout, "limit", verbosity FROM unified_job_templates
					WHERE type = 'inventory_source' AND source = 'constructed' AND inventory_id = $1`, o["id"]).Scan(&sv, &cache, &limit, &verbosity)
				o["source_vars"], o["update_cache_timeout"], o["limit"], o["verbosity"] = sv, cache, limit, verbosity
				id, _ := api.ID(o["id"])
				o["related"].(Obj)["inventory_sources"] = inv.URL(id) + "inventory_sources/"
				o["related"].(Obj)["input_inventories"] = inv.URL(id) + "input_inventories/"
			}
			return nil
		},
	})
	for _, s := range inv.SubLists {
		if s.Name == "input_inventories" || s.Name == "hosts" || s.Name == "groups" || s.Name == "inventory_sources" {
			ci.SubLists = append(ci.SubLists, s)
		}
	}
}

// insertRaw inserts a row with literal column values (for system-created objects).
func insertRaw(c *Ctx, table string, values map[string]any) (int64, error) {
	cols, phs, args := []string{}, []string{}, []any{}
	for k, v := range values {
		args = append(args, v)
		cols = append(cols, fmt.Sprintf("%q", k))
		phs = append(phs, fmt.Sprintf("$%d", len(args)))
	}
	var id int64
	err := c.DB().QueryRow(c, "INSERT INTO "+table+" ("+joinStrings(cols)+") VALUES ("+joinStrings(phs)+") RETURNING id", args...).Scan(&id)
	return id, err
}

func joinStrings(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
