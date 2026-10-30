package resources

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
)

// The awx-manage commands for instances and instance groups, with AWX's
// options and messages: provision_instance, deprovision_instance,
// register_queue, unregister_queue, remove_from_queue and list_instances.

// CommandFailed is a command's failure, exiting with status 1: awx-manage's
// CommandError (a missing argument), or a message the command printed.
type CommandFailed struct {
	Message string
	Printed bool
}

func (e *CommandFailed) Error() string { return e.Message }

// changed is AWX's last line when a command changed something.
func changed(w io.Writer, c bool) {
	if c {
		fmt.Fprintln(w, "(changed: True)")
	}
}

// ProvisionInstance is provision_instance: it registers an instance by
// hostname (or, given, by UUID), creating it installed and with no
// capacity, or updating its hostname and node type; as in AWX, an instance
// already registered is set back to installed until it shows up.
func ProvisionInstance(ctx context.Context, app *api.App, w io.Writer, hostname, nodeType, uuid string) error {
	if hostname == "" {
		return &CommandFailed{Message: "--hostname is a required argument: control nodes register themselves when they start."}
	}
	switch nodeType {
	case "":
		nodeType = "hybrid"
	case "control", "execution", "hop", "hybrid":
	default:
		return &CommandFailed{Message: fmt.Sprintf("argument --node_type: invalid choice: %q (choose from 'control', 'execution', 'hop', 'hybrid')", nodeType)}
	}
	err := pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
		tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "instance_registration_"+hostname)
		var id int64
		err := pgx.ErrNoRows
		if uuid != "" {
			err = tx.QueryRow(ctx, "SELECT id FROM instances WHERE uuid = $1", uuid).Scan(&id)
		}
		if err == pgx.ErrNoRows {
			err = tx.QueryRow(ctx, "SELECT id FROM instances WHERE hostname = $1", hostname).Scan(&id)
		}
		switch err {
		case nil:
			if _, err := tx.Exec(ctx, "UPDATE instances SET node_state = 'installed', hostname = $2, node_type = $3, modified = now() WHERE id = $1", id, hostname, nodeType); err != nil {
				return err
			}
		case pgx.ErrNoRows:
			if _, err := tx.Exec(ctx, `INSERT INTO instances (hostname, node_type, node_state, capacity, managed, uuid)
				VALUES ($1, $2, 'installed', 0, true, coalesce(nullif($3, ''), gen_random_uuid()::text))`, hostname, nodeType, uuid); err != nil {
				return err
			}
		default:
			return err
		}
		return ApplyMembershipPolicies(ctx, tx)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Successfully registered instance %s\n", hostname)
	changed(w, true)
	return nil
}

// DeprovisionInstance is deprovision_instance: it removes an instance.
func DeprovisionInstance(ctx context.Context, app *api.App, w io.Writer, hostname string) error {
	if hostname == "" {
		return &CommandFailed{Message: "--hostname is a required argument"}
	}
	var removed bool
	err := pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
		tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "instance_registration_"+hostname)
		tag, err := tx.Exec(ctx, "DELETE FROM instances WHERE hostname = $1", hostname)
		if err != nil {
			return err
		}
		if removed = tag.RowsAffected() > 0; !removed {
			return nil
		}
		return ApplyMembershipPolicies(ctx, tx)
	})
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintf(w, "No instance found matching name %s\n", hostname)
		return nil
	}
	fmt.Fprintln(w, "Instance Removed")
	fmt.Fprintf(w, "Successfully deprovisioned %s\n", hostname)
	changed(w, true)
	return nil
}

// Queue is register_queue's options.
type Queue struct {
	Name                    string
	Hostnames               []string
	Percent, Minimum        int
	ContainerGroup          bool
	PodSpecOverride         string
	MaxForks, MaxConcurrent int
}

// RegisterQueue is register_queue: it creates or updates an instance group
// with the given policies, and lists the given instances in it (never
// removing any). As in AWX, the percentage and minimum are set, 0 when not
// given; the other fields only when given.
func RegisterQueue(ctx context.Context, app *api.App, w io.Writer, q Queue) error {
	if q.Name == "" {
		return &CommandFailed{Message: "Specify `--queuename` to use this command."}
	}
	var created, updated, listed bool
	var added []string
	var missing string
	err := pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
		tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('cluster_policy_lock'))")
		var id int64
		var pct, min, forks, jobs int
		var container bool
		var spec string
		var list []string
		err := tx.QueryRow(ctx, `SELECT id, policy_instance_percentage, policy_instance_minimum, is_container_group, pod_spec_override,
			max_forks, max_concurrent_jobs, ARRAY(SELECT jsonb_array_elements_text(policy_instance_list)) FROM instance_groups WHERE name = $1`, q.Name).
			Scan(&id, &pct, &min, &container, &spec, &forks, &jobs, &list)
		if err == pgx.ErrNoRows {
			created = true
			if err = tx.QueryRow(ctx, "INSERT INTO instance_groups (name) VALUES ($1) RETURNING id", q.Name).Scan(&id); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		updated = pct != q.Percent || min != q.Minimum || (q.ContainerGroup && !container) ||
			(q.PodSpecOverride != "" && spec != q.PodSpecOverride) || (q.MaxForks != 0 && forks != q.MaxForks) || (q.MaxConcurrent != 0 && jobs != q.MaxConcurrent)
		if updated {
			if _, err := tx.Exec(ctx, `UPDATE instance_groups SET policy_instance_percentage = $2, policy_instance_minimum = $3,
				is_container_group = is_container_group OR $4, pod_spec_override = CASE WHEN $5 <> '' THEN $5 ELSE pod_spec_override END,
				max_forks = CASE WHEN $6 <> 0 THEN $6 ELSE max_forks END, max_concurrent_jobs = CASE WHEN $7 <> 0 THEN $7 ELSE max_concurrent_jobs END,
				modified = now() WHERE id = $1`, id, q.Percent, q.Minimum, q.ContainerGroup, q.PodSpecOverride, q.MaxForks, q.MaxConcurrent); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, h := range q.Hostnames {
			if h = strings.TrimSpace(h); h == "" || seen[h] {
				continue
			}
			seen[h] = true
			var iid int64
			if err := tx.QueryRow(ctx, "SELECT id FROM instances WHERE hostname = $1 AND node_type <> 'hop'", h).Scan(&iid); err != nil {
				missing = h
				break
			}
			if _, err := tx.Exec(ctx, "INSERT INTO instance_group_instances (instance_group_id, instance_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, iid); err != nil {
				return err
			}
			added = append(added, h)
			tag, err := tx.Exec(ctx, `UPDATE instance_groups SET policy_instance_list = policy_instance_list || to_jsonb($2::text)
				WHERE id = $1 AND NOT policy_instance_list ? $2`, id, h)
			if err != nil {
				return err
			}
			listed = listed || tag.RowsAffected() > 0
		}
		return ApplyMembershipPolicies(ctx, tx)
	})
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(w, "Creating instance group %s\n", q.Name)
	} else {
		fmt.Fprintf(w, "Instance Group already registered %s\n", q.Name)
	}
	for _, h := range added {
		fmt.Fprintf(w, "Added instance %s to %s\n", h, q.Name)
	}
	changed(w, updated || listed)
	if missing != "" {
		msg := "Instance does not exist or cannot run jobs: " + missing
		fmt.Fprintln(w, msg)
		return &CommandFailed{msg, true}
	}
	return nil
}

// UnregisterQueue is unregister_queue: it removes an instance group; its
// instances stay. The installer's groups stay too, as in the API.
func UnregisterQueue(ctx context.Context, app *api.App, w io.Writer, name string) error {
	if name == "" {
		return &CommandFailed{Message: "Must specify `--queuename` in order to use command."}
	}
	if installerGroup(name) {
		msg := fmt.Sprintf("The %s instance group cannot be removed.", name)
		fmt.Fprintln(w, msg)
		return &CommandFailed{msg, true}
	}
	var removed bool
	err := pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
		tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "instance_group_registration_"+name)
		tag, err := tx.Exec(ctx, "DELETE FROM instance_groups WHERE name = $1", name)
		if err != nil {
			return err
		}
		if removed = tag.RowsAffected() > 0; !removed {
			return nil
		}
		return ApplyMembershipPolicies(ctx, tx)
	})
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintln(w, "Instance group doesn't exist")
		return &CommandFailed{"Instance group doesn't exist", true}
	}
	fmt.Fprintln(w, "Instance Group Removed")
	changed(w, true)
	return nil
}

// RemoveFromQueue is remove_from_queue: it takes an instance out of an
// instance group and its policy list; the group's policies may keep it.
func RemoveFromQueue(ctx context.Context, app *api.App, w io.Writer, name, hostname string) error {
	if name == "" {
		return &CommandFailed{Message: "Must specify `--queuename` in order to use command."}
	}
	var gid, iid int64
	if app.DB.QueryRow(ctx, "SELECT id FROM instance_groups WHERE name = $1", name).Scan(&gid) != nil {
		fmt.Fprintln(w, "Queue doesn't exist")
		return &CommandFailed{"Queue doesn't exist", true}
	}
	if app.DB.QueryRow(ctx, "SELECT id FROM instances WHERE hostname = $1", hostname).Scan(&iid) != nil {
		fmt.Fprintln(w, "Host doesn't exist")
		return &CommandFailed{"Host doesn't exist", true}
	}
	err := pgx.BeginFunc(ctx, app.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM instance_group_instances WHERE instance_group_id = $1 AND instance_id = $2", gid, iid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE instance_groups SET policy_instance_list = policy_instance_list - $2::text WHERE id = $1", gid, hostname); err != nil {
			return err
		}
		return ApplyMembershipPolicies(ctx, tx)
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Instance removed from instance group")
	return nil
}

// ListInstances is list_instances: each instance group with its capacity
// and policy, then its instances, and the instances in no group; green,
// red without capacity, grey when disabled, unless color is off.
func ListInstances(ctx context.Context, app *api.App, w io.Writer, color bool) error {
	type inst struct {
		host, kind, version string
		capacity            int
		enabled             bool
		seen                *time.Time
	}
	type group struct {
		name     string
		pct, min int
		members  []inst
	}
	scanInsts := func(sql string, args ...any) ([]inst, error) {
		rows, err := app.DB.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []inst
		for rows.Next() {
			var i inst
			if err := rows.Scan(&i.host, &i.kind, &i.version, &i.capacity, &i.enabled, &i.seen); err != nil {
				return nil, err
			}
			out = append(out, i)
		}
		return out, rows.Err()
	}
	const cols = "i.hostname, i.node_type, i.version, i.capacity, i.enabled, i.last_seen"
	rows, err := app.DB.Query(ctx, "SELECT id, name, policy_instance_percentage, policy_instance_minimum FROM instance_groups ORDER BY id")
	if err != nil {
		return err
	}
	var groups []group
	var ids []int64
	for rows.Next() {
		var g group
		var id int64
		if err := rows.Scan(&id, &g.name, &g.pct, &g.min); err != nil {
			rows.Close()
			return err
		}
		groups, ids = append(groups, g), append(ids, id)
	}
	rows.Close()
	for n := range groups {
		if groups[n].members, err = scanInsts(`SELECT `+cols+` FROM instances i JOIN instance_group_instances gi ON gi.instance_id = i.id
			WHERE gi.instance_group_id = $1 ORDER BY i.hostname`, ids[n]); err != nil {
			return err
		}
	}
	loose, err := scanInsts(`SELECT ` + cols + ` FROM instances i WHERE NOT EXISTS (SELECT 1 FROM instance_group_instances gi WHERE gi.instance_id = i.id) ORDER BY i.hostname`)
	if err != nil {
		return err
	}
	if len(loose) > 0 {
		groups = append(groups, group{name: "ungrouped", members: loose})
	}
	for _, g := range groups {
		capacity := 0
		for _, i := range g.members {
			capacity += i.capacity
		}
		// As AWX: a minimum replaces a percentage in the header.
		policy := ""
		if g.pct != 0 {
			policy = fmt.Sprintf(" policy=%d%%", g.pct)
		}
		if g.min != 0 {
			policy = fmt.Sprintf(" policy>=%d", g.min)
		}
		fmt.Fprintf(w, "[%s capacity=%d%s]\n", g.name, capacity, policy)
		for _, i := range g.members {
			start, end := "\033[92m", "\033[0m"
			if i.capacity == 0 && i.kind != "hop" {
				start = "\033[91m"
			}
			if !i.enabled {
				start = "\033[90m[DISABLED] "
			}
			if !color {
				// As AWX, without color nothing marks a disabled instance.
				start, end = "", ""
			}
			line := i.host
			if i.kind != "hop" {
				v := i.version
				if v == "" {
					v = "?"
				}
				line += fmt.Sprintf(" capacity=%d node_type=%s version=%s", i.capacity, i.kind, v)
			} else {
				line += " node_type=hop"
			}
			if i.seen != nil {
				line += fmt.Sprintf(` heartbeat="%s"`, i.seen.UTC().Format("2006-01-02 15:04:05"))
			}
			fmt.Fprintf(w, "\t%s%s%s\n", start, line, end)
		}
		fmt.Fprintln(w)
	}
	return nil
}
