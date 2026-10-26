package ansible

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/resources"
	"gopkg.in/yaml.v3"
)

// Requirements are the directories holding a project's installed roles and
// collections, ready to be mounted into a run.
type Requirements struct {
	Roles, Collections string
}

// Mount adds the installed requirements to a run.
func (r *Requirements) Mount(run *Run) {
	if r == nil {
		return
	}
	if r.Collections != "" {
		run.Mounts = append(run.Mounts, Mount{Prefix: "requirements_collections", Path: r.Collections})
	}
	if r.Roles != "" {
		run.Mounts = append(run.Mounts, Mount{Prefix: "requirements_roles", Path: r.Roles})
	}
}

// findRequirements lists requirement files of a project checkout, like AWX:
// roles/requirements.yml, collections/requirements.yml and a top-level
// requirements.yml holding roles, collections or both.
func findRequirements(dir string) (roles, collections []string) {
	first := func(names ...string) string {
		for _, n := range names {
			if st, err := os.Stat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
				return n
			}
		}
		return ""
	}
	if f := first("roles/requirements.yml", "roles/requirements.yaml"); f != "" {
		roles = append(roles, f)
	}
	if f := first("collections/requirements.yml", "collections/requirements.yaml"); f != "" {
		collections = append(collections, f)
	}
	if f := first("requirements.yml", "requirements.yaml"); f != "" {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		var doc any
		if yaml.Unmarshal(b, &doc) == nil {
			switch d := doc.(type) {
			case []any:
				roles = append(roles, f)
			case map[string]any:
				if _, ok := d["roles"]; ok {
					roles = append(roles, f)
				}
				if _, ok := d["collections"]; ok {
					collections = append(collections, f)
				}
			}
		}
	}
	return
}

var requirementLocks sync.Map

// galaxyEnv configures ansible-galaxy with the organization's Galaxy credentials, in order.
func galaxyEnv(ctx context.Context, app *api.App, org int64) (map[string]string, string, error) {
	env := map[string]string{}
	var extra map[string]any
	app.Settings.Decode(ctx, "GALAXY_TASK_ENV", &extra)
	for k, v := range extra {
		env[k] = fmt.Sprint(v)
	}
	rows, err := app.DB.Query(ctx, `SELECT credential_id, c.modified FROM organization_galaxy_credentials g JOIN credentials c ON c.id = g.credential_id
		WHERE g.organization_id = $1 ORDER BY g.position, g.credential_id`, org)
	if err != nil {
		return nil, "", err
	}
	type cred struct {
		id  int64
		mod time.Time
	}
	var creds []cred
	for rows.Next() {
		var c cred
		rows.Scan(&c.id, &c.mod)
		creds = append(creds, c)
	}
	rows.Close()
	var names []string
	var fingerprint strings.Builder
	for i, c := range creds {
		rc, err := resources.ResolveCredential(ctx, app, c.id)
		if err != nil {
			return nil, "", err
		}
		name := fmt.Sprintf("server%d", i)
		up := strings.ToUpper(name)
		url, _ := rc.Inputs["url"].(string)
		env["ANSIBLE_GALAXY_SERVER_"+up+"_URL"] = url
		if v, _ := rc.Inputs["auth_url"].(string); v != "" {
			env["ANSIBLE_GALAXY_SERVER_"+up+"_AUTH_URL"] = v
		}
		if v, _ := rc.Inputs["token"].(string); v != "" {
			env["ANSIBLE_GALAXY_SERVER_"+up+"_TOKEN"] = v
		}
		names = append(names, name)
		fmt.Fprintf(&fingerprint, "%d@%s;", c.id, c.mod.UTC().Format(time.RFC3339Nano))
	}
	if len(names) > 0 {
		env["ANSIBLE_GALAXY_SERVER_LIST"] = strings.Join(names, ",")
	}
	return env, fingerprint.String(), nil
}

// InstallRequirements installs a project checkout's roles and collections in
// the execution environment of row, reusing a cache keyed by the requirement
// files' content and the Galaxy configuration. It returns nil when the project
// has no requirements.
func InstallRequirements(ctx context.Context, app *api.App, out *jobs.Output, row jobs.Row, projectID int64, projectDir string) (*Requirements, error) {
	roles, colls := findRequirements(projectDir)
	if !app.Settings.Bool(ctx, "AWX_ROLES_ENABLED") {
		roles = nil
	}
	if !app.Settings.Bool(ctx, "AWX_COLLECTIONS_ENABLED") {
		colls = nil
	}
	if len(roles) == 0 && len(colls) == 0 {
		return nil, nil
	}
	var org int64
	app.DB.QueryRow(ctx, "SELECT coalesce(organization_id, 0) FROM unified_job_templates WHERE id = $1", projectID).Scan(&org)
	env, fingerprint, err := galaxyEnv(ctx, app, org)
	if err != nil {
		return nil, err
	}
	ignoreCerts := app.Settings.Bool(ctx, "GALAXY_IGNORE_CERTS")

	h := sha256.New()
	fmt.Fprintf(h, "v1|%s|%v|", fingerprint, ignoreCerts)
	for _, set := range [][]string{roles, colls} {
		for _, f := range set {
			b, err := os.ReadFile(filepath.Join(projectDir, f))
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(h, "%s|%d|", f, len(b))
			h.Write(b)
		}
		h.Write([]byte("||"))
	}
	key := hex.EncodeToString(h.Sum(nil))[:24]
	base := filepath.Join(app.Cfg.ProjectsRoot, ".requirements", fmt.Sprint(projectID))
	dir := filepath.Join(base, key)
	mu, _ := requirementLocks.LoadOrStore(dir, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	result := func() *Requirements {
		r := &Requirements{}
		if len(roles) > 0 {
			r.Roles = filepath.Join(dir, "roles")
		}
		if len(colls) > 0 {
			r.Collections = filepath.Join(dir, "collections")
		}
		now := time.Now()
		os.Chtimes(dir, now, now)
		return r
	}
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		out.Line(fmt.Sprintf("Using cached project requirements (%s)", key[:12]))
		return result(), nil
	}

	ident := fmt.Sprintf("%d_requirements", row.ID())
	run, err := NewRun(app, ident)
	if err != nil {
		return nil, err
	}
	defer run.Cleanup()
	// No inventory: ansible-runner would pass -i to ansible-galaxy.
	os.RemoveAll(filepath.Join(run.Dir, "inventory"))
	run.Mounts = append(run.Mounts, Mount{Prefix: "project", Path: projectDir})
	dest := run.Path(fmt.Sprintf("artifacts/%s/requirements", ident))
	var script []string
	script = append(script, "set -e")
	flags := ""
	if ignoreCerts {
		flags = " --ignore-certs"
	}
	for _, f := range roles {
		script = append(script, fmt.Sprintf("ansible-galaxy role install -r %s -p %s --force%s",
			ShellQuote([]string{run.Path("project/" + f)}), ShellQuote([]string{dest + "/roles"}), flags))
	}
	for _, f := range colls {
		script = append(script, fmt.Sprintf("ansible-galaxy collection install -r %s -p %s --force%s",
			ShellQuote([]string{run.Path("project/" + f)}), ShellQuote([]string{dest + "/collections"}), flags))
	}
	if err := run.WriteJSON("env/envvars", env); err != nil {
		return nil, err
	}
	run.WriteJSON("env/settings", map[string]any{"suppress_ansible_output": false, "suppress_output_file": true})
	run.Kwargs = map[string]any{"ident": ident, "binary": "sh", "cmdline": ShellQuote([]string{"-c", strings.Join(script, "\n")})}
	if err := setImage(ctx, app, run, row); err != nil {
		return nil, err
	}
	if t := app.Settings.Int(ctx, "DEFAULT_PROJECT_UPDATE_TIMEOUT"); t > 0 {
		run.Timeout = time.Duration(t) * time.Second
	}
	out.Line("Installing project requirements: " + strings.Join(append(append([]string{}, roles...), colls...), ", "))
	oc, err := run.Execute(ctx, app, out)
	if err != nil {
		return nil, err
	}
	if oc.Status != "successful" {
		return nil, fmt.Errorf("installing requirements %s", oc.Status)
	}
	src := filepath.Join(oc.Artifacts, "requirements")
	if _, err := os.Stat(src); err != nil {
		src = filepath.Join(oc.Artifacts, ident, "requirements")
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("storing requirements: %w", err)
	}
	for _, d := range []string{"roles", "collections"} {
		os.MkdirAll(filepath.Join(tmp, d), 0o755)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644); err != nil {
		return nil, err
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
	}
	pruneRequirements(base, 3)
	return result(), nil
}

// pruneRequirements keeps the most recently used cache entries of a project.
func pruneRequirements(base string, keep int) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	type ent struct {
		name string
		mod  time.Time
	}
	var list []ent
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		if info, err := e.Info(); err == nil {
			list = append(list, ent{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].mod.After(list[j].mod) })
	for i := keep; i < len(list); i++ {
		os.RemoveAll(filepath.Join(base, list[i].name))
	}
}

// copyTree copies a directory tree, preserving file modes and symlinks.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			o, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(o, in); err != nil {
				o.Close()
				return err
			}
			return o.Close()
		}
		return nil
	})
}
