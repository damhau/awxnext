package resources

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

// resourceForCT returns the API resource for an RBAC content type.
func resourceForCT(ct string) *Resource {
	for _, r := range api.Resources() {
		if r.ContentType == ct {
			return r
		}
	}
	return nil
}

// objectScope returns the organization and folder of an RBAC object.
func objectScope(c *Ctx, ct string, objectID int64) (org, folder *int64, err error) {
	res := resourceForCT(ct)
	if res == nil {
		return nil, nil, nil
	}
	obj, err := api.Get(c, res, objectID)
	if err != nil {
		return nil, nil, api.ErrNotFound
	}
	org, folder = res.ObjScope(obj)
	return org, folder, nil
}

// canGrant: the requester must hold every permission of the role definition on the object.
func canGrant(c *Ctx, rdID int64, ct string, objectID int64) error {
	if c.User.Superuser {
		return nil
	}
	if err := membershipChange(c, ct); err != nil {
		return err
	}
	var perms []string
	if err := c.DB().QueryRow(c, "SELECT permissions FROM role_definitions WHERE id = $1", rdID).Scan(&perms); err != nil {
		return api.ErrNotFound
	}
	if ct == "" {
		return api.ErrForbidden
	}
	org, folder, err := objectScope(c, ct, objectID)
	if err != nil {
		return err
	}
	for _, p := range perms {
		ok, err := c.User.Has(c, c.App.DB, ct, p, objectID, org, folder)
		if err != nil {
			return err
		}
		if !ok {
			return api.ErrForbidden
		}
	}
	return nil
}

type objectRole struct {
	ID       int64
	CT       string
	ObjectID int64
	Field    string
	RDID     int64
	RDName   string
}

func loadObjectRole(c *Ctx, id int64) (*objectRole, error) {
	r := &objectRole{ID: id}
	err := c.DB().QueryRow(c, `SELECT o.content_type, o.object_id, o.role_field, o.role_definition_id, rd.name
		FROM object_roles o JOIN role_definitions rd ON rd.id = o.role_definition_id WHERE o.id = $1`, id).
		Scan(&r.CT, &r.ObjectID, &r.Field, &r.RDID, &r.RDName)
	if err != nil {
		return nil, api.ErrNotFound
	}
	return r, nil
}

func assignObjectRole(c *Ctx, roleID, userID, teamID int64, remove bool) error {
	r, err := loadObjectRole(c, roleID)
	if err != nil {
		return err
	}
	if teamID != 0 && r.CT == "team" && r.ObjectID == teamID {
		return api.BadRequest("A team cannot be granted a role on itself.")
	}
	if err := canGrant(c, r.RDID, r.CT, r.ObjectID); err != nil {
		return err
	}
	if remove {
		return Unassign(c, r.RDName, userID, teamID, r.CT, r.ObjectID)
	}
	return Assign(c, r.RDName, userID, teamID, r.CT, r.ObjectID)
}

// roleVisible: roles on objects the principal can view.
func roleVisible(c *Ctx, arg ArgFunc) string {
	if c.User.Superuser || c.User.Auditor {
		return "TRUE"
	}
	var parts []string
	seen := map[string]bool{}
	for _, res := range api.Resources() {
		if res.ContentType == "" || seen[res.ContentType] || len(rbac.LegacyRoles(res.ContentType)) == 0 {
			continue
		}
		seen[res.ContentType] = true
		pred := c.User.Predicate(rbac.ArgFunc(arg), res.ContentType, "view_"+res.ContentType, res.Scope("t"))
		parts = append(parts, fmt.Sprintf("(t.content_type = %s AND t.object_id IN (SELECT t.id FROM %s t WHERE %s))", arg(res.ContentType), res.Table, pred))
	}
	if len(parts) == 0 {
		return "FALSE"
	}
	return strings.Join(parts, " OR ")
}

// assignmentPredicate selects role rows (alias t) matching assignments of a principal column.
func assignmentPredicate(column string) func(arg ArgFunc, pid int64) string {
	return func(arg ArgFunc, pid int64) string {
		return fmt.Sprintf(`EXISTS (SELECT 1 FROM role_assignments ra WHERE ra.%s = %s AND ra.role_definition_id = t.role_definition_id
			AND ra.content_type = t.content_type AND ra.object_id = t.object_id)`, column, arg(pid))
	}
}

func ctLabel(ct string) string {
	switch ct {
	case "jobtemplate":
		return "Job Template"
	case "workflowjobtemplate":
		return "Workflow Job Template"
	case "executionenvironment":
		return "Execution Environment"
	case "instancegroup":
		return "Instance Group"
	case "notificationtemplate":
		return "Notification Template"
	}
	return strings.ToUpper(ct[:1]) + ct[1:]
}

func ctType(ct string) string {
	if res := resourceForCT(ct); res != nil {
		return res.Type
	}
	return ct
}

// contentObjectSummaries loads id/name summaries for (content_type, object_id) pairs.
func contentObjectName(c *Ctx, ct string, id int64) string {
	res := resourceForCT(ct)
	if res == nil {
		return ""
	}
	var name string
	c.DB().QueryRow(c, fmt.Sprintf("SELECT name FROM %s WHERE id = $1", res.Table), id).Scan(&name)
	return name
}

func registerRoles() {
	roles := api.Register(&Resource{
		Name:         "roles",
		Type:         "role",
		Table:        "object_roles",
		Verbose:      "Role",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		// As AWX's: the role's field, and the object it is on.
		ActivitySummary: func(c *Ctx, obj Obj) Obj {
			ct := str(obj["content_type"])
			oid, _ := api.ID(obj["object_id"])
			s := Obj{"id": obj["id"], "role_field": obj["role_field"], "resource_type": nil, "resource_id": nil, "resource_name": nil}
			if res := resourceForCT(ct); res != nil {
				s["resource_type"], s["resource_id"], s["resource_name"] = res.Type, oid, contentObjectName(c, ct, oid)
			}
			return s
		},
		Fields: []*Field{
			{Name: "content_type", Type: api.TString, ReadOnly: true, Hidden: true},
			{Name: "object_id", Type: api.TInt, ReadOnly: true, Hidden: true},
			{Name: "role_field", Type: api.TString, ReadOnly: true, Hidden: true},
			// Not a link: awxkit takes a role's object from its first related link
			// other than users and teams, and AWX's roles link nothing else.
			{Name: "role_definition", Type: api.TInt, Column: "role_definition_id", ReadOnly: true, Hidden: true},
		},
		SearchFields: []string{"role_field", "content_type"},
		View:         roleVisible,
	})
	roles.Summary = func(c *Ctx, objs []Obj) error {
		for _, o := range objs {
			id, _ := api.ID(o["id"])
			r, err := loadObjectRole(c, id)
			if err != nil {
				continue
			}
			var desc string
			c.DB().QueryRow(c, "SELECT description FROM role_definitions WHERE id = $1", r.RDID).Scan(&desc)
			o["name"] = api.LegacyRoleName(r.Field)
			o["description"] = desc
			sf := Obj{
				"resource_name":              contentObjectName(c, r.CT, r.ObjectID),
				"resource_type":              ctType(r.CT),
				"resource_type_display_name": ctLabel(r.CT),
				"resource_id":                r.ObjectID,
			}
			o["summary_fields"] = sf
			rel := o["related"].(Obj)
			if res := resourceForCT(r.CT); res != nil {
				rel[res.Type] = res.URL(r.ObjectID)
			}
		}
		return nil
	}
	roles.SubLists = []*SubList{
		{
			Name:   "users",
			Target: "users",
			Filter: func(arg ArgFunc, rid int64) string {
				return fmt.Sprintf(`t.id IN (SELECT ra.user_id FROM role_assignments ra JOIN object_roles o ON o.role_definition_id = ra.role_definition_id
					AND o.content_type = ra.content_type AND o.object_id = ra.object_id WHERE o.id = %s)`, arg(rid))
			},
			NoCreate:     true,
			AttachPerm:   "none",
			Associate:    func(c *Ctx, rid, uid int64) error { return assignObjectRole(c, rid, uid, 0, false) },
			Disassociate: func(c *Ctx, rid, uid int64) error { return assignObjectRole(c, rid, uid, 0, true) },
		},
		{
			Name:   "teams",
			Target: "teams",
			Filter: func(arg ArgFunc, rid int64) string {
				return fmt.Sprintf(`t.id IN (SELECT ra.team_id FROM role_assignments ra JOIN object_roles o ON o.role_definition_id = ra.role_definition_id
					AND o.content_type = ra.content_type AND o.object_id = ra.object_id WHERE o.id = %s)`, arg(rid))
			},
			NoCreate:     true,
			AttachPerm:   "none",
			Associate:    func(c *Ctx, rid, tid int64) error { return assignObjectRole(c, rid, 0, tid, false) },
			Disassociate: func(c *Ctx, rid, tid int64) error { return assignObjectRole(c, rid, 0, tid, true) },
		},
	}

	principalRoles := func(column string, isTeam bool) *SubList {
		return &SubList{
			Name:       "roles",
			Target:     "roles",
			Filter:     assignmentPredicate(column),
			NoCreate:   true,
			AttachPerm: "none",
			Associate: func(c *Ctx, pid, rid int64) error {
				if isTeam {
					return assignObjectRole(c, rid, 0, pid, false)
				}
				return assignObjectRole(c, rid, pid, 0, false)
			},
			Disassociate: func(c *Ctx, pid, rid int64) error {
				if isTeam {
					return assignObjectRole(c, rid, 0, pid, true)
				}
				return assignObjectRole(c, rid, pid, 0, true)
			},
		}
	}
	users := api.Lookup("users")
	users.SubLists = append(users.SubLists, principalRoles("user_id", false))
	teamsRes := api.Lookup("teams")
	teamsRes.SubLists = append(teamsRes.SubLists, principalRoles("team_id", true))
	// /teams/N/object_roles/ is generic; /teams/N/roles/ lists roles granted to the team.

	api.SetObjectRoleHandler("object_roles", func(c *Ctx, w http.ResponseWriter, r *http.Request, res *Resource, obj Obj) {
		id, _ := api.ID(obj["id"])
		api.WriteList(c, w, r, roles, api.ListParams{SkipView: true, Extra: []func(ArgFunc) string{func(arg ArgFunc) string {
			return fmt.Sprintf("t.content_type = %s AND t.object_id = %s", arg(res.ContentType), arg(id))
		}}})
	})
	api.SetObjectRoleHandler("access_list", func(c *Ctx, w http.ResponseWriter, r *http.Request, res *Resource, obj Obj) {
		id, _ := api.ID(obj["id"])
		api.WriteList(c, w, r, users, api.ListParams{Extra: []func(ArgFunc) string{func(arg ArgFunc) string {
			return fmt.Sprintf(`t.is_superuser OR EXISTS (SELECT 1 FROM %s o JOIN role_assignments ra ON (%s)
				WHERE o.id = %s AND (ra.user_id = t.id OR ra.team_id IN (SELECT user_teams(t.id))))`,
				res.Table, rbac.ScopeSQL(rbac.ArgFunc(arg), res.ContentType, res.Scope("o")), arg(id))
		}}, Decorate: func(out []Obj) error { return accessDetails(c, res, obj, out) }})
	})

	registerDAB()
}

// --- django-ansible-base style RBAC endpoints ---

func dabCT(ct any) any {
	if s, ok := ct.(string); ok && s != "" {
		if s == "team" || s == "organization" {
			return "shared." + s
		}
		return "awx." + s
	}
	return nil
}

// dabPerm writes a permission as django-ansible-base does, prefixed with its
// model's app: shared.view_organization, awx.view_credential.
func dabPerm(p string) string {
	if _, model, ok := strings.Cut(p, "_"); ok && (model == "team" || model == "organization") {
		return "shared." + p
	}
	return "awx." + p
}

// sharedTables are the shared resource types, which have an ansible_id.
var sharedTables = map[string]string{"organization": "organizations", "team": "teams"}

// ansibleID is the ansible_id of a user, team or organization (nil otherwise).
func ansibleID(c *Ctx, table string, id any) any {
	if table == "" {
		return nil
	}
	var a string
	if c.DB().QueryRow(c, "SELECT ansible_id::text FROM "+table+" WHERE id = $1", id).Scan(&a) != nil {
		return nil
	}
	return a
}

// byAnsibleID resolves an ansible_id to the id of a user, team or organization.
func byAnsibleID(c *Ctx, table, field string, v any) (int64, error) {
	var id int64
	if err := c.DB().QueryRow(c, "SELECT id FROM "+table+" WHERE ansible_id::text = $1", str(v)).Scan(&id); err != nil {
		return 0, api.FieldError(field, fmt.Sprintf("Object with ansible_id=%s does not exist.", str(v)))
	}
	return id, nil
}

func stripCT(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// allowedPerms is what a role definition of a content type may grant, as
// role_metadata/ lists it: the permissions of the managed roles of that type
// (an organization's or folder's include those of the objects inside them).
func allowedPerms(ct string) map[string]bool {
	out := map[string]bool{}
	for _, m := range rbac.Managed {
		if m.ContentType == ct {
			for _, p := range m.Permissions {
				out[p] = true
			}
		}
	}
	return out
}

// permModel is the model a permission is about: view_jobtemplate -> jobtemplate.
func permModel(p string) string {
	if i := strings.IndexByte(p, '_'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// checkRolePerms applies django-ansible-base's rules to a custom role
// definition: its permissions are allowed for its content type, and it can
// view every kind of object it grants anything else on (adding excepted).
func checkRolePerms(ct string, perms []string) error {
	if ct != "" {
		allowed := allowedPerms(ct)
		for _, p := range perms {
			if !allowed[p] {
				return api.FieldError("permissions", fmt.Sprintf("Permission %q is not valid for content type %q.", dabPerm(p), str(dabCT(ct))))
			}
		}
	}
	has := map[string]bool{}
	for _, p := range perms {
		has[p] = true
	}
	var missing []string
	seen := map[string]bool{}
	for _, p := range perms {
		m := permModel(p)
		if strings.HasPrefix(p, "add_") || seen[m] {
			continue
		}
		seen[m] = true
		if !has["view_"+m] {
			missing = append(missing, dabPerm("view_"+m))
		}
	}
	if len(missing) > 0 {
		return api.FieldError("permissions", fmt.Sprintf("Permissions also need %s: a role that grants something on an object must let it be seen.", strings.Join(missing, ", ")))
	}
	return nil
}

func validPerm(p string) bool {
	for _, m := range rbac.Managed {
		for _, x := range m.Permissions {
			if x == p {
				return true
			}
		}
	}
	return false
}

func registerDAB() {
	api.Register(&Resource{
		Name:     "role_definitions",
		Type:     "role_definition",
		Table:    "role_definitions",
		Verbose:  "Role Definition",
		Ordering: "id",
		Fields: []*Field{
			{Name: "content_type", Type: api.TString, Nullable: true, Help: "The type of resource this applies to."},
			{Name: "permissions", Type: api.TJSON, Required: true},
			{Name: "managed", Type: api.TBool, ReadOnly: true},
		},
		CanAdd: func(c *Ctx, data Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			return nil
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			if !c.User.Superuser || boolv(old["managed"]) {
				return api.Detail(403, "Managed role definitions cannot be modified.")
			}
			return nil
		},
		CanDelete: func(c *Ctx, obj Obj) error {
			if !c.User.Superuser || boolv(obj["managed"]) {
				return api.Detail(403, "Managed role definitions cannot be deleted.")
			}
			return nil
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			if ct, ok := data["content_type"]; ok && ct != nil {
				data["content_type"] = stripCT(str(ct))
			}
			if p, ok := data["permissions"]; ok {
				list, ok := p.([]any)
				if !ok {
					return api.FieldError("permissions", "Expected a list of permissions.")
				}
				var perms []string
				for _, x := range list {
					s := stripCT(str(x))
					if !validPerm(s) {
						return api.FieldError("permissions", fmt.Sprintf("Invalid permission %q.", str(x)))
					}
					perms = append(perms, s)
				}
				data["permissions"] = perms
			}
			// The rules apply to the definition as it will be saved.
			ct := str(old["content_type"])
			if v, ok := data["content_type"]; ok {
				ct = str(v)
			}
			perms, _ := data["permissions"].([]string)
			if perms == nil {
				if _, given := data["permissions"]; !given && old != nil {
					if l, ok := old["permissions"].([]any); ok {
						for _, p := range l {
							perms = append(perms, stripCT(str(p)))
						}
					}
				}
			}
			if _, ctGiven := data["content_type"]; ctGiven || data["permissions"] != nil {
				if err := checkRolePerms(ct, perms); err != nil {
					return err
				}
			}
			return nil
		},
		Serialize: func(c *Ctx, o Obj) {
			o["content_type"] = dabCT(o["content_type"])
			if l, ok := o["permissions"].([]any); ok {
				out := make([]string, len(l))
				for i, p := range l {
					out[i] = dabPerm(str(p))
				}
				o["permissions"] = out
			}
		},
		Capabilities: func(c *Ctx, obj Obj, p map[string]bool) map[string]bool {
			ok := c.User.Superuser && !boolv(obj["managed"])
			return map[string]bool{"edit": ok, "delete": ok}
		},
	})

	for _, kind := range []struct{ name, typ, col, ref string }{
		{"role_user_assignments", "role_user_assignment", "user", "users"},
		{"role_team_assignments", "role_team_assignment", "team", "teams"},
	} {
		kind := kind
		api.Register(&Resource{
			Name:         kind.name,
			Type:         kind.typ,
			Table:        "role_assignments",
			Verbose:      title2(kind.typ),
			NoName:       true,
			NoActivity:   true,
			NoTimestamps: true,
			Fields: []*Field{
				{Name: "created", Type: api.TDateTime, ReadOnly: true},
				{Name: "created_by", Type: api.TFK, Ref: "users", ReadOnly: true},
				{Name: "role_definition", Type: api.TFK, Ref: "role_definitions", Required: true},
				// Given here or by its ansible_id.
				{Name: kind.col, Type: api.TFK, Ref: kind.ref, Nullable: true},
				{Name: kind.col + "_ansible_id", Type: api.TString, Virtual: true, Nullable: true, Help: "The resource id of the " + kind.col + " who will receive permissions from this assignment. Alternative to " + kind.col + " field."},
				{Name: "content_type", Type: api.TString, ReadOnly: true, Nullable: true},
				{Name: "object_id", Type: api.TInt, Nullable: true},
				{Name: "object_ansible_id", Type: api.TString, Virtual: true, Nullable: true, Help: "Resource id of the object this role applies to. Alternative to the object_id field."},
			},
			View: func(c *Ctx, arg ArgFunc) string {
				base := fmt.Sprintf("t.%s_id IS NOT NULL", kind.col)
				if c.User.Superuser || c.User.Auditor {
					return base
				}
				return base + " AND (" + roleVisible(c, arg) + ")"
			},
			BeforeSave: func(c *Ctx, old, data Obj) error {
				if old == nil && !c.User.Anonymous() {
					data["created_by"] = c.User.UserID
				}
				return nil
			},
			CanAdd: func(c *Ctx, data Obj) error {
				rd, _ := api.ID(data["role_definition"])
				var ct *string
				if err := c.DB().QueryRow(c, "SELECT content_type FROM role_definitions WHERE id = $1", rd).Scan(&ct); err != nil {
					return api.FieldError("role_definition", "Invalid role definition.")
				}
				if v := data[kind.col+"_ansible_id"]; data[kind.col] == nil && str(v) != "" {
					id, err := byAnsibleID(c, kind.ref, kind.col+"_ansible_id", v)
					if err != nil {
						return err
					}
					data[kind.col] = id
				}
				if data[kind.col] == nil {
					return api.FieldError(kind.col, "This field is required.")
				}
				if v := data["object_ansible_id"]; data["object_id"] == nil && str(v) != "" && ct != nil {
					table := sharedTables[*ct]
					if table == "" {
						return api.FieldError("object_ansible_id", "Only organizations and teams have an ansible_id.")
					}
					id, err := byAnsibleID(c, table, "object_ansible_id", v)
					if err != nil {
						return err
					}
					data["object_id"] = id
				}
				obj, hasObj := api.ID(data["object_id"])
				if ct == nil || *ct == "" {
					if !c.User.Superuser {
						return api.ErrForbidden
					}
					data["content_type"] = nil
					data["object_id"] = nil
					return nil
				}
				if !hasObj {
					return api.FieldError("object_id", "This field is required for this role definition.")
				}
				data["content_type"] = *ct
				return canGrant(c, rd, *ct, obj)
			},
			CanChange: func(c *Ctx, old, data Obj) error {
				return api.Detail(405, "Role assignments cannot be modified; delete and recreate instead.")
			},
			AfterCreate: func(c *Ctx, obj Obj) error { return assignmentActivity(c, "associate", kind.col, obj) },
			AfterDelete: func(c *Ctx, obj Obj) error { return assignmentActivity(c, "disassociate", kind.col, obj) },
			CanDelete: func(c *Ctx, obj Obj) error {
				rd, _ := api.ID(obj["role_definition"])
				oid, _ := api.ID(obj["object_id"])
				return canGrant(c, rd, str(obj["content_type"]), oid)
			},
			Serialize: func(c *Ctx, o Obj) {
				ct := str(o["content_type"])
				o["content_type"] = dabCT(o["content_type"])
				o[kind.col+"_ansible_id"] = ansibleID(c, kind.ref, o[kind.col])
				o["object_ansible_id"] = nil
				if id, ok := api.ID(o["object_id"]); ok {
					o["object_id"] = fmt.Sprint(id)
					o["object_ansible_id"] = ansibleID(c, sharedTables[ct], id)
					if sf, ok := o["summary_fields"].(Obj); ok {
						sf["content_object"] = Obj{"id": id, "name": contentObjectName(c, ct, id)}
					}
				}
			},
			Capabilities: func(c *Ctx, obj Obj, p map[string]bool) map[string]bool {
				rd, _ := api.ID(obj["role_definition"])
				oid, _ := api.ID(obj["object_id"])
				return map[string]bool{"delete": canGrant(c, rd, str(obj["content_type"]), oid) == nil}
			},
		})
	}
}

func title2(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// roleMetadata is django-ansible-base's /role_metadata/: for each content
// type, the permissions a role definition of that type can hold, here those
// its managed roles hold (its own, and on an organization or a folder those
// of what it contains).
func roleMetadata(c *Ctx, w http.ResponseWriter, r *http.Request) {
	byType := map[string][]string{}
	seen := map[string]bool{}
	for _, m := range rbac.Managed {
		if m.ContentType == "" {
			continue
		}
		key := str(dabCT(m.ContentType))
		for _, p := range m.Permissions {
			if !seen[key+p] {
				seen[key+p] = true
				byType[key] = append(byType[key], dabPerm(p))
			}
		}
	}
	allowed := Obj{}
	for k, perms := range byType {
		sort.Strings(perms)
		allowed[k] = perms
	}
	api.WriteJSON(w, 200, Obj{"allowed_permissions": allowed})
}
