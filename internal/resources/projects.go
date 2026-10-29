package resources

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/projectscan"
)

var scmURLRe = regexp.MustCompile(`^(https?://|ssh://|git://|file://|[\w.-]+@[\w.-]+:)`)

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// ProjectLocalPath is the AWX-style directory name of a git project.
func ProjectLocalPath(id int64, name string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(name, "_"), "_")
	if len(slug) > 64 {
		slug = slug[:64]
	}
	return fmt.Sprintf("_%d__%s", id, strings.ToLower(slug))
}

// ProjectRepoDir is the fetch clone of a git project on this node.
func ProjectRepoDir(root string, id int64) string {
	return filepath.Join(root, ".repos", fmt.Sprint(id))
}

// LaunchProjectUpdate creates a project update job. jobType is "check" for a
// project sync or "run" for a job-specific checkout (branch override).
func LaunchProjectUpdate(c *Ctx, project Obj, launchType, jobType, branch string) (int64, error) {
	pid, _ := api.ID(project["id"])
	if branch == "" {
		branch = str(project["scm_branch"])
	}
	values := map[string]any{
		"type":                     "project_update",
		"name":                     str(project["name"]),
		"unified_job_template_id":  pid,
		"project_id":               pid,
		"organization_id":          project["organization"],
		"scm_type":                 project["scm_type"],
		"scm_url":                  project["scm_url"],
		"scm_branch":               branch,
		"scm_refspec":              project["scm_refspec"],
		"scm_clean":                project["scm_clean"],
		"scm_track_submodules":     project["scm_track_submodules"],
		"scm_delete_on_update":     project["scm_delete_on_update"],
		"credential_id":            project["credential"],
		"local_path":               project["local_path"],
		"timeout":                  project["timeout"],
		"job_type":                 jobType,
		"launch_type":              launchType,
		"execution_environment_id": project["default_environment"],
	}
	if !c.User.Anonymous() {
		values["created_by_id"] = c.User.UserID
		values["modified_by_id"] = c.User.UserID
	}
	id, err := jobs.Create(c, c.DB(), values)
	if err != nil {
		return 0, err
	}
	jobs.Wake(c, c.DB())
	return id, nil
}

func registerProjects() {
	proj := api.Register(&Resource{
		Name:          "projects",
		Type:          "project",
		Table:         "unified_job_templates",
		Discriminator: true,
		Verbose:       "Project",
		Ordering:      "name",
		ContentType:   "project",
		OrgColumn:     "organization_id",
		FolderColumn:  "folder_id",
		Versioned:     true,
		Managed:       true,
		Copyable:      true,
		Capabilities: func(c *Ctx, obj Obj, p map[string]bool) map[string]bool {
			su := c.User.Superuser
			edit := su || p["change_project"]
			update := (su || p["update_project"]) && str(obj["scm_type"]) != ""
			return map[string]bool{"edit": edit, "delete": su || p["delete_project"], "copy": edit, "start": update, "schedule": update}
		},
		Fields: append([]*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true, Help: "The organization used to determine access to this template."},
			{Name: "scm_type", Type: api.TChoice, Default: "", Choices: labelled("", "Manual", "git", "Git", "archive", "Remote Archive"), Label: "Source Control Type", Help: "Specifies the source control system used to store the project."},
			{Name: "scm_url", Type: api.TString, Default: "", MaxLength: 1024, Label: "Source Control URL", Help: "The location where the project is stored."},
			{Name: "scm_branch", Type: api.TString, Default: "", MaxLength: 256, Label: "Source Control Branch", Help: "Specific branch, tag or commit to checkout."},
			{Name: "scm_refspec", Type: api.TString, Default: "", MaxLength: 1024, Label: "Source Control Refspec", Help: "For git projects, an additional refspec to fetch."},
			{Name: "scm_clean", Type: api.TBool, Default: false, Help: "Discard any local changes before syncing the project. Jobs always work on a copy, so only changes made on the server's disk are discarded."},
			{Name: "scm_track_submodules", Type: api.TBool, Default: false, Help: "Track submodules latest commits on defined branch."},
			{Name: "scm_delete_on_update", Type: api.TBool, Default: false, Help: "Delete the project before syncing."},
			{Name: "credential", Type: api.TFK, Ref: "credentials", Nullable: true},
			{Name: "timeout", Type: api.TInt, Default: 0, Help: "The amount of time (in seconds) to run before the task is canceled."},
			{Name: "scm_revision", Type: api.TString, ReadOnly: true, Label: "SCM Revision", Help: "The last revision fetched by a project update"},
			{Name: "scm_update_on_launch", Type: api.TBool, Default: false, Help: "Update the project when a job is launched that uses the project."},
			{Name: "scm_update_cache_timeout", Type: api.TInt, Default: 0, Help: "The number of seconds after the last project update ran that a new project update will be launched as a job dependency."},
			{Name: "allow_override", Type: api.TBool, Default: false, Help: "Allow changing the SCM branch or revision in a job template that uses this project."},
			{Name: "local_path", Type: api.TString, Default: "", MaxLength: 1024, Help: "Local path (relative to PROJECTS_ROOT) containing playbooks and related files for this project."},
			{Name: "default_environment", Type: api.TFK, Ref: "execution_environments", Nullable: true, Help: "The default execution environment for jobs run using this project."},
			{Name: "signature_validation_credential", Type: api.TFK, Ref: "credentials", Nullable: true, Help: "An optional credential used for validating files in the project against unexpected changes."},
			{Name: "playbook_files", Type: api.TJSON, ReadOnly: true, Hidden: true, NoFilter: true},
			{Name: "inventory_files", Type: api.TJSON, ReadOnly: true, Hidden: true, NoFilter: true},
			folderField(),
		}, ujtFields()...),
		BeforeSave: func(c *Ctx, old, data Obj) error {
			merged := Obj{}
			for k, v := range old {
				merged[k] = v
			}
			for k, v := range data {
				merged[k] = v
			}
			switch str(merged["scm_type"]) {
			case "git":
				u := str(merged["scm_url"])
				if u == "" {
					return api.FieldError("scm_url", "SCM URL is required.")
				}
				if !scmURLRe.MatchString(u) && !strings.HasPrefix(u, "/") {
					return api.FieldError("scm_url", "Invalid git URL. Use https://, ssh://, git:// or user@host:path.")
				}
				if (strings.HasPrefix(u, "file://") || strings.HasPrefix(u, "/")) && !c.App.Settings.Bool(c, "PROJECT_ALLOW_FILE_URLS") {
					return api.FieldError("scm_url", "Local file URLs are disabled (setting PROJECT_ALLOW_FILE_URLS).")
				}
				if strings.Contains(u, "@") && strings.HasPrefix(u, "http") && strings.Contains(strings.SplitN(u, "@", 2)[0], ":") {
					return api.FieldError("scm_url", "Password in SCM URLs is not allowed; use a Source Control credential.")
				}
			case "archive":
				u := str(merged["scm_url"])
				if u == "" {
					return api.FieldError("scm_url", "SCM URL is required.")
				}
				if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
					return api.FieldError("scm_url", "Unsupported archive URL. Use http:// or https://.")
				} else if _, set := p.User.Password(); set {
					return api.FieldError("scm_url", "Password in SCM URLs is not allowed; use a Source Control credential.")
				}
				if str(data["scm_branch"]) != "" {
					return api.FieldError("scm_branch", "SCM branch cannot be used with archive projects.")
				}
			case "":
				if _, ok := data["local_path"]; ok || old == nil {
					lp := str(merged["local_path"])
					if lp != "" {
						if strings.Contains(lp, "..") || strings.HasPrefix(lp, "/") || strings.HasPrefix(lp, "_") {
							return api.FieldError("local_path", "Invalid local path.")
						}
						if st, err := os.Stat(filepath.Join(c.App.Cfg.ProjectsRoot, lp)); err != nil || !st.IsDir() {
							return api.FieldError("local_path", fmt.Sprintf("Invalid path choice %q: not a directory in %s.", lp, c.App.Cfg.ProjectsRoot))
						}
					}
				}
			}
			if typ := str(merged["scm_type"]); typ != "git" {
				if str(data["scm_refspec"]) != "" {
					return api.FieldError("scm_refspec", "SCM refspec can only be used with git projects.")
				}
				if data["scm_track_submodules"] == true {
					return api.FieldError("scm_track_submodules", "SCM track_submodules can only be used with git projects.")
				}
			}
			if err := requireCredentialKind(c, "credential", data["credential"], "scm"); err != nil {
				return err
			}
			if err := requireCredentialKind(c, "signature_validation_credential", data["signature_validation_credential"], "cryptography"); err != nil {
				return err
			}
			if old == nil || data["scm_type"] != nil {
				if str(merged["scm_type"]) == "" {
					data["status"] = "ok"
					if str(merged["local_path"]) == "" {
						data["status"] = "missing"
					}
				} else if old == nil {
					data["status"] = "never updated"
				}
			}
			return nil
		},
		AfterCreate: func(c *Ctx, obj Obj) error {
			if str(obj["scm_type"]) == "" {
				return nil
			}
			id, _ := api.ID(obj["id"])
			lp := ProjectLocalPath(id, str(obj["name"]))
			if _, err := c.DB().Exec(c, "UPDATE unified_job_templates SET local_path = $2 WHERE id = $1", id, lp); err != nil {
				return err
			}
			obj["local_path"] = lp
			_, err := LaunchProjectUpdate(c, obj, "manual", "check", "")
			return err
		},
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			if str(obj["scm_type"]) == "" {
				return nil
			}
			for _, f := range []string{"scm_type", "scm_url", "scm_branch", "scm_refspec", "credential", "scm_track_submodules"} {
				if fmt.Sprint(old[f]) != fmt.Sprint(obj[f]) {
					if str(obj["local_path"]) == "" {
						id, _ := api.ID(obj["id"])
						obj["local_path"] = ProjectLocalPath(id, str(obj["name"]))
						c.DB().Exec(c, "UPDATE unified_job_templates SET local_path = $2 WHERE id = $1", id, obj["local_path"])
					}
					_, err := LaunchProjectUpdate(c, obj, "manual", "check", "")
					return err
				}
			}
			return nil
		},
		BeforeDelete: func(c *Ctx, obj Obj) error {
			if countQuery(c, "SELECT count(*) FROM unified_jobs WHERE unified_job_template_id = $1 AND status IN ('pending','waiting','running')", obj["id"]) > 0 {
				return api.Detail(409, "Resource is being used by running jobs.")
			}
			return nil
		},
		Serialize: func(c *Ctx, o Obj) {
			o["last_update_failed"] = o["last_job_failed"]
			o["last_updated"] = o["last_job_run"]
			o["custom_virtualenv"] = nil
		},
		Summary: ujtSummary,
	})
	proj.SubLists = append([]*SubList{
		{
			Name: "project_updates", Target: "project_updates", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) },
		},
	}, notificationSubs("project", false)...)
	proj.Actions = []*api.Action{
		{Name: "update", Methods: []string{"GET", "POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			ok, err := api.HasPerm(c, proj, obj, "update")
			if err != nil {
				api.WriteError(w, err)
				return
			}
			can := ok && str(obj["scm_type"]) != ""
			if r.Method == "GET" {
				api.WriteJSON(w, 200, Obj{"can_update": can})
				return
			}
			if !ok {
				api.WriteError(w, api.ErrForbidden)
				return
			}
			if !can {
				api.WriteError(w, api.Detail(405, "Manual projects cannot be updated."))
				return
			}
			id, err := LaunchProjectUpdate(c, obj, "manual", "check", "")
			if err != nil {
				api.WriteError(w, err)
				return
			}
			pu, err := api.Get(c, api.Lookup("project_updates"), id)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			out, err := api.SerializeOne(c, api.Lookup("project_updates"), pu)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			out["project_update"] = id
			api.WriteJSON(w, 202, out)
		}},
		{Name: "playbooks", Methods: []string{"GET"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			files, _ := projectFiles(c, obj)
			api.WriteJSON(w, 200, files.Playbooks)
		}},
		{Name: "inventories", Methods: []string{"GET"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			files, _ := projectFiles(c, obj)
			api.WriteJSON(w, 200, files.Inventories)
		}},
	}

	registerJobResource(jobSpec{
		name: "project_updates", typ: "project_update", verbose: "Project Update",
		templateField: "project", templateRes: "projects", eventsName: "events", eventsRes: "project_update_events",
		visible: func(c *Ctx, arg ArgFunc) string {
			return "t.unified_job_template_id IN (SELECT t.id FROM unified_job_templates t WHERE t.type = 'project' AND (" + viewOf(c, proj, arg) + "))"
		},
		canManage: func(c *Ctx, job Obj) (bool, error) {
			pid, ok := api.ID(job["project"])
			if !ok {
				return false, nil
			}
			return api.HasPermID(c, "projects", pid, "change")
		},
		canCancel: func(c *Ctx, job Obj) (bool, error) {
			pid, ok := api.ID(job["project"])
			if !ok {
				return false, nil
			}
			return api.HasPermID(c, "projects", pid, "update")
		},
		extra: []*Field{
			{Name: "job_type", Type: api.TChoice, ReadOnly: true, Choices: labelled("run", "Run", "check", "Check")},
			{Name: "scm_type", Type: api.TString, ReadOnly: true},
			{Name: "scm_url", Type: api.TString, ReadOnly: true},
			{Name: "scm_branch", Type: api.TString, ReadOnly: true},
			{Name: "scm_refspec", Type: api.TString, ReadOnly: true},
			{Name: "scm_clean", Type: api.TBool, ReadOnly: true},
			{Name: "scm_track_submodules", Type: api.TBool, ReadOnly: true},
			{Name: "scm_delete_on_update", Type: api.TBool, ReadOnly: true},
			{Name: "credential", Type: api.TFK, Ref: "credentials", ReadOnly: true, Nullable: true},
			{Name: "timeout", Type: api.TInt, ReadOnly: true},
			{Name: "scm_revision", Type: api.TString, ReadOnly: true},
			{Name: "local_path", Type: api.TString, ReadOnly: true},
		},
	})
	registerEventResource("project_update_events", "project_update_event", "project_update", "project_update")
}

// projectFiles lists playbooks and inventory files: from the last sync for git
// projects, or by scanning the directory of manual projects.
func projectFiles(c *Ctx, obj Obj) (projectscan.Files, error) {
	if str(obj["scm_type"]) != "" {
		var files projectscan.Files
		for _, p := range asList(obj["playbook_files"]) {
			files.Playbooks = append(files.Playbooks, p)
		}
		for _, p := range asList(obj["inventory_files"]) {
			files.Inventories = append(files.Inventories, p)
		}
		if files.Playbooks == nil {
			files.Playbooks = []string{}
		}
		if files.Inventories == nil {
			files.Inventories = []string{}
		}
		return files, nil
	}
	lp := str(obj["local_path"])
	if lp == "" {
		return projectscan.Files{Playbooks: []string{}, Inventories: []string{}}, nil
	}
	return projectscan.Scan(filepath.Join(c.App.Cfg.ProjectsRoot, lp), c.App.Settings.Bool(c, "AWX_SHOW_PLAYBOOK_LINKS"))
}

func asList(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
