package gitops

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/runners/gitsync"
)

type Ctx = api.Ctx

func init() {
	register()
}

func orgAdmin(c *Ctx, org any) error {
	id, _ := api.ID(org)
	ok, err := c.User.HasOrg(c, c.App.DB, "change_organization", id)
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrForbidden
	}
	return nil
}

func register() {
	res := api.Register(&api.Resource{
		Name:         "gitops_sources",
		Type:         "gitops_source",
		Table:        "gitops_sources",
		Verbose:      "GitOps Source",
		Ordering:     "name",
		OrgColumn:    "organization_id",
		FolderColumn: "folder_id",
		SearchFields: []string{"name", "description", "path"},
		Fields: []*api.Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true, CreateOnly: true},
			{Name: "folder", Type: api.TFK, Ref: "folders", Nullable: true,
				Help: "Folder the declared objects are placed in."},
			{Name: "project", Type: api.TFK, Ref: "projects", Nullable: true, Required: true,
				Help: "Project holding the declarations; its synced revision is applied."},
			{Name: "path", Type: api.TString, Default: "", MaxLength: 1024,
				Help: "Directory within the project containing the YAML declarations."},
			{Name: "enabled", Type: api.TBool, Default: true},
			{Name: "prune", Type: api.TBool, Default: true,
				Help: "Delete objects of this source that are no longer declared."},
			{Name: "adopt_existing", Type: api.TBool, Default: false,
				Help: "Take over unmanaged objects with the same name instead of failing."},
			{Name: "sync_on_project_update", Type: api.TBool, Default: true,
				Help: "Apply the declarations after each successful project update."},
			{Name: "status", Type: api.TString, ReadOnly: true},
			{Name: "last_sync", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "last_revision", Type: api.TString, ReadOnly: true},
			{Name: "last_error", Type: api.TText, ReadOnly: true},
			{Name: "last_report", Type: api.TJSON, ReadOnly: true, NoFilter: true},
		},
		View: func(c *Ctx, arg api.ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return fmt.Sprintf(`t.organization_id IN (SELECT ra.object_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
				WHERE (ra.user_id = %s OR ra.team_id = ANY(%s)) AND ra.content_type = 'organization' AND 'change_organization' = ANY(rd.permissions))`,
				arg(c.User.UserID), arg(c.User.Teams))
		},
		CanAdd:    func(c *Ctx, data Obj) error { return orgAdmin(c, data["organization"]) },
		CanChange: func(c *Ctx, obj, data Obj) error { return orgAdmin(c, obj["organization"]) },
		CanDelete: func(c *Ctx, obj Obj) error { return orgAdmin(c, obj["organization"]) },
		Capabilities: func(c *Ctx, obj Obj, perms map[string]bool) map[string]bool {
			ok := orgAdmin(c, obj["organization"]) == nil
			return map[string]bool{"edit": ok, "delete": ok, "sync": ok}
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			if p, ok := data["project"]; ok && p != nil {
				pid, _ := api.ID(p)
				var org *int64
				if err := c.DB().QueryRow(c, "SELECT organization_id FROM unified_job_templates WHERE id = $1", pid).Scan(&org); err != nil {
					return api.FieldError("project", "Project not found.")
				}
				want, _ := api.ID(data["organization"])
				if old != nil {
					want, _ = api.ID(old["organization"])
				}
				if org == nil || *org != want {
					return api.FieldError("project", "The project must belong to the source's organization.")
				}
				if err := api.RequirePermID(c, "project", "projects", pid, "use"); err != nil {
					return err
				}
			}
			return nil
		},
		AfterDelete: func(c *Ctx, obj Obj) error {
			// Objects of a deleted source become ordinary objects again.
			id, _ := api.ID(obj["id"])
			for _, r := range api.Resources() {
				if !r.Managed {
					continue
				}
				if _, err := c.DB().Exec(c, "UPDATE "+r.Table+" SET managed_by = '' WHERE managed_by = $1", Tag(id)); err != nil {
					return err
				}
			}
			return nil
		},
	})
	res.Actions = append(res.Actions, &api.Action{Name: "sync", Methods: []string{"GET", "POST"}, Handler: handleSync})
}

// handleSync applies the source (POST) or reports whether it can be applied
// (GET). POST with ?dry_run=true or {"dry_run": true} returns the plan only.
func handleSync(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	allowed := orgAdmin(c, obj["organization"]) == nil
	if r.Method == "GET" {
		api.WriteJSON(w, 200, Obj{"can_sync": allowed})
		return
	}
	if !allowed {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	dry, _ := strconv.ParseBool(r.URL.Query().Get("dry_run"))
	if r.ContentLength > 0 {
		body, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		if b, ok := body["dry_run"].(bool); ok {
			dry = b
		}
	}
	id, _ := api.ID(obj["id"])
	rep, err := Reconcile(c, c.App, id, dry)
	if rep == nil {
		api.WriteError(w, err)
		return
	}
	status := 200
	if !dry && err == nil {
		status = 202
	}
	if err != nil {
		status = 400
	}
	api.WriteJSON(w, status, rep)
}

// ProjectUpdated applies the sources of a project after a successful update.
func ProjectUpdated(ctx context.Context, m *jobs.Manager, job jobs.Row) {
	if job.Str("type") != "project_update" || job.Str("status") != "successful" || job.Str("job_type") == "run" {
		return
	}
	pid := job.Int("unified_job_template_id")
	rows, err := m.App.DB.Query(ctx, "SELECT id FROM gitops_sources WHERE project_id = $1 AND enabled AND sync_on_project_update", pid)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		go func(id int64) {
			if _, err := Reconcile(context.Background(), m.App, id, false); err != nil {
				slog.Warn("gitops sync failed", "source", id, "error", err)
			}
		}(id)
	}
}

// revisionDir returns an existing checkout of a project revision.
func revisionDir(app *api.App, project int64, rev string) (string, bool) {
	dir := gitsync.RevisionDir(app.Cfg.ProjectsRoot, project, rev)
	if _, err := os.Stat(dir); err == nil {
		return dir, true
	}
	return "", false
}
