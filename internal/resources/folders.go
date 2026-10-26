package resources

import (
	"fmt"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

// folderField is the optional folder placement field shared by folder-capable resources.
func folderField() *Field {
	return &Field{Name: "folder", Type: api.TFK, Ref: "folders", Nullable: true,
		Help: "Folder containing this object. Roles granted on the folder (or its parents) apply to it."}
}

func registerFolders() {
	api.Register(&Resource{
		Name:         "folders",
		Type:         "folder",
		Table:        "folders",
		Verbose:      "Folder",
		Ordering:     "name",
		ContentType:  "folder",
		Versioned:    true,
		Managed:      true,
		OrgColumn:    "organization_id",
		FolderColumn: "id",
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true, CreateOnly: true},
			{Name: "parent", Type: api.TFK, Ref: "folders", Nullable: true, Help: "Parent folder; empty for a top-level folder."},
			{Name: "ancestors", Type: api.TJSON, ReadOnly: true, Hidden: true, NoFilter: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			pred := c.User.Predicate(rbac.ArgFunc(arg), "folder", "view_folder", rbac.Scope{ID: "t.id", Org: "t.organization_id", Folder: "t.id"})
			if pred == "TRUE" {
				return pred
			}
			// Folders above anything the user was granted are visible so the tree can be navigated.
			return fmt.Sprintf(`(%s) OR EXISTS (SELECT 1 FROM folders d JOIN role_assignments ra ON ra.content_type = 'folder' AND ra.object_id = d.id
				WHERE d.ancestors @> ARRAY[t.id] AND (ra.user_id = %s OR ra.team_id = ANY(%s)))`, pred, arg(c.User.UserID), arg(teams(c)))
		},
		CanAdd: func(c *Ctx, data Obj) error {
			org, _ := api.ID(data["organization"])
			if err := checkParent(c, data, org, 0); err != nil {
				return err
			}
			return requireAddFolder(c, data["parent"], org)
		},
		CanChange: func(c *Ctx, old, data Obj) error {
			ok, err := api.HasPerm(c, api.Lookup("folders"), old, "change")
			if err != nil {
				return err
			}
			if !ok {
				return api.ErrForbidden
			}
			if fmt.Sprint(old["parent"]) != fmt.Sprint(data["parent"]) {
				id, _ := api.ID(old["id"])
				org, _ := api.ID(old["organization"])
				if err := checkParent(c, data, org, id); err != nil {
					return err
				}
				return requireAddFolder(c, data["parent"], org)
			}
			return nil
		},
		AfterCreate: func(c *Ctx, obj Obj) error {
			id, _ := api.ID(obj["id"])
			_, err := c.DB().Exec(c, `UPDATE folders SET ancestors = coalesce((SELECT p.ancestors FROM folders p WHERE p.id = folders.parent_id), '{}') || id WHERE id = $1`, id)
			return err
		},
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			if fmt.Sprint(old["parent"]) == fmt.Sprint(obj["parent"]) {
				return nil
			}
			id, _ := api.ID(obj["id"])
			var oldPath, newPath []int64
			if err := c.DB().QueryRow(c, "SELECT ancestors FROM folders WHERE id = $1", id).Scan(&oldPath); err != nil {
				return err
			}
			if err := c.DB().QueryRow(c, `SELECT coalesce((SELECT ancestors FROM folders WHERE id = $1), '{}')`, obj["parent"]).Scan(&newPath); err != nil {
				return err
			}
			newPath = append(newPath, id)
			// Rewrite the path prefix of the folder and all its descendants.
			_, err := c.DB().Exec(c, `UPDATE folders SET ancestors = $1::bigint[] || ancestors[$2 + 1:] WHERE ancestors @> ARRAY[$3]::bigint[]`,
				newPath, len(oldPath), id)
			return err
		},
		BeforeDelete: func(c *Ctx, obj Obj) error {
			id, _ := api.ID(obj["id"])
			var n int64
			c.DB().QueryRow(c, `SELECT (SELECT count(*) FROM folders WHERE parent_id = $1)
				+ (SELECT count(*) FROM inventories WHERE folder_id = $1)
				+ (SELECT count(*) FROM credentials WHERE folder_id = $1)
				+ (SELECT count(*) FROM unified_job_templates WHERE folder_id = $1)
				+ (SELECT count(*) FROM notification_templates WHERE folder_id = $1)`, id).Scan(&n)
			if n > 0 {
				return api.Detail(409, "Folder is not empty. Move or delete its contents first.")
			}
			return nil
		},
		Summary: func(c *Ctx, objs []Obj) error {
			for _, o := range objs {
				id, _ := api.ID(o["id"])
				rows, err := c.DB().Query(c, `SELECT f.id, f.name FROM folders x CROSS JOIN LATERAL unnest(x.ancestors) WITH ORDINALITY a(fid, pos)
					JOIN folders f ON f.id = a.fid WHERE x.id = $1 ORDER BY a.pos`, id)
				if err != nil {
					return err
				}
				path := []Obj{}
				for rows.Next() {
					var fid int64
					var name string
					rows.Scan(&fid, &name)
					path = append(path, Obj{"id": fid, "name": name})
				}
				rows.Close()
				sf := o["summary_fields"].(Obj)
				sf["path"] = path
				sf["related_field_counts"] = Obj{
					"folders":                countQuery(c, "SELECT count(*) FROM folders WHERE parent_id = $1", id),
					"inventories":            countQuery(c, "SELECT count(*) FROM inventories WHERE folder_id = $1", id),
					"credentials":            countQuery(c, "SELECT count(*) FROM credentials WHERE folder_id = $1", id),
					"job_templates":          countQuery(c, "SELECT count(*) FROM unified_job_templates WHERE folder_id = $1 AND type = 'job_template'", id),
					"workflow_job_templates": countQuery(c, "SELECT count(*) FROM unified_job_templates WHERE folder_id = $1 AND type = 'workflow_job_template'", id),
					"projects":               countQuery(c, "SELECT count(*) FROM unified_job_templates WHERE folder_id = $1 AND type = 'project'", id),
				}
			}
			return nil
		},
		SubLists: []*SubList{
			childSub("folders", "folders", "parent_id", "parent"),
			childSub("inventories", "inventories", "folder_id", "folder"),
			childSub("projects", "projects", "folder_id", "folder"),
			childSub("job_templates", "job_templates", "folder_id", "folder"),
			childSub("workflow_job_templates", "workflow_job_templates", "folder_id", "folder"),
			childSub("credentials", "credentials", "folder_id", "folder"),
			childSub("notification_templates", "notification_templates", "folder_id", "folder"),
		},
	})
	org := api.Lookup("organizations")
	org.SubLists = append(org.SubLists, childSub("folders", "folders", "organization_id", "organization"))
}

// checkParent validates a folder's parent: same organization and no cycles.
func checkParent(c *Ctx, data Obj, org, self int64) error {
	pid, ok := api.ID(data["parent"])
	if !ok {
		return nil
	}
	var porg int64
	var path []int64
	if err := c.DB().QueryRow(c, "SELECT organization_id, ancestors FROM folders WHERE id = $1", pid).Scan(&porg, &path); err != nil {
		return api.FieldError("parent", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", pid))
	}
	if porg != org {
		return api.FieldError("parent", "Parent folder must belong to the same organization.")
	}
	for _, a := range path {
		if self != 0 && a == self {
			return api.FieldError("parent", "A folder cannot be moved inside itself.")
		}
	}
	return nil
}

// requireAddFolder checks add_folder on the parent folder, or on the organization for top-level folders.
func requireAddFolder(c *Ctx, parent any, org int64) error {
	if c.User.Superuser {
		return nil
	}
	var ok bool
	var err error
	if pid, has := api.ID(parent); has {
		ok, err = c.User.HasFolder(c, c.App.DB, "add_folder", pid, org)
	} else {
		ok, err = c.User.HasOrg(c, c.App.DB, "add_folder", org)
	}
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	return nil
}
