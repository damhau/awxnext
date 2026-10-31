package resources

import (
	"context"
	"log/slog"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/jackc/pgx/v5"
)

// Lost nodes, as AWX's cluster_node_heartbeat. Dispatch already skips a
// node silent for 2 minutes, and the reaper fails the jobs it controls;
// this marks it lost, so the API and its groups' capacity show it.

// lostNodes selects the lost nodes other than $1, the leader: control and
// hybrid nodes, ready or unavailable, silent for more than 2 minutes (the
// reaper's wait) or never seen. A node only provisioned (installed), which
// has not started yet, is not lost; AWX would deprovision it.
const lostNodes = `node_type IN ('control', 'hybrid') AND hostname <> $1 AND node_state IN ('ready', 'unavailable')
	AND coalesce(last_seen, '-infinity') < now() - interval '2 minutes'`

// InstanceTick handles the lost nodes on the dispatcher leader.
func InstanceTick(ctx context.Context, m *jobs.Manager) {
	HandleLostInstances(ctx, m.App, m.Node, m.App.Cfg.AutoDeprovisionInstances)
}

// HandleLostInstances marks the lost ready nodes unavailable, without
// capacity and with AWX's error, so their groups' capacity drops; their
// next heartbeat makes them ready again (jobs.Manager.Beat). With
// deprovision (AWXNEXT_AUTO_DEPROVISION_INSTANCES) it deletes them instead,
// once the reaper has failed the jobs they control, and applies the group
// policies again.
func HandleLostInstances(ctx context.Context, app *api.App, leader string, deprovision bool) {
	if deprovision {
		var gone []string
		err := pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `DELETE FROM instances i WHERE `+lostNodes+` AND NOT EXISTS (SELECT 1 FROM unified_jobs j
				WHERE j.controller_node = i.hostname AND j.status IN ('waiting', 'running') AND j.type <> ALL($2)) RETURNING i.hostname`,
				leader, jobs.Processless())
			if err != nil {
				return err
			}
			if gone, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || len(gone) == 0 {
				return err
			}
			return ApplyMembershipPolicies(ctx, tx)
		})
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("deprovisioning lost instances", "err", err)
			}
			return
		}
		for _, h := range gone {
			slog.Info("instance automatically deprovisioned", "hostname", h)
		}
	}
	rows, err := app.DB.Query(ctx, `UPDATE instances SET node_state = 'unavailable', capacity = 0, cpu_capacity = 0, mem_capacity = 0, errors = $2
		WHERE `+lostNodes+` AND node_state = 'ready' RETURNING hostname, last_seen`, leader, jobs.LostError)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("marking lost instances", "err", err)
		}
		return
	}
	defer rows.Close()
	for rows.Next() {
		var host string
		var seen *time.Time
		if rows.Scan(&host, &seen) == nil {
			slog.Warn("instance lost: marked unavailable", "hostname", host, "last_seen", seen)
		}
	}
}

// Rejoin registers node again, measured, and applies the group policies:
// the job manager's Rejoin, for a node marked lost whose heartbeats
// resume, or one deleted as lost that comes back.
func Rejoin(ctx context.Context, app *api.App, node string) error {
	return pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
		tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "instance_registration_"+node)
		if err := registerNode(ctx, tx, app, node); err != nil {
			return err
		}
		return ApplyMembershipPolicies(ctx, tx)
	})
}
