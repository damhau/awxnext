package resources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/vars"
)

// maxCommits is how many of the project's commits a job's changes list.
const maxCommits = 50

// maxHostNames is how many added or removed hosts a job's changes name.
const maxHostNames = 200

// changeRun is what the changes of a job compare in one run.
type changeRun struct {
	ID          int64
	Type        string
	Status      string
	Created     time.Time
	Finished    *time.Time
	UJT         int64
	Project     int64
	SliceNumber int
	Revision    string
	Image       string
	Digest      string
	ExtraVars   string
	Passwords   map[string]any
	Inputs      map[string]any
}

func loadChangeRun(c *Ctx, id int64) (*changeRun, error) {
	r := &changeRun{Inputs: map[string]any{}}
	var inv, ee int64
	var jobType, limit, tags, skip, startAt, branch string
	var forks, verbosity, timeout, sliceCount int
	var diff bool
	var pw []byte
	err := c.DB().QueryRow(c, `SELECT id, type, status, created, finished, coalesce(unified_job_template_id, 0), coalesce(project_id, 0),
		coalesce(inventory_id, 0), coalesce(execution_environment_id, 0), job_slice_number, scm_revision, scm_branch,
		execution_environment_image, execution_environment_digest, extra_vars, survey_passwords,
		job_type, "limit", job_tags, skip_tags, start_at_task, forks, verbosity, diff_mode, timeout, job_slice_count
		FROM unified_jobs WHERE id = $1`, id).Scan(&r.ID, &r.Type, &r.Status, &r.Created, &r.Finished, &r.UJT, &r.Project,
		&inv, &ee, &r.SliceNumber, &r.Revision, &branch, &r.Image, &r.Digest, &r.ExtraVars, &pw,
		&jobType, &limit, &tags, &skip, &startAt, &forks, &verbosity, &diff, &timeout, &sliceCount)
	if err != nil {
		return nil, api.ErrNotFound
	}
	json.Unmarshal(pw, &r.Passwords)
	in := r.Inputs
	in["inventory"], in["execution_environment"] = nilZero(inv), nilZero(ee)
	in["scm_branch"], in["job_type"], in["limit"], in["job_tags"], in["skip_tags"], in["start_at_task"] = branch, jobType, limit, tags, skip, startAt
	in["forks"], in["verbosity"], in["timeout"], in["job_slice_count"], in["diff_mode"] = forks, verbosity, timeout, sliceCount, diff
	creds, err := int64s(c, "SELECT credential_id FROM unified_job_credentials WHERE unified_job_id = $1 ORDER BY credential_id", id)
	if err != nil {
		return nil, err
	}
	in["credentials"] = creds
	groups, err := int64s(c, "SELECT instance_group_id FROM instance_group_links WHERE content_type = 'unifiedjob' AND object_id = $1 ORDER BY position", id)
	if err != nil {
		return nil, err
	}
	in["instance_groups"] = groups
	return r, nil
}

func nilZero(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func int64s(c *Ctx, q string, args ...any) ([]int64, error) {
	rows, err := c.DB().Query(c, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *changeRun) ref(reason string) Obj {
	o := Obj{"id": r.ID, "type": r.Type, "status": r.Status, "created": r.Created, "finished": r.Finished}
	if reason != "" {
		o["reason"] = reason
	}
	return o
}

// comparedRun picks the run a job's changes compare with: ?since= when
// given, else the template's last good run (as M11-04's change events
// compare with), else its previous run. Only runs of the same kind and
// slice count, and only those the user can see.
func comparedRun(c *Ctx, job *changeRun, since string) (cmp, lastOK, prev *changeRun, reason string, err error) {
	if job.UJT != 0 {
		if lastOK, err = latestRun(c, job, "status = 'successful'"); err != nil {
			return
		}
		if prev, err = latestRun(c, job, "status IN ('successful', 'failed', 'error')"); err != nil {
			return
		}
	}
	if since != "" {
		id, perr := strconv.ParseInt(since, 10, 64)
		var other *changeRun
		if perr == nil && id != job.ID {
			other, _ = loadChangeRun(c, id)
		}
		if other == nil || other.UJT != job.UJT || job.UJT == 0 || other.Type != job.Type || other.Finished == nil {
			err = api.FieldError("since", "Choose another finished run of the same template.")
			return
		}
		if ok, verr := api.CanView(c, api.Lookup(jobResName(other.Type)), other.ID); verr != nil || !ok {
			err = api.FieldError("since", "Choose another finished run of the same template.")
			return
		}
		return other, lastOK, prev, "chosen", nil
	}
	switch {
	case lastOK != nil:
		return lastOK, lastOK, prev, "last_success", nil
	case prev != nil:
		return prev, lastOK, prev, "previous", nil
	}
	return nil, nil, nil, "", nil
}

func latestRun(c *Ctx, job *changeRun, status string) (*changeRun, error) {
	until := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	if job.Finished != nil {
		until = *job.Finished
	}
	ids, err := int64s(c, `SELECT id FROM unified_jobs WHERE unified_job_template_id = $1 AND type = $2 AND job_slice_number = $3
		AND finished IS NOT NULL AND (finished, id) < ($4, $5) AND `+status+` ORDER BY finished DESC, id DESC LIMIT 20`,
		job.UJT, job.Type, job.SliceNumber, until, job.ID)
	if err != nil {
		return nil, err
	}
	res := api.Lookup(jobResName(job.Type))
	for _, id := range ids {
		if ok, err := api.CanView(c, res, id); err != nil {
			return nil, err
		} else if ok {
			return loadChangeRun(c, id)
		}
	}
	return nil, nil
}

// handleJobChanges serves /jobs/N/changes/ and /workflow_jobs/N/changes/:
// what differs between the job and an earlier run of its template.
func handleJobChanges(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	id, _ := api.ID(obj["id"])
	out, err := jobChanges(c, id, r.URL.Query().Get("since"))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, out)
}

func jobChanges(c *Ctx, id int64, since string) (Obj, error) {
	job, err := loadChangeRun(c, id)
	if err != nil {
		return nil, err
	}
	cmp, lastOK, prev, reason, err := comparedRun(c, job, since)
	if err != nil {
		return nil, err
	}
	out := Obj{"job": job.ref(""), "compared_with": nil, "last_success": nil, "previous": nil, "count": 0,
		"template": nil, "project": nil, "inventory": nil, "credentials": []Obj{}, "execution_environment": nil,
		"inputs": []Obj{}, "hosts": nil, "nodes": []Obj{}}
	if lastOK != nil {
		out["last_success"] = lastOK.ref("")
	}
	if prev != nil {
		out["previous"] = prev.ref("")
	}
	if cmp == nil {
		return out, nil
	}
	out["compared_with"] = cmp.ref(reason)
	d := &differ{c: c, before: cmp, after: job}
	count := 0

	tplRes := "job_templates"
	if job.Type == "workflow_job" {
		tplRes = "workflow_job_templates"
	}
	tpl, tplSnaps, err := d.object(tplRes, job.UJT)
	if err != nil {
		return nil, err
	}
	if tpl != nil {
		out["template"] = tpl
		count++
	}

	inputs, err := d.inputs(tplSnaps)
	if err != nil {
		return nil, err
	}
	out["inputs"] = inputs
	count += len(inputs)

	if inv, ok := job.Inputs["inventory"].(int64); ok && cmp.Inputs["inventory"] == job.Inputs["inventory"] {
		o, _, err := d.object("inventories", inv)
		if err != nil {
			return nil, err
		}
		if o != nil {
			out["inventory"] = o
			count++
		}
	}
	var creds []Obj
	for _, cid := range job.Inputs["credentials"].([]int64) {
		if !slices.Contains(cmp.Inputs["credentials"].([]int64), cid) {
			continue
		}
		o, _, err := d.object("credentials", cid)
		if err != nil {
			return nil, err
		}
		if o != nil {
			creds = append(creds, o)
		}
	}
	if creds != nil {
		out["credentials"] = creds
		count += len(creds)
	}

	if job.Type == "job" {
		p, err := d.project()
		if err != nil {
			return nil, err
		}
		if p != nil {
			out["project"] = p
			count++
		}
		ee, err := d.image()
		if err != nil {
			return nil, err
		}
		if ee != nil {
			out["execution_environment"] = ee
			count++
		}
		h, err := d.hosts()
		if err != nil {
			return nil, err
		}
		if h != nil {
			out["hosts"] = h
			count++
		}
	}
	if job.Type == "workflow_job" {
		nodes, n, err := d.nodes()
		if err != nil {
			return nil, err
		}
		out["nodes"] = nodes
		count += n
	}
	out["count"] = count
	return out, nil
}

// differ compares two runs of a template: before is the run compared with.
type differ struct {
	c             *Ctx
	before, after *changeRun
}

// window is the time between the two runs' launches: an edit made in it
// can explain a difference.
func (d *differ) window() (time.Time, time.Time) {
	a, b := d.before.Created, d.after.Created
	if b.Before(a) {
		a, b = b, a
	}
	return a, b
}

// snapshots are an object's versions as each run found it at launch.
type snapshots struct{ before, after Obj }

func (s snapshots) value(when string, key string) (any, bool) {
	m := s.before
	if when == "after" {
		m = s.after
	}
	if m == nil {
		return nil, false
	}
	v, ok := m[key]
	return v, ok
}

func (d *differ) snapshotAt(res string, id int64, at time.Time) (Obj, int, error) {
	var data []byte
	var version int
	err := d.c.DB().QueryRow(d.c, `SELECT data, version FROM object_versions WHERE resource = $1 AND object_id = $2 AND created <= $3
		ORDER BY version DESC LIMIT 1`, res, id, at).Scan(&data, &version)
	if err != nil {
		return nil, 0, nil
	}
	var snap Obj
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, 0, err
	}
	return snap, version, nil
}

// object is how one object both runs used was edited between them: who
// changed it, when, and its fields before and after. Nil when it wasn't.
// Someone who can't read the object sees only that it changed.
func (d *differ) object(resName string, id int64) (Obj, snapshots, error) {
	var snaps snapshots
	res := api.Lookup(resName)
	if res == nil || id == 0 {
		return nil, snaps, nil
	}
	obj, err := api.Get(d.c, res, id)
	if errors.Is(err, api.ErrNotFound) {
		return nil, snaps, nil
	} else if err != nil {
		return nil, snaps, err
	}
	var fromV, toV int
	if snaps.before, fromV, err = d.snapshotAt(resName, id, d.before.Created); err != nil {
		return nil, snaps, err
	}
	if snaps.after, toV, err = d.snapshotAt(resName, id, d.after.Created); err != nil {
		return nil, snaps, err
	}
	lo, hi := d.window()
	rows, err := d.c.DB().Query(d.c, `SELECT v.version, v.created, v.operation, v.comment, v.changes, coalesce(v.actor_id, 0), coalesce(u.username, '')
		FROM object_versions v LEFT JOIN users u ON u.id = v.actor_id
		WHERE v.resource = $1 AND v.object_id = $2 AND v.created > $3 AND v.created <= $4 ORDER BY v.version`, resName, id, lo, hi)
	if err != nil {
		return nil, snaps, err
	}
	var versions []Obj
	for rows.Next() {
		var version int
		var created time.Time
		var op, comment, username string
		var changes []byte
		var actor int64
		if err := rows.Scan(&version, &created, &op, &comment, &changes, &actor, &username); err != nil {
			rows.Close()
			return nil, snaps, err
		}
		var ch map[string]any
		json.Unmarshal(changes, &ch)
		var fields []string
		for k := range ch {
			if editable(res, k) {
				fields = append(fields, k)
			}
		}
		slices.Sort(fields)
		if len(fields) == 0 && comment == "" && op == "update" {
			continue
		}
		v := Obj{"version": version, "created": created, "operation": op, "comment": comment, "fields": orEmpty(fields), "actor": nil}
		if actor != 0 {
			v["actor"] = Obj{"id": actor, "username": username}
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, snaps, err
	}
	var fields []Obj
	if snaps.before != nil && snaps.after != nil {
		fields = fieldChanges(res, snaps.before, snaps.after)
	}
	if len(versions) == 0 || (fields != nil && len(fields) == 0) {
		return nil, snaps, nil
	}
	out := Obj{"kind": res.Type, "id": id, "name": str(obj["name"]), "readable": false}
	readable, err := api.CanView(d.c, res, id)
	if err != nil {
		return nil, snaps, err
	}
	if readable && res.HistoryPerm != "" {
		if readable, err = api.HasPerm(d.c, res, obj, res.HistoryPerm); err != nil {
			return nil, snaps, err
		}
	}
	if !readable {
		return out, snaps, nil
	}
	out["readable"] = true
	out["versions"] = versions
	out["from_version"], out["to_version"] = fromV, toV
	out["fields"] = orEmpty(fields)
	return out, snaps, nil
}

// editable reports whether a snapshot key is something people edit, not
// bookkeeping such as status or the last job.
func editable(res *api.Resource, key string) bool {
	switch key {
	case "id", "type", "url", "related", "summary_fields", "created", "modified", "created_by", "modified_by":
		return false
	}
	for _, f := range res.Fields {
		if f.Name == key {
			return !f.ReadOnly && !f.Hidden
		}
	}
	return true
}

func fieldDef(res *api.Resource, key string) *api.Field {
	for _, f := range res.Fields {
		if f.Name == key {
			return f
		}
	}
	return nil
}

// fieldChanges are the edited fields that differ between two snapshots.
// Secret values stay hidden: such a field only says it changed.
func fieldChanges(res *api.Resource, before, after Obj) []Obj {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		if editable(res, k) {
			sorted = append(sorted, k)
		}
	}
	slices.Sort(sorted)
	out := []Obj{}
	for _, k := range sorted {
		b, a := before[k], after[k]
		if same(b, a) {
			continue
		}
		f := fieldDef(res, k)
		switch {
		case f != nil && (f.Type == api.TSecret || f.WriteOnly):
			out = append(out, secretChange(k, ""))
		case f != nil && f.Redact != nil:
			bm, _ := b.(map[string]any)
			am, _ := a.(map[string]any)
			rb, _ := f.Redact(b).(map[string]any)
			ra, _ := f.Redact(a).(map[string]any)
			sub := map[string]bool{}
			for x := range bm {
				sub[x] = true
			}
			for x := range am {
				sub[x] = true
			}
			subs := make([]string, 0, len(sub))
			for x := range sub {
				subs = append(subs, x)
			}
			slices.Sort(subs)
			for _, x := range subs {
				if same(bm[x], am[x]) {
					continue
				}
				if same(rb[x], ra[x]) || isSecretValue(bm[x]) || isSecretValue(am[x]) {
					out = append(out, secretChange(k, x))
				} else {
					out = append(out, Obj{"field": k, "key": x, "before": rb[x], "after": ra[x]})
				}
			}
		default:
			out = append(out, Obj{"field": k, "before": b, "after": a})
		}
	}
	return out
}

func secretChange(field, key string) Obj {
	o := Obj{"field": field, "before": crypto.Placeholder, "after": crypto.Placeholder, "secret": true}
	if key != "" {
		o["key"] = key
	}
	return o
}

func isSecretValue(v any) bool {
	s, ok := v.(string)
	return ok && (crypto.IsEncrypted(s) || s == crypto.Placeholder)
}

// same compares values as JSON, so that 3 and 3.0 or []int64 and []any agree.
func same(a, b any) bool {
	return string(jsonOf(a)) == string(jsonOf(b))
}

func jsonOf(v any) []byte {
	b, _ := json.Marshal(v)
	var n any
	json.Unmarshal(b, &n)
	b, _ = json.Marshal(n)
	return b
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// inputs are the launch values that differ between the runs: prompted
// fields, credentials, extra variables (one key at a time) and instance
// groups. A difference the template's own edit explains, such as its new
// limit, shows with the template, not here.
func (d *differ) inputs(tpl snapshots) ([]Obj, error) {
	out := []Obj{}
	explained := func(key string, b, a any) bool {
		tb, ok1 := tpl.value("before", key)
		ta, ok2 := tpl.value("after", key)
		return ok1 && ok2 && same(tb, b) && same(ta, a)
	}
	for _, k := range []string{"inventory", "credentials", "execution_environment", "instance_groups", "scm_branch", "job_type", "limit",
		"job_tags", "skip_tags", "start_at_task", "forks", "verbosity", "timeout", "job_slice_count", "diff_mode"} {
		b, a := d.before.Inputs[k], d.after.Inputs[k]
		if same(b, a) || explained(k, b, a) {
			continue
		}
		entry := Obj{"field": k, "before": b, "after": a}
		switch k {
		case "inventory", "execution_environment":
			entry["before"], entry["after"] = d.named(k, b), d.named(k, a)
		case "credentials", "instance_groups":
			entry["before"], entry["after"] = d.namedList(k, b), d.namedList(k, a)
		}
		out = append(out, entry)
	}
	ev, err := d.extraVars(tpl)
	if err != nil {
		return nil, err
	}
	return append(out, ev...), nil
}

var namedTables = map[string]string{"inventory": "inventories", "execution_environment": "execution_environments",
	"credentials": "credentials", "instance_groups": "instance_groups"}

func (d *differ) named(field string, v any) any {
	id, ok := v.(int64)
	if !ok {
		return nil
	}
	var name string
	d.c.DB().QueryRow(d.c, "SELECT name FROM "+namedTables[field]+" WHERE id = $1", id).Scan(&name)
	return Obj{"id": id, "name": name}
}

func (d *differ) namedList(field string, v any) []Obj {
	out := []Obj{}
	ids, _ := v.([]int64)
	for _, id := range ids {
		out = append(out, d.named(field, id).(Obj))
	}
	return out
}

// extraVars compares the runs' extra variables key by key. A survey
// password says only that it changed.
func (d *differ) extraVars(tpl snapshots) ([]Obj, error) {
	b, _ := vars.Parse(d.before.ExtraVars)
	a, _ := vars.Parse(d.after.ExtraVars)
	var tb, ta map[string]any
	if s, ok := tpl.value("before", "extra_vars"); ok {
		tb, _ = vars.Parse(str(s))
	}
	if s, ok := tpl.value("after", "extra_vars"); ok {
		ta, _ = vars.Parse(str(s))
	}
	keys := map[string]bool{}
	for k := range b {
		keys[k] = true
	}
	for k := range a {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	slices.Sort(sorted)
	out := []Obj{}
	for _, k := range sorted {
		bv, inB := b[k]
		av, inA := a[k]
		_, pwB := d.before.Passwords[k]
		_, pwA := d.after.Passwords[k]
		if pwA || pwB || isSecretValue(bv) || isSecretValue(av) {
			if inA == inB && d.plain(bv) == d.plain(av) {
				continue
			}
			out = append(out, secretChange("extra_vars", k))
			continue
		}
		if inA == inB && same(bv, av) {
			continue
		}
		if tb != nil && ta != nil {
			tbv, tInB := tb[k]
			tav, tInA := ta[k]
			if tInB == inB && tInA == inA && same(tbv, bv) && same(tav, av) {
				continue
			}
		}
		entry := Obj{"field": "extra_vars", "key": k, "before": bv, "after": av}
		if !inB {
			entry["before"], entry["added"] = nil, true
		}
		if !inA {
			entry["after"], entry["removed"] = nil, true
		}
		out = append(out, entry)
	}
	return out, nil
}

// plain is a survey password as it was answered, to compare two runs'
// answers: each answer is encrypted on its own, so their ciphertexts differ.
func (d *differ) plain(v any) string {
	s, ok := v.(string)
	if !ok {
		return string(jsonOf(v))
	}
	if crypto.IsEncrypted(s) {
		if p, err := d.c.App.Crypto.Decrypt(s); err == nil {
			return p
		}
	}
	return s
}

var revisionRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// project is the project's side of the changes: its edits, and the commits
// between the revisions the runs checked out.
func (d *differ) project() (Obj, error) {
	b, a := d.before, d.after
	if a.Project == 0 {
		return nil, nil
	}
	if b.Project != a.Project {
		return Obj{"id": a.Project, "name": d.projectName(a.Project), "other_project": d.projectRef(b),
			"before": Obj{"revision": b.Revision}, "after": Obj{"revision": a.Revision}, "commits": []Obj{}}, nil
	}
	obj, _, err := d.object("projects", a.Project)
	if err != nil {
		return nil, err
	}
	if obj == nil && b.Revision == a.Revision {
		return nil, nil
	}
	if obj == nil {
		obj = Obj{"kind": "project", "id": a.Project, "name": d.projectName(a.Project), "readable": false}
		ok, err := api.CanView(d.c, api.Lookup("projects"), a.Project)
		if err != nil {
			return nil, err
		}
		obj["readable"] = ok
	}
	obj["before"], obj["after"] = Obj{"revision": b.Revision}, Obj{"revision": a.Revision}
	obj["commits"] = []Obj{}
	if b.Revision == a.Revision || obj["readable"] != true {
		return obj, nil
	}
	var scm string
	d.c.DB().QueryRow(d.c, "SELECT scm_type FROM unified_job_templates WHERE id = $1", a.Project).Scan(&scm)
	if scm != "git" || !revisionRe.MatchString(b.Revision) || !revisionRe.MatchString(a.Revision) {
		return obj, nil
	}
	for k, v := range commitsBetween(d.c, ProjectRepoDir(d.c.App.Cfg.ProjectsRoot, a.Project), b.Revision, a.Revision) {
		obj[k] = v
	}
	return obj, nil
}

func (d *differ) projectName(id int64) string {
	var name string
	d.c.DB().QueryRow(d.c, "SELECT name FROM unified_job_templates WHERE id = $1", id).Scan(&name)
	return name
}

func (d *differ) projectRef(r *changeRun) any {
	if r.Project == 0 {
		return nil
	}
	return Obj{"id": r.Project, "name": d.projectName(r.Project)}
}

// commitsBetween lists, from this node's clone of the project, the commits
// in the newer revision that the older one doesn't have.
func commitsBetween(ctx context.Context, repo, older, newer string) Obj {
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		return Obj{"commits_available": false, "missing": "repository"}
	}
	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.Output()
		return string(out), err
	}
	has := func(rev string) bool {
		_, err := git("cat-file", "-e", rev+"^{commit}")
		return err == nil
	}
	switch hasOld, hasNew := has(older), has(newer); {
	case !hasNew:
		return Obj{"commits_available": false, "missing": "newer"}
	case !hasOld:
		return Obj{"commits_available": false, "missing": "older"}
	}
	out := Obj{"commits_available": true, "diverged": false}
	if _, err := git("merge-base", "--is-ancestor", older, newer); err != nil {
		out["diverged"] = true
	}
	n, err := git("rev-list", "--count", older+".."+newer, "--")
	if err != nil {
		return Obj{"commits_available": false, "missing": "repository"}
	}
	total, _ := strconv.Atoi(strings.TrimSpace(n))
	log, err := git("log", "--no-show-signature", "-n", strconv.Itoa(maxCommits), "--format=%H%x1f%an%x1f%aI%x1f%s%x1e", older+".."+newer, "--")
	if err != nil {
		return Obj{"commits_available": false, "missing": "repository"}
	}
	commits := []Obj{}
	for _, rec := range strings.Split(log, "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) != 4 {
			continue
		}
		commits = append(commits, Obj{"sha": f[0], "author": f[1], "date": f[2], "message": f[3]})
	}
	out["commits"], out["total_commits"] = commits, total
	return out
}

// image is the execution environment's side: its edits, and the image and
// digest each run had, since a floating tag such as :latest hides a new image.
func (d *differ) image() (Obj, error) {
	b, a := d.before, d.after
	eb, ea := b.Inputs["execution_environment"], a.Inputs["execution_environment"]
	var obj Obj
	if id, ok := ea.(int64); ok && same(eb, ea) {
		var err error
		if obj, _, err = d.object("execution_environments", id); err != nil {
			return nil, err
		}
	}
	newImage := b.Image != "" && a.Image != "" && b.Image != a.Image
	newDigest := b.Image == a.Image && b.Digest != "" && a.Digest != "" && b.Digest != a.Digest
	if obj == nil && !newImage && !newDigest {
		return nil, nil
	}
	if obj == nil {
		obj = Obj{"kind": "execution_environment", "readable": true}
		if id, ok := ea.(int64); ok {
			obj["id"] = id
			var name string
			d.c.DB().QueryRow(d.c, "SELECT name FROM execution_environments WHERE id = $1", id).Scan(&name)
			obj["name"] = name
		}
	}
	obj["before"] = Obj{"image": b.Image, "digest": b.Digest}
	obj["after"] = Obj{"image": a.Image, "digest": a.Digest}
	obj["same_tag"] = newDigest
	return obj, nil
}

// hosts are those one run had and the other didn't, when both reported hosts.
func (d *differ) hosts() (Obj, error) {
	names := func(id int64) (map[string]bool, error) {
		rows, err := d.c.DB().Query(d.c, "SELECT DISTINCT host_name FROM job_host_summaries WHERE job_id = $1", id)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return nil, err
			}
			out[n] = true
		}
		return out, rows.Err()
	}
	b, err := names(d.before.ID)
	if err != nil {
		return nil, err
	}
	a, err := names(d.after.ID)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(a) == 0 {
		return nil, nil
	}
	diff := func(x, y map[string]bool) ([]string, int) {
		var out []string
		for n := range x {
			if !y[n] {
				out = append(out, n)
			}
		}
		slices.Sort(out)
		total := len(out)
		if total > maxHostNames {
			out = out[:maxHostNames]
		}
		return orEmpty(out), total
	}
	added, nAdded := diff(a, b)
	removed, nRemoved := diff(b, a)
	if nAdded == 0 && nRemoved == 0 {
		return nil, nil
	}
	return Obj{"added": added, "removed": removed, "added_count": nAdded, "removed_count": nRemoved,
		"before_count": len(b), "after_count": len(a)}, nil
}

// nodes pair the two workflow runs' nodes by identifier, each with the job
// it ran, so that each job's own changes are a link away.
func (d *differ) nodes() ([]Obj, int, error) {
	type node struct {
		ujt                 int64
		name, typ, status   string
		job                 int64
		identifier, jobType string
	}
	load := func(wf int64) ([]node, error) {
		rows, err := d.c.DB().Query(d.c, `SELECT n.identifier, coalesce(n.unified_job_template_id, 0), coalesce(t.name, ''), coalesce(t.type, ''),
			coalesce(n.job_id, 0), coalesce(j.type, ''), coalesce(j.status, '')
			FROM workflow_job_nodes n LEFT JOIN unified_job_templates t ON t.id = n.unified_job_template_id LEFT JOIN unified_jobs j ON j.id = n.job_id
			WHERE n.workflow_job_id = $1 ORDER BY n.id`, wf)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []node
		for rows.Next() {
			var n node
			if err := rows.Scan(&n.identifier, &n.ujt, &n.name, &n.typ, &n.job, &n.jobType, &n.status); err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, rows.Err()
	}
	before, err := load(d.before.ID)
	if err != nil {
		return nil, 0, err
	}
	after, err := load(d.after.ID)
	if err != nil {
		return nil, 0, err
	}
	ref := func(n *node) any {
		if n == nil || n.job == 0 {
			return nil
		}
		return Obj{"id": n.job, "type": n.jobType, "status": n.status}
	}
	tpl := func(n *node) any {
		if n == nil || n.ujt == 0 {
			return nil
		}
		return Obj{"id": n.ujt, "name": n.name, "type": n.typ}
	}
	byID := map[string]*node{}
	for i := range before {
		byID[before[i].identifier] = &before[i]
	}
	out := []Obj{}
	changed := 0
	seen := map[string]bool{}
	for i := range after {
		a := &after[i]
		seen[a.identifier] = true
		b := byID[a.identifier]
		change := ""
		switch {
		case b == nil:
			change = "added"
		case b.ujt != a.ujt:
			change = "template"
		}
		if change != "" {
			changed++
		}
		out = append(out, Obj{"identifier": a.identifier, "change": change, "template": tpl(a), "before_template": tpl(b),
			"before_job": ref(b), "after_job": ref(a)})
	}
	for i := range before {
		b := &before[i]
		if seen[b.identifier] {
			continue
		}
		changed++
		out = append(out, Obj{"identifier": b.identifier, "change": "removed", "template": tpl(b), "before_template": tpl(b),
			"before_job": ref(b), "after_job": nil})
	}
	return out, changed, nil
}

func registerJobChanges() {
	for _, name := range []string{"jobs", "workflow_jobs"} {
		res := api.Lookup(name)
		res.Actions = append(res.Actions, &api.Action{Name: "changes", Methods: []string{"GET"}, Handler: handleJobChanges})
	}
}
