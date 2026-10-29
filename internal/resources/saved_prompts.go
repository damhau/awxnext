package resources

import (
	"fmt"

	"github.com/damhau/awxnext/internal/api"
)

// Labels and instance groups saved as launch prompts on schedules and
// workflow nodes, as AWX's launch configurations keep them. As in AWX,
// attaching one only takes seeing the label or using the instance group;
// the jobs launched get them when their template prompts for them, and
// ignore them otherwise.

// promptLabelSub lists, attaches and detaches the labels saved on a schedule or node.
func promptLabelSub(table, column string) *SubList {
	s := labelSub(table, column)
	s.CanAttach = func(c *Ctx, parent Obj, lid int64) error { return canUseLabel(c, lid) }
	return s
}

// groupSub lists the ordered instance groups of a schedule or node; unless
// readOnly, attaching appends one (it must be usable) and detaching removes it.
func groupSub(table, column string, readOnly bool) *SubList {
	s := &SubList{
		Name: "instance_groups", Target: "instance_groups", NoCreate: true, ReadOnly: readOnly,
		Filter: func(arg ArgFunc, pid int64) string {
			return fmt.Sprintf("t.id IN (SELECT instance_group_id FROM %s WHERE %s = %s)", table, column, arg(pid))
		},
		Order: func(pid int64) string {
			return fmt.Sprintf("(SELECT position FROM %s WHERE %s = %d AND instance_group_id = t.id)", table, column, pid)
		},
	}
	if readOnly {
		return s
	}
	s.CanAttach = func(c *Ctx, parent Obj, gid int64) error {
		return api.RequirePermID(c, "instance_groups", "instance_groups", gid, "use")
	}
	s.Associate = func(c *Ctx, pid, gid int64) error {
		_, err := c.DB().Exec(c, fmt.Sprintf(`INSERT INTO %[1]s (%[2]s, instance_group_id, position)
			VALUES ($1, $2, (SELECT coalesce(max(position) + 1, 0) FROM %[1]s WHERE %[2]s = $1)) ON CONFLICT DO NOTHING`, table, column), pid, gid)
		return err
	}
	s.Disassociate = func(c *Ctx, pid, gid int64) error {
		_, err := c.DB().Exec(c, fmt.Sprintf("DELETE FROM %s WHERE %s = $1 AND instance_group_id = $2", table, column), pid, gid)
		return err
	}
	return s
}

// savedPrompts adds the labels and instance groups saved in two tables to a
// launch's trusted prompts (labels after any already there), those the
// template prompts for.
func savedPrompts(c *Ctx, trusted Obj, tpl Obj, labelTable, groupTable, column string, id int64) error {
	if boolv(tpl["ask_labels_on_launch"]) {
		labels, err := idList(c, fmt.Sprintf("SELECT label_id FROM %s WHERE %s = $1 ORDER BY label_id", labelTable, column), id)
		if err != nil {
			return err
		}
		addLabels(trusted, labels)
	}
	if groupTable == "" || !boolv(tpl["ask_instance_groups_on_launch"]) {
		return nil
	}
	groups, err := idList(c, fmt.Sprintf("SELECT instance_group_id FROM %s WHERE %s = $1 ORDER BY position", groupTable, column), id)
	if err != nil {
		return err
	}
	if len(groups) > 0 {
		list := make([]any, len(groups))
		for i, g := range groups {
			list[i] = g
		}
		trusted["instance_groups"] = list
	}
	return nil
}

// addLabels adds labels to a launch's trusted prompts, once each.
func addLabels(trusted Obj, labels []int64) {
	if len(labels) == 0 {
		return
	}
	list, _ := trusted["labels"].([]any)
	seen := map[int64]bool{}
	for _, l := range list {
		if id, ok := api.ID(l); ok {
			seen[id] = true
		}
	}
	for _, l := range labels {
		if !seen[l] {
			seen[l] = true
			list = append(list, l)
		}
	}
	trusted["labels"] = list
}

// nodeLabelsAndGroups adds to a node's launch its saved labels and instance
// groups, and, when its template prompts for labels, the workflow job's
// labels: a workflow's labels reach the jobs that can take them.
func nodeLabelsAndGroups(c *Ctx, trusted Obj, wfJob, node int64, tpl Obj) error {
	if err := savedPrompts(c, trusted, tpl, "workflow_job_node_labels", "workflow_job_node_instance_groups", "node_id", node); err != nil {
		return err
	}
	if !boolv(tpl["ask_labels_on_launch"]) {
		return nil
	}
	labels, err := idList(c, "SELECT label_id FROM unified_job_labels WHERE unified_job_id = $1 ORDER BY label_id", wfJob)
	if err != nil {
		return err
	}
	addLabels(trusted, labels)
	return nil
}
