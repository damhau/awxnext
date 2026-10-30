package resources

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/damhau/awxnext/internal/api"
)

// ApplyMembershipPolicies is AWX's apply_cluster_membership_policies: each
// instance group's members are computed from its policy_instance_list, then
// policy_instance_minimum, then policy_instance_percentage, and the groups
// are changed to match. Hop nodes join no group; execution nodes never join
// controlplane and control nodes never join another group; instances with
// managed_by_policy off join only the groups that list them. Container
// groups are left alone. Run it in a transaction: it holds AWX's
// cluster_policy_lock while it works.
func ApplyMembershipPolicies(ctx context.Context, db api.DBTX) error {
	if _, err := db.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('cluster_policy_lock'))"); err != nil {
		return err
	}
	type instance struct {
		id              int64
		hostname, kind  string
		managedByPolicy bool
		groups          int
	}
	type group struct {
		id                  int64
		name                string
		container           bool
		list                []string
		minimum, percentage int
		members             map[int64]bool
		prior               map[int64]bool
	}
	rows, err := db.Query(ctx, "SELECT id, hostname, node_type, managed_by_policy FROM instances WHERE node_type <> 'hop' ORDER BY id")
	if err != nil {
		return err
	}
	var all []*instance
	byHost := map[string]*instance{}
	for rows.Next() {
		i := &instance{}
		if err := rows.Scan(&i.id, &i.hostname, &i.kind, &i.managedByPolicy); err != nil {
			rows.Close()
			return err
		}
		all = append(all, i)
		byHost[i.hostname] = i
	}
	rows.Close()
	rows, err = db.Query(ctx, `SELECT g.id, g.name, g.is_container_group, coalesce(g.policy_instance_list, '[]'::jsonb)::text, g.policy_instance_minimum,
		g.policy_instance_percentage, coalesce(array_agg(gi.instance_id) FILTER (WHERE gi.instance_id IS NOT NULL), '{}')
		FROM instance_groups g LEFT JOIN instance_group_instances gi ON gi.instance_group_id = g.id GROUP BY g.id ORDER BY g.id`)
	if err != nil {
		return err
	}
	var groups []*group
	for rows.Next() {
		g := &group{members: map[int64]bool{}, prior: map[int64]bool{}}
		var list string
		var prior []int64
		if err := rows.Scan(&g.id, &g.name, &g.container, &list, &g.minimum, &g.percentage, &prior); err != nil {
			rows.Close()
			return err
		}
		json.Unmarshal([]byte(list), &g.list)
		for _, id := range prior {
			g.prior[id] = true
		}
		groups = append(groups, g)
	}
	rows.Close()

	// The policy list first: manually managed memberships, which do not
	// count towards an instance's groups for the minimums.
	for _, g := range groups {
		for _, h := range g.list {
			if i := byHost[h]; i != nil {
				g.members[i.id] = true
			}
		}
	}
	var managed []*instance
	for _, i := range all {
		if i.managedByPolicy {
			managed = append(managed, i)
		}
	}
	excluded := func(g *group) string {
		if g.name == "controlplane" {
			return "execution"
		}
		return "control"
	}
	// Python's sorted is stable: ties keep their order.
	byMembers := func() []*group {
		out := append([]*group(nil), groups...)
		sort.SliceStable(out, func(a, b int) bool { return len(out[a].members) < len(out[b].members) })
		return out
	}
	byGroups := func() []*instance {
		out := append([]*instance(nil), managed...)
		sort.SliceStable(out, func(a, b int) bool { return out[a].groups < out[b].groups })
		return out
	}
	// Minimums, a lower bound of instances for each group.
	for _, g := range byMembers() {
		for _, i := range byGroups() {
			if i.kind == excluded(g) {
				continue
			}
			if len(g.members) >= g.minimum {
				break
			}
			if g.members[i.id] {
				continue
			}
			g.members[i.id] = true
			i.groups++
		}
	}
	// Then percentages of the instances the group may hold.
	for _, g := range byMembers() {
		pool := 0
		for _, i := range managed {
			if i.kind != excluded(g) {
				pool++
			}
		}
		if pool == 0 {
			continue
		}
		for _, i := range byGroups() {
			if i.kind == excluded(g) || g.members[i.id] {
				continue
			}
			if 100*float64(len(g.members))/float64(pool) >= float64(g.percentage) {
				break
			}
			g.members[i.id] = true
			i.groups++
		}
	}
	for _, g := range groups {
		if g.container {
			continue
		}
		for id := range g.members {
			if !g.prior[id] {
				if _, err := db.Exec(ctx, "INSERT INTO instance_group_instances (instance_group_id, instance_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", g.id, id); err != nil {
					return err
				}
			}
		}
		for id := range g.prior {
			if !g.members[id] {
				if _, err := db.Exec(ctx, "DELETE FROM instance_group_instances WHERE instance_group_id = $1 AND instance_id = $2", g.id, id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
