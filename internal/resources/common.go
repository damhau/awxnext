// Package resources defines the AWX API resources on top of the generic engine.
package resources

import (
	"fmt"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

type (
	Obj      = api.Obj
	Ctx      = api.Ctx
	Field    = api.Field
	Resource = api.Resource
	SubList  = api.SubList
	ArgFunc  = api.ArgFunc
)

// choices builds string choices labelled by title-casing.
func choices(vals ...string) []api.Choice {
	out := make([]api.Choice, 0, len(vals))
	for _, v := range vals {
		label := v
		if v != "" {
			label = strings.ToUpper(v[:1]) + strings.ReplaceAll(v[1:], "_", " ")
		}
		out = append(out, api.Choice{Value: v, Label: label})
	}
	return out
}

// labelled builds choices from value/label pairs.
func labelled(pairs ...string) []api.Choice {
	out := make([]api.Choice, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, api.Choice{Value: pairs[i], Label: pairs[i+1]})
	}
	return out
}

func intChoices(pairs ...any) []api.Choice {
	out := make([]api.Choice, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, api.Choice{Value: pairs[i].(int), Label: pairs[i+1].(string)})
	}
	return out
}

var verbosityChoices = intChoices(0, "0 (Normal)", 1, "1 (Verbose)", 2, "2 (More Verbose)", 3, "3 (Debug)", 4, "4 (Connection Debug)", 5, "5 (WinRM Debug)")

// Assign grants a role definition (by name) to a user or team on an object.
func Assign(c *Ctx, roleName string, userID, teamID int64, ct string, objectID int64) error {
	var u, t any
	if userID != 0 {
		u = userID
	}
	if teamID != 0 {
		t = teamID
	}
	var createdBy any
	if !c.User.Anonymous() {
		createdBy = c.User.UserID
	}
	tag, err := c.DB().Exec(c, `INSERT INTO role_assignments (role_definition_id, user_id, team_id, content_type, object_id, created_by_id)
		SELECT id, $1, $2, $3, $4, $5 FROM role_definitions WHERE name = $6 ON CONFLICT DO NOTHING`,
		u, t, ct, objectID, createdBy, roleName)
	if err != nil {
		return err
	}
	_ = tag
	return nil
}

// Unassign removes a role assignment.
func Unassign(c *Ctx, roleName string, userID, teamID int64, ct string, objectID int64) error {
	_, err := c.DB().Exec(c, `DELETE FROM role_assignments WHERE role_definition_id = (SELECT id FROM role_definitions WHERE name = $1)
		AND user_id IS NOT DISTINCT FROM $2 AND team_id IS NOT DISTINCT FROM $3 AND content_type = $4 AND object_id = $5`,
		roleName, nullID(userID), nullID(teamID), ct, objectID)
	return err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// roleHolders is a SQL predicate selecting users (alias t) holding roleName directly on an object.
func roleHolders(roleName, ct string) func(arg ArgFunc, pid int64) string {
	return func(arg ArgFunc, pid int64) string {
		return fmt.Sprintf(`t.id IN (SELECT ra.user_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
			WHERE rd.name = %s AND ra.content_type = %s AND ra.object_id = %s)`, arg(roleName), arg(ct), arg(pid))
	}
}

// roleUserSub exposes the users holding a role on the parent object as an associable sublist.
func roleUserSub(name, roleName, ct string) *SubList {
	return &SubList{
		Name:   name,
		Target: "users",
		Filter: roleHolders(roleName, ct),
		Associate: func(c *Ctx, pid, uid int64) error {
			if err := membershipChange(c, ct); err != nil {
				return err
			}
			return Assign(c, roleName, uid, 0, ct, pid)
		},
		Disassociate: func(c *Ctx, pid, uid int64) error {
			if err := membershipChange(c, ct); err != nil {
				return err
			}
			return Unassign(c, roleName, uid, 0, ct, pid)
		},
	}
}

// membershipChange checks MANAGE_ORGANIZATION_AUTH for roles on
// organizations and teams.
func membershipChange(c *Ctx, ct string) error {
	if ct == "organization" || ct == "team" {
		return manageOrgAuth(c)
	}
	return nil
}

// childSub is a sublist of objects whose column references the parent.
func childSub(name, target, column, parentField string) *SubList {
	return &SubList{
		Name:   name,
		Target: target,
		Filter: func(arg ArgFunc, pid int64) string {
			return fmt.Sprintf("t.%s = %s", column, arg(pid))
		},
		ParentField: parentField,
	}
}

// readOnlyChildSub is a non-writable child sublist.
func readOnlyChildSub(name, target, column string) *SubList {
	s := childSub(name, target, column, "")
	s.ReadOnly = true
	return s
}

// m2mSub is an associable many-to-many sublist backed by a join table.
func m2mSub(name, target, table, parentCol, childCol string) *SubList {
	return &SubList{
		Name:   name,
		Target: target,
		Filter: func(arg ArgFunc, pid int64) string {
			return fmt.Sprintf("t.id IN (SELECT %s FROM %s WHERE %s = %s)", childCol, table, parentCol, arg(pid))
		},
		Associate: func(c *Ctx, pid, cid int64) error {
			_, err := c.DB().Exec(c, fmt.Sprintf("INSERT INTO %s (%s, %s) VALUES ($1, $2) ON CONFLICT DO NOTHING", table, parentCol, childCol), pid, cid)
			return err
		},
		Disassociate: func(c *Ctx, pid, cid int64) error {
			_, err := c.DB().Exec(c, fmt.Sprintf("DELETE FROM %s WHERE %s = $1 AND %s = $2", table, parentCol, childCol), pid, cid)
			return err
		},
	}
}

// orderedLinkSub is an associable sublist on a (content_type, object_id, <child>, position) link table.
func orderedLinkSub(name, target, table, childCol, ct string) *SubList {
	return &SubList{
		Name:   name,
		Target: target,
		Filter: func(arg ArgFunc, pid int64) string {
			return fmt.Sprintf("t.id IN (SELECT %s FROM %s WHERE content_type = %s AND object_id = %s)", childCol, table, arg(ct), arg(pid))
		},
		// Listed in their saved order (instance group preference, Galaxy credential order).
		Order: func(pid int64) string {
			return fmt.Sprintf("(SELECT position FROM %s WHERE content_type = '%s' AND object_id = %d AND %s = t.id)", table, ct, pid, childCol)
		},
		NoCreate: true,
		Associate: func(c *Ctx, pid, cid int64) error {
			_, err := c.DB().Exec(c, fmt.Sprintf(`INSERT INTO %s (content_type, object_id, %s, position)
				VALUES ($1, $2, $3, (SELECT coalesce(max(position) + 1, 0) FROM %s WHERE content_type = $1 AND object_id = $2))
				ON CONFLICT DO NOTHING`, table, childCol, table), ct, pid, cid)
			return err
		},
		Disassociate: func(c *Ctx, pid, cid int64) error {
			_, err := c.DB().Exec(c, fmt.Sprintf("DELETE FROM %s WHERE content_type = $1 AND object_id = $2 AND %s = $3", table, childCol), ct, pid, cid)
			return err
		},
	}
}

// notifiedResources maps the content types notifications attach to to their resources.
var notifiedResources = map[string]string{
	"jobtemplate":         "job_templates",
	"workflowjobtemplate": "workflow_job_templates",
	"project":             "projects",
	"inventorysource":     "inventory_sources",
	"organization":        "organizations",
	"systemjobtemplate":   "system_job_templates",
}

// notificationSubs exposes notification_templates_{started,success,error[,approvals]} sublists.
// Attaching a notification needs change on it too, as in AWX: whoever may
// change a template may not attach every notification they can see.
func notificationSubs(ct string, approvals bool) []*SubList {
	events := []string{"started", "success", "error"}
	if approvals {
		events = append(events, "approvals")
	}
	var out []*SubList
	for _, ev := range events {
		ev := ev
		out = append(out, &SubList{
			Name:   "notification_templates_" + ev,
			Target: "notification_templates",
			Filter: func(arg ArgFunc, pid int64) string {
				return fmt.Sprintf("t.id IN (SELECT notification_template_id FROM notification_template_links WHERE content_type = %s AND object_id = %s AND event = %s)", arg(ct), arg(pid), arg(ev))
			},
			NoCreate: true,
			CanAttach: func(c *Ctx, parent Obj, cid int64) error {
				return allowed(api.HasPermID(c, "notification_templates", cid, "change"))
			},
			Associate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, `INSERT INTO notification_template_links (content_type, object_id, notification_template_id, event) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, ct, pid, cid, ev)
				return err
			},
			Disassociate: func(c *Ctx, pid, cid int64) error {
				_, err := c.DB().Exec(c, `DELETE FROM notification_template_links WHERE content_type = $1 AND object_id = $2 AND notification_template_id = $3 AND event = $4`, ct, pid, cid, ev)
				return err
			},
		})
	}
	return out
}

// isOrgAdminOfAny reports whether the principal is admin of at least one organization.
// manageOrgAuth is MANAGE_ORGANIZATION_AUTH: when off, only superusers
// manage users and teams (create, change, delete them) and organization and
// team roles, as when an identity provider owns membership.
func manageOrgAuth(c *Ctx) error {
	if c.User.Superuser || c.App.Settings.Bool(c, "MANAGE_ORGANIZATION_AUTH") {
		return nil
	}
	return api.Detail(403, "Only superusers manage users, teams, and organization and team roles here (setting MANAGE_ORGANIZATION_AUTH is off).")
}

// allowed turns a permission answer into an error.
func allowed(ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	return nil
}

// orgAdminsSeeAll is ORG_ADMINS_CAN_SEE_ALL_USERS: organization admins see
// every user and team, not only those of their organizations.
func orgAdminsSeeAll(c *Ctx) bool {
	if !c.App.Settings.Bool(c, "ORG_ADMINS_CAN_SEE_ALL_USERS") {
		return false
	}
	ok, _ := isOrgAdminOfAny(c)
	return ok
}

func isOrgAdminOfAny(c *Ctx) (bool, error) {
	if c.User.Superuser {
		return true, nil
	}
	var ok bool
	err := c.App.DB.QueryRow(c, `SELECT EXISTS (SELECT 1 FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
		WHERE (ra.user_id = $1 OR ra.team_id = ANY($2)) AND ra.content_type = 'organization' AND 'change_organization' = ANY(rd.permissions))`,
		c.User.UserID, teams(c)).Scan(&ok)
	return ok, err
}

func teams(c *Ctx) []int64 {
	if c.User.Teams == nil {
		return []int64{}
	}
	return c.User.Teams
}

func countQuery(c *Ctx, sql string, args ...any) int64 {
	var n int64
	c.DB().QueryRow(c, sql, args...).Scan(&n)
	return n
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func boolv(v any) bool {
	b, _ := v.(bool)
	return b
}

func rbacArg(a ArgFunc) rbac.ArgFunc { return rbac.ArgFunc(a) }

// requireUnmanaged rejects changes to children of an object owned by an
// external source (workflow nodes, hosts and groups, surveys).
func requireUnmanaged(c *Ctx, resName string, id any) error {
	res := api.Lookup(resName)
	pid, ok := api.ID(id)
	if res == nil || !ok {
		return nil
	}
	var owner string
	c.DB().QueryRow(c, "SELECT managed_by FROM "+res.Table+" WHERE id = $1", pid).Scan(&owner)
	return api.CheckManaged(c, res, Obj{"managed_by": owner})
}
