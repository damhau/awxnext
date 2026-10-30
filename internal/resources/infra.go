package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/capacity"
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
	// AWX's installer registers both with every instance at each start
	// (register_queue --instance_percent=100).
	for _, g := range []string{"controlplane", "default"} {
		if _, err := app.DB.Exec(ctx, `INSERT INTO instance_groups (name, policy_instance_percentage) VALUES ($1, 100)
			ON CONFLICT (name) DO UPDATE SET policy_instance_percentage = 100`, g); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error { return ApplyMembershipPolicies(ctx, tx) })
}

// installerGroup reports whether a group is one of the two the installer
// creates, whose names and policies AWX does not let the API change.
func installerGroup(name string) bool { return name == "controlplane" || name == "default" }

// recomputeMembership applies the membership policies after a change to
// an instance or a group.
func recomputeMembership(c *Ctx) error {
	return c.WithTx(func(c *Ctx) error { return ApplyMembershipPolicies(c, c.DB()) })
}

// adjustment is the DecimalField(max_digits=3, decimal_places=2) of AWX's
// capacity_adjustment, at least 0.
func adjustment(c *Ctx, v any, data Obj) error {
	f, _ := v.(float64)
	if f < 0 {
		return errors.New("Ensure this value is greater than or equal to 0.")
	}
	whole, frac, _ := strings.Cut(strconv.FormatFloat(f, 'f', -1, 64), ".")
	whole = strings.TrimLeft(whole, "0")
	switch {
	case len(whole)+len(frac) > 3:
		return errors.New("Ensure that there are no more than 3 digits in total.")
	case len(frac) > 2:
		return errors.New("Ensure that there are no more than 2 decimal places.")
	case len(whole) > 1:
		return errors.New("Ensure that there are no more than 1 digits before the decimal point.")
	}
	return nil
}

func percentage(c *Ctx, v any, data Obj) error {
	if n, _ := api.ID(v); n > 100 {
		return errors.New("Ensure this value is less than or equal to 100.")
	}
	return atLeast(0)(c, v, data)
}

func atLeast(min int64) func(c *Ctx, v any, data Obj) error {
	return func(c *Ctx, v any, data Obj) error {
		if n, _ := api.ID(v); n < min {
			return fmt.Errorf("Ensure this value is greater than or equal to %d.", min)
		}
		return nil
	}
}

// policyList is AWX's validate_policy_instance_list without the checks
// against the stored group, which are in checkGroupPolicy.
func policyList(c *Ctx, v any, data Obj) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("Expected a list of items but got type %q.", pyType(v))
	}
	seen := map[string]bool{}
	for _, h := range list {
		host, ok := h.(string)
		if !ok {
			return errors.New("Not a valid string.")
		}
		if seen[host] {
			return fmt.Errorf("Duplicate entry %s.", host)
		}
		seen[host] = true
		if countQuery(c, "SELECT count(*) FROM instances WHERE hostname = $1", host) == 0 {
			return fmt.Errorf("%s is not a valid hostname of an existing instance.", host)
		}
	}
	return nil
}

func pyType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "dict"
	case string:
		return "str"
	case bool:
		return "bool"
	case float64, int64, int:
		return "int"
	case nil:
		return "NoneType"
	}
	return fmt.Sprintf("%T", v)
}

// checkGroupPolicy is the part of AWX's instance group validation that
// depends on the stored group: the installer's groups keep their name,
// kind and policies, and container groups take no policy.
func checkGroupPolicy(c *Ctx, old, data Obj) error {
	if old == nil {
		return nil
	}
	name := str(old["name"])
	fe := api.FieldErrors{}
	changed := func(f string) bool {
		v, ok := data[f]
		if !ok {
			return false
		}
		a, _ := json.Marshal(v)
		b, _ := json.Marshal(old[f])
		return string(a) != string(b)
	}
	set := func(f string) bool {
		v, ok := data[f]
		if !ok {
			return false
		}
		if l, isList := v.([]any); isList {
			return len(l) > 0
		}
		n, _ := api.ID(v)
		return n != 0
	}
	container := boolv(old["is_container_group"])
	if installerGroup(name) {
		if changed("name") {
			fe.Add("name", name+" instance group name may not be changed.")
		}
		if changed("is_container_group") {
			fe.Add("is_container_group", name+" instance group is_container_group may not be changed.")
		}
		if changed("policy_instance_list") {
			fe.Add("policy_instance_list", name+" instance group policy_instance_list may not be changed.")
		}
		if changed("policy_instance_percentage") {
			fe.Add("policy_instance_percentage", name+" instance group policy_instance_percentage may not be changed from the initial value set by the installer.")
		}
	}
	for _, f := range []string{"policy_instance_list", "policy_instance_percentage", "policy_instance_minimum"} {
		if container && set(f) && len(fe[f]) == 0 {
			fe.Add(f, "Containerized instances may not be managed via the API")
		}
	}
	return fe.Err()
}

// changeMembership is AWX's InstanceGroupMembershipMixin: attaching an
// instance lists it in the group's policy_instance_list, so the policies
// keep it; detaching unlists it. The policies are then applied, which may
// put a detached instance back when the group's minimum or percentage
// still selects it. instanceSide picks the words of the instance's own
// list of groups.
func changeMembership(c *Ctx, gid, iid int64, add, instanceSide bool) error {
	var host, kind, group string
	if err := c.DB().QueryRow(c, "SELECT hostname, node_type FROM instances WHERE id = $1", iid).Scan(&host, &kind); err != nil {
		return err
	}
	if err := c.DB().QueryRow(c, "SELECT name FROM instance_groups WHERE id = $1", gid).Scan(&group); err != nil {
		return err
	}
	refuse := func(msg string) error { return &api.Error{Status: 400, Body: Obj{"msg": msg}} }
	switch {
	case kind == "control":
		return refuse(fmt.Sprintf("Cannot change instance group membership of control-only node: %s.", host))
	case kind == "hop":
		return refuse(fmt.Sprintf("Cannot change instance group membership of hop node : %s.", host))
	case !add && kind == "hybrid" && group == "controlplane" && instanceSide:
		return refuse(fmt.Sprintf("Cannot disassociate hybrid instance %s from %s.", host, group))
	case !add && kind == "hybrid" && group == "controlplane":
		return refuse(fmt.Sprintf("Cannot disassociate hybrid node %s from %s.", host, group))
	}
	if add {
		if _, err := c.DB().Exec(c, "INSERT INTO instance_group_instances (instance_group_id, instance_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", gid, iid); err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, `UPDATE instance_groups SET policy_instance_list = policy_instance_list || to_jsonb($2::text)
			WHERE id = $1 AND NOT policy_instance_list ? $2`, gid, host); err != nil {
			return err
		}
	} else {
		if _, err := c.DB().Exec(c, "DELETE FROM instance_group_instances WHERE instance_group_id = $1 AND instance_id = $2", gid, iid); err != nil {
			return err
		}
		if _, err := c.DB().Exec(c, "UPDATE instance_groups SET policy_instance_list = policy_instance_list - $2::text WHERE id = $1", gid, host); err != nil {
			return err
		}
	}
	return ApplyMembershipPolicies(c, c.DB())
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
			{Name: "policy_instance_percentage", Type: api.TInt, Default: 0, Validate: percentage, Help: "Minimum percentage of all instances that will be automatically assigned to this group when new instances come online."},
			{Name: "policy_instance_minimum", Type: api.TInt, Default: 0, Validate: atLeast(0), Help: "Static minimum number of Instances that will be automatically assign to this group when new instances come online."},
			{Name: "policy_instance_list", Type: api.TJSON, Default: []any{}, Validate: policyList, Help: "List of exact-match Instances that will be assigned to this group"},
			{Name: "pod_spec_override", Type: api.TText, Default: ""},
			{Name: "max_concurrent_jobs", Type: api.TInt, Default: 0, Validate: atLeast(0), Help: "Maximum number of concurrent jobs to run on a group. When set to zero, no maximum is enforced."},
			{Name: "max_forks", Type: api.TInt, Default: 0, Validate: atLeast(0), Help: "Maximum number of forks to execute concurrently on a group. When set to zero, no maximum is enforced."},
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
		BeforeSave: checkGroupPolicy,
		AfterCreate: func(c *Ctx, obj Obj) error {
			if boolv(obj["is_container_group"]) {
				return nil
			}
			return recomputeMembership(c)
		},
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			for _, f := range []string{"policy_instance_list", "policy_instance_minimum", "policy_instance_percentage"} {
				if fmt.Sprint(old[f]) != fmt.Sprint(obj[f]) && !boolv(obj["is_container_group"]) {
					return recomputeMembership(c)
				}
			}
			return nil
		},
		AfterDelete: func(c *Ctx, obj Obj) error {
			if boolv(obj["is_container_group"]) {
				return nil
			}
			return recomputeMembership(c)
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
			NoCreate:     true,
			Associate:    func(c *Ctx, pid, iid int64) error { return changeMembership(c, pid, iid, true, false) },
			Disassociate: func(c *Ctx, pid, iid int64) error { return changeMembership(c, pid, iid, false, false) },
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
			{Name: "capacity_adjustment", Type: api.TFloat, Default: 1.0, Validate: adjustment},
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
			{Name: "health_check_started", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "The last time a health check was initiated on this instance."},
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
		// AWX applies the policies when an instance joins or leaves, and when
		// its POLICY_FIELDS change.
		AfterCreate: func(c *Ctx, obj Obj) error { return recomputeMembership(c) },
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			// AWX's InstanceDetail.update: set_capacity_value.
			if fmt.Sprint(old["capacity_adjustment"]) != fmt.Sprint(obj["capacity_adjustment"]) || fmt.Sprint(old["enabled"]) != fmt.Sprint(obj["enabled"]) {
				var forks int64
				if err := c.DB().QueryRow(c, "UPDATE instances SET capacity = "+capacity.SQL("cpu_capacity", "mem_capacity")+" WHERE id = $1 RETURNING capacity",
					obj["id"]).Scan(&forks); err != nil {
					return err
				}
				obj["capacity"] = forks
			}
			for _, f := range []string{"managed_by_policy", "hostname", "capacity_adjustment"} {
				if fmt.Sprint(old[f]) != fmt.Sprint(obj[f]) {
					return recomputeMembership(c)
				}
			}
			return nil
		},
		AfterDelete: func(c *Ctx, obj Obj) error { return recomputeMembership(c) },
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
				started, serr := time.Parse(time.RFC3339Nano, str(o["health_check_started"]))
				last, lerr := time.Parse(time.RFC3339Nano, str(o["last_health_check"]))
				o["health_check_pending"] = serr == nil && (lerr != nil || started.After(last))
			}
			return nil
		},
	})
	inst.Actions = append(inst.Actions, &api.Action{Name: "health_check", Methods: []string{"GET", "POST"}, Handler: instanceHealthCheck})
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
			NoCreate:     true,
			Associate:    func(c *Ctx, pid, gid int64) error { return changeMembership(c, gid, pid, true, true) },
			Disassociate: func(c *Ctx, pid, gid int64) error { return changeMembership(c, gid, pid, false, true) },
		},
	}

	api.Register(&Resource{
		Name:     "labels",
		Type:     "label",
		Table:    "labels",
		Verbose:  "Label",
		Ordering: "name",
		Managed:  true,
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
