package resources

import (
	"fmt"

	"github.com/damhau/awxnext/internal/api"
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
