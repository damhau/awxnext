package resources

import (
	"fmt"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
)

// userVisible: superusers/auditors see everyone; others see themselves and users sharing an organization or team.
func userVisible(c *Ctx, arg ArgFunc) string {
	if c.User.Superuser || c.User.Auditor {
		return "TRUE"
	}
	me := arg(c.User.UserID)
	tm := arg(teams(c))
	return fmt.Sprintf(`t.id = %[1]s OR EXISTS (
		SELECT 1 FROM role_assignments a1 WHERE a1.user_id = t.id AND (
			(a1.content_type = 'organization' AND a1.object_id IN (
				SELECT ra.object_id FROM role_assignments ra WHERE (ra.user_id = %[1]s OR ra.team_id = ANY(%[2]s)) AND ra.content_type = 'organization'
				UNION SELECT tt.organization_id FROM teams tt WHERE tt.id = ANY(%[2]s)))
			OR (a1.content_type = 'team' AND a1.object_id = ANY(%[2]s))))`, me, tm)
}

// canManageUser: superuser, or admin of an organization the target user belongs to (and the target is not a superuser).
func canManageUser(c *Ctx, target Obj) (bool, error) {
	if c.User.Superuser {
		return true, nil
	}
	if boolv(target["is_superuser"]) {
		return false, nil
	}
	id, _ := api.ID(target["id"])
	var ok bool
	err := c.App.DB.QueryRow(c, `SELECT EXISTS (
		SELECT 1 FROM role_assignments member
		WHERE member.user_id = $1 AND member.content_type = 'organization'
		AND member.object_id IN (
			SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
			WHERE (ra.user_id = $2 OR ra.team_id = ANY($3)) AND ra.content_type = 'organization' AND 'change_organization' = ANY(rd.permissions)))`,
		id, c.User.UserID, teams(c)).Scan(&ok)
	return ok, err
}

func registerUsers() {
	api.Register(&Resource{
		Name:         "users",
		Type:         "user",
		Table:        "users",
		Verbose:      "User",
		NoName:       true,
		SearchFields: []string{"username", "first_name", "last_name", "email"},
		Ordering:     "username",
		Fields: []*Field{
			{Name: "username", Type: api.TString, Required: true, MaxLength: 150, Help: "Required. 150 characters or fewer. Letters, digits and @/./+/-/_ only."},
			{Name: "first_name", Type: api.TString, Default: "", MaxLength: 150},
			{Name: "last_name", Type: api.TString, Default: "", MaxLength: 150},
			{Name: "email", Type: api.TString, Default: "", MaxLength: 254, Label: "Email address"},
			{Name: "is_superuser", Type: api.TBool, Default: false, Label: "Superuser status", Help: "Designates that this user has all permissions without explicitly assigning them."},
			{Name: "is_system_auditor", Type: api.TBool, Default: false},
			{Name: "password", Type: api.TString, WriteOnly: true, Default: "", Help: "Field used to change the password.", Validate: func(c *Ctx, v any, data Obj) error {
				if s := str(v); s != "" && len(s) < 1 {
					return fmt.Errorf("Password too short.")
				}
				return nil
			}},
			{Name: "is_active", Type: api.TBool, Hidden: true, Default: true},
			{Name: "external_source", Type: api.TString, ReadOnly: true, Hidden: true},
			{Name: "last_login", Type: api.TDateTime, ReadOnly: true, Nullable: true},
		},
		View: userVisible,
		CanAdd: func(c *Ctx, data Obj) error {
			if boolv(data["is_superuser"]) || boolv(data["is_system_auditor"]) {
				if !c.User.Superuser {
					return api.ErrForbidden
				}
			}
			ok, err := isOrgAdminOfAny(c)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			for _, f := range []string{"is_superuser", "is_system_auditor"} {
				if data[f] != old[f] && !c.User.Superuser {
					return api.Detail(403, "Only superusers may change "+f+".")
				}
			}
			id, _ := api.ID(old["id"])
			if id == c.User.UserID {
				return nil
			}
			ok, err := canManageUser(c, old)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			id, _ := api.ID(obj["id"])
			if id == c.User.UserID {
				return api.Detail(403, "Cannot delete yourself.")
			}
			ok, err := canManageUser(c, obj)
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			return nil
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			if pw, ok := data["password"]; ok {
				s := str(pw)
				if s == "" {
					if old == nil {
						delete(data, "password")
					} else {
						delete(data, "password")
					}
					return nil
				}
				h, err := auth.HashPassword(s)
				if err != nil {
					return err
				}
				data["password"] = h
			}
			return nil
		},
		Serialize: func(c *Ctx, o Obj) {
			o["password"] = "$encrypted$"
			o["ldap_dn"] = ""
			o["external_account"] = nil
			o["auth"] = []any{}
		},
		Capabilities: func(c *Ctx, obj Obj, p map[string]bool) map[string]bool {
			id, _ := api.ID(obj["id"])
			ok, _ := canManageUser(c, obj)
			return map[string]bool{"edit": ok || id == c.User.UserID, "delete": ok && id != c.User.UserID}
		},
		SubLists: []*SubList{
			{
				Name:   "teams",
				Target: "teams",
				Filter: func(arg ArgFunc, uid int64) string {
					return fmt.Sprintf(`t.id IN (SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
						WHERE ra.user_id = %s AND ra.content_type = 'team' AND 'member_team' = ANY(rd.permissions))`, arg(uid))
				},
				ReadOnly: true,
			},
			{
				Name:   "organizations",
				Target: "organizations",
				Filter: func(arg ArgFunc, uid int64) string {
					return fmt.Sprintf(`t.id IN (SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
						WHERE ra.user_id = %s AND ra.content_type = 'organization' AND rd.name = 'Organization Member')`, arg(uid))
				},
				ReadOnly: true,
			},
			{
				Name:   "admin_of_organizations",
				Target: "organizations",
				Filter: func(arg ArgFunc, uid int64) string {
					return fmt.Sprintf(`t.id IN (SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
						WHERE ra.user_id = %s AND ra.content_type = 'organization' AND rd.name = 'Organization Admin')`, arg(uid))
				},
				ReadOnly: true,
			},
			{
				Name:   "credentials",
				Target: "credentials",
				Filter: func(arg ArgFunc, uid int64) string {
					return fmt.Sprintf(`t.id IN (SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
						WHERE ra.user_id = %s AND ra.content_type = 'credential' AND rd.name = 'Credential Admin')`, arg(uid))
				},
				ReadOnly: true,
			},
		},
	})
}
