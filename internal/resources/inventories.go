package resources

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/vars"
)

// validateVars checks a variables text field parses as a YAML/JSON mapping.
func validateVars(c *Ctx, v any, data Obj) error {
	if _, err := vars.Parse(str(v)); err != nil {
		return err
	}
	return nil
}

func varsField() *Field {
	return &Field{Name: "variables", Type: api.TText, Default: "", Validate: validateVars,
		Help: "Variables in JSON or YAML format. Ansible Vault values are supported (!vault or {\"__ansible_vault\": ...})."}
}

// inventoryOf returns the inventory id of a host/group object.
func inventoryOf(obj Obj) int64 {
	id, _ := api.ID(obj["inventory"])
	return id
}

func requireInventoryPerm(c *Ctx, inv any, perm string) error {
	id, ok := api.ID(inv)
	if !ok {
		return api.FieldError("inventory", "This field is required.")
	}
	ok, err := api.HasPermID(c, "inventories", id, perm)
	if err != nil {
		if err == api.ErrNotFound {
			return api.FieldError("inventory", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
		}
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	return nil
}

// inventoryKind returns the kind of an inventory.
func inventoryKind(c *Ctx, id int64) string {
	var kind string
	c.DB().QueryRow(c, "SELECT kind FROM inventories WHERE id = $1", id).Scan(&kind)
	return kind
}

// childOfInventory gives hosts and groups their access rules: visible with the
// inventory, writable by inventory admins. Smart and constructed inventories have no own hosts/groups.
func childOfInventory(kindName string) (view func(c *Ctx, arg ArgFunc) string, add func(c *Ctx, data Obj) error,
	change func(c *Ctx, old, data Obj) error, del func(c *Ctx, obj Obj) error) {
	view = func(c *Ctx, arg ArgFunc) string {
		if c.User.Superuser || c.User.Auditor {
			return "TRUE"
		}
		return "t.inventory_id IN (SELECT t.id FROM inventories t WHERE " + viewOf(c, api.Lookup("inventories"), arg) + ")"
	}
	add = func(c *Ctx, data Obj) error {
		if err := requireInventoryPerm(c, data["inventory"], "change"); err != nil {
			return err
		}
		inv, _ := api.ID(data["inventory"])
		if k := inventoryKind(c, inv); k != "" {
			return api.BadRequest(fmt.Sprintf("Cannot create %s for %s inventories.", kindName, k))
		}
		return nil
	}
	change = func(c *Ctx, old, data Obj) error {
		if inventoryOf(old) != inventoryOf(data) {
			return api.FieldError("inventory", "Cannot move to another inventory.")
		}
		return requireInventoryPerm(c, old["inventory"], "change")
	}
	del = func(c *Ctx, obj Obj) error { return requireInventoryPerm(c, obj["inventory"], "change") }
	return
}

func registerInventories() {
	inv := api.Register(&Resource{
		Name:         "inventories",
		Type:         "inventory",
		Table:        "inventories",
		Verbose:      "Inventory",
		Ordering:     "name",
		ContentType:  "inventory",
		OrgColumn:    "organization_id",
		FolderColumn: "folder_id",
		Versioned:    true,
		Managed:      true,
		Copyable:     true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true, Help: "Organization containing this inventory."},
			{Name: "kind", Type: api.TChoice, Default: "", CreateOnly: true, Choices: labelled("", "Hosts have a direct link to this inventory.", "smart", "Hosts for inventory generated using the host_filter property.", "constructed", "Parse list of source inventories with the constructed inventory plugin."), Help: "Kind of inventory being represented."},
			{Name: "host_filter", Type: api.TText, Default: "", Help: "Filter that will be applied to the hosts of this inventory."},
			varsField(),
			folderField(),
			{Name: "has_active_failures", Type: api.TBool, ReadOnly: true, NoFilter: true},
			{Name: "total_hosts", Type: api.TInt, ReadOnly: true, NoFilter: true},
			{Name: "hosts_with_active_failures", Type: api.TInt, ReadOnly: true, NoFilter: true},
			{Name: "total_groups", Type: api.TInt, ReadOnly: true, NoFilter: true},
			{Name: "has_inventory_sources", Type: api.TBool, ReadOnly: true, NoFilter: true},
			{Name: "total_inventory_sources", Type: api.TInt, ReadOnly: true, NoFilter: true},
			{Name: "inventory_sources_with_failures", Type: api.TInt, ReadOnly: true, NoFilter: true},
			{Name: "pending_deletion", Type: api.TBool, ReadOnly: true},
			{Name: "prevent_instance_group_fallback", Type: api.TBool, Default: false},
			{Name: "host_filter_sql", Type: api.TText, ReadOnly: true, Hidden: true, NoFilter: true},
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			kind := str(data["kind"])
			if old != nil {
				kind = str(old["kind"])
			}
			hf, given := data["host_filter"]
			if !given && old != nil {
				hf = old["host_filter"]
			}
			if kind == "smart" {
				if str(hf) == "" {
					return api.FieldError("host_filter", "Smart inventories must specify a host_filter.")
				}
				compiled, err := SmartFilterSQL(str(hf), api.SQLLiteral)
				if err != nil {
					return api.FieldError("host_filter", "Invalid filter: "+err.Error())
				}
				// Validate the compiled SQL before storing it.
				if _, err := c.DB().Exec(c, "SELECT 1 FROM hosts t WHERE ("+compiled+") LIMIT 0"); err != nil {
					return api.FieldError("host_filter", "Invalid filter: "+err.Error())
				}
				data["host_filter_sql"] = compiled
			} else if given && str(hf) != "" {
				return api.FieldError("host_filter", "host_filter is only valid for smart inventories.")
			}
			return nil
		},
		Summary: summarizeInventories,
		AfterCopy: func(c *Ctx, src, dst Obj) error {
			return copyInventoryContents(c, src["id"], dst["id"])
		},
	})
	inv.SubLists = []*SubList{
		{
			Name: "hosts", Target: "hosts", ParentField: "inventory", SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string {
				// Smart inventories list hosts of the organization's regular inventories that match host_filter.
				p := arg(pid)
				return fmt.Sprintf(`t.inventory_id = %[1]s OR t.id IN (SELECT host_id FROM smart_inventory_hosts(%[1]s))`, p)
			},
		},
		childSub("groups", "groups", "inventory_id", "inventory"),
		{
			Name: "root_groups", Target: "groups", ParentField: "inventory",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.inventory_id = %s AND NOT EXISTS (SELECT 1 FROM group_children gc WHERE gc.child_id = t.id)", arg(pid))
			},
		},
		{
			Name: "input_inventories", Target: "inventories", NoCreate: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT input_inventory_id FROM inventory_input_inventories WHERE inventory_id = %s)", arg(pid))
			},
			CanAttach: func(c *Ctx, parent Obj, child int64) error {
				if str(parent["kind"]) != "constructed" {
					return api.BadRequest("Input inventories can only be set on constructed inventories.")
				}
				if k := inventoryKind(c, child); k == "constructed" {
					return api.BadRequest("A constructed inventory cannot be an input of another constructed inventory.")
				}
				return api.RequirePermID(c, "id", "inventories", child, "use")
			},
			Associate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, `INSERT INTO inventory_input_inventories (inventory_id, input_inventory_id, position)
					VALUES ($1, $2, (SELECT coalesce(max(position) + 1, 0) FROM inventory_input_inventories WHERE inventory_id = $1)) ON CONFLICT DO NOTHING`, pid, cid)
				return err
			},
			Disassociate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM inventory_input_inventories WHERE inventory_id = $1 AND input_inventory_id = $2", pid, cid)
				return err
			},
		},
		orderedLinkSub("instance_groups", "instance_groups", "instance_group_links", "instance_group_id", "inventory"),
	}
	inv.Actions = []*api.Action{
		{Name: "script", Methods: []string{"GET"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			q := r.URL.Query()
			id, _ := api.ID(obj["id"])
			if h := q.Get("host"); h != "" {
				hv, err := hostVars(c, id, h)
				if err != nil {
					api.WriteError(w, err)
					return
				}
				api.WriteJSON(w, 200, hv)
				return
			}
			script, err := InventoryScript(c, c.App, id, ScriptOptions{HostVars: q.Get("hostvars") != "", All: q.Get("all") != "", TowerVars: q.Get("towervars") != ""})
			if err != nil {
				api.WriteError(w, err)
				return
			}
			api.WriteJSON(w, 200, script)
		}},
		{Name: "tree", Methods: []string{"GET"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			id, _ := api.ID(obj["id"])
			tree, err := groupTree(c, id)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			api.WriteJSON(w, 200, tree)
		}},
		variableDataAction("inventories"),
	}

	hView, hAdd, hChange, hDel := childOfInventory("hosts")
	hosts := api.Register(&Resource{
		Name:     "hosts",
		Type:     "host",
		Table:    "hosts",
		Verbose:  "Host",
		Ordering: "name",
		Fields: []*Field{
			{Name: "inventory", Type: api.TFK, Ref: "inventories", Required: true},
			{Name: "enabled", Type: api.TBool, Default: true, Help: "Is this host online and available for running jobs?"},
			{Name: "instance_id", Type: api.TString, Default: "", MaxLength: 1024, Help: "The value used by the remote inventory source to uniquely identify the host"},
			varsField(),
			{Name: "last_job", Type: api.TFK, Ref: "jobs", ReadOnly: true, Nullable: true},
			{Name: "last_job_host_summary", Type: api.TFK, Ref: "job_host_summaries", ReadOnly: true, Nullable: true},
			{Name: "ansible_facts", Type: api.TJSON, ReadOnly: true, Hidden: true},
			{Name: "ansible_facts_modified", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "The date and time ansible_facts was last modified."},
		},
		Relations: map[string]api.Relation{
			"groups": {Target: "groups", Join: func(a, b string) string {
				return fmt.Sprintf("EXISTS (SELECT 1 FROM group_hosts gh WHERE gh.host_id = %s.id AND gh.group_id = %s.id)", a, b)
			}},
		},
		View:      hView,
		CanAdd:    func(c *Ctx, data Obj) error { return checkHostAdd(c, data, hAdd) },
		CanChange: hChange,
		CanDelete: hDel,
		Summary:   summarizeHosts,
	})
	hosts.SubLists = []*SubList{
		{
			Name: "groups", Target: "groups", NoCreate: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT group_id FROM group_hosts WHERE host_id = %s)", arg(pid))
			},
			CanAttach:  sameInventory("groups"),
			AttachPerm: "none",
			Associate: func(c *Ctx, hid, gid int64) error {
				_, err := c.DB().Exec(c, "INSERT INTO group_hosts (group_id, host_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", gid, hid)
				return err
			},
			Disassociate: func(c *Ctx, hid, gid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM group_hosts WHERE group_id = $1 AND host_id = $2", gid, hid)
				return err
			},
		},
		{
			Name: "all_groups", Target: "groups", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf(`t.id IN (WITH RECURSIVE anc(id) AS (SELECT group_id FROM group_hosts WHERE host_id = %s
					UNION SELECT gc.parent_id FROM group_children gc JOIN anc ON gc.child_id = anc.id) SELECT id FROM anc)`, arg(pid))
			},
		},
		{
			Name: "smart_inventories", Target: "inventories", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf(`t.kind = 'smart' AND t.organization_id = (SELECT i.organization_id FROM hosts h JOIN inventories i ON i.id = h.inventory_id WHERE h.id = %[1]s)
					AND %[1]s IN (SELECT host_id FROM smart_inventory_hosts(t.id))`, arg(pid))
			},
		},
	}
	hosts.Actions = []*api.Action{
		{Name: "ansible_facts", Methods: []string{"GET"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			facts := obj["ansible_facts"]
			if facts == nil {
				facts = Obj{}
			}
			api.WriteJSON(w, 200, facts)
		}},
		variableDataAction("hosts"),
	}

	gView, gAdd, gChange, gDel := childOfInventory("groups")
	groups := api.Register(&Resource{
		Name:     "groups",
		Type:     "group",
		Table:    "groups",
		Verbose:  "Group",
		Ordering: "name",
		Fields: []*Field{
			{Name: "inventory", Type: api.TFK, Ref: "inventories", Required: true},
			varsField(),
		},
		Relations: map[string]api.Relation{
			"hosts": {Target: "hosts", Join: func(a, b string) string {
				return fmt.Sprintf("EXISTS (SELECT 1 FROM group_hosts gh WHERE gh.group_id = %s.id AND gh.host_id = %s.id)", a, b)
			}},
			"children": {Target: "groups", Join: func(a, b string) string {
				return fmt.Sprintf("EXISTS (SELECT 1 FROM group_children gc WHERE gc.parent_id = %s.id AND gc.child_id = %s.id)", a, b)
			}},
			"parents": {Target: "groups", Join: func(a, b string) string {
				return fmt.Sprintf("EXISTS (SELECT 1 FROM group_children gc WHERE gc.child_id = %s.id AND gc.parent_id = %s.id)", a, b)
			}},
		},
		View:      gView,
		CanAdd:    gAdd,
		CanChange: gChange,
		CanDelete: gDel,
	})
	groups.SubLists = []*SubList{
		{
			Name: "children", Target: "groups",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT child_id FROM group_children WHERE parent_id = %s)", arg(pid))
			},
			CreateDefaults: func(c *Ctx, parent Obj) Obj { return Obj{"inventory": parent["inventory"]} },
			AttachPerm:     "none",
			CanAttach: func(c *Ctx, parent Obj, child int64) error {
				if err := sameInventory("groups")(c, parent, child); err != nil {
					return err
				}
				pid, _ := api.ID(parent["id"])
				var cycle bool
				c.DB().QueryRow(c, `WITH RECURSIVE d(id) AS (SELECT $1::bigint UNION SELECT gc.child_id FROM group_children gc JOIN d ON gc.parent_id = d.id)
					SELECT EXISTS (SELECT 1 FROM d WHERE id = $2)`, child, pid).Scan(&cycle)
				if cycle {
					return api.BadRequest("Cyclical Group association.")
				}
				return nil
			},
			Associate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "INSERT INTO group_children (parent_id, child_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", pid, cid)
				return err
			},
			Disassociate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM group_children WHERE parent_id = $1 AND child_id = $2", pid, cid)
				return err
			},
		},
		{
			Name: "hosts", Target: "hosts",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT host_id FROM group_hosts WHERE group_id = %s)", arg(pid))
			},
			CreateDefaults: func(c *Ctx, parent Obj) Obj { return Obj{"inventory": parent["inventory"]} },
			CanAttach:      sameInventory("hosts"),
			AttachPerm:     "none",
			Associate: func(c *Ctx, gid, hid int64) error {
				_, err := c.DB().Exec(c, "INSERT INTO group_hosts (group_id, host_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", gid, hid)
				return err
			},
			Disassociate: func(c *Ctx, gid, hid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM group_hosts WHERE group_id = $1 AND host_id = $2", gid, hid)
				return err
			},
		},
		{
			Name: "all_hosts", Target: "hosts", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf(`t.id IN (SELECT gh.host_id FROM group_hosts gh WHERE gh.group_id IN (
					WITH RECURSIVE d(id) AS (SELECT %s::bigint UNION SELECT gc.child_id FROM group_children gc JOIN d ON gc.parent_id = d.id) SELECT id FROM d))`, arg(pid))
			},
		},
		{
			Name: "potential_children", Target: "groups", ReadOnly: true,
			Filter: func(arg ArgFunc, pid int64) string {
				p := arg(pid)
				return fmt.Sprintf(`t.inventory_id = (SELECT inventory_id FROM groups WHERE id = %[1]s) AND t.id NOT IN (
					WITH RECURSIVE anc(id) AS (SELECT %[1]s::bigint UNION SELECT gc.parent_id FROM group_children gc JOIN anc ON gc.child_id = anc.id) SELECT id FROM anc)
					AND t.id NOT IN (SELECT child_id FROM group_children WHERE parent_id = %[1]s)`, p)
			},
		},
	}
	groups.Actions = []*api.Action{variableDataAction("groups")}
}

// checkHostAdd enforces inventory permissions and the organization's max_hosts.
func checkHostAdd(c *Ctx, data Obj, base func(c *Ctx, data Obj) error) error {
	if err := base(c, data); err != nil {
		return err
	}
	inv, _ := api.ID(data["inventory"])
	var max, current int64
	c.DB().QueryRow(c, `SELECT o.max_hosts, (SELECT count(DISTINCT h.name) FROM hosts h JOIN inventories i2 ON i2.id = h.inventory_id WHERE i2.organization_id = o.id)
		FROM inventories i JOIN organizations o ON o.id = i.organization_id WHERE i.id = $1`, inv).Scan(&max, &current)
	if max > 0 && current >= max {
		return api.BadRequest(fmt.Sprintf("You have already reached the maximum number of %d hosts allowed for your organization. Contact your System Administrator for assistance.", max))
	}
	return nil
}

// sameInventory requires change on the parent's inventory and the attached
// object to be in that same inventory.
func sameInventory(table string) func(c *Ctx, parent Obj, child int64) error {
	return func(c *Ctx, parent Obj, child int64) error {
		if err := requireInventoryPerm(c, parent["inventory"], "change"); err != nil {
			return err
		}
		var inv int64
		c.DB().QueryRow(c, "SELECT inventory_id FROM "+table+" WHERE id = $1", child).Scan(&inv)
		if inv != inventoryOf(parent) {
			return api.BadRequest("Cannot associate objects from different inventories.")
		}
		return nil
	}
}

// variableDataAction exposes /<res>/<id>/variable_data/ as a parsed mapping.
func variableDataAction(resName string) *api.Action {
	return &api.Action{Name: "variable_data", Methods: []string{"GET", "PUT", "PATCH"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		res := api.Lookup(resName)
		current, err := vars.Parse(str(obj["variables"]))
		if err != nil {
			current = map[string]any{}
		}
		if r.Method == "GET" {
			api.WriteJSON(w, 200, current)
			return
		}
		data, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		next, _ := normalize(data).(map[string]any)
		if r.Method == "PATCH" {
			next = vars.Merge(current, next)
		}
		updated, err := api.Update(c, res, obj, Obj{"variables": vars.MustJSON(next)})
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out, _ := vars.Parse(str(updated["variables"]))
		api.WriteJSON(w, 200, out)
	}}
}

func summarizeInventories(c *Ctx, objs []Obj) error {
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		var hosts, failedHosts, groups, sources, failedSources int64
		if str(o["kind"]) == "smart" {
			c.DB().QueryRow(c, "SELECT count(*) FROM smart_inventory_hosts($1)", id).Scan(&hosts)
		} else {
			c.DB().QueryRow(c, `SELECT
				(SELECT count(*) FROM hosts WHERE inventory_id = $1),
				(SELECT count(*) FROM hosts h JOIN job_host_summaries s ON s.id = h.last_job_host_summary_id WHERE h.inventory_id = $1 AND s.failed),
				(SELECT count(*) FROM groups WHERE inventory_id = $1),
				(SELECT count(*) FROM unified_job_templates WHERE type = 'inventory_source' AND inventory_id = $1),
				(SELECT count(*) FROM unified_job_templates WHERE type = 'inventory_source' AND inventory_id = $1 AND last_job_failed)`, id).
				Scan(&hosts, &failedHosts, &groups, &sources, &failedSources)
		}
		o["total_hosts"] = hosts
		o["hosts_with_active_failures"] = failedHosts
		o["has_active_failures"] = failedHosts > 0
		o["total_groups"] = groups
		o["total_inventory_sources"] = sources
		o["has_inventory_sources"] = sources > 0
		o["inventory_sources_with_failures"] = failedSources
	}
	return nil
}

func summarizeHosts(c *Ctx, objs []Obj) error {
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		groups := []Obj{}
		var total int64
		rows, err := c.DB().Query(c, `SELECT g.id, g.name, count(*) OVER () FROM groups g JOIN group_hosts gh ON gh.group_id = g.id
			WHERE gh.host_id = $1 ORDER BY g.name LIMIT 5`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var gid int64
			var name string
			rows.Scan(&gid, &name, &total)
			groups = append(groups, Obj{"id": gid, "name": name})
		}
		rows.Close()
		recent := []Obj{}
		rows, err = c.DB().Query(c, `SELECT j.id, j.name, j.status, j.finished, j.type FROM job_host_summaries s JOIN unified_jobs j ON j.id = s.job_id
			WHERE s.host_id = $1 ORDER BY j.id DESC LIMIT 5`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var jid int64
			var name, status, typ string
			var finished any
			rows.Scan(&jid, &name, &status, &finished, &typ)
			recent = append(recent, Obj{"id": jid, "name": name, "status": status, "finished": finished, "type": typ})
		}
		rows.Close()
		var failed bool
		c.DB().QueryRow(c, "SELECT coalesce(s.failed, false) FROM hosts h LEFT JOIN job_host_summaries s ON s.id = h.last_job_host_summary_id WHERE h.id = $1", id).Scan(&failed)
		o["has_active_failures"] = failed
		o["has_inventory_sources"] = countQuery(c, "SELECT count(*) FROM host_inventory_sources WHERE host_id = $1", id) > 0
		sf := o["summary_fields"].(Obj)
		sf["groups"] = Obj{"count": total, "results": groups}
		sf["recent_jobs"] = recent
	}
	return nil
}

func copyInventoryContents(c *Ctx, src, dst any) error {
	for _, sql := range []string{
		`INSERT INTO groups (inventory_id, name, description, variables, created_by_id, modified_by_id)
			SELECT $2, name, description, variables, $3, $3 FROM groups WHERE inventory_id = $1`,
		`INSERT INTO hosts (inventory_id, name, description, variables, enabled, instance_id, created_by_id, modified_by_id)
			SELECT $2, name, description, variables, enabled, instance_id, $3, $3 FROM hosts WHERE inventory_id = $1`,
		`INSERT INTO group_children (parent_id, child_id)
			SELECT np.id, nc.id FROM group_children x
			JOIN groups op ON op.id = x.parent_id JOIN groups oc ON oc.id = x.child_id
			JOIN groups np ON np.inventory_id = $2 AND np.name = op.name JOIN groups nc ON nc.inventory_id = $2 AND nc.name = oc.name
			WHERE op.inventory_id = $1 AND $3::bigint IS NOT DISTINCT FROM $3`,
		`INSERT INTO group_hosts (group_id, host_id)
			SELECT ng.id, nh.id FROM group_hosts x
			JOIN groups og ON og.id = x.group_id JOIN hosts oh ON oh.id = x.host_id
			JOIN groups ng ON ng.inventory_id = $2 AND ng.name = og.name JOIN hosts nh ON nh.inventory_id = $2 AND nh.name = oh.name
			WHERE og.inventory_id = $1 AND $3::bigint IS NOT DISTINCT FROM $3`,
	} {
		var actor any
		if !c.User.Anonymous() {
			actor = c.User.UserID
		}
		if _, err := c.DB().Exec(c, sql, src, dst, actor); err != nil {
			return err
		}
	}
	return nil
}

// ScriptOptions controls InventoryScript output (AWX /script/ query parameters).
type ScriptOptions struct {
	HostVars  bool // include _meta.hostvars
	All       bool // include disabled hosts
	TowerVars bool // add remote_tower_enabled / remote_tower_id to hostvars
}

// InventoryScript renders an inventory in Ansible's JSON inventory format.
func InventoryScript(ctx context.Context, app *api.App, invID int64, opt ScriptOptions) (map[string]any, error) {
	db := app.DB
	var kind, invVars string
	if err := db.QueryRow(ctx, "SELECT kind, variables FROM inventories WHERE id = $1", invID).Scan(&kind, &invVars); err != nil {
		return nil, api.ErrNotFound
	}
	allVars, err := vars.Parse(invVars)
	if err != nil {
		return nil, fmt.Errorf("inventory variables: %w", err)
	}
	type host struct {
		id      int64
		name    string
		vars    string
		enabled bool
	}
	hostSQL := "SELECT id, name, variables, enabled FROM hosts WHERE inventory_id = $1"
	if kind == "smart" {
		hostSQL = "SELECT h.id, h.name, h.variables, h.enabled FROM hosts h WHERE h.id IN (SELECT host_id FROM smart_inventory_hosts($1))"
	}
	rows, err := db.Query(ctx, hostSQL+" ORDER BY name", invID)
	if err != nil {
		return nil, err
	}
	var hostList []host
	hostByID := map[int64]host{}
	for rows.Next() {
		var h host
		if err := rows.Scan(&h.id, &h.name, &h.vars, &h.enabled); err != nil {
			rows.Close()
			return nil, err
		}
		if !h.enabled && !opt.All {
			continue
		}
		hostList = append(hostList, h)
		hostByID[h.id] = h
	}
	rows.Close()

	out := map[string]any{}
	grouped := map[int64]bool{}
	var topLevel []string
	if kind != "smart" {
		type group struct {
			id   int64
			name string
			vars string
		}
		rows, err = db.Query(ctx, "SELECT id, name, variables FROM groups WHERE inventory_id = $1 ORDER BY name", invID)
		if err != nil {
			return nil, err
		}
		var groupList []group
		groupName := map[int64]string{}
		for rows.Next() {
			var g group
			rows.Scan(&g.id, &g.name, &g.vars)
			groupList = append(groupList, g)
			groupName[g.id] = g.name
		}
		rows.Close()
		children := map[int64][]string{}
		hasParent := map[int64]bool{}
		rows, err = db.Query(ctx, "SELECT gc.parent_id, gc.child_id FROM group_children gc JOIN groups g ON g.id = gc.parent_id WHERE g.inventory_id = $1", invID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p, ch int64
			rows.Scan(&p, &ch)
			children[p] = append(children[p], groupName[ch])
			hasParent[ch] = true
		}
		rows.Close()
		members := map[int64][]string{}
		rows, err = db.Query(ctx, "SELECT gh.group_id, gh.host_id FROM group_hosts gh JOIN groups g ON g.id = gh.group_id WHERE g.inventory_id = $1", invID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var gid, hid int64
			rows.Scan(&gid, &hid)
			if h, ok := hostByID[hid]; ok {
				members[gid] = append(members[gid], h.name)
				grouped[hid] = true
			}
		}
		rows.Close()
		for _, g := range groupList {
			gv, err := vars.Parse(g.vars)
			if err != nil {
				return nil, fmt.Errorf("group %s variables: %w", g.name, err)
			}
			hs := members[g.id]
			sort.Strings(hs)
			ch := children[g.id]
			sort.Strings(ch)
			entry := map[string]any{"hosts": nonNil(hs), "children": nonNil(ch), "vars": gv}
			out[g.name] = entry
			if !hasParent[g.id] {
				topLevel = append(topLevel, g.name)
			}
		}
	}
	var ungrouped []string
	for _, h := range hostList {
		if !grouped[h.id] {
			ungrouped = append(ungrouped, h.name)
		}
	}
	if len(ungrouped) > 0 || kind == "smart" {
		out["ungrouped"] = map[string]any{"hosts": nonNil(ungrouped)}
		topLevel = append(topLevel, "ungrouped")
	}
	sort.Strings(topLevel)
	out["all"] = map[string]any{"children": nonNil(topLevel), "vars": allVars}
	if opt.HostVars {
		hv := map[string]any{}
		for _, h := range hostList {
			v, err := vars.Parse(h.vars)
			if err != nil {
				return nil, fmt.Errorf("host %s variables: %w", h.name, err)
			}
			if opt.TowerVars {
				v["remote_tower_enabled"] = fmt.Sprint(h.enabled)
				v["remote_tower_id"] = h.id
			}
			hv[h.name] = v
		}
		out["_meta"] = map[string]any{"hostvars": hv}
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func hostVars(c *Ctx, invID int64, name string) (map[string]any, error) {
	var v string
	if err := c.DB().QueryRow(c, "SELECT variables FROM hosts WHERE inventory_id = $1 AND name = $2", invID, name).Scan(&v); err != nil {
		return nil, api.ErrNotFound
	}
	return vars.Parse(v)
}

// groupTree returns the inventory's group hierarchy.
func groupTree(c *Ctx, invID int64) ([]Obj, error) {
	type g struct {
		id         int64
		name, desc string
	}
	rows, err := c.DB().Query(c, "SELECT id, name, description FROM groups WHERE inventory_id = $1 ORDER BY name", invID)
	if err != nil {
		return nil, err
	}
	var all []g
	for rows.Next() {
		var x g
		rows.Scan(&x.id, &x.name, &x.desc)
		all = append(all, x)
	}
	rows.Close()
	kids := map[int64][]int64{}
	hasParent := map[int64]bool{}
	rows, err = c.DB().Query(c, "SELECT gc.parent_id, gc.child_id FROM group_children gc JOIN groups p ON p.id = gc.parent_id WHERE p.inventory_id = $1", invID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p, ch int64
		rows.Scan(&p, &ch)
		kids[p] = append(kids[p], ch)
		hasParent[ch] = true
	}
	rows.Close()
	byID := map[int64]g{}
	for _, x := range all {
		byID[x.id] = x
	}
	var build func(id int64, seen map[int64]bool) Obj
	build = func(id int64, seen map[int64]bool) Obj {
		x := byID[id]
		node := Obj{"id": id, "name": x.name, "description": x.desc, "url": api.Lookup("groups").URL(id)}
		children := []Obj{}
		if !seen[id] {
			seen[id] = true
			for _, ch := range kids[id] {
				children = append(children, build(ch, seen))
			}
			delete(seen, id)
		}
		node["children"] = children
		return node
	}
	out := []Obj{}
	for _, x := range all {
		if !hasParent[x.id] {
			out = append(out, build(x.id, map[int64]bool{}))
		}
	}
	return out, nil
}
