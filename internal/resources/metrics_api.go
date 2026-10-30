package resources

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/metrics"
)

// handleAWXMetrics serves /api/v2/metrics/ with AWX's metric names, computed
// from the database (system auditors and superusers only).
func handleAWXMetrics(c *Ctx, w http.ResponseWriter, r *http.Request) {
	if !c.User.Superuser && !c.User.Auditor {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	q := r.URL.Query()
	var b strings.Builder
	if q.Get("subsystemonly") != "1" {
		writeDBMetrics(c, metrics.NewWriter(&b))
	}
	// AWX's subsystem metrics of every node, filtered by node and metric.
	if q.Get("dbonly") != "1" {
		nodes, err := subsystemSnapshots(c, q["node"])
		if err != nil {
			api.WriteError(w, err)
			return
		}
		metrics.WriteSubsystem(&b, nodes, q["metric"])
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}

// writeDBMetrics writes AWX's metrics computed from the database.
func writeDBMetrics(c *Ctx, mw *metrics.Writer) {
	var installUUID string
	c.DB().QueryRow(c, "SELECT value #>> '{}' FROM settings WHERE key = 'INSTALL_UUID'").Scan(&installUUID)
	mw.Sample("awx_system_info", "gauge", "AWX System Information", 1,
		"install_uuid", installUUID, "insights_analytics", "False", "tower_url_base", c.App.Cfg.BaseURL, "tower_version", api.ProductVersion,
		"license_type", "open", "license_expiry", "0", "pendo_tracking", "off", "external_logger_enabled", "False", "external_logger_type", "None")
	counts := []struct{ name, help, sql string }{
		{"awx_organizations_total", "Number of organizations", "SELECT count(*) FROM organizations"},
		{"awx_users_total", "Number of users", "SELECT count(*) FROM users"},
		{"awx_teams_total", "Number of teams", "SELECT count(*) FROM teams"},
		{"awx_inventories_total", "Number of inventories", "SELECT count(*) FROM inventories"},
		{"awx_projects_total", "Number of projects", "SELECT count(*) FROM unified_job_templates WHERE type = 'project'"},
		{"awx_job_templates_total", "Number of job templates", "SELECT count(*) FROM unified_job_templates WHERE type = 'job_template'"},
		{"awx_workflow_job_templates_total", "Number of workflow job templates", "SELECT count(*) FROM unified_job_templates WHERE type = 'workflow_job_template'"},
		{"awx_hosts_total", "Number of hosts", "SELECT count(*) FROM hosts"},
		{"awx_schedules_total", "Number of schedules", "SELECT count(*) FROM schedules"},
		{"awx_running_jobs_total", "Number of running jobs on the system", "SELECT count(*) FROM unified_jobs WHERE status = 'running'"},
		{"awx_pending_jobs_total", "Number of pending jobs on the system", "SELECT count(*) FROM unified_jobs WHERE status = 'pending'"},
		{"awx_database_connections_total", "Number of connections to database", "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()"},
	}
	for _, m := range counts {
		var n float64
		c.DB().QueryRow(c, m.sql).Scan(&n)
		mw.Sample(m.name, "gauge", m.help, n)
	}
	var active, user float64
	c.DB().QueryRow(c, "SELECT count(*), count(DISTINCT user_id) FROM sessions WHERE expires > now()").Scan(&active, &user)
	mw.Sample("awx_sessions_total", "gauge", "Number of sessions", active, "type", "all")
	mw.Sample("awx_sessions_total", "gauge", "Number of sessions", user, "type", "user")
	mw.Sample("awx_sessions_total", "gauge", "Number of sessions", 0, "type", "anonymous")

	rows, err := c.DB().Query(c, "SELECT status, count(*) FROM unified_jobs GROUP BY status ORDER BY status")
	if err == nil {
		for rows.Next() {
			var status string
			var n float64
			rows.Scan(&status, &n)
			mw.Sample("awx_status_total", "gauge", "Status of Job launched", n, "status", status)
		}
		rows.Close()
	}
	type inst struct {
		host, uuid, version, nodeType, state string
		capacity, cpu, cpuCap, memCap        float64
		memory                               float64
		consumed                             float64
		enabled                              bool
		lastSeen                             *time.Time
	}
	var list []inst
	rows, err = c.DB().Query(c, `SELECT i.hostname, i.uuid, i.version, i.node_type, i.node_state, i.capacity::float8,
		i.cpu::float8, i.cpu_capacity, i.mem_capacity, i.memory, i.enabled, i.last_seen,
		coalesce((SELECT sum(task_impact) FROM unified_jobs j WHERE j.execution_node = i.hostname AND j.status IN ('waiting', 'running')), 0)
		FROM instances i ORDER BY i.hostname`)
	if err == nil {
		for rows.Next() {
			var x inst
			rows.Scan(&x.host, &x.uuid, &x.version, &x.nodeType, &x.state, &x.capacity, &x.cpu, &x.cpuCap, &x.memCap, &x.memory, &x.enabled, &x.lastSeen, &x.consumed)
			list = append(list, x)
		}
		rows.Close()
	}
	for _, x := range list {
		mw.Sample("awx_instance_capacity", "gauge", "Capacity of each node in the system", x.capacity, "hostname", x.host, "instance_uuid", x.uuid)
	}
	for _, x := range list {
		mw.Sample("awx_instance_cpu", "gauge", "CPU cores on each node in the system", x.cpu, "hostname", x.host, "instance_uuid", x.uuid)
	}
	for _, x := range list {
		mw.Sample("awx_instance_memory", "gauge", "RAM (Kb) on each node in the system", x.memory/1024, "hostname", x.host, "instance_uuid", x.uuid)
	}
	for _, x := range list {
		seen := ""
		if x.lastSeen != nil {
			seen = api.FormatTime(*x.lastSeen)
		}
		enabled := "False"
		if x.enabled {
			enabled = "True"
		}
		mw.Sample("awx_instance_info", "gauge", "Info about each node in the system", 1, "hostname", x.host, "instance_uuid", x.uuid,
			"last_healthcheck", seen, "version", x.version, "node_type", x.nodeType, "node_state", x.state, "enabled", enabled)
	}
	for _, x := range list {
		mw.Sample("awx_instance_consumed_capacity", "gauge", "Consumed capacity of each node in the system", x.consumed, "hostname", x.host, "instance_uuid", x.uuid)
	}
	for _, x := range list {
		mw.Sample("awx_instance_remaining_capacity", "gauge", "Remaining capacity of each node in the system", max(x.capacity-x.consumed, 0), "hostname", x.host, "instance_uuid", x.uuid)
	}
	rows, err = c.DB().Query(c, `SELECT coalesce(nullif(execution_node, ''), controller_node), launch_type, count(*) FROM unified_jobs
		WHERE status IN ('waiting', 'running') GROUP BY 1, 2 ORDER BY 1, 2`)
	if err == nil {
		for rows.Next() {
			var node, lt string
			var n float64
			rows.Scan(&node, &lt, &n)
			mw.Sample("awx_instance_launch_type_total", "gauge", "Type of Job launched", n, "node", node, "launch_type", lt)
		}
		rows.Close()
	}
	rows, err = c.DB().Query(c, `SELECT coalesce(nullif(execution_node, ''), controller_node), status, count(*) FROM unified_jobs
		WHERE status IN ('waiting', 'running') GROUP BY 1, 2 ORDER BY 1, 2`)
	if err == nil {
		for rows.Next() {
			var node, status string
			var n float64
			rows.Scan(&node, &status, &n)
			mw.Sample("awx_instance_status_total", "gauge", "Status of Job launched", n, "node", node, "status", status)
		}
		rows.Close()
	}
}

// subsystemSnapshots are the subsystem metrics published by the nodes of
// the cluster (this node's live), keeping those named in only.
func subsystemSnapshots(c *Ctx, only []string) (map[string]map[string]any, error) {
	keep := map[string]bool{}
	for _, n := range only {
		keep[n] = true
	}
	out := map[string]map[string]any{}
	rows, err := c.DB().Query(c, "SELECT s.node, s.data FROM subsystem_metrics s JOIN instances i ON i.hostname = s.node")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var node string
		var data map[string]any
		if err := rows.Scan(&node, &data); err == nil {
			out[node] = data
		}
	}
	rows.Close()
	var live map[string]any
	b, _ := json.Marshal(metrics.Sub.Snapshot())
	json.Unmarshal(b, &live)
	out[c.App.Cfg.NodeName] = live
	if len(keep) > 0 {
		for n := range out {
			if !keep[n] {
				delete(out, n)
			}
		}
	}
	return out, nil
}
