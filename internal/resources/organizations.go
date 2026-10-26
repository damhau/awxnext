package resources

import (
	"fmt"

	"github.com/damhau/awxnext/internal/api"
)

func registerOrganizations() {
	api.Register(&Resource{
		Name:        "organizations",
		Type:        "organization",
		Table:       "organizations",
		Verbose:     "Organization",
		Ordering:    "name",
		ContentType: "organization",
		Versioned:   true,
		OrgColumn:   "id",
		Fields: []*Field{
			{Name: "max_hosts", Type: api.TInt, Default: 0, Help: "Maximum number of hosts allowed to be managed by this organization."},
			{Name: "default_environment", Type: api.TFK, Ref: "execution_environments", Nullable: true, Help: "The default execution environment for jobs run by this organization."},
		},
		CanAdd: func(c *Ctx, data Obj) error {
			if !c.User.Superuser {
				return api.ErrForbidden
			}
			return nil
		},
		Serialize: func(c *Ctx, o Obj) {
			o["custom_virtualenv"] = nil
		},
		Summary: func(c *Ctx, objs []Obj) error {
			for _, o := range objs {
				id := o["id"]
				o["summary_fields"].(Obj)["related_field_counts"] = Obj{
					"inventories":   countQuery(c, "SELECT count(*) FROM inventories WHERE organization_id = $1", id),
					"teams":         countQuery(c, "SELECT count(*) FROM teams WHERE organization_id = $1", id),
					"users":         countQuery(c, "SELECT count(DISTINCT user_id) FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id WHERE rd.name = 'Organization Member' AND ra.content_type = 'organization' AND ra.object_id = $1", id),
					"admins":        countQuery(c, "SELECT count(DISTINCT user_id) FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id WHERE rd.name = 'Organization Admin' AND ra.content_type = 'organization' AND ra.object_id = $1", id),
					"job_templates": countQuery(c, "SELECT count(*) FROM unified_job_templates WHERE type = 'job_template' AND organization_id = $1", id),
					"projects":      countQuery(c, "SELECT count(*) FROM unified_job_templates WHERE type = 'project' AND organization_id = $1", id),
				}
			}
			return nil
		},
		SubLists: append([]*SubList{
			roleUserSub("users", "Organization Member", "organization"),
			roleUserSub("admins", "Organization Admin", "organization"),
			childSub("teams", "teams", "organization_id", "organization"),
			childSub("inventories", "inventories", "organization_id", "organization"),
			childSub("projects", "projects", "organization_id", "organization"),
			childSub("job_templates", "job_templates", "organization_id", "organization"),
			childSub("workflow_job_templates", "workflow_job_templates", "organization_id", "organization"),
			childSub("credentials", "credentials", "organization_id", "organization"),
			childSub("notification_templates", "notification_templates", "organization_id", "organization"),
			childSub("execution_environments", "execution_environments", "organization_id", "organization"),
			orderedLinkSub("instance_groups", "instance_groups", "instance_group_links", "instance_group_id", "organization"),
			{
				Name:   "galaxy_credentials",
				Target: "credentials",
				Filter: func(arg ArgFunc, pid int64) string {
					return fmt.Sprintf("t.id IN (SELECT credential_id FROM organization_galaxy_credentials WHERE organization_id = %s)", arg(pid))
				},
				NoCreate: true,
				Associate: func(c *Ctx, pid, cid int64) error {
					var kind string
					c.DB().QueryRow(c, "SELECT ct.kind FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1", cid).Scan(&kind)
					if kind != "galaxy" {
						return api.BadRequest("Credential must be a Galaxy credential, not " + kind + ".")
					}
					_, err := c.DB().Exec(c, `INSERT INTO organization_galaxy_credentials (organization_id, credential_id, position)
						VALUES ($1, $2, (SELECT coalesce(max(position) + 1, 0) FROM organization_galaxy_credentials WHERE organization_id = $1)) ON CONFLICT DO NOTHING`, pid, cid)
					return err
				},
				Disassociate: func(c *Ctx, pid, cid int64) error {
					_, err := c.DB().Exec(c, "DELETE FROM organization_galaxy_credentials WHERE organization_id = $1 AND credential_id = $2", pid, cid)
					return err
				},
			},
		}, notificationSubs("organization", true)...),
	})

	api.Register(&Resource{
		Name:        "teams",
		Type:        "team",
		Table:       "teams",
		Verbose:     "Team",
		Ordering:    "name",
		ContentType: "team",
		Versioned:   true,
		Managed:     true,
		OrgColumn:   "organization_id",
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true},
		},
		SubLists: []*SubList{
			roleUserSub("users", "Team Member", "team"),
			roleUserSub("admins", "Team Admin", "team"),
		},
	})
}
