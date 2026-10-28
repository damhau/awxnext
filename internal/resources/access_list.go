package resources

import (
	"fmt"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

// legacyRoleOfAction maps a permission's action to the AWX role field that
// carries it (AWX's to_permissions, reversed).
var legacyRoleOfAction = map[string]string{"view": "read_role", "change": "admin_role", "execute": "execute_role", "use": "use_role",
	"update": "update_role", "adhoc": "adhoc_role", "approve": "approval_role", "member": "member_role", "audit": "auditor_role"}

// orgRoleOfAdd maps an organization's add_ permissions to its AWX role field
// (AWX's org_role_to_permission, reversed).
var orgRoleOfAdd = map[string]string{"add_project": "project_admin_role", "add_inventory": "inventory_admin_role",
	"add_credential": "credential_admin_role", "add_jobtemplate": "job_template_admin_role",
	"add_workflowjobtemplate": "workflow_admin_role", "add_notificationtemplate": "notification_admin_role",
	"add_executionenvironment": "execution_environment_admin_role"}

// descendantRoles are the role fields of an object of type ct that a role
// definition's permissions amount to.
func descendantRoles(ct string, perms []string) []string {
	seen := map[string]bool{}
	for _, p := range perms {
		if ct == "organization" && orgRoleOfAdd[p] != "" {
			seen[orgRoleOfAdd[p]] = true
		}
		action, model, ok := strings.Cut(p, "_")
		if !ok || model != ct {
			continue
		}
		if f := legacyRoleOfAction[action]; f != "" {
			seen[f] = true
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// accessDetails adds AWX's summary_fields.direct_access and indirect_access
// to the users of an object's access list: each role that gives the user
// access, where it comes from (the object itself, a team, its organization
// or folder, or the system), and whether the requester can remove it.
func accessDetails(c *Ctx, res *Resource, obj Obj, users []Obj) error {
	ct := res.ContentType
	objID, _ := api.ID(obj["id"])
	canChange := func(rct string, rid int64) bool {
		if c.User.Superuser {
			return true
		}
		target := resourceForCT(rct)
		if target == nil {
			return false
		}
		ok, _ := api.HasPermID(c, target.Name, rid, "change")
		return ok
	}
	for _, u := range users {
		uid, _ := api.ID(u["id"])
		var args []any
		arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
		q := fmt.Sprintf(`SELECT ra.team_id, coalesce(ra.content_type, ''), coalesce(ra.object_id, 0), rd.id, rd.name, rd.description, rd.permissions
			FROM %s o JOIN role_assignments ra ON (%s) JOIN role_definitions rd ON rd.id = ra.role_definition_id
			WHERE o.id = %s AND (ra.user_id = %s OR ra.team_id IN (SELECT user_teams(%s))) ORDER BY ra.id`,
			res.Table, rbac.ScopeSQL(rbac.ArgFunc(arg), ct, res.Scope("o")), arg(objID), arg(uid), arg(uid))
		type grant struct {
			team           *int64
			rct            string
			rid, rdID      int64
			rdName, rdDesc string
			perms          []string
		}
		var grants []grant
		rows, err := c.DB().Query(c, q, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g grant
			if err := rows.Scan(&g.team, &g.rct, &g.rid, &g.rdID, &g.rdName, &g.rdDesc, &g.perms); err != nil {
				rows.Close()
				return err
			}
			grants = append(grants, g)
		}
		rows.Close()
		direct, indirect := []Obj{}, []Obj{}
		for _, g := range grants {
			role := Obj{"id": nil, "name": g.rdName, "description": g.rdDesc, "user_capabilities": Obj{"unattach": false}}
			if g.rct != "" {
				var roleID int64
				var field string
				if c.DB().QueryRow(c, `SELECT id, role_field FROM object_roles WHERE content_type = $1 AND object_id = $2 AND role_definition_id = $3`,
					g.rct, g.rid, g.rdID).Scan(&roleID, &field) == nil {
					role["id"], role["name"] = roleID, api.LegacyRoleName(field)
				}
				typ := ctType(g.rct)
				role["resource_name"], role["resource_type"] = contentObjectName(c, g.rct, g.rid), typ
				if target := resourceForCT(g.rct); target != nil {
					role["related"] = Obj{typ: target.URL(g.rid)}
				}
				role["user_capabilities"] = Obj{"unattach": canChange(g.rct, g.rid)}
			}
			if g.team != nil {
				var teamName, orgName string
				c.DB().QueryRow(c, `SELECT t.name, coalesce(o.name, '') FROM teams t LEFT JOIN organizations o ON o.id = t.organization_id WHERE t.id = $1`,
					*g.team).Scan(&teamName, &orgName)
				role["team_id"], role["team_name"], role["team_organization_name"] = *g.team, teamName, orgName
			}
			entry := Obj{"role": role, "descendant_roles": descendantRoles(ct, g.perms)}
			if g.rct == ct && g.rid == objID {
				direct = append(direct, entry)
			} else {
				indirect = append(indirect, entry)
			}
		}
		if boolv(u["is_superuser"]) {
			var all []string
			for _, m := range rbac.LegacyRoles(ct) {
				all = append(all, m.LegacyField)
			}
			indirect = append(indirect, Obj{"role": Obj{"id": nil, "name": "System Administrator", "description": "Can manage all aspects of the system",
				"user_capabilities": Obj{"unattach": false}}, "descendant_roles": all})
		}
		sf, _ := u["summary_fields"].(Obj)
		if sf == nil {
			sf = Obj{}
			u["summary_fields"] = sf
		}
		sf["direct_access"], sf["indirect_access"] = direct, indirect
	}
	return nil
}
