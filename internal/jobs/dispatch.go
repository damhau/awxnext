package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/jackc/pgx/v5"
)

// Create inserts a pending unified job. Column names are unified_jobs columns.
// Call Wake after the surrounding transaction commits.
func Create(ctx context.Context, db api.DBTX, values map[string]any) (int64, error) {
	if values["type"] == nil {
		return 0, fmt.Errorf("job type is required")
	}
	if _, ok := values["status"]; !ok {
		values["status"] = "pending"
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cols := make([]string, len(keys))
	phs := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		cols[i] = pgx.Identifier{k}.Sanitize()
		phs[i] = fmt.Sprintf("$%d", i+1)
		args[i] = values[k]
	}
	var id int64
	err := db.QueryRow(ctx, "INSERT INTO unified_jobs ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(phs, ", ")+") RETURNING id", args...).Scan(&id)
	return id, err
}

// Wake signals the dispatcher (on any node) that there is work.
func Wake(ctx context.Context, db api.DBTX) {
	db.Exec(ctx, "SELECT pg_notify($1, '')", chDispatch)
}

// dispatchPending starts or places pending jobs. Approvals stay pending until decided.
func (m *Manager) dispatchPending(ctx context.Context) error {
	rows, err := m.DB().Query(ctx, "SELECT id FROM unified_jobs WHERE status = 'pending' AND type <> 'workflow_approval' ORDER BY created, id LIMIT 200")
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	assigned := false
	for _, id := range ids {
		row, err := LoadRow(ctx, m.DB(), id)
		if err != nil {
			continue
		}
		ok, err := m.dispatchOne(ctx, row)
		if err != nil {
			slog.Error("dispatching job", "job", id, "err", err)
			m.finish(ctx, row, Result{Status: "error", Explanation: err.Error()})
			continue
		}
		assigned = assigned || ok
	}
	if assigned {
		m.Notify(ctx, chWork, "")
	}
	return nil
}

func (m *Manager) dispatchOne(ctx context.Context, row Row) (bool, error) {
	sp := spec(row.Str("type"))
	if sp == nil {
		return false, fmt.Errorf("unknown job type %q", row.Str("type"))
	}
	id := row.ID()
	if !row.Bool("dependencies_processed") {
		if sp.Dependencies != nil {
			deps, err := sp.Dependencies(ctx, m, row)
			if err != nil {
				return false, err
			}
			for _, d := range deps {
				if _, err := m.DB().Exec(ctx, "INSERT INTO unified_job_dependencies (unified_job_id, depends_on_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, d); err != nil {
					return false, err
				}
			}
		}
		if _, err := m.DB().Exec(ctx, "UPDATE unified_jobs SET dependencies_processed = true WHERE id = $1", id); err != nil {
			return false, err
		}
	}
	// Wait for dependencies; fail if one of them failed.
	var blocking int
	var failedDep struct {
		id        int64
		typ, name string
	}
	drows, err := m.DB().Query(ctx, `SELECT d.id, d.type, d.name, d.status FROM unified_job_dependencies x JOIN unified_jobs d ON d.id = x.depends_on_id
		WHERE x.unified_job_id = $1`, id)
	if err != nil {
		return false, err
	}
	for drows.Next() {
		var did int64
		var typ, name, status string
		drows.Scan(&did, &typ, &name, &status)
		switch status {
		case "successful":
		case "failed", "error", "canceled":
			failedDep.id, failedDep.typ, failedDep.name = did, typ, name
		default:
			blocking++
		}
	}
	drows.Close()
	if failedDep.id != 0 {
		b, _ := json.Marshal(map[string]any{"job_type": failedDep.typ, "job_name": failedDep.name, "job_id": fmt.Sprint(failedDep.id)})
		m.finish(ctx, row, Result{Status: "failed", Explanation: "Previous Task Failed: " + string(b)})
		return false, nil
	}
	if blocking > 0 {
		return false, nil
	}
	if sp.Serial && !row.Bool("allow_simultaneous") && row.Has("unified_job_template_id") {
		var busy bool
		m.DB().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM unified_jobs WHERE unified_job_template_id = $1 AND id <> $2
			AND type = $3 AND status IN ('waiting', 'running'))`, row["unified_job_template_id"], id, row.Str("type")).Scan(&busy)
		if busy {
			return false, nil
		}
	}
	if sp.InDispatcher {
		// Workflow jobs run in the dispatcher itself (no capacity).
		_, err := m.DB().Exec(ctx, "UPDATE unified_jobs SET status = 'running', started = now(), controller_node = $2 WHERE id = $1 AND status = 'pending'", id, m.Node)
		if err == nil {
			m.publishStatus(ctx, row, "running")
		}
		return false, err
	}
	impact := 1
	if sp.Impact != nil {
		impact = sp.Impact(ctx, m, row)
	}
	pl, reason, err := m.place(ctx, row, sp, impact)
	if err != nil {
		return false, err
	}
	if pl == nil {
		if reason != "" && row.Str("job_explanation") != reason {
			m.DB().Exec(ctx, "UPDATE unified_jobs SET job_explanation = $2 WHERE id = $1", id, reason)
		}
		return false, nil
	}
	tag, err := m.DB().Exec(ctx, `UPDATE unified_jobs SET status = 'waiting', execution_node = $2, controller_node = $3,
		instance_group_id = $4, task_impact = $5, job_explanation = '', modified = now() WHERE id = $1 AND status = 'pending'`,
		id, pl.executionNode, pl.controllerNode, pl.groupID, impact)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		m.publishStatus(ctx, row, "waiting")
	}
	return tag.RowsAffected() > 0, nil
}

type placement struct {
	executionNode  string
	controllerNode string
	groupID        *int64
}

type instanceRow struct {
	hostname  string
	nodeType  string
	capacity  int
	remaining int
}

// instanceGroupsFor returns the ordered instance group preference for a job:
// template, inventory, organization, then "default". prevent_instance_group_fallback
// on the template or inventory stops the chain.
func (m *Manager) instanceGroupsFor(ctx context.Context, row Row) ([]int64, error) {
	var out []int64
	add := func(ct string, id int64) error {
		if id == 0 {
			return nil
		}
		rows, err := m.DB().Query(ctx, "SELECT instance_group_id FROM instance_group_links WHERE content_type = $1 AND object_id = $2 ORDER BY position", ct, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g int64
			rows.Scan(&g)
			out = append(out, g)
		}
		return nil
	}
	// Instance groups chosen at launch (stored on the job) take precedence.
	if err := add("unifiedjob", row.ID()); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}
	ujt := row.Int("unified_job_template_id")
	var tplNoFallback, invNoFallback bool
	if ujt != 0 {
		ct := "jobtemplate"
		if row.Str("type") == "inventory_update" {
			ct = "inventorysource"
		}
		if err := add(ct, ujt); err != nil {
			return nil, err
		}
		m.DB().QueryRow(ctx, "SELECT prevent_instance_group_fallback FROM unified_job_templates WHERE id = $1", ujt).Scan(&tplNoFallback)
	}
	if tplNoFallback && len(out) > 0 {
		return out, nil
	}
	if inv := row.Int("inventory_id"); inv != 0 {
		if err := add("inventory", inv); err != nil {
			return nil, err
		}
		m.DB().QueryRow(ctx, "SELECT prevent_instance_group_fallback FROM inventories WHERE id = $1", inv).Scan(&invNoFallback)
	}
	if invNoFallback && len(out) > 0 {
		return out, nil
	}
	if err := add("organization", row.Int("organization_id")); err != nil {
		return nil, err
	}
	var def int64
	if err := m.DB().QueryRow(ctx, "SELECT id FROM instance_groups WHERE name = 'default'").Scan(&def); err == nil {
		out = append(out, def)
	}
	return dedupe(out), nil
}

func dedupe(ids []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (m *Manager) place(ctx context.Context, row Row, sp *TypeSpec, impact int) (*placement, string, error) {
	var groups []int64
	if sp.ControlPlane {
		var cp int64
		if err := m.DB().QueryRow(ctx, "SELECT id FROM instance_groups WHERE name = 'controlplane'").Scan(&cp); err == nil {
			groups = []int64{cp}
		}
	} else {
		var err error
		groups, err = m.instanceGroupsFor(ctx, row)
		if err != nil {
			return nil, "", err
		}
	}
	for _, g := range groups {
		var isContainer bool
		var maxJobs int
		if err := m.DB().QueryRow(ctx, "SELECT is_container_group, max_concurrent_jobs FROM instance_groups WHERE id = $1", g).Scan(&isContainer, &maxJobs); err != nil {
			continue
		}
		gid := g
		if isContainer {
			if maxJobs > 0 {
				var n int
				m.DB().QueryRow(ctx, "SELECT count(*) FROM unified_jobs WHERE instance_group_id = $1 AND status IN ('waiting','running')", g).Scan(&n)
				if n >= maxJobs {
					continue
				}
			}
			ctl, err := m.controlNode(ctx)
			if err != nil || ctl == "" {
				continue
			}
			return &placement{executionNode: "", controllerNode: ctl, groupID: &gid}, "", nil
		}
		cands, err := m.candidates(ctx, g, sp.ControlPlane)
		if err != nil {
			return nil, "", err
		}
		var best *instanceRow
		for i := range cands {
			c := &cands[i]
			if c.remaining >= impact && (best == nil || c.remaining > best.remaining) {
				best = c
			}
		}
		if best == nil {
			// An idle instance may take a job larger than its capacity.
			for i := range cands {
				c := &cands[i]
				if c.remaining == c.capacity && (best == nil || c.capacity > best.capacity) {
					best = c
				}
			}
		}
		if best != nil {
			ctl := best.hostname
			if best.nodeType == "execution" {
				// Remote execution nodes are driven by a control node.
				if ctl, err = m.controlNode(ctx); err != nil || ctl == "" {
					continue
				}
			}
			return &placement{executionNode: best.hostname, controllerNode: ctl, groupID: &gid}, "", nil
		}
	}
	return nil, "Waiting for capacity in the job's instance groups.", nil
}

func (m *Manager) candidates(ctx context.Context, group int64, controlPlane bool) ([]instanceRow, error) {
	types := "('execution', 'hybrid')"
	if controlPlane {
		types = "('control', 'hybrid')"
	}
	rows, err := m.DB().Query(ctx, `SELECT i.hostname, i.node_type, (i.capacity * i.capacity_adjustment)::int,
			(i.capacity * i.capacity_adjustment)::int - coalesce((SELECT sum(task_impact) FROM unified_jobs j
				WHERE j.execution_node = i.hostname AND j.status IN ('waiting', 'running')), 0)
		FROM instances i JOIN instance_group_instances gi ON gi.instance_id = i.id
		WHERE gi.instance_group_id = $1 AND i.enabled AND i.node_state = 'ready' AND i.node_type IN `+types+`
			AND i.last_seen > now() - interval '2 minutes'
		ORDER BY i.hostname`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []instanceRow
	for rows.Next() {
		var r instanceRow
		if err := rows.Scan(&r.hostname, &r.nodeType, &r.capacity, &r.remaining); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// controlNode picks the least busy live control-capable node.
func (m *Manager) controlNode(ctx context.Context) (string, error) {
	var host string
	err := m.DB().QueryRow(ctx, `SELECT i.hostname FROM instances i
		WHERE i.node_type IN ('control', 'hybrid') AND i.enabled AND i.last_seen > now() - interval '2 minutes'
		ORDER BY (SELECT count(*) FROM unified_jobs j WHERE j.controller_node = i.hostname AND j.status IN ('waiting','running')), i.hostname
		LIMIT 1`).Scan(&host)
	return host, err
}
