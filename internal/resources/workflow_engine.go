package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/vars"
)

// RegisterJobTypes registers job types driven by the API layer (workflows).
func RegisterJobTypes() {
	jobs.Register(&jobs.TypeSpec{
		Type:         "workflow_job",
		TemplateType: "workflow_job_template",
		InDispatcher: true,
		Serial:       true,
	})
}

// wfNode is a workflow job node with its job state and edges.
type wfNode struct {
	id, ujt, job       int64
	ujtType, jobStatus string
	dnr, converge      bool
	artifacts          map[string]any
	children           map[string][]int64 // edge kind -> child ids
	parents            []int64
}

func finishedStatus(s string) bool {
	return s == "successful" || s == "failed" || s == "error" || s == "canceled"
}
func failedStatus(s string) bool { return s == "failed" || s == "error" || s == "canceled" }

type dag struct {
	nodes map[int64]*wfNode
	order []int64 // node ids by id
}

func loadDAG(c *Ctx, wfID int64) (*dag, error) {
	d := &dag{nodes: map[int64]*wfNode{}}
	rows, err := c.DB().Query(c, `SELECT n.id, coalesce(n.unified_job_template_id, 0), coalesce(u.type, ''), coalesce(n.job_id, 0),
		coalesce(j.status, ''), n.do_not_run, n.all_parents_must_converge, n.ancestor_artifacts
		FROM workflow_job_nodes n LEFT JOIN unified_job_templates u ON u.id = n.unified_job_template_id
		LEFT JOIN unified_jobs j ON j.id = n.job_id WHERE n.workflow_job_id = $1 ORDER BY n.id`, wfID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		n := &wfNode{children: map[string][]int64{}}
		if err := rows.Scan(&n.id, &n.ujt, &n.ujtType, &n.job, &n.jobStatus, &n.dnr, &n.converge, &n.artifacts); err != nil {
			rows.Close()
			return nil, err
		}
		d.nodes[n.id] = n
		d.order = append(d.order, n.id)
	}
	rows.Close()
	rows, err = c.DB().Query(c, `SELECT e.from_node_id, e.to_node_id, e.kind FROM workflow_job_node_edges e
		JOIN workflow_job_nodes n ON n.id = e.from_node_id WHERE n.workflow_job_id = $1 ORDER BY e.from_node_id, e.to_node_id`, wfID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var from, to int64
		var kind string
		rows.Scan(&from, &to, &kind)
		if f, t := d.nodes[from], d.nodes[to]; f != nil && t != nil {
			f.children[kind] = append(f.children[kind], to)
			t.parents = append(t.parents, from)
		}
	}
	rows.Close()
	return d, nil
}

func (n *wfNode) has(kind string, child int64) bool {
	for _, c := range n.children[kind] {
		if c == child {
			return true
		}
	}
	return false
}

// decided: the node will not change its outcome any more.
func (n *wfNode) decided() bool {
	return n.dnr || (n.job != 0 && finishedStatus(n.jobStatus)) || n.ujt == 0
}

// triggers reports whether a finished parent leads to child.
func (n *wfNode) triggers(child int64) bool {
	switch {
	case n.dnr:
		return false
	case n.job != 0 && n.jobStatus == "successful":
		return n.has("success", child) || n.has("always", child)
	case n.job != 0 && failedStatus(n.jobStatus), n.ujt == 0:
		return n.has("failure", child) || n.has("always", child)
	}
	return false
}

func (d *dag) parentsDecided(n *wfNode) bool {
	for _, p := range n.parents {
		if !d.nodes[p].decided() {
			return false
		}
	}
	return true
}

// convergenceMet: every finished parent leads to n (unfinished parents are not counted against it).
func (d *dag) convergenceMet(n *wfNode) bool {
	for _, pid := range n.parents {
		p := d.nodes[pid]
		if p.job != 0 && finishedStatus(p.jobStatus) && !p.triggers(n.id) {
			return false
		}
	}
	return true
}

func (d *dag) relevantParentsFinished(n *wfNode) bool {
	for _, pid := range n.parents {
		p := d.nodes[pid]
		switch {
		case p.dnr, p.ujt == 0:
		case p.job == 0:
			return false
		case !finishedStatus(p.jobStatus):
			return false
		}
	}
	return true
}

// topological returns node ids in dependency order.
func (d *dag) topological() []int64 {
	indeg := map[int64]int{}
	for _, id := range d.order {
		indeg[id] = len(d.nodes[id].parents)
	}
	var queue, out []int64
	for _, id := range d.order {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		out = append(out, id)
		n := d.nodes[id]
		for _, kind := range edgeKinds {
			for _, ch := range n.children[kind] {
				indeg[ch]--
				if indeg[ch] == 0 {
					queue = append(queue, ch)
				}
			}
		}
	}
	return out
}

// markDNR flags nodes that can no longer run.
func (d *dag) markDNR() []int64 {
	var marked []int64
	for _, id := range d.topological() {
		n := d.nodes[id]
		if n.dnr || n.job != 0 || len(n.parents) == 0 {
			continue
		}
		if n.converge {
			anyDNR := false
			for _, p := range n.parents {
				anyDNR = anyDNR || d.nodes[p].dnr
			}
			if anyDNR || !d.convergenceMet(n) {
				n.dnr = true
				marked = append(marked, id)
			}
			continue
		}
		if d.parentsDecided(n) {
			reached := false
			for _, p := range n.parents {
				reached = reached || d.nodes[p].triggers(id)
			}
			if !reached {
				n.dnr = true
				marked = append(marked, id)
			}
		}
	}
	return marked
}

// toRun returns nodes whose jobs should start now (breadth first from the roots).
func (d *dag) toRun() []*wfNode {
	var queue []int64
	for _, id := range d.order {
		if len(d.nodes[id].parents) == 0 {
			queue = append(queue, id)
		}
	}
	visited := map[int64]bool{}
	var out []*wfNode
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		n := d.nodes[id]
		switch {
		case n.dnr:
		case n.job != 0:
			if n.jobStatus == "successful" {
				queue = append(queue, n.children["success"]...)
				queue = append(queue, n.children["always"]...)
			} else if failedStatus(n.jobStatus) {
				queue = append(queue, n.children["failure"]...)
				queue = append(queue, n.children["always"]...)
			}
		case n.ujt == 0:
			queue = append(queue, n.children["failure"]...)
			queue = append(queue, n.children["always"]...)
		case !n.converge:
			if d.relevantParentsFinished(n) {
				out = append(out, n)
			}
		default:
			if d.parentsDecided(n) && d.convergenceMet(n) {
				out = append(out, n)
			}
		}
	}
	return out
}

func (d *dag) done() bool {
	for _, n := range d.nodes {
		if !n.decided() {
			return false
		}
	}
	return true
}

// failed implements AWX's rule: a failed node without failure or always path fails the workflow.
func (d *dag) failed() (bool, string) {
	var failedPath, missing []string
	res := false
	for _, id := range d.order {
		n := d.nodes[id]
		switch {
		case !n.dnr && n.ujt == 0 && !n.converge:
			res = true
			missing = append(missing, fmt.Sprint(n.id))
		case !n.dnr && n.job != 0 && failedStatus(n.jobStatus):
			if len(n.children["failure"]) == 0 && len(n.children["always"]) == 0 {
				res = true
				failedPath = append(failedPath, fmt.Sprintf("(%d, %s)", n.id, n.jobStatus))
			}
		}
	}
	if !res {
		return false, ""
	}
	return true, fmt.Sprintf("No error handling path for workflow job node(s) [%s]. Workflow job node(s) missing unified job template and error handling path [%s].",
		strings.Join(failedPath, ", "), strings.Join(missing, ", "))
}

// WorkflowTick advances running workflow jobs and expires approvals. It runs on the dispatcher leader.
func WorkflowTick(ctx context.Context, m *jobs.Manager) {
	c := &api.Ctx{Context: ctx, App: m.App, User: rbac.System(), Extra: map[string]any{}}
	ids, err := idList(c, "SELECT id FROM unified_jobs WHERE type = 'workflow_job' AND status = 'running' ORDER BY id")
	if err != nil {
		return
	}
	for _, id := range ids {
		if err := processWorkflow(c, m, id); err != nil && ctx.Err() == nil {
			slog.Error("workflow processing failed", "workflow_job", id, "err", err)
		}
	}
	expired, err := idList(c, `SELECT id FROM unified_jobs WHERE type = 'workflow_approval' AND status = 'pending' AND approval_timeout > 0
		AND created + make_interval(secs => approval_timeout) < now()`)
	if err != nil {
		return
	}
	for _, id := range expired {
		var name string
		var timeout int64
		tag, err := m.DB().Exec(ctx, "UPDATE unified_jobs SET timed_out = true, started = created WHERE id = $1 AND status = 'pending' AND NOT timed_out", id)
		if err != nil || tag.RowsAffected() == 0 {
			continue
		}
		m.DB().QueryRow(ctx, "SELECT name, approval_timeout FROM unified_jobs WHERE id = $1", id).Scan(&name, &timeout)
		m.Complete(ctx, id, jobs.Result{Status: "failed", Explanation: fmt.Sprintf("The approval node %s (%d) has expired after %d seconds.", name, id, timeout)})
		notifyApproval(c, id, "timed_out")
	}
}

func idList(c *Ctx, sql string, args ...any) ([]int64, error) {
	rows, err := c.DB().Query(c, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func processWorkflow(c *Ctx, m *jobs.Manager, wfID int64) error {
	wf, err := jobs.LoadRow(c, m.DB(), wfID)
	if err != nil {
		return err
	}
	d, err := loadDAG(c, wfID)
	if err != nil {
		return err
	}
	if wf.Bool("cancel_flag") {
		running := 0
		for _, n := range d.nodes {
			if n.job != 0 && !finishedStatus(n.jobStatus) {
				running++
				m.Cancel(c, n.job)
			}
		}
		if running == 0 {
			return m.Complete(c, wfID, jobs.Result{Status: "canceled", Explanation: "Workflow job was canceled."})
		}
		return nil
	}
	if marked := d.markDNR(); len(marked) > 0 {
		if _, err := m.DB().Exec(c, "UPDATE workflow_job_nodes SET do_not_run = true WHERE id = ANY($1)", marked); err != nil {
			return err
		}
	}
	spawned := false
	for _, n := range d.toRun() {
		if err := spawnNode(c, m, wf, d, n); err != nil {
			return err
		}
		spawned = true
	}
	if spawned {
		jobs.Wake(c, m.DB())
		return nil
	}
	if !d.done() {
		return nil
	}
	if failed, why := d.failed(); failed {
		return m.Complete(c, wfID, jobs.Result{Status: "failed", Explanation: why})
	}
	return m.Complete(c, wfID, jobs.Result{Status: "successful"})
}

// nodeAncestorArtifacts merges artifacts of a node's parents (their own ancestors first).
func nodeAncestorArtifacts(c *Ctx, d *dag, n *wfNode) map[string]any {
	out := map[string]any{}
	parents := append([]int64{}, n.parents...)
	sort.Slice(parents, func(i, j int) bool { return parents[i] < parents[j] })
	for _, pid := range parents {
		p := d.nodes[pid]
		for k, v := range p.artifacts {
			out[k] = v
		}
		if p.job != 0 {
			var art map[string]any
			c.DB().QueryRow(c, "SELECT artifacts FROM unified_jobs WHERE id = $1", p.job).Scan(&art)
			for k, v := range art {
				out[k] = v
			}
		}
	}
	return out
}

// workflowAncestry lists the workflow job templates of a workflow job and the workflows that spawned it.
func workflowAncestry(c *Ctx, wfID int64) []int64 {
	var out []int64
	for i := 0; i < 50 && wfID != 0; i++ {
		var ujt, parent int64
		c.DB().QueryRow(c, `SELECT coalesce(w.unified_job_template_id, 0), coalesce(n.workflow_job_id, 0) FROM unified_jobs w
			LEFT JOIN workflow_job_nodes n ON n.job_id = w.id WHERE w.id = $1`, wfID).Scan(&ujt, &parent)
		out = append(out, ujt)
		wfID = parent
	}
	return out
}

func spawnNode(c *Ctx, m *jobs.Manager, wf jobs.Row, d *dag, n *wfNode) error {
	node, err := api.Get(c, api.Lookup("workflow_job_nodes"), n.id)
	if err != nil {
		return err
	}
	artifacts := map[string]any{}
	if len(n.parents) > 0 {
		artifacts = nodeAncestorArtifacts(c, d, n)
	} else {
		for k, v := range n.artifacts {
			artifacts[k] = v
		}
	}
	// Variables: node extra_data < ancestor artifacts < workflow extra vars.
	extra := map[string]any{}
	if ed, ok := node["extra_data"].(map[string]any); ok {
		for k, v := range ed {
			if s, ok := v.(string); ok && crypto.IsEncrypted(s) {
				if p, err := c.App.Crypto.Decrypt(s); err == nil {
					v = p
				}
			}
			extra[k] = v
		}
	}
	for k, v := range artifacts {
		if k != "job_slice" {
			extra[k] = v
		}
	}
	wfVars, _ := vars.Parse(wf.Str("extra_vars"))
	for k, v := range wfVars {
		if s, ok := v.(string); ok && crypto.IsEncrypted(s) {
			if p, err := c.App.Crypto.Decrypt(s); err == nil {
				v = p
			}
		}
		extra[k] = v
	}
	launchedBy, _ := json.Marshal(Obj{"id": wf.ID(), "name": wf.Str("name"), "type": "workflow_job", "url": api.Lookup("workflow_jobs").URL(wf.ID())})

	var jobID int64
	var launchErr error
	err = c.WithTx(func(c *Ctx) error {
		jobID, launchErr = launchNode(c, wf, n, node, extra)
		if launchErr != nil {
			// The node fails visibly so error handling paths can run.
			var name string
			c.DB().QueryRow(c, "SELECT name FROM unified_job_templates WHERE id = $1", n.ujt).Scan(&name)
			typ := unifiedJobTypes[n.ujtType]
			if typ == "" {
				typ = "job"
			}
			var err error
			jobID, err = jobs.Create(c, c.DB(), map[string]any{"type": typ, "name": name, "unified_job_template_id": n.ujt, "status": "failed",
				"failed": true, "launch_type": "workflow", "job_explanation": launchFailure(launchErr), "started": time.Now(), "finished": time.Now()})
			if err != nil {
				return err
			}
		}
		art, _ := json.Marshal(artifacts)
		if _, err := c.DB().Exec(c, "UPDATE workflow_job_nodes SET job_id = $2, ancestor_artifacts = $3, modified = now() WHERE id = $1", n.id, jobID, string(art)); err != nil {
			return err
		}
		_, err := c.DB().Exec(c, "UPDATE unified_jobs SET launched_by = $2, created_by_id = coalesce(created_by_id, $3) WHERE id = $1",
			jobID, string(launchedBy), nullID(wf.Int("created_by_id")))
		return err
	})
	if err != nil {
		return err
	}
	if launchErr == nil && n.ujtType == "workflow_approval_template" {
		notifyApproval(c, jobID, "running")
	}
	if launchErr != nil {
		jobs.Wake(c, m.DB())
	}
	return nil
}

func launchFailure(err error) string {
	if e, ok := err.(*api.Error); ok {
		b, _ := json.Marshal(e.Body)
		return "Job could not start: " + string(b)
	}
	return "Job could not start: " + err.Error()
}

// launchNode starts the job of a workflow node.
func launchNode(c *Ctx, wf jobs.Row, n *wfNode, node Obj, extra map[string]any) (int64, error) {
	tplRes := api.LookupType(n.ujtType)
	if tplRes == nil {
		return 0, fmt.Errorf("unsupported node type %q", n.ujtType)
	}
	tpl, err := api.Get(c, tplRes, n.ujt)
	if err != nil {
		return 0, fmt.Errorf("the node's template no longer exists")
	}
	switch n.ujtType {
	case "job_template":
		trusted := Obj{}
		for _, f := range schedulePrompts {
			if v := node[f]; v != nil {
				trusted[f] = v
			}
		}
		// Workflow-level prompts override nodes whose template prompts for them.
		for field, col := range map[string]string{"inventory": "inventory_id", "limit": "limit", "scm_branch": "scm_branch", "job_tags": "job_tags", "skip_tags": "skip_tags"} {
			v := wf[col]
			if v == nil || v == "" {
				continue
			}
			if boolv(tpl[promptable[field]]) {
				trusted[field] = v
			}
		}
		if creds, _ := idList(c, "SELECT credential_id FROM workflow_job_node_credentials WHERE node_id = $1 ORDER BY credential_id", n.id); len(creds) > 0 {
			list := make([]any, len(creds))
			for i, id := range creds {
				list[i] = id
			}
			trusted["credentials"] = list
		}
		if labels, _ := idList(c, "SELECT label_id FROM workflow_job_node_labels WHERE node_id = $1", n.id); len(labels) > 0 {
			list := make([]any, len(labels))
			for i, id := range labels {
				list[i] = id
			}
			trusted["labels"] = list
		}
		ov := &LaunchOverrides{Trusted: trusted}
		if aa := n.artifacts; aa["job_slice"] != nil {
			ov.ExtraJob = map[string]any{"job_slice_number": aa["job_slice"]}
		}
		id, _, err := LaunchJobTemplate(c, tpl, Obj{"extra_vars": extra}, "workflow", ov)
		return id, err
	case "project":
		return LaunchProjectUpdate(c, tpl, "workflow", "check", "")
	case "inventory_source":
		return LaunchInventoryUpdate(c, tpl, "workflow")
	case "workflow_job_template":
		for _, anc := range workflowAncestry(c, wf.ID()) {
			if anc == n.ujt {
				return 0, fmt.Errorf("Workflow Job spawned from workflow could not start because it would result in recursion (spawn order, most recent first: %v)", workflowAncestry(c, wf.ID()))
			}
		}
		id, _, err := LaunchWorkflowJobTemplate(c, tpl, Obj{"extra_vars": extra}, "workflow", &LaunchOverrides{Trusted: Obj{}})
		return id, err
	case "workflow_approval_template":
		return jobs.Create(c, c.DB(), map[string]any{"type": "workflow_approval", "name": tpl["name"], "description": tpl["description"],
			"unified_job_template_id": n.ujt, "approval_timeout": tpl["timeout"], "status": "pending", "launch_type": "workflow"})
	}
	return 0, fmt.Errorf("unsupported node type %q", n.ujtType)
}
