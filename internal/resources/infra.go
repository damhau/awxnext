package resources

import (
	"context"
	"fmt"
	"net/http"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
)

var pullChoices = labelled("", "---------", "always", "Always pull container before running.", "missing", "Only pull the image if not present before running.", "never", "Never pull container before running.")

// requireCredentialKind validates that a credential FK points at the given kind.
func requireCredentialKind(c *Ctx, field string, v any, kinds ...string) error {
	id, ok := api.ID(v)
	if !ok {
		return nil
	}
	var kind string
	if err := c.DB().QueryRow(c, "SELECT ct.kind FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1", id).Scan(&kind); err != nil {
		return api.FieldError(field, fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
	}
	for _, k := range kinds {
		if k == kind {
			return nil
		}
	}
	return api.FieldError(field, fmt.Sprintf("Credential kind must be %v, not %s.", kinds, kind))
}

// SeedInfrastructure creates the default execution environment and instance groups.
func SeedInfrastructure(ctx context.Context, app *api.App) error {
	if _, err := app.DB.Exec(ctx, `INSERT INTO execution_environments (name, image, managed, pull, description)
		SELECT 'AWX EE (latest)', $1, true, 'missing', 'Default execution environment'
		WHERE NOT EXISTS (SELECT 1 FROM execution_environments WHERE managed)`, app.Cfg.DefaultEEImage); err != nil {
		return err
	}
	for _, g := range []string{"controlplane", "default"} {
		var id int64
		if err := app.DB.QueryRow(ctx, `INSERT INTO instance_groups (name) VALUES ($1)
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, g).Scan(&id); err != nil {
			return err
		}
		if _, err := app.DB.Exec(ctx, `INSERT INTO instance_group_instances (instance_group_id, instance_id)
			SELECT $1, id FROM instances WHERE hostname = $2 AND managed_by_policy ON CONFLICT DO NOTHING`, id, app.Cfg.NodeName); err != nil {
			return err
		}
	}
	return nil
}

func registerInfrastructure() {
	eeView := func(c *Ctx, arg ArgFunc) string {
		if c.User.Superuser || c.User.Auditor {
			return "TRUE"
		}
		res := api.Lookup("execution_environments")
		return "t.organization_id IS NULL OR " + c.User.Predicate(rbacArg(arg), "executionenvironment", "view_executionenvironment", res.Scope("t"))
	}
	ee := api.Register(&Resource{
		Name:        "execution_environments",
		Type:        "execution_environment",
		Table:       "execution_environments",
		Verbose:     "Execution Environment",
		Ordering:    "name",
		ContentType: "executionenvironment",
		OrgColumn:   "organization_id",
		Versioned:   true,
		Copyable:    true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Nullable: true, Help: "The organization used to determine access to this execution environment."},
			{Name: "image", Type: api.TString, Required: true, MaxLength: 1024, Label: "Image location", Help: "The full image location, including the container registry, image name, and version tag."},
			{Name: "managed", Type: api.TBool, ReadOnly: true},
			{Name: "credential", Type: api.TFK, Ref: "credentials", Nullable: true},
			{Name: "pull", Type: api.TChoice, Default: "", Choices: pullChoices, Help: "Pull image before running?"},
		},
		View: eeView,
		CanAdd: func(c *Ctx, data Obj) error {
			if err := requireCredentialKind(c, "credential", data["credential"], "registry"); err != nil {
				return err
			}
			if _, ok := api.ID(data["organization"]); !ok {
				if !c.User.Superuser {
					return api.ErrForbidden
				}
				return nil
			}
			ok, err := api.CanAddIn(c, api.Lookup("execution_environments"), data)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			if boolv(old["managed"]) {
				for _, f := range []string{"name", "image", "organization", "credential", "description"} {
					if fmt.Sprint(old[f]) != fmt.Sprint(data[f]) {
						return api.Detail(403, "Only the pull policy of a managed execution environment can be changed.")
					}
				}
				if !c.User.Superuser {
					return api.ErrForbidden
				}
				return nil
			}
			if err := requireCredentialKind(c, "credential", data["credential"], "registry"); err != nil {
				return err
			}
			if _, ok := api.ID(old["organization"]); !ok {
				if !c.User.Superuser {
					return api.ErrForbidden
				}
				return nil
			}
			ok, err := api.HasPerm(c, api.Lookup("execution_environments"), old, "change")
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if boolv(obj["managed"]) {
				return api.Detail(403, "Managed execution environments cannot be deleted.")
			}
			if _, ok := api.ID(obj["organization"]); !ok {
				if !c.User.Superuser {
					return api.ErrForbidden
				}
				return nil
			}
			ok, err := api.HasPerm(c, api.Lookup("execution_environments"), obj, "delete")
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
	})
	ee.SubLists = []*SubList{readOnlyChildSub("unified_job_templates", "unified_job_templates", "execution_environment_id")}

	ig := api.Register(&Resource{
		Name:        "instance_groups",
		Type:        "instance_group",
		Table:       "instance_groups",
		Verbose:     "Instance Group",
		Ordering:    "name",
		ContentType: "instancegroup",
		NoName:      true,
		Versioned:   true,
		Fields: []*Field{
			{Name: "name", Type: api.TString, Required: true, MaxLength: 250},
			{Name: "is_container_group", Type: api.TBool, Default: false, Help: "Indicates whether instances in this group are containerized. Containerized groups have a designated Openshift or Kubernetes cluster."},
			{Name: "credential", Type: api.TFK, Ref: "credentials", Nullable: true},
			{Name: "policy_instance_percentage", Type: api.TInt, Default: 0, Help: "Minimum percentage of all instances that will be automatically assigned to this group when new instances come online."},
			{Name: "policy_instance_minimum", Type: api.TInt, Default: 0, Help: "Static minimum number of Instances that will be automatically assign to this group when new instances come online."},
			{Name: "policy_instance_list", Type: api.TJSON, Default: []any{}, Help: "List of exact-match Instances that will be assigned to this group"},
			{Name: "pod_spec_override", Type: api.TText, Default: ""},
			{Name: "max_concurrent_jobs", Type: api.TInt, Default: 0, Help: "Maximum number of concurrent jobs to run on a group. When set to zero, no maximum is enforced."},
			{Name: "max_forks", Type: api.TInt, Default: 0, Help: "Maximum number of forks to execute concurrently on a group. When set to zero, no maximum is enforced."},
		},
		SearchFields: []string{"name"},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			pred := c.User.Predicate(rbacArg(arg), "instancegroup", "view_instancegroup", api.Lookup("instance_groups").Scope("t"))
			// Groups assigned to organizations the user belongs to are visible.
			return fmt.Sprintf(`(%s) OR t.id IN (SELECT l.instance_group_id FROM instance_group_links l WHERE l.content_type = 'organization' AND l.object_id IN (
				SELECT ra.object_id FROM role_assignments ra WHERE (ra.user_id = %s OR ra.team_id = ANY(%s)) AND ra.content_type = 'organization'))`,
				pred, arg(c.User.UserID), arg(teams(c)))
		},
		CanAdd: func(c *Ctx, data Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			if boolv(data["is_container_group"]) {
				return requireCredentialKind(c, "credential", data["credential"], "kubernetes")
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			if n := str(obj["name"]); n == "controlplane" || n == "default" {
				return api.Detail(403, "The "+n+" instance group cannot be deleted.")
			}
			if countQuery(c, "SELECT count(*) FROM unified_jobs WHERE instance_group_id = $1 AND status IN ('pending','waiting','running')", obj["id"]) > 0 {
				return api.Detail(409, "Instance group has running jobs.")
			}
			return nil
		},
		Summary: func(c *Ctx, objs []Obj) error {
			for _, o := range objs {
				id := o["id"]
				var capacity, consumed, running, total, instances int64
				c.DB().QueryRow(c, `SELECT coalesce(sum(i.capacity) FILTER (WHERE i.enabled), 0), count(*) FROM instances i
					JOIN instance_group_instances gi ON gi.instance_id = i.id WHERE gi.instance_group_id = $1`, id).Scan(&capacity, &instances)
				c.DB().QueryRow(c, `SELECT coalesce(sum(task_impact) FILTER (WHERE status IN ('waiting','running')), 0),
					count(*) FILTER (WHERE status IN ('waiting','running')), count(*) FROM unified_jobs WHERE instance_group_id = $1`, id).Scan(&consumed, &running, &total)
				pct := 100.0
				if capacity > 0 {
					pct = float64(capacity-consumed) / float64(capacity) * 100
				}
				o["capacity"] = capacity
				o["consumed_capacity"] = consumed
				o["percent_capacity_remaining"] = float64(int(pct*100)) / 100
				o["jobs_running"] = running
				o["jobs_total"] = total
				o["instances"] = instances
			}
			return nil
		},
	})
	ig.SubLists = []*SubList{
		{
			Name:   "instances",
			Target: "instances",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT instance_id FROM instance_group_instances WHERE instance_group_id = %s)", arg(pid))
			},
			NoCreate: true,
			Associate: func(c *Ctx, pid, iid int64) error {
				_, err := c.DB().Exec(c, "INSERT INTO instance_group_instances (instance_group_id, instance_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", pid, iid)
				return err
			},
			Disassociate: func(c *Ctx, pid, iid int64) error {
				_, err := c.DB().Exec(c, "DELETE FROM instance_group_instances WHERE instance_group_id = $1 AND instance_id = $2", pid, iid)
				return err
			},
		},
		readOnlyChildSub("jobs", "unified_jobs", "instance_group_id"),
	}

	inst := api.Register(&Resource{
		Name:         "instances",
		Type:         "instance",
		Table:        "instances",
		Verbose:      "Instance",
		Ordering:     "hostname",
		NoName:       true,
		SearchFields: []string{"hostname"},
		Fields: []*Field{
			{Name: "hostname", Type: api.TString, Required: true, CreateOnly: true, MaxLength: 250},
			{Name: "uuid", Type: api.TString, ReadOnly: true},
			{Name: "ip_address", Type: api.TString, Default: ""},
			{Name: "version", Type: api.TString, ReadOnly: true},
			{Name: "node_type", Type: api.TChoice, Default: "execution", CreateOnly: true, Choices: labelled("control", "Control plane node", "execution", "Execution plane node", "hybrid", "Controller and execution", "hop", "Message-passing node, no execution capability")},
			{Name: "node_state", Type: api.TChoice, ReadOnly: true, Choices: choices("provisioning", "provision-fail", "installed", "ready", "unavailable", "deprovisioning", "deprovision-fail")},
			{Name: "capacity", Type: api.TInt, ReadOnly: true},
			{Name: "capacity_adjustment", Type: api.TFloat, Default: 1.0},
			{Name: "cpu", Type: api.TFloat, ReadOnly: true},
			{Name: "memory", Type: api.TInt, ReadOnly: true},
			{Name: "cpu_capacity", Type: api.TInt, ReadOnly: true},
			{Name: "mem_capacity", Type: api.TInt, ReadOnly: true},
			{Name: "enabled", Type: api.TBool, Default: true},
			{Name: "managed_by_policy", Type: api.TBool, Default: true},
			{Name: "managed", Type: api.TBool, ReadOnly: true},
			{Name: "listener_port", Type: api.TInt, Nullable: true},
			{Name: "agent_runtime", Type: api.TString, ReadOnly: true, Help: "How the agent of a remote execution node runs ansible-runner (podman, docker or none)."},
			{Name: "errors", Type: api.TText, ReadOnly: true, Help: "Any error details from the last health check."},
			{Name: "last_seen", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "last_health_check", Type: api.TDateTime, ReadOnly: true, Nullable: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.id IN (SELECT gi.instance_id FROM instance_group_instances gi WHERE gi.instance_group_id IN (SELECT t.id FROM instance_groups t WHERE " +
				api.Lookup("instance_groups").View(c, arg) + "))"
		},
		CanAdd: func(c *Ctx, data Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			if nt := str(data["node_type"]); nt != "execution" && nt != "hop" {
				return api.FieldError("node_type", "Can only create execution or hop nodes.")
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			if boolv(obj["managed"]) {
				return api.Detail(403, "Control plane instances register themselves and cannot be deleted here.")
			}
			return nil
		},
		Summary: func(c *Ctx, objs []Obj) error {
			for _, o := range objs {
				var consumed, running, total int64
				c.DB().QueryRow(c, `SELECT coalesce(sum(task_impact) FILTER (WHERE status IN ('waiting','running')), 0),
					count(*) FILTER (WHERE status IN ('waiting','running')), count(*)
					FROM unified_jobs WHERE execution_node = $1`, o["hostname"]).Scan(&consumed, &running, &total)
				capacity, _ := api.ID(o["capacity"])
				o["consumed_capacity"] = consumed
				o["jobs_running"] = running
				o["jobs_total"] = total
				pct := 0.0
				if capacity > 0 {
					pct = float64(capacity-consumed) / float64(capacity) * 100
				}
				o["percent_capacity_remaining"] = float64(int(pct*100)) / 100
			}
			return nil
		},
	})
	inst.Actions = append(inst.Actions, &api.Action{Name: "agent_token", Methods: []string{"POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		if !c.User.Superuser {
			api.WriteError(w, api.ErrForbidden)
			return
		}
		if str(obj["node_type"]) != "execution" {
			api.WriteError(w, api.BadRequest("Agent tokens are only issued for execution nodes."))
			return
		}
		tok := agent.NewToken()
		c.DB().Exec(c, "UPDATE instances SET agent_token_hash = $2, node_state = 'installed', modified = now() WHERE id = $1", obj["id"], agent.HashToken(tok))
		api.WriteJSON(w, 201, Obj{"token": tok, "url": c.App.Cfg.BaseURL,
			"command": fmt.Sprintf("awxnext agent --url %s --token %s --runtime podman", c.App.Cfg.BaseURL, tok)})
	}})
	inst.SubLists = []*SubList{
		{
			Name:   "instance_groups",
			Target: "instance_groups",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT instance_group_id FROM instance_group_instances WHERE instance_id = %s)", arg(pid))
			},
			ReadOnly: true,
		},
	}

	api.Register(&Resource{
		Name:     "labels",
		Type:     "label",
		Table:    "labels",
		Verbose:  "Label",
		Ordering: "name",
		NoName:   true,
		Fields: []*Field{
			{Name: "name", Type: api.TString, Required: true, MaxLength: 512},
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true, Help: "Organization this label belongs to."},
		},
		SearchFields: []string{"name"},
		CanAdd: func(c *Ctx, data Obj) error {
			org, _ := api.ID(data["organization"])
			ok, err := c.User.HasOrg(c, c.App.DB, "member_organization", org)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			org, _ := api.ID(old["organization"])
			ok, err := c.User.HasOrg(c, c.App.DB, "change_organization", org)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			org, _ := api.ID(obj["organization"])
			ok, err := c.User.HasOrg(c, c.App.DB, "change_organization", org)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
	})
	// Labels are visible to anyone who can view their organization.
	api.Lookup("labels").View = func(c *Ctx, arg ArgFunc) string {
		if c.User.Superuser || c.User.Auditor {
			return "TRUE"
		}
		return "t.organization_id IN (SELECT t.id FROM organizations t WHERE " + viewOf(c, api.Lookup("organizations"), arg) + ")"
	}
	org := api.Lookup("organizations")
	org.SubLists = append(org.SubLists, readOnlyChildSub("labels", "labels", "organization_id"))
}
