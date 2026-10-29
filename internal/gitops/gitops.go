// Package gitops reconciles objects declared in YAML files of a git project
// into an organization (and optionally a folder). Objects created by a
// source carry managed_by "gitops:<source id>" and can only be changed by
// that source. A sync is atomic: either every document applies or nothing
// changes. Supported kinds: Label, Project, Inventory (with inline Ansible
// YAML inventory content), InventorySource, JobTemplate,
// WorkflowJobTemplate (with nodes), NotificationTemplate, Schedule, Team.
//
//	kind: JobTemplate
//	name: Deploy web
//	spec:
//	  project: web-app          # references by name within the organization
//	  inventory: Production
//	  playbook: deploy.yml
//	  credentials: [prod-ssh]
//	  labels: [web]
package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/runners/ansible"
	"github.com/damhau/awxnext/internal/runners/gitsync"
	"github.com/damhau/awxnext/internal/vars"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

type Obj = api.Obj

// Document is one declared object.
type Document struct {
	Kind        string         `yaml:"kind" json:"kind"`
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description,omitempty"`
	Spec        map[string]any `yaml:"spec" json:"spec,omitempty"`
	// Content is an Ansible YAML inventory (Inventory kind).
	Content map[string]any `yaml:"content" json:"content,omitempty"`
	// Nodes of a WorkflowJobTemplate.
	Nodes []Node `yaml:"nodes" json:"nodes,omitempty"`
	// Members (usernames) of a Team.
	Members []string `yaml:"members" json:"members,omitempty"`
	File    string   `yaml:"-" json:"file"`
}

// Node is a workflow node.
type Node struct {
	ID       string         `yaml:"id" json:"id"`
	Template *Ref           `yaml:"template" json:"template,omitempty"`
	Approval *Approval      `yaml:"approval" json:"approval,omitempty"`
	Success  []string       `yaml:"success" json:"success,omitempty"`
	Failure  []string       `yaml:"failure" json:"failure,omitempty"`
	Always   []string       `yaml:"always" json:"always,omitempty"`
	Converge bool           `yaml:"all_parents_must_converge" json:"all_parents_must_converge,omitempty"`
	Prompts  map[string]any `yaml:",inline" json:"prompts,omitempty"`
}

// Ref names another declared or existing object.
type Ref struct {
	Kind      string `yaml:"kind" json:"kind"`
	Name      string `yaml:"name" json:"name"`
	Inventory string `yaml:"inventory" json:"inventory,omitempty"`
}

// Approval declares an approval node.
type Approval struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	Timeout     int    `yaml:"timeout" json:"timeout,omitempty"`
}

// Change is one planned or applied change.
type Change struct {
	Action string   `json:"action"` // create, update, delete, unchanged, error
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	ID     int64    `json:"id,omitempty"`
	Fields []string `json:"fields,omitempty"`
	Error  string   `json:"error,omitempty"`
	File   string   `json:"file,omitempty"`
}

// Report describes a sync.
type Report struct {
	Revision string         `json:"revision"`
	DryRun   bool           `json:"dry_run"`
	Changes  []Change       `json:"changes"`
	Summary  map[string]int `json:"summary"`
	Error    string         `json:"error,omitempty"`
}

type kind struct {
	res string
	// scope column besides organization identifying objects by name.
	identity func(r *reconciler, d *Document) (string, []any, error)
}

// order is the apply order (dependencies first); pruning runs in reverse.
var order = []string{"Label", "Team", "Project", "Inventory", "InventorySource", "NotificationTemplate", "JobTemplate", "WorkflowJobTemplate", "Schedule"}

var kinds = map[string]kind{
	"Label":                {res: "labels"},
	"Team":                 {res: "teams"},
	"Project":              {res: "projects"},
	"Inventory":            {res: "inventories"},
	"InventorySource":      {res: "inventory_sources", identity: inventorySourceIdentity},
	"NotificationTemplate": {res: "notification_templates"},
	"JobTemplate":          {res: "job_templates"},
	"WorkflowJobTemplate":  {res: "workflow_job_templates"},
	"Schedule":             {res: "schedules", identity: scheduleIdentity},
}

var errDryRun = errors.New("dry run")

// Source is a gitops_sources row.
type Source struct {
	ID, Org                           int64
	Folder, Project                   *int64
	Name, Path                        string
	Prune, Adopt, Enabled, SyncOnProj bool
}

// Tag is the managed_by value of a source.
func Tag(id int64) string { return fmt.Sprintf("gitops:%d", id) }

var locks sync.Map

// Reconcile syncs a source. With dryRun the changes are computed and rolled back.
func Reconcile(ctx context.Context, app *api.App, id int64, dryRun bool) (*Report, error) {
	mu, _ := locks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	var s Source
	err := app.DB.QueryRow(ctx, `SELECT id, name, organization_id, folder_id, project_id, path, prune, adopt_existing, enabled, sync_on_project_update
		FROM gitops_sources WHERE id = $1`, id).Scan(&s.ID, &s.Name, &s.Org, &s.Folder, &s.Project, &s.Path, &s.Prune, &s.Adopt, &s.Enabled, &s.SyncOnProj)
	if err != nil {
		return nil, api.ErrNotFound
	}
	rep := &Report{DryRun: dryRun, Summary: map[string]int{}}
	dir, rev, err := checkout(ctx, app, s.Project)
	rep.Revision = rev
	if err == nil {
		var docs []*Document
		if docs, err = Load(filepath.Join(dir, filepath.FromSlash(path.Clean("/"+s.Path)))); err == nil {
			err = apply(ctx, app, &s, docs, rep, dryRun)
		}
	}
	for i, ch := range rep.Changes {
		rep.Summary[ch.Action]++
		if dryRun && ch.Action == "create" {
			rep.Changes[i].ID = 0
		}
	}
	if err != nil && !errors.Is(err, errDryRun) {
		rep.Error = err.Error()
	}
	if !dryRun {
		status, msg := "successful", ""
		if rep.Error != "" {
			status, msg = "failed", rep.Error
		}
		b, _ := json.Marshal(rep)
		app.DB.Exec(context.Background(), `UPDATE gitops_sources SET status = $2, last_error = $3, last_report = $4, last_sync = now(),
			last_revision = CASE WHEN $2 = 'successful' THEN $5 ELSE last_revision END WHERE id = $1`, s.ID, status, msg, string(b), rev)
	}
	if rep.Error != "" {
		return rep, errors.New(rep.Error)
	}
	return rep, nil
}

// checkout returns the directory of the project's current revision.
func checkout(ctx context.Context, app *api.App, project *int64) (string, string, error) {
	if project == nil {
		return "", "", errors.New("the source has no project")
	}
	var scmType, url, localPath, rev string
	var credID *int64
	err := app.DB.QueryRow(ctx, "SELECT scm_type, scm_url, local_path, scm_revision, credential_id FROM unified_job_templates WHERE id = $1 AND type = 'project'",
		*project).Scan(&scmType, &url, &localPath, &rev, &credID)
	if err != nil {
		return "", "", errors.New("the source's project no longer exists")
	}
	if scmType == "" {
		if localPath == "" || strings.Contains(localPath, "..") {
			return "", "", errors.New("the manual project has no local path")
		}
		return filepath.Join(app.Cfg.ProjectsRoot, localPath), "", nil
	}
	if rev == "" {
		return "", "", errors.New("the project has not been synced yet")
	}
	if dir, ok := revisionDir(app, *project, rev); ok {
		return dir, rev, nil
	}
	src := gitsync.Source{ProjectID: *project, URL: url, Branch: rev}
	if credID != nil {
		src.CredentialID = *credID
	}
	sha, dir, err := gitsync.SyncProject(ctx, app, scmType, src, func(string) {})
	return dir, sha, err
}

// Load reads all YAML documents below dir.
func Load(dir string) ([]*Document, error) {
	var docs []*Document
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("path %s not found in the project", filepath.Base(dir))
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext != ".yml" && ext != ".yaml" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		dec := yaml.NewDecoder(bytes.NewReader(b))
		for n := 1; ; n++ {
			var node yaml.Node
			if err := dec.Decode(&node); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return fmt.Errorf("%s: %w", rel, err)
			}
			// Playbooks, variable files and other YAML without a kind are not declarations.
			if !hasKind(&node) {
				continue
			}
			var doc Document
			if err := node.Decode(&doc); err != nil {
				return fmt.Errorf("%s#%d: %w", filepath.ToSlash(rel), n, err)
			}
			doc.File = fmt.Sprintf("%s#%d", filepath.ToSlash(rel), n)
			if _, ok := kinds[doc.Kind]; !ok {
				return fmt.Errorf("%s: unsupported kind %q", doc.File, doc.Kind)
			}
			if doc.Name == "" {
				return fmt.Errorf("%s: name is required", doc.File)
			}
			doc.Spec = normalize(doc.Spec).(map[string]any)
			doc.Content, _ = normalize(doc.Content).(map[string]any)
			docs = append(docs, &doc)
		}
		return nil
	})
	return docs, err
}

func hasKind(n *yaml.Node) bool {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "kind" {
			return true
		}
	}
	return false
}

func normalize(v any) any {
	switch x := v.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		for k, e := range x {
			if e == nil {
				x[k] = nil
				continue
			}
			x[k] = normalize(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = normalize(e)
		}
	}
	return v
}

type reconciler struct {
	c       *api.Ctx
	src     *Source
	tag     string
	rep     *Report
	touched map[string]map[int64]bool // resource -> ids declared
	errs    []string
}

func apply(ctx context.Context, app *api.App, s *Source, docs []*Document, rep *Report, dryRun bool) error {
	tag := Tag(s.ID)
	c := &api.Ctx{Context: ctx, App: app, User: rbac.System(), Extra: map[string]any{"managed_by": tag}}
	seen := map[string]string{}
	for _, d := range docs {
		key := d.Kind + "/" + d.Name
		if d.Kind == "Schedule" || d.Kind == "InventorySource" {
			key += "/" + fmt.Sprint(d.Spec["template"], d.Spec["inventory"])
		}
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("%s: %s %q is also declared in %s", d.File, d.Kind, d.Name, prev)
		}
		seen[key] = d.File
	}
	err := c.WithTx(func(c *api.Ctx) error {
		r := &reconciler{c: c, src: s, tag: tag, rep: rep, touched: map[string]map[int64]bool{}}
		for _, k := range order {
			for _, d := range docs {
				if d.Kind != k {
					continue
				}
				if err := r.applyDoc(d); err != nil {
					r.errs = append(r.errs, fmt.Sprintf("%s %q (%s): %v", d.Kind, d.Name, d.File, err))
					rep.Changes = append(rep.Changes, Change{Action: "error", Kind: d.Kind, Name: d.Name, File: d.File, Error: err.Error()})
				}
			}
		}
		if len(r.errs) > 0 {
			return errors.New(strings.Join(r.errs, "; "))
		}
		if s.Prune {
			if err := r.prune(); err != nil {
				return err
			}
		}
		if dryRun {
			return errDryRun
		}
		return nil
	})
	return err
}

func (r *reconciler) touch(res string, id int64) {
	if r.touched[res] == nil {
		r.touched[res] = map[int64]bool{}
	}
	r.touched[res][id] = true
}

// lookup finds an object of res by name in the source organization.
func (r *reconciler) lookup(resName, name string, extra string, args ...any) (int64, error) {
	res := api.Lookup(resName)
	where := "name = $1"
	params := []any{name}
	if res.Discriminator {
		params = append(params, res.Type)
		where += fmt.Sprintf(" AND type = $%d", len(params))
	}
	if hasField(res, "organization") && resName != "inventory_sources" && resName != "schedules" {
		params = append(params, r.src.Org)
		where += fmt.Sprintf(" AND organization_id = $%d", len(params))
	}
	if extra != "" {
		for _, a := range args {
			params = append(params, a)
			extra = strings.Replace(extra, "?", fmt.Sprintf("$%d", len(params)), 1)
		}
		where += " AND " + extra
	}
	var id int64
	err := r.c.DB().QueryRow(r.c, "SELECT id FROM "+res.Table+" WHERE "+where+" ORDER BY id LIMIT 1", params...).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("%s %q not found", strings.TrimSuffix(resName, "s"), name)
	}
	return id, nil
}

func hasField(res *api.Resource, name string) bool {
	for _, f := range res.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

func field(res *api.Resource, name string) *api.Field {
	for _, f := range res.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// refByName resolves a reference field given by name.
func (r *reconciler) refByName(f *api.Field, v any) (any, error) {
	name, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("%s must be referenced by name", f.Name)
	}
	switch f.Ref {
	case "credential_types", "instance_groups":
		var id int64
		if err := r.c.DB().QueryRow(r.c, "SELECT id FROM "+api.Lookup(f.Ref).Table+" WHERE name = $1 ORDER BY id LIMIT 1", name).Scan(&id); err != nil {
			return nil, fmt.Errorf("%s %q not found", f.Name, name)
		}
		return id, nil
	case "execution_environments":
		var id int64
		if err := r.c.DB().QueryRow(r.c, "SELECT id FROM execution_environments WHERE name = $1 AND (organization_id = $2 OR organization_id IS NULL) ORDER BY organization_id NULLS LAST LIMIT 1",
			name, r.src.Org).Scan(&id); err != nil {
			return nil, fmt.Errorf("execution environment %q not found", name)
		}
		return id, nil
	case "unified_job_templates":
		return nil, fmt.Errorf("use template: {kind, name} to reference templates")
	}
	return r.lookup(f.Ref, name, "")
}

func inventorySourceIdentity(r *reconciler, d *Document) (string, []any, error) {
	inv, ok := d.Spec["inventory"].(string)
	if !ok {
		return "", nil, errors.New("spec.inventory is required")
	}
	id, err := r.lookup("inventories", inv, "")
	if err != nil {
		return "", nil, err
	}
	return "inventory_id = ?", []any{id}, nil
}

func scheduleIdentity(r *reconciler, d *Document) (string, []any, error) {
	ref, err := templateRef(d.Spec["template"])
	if err != nil {
		return "", nil, err
	}
	id, err := r.resolveTemplate(ref)
	if err != nil {
		return "", nil, err
	}
	return "unified_job_template_id = ?", []any{id}, nil
}

func templateRef(v any) (*Ref, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("template must be {kind, name}")
	}
	ref := &Ref{Kind: fmt.Sprint(m["kind"]), Name: fmt.Sprint(m["name"])}
	if inv, ok := m["inventory"].(string); ok {
		ref.Inventory = inv
	}
	return ref, nil
}

func (r *reconciler) resolveTemplate(ref *Ref) (int64, error) {
	switch ref.Kind {
	case "JobTemplate":
		return r.lookup("job_templates", ref.Name, "")
	case "WorkflowJobTemplate":
		return r.lookup("workflow_job_templates", ref.Name, "")
	case "Project":
		return r.lookup("projects", ref.Name, "")
	case "InventorySource":
		inv, err := r.lookup("inventories", ref.Inventory, "")
		if err != nil {
			return 0, err
		}
		return r.lookup("inventory_sources", ref.Name, "inventory_id = ?", inv)
	}
	return 0, fmt.Errorf("unsupported template kind %q", ref.Kind)
}

// listRefs resolves a list of names (credentials, labels, instance groups).
func (r *reconciler) listRefs(resName string, v any) ([]int64, error) {
	if v == nil {
		return []int64{}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of names", resName)
	}
	out := []int64{}
	for _, x := range list {
		name := fmt.Sprint(x)
		var id int64
		var err error
		if resName == "instance_groups" {
			err = r.c.DB().QueryRow(r.c, "SELECT id FROM instance_groups WHERE name = $1", name).Scan(&id)
		} else {
			id, err = r.lookup(resName, name, "")
		}
		if err != nil {
			return nil, fmt.Errorf("%s %q not found", strings.TrimSuffix(resName, "s"), name)
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// relations handled outside the resource fields.
var relationKeys = map[string]bool{"credentials": true, "labels": true, "instance_groups": true, "survey_spec": true, "template": true}

func (r *reconciler) applyDoc(d *Document) error {
	k := kinds[d.Kind]
	res := api.Lookup(k.res)
	data := Obj{"name": d.Name}
	if d.Description != "" || hasField(res, "description") {
		data["description"] = d.Description
	}
	for key, v := range d.Spec {
		if relationKeys[key] {
			continue
		}
		f := field(res, key)
		if f == nil || f.ReadOnly || f.Name == "id" {
			return fmt.Errorf("unknown or read-only field %q", key)
		}
		if f.Type == api.TFK && v != nil {
			id, err := r.refByName(f, v)
			if err != nil {
				return err
			}
			v = id
		}
		if (f.Type == api.TText || f.Type == api.TString) && key != "name" {
			if m, ok := v.(map[string]any); ok {
				// Variables may be written as YAML mappings.
				b, _ := json.Marshal(m)
				v = string(b)
			}
		}
		data[key] = v
	}
	if f := field(res, "organization"); f != nil && !f.ReadOnly {
		data["organization"] = r.src.Org
	}
	if f := field(res, "folder"); f != nil && r.src.Folder != nil {
		data["folder"] = *r.src.Folder
	}
	if d.Kind == "Inventory" && d.Content != nil {
		// all.vars of the content are the inventory variables.
		if all, ok := d.Content["all"].(map[string]any); ok && all["vars"] != nil {
			if _, dup := d.Spec["variables"]; dup {
				return errors.New("set inventory variables in spec.variables or content all.vars, not both")
			}
			b, _ := json.Marshal(all["vars"])
			data["variables"] = string(b)
		}
	}
	if d.Kind == "Schedule" {
		ref, err := templateRef(d.Spec["template"])
		if err != nil {
			return err
		}
		id, err := r.resolveTemplate(ref)
		if err != nil {
			return err
		}
		data["unified_job_template"] = id
	}
	identity, args := "", []any{}
	if k.identity != nil {
		var err error
		if identity, args, err = k.identity(r, d); err != nil {
			return err
		}
	}
	ch := Change{Kind: d.Kind, Name: d.Name, File: d.File}
	var obj Obj
	if oid, err := r.lookup(k.res, d.Name, identity, args...); err == nil {
		old, err := api.Get(r.c, res, oid)
		if err != nil {
			return err
		}
		owner, _ := old["managed_by"].(string)
		switch {
		case owner == r.tag:
		case owner == "" && r.src.Adopt:
			ch.Fields = append(ch.Fields, "managed_by")
			if _, err := r.c.DB().Exec(r.c, "UPDATE "+res.Table+" SET managed_by = $2 WHERE id = $1", oid, r.tag); err != nil {
				return err
			}
		case owner == "":
			return errors.New("an object with this name exists and is not managed by this source (enable adopt_existing to take it over)")
		default:
			return fmt.Errorf("the object is managed by %s", owner)
		}
		if _, isCreateOnly := data["unified_job_template"]; isCreateOnly && fmt.Sprint(old["unified_job_template"]) == fmt.Sprint(data["unified_job_template"]) {
			delete(data, "unified_job_template")
		}
		if d.Kind == "InventorySource" {
			delete(data, "inventory")
		}
		resetUndeclared(res, data)
		vals, err := api.Validate(r.c, res, data, false, old)
		if err != nil {
			return describe(err)
		}
		if cfg, ok := old["notification_configuration"].(map[string]any); ok {
			// Compare webhook passwords in clear text.
			if p, _ := cfg["password"].(string); crypto.IsEncrypted(p) {
				if plain, err := r.c.App.Crypto.Decrypt(p); err == nil {
					cfg = maps.Clone(cfg)
					cfg["password"] = plain
					old = maps.Clone(old)
					old["notification_configuration"] = cfg
				}
			}
		}
		for k, v := range vals {
			if !same(old[k], v) {
				ch.Fields = append(ch.Fields, k)
			}
		}
		obj = old
		if len(ch.Fields) > 0 {
			if obj, err = api.Apply(r.c, res, old, vals); err != nil {
				return describe(err)
			}
		}
		ch.Action, ch.ID = "update", oid
	} else {
		created, err := api.Create(r.c, res, data, Obj{"managed_by": r.tag})
		if err != nil {
			return describe(err)
		}
		obj = created
		ch.Action, ch.ID = "create", id(created["id"])
	}
	r.touch(k.res, ch.ID)
	extra, err := r.relations(d, res, obj)
	if err != nil {
		return err
	}
	ch.Fields = append(ch.Fields, extra...)
	if ch.Action == "update" && len(ch.Fields) == 0 {
		ch.Action = "unchanged"
	}
	sort.Strings(ch.Fields)
	r.rep.Changes = append(r.rep.Changes, ch)
	return nil
}

func id(v any) int64 { n, _ := api.ID(v); return n }

// resetUndeclared returns writable fields missing from a declaration to their
// defaults, so removing a line from a file reverts the setting.
func resetUndeclared(res *api.Resource, data Obj) {
	for _, f := range res.Fields {
		if _, ok := data[f.Name]; ok || f.ReadOnly || f.CreateOnly || f.Virtual || f.WriteOnly || f.Hidden || f.Required || f.Type == api.TSecret {
			continue
		}
		switch f.Name {
		case "organization", "folder", "inventory", "unified_job_template", "webhook_key":
			continue
		}
		switch {
		case f.Default != nil:
			// Copy so mutable defaults ({} and []) are never shared.
			var v any
			b, _ := json.Marshal(f.Default)
			json.Unmarshal(b, &v)
			data[f.Name] = v
		case f.Nullable || f.Type == api.TFK:
			data[f.Name] = nil
		}
	}
}

// same compares an existing value with a validated new one.
func same(a, b any) bool {
	if fmt.Sprint(a) == fmt.Sprint(b) {
		return true
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) == string(jb) {
		return true
	}
	var x, y any
	json.Unmarshal(ja, &x)
	json.Unmarshal(jb, &y)
	return reflect.DeepEqual(x, y)
}

func describe(err error) error {
	if e, ok := err.(*api.Error); ok {
		b, _ := json.Marshal(e.Body)
		return errors.New(string(b))
	}
	return err
}

// relations applies credentials, labels, instance groups, surveys, workflow
// nodes, inventory content and team members. It returns the changed relations.
func (r *reconciler) relations(d *Document, res *api.Resource, obj Obj) ([]string, error) {
	oid := id(obj["id"])
	var changed []string
	setList := func(name, table, col, other string, want []int64) error {
		var cur []int64
		rows, err := r.c.DB().Query(r.c, fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1 ORDER BY 1", other, table, col), oid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x int64
			rows.Scan(&x)
			cur = append(cur, x)
		}
		rows.Close()
		if cur == nil {
			cur = []int64{}
		}
		if reflect.DeepEqual(cur, want) {
			return nil
		}
		changed = append(changed, name)
		if _, err := r.c.DB().Exec(r.c, fmt.Sprintf("DELETE FROM %s WHERE %s = $1", table, col), oid); err != nil {
			return err
		}
		for i, x := range want {
			q := fmt.Sprintf("INSERT INTO %s (%s, %s) VALUES ($1, $2)", table, col, other)
			args := []any{oid, x}
			if table == "instance_group_links" {
				q = "INSERT INTO instance_group_links (content_type, object_id, instance_group_id, position) VALUES ($3, $1, $2, $4)"
				args = append(args, res.ContentType, i)
			}
			if _, err := r.c.DB().Exec(r.c, q, args...); err != nil {
				return err
			}
		}
		return nil
	}
	if d.Kind == "JobTemplate" {
		creds, err := r.listRefs("credentials", d.Spec["credentials"])
		if err != nil {
			return nil, err
		}
		if err := setList("credentials", "unified_job_template_credentials", "unified_job_template_id", "credential_id", creds); err != nil {
			return nil, err
		}
	}
	if d.Kind == "JobTemplate" || d.Kind == "WorkflowJobTemplate" {
		labels, err := r.listRefs("labels", d.Spec["labels"])
		if err != nil {
			return nil, err
		}
		if err := setList("labels", "unified_job_template_labels", "unified_job_template_id", "label_id", labels); err != nil {
			return nil, err
		}
		if _, ok := d.Spec["survey_spec"]; ok || d.Spec["survey_enabled"] != nil {
			spec, _ := d.Spec["survey_spec"].(map[string]any)
			if spec == nil {
				spec = map[string]any{}
			}
			var cur map[string]any
			r.c.DB().QueryRow(r.c, "SELECT survey_spec FROM unified_job_templates WHERE id = $1", oid).Scan(&cur)
			if !same(r.plainSurvey(cur), spec) {
				valid, err := resources.ValidateSurveySpec(r.c, spec, nil)
				if err != nil {
					return nil, fmt.Errorf("survey_spec: %v", describe(err))
				}
				changed = append(changed, "survey_spec")
				b, _ := json.Marshal(valid)
				if _, err := r.c.DB().Exec(r.c, "UPDATE unified_job_templates SET survey_spec = $2 WHERE id = $1", oid, string(b)); err != nil {
					return nil, err
				}
			}
		}
	}
	if d.Kind == "JobTemplate" || d.Kind == "Inventory" {
		if v, ok := d.Spec["instance_groups"]; ok {
			igs, err := r.listRefs("instance_groups", v)
			if err != nil {
				return nil, err
			}
			ct := map[string]string{"JobTemplate": "jobtemplate", "Inventory": "inventory"}[d.Kind]
			var cur []int64
			rows, _ := r.c.DB().Query(r.c, "SELECT instance_group_id FROM instance_group_links WHERE content_type = $1 AND object_id = $2 ORDER BY position", ct, oid)
			for rows.Next() {
				var x int64
				rows.Scan(&x)
				cur = append(cur, x)
			}
			rows.Close()
			if !reflect.DeepEqual(cur, igs) && !(len(cur) == 0 && len(igs) == 0) {
				changed = append(changed, "instance_groups")
				r.c.DB().Exec(r.c, "DELETE FROM instance_group_links WHERE content_type = $1 AND object_id = $2", ct, oid)
				for i, g := range igs {
					r.c.DB().Exec(r.c, "INSERT INTO instance_group_links (content_type, object_id, instance_group_id, position) VALUES ($1, $2, $3, $4)", ct, oid, g, i)
				}
			}
		}
	}
	switch d.Kind {
	case "Inventory":
		if d.Content != nil {
			ok, err := r.inventoryContent(oid, d.Content)
			if err != nil {
				return nil, fmt.Errorf("content: %w", err)
			}
			if ok {
				changed = append(changed, "content")
			}
		}
	case "WorkflowJobTemplate":
		ok, err := r.workflowNodes(oid, d.Nodes)
		if err != nil {
			return nil, fmt.Errorf("nodes: %w", err)
		}
		if ok {
			changed = append(changed, "nodes")
		}
	case "Team":
		if d.Members != nil {
			ok, err := r.teamMembers(oid, d.Members)
			if err != nil {
				return nil, err
			}
			if ok {
				changed = append(changed, "members")
			}
		}
	}
	return changed, nil
}

// plainSurvey returns a stored survey spec with password defaults decrypted.
func (r *reconciler) plainSurvey(spec map[string]any) map[string]any {
	if spec == nil {
		return map[string]any{}
	}
	qs, _ := spec["spec"].([]any)
	for _, q := range qs {
		if m, ok := q.(map[string]any); ok && m["type"] == "password" {
			if s, _ := m["default"].(string); crypto.IsEncrypted(s) {
				if p, err := r.c.App.Crypto.Decrypt(s); err == nil {
					m["default"] = p
				}
			}
		}
	}
	return spec
}

// inventoryContent makes the inventory's hosts and groups match an Ansible
// YAML inventory. It reports whether anything changed.
func (r *reconciler) inventoryContent(inv int64, content map[string]any) (bool, error) {
	data, err := ansible.YAMLInventory(content)
	if err != nil {
		return false, err
	}
	// Inventory variables are set from the declaration, not by the import.
	var cur string
	if err := r.c.DB().QueryRow(r.c, "SELECT variables FROM inventories WHERE id = $1", inv).Scan(&cur); err != nil {
		return false, err
	}
	if data.AllVars, err = vars.Parse(cur); err != nil || data.AllVars == nil {
		data.AllVars = map[string]any{}
	}
	before, err := r.inventoryDigest(inv)
	if err != nil {
		return false, err
	}
	tx, ok := r.c.Tx.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return false, errors.New("no transaction")
	}
	if _, err := ansible.ImportInventory(r.c, tx, inv, 0, data, ansible.ImportOptions{Overwrite: true, OverwriteVars: true}); err != nil {
		return false, err
	}
	groupNames := make([]string, 0, len(data.Groups))
	for g := range data.Groups {
		groupNames = append(groupNames, g)
	}
	hostNames := append([]string{}, data.Hosts...)
	if _, err := r.c.DB().Exec(r.c, "DELETE FROM hosts WHERE inventory_id = $1 AND name <> ALL($2)", inv, hostNames); err != nil {
		return false, err
	}
	if _, err := r.c.DB().Exec(r.c, "DELETE FROM groups WHERE inventory_id = $1 AND name <> ALL($2)", inv, groupNames); err != nil {
		return false, err
	}
	after, err := r.inventoryDigest(inv)
	return before != after, err
}

// inventoryDigest fingerprints an inventory's variables, hosts, groups and memberships.
func (r *reconciler) inventoryDigest(inv int64) (string, error) {
	var d string
	err := r.c.DB().QueryRow(r.c, `SELECT md5(concat_ws('|',
		(SELECT variables FROM inventories WHERE id = $1),
		(SELECT string_agg(name || '=' || variables || enabled::text, ',' ORDER BY name) FROM hosts WHERE inventory_id = $1),
		(SELECT string_agg(name || '=' || variables, ',' ORDER BY name) FROM groups WHERE inventory_id = $1),
		(SELECT string_agg(g.name || '>' || h.name, ',' ORDER BY g.name, h.name) FROM group_hosts gh
			JOIN groups g ON g.id = gh.group_id JOIN hosts h ON h.id = gh.host_id WHERE g.inventory_id = $1),
		(SELECT string_agg(p.name || '>' || c.name, ',' ORDER BY p.name, c.name) FROM group_children gc
			JOIN groups p ON p.id = gc.parent_id JOIN groups c ON c.id = gc.child_id WHERE p.inventory_id = $1)))`, inv).Scan(&d)
	return d, err
}

type nodeState struct {
	Template string         `json:"t"`
	Approval *Approval      `json:"a,omitempty"`
	Edges    [][2]string    `json:"e"`
	Converge bool           `json:"c"`
	Prompts  map[string]any `json:"p"`
}

// workflowNodes replaces the workflow graph when it differs from the declaration.
func (r *reconciler) workflowNodes(wf int64, nodes []Node) (bool, error) {
	want := map[string]nodeState{}
	ids := map[string]bool{}
	for _, n := range nodes {
		if n.ID == "" {
			return false, errors.New("every node needs an id")
		}
		if ids[n.ID] {
			return false, fmt.Errorf("duplicate node id %q", n.ID)
		}
		ids[n.ID] = true
	}
	tplIDs := map[string]int64{}
	for _, n := range nodes {
		st := nodeState{Converge: n.Converge, Prompts: n.Prompts, Approval: n.Approval}
		if st.Prompts == nil {
			st.Prompts = map[string]any{}
		}
		switch {
		case n.Template != nil:
			tid, err := r.resolveTemplate(n.Template)
			if err != nil {
				return false, fmt.Errorf("node %s: %w", n.ID, err)
			}
			tplIDs[n.ID] = tid
			st.Template = fmt.Sprint(tid)
		case n.Approval != nil:
			st.Template = "approval"
		default:
			return false, fmt.Errorf("node %s needs a template or an approval", n.ID)
		}
		for kind, list := range map[string][]string{"success": n.Success, "failure": n.Failure, "always": n.Always} {
			for _, to := range list {
				if !ids[to] {
					return false, fmt.Errorf("node %s: unknown target node %q", n.ID, to)
				}
				st.Edges = append(st.Edges, [2]string{kind, to})
			}
		}
		sort.Slice(st.Edges, func(i, j int) bool { return fmt.Sprint(st.Edges[i]) < fmt.Sprint(st.Edges[j]) })
		want[n.ID] = st
	}
	// Current graph.
	cur := map[string]nodeState{}
	rows, err := r.c.DB().Query(r.c, `SELECT n.id, n.identifier, coalesce(n.unified_job_template_id, 0), coalesce(u.type, ''), coalesce(u.name, ''),
		coalesce(u.description, ''), coalesce(u.timeout, 0), n.all_parents_must_converge, n.extra_data, n.inventory_id, n.execution_environment_id,
		n.scm_branch, n.job_type, n.job_tags, n.skip_tags, n."limit", n.diff_mode, n.verbosity, n.forks, n.job_slice_count, n.timeout,
		n.when_expr, n.loop_expr, n.loop_var, n.loop_concurrency
		FROM workflow_job_template_nodes n LEFT JOIN unified_job_templates u ON u.id = n.unified_job_template_id WHERE n.workflow_job_template_id = $1`, wf)
	if err != nil {
		return false, err
	}
	byID := map[int64]string{}
	var oldApprovals []int64
	for rows.Next() {
		var nid, tid, timeout int64
		var ident, typ, name, desc string
		var converge bool
		var extra map[string]any
		var inv, ee *int64
		var scm, jt, tags, skip, limit *string
		var diff *bool
		var verb, forks, slices, tout *int64
		var when, loop, loopVar string
		var loopConcurrency int64
		if err := rows.Scan(&nid, &ident, &tid, &typ, &name, &desc, &timeout, &converge, &extra, &inv, &ee, &scm, &jt, &tags, &skip, &limit, &diff, &verb, &forks, &slices, &tout,
			&when, &loop, &loopVar, &loopConcurrency); err != nil {
			rows.Close()
			return false, err
		}
		byID[nid] = ident
		st := nodeState{Converge: converge, Template: fmt.Sprint(tid), Prompts: map[string]any{}}
		if typ == "workflow_approval_template" {
			st.Template = "approval"
			st.Approval = &Approval{Name: name, Description: desc, Timeout: int(timeout)}
			oldApprovals = append(oldApprovals, tid)
		}
		if len(extra) > 0 {
			st.Prompts["extra_data"] = extra
		}
		for k, v := range map[string]any{"scm_branch": scm, "job_type": jt, "job_tags": tags, "skip_tags": skip, "limit": limit} {
			if p := v.(*string); p != nil {
				st.Prompts[k] = *p
			}
		}
		if diff != nil {
			st.Prompts["diff_mode"] = *diff
		}
		for k, v := range map[string]string{"when": when, "loop": loop, "loop_var": loopVar} {
			if v != "" {
				st.Prompts[k] = v
			}
		}
		if loopConcurrency != 0 {
			st.Prompts["loop_concurrency"] = loopConcurrency
		}
		for k, v := range map[string]*int64{"verbosity": verb, "forks": forks, "job_slice_count": slices, "timeout": tout} {
			if v != nil {
				st.Prompts[k] = *v
			}
		}
		if inv != nil {
			var n string
			r.c.DB().QueryRow(r.c, "SELECT name FROM inventories WHERE id = $1", *inv).Scan(&n)
			st.Prompts["inventory"] = n
		}
		if ee != nil {
			var n string
			r.c.DB().QueryRow(r.c, "SELECT name FROM execution_environments WHERE id = $1", *ee).Scan(&n)
			st.Prompts["execution_environment"] = n
		}
		cur[ident] = st
	}
	rows.Close()
	erows, err := r.c.DB().Query(r.c, `SELECT e.from_node_id, e.to_node_id, e.kind FROM workflow_job_template_node_edges e
		JOIN workflow_job_template_nodes n ON n.id = e.from_node_id WHERE n.workflow_job_template_id = $1`, wf)
	if err != nil {
		return false, err
	}
	for erows.Next() {
		var f, t int64
		var kind string
		erows.Scan(&f, &t, &kind)
		st := cur[byID[f]]
		st.Edges = append(st.Edges, [2]string{kind, byID[t]})
		cur[byID[f]] = st
	}
	erows.Close()
	for k, st := range cur {
		sort.Slice(st.Edges, func(i, j int) bool { return fmt.Sprint(st.Edges[i]) < fmt.Sprint(st.Edges[j]) })
		cur[k] = st
	}
	if same(cur, want) {
		return false, nil
	}
	// Replace the graph.
	if _, err := r.c.DB().Exec(r.c, "DELETE FROM workflow_job_template_nodes WHERE workflow_job_template_id = $1", wf); err != nil {
		return false, err
	}
	if len(oldApprovals) > 0 {
		r.c.DB().Exec(r.c, "DELETE FROM unified_job_templates WHERE id = ANY($1) AND type = 'workflow_approval_template'", oldApprovals)
	}
	nodeRes := api.Lookup("workflow_job_template_nodes")
	newIDs := map[string]int64{}
	for _, n := range nodes {
		data := Obj{"workflow_job_template": wf, "identifier": n.ID, "all_parents_must_converge": n.Converge}
		for k, v := range n.Prompts {
			f := field(nodeRes, k)
			if f == nil || f.ReadOnly {
				return false, fmt.Errorf("node %s: unknown field %q", n.ID, k)
			}
			if f.Type == api.TFK && v != nil {
				ref, err := r.refByName(f, v)
				if err != nil {
					return false, fmt.Errorf("node %s: %w", n.ID, err)
				}
				v = ref
			}
			data[k] = v
		}
		if n.Template != nil {
			data["unified_job_template"] = tplIDs[n.ID]
		} else {
			var org any = r.src.Org
			var aid int64
			if err := r.c.DB().QueryRow(r.c, `INSERT INTO unified_job_templates (type, name, description, timeout, organization_id, status, managed_by)
				VALUES ('workflow_approval_template', $1, $2, $3, $4, 'never updated', $5) RETURNING id`,
				n.Approval.Name, n.Approval.Description, n.Approval.Timeout, org, r.tag).Scan(&aid); err != nil {
				return false, err
			}
			data["unified_job_template"] = aid
		}
		created, err := api.Create(r.c, nodeRes, data, nil)
		if err != nil {
			return false, fmt.Errorf("node %s: %v", n.ID, describe(err))
		}
		newIDs[n.ID] = id(created["id"])
	}
	for _, n := range nodes {
		for kind, list := range map[string][]string{"success": n.Success, "failure": n.Failure, "always": n.Always} {
			for _, to := range list {
				if _, err := r.c.DB().Exec(r.c, "INSERT INTO workflow_job_template_node_edges (from_node_id, to_node_id, kind) VALUES ($1, $2, $3)",
					newIDs[n.ID], newIDs[to], kind); err != nil {
					return false, err
				}
			}
		}
	}
	return true, nil
}

// teamMembers sets the users holding Team Member on a team (direct grants).
func (r *reconciler) teamMembers(team int64, members []string) (bool, error) {
	want := map[int64]bool{}
	for _, u := range members {
		var uid int64
		if err := r.c.DB().QueryRow(r.c, "SELECT id FROM users WHERE username = $1", u).Scan(&uid); err != nil {
			return false, fmt.Errorf("user %q not found", u)
		}
		want[uid] = true
	}
	cur := map[int64]bool{}
	rows, err := r.c.DB().Query(r.c, `SELECT ra.user_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
		WHERE rd.name = 'Team Member' AND ra.content_type = 'team' AND ra.object_id = $1 AND ra.user_id IS NOT NULL`, team)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var uid int64
		rows.Scan(&uid)
		cur[uid] = true
	}
	rows.Close()
	if reflect.DeepEqual(cur, want) {
		return false, nil
	}
	for uid := range cur {
		if !want[uid] {
			r.c.DB().Exec(r.c, `DELETE FROM role_assignments WHERE content_type = 'team' AND object_id = $1 AND user_id = $2
				AND role_definition_id = (SELECT id FROM role_definitions WHERE name = 'Team Member')`, team, uid)
		}
	}
	for uid := range want {
		if !cur[uid] {
			if _, err := r.c.DB().Exec(r.c, `INSERT INTO role_assignments (role_definition_id, user_id, content_type, object_id)
				SELECT id, $1, 'team', $2 FROM role_definitions WHERE name = 'Team Member' ON CONFLICT DO NOTHING`, uid, team); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// prune deletes objects of this source that are no longer declared.
func (r *reconciler) prune() error {
	for i := len(order) - 1; i >= 0; i-- {
		k := kinds[order[i]]
		res := api.Lookup(k.res)
		where := "managed_by = $1"
		if res.Discriminator {
			where += " AND type = '" + res.Type + "'"
		}
		rows, err := r.c.DB().Query(r.c, "SELECT id FROM "+res.Table+" WHERE "+where+" ORDER BY id", r.tag)
		if err != nil {
			return err
		}
		var stale []int64
		for rows.Next() {
			var x int64
			rows.Scan(&x)
			if !r.touched[k.res][x] {
				stale = append(stale, x)
			}
		}
		rows.Close()
		for _, x := range stale {
			obj, err := api.Get(r.c, res, x)
			if err != nil {
				continue
			}
			if err := api.Delete(r.c, res, obj); err != nil {
				return fmt.Errorf("deleting %s %q: %v", order[i], obj["name"], describe(err))
			}
			r.rep.Changes = append(r.rep.Changes, Change{Action: "delete", Kind: order[i], Name: fmt.Sprint(obj["name"]), ID: x})
		}
	}
	return nil
}
