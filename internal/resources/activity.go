package resources

import (
	"fmt"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

func registerActivity() {
	api.Register(&Resource{
		Name:         "activity_stream",
		Type:         "activity_stream",
		Table:        "activity_stream",
		Verbose:      "Activity Stream",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "-timestamp",
		SearchFields: []string{"changes"},
		Fields: []*Field{
			{Name: "timestamp", Type: api.TDateTime, ReadOnly: true},
			{Name: "operation", Type: api.TChoice, ReadOnly: true, Choices: labelled("create", "Entity Created", "update", "Entity Updated", "delete", "Entity Deleted", "associate", "Entity Associated with another Entity", "disassociate", "Entity was Disassociated with another Entity")},
			{Name: "changes", Type: api.TJSON, ReadOnly: true, Help: "A summary of the new and changed values when an object is created, updated, or deleted"},
			{Name: "object1", Type: api.TString, ReadOnly: true},
			{Name: "object1_id", Type: api.TInt, ReadOnly: true, Hidden: true},
			{Name: "object2", Type: api.TString, ReadOnly: true},
			{Name: "object2_id", Type: api.TInt, ReadOnly: true, Hidden: true},
			{Name: "object_relationship_type", Column: "object_relationship_type", Type: api.TString, ReadOnly: true, Label: "Object association"},
			{Name: "actor", Type: api.TFK, Ref: "users", ReadOnly: true, Hidden: true},
			{Name: "summary", Type: api.TJSON, ReadOnly: true, Hidden: true},
			{Name: "organization", Type: api.TFK, Ref: "organizations", ReadOnly: true, Hidden: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return fmt.Sprintf(`t.actor_id = %s OR t.organization_id IN (SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
				WHERE (ra.user_id = %s OR ra.team_id = ANY(%s)) AND ra.content_type = 'organization' AND 'audit_organization' = ANY(rd.permissions)
				UNION SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
				WHERE (ra.user_id = %s OR ra.team_id = ANY(%s)) AND ra.content_type = 'organization' AND 'change_organization' = ANY(rd.permissions))`,
				arg(c.User.UserID), arg(c.User.UserID), arg(teams(c)), arg(c.User.UserID), arg(teams(c)))
		},
		Serialize: func(c *Ctx, o Obj) {
			o["object_association"] = o["object_relationship_type"]
			delete(o, "object_relationship_type")
		},
		Summary: func(c *Ctx, objs []Obj) error {
			for _, o := range objs {
				id, _ := api.ID(o["id"])
				var summary map[string]any
				var o1, o2 string
				var o1id, o2id *int64
				c.DB().QueryRow(c, "SELECT summary, object1, object1_id, object2, object2_id FROM activity_stream WHERE id = $1", id).Scan(&summary, &o1, &o1id, &o2, &o2id)
				sf := o["summary_fields"].(Obj)
				for k, v := range summary {
					sf[k] = v
				}
				rel := o["related"].(Obj)
				if r := api.LookupType(o1); r != nil && o1id != nil {
					rel[o1] = []string{r.URL(*o1id)}
				}
				if r := api.LookupType(o2); r != nil && o2id != nil {
					rel[o2] = append(asStrings(rel[o2]), r.URL(*o2id))
				}
			}
			return nil
		},
	})
}

func asStrings(v any) []string {
	if l, ok := v.([]string); ok {
		return l
	}
	return nil
}

// jobLaunched records a launch as AWX's activity_stream_create does for
// jobs, workflow jobs and workflow approvals: the job's fields, its
// credentials ("name (id)") and labels by name, survey passwords hidden in
// its extra_vars, and the user who launched it.
func jobLaunched(c *Ctx, resName string, id int64) error {
	if !c.App.Settings.Bool(c, "ACTIVITY_STREAM_ENABLED") {
		return nil
	}
	res := api.Lookup(resName)
	sys := *c
	sys.User = rbac.System()
	obj, err := api.Get(&sys, res, id)
	if err != nil {
		return err
	}
	changes := api.ActivityFields(res, obj)
	if ev, ok := changes["extra_vars"].(string); ok {
		changes["extra_vars"] = maskSurveyPasswords(ev)
	}
	names := func(sql string) []string {
		out := []string{}
		rows, err := c.DB().Query(c, sql, id)
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				out = append(out, n)
			}
		}
		return out
	}
	if resName == "jobs" {
		changes["credentials"] = names(`SELECT cr.name || ' (' || cr.id || ')' FROM unified_job_credentials x JOIN credentials cr ON cr.id = x.credential_id
			WHERE x.unified_job_id = $1 ORDER BY cr.id`)
		changes["labels"] = names("SELECT l.name FROM unified_job_labels x JOIN labels l ON l.id = x.label_id WHERE x.unified_job_id = $1 ORDER BY l.name")
	}
	return api.Activity(c, "create", res, obj, changes, nil, nil, "")
}

// assignmentActivity records a role given or taken away through the role
// assignment API as the role API records it, which AWX's role assignments
// do too: the role linked to the user or team. A role definition with no
// role on the object (custom or system-wide ones) is recorded on the
// definition, the object in the changes.
func assignmentActivity(c *Ctx, op, principal string, obj Obj) error {
	rd, _ := api.ID(obj["role_definition"])
	pid, _ := api.ID(obj[principal])
	ct := str(obj["content_type"])
	oid, _ := api.ID(obj["object_id"])
	sys := *c
	sys.User = rbac.System()
	plural := principal + "s"
	who, err := api.Get(&sys, api.Lookup(plural), pid)
	if err != nil {
		return err
	}
	var roleID int64
	if c.DB().QueryRow(c, "SELECT id FROM object_roles WHERE role_definition_id = $1 AND content_type = $2 AND object_id = $3", rd, ct, oid).Scan(&roleID) == nil {
		roles := api.Lookup("roles")
		role, err := api.Get(&sys, roles, roleID)
		if err != nil {
			return err
		}
		return api.Activity(c, op, roles, role, Obj{}, api.Lookup(plural), who, "role."+plural)
	}
	defs := api.Lookup("role_definitions")
	def, err := api.Get(&sys, defs, rd)
	if err != nil {
		return err
	}
	changes := Obj{"content_type": dabCT(obj["content_type"]), "object_id": nil}
	if ct != "" {
		changes["object_id"] = oid
		changes["object_name"] = contentObjectName(c, ct, oid)
	}
	return api.Activity(c, op, defs, def, changes, api.Lookup(plural), who, "role_definition."+plural)
}
