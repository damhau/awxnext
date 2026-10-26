package rbac

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Permissions per content type (without the "_<type>" suffix).
var typePerms = map[string][]string{
	"organization":         {"view", "change", "delete", "member", "audit"},
	"team":                 {"view", "change", "delete", "member"},
	"credential":           {"view", "change", "delete", "use"},
	"project":              {"view", "change", "delete", "use", "update"},
	"inventory":            {"view", "change", "delete", "use", "adhoc", "update"},
	"jobtemplate":          {"view", "change", "delete", "execute"},
	"workflowjobtemplate":  {"view", "change", "delete", "execute", "approve"},
	"notificationtemplate": {"view", "change", "delete"},
	"executionenvironment": {"view", "change", "delete"},
	"instancegroup":        {"view", "change", "delete", "use"},
	"folder":               {"view", "change", "delete"},
}

// Types that can live in a folder.
var folderChildTypes = []string{"credential", "project", "inventory", "jobtemplate", "workflowjobtemplate", "notificationtemplate"}

// Types that can be created inside an organization (add_<type> is granted on the org).
var orgChildTypes = []string{"team", "credential", "project", "inventory", "workflowjobtemplate", "notificationtemplate", "executionenvironment", "folder"}

func perms(ct string, actions ...string) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a+"_"+ct)
	}
	return out
}

func all(ct string) []string { return perms(ct, typePerms[ct]...) }

func concat(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, p := range l {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

type ManagedRole struct {
	Name        string
	Description string
	ContentType string
	Permissions []string
	// LegacyField is the AWX role field (e.g. "admin_role") this definition backs on objects of ContentType.
	LegacyField string
}

func viewAll() []string {
	var out []string
	for ct := range typePerms {
		if ct != "instancegroup" {
			out = append(out, "view_"+ct)
		}
	}
	return out
}

// Managed is the list of built-in role definitions.
var Managed = buildManaged()

func buildManaged() []ManagedRole {
	orgAdmin := []string{}
	for ct := range typePerms {
		if ct != "instancegroup" {
			orgAdmin = append(orgAdmin, all(ct)...)
		}
	}
	for _, ct := range orgChildTypes {
		orgAdmin = append(orgAdmin, "add_"+ct)
	}
	r := []ManagedRole{
		{"Organization Admin", "Has all permissions to a single organization and all objects inside of it", "organization", concat(orgAdmin), "admin_role"},
		{"Organization Member", "Is a member of the organization", "organization", []string{"view_organization", "member_organization", "view_team", "view_executionenvironment", "view_folder"}, "member_role"},
		{"Organization Audit", "Has permission to view all objects inside of a single organization", "organization", concat([]string{"audit_organization"}, viewAll()), "auditor_role"},
		{"Organization Read", "May view settings for the organization", "organization", []string{"view_organization"}, "read_role"},
		{"Organization Execute", "May run any executable resources in the organization", "organization", concat([]string{"view_organization"}, perms("jobtemplate", "view", "execute"), perms("workflowjobtemplate", "view", "execute")), "execute_role"},
		{"Organization Project Admin", "Can manage all projects of the organization", "organization", concat([]string{"view_organization", "add_project"}, all("project")), "project_admin_role"},
		{"Organization Inventory Admin", "Can manage all inventories of the organization", "organization", concat([]string{"view_organization", "add_inventory"}, all("inventory")), "inventory_admin_role"},
		{"Organization Credential Admin", "Can manage all credentials of the organization", "organization", concat([]string{"view_organization", "add_credential"}, all("credential")), "credential_admin_role"},
		{"Organization WorkflowJobTemplate Admin", "Can manage all workflows of the organization", "organization", concat([]string{"view_organization", "add_workflowjobtemplate"}, all("workflowjobtemplate")), "workflow_admin_role"},
		{"Organization NotificationTemplate Admin", "Can manage all notifications of the organization", "organization", concat([]string{"view_organization", "add_notificationtemplate"}, all("notificationtemplate")), "notification_admin_role"},
		{"Organization JobTemplate Admin", "Can manage all job templates of the organization", "organization", concat([]string{"view_organization"}, all("jobtemplate")), "job_template_admin_role"},
		{"Organization ExecutionEnvironment Admin", "Can manage all execution environments of the organization", "organization", concat([]string{"view_organization", "add_executionenvironment"}, all("executionenvironment")), "execution_environment_admin_role"},
		{"Organization Approval", "Can approve or deny a workflow approval node", "organization", concat([]string{"view_organization"}, perms("workflowjobtemplate", "view", "approve")), "approval_role"},

		{"Team Admin", "Can manage a single team and its membership", "team", all("team"), "admin_role"},
		{"Team Member", "Inherits permissions assigned to this team", "team", []string{"view_team", "member_team"}, "member_role"},
		{"Team Read", "May view settings for the team", "team", []string{"view_team"}, "read_role"},

		{"Credential Admin", "Has all permissions to a single credential", "credential", all("credential"), "admin_role"},
		{"Credential Use", "Can use the credential in a job template", "credential", perms("credential", "view", "use"), "use_role"},
		{"Credential Read", "May view settings for the credential", "credential", perms("credential", "view"), "read_role"},

		{"Project Admin", "Has all permissions to a single project", "project", all("project"), "admin_role"},
		{"Project Use", "Can use project in a job template", "project", perms("project", "view", "use"), "use_role"},
		{"Project Update", "May update the project", "project", perms("project", "view", "update"), "update_role"},
		{"Project Read", "May view settings for the project", "project", perms("project", "view"), "read_role"},

		{"Inventory Admin", "Has all permissions to a single inventory", "inventory", all("inventory"), "admin_role"},
		{"Inventory Use", "Can use inventory in a job template", "inventory", perms("inventory", "view", "use"), "use_role"},
		{"Inventory Adhoc", "May run ad hoc commands on the inventory", "inventory", perms("inventory", "view", "use", "adhoc"), "adhoc_role"},
		{"Inventory Update", "May update the inventory", "inventory", perms("inventory", "view", "update"), "update_role"},
		{"Inventory Read", "May view settings for the inventory", "inventory", perms("inventory", "view"), "read_role"},

		{"JobTemplate Admin", "Has all permissions to a single job template", "jobtemplate", all("jobtemplate"), "admin_role"},
		{"JobTemplate Execute", "May run the job template", "jobtemplate", perms("jobtemplate", "view", "execute"), "execute_role"},
		{"JobTemplate Read", "May view settings for the job template", "jobtemplate", perms("jobtemplate", "view"), "read_role"},

		{"WorkflowJobTemplate Admin", "Has all permissions to a single workflow job template", "workflowjobtemplate", all("workflowjobtemplate"), "admin_role"},
		{"WorkflowJobTemplate Execute", "May run the workflow job template", "workflowjobtemplate", perms("workflowjobtemplate", "view", "execute"), "execute_role"},
		{"WorkflowJobTemplate Approve", "May approve workflow approval nodes", "workflowjobtemplate", perms("workflowjobtemplate", "view", "approve"), "approval_role"},
		{"WorkflowJobTemplate Read", "May view settings for the workflow job template", "workflowjobtemplate", perms("workflowjobtemplate", "view"), "read_role"},

		{"NotificationTemplate Admin", "Has all permissions to a single notification template", "notificationtemplate", all("notificationtemplate"), "admin_role"},
		{"ExecutionEnvironment Admin", "Has all permissions to a single execution environment", "executionenvironment", all("executionenvironment"), "admin_role"},

		{"Folder Admin", "Has all permissions to a folder, its subfolders and every object inside", "folder", folderAdmin(), "admin_role"},
		{"Folder Execute", "May run any job or workflow template in the folder", "folder", concat([]string{"view_folder"}, perms("jobtemplate", "view", "execute"), perms("workflowjobtemplate", "view", "execute")), "execute_role"},
		{"Folder Use", "May use inventories, projects and credentials in the folder", "folder", concat([]string{"view_folder"}, perms("inventory", "view", "use"), perms("project", "view", "use"), perms("credential", "view", "use")), "use_role"},
		{"Folder Auditor", "May view every object in the folder", "folder", folderViewAll(), "auditor_role"},
		{"Folder Read", "May view the folder", "folder", []string{"view_folder"}, "read_role"},

		{"InstanceGroup Admin", "Has all permissions to a single instance group", "instancegroup", all("instancegroup"), "admin_role"},
		{"InstanceGroup Use", "Can use the instance group in a job template", "instancegroup", perms("instancegroup", "view", "use"), "use_role"},
		{"InstanceGroup Read", "May view settings for the instance group", "instancegroup", perms("instancegroup", "view"), "read_role"},
	}
	return r
}

func folderAdmin() []string {
	out := all("folder")
	out = append(out, "add_folder")
	for _, ct := range folderChildTypes {
		out = append(out, all(ct)...)
		out = append(out, "add_"+ct)
	}
	return concat(out)
}

func folderViewAll() []string {
	out := []string{"view_folder"}
	for _, ct := range folderChildTypes {
		out = append(out, "view_"+ct)
	}
	return out
}

// LegacyRoles returns the legacy role fields available on a content type, in display order.
func LegacyRoles(ct string) []ManagedRole {
	var out []ManagedRole
	for _, m := range Managed {
		if m.ContentType == ct && m.LegacyField != "" {
			out = append(out, m)
		}
	}
	return out
}

// SyncManaged upserts the managed role definitions.
func SyncManaged(ctx context.Context, db *pgxpool.Pool) error {
	for _, m := range Managed {
		_, err := db.Exec(ctx, `
			INSERT INTO role_definitions (name, description, content_type, permissions, managed)
			VALUES ($1, $2, $3, $4, true)
			ON CONFLICT (name) DO UPDATE SET description = EXCLUDED.description,
				content_type = EXCLUDED.content_type, permissions = EXCLUDED.permissions, managed = true, modified = now()`,
			m.Name, m.Description, m.ContentType, m.Permissions)
		if err != nil {
			return err
		}
	}
	return nil
}

// EnsureObjectRoles creates the legacy object role rows for an object.
func EnsureObjectRoles(ctx context.Context, db Execer, ct string, objectID int64) error {
	for _, m := range LegacyRoles(ct) {
		_, err := db.Exec(ctx, `
			INSERT INTO object_roles (content_type, object_id, role_field, role_definition_id)
			SELECT $1, $2, $3, id FROM role_definitions WHERE name = $4
			ON CONFLICT DO NOTHING`, ct, objectID, m.LegacyField, m.Name)
		if err != nil {
			return err
		}
	}
	return nil
}
