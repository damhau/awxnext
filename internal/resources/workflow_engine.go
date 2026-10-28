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
	"github.com/damhau/awxnext/internal/metrics"
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
	identifier         string
	when, loop         string
	loopVar            string
	loopConcurrency    int
	// A loop node has expanded once its items are evaluated; its jobs are its iterations.
	expanded bool
	items    []any
	iters    []wfIter
}

// wfIter is the job of one loop item. Iterations start in item order.
type wfIter struct {
	job    int64
	status string
}

// loopJob stands in for the job of a started loop node, whose status is the
// combined status of its iterations.
const loopJob = -1

// settleLoop derives a started loop node's status from its iterations:
// running until every item has finished, then failed if any item failed.
func (n *wfNode) settleLoop() {
	if !n.expanded {
		return
	}
	n.job, n.jobStatus = loopJob, "successful"
	if len(n.iters) < len(n.items) {
		n.jobStatus = "running"
		return
	}
	for _, it := range n.iters {
		switch {
		case it.job == 0, failedStatus(it.status): // a deleted job counts as failed
			n.jobStatus = "failed"
		case !finishedStatus(it.status):
			n.jobStatus = "running"
			return
		}
	}
}

// runningIters counts started iterations that have not finished.
func (n *wfNode) runningIters() int {
	k := 0
	for _, it := range n.iters {
		if it.job != 0 && !finishedStatus(it.status) {
			k++
		}
	}
	return k
}

func finishedStatus(s string) bool {
	return s == "successful" || s == "failed" || s == "error" || s == "canceled"
}
func failedStatus(s string) bool { return s == "failed" || s == "error" || s == "canceled" }

type dag struct {
	nodes map[int64]*wfNode
	order []int64 // node ids by id
	// own caches the artifacts each node's job (or iterations) produced.
	own map[int64]map[string]any
}

func loadDAG(c *Ctx, wfID int64) (*dag, error) {
	d := &dag{nodes: map[int64]*wfNode{}, own: map[int64]map[string]any{}}
	rows, err := c.DB().Query(c, `SELECT n.id, coalesce(n.unified_job_template_id, 0), coalesce(u.type, ''), coalesce(n.job_id, 0),
		coalesce(j.status, ''), n.do_not_run, n.all_parents_must_converge, n.ancestor_artifacts, n.identifier,
		n.when_expr, n.loop_expr, n.loop_var, n.loop_concurrency, n.loop_items
		FROM workflow_job_nodes n LEFT JOIN unified_job_templates u ON u.id = n.unified_job_template_id
		LEFT JOIN unified_jobs j ON j.id = n.job_id WHERE n.workflow_job_id = $1 ORDER BY n.id`, wfID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		n := &wfNode{children: map[string][]int64{}}
		var items []byte
		if err := rows.Scan(&n.id, &n.ujt, &n.ujtType, &n.job, &n.jobStatus, &n.dnr, &n.converge, &n.artifacts, &n.identifier,
			&n.when, &n.loop, &n.loopVar, &n.loopConcurrency, &items); err != nil {
			rows.Close()
			return nil, err
		}
		if items != nil && n.job == 0 {
			n.expanded = true
			json.Unmarshal(items, &n.items)
		}
		d.nodes[n.id] = n
		d.order = append(d.order, n.id)
	}
	rows.Close()
	rows, err = c.DB().Query(c, `SELECT i.node_id, coalesce(i.job_id, 0), coalesce(j.status, '') FROM workflow_job_node_iterations i
		JOIN workflow_job_nodes n ON n.id = i.node_id LEFT JOIN unified_jobs j ON j.id = i.job_id
		WHERE n.workflow_job_id = $1 ORDER BY i.node_id, i.idx`, wfID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var nid int64
		var it wfIter
		if err := rows.Scan(&nid, &it.job, &it.status); err != nil {
			rows.Close()
			return nil, err
		}
		if n := d.nodes[nid]; n != nil {
			n.iters = append(n.iters, it)
		}
	}
	rows.Close()
	for _, n := range d.nodes {
		n.settleLoop()
	}
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
	start := time.Now()
	ids, err := idList(c, "SELECT id FROM unified_jobs WHERE type = 'workflow_job' AND status = 'running' ORDER BY id")
	if err != nil {
		return
	}
	get := time.Since(start).Seconds()
	spawn := time.Now()
	for _, id := range ids {
		if err := processWorkflow(c, m, id); err != nil && ctx.Err() == nil {
			slog.Error("workflow processing failed", "workflow_job", id, "err", err)
		}
	}
	// AWX's workflow manager metrics.
	metrics.Sub.Set("workflow_manager_get_tasks_seconds", get)
	metrics.Sub.Set("workflow_manager_spawn_workflow_graph_jobs_seconds", time.Since(spawn).Seconds())
	metrics.Sub.Set("workflow_manager__schedule_seconds", time.Since(start).Seconds())
	metrics.Sub.Add("workflow_manager__schedule_calls", 1)
	metrics.Sub.Set("workflow_manager_recorded_timestamp", float64(time.Now().UnixNano())/1e9)
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
		for _, id := range d.unfinishedJobs() {
			running++
			m.Cancel(c, id)
		}
		if running == 0 {
			return m.Complete(c, wfID, jobs.Result{Status: "canceled", Explanation: "Workflow job was canceled."})
		}
		return nil
	}
	spawned := false
	// Nodes decided without a job (a false condition, an empty loop) can
	// unblock or skip their children within the same tick.
	for pass := 0; pass <= len(d.nodes); pass++ {
		if marked := d.markDNR(); len(marked) > 0 {
			if _, err := m.DB().Exec(c, "UPDATE workflow_job_nodes SET do_not_run = true WHERE id = ANY($1)", marked); err != nil {
				return err
			}
		}
		decided := false
		for _, n := range d.toRun() {
			started, err := startNode(c, m, wf, d, n)
			if err != nil {
				return err
			}
			spawned = spawned || started
			decided = decided || !started
		}
		if !decided {
			break
		}
	}
	for _, id := range d.order {
		n := d.nodes[id]
		if n.expanded && len(n.iters) < len(n.items) {
			started, err := continueLoop(c, m, wf, d, n)
			if err != nil {
				return err
			}
			spawned = spawned || started
		}
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

// unfinishedJobs lists the jobs of the workflow's nodes and loop iterations still running.
func (d *dag) unfinishedJobs() []int64 {
	var ids []int64
	for _, id := range d.order {
		n := d.nodes[id]
		if n.job > 0 && !finishedStatus(n.jobStatus) {
			ids = append(ids, n.job)
		}
		for _, it := range n.iters {
			if it.job != 0 && !finishedStatus(it.status) {
				ids = append(ids, it.job)
			}
		}
	}
	return ids
}

// ownArtifacts returns the artifacts a node's job produced; a loop node
// merges its iterations' artifacts in item order.
func (d *dag) ownArtifacts(c *Ctx, n *wfNode) map[string]any {
	if a, ok := d.own[n.id]; ok {
		return a
	}
	var jobIDs []int64
	switch {
	case n.job > 0:
		jobIDs = []int64{n.job}
	case n.job == loopJob:
		for _, it := range n.iters {
			if it.job != 0 {
				jobIDs = append(jobIDs, it.job)
			}
		}
	}
	out := map[string]any{}
	for _, id := range jobIDs {
		var art map[string]any
		c.DB().QueryRow(c, "SELECT artifacts FROM unified_jobs WHERE id = $1", id).Scan(&art)
		for k, v := range art {
			out[k] = v
		}
	}
	d.own[n.id] = out
	return out
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
		for k, v := range d.ownArtifacts(c, p) {
			out[k] = v
		}
	}
	return out
}

// nodeStates describes every node for conditions, by identifier:
// nodes.build.status, nodes.build.artifacts.image_tag.
func (d *dag) nodeStates(c *Ctx) map[string]any {
	out := map[string]any{}
	for _, id := range d.order {
		n := d.nodes[id]
		status := n.jobStatus
		switch {
		case n.dnr:
			status = "skipped"
		case n.job == 0:
			status = "pending"
		}
		st := map[string]any{"status": status, "failed": failedStatus(status), "artifacts": d.ownArtifacts(c, n), "job": nil}
		if n.job > 0 {
			st["job"] = n.job
		}
		if n.expanded {
			st["items"] = n.items
		}
		out[n.identifier] = st
	}
	return out
}

// workflowAncestry lists the workflow job templates of a workflow job and the workflows that spawned it.
func workflowAncestry(c *Ctx, wfID int64) []int64 {
	var out []int64
	for i := 0; i < 50 && wfID != 0; i++ {
		var ujt, parent int64
		c.DB().QueryRow(c, `SELECT coalesce(w.unified_job_template_id, 0), coalesce(n.workflow_job_id, 0) FROM unified_jobs w
			LEFT JOIN workflow_node_jobs n ON n.job_id = w.id WHERE w.id = $1`, wfID).Scan(&ujt, &parent)
		out = append(out, ujt)
		wfID = parent
	}
	return out
}

// nodeVars returns the artifacts passed down to a node and the variables its
// job gets: node extra_data < ancestor artifacts < workflow extra vars.
func nodeVars(c *Ctx, wf jobs.Row, d *dag, n *wfNode, node Obj) (artifacts, extra map[string]any) {
	artifacts = map[string]any{}
	if len(n.parents) > 0 {
		artifacts = nodeAncestorArtifacts(c, d, n)
	} else {
		for k, v := range n.artifacts {
			artifacts[k] = v
		}
	}
	extra = map[string]any{}
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
	return artifacts, extra
}

// exprEnv is what node conditions and loops see: the node's variables, the
// state of every node by identifier, and the workflow job.
func exprEnv(c *Ctx, wf jobs.Row, d *dag, extra map[string]any) map[string]any {
	env := map[string]any{}
	for k, v := range extra {
		env[k] = v
	}
	env["nodes"] = d.nodeStates(c)
	env["workflow"] = map[string]any{"id": wf.ID(), "name": wf.Str("name")}
	return env
}

// startNode starts a node whose parents allow it to run. It reports whether
// it started a job; otherwise the node was decided without one (its
// condition is false, or its loop has no items).
func startNode(c *Ctx, m *jobs.Manager, wf jobs.Row, d *dag, n *wfNode) (bool, error) {
	node, err := api.Get(c, api.Lookup("workflow_job_nodes"), n.id)
	if err != nil {
		return false, err
	}
	artifacts, extra := nodeVars(c, wf, d, n, node)
	art, _ := json.Marshal(artifacts)
	var env map[string]any
	if n.when != "" || n.loop != "" {
		env = exprEnv(c, wf, d, extra)
	}
	if n.when != "" {
		ok, err := evalWhen(n.when, env)
		if err != nil {
			return true, failNode(c, m, wf, n, string(art), "The node's condition could not be evaluated: "+err.Error())
		}
		if !ok {
			n.dnr = true
			_, err := c.DB().Exec(c, "UPDATE workflow_job_nodes SET do_not_run = true, skip_reason = $2, ancestor_artifacts = $3, modified = now() WHERE id = $1",
				n.id, "Condition is false: "+n.when, string(art))
			return false, err
		}
	}
	if n.loop != "" {
		items, err := evalLoop(n.loop, env)
		if err != nil {
			return true, failNode(c, m, wf, n, string(art), "The node's loop could not be evaluated: "+err.Error())
		}
		b, _ := json.Marshal(items)
		if _, err := c.DB().Exec(c, "UPDATE workflow_job_nodes SET loop_items = $2, ancestor_artifacts = $3, modified = now() WHERE id = $1", n.id, string(b), string(art)); err != nil {
			return false, err
		}
		n.expanded, n.items = true, items
		n.settleLoop()
		if len(items) == 0 {
			return false, nil
		}
		return continueLoop(c, m, wf, d, n)
	}
	jobID, launchErr, err := launchNodeJob(c, wf, n, node, extra)
	if err != nil {
		return false, err
	}
	if _, err := c.DB().Exec(c, "UPDATE workflow_job_nodes SET job_id = $2, ancestor_artifacts = $3, modified = now() WHERE id = $1", n.id, jobID, string(art)); err != nil {
		return false, err
	}
	n.job, n.jobStatus = jobID, "pending"
	if launchErr == nil && n.ujtType == "workflow_approval_template" {
		notifyApproval(c, jobID, "running")
	}
	return true, nil
}

// continueLoop starts the next iterations of a loop node, up to its concurrency.
func continueLoop(c *Ctx, m *jobs.Manager, wf jobs.Row, d *dag, n *wfNode) (bool, error) {
	running := n.runningIters()
	if len(n.iters) >= len(n.items) || (n.loopConcurrency > 0 && running >= n.loopConcurrency) {
		return false, nil
	}
	node, err := api.Get(c, api.Lookup("workflow_job_nodes"), n.id)
	if err != nil {
		return false, err
	}
	_, extra := nodeVars(c, wf, d, n, node)
	loopVar := n.loopVar
	if loopVar == "" {
		loopVar = "item"
	}
	started := false
	for idx := len(n.iters); idx < len(n.items); idx++ {
		if n.loopConcurrency > 0 && running >= n.loopConcurrency {
			break
		}
		item := n.items[idx]
		vars := map[string]any{}
		for k, v := range extra {
			vars[k] = v
		}
		vars[loopVar] = item
		vars["loop_index"] = idx
		jobID, _, err := launchNodeJob(c, wf, n, node, vars)
		if err != nil {
			return started, err
		}
		if _, err := c.DB().Exec(c, "INSERT INTO workflow_job_node_iterations (node_id, idx, job_id) VALUES ($1, $2, $3)", n.id, idx, jobID); err != nil {
			return started, err
		}
		c.DB().Exec(c, "UPDATE unified_jobs SET name = left(name || ' (' || $2 || ')', 512) WHERE id = $1", jobID, itemLabel(item, idx))
		n.iters = append(n.iters, wfIter{job: jobID, status: "pending"})
		running++
		started = true
	}
	return started, nil
}

// itemLabel names a loop item in its job's name: the item itself when it is
// short and scalar, its position otherwise.
func itemLabel(item any, idx int) string {
	switch v := item.(type) {
	case string, float64, int, int64, bool:
		if s := fmt.Sprint(v); len(s) <= 60 {
			return s
		}
	}
	return fmt.Sprintf("#%d", idx+1)
}

// launchNodeJob launches a node's template with vars. When the launch is
// refused, a failed job records why (launchErr), so the node fails visibly
// and error handling paths can run.
func launchNodeJob(c *Ctx, wf jobs.Row, n *wfNode, node Obj, vars map[string]any) (jobID int64, launchErr, err error) {
	launchedBy, _ := json.Marshal(Obj{"id": wf.ID(), "name": wf.Str("name"), "type": "workflow_job", "url": api.Lookup("workflow_jobs").URL(wf.ID())})
	err = c.WithTx(func(c *Ctx) error {
		jobID, launchErr = launchNode(c, wf, n, node, vars)
		if launchErr != nil {
			var err error
			if jobID, err = createFailedJob(c, n, launchFailure(launchErr)); err != nil {
				return err
			}
		}
		_, err := c.DB().Exec(c, "UPDATE unified_jobs SET launched_by = $2, created_by_id = coalesce(created_by_id, $3) WHERE id = $1",
			jobID, string(launchedBy), nullID(wf.Int("created_by_id")))
		return err
	})
	if err == nil && launchErr != nil {
		jobs.Wake(c, c.App.DB)
	}
	return jobID, launchErr, err
}

// createFailedJob records a node job that could not run.
func createFailedJob(c *Ctx, n *wfNode, explanation string) (int64, error) {
	var name string
	c.DB().QueryRow(c, "SELECT name FROM unified_job_templates WHERE id = $1", n.ujt).Scan(&name)
	typ := unifiedJobTypes[n.ujtType]
	if typ == "" {
		typ = "job"
	}
	return jobs.Create(c, c.DB(), map[string]any{"type": typ, "name": name, "unified_job_template_id": n.ujt, "status": "failed",
		"failed": true, "launch_type": "workflow", "job_explanation": explanation, "started": time.Now(), "finished": time.Now()})
}

// failNode fails a node without running its template.
func failNode(c *Ctx, m *jobs.Manager, wf jobs.Row, n *wfNode, artifacts, explanation string) error {
	err := c.WithTx(func(c *Ctx) error {
		jobID, err := createFailedJob(c, n, explanation)
		if err != nil {
			return err
		}
		n.job, n.jobStatus = jobID, "failed"
		launchedBy, _ := json.Marshal(Obj{"id": wf.ID(), "name": wf.Str("name"), "type": "workflow_job", "url": api.Lookup("workflow_jobs").URL(wf.ID())})
		if _, err := c.DB().Exec(c, "UPDATE unified_jobs SET launched_by = $2 WHERE id = $1", jobID, string(launchedBy)); err != nil {
			return err
		}
		_, err = c.DB().Exec(c, "UPDATE workflow_job_nodes SET job_id = $2, ancestor_artifacts = $3, modified = now() WHERE id = $1", n.id, jobID, artifacts)
		return err
	})
	if err == nil {
		jobs.Wake(c, m.DB())
	}
	return err
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
	case "system_job_template":
		// Only the node's own days and dry_run: management jobs take no other variables.
		ed, _ := node["extra_data"].(map[string]any)
		if err := validateSystemJobVars("extra_data", ed); err != nil {
			return 0, err
		}
		return LaunchSystemJob(c, tpl, ed, "workflow")
	case "workflow_approval_template":
		return jobs.Create(c, c.DB(), map[string]any{"type": "workflow_approval", "name": tpl["name"], "description": tpl["description"],
			"unified_job_template_id": n.ujt, "approval_timeout": tpl["timeout"], "status": "pending", "launch_type": "workflow"})
	}
	return 0, fmt.Errorf("unsupported node type %q", n.ujtType)
}

// WorkflowJobFinished cancels approvals still pending when their workflow
// job ends by any path (canceled, reaped, failed), so nobody is asked to
// approve a step that can no longer run.
func WorkflowJobFinished(ctx context.Context, m *jobs.Manager, job jobs.Row) {
	if job.Str("type") != "workflow_job" {
		return
	}
	rows, err := m.DB().Query(ctx, `SELECT j.id FROM workflow_job_nodes n JOIN unified_jobs j ON j.id = n.job_id
		WHERE n.workflow_job_id = $1 AND j.type = 'workflow_approval' AND j.status IN ('new', 'pending', 'waiting')`, job.ID())
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
		m.Cancel(ctx, id)
	}
}
