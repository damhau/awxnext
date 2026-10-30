package resources

import (
	"net/http"
	"strconv"

	"github.com/damhau/awxnext/internal/api"
)

// used_by (awxnext): what refers to an object, for its "Used by" panel and
// its delete confirmation (roadmap M7-11). Each dependent says which of its
// fields refers to the object and what deleting the object does to it:
// "deleted" with it, or "cleared" (the dependent loses it). Dependents the
// user cannot see are only counted.

type dependency struct {
	field  string
	effect string
	// query selects the dependents' resource, id and name; $1 is the object's id.
	query string
}

const ujtResource = `t.type || 's'`

var dependencies = map[string][]dependency{
	"inventories": {
		{"inventory", "cleared", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_templates t
			WHERE t.inventory_id = $1 AND t.type IN ('job_template', 'workflow_job_template')`},
		{"node_inventory", "cleared", `SELECT 'workflow_job_templates', t.id, t.name FROM workflow_job_template_nodes n
			JOIN unified_job_templates t ON t.id = n.workflow_job_template_id WHERE n.inventory_id = $1`},
		{"inventory", "cleared", `SELECT 'schedules', s.id, s.name FROM schedules s WHERE s.inventory_id = $1`},
		{"input_inventories", "cleared", `SELECT 'inventories', i.id, i.name FROM inventory_input_inventories x
			JOIN inventories i ON i.id = x.inventory_id WHERE x.input_inventory_id = $1`},
	},
	"credentials": {
		{"credentials", "cleared", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_template_credentials x
			JOIN unified_job_templates t ON t.id = x.unified_job_template_id WHERE x.credential_id = $1`},
		{"credential", "cleared", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_templates t WHERE t.credential_id = $1`},
		{"webhook_credential", "cleared", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_templates t WHERE t.webhook_credential_id = $1`},
		{"signature_validation_credential", "cleared", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_templates t
			WHERE t.signature_validation_credential_id = $1`},
		{"node_credentials", "cleared", `SELECT 'workflow_job_templates', t.id, t.name FROM workflow_job_template_node_credentials x
			JOIN workflow_job_template_nodes n ON n.id = x.node_id JOIN unified_job_templates t ON t.id = n.workflow_job_template_id
			WHERE x.credential_id = $1`},
		{"credentials", "cleared", `SELECT 'schedules', s.id, s.name FROM schedule_credentials x JOIN schedules s ON s.id = x.schedule_id
			WHERE x.credential_id = $1`},
		{"credential", "cleared", `SELECT 'execution_environments', e.id, e.name FROM execution_environments e WHERE e.credential_id = $1`},
		{"input_sources", "cleared", `SELECT 'credentials', c.id, c.name FROM credential_input_sources x
			JOIN credentials c ON c.id = x.target_credential_id WHERE x.source_credential_id = $1 AND x.target_credential_id <> $1`},
		{"galaxy_credentials", "cleared", `SELECT 'organizations', o.id, o.name FROM organization_galaxy_credentials x
			JOIN organizations o ON o.id = x.organization_id WHERE x.credential_id = $1`},
		{"credential", "cleared", `SELECT 'instance_groups', g.id, g.name FROM instance_groups g WHERE g.credential_id = $1`},
	},
	"projects": {
		{"source_project", "deleted", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_templates t WHERE t.source_project_id = $1`},
		{"unified_job_template", "deleted", `SELECT 'schedules', s.id, s.name FROM schedules s WHERE s.unified_job_template_id = $1`},
		{"project", "cleared", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_templates t WHERE t.project_id = $1`},
		{"node_template", "cleared", `SELECT 'workflow_job_templates', t.id, t.name FROM workflow_job_template_nodes n
			JOIN unified_job_templates t ON t.id = n.workflow_job_template_id WHERE n.unified_job_template_id = $1`},
		{"project", "cleared", `SELECT 'gitops_sources', g.id, g.name FROM gitops_sources g WHERE g.project_id = $1`},
	},
	"execution_environments": {
		{"execution_environment", "cleared", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_templates t WHERE t.execution_environment_id = $1`},
		{"default_environment", "cleared", `SELECT ` + ujtResource + `, t.id, t.name FROM unified_job_templates t WHERE t.default_environment_id = $1`},
		{"default_environment", "cleared", `SELECT 'organizations', o.id, o.name FROM organizations o WHERE o.default_environment_id = $1`},
		{"node_execution_environment", "cleared", `SELECT 'workflow_job_templates', t.id, t.name FROM workflow_job_template_nodes n
			JOIN unified_job_templates t ON t.id = n.workflow_job_template_id WHERE n.execution_environment_id = $1`},
		{"execution_environment", "cleared", `SELECT 'schedules', s.id, s.name FROM schedules s WHERE s.execution_environment_id = $1`},
	},
}

// Templates: their schedules go with them; workflows using them lose a step's template.
var templateDependencies = []dependency{
	{"unified_job_template", "deleted", `SELECT 'schedules', s.id, s.name FROM schedules s WHERE s.unified_job_template_id = $1`},
	{"node_template", "cleared", `SELECT 'workflow_job_templates', t.id, t.name FROM workflow_job_template_nodes n
		JOIN unified_job_templates t ON t.id = n.workflow_job_template_id WHERE n.unified_job_template_id = $1 AND n.workflow_job_template_id <> $1`},
}

const maxDependents = 500

func installUsedBy() {
	dependencies["job_templates"] = templateDependencies
	dependencies["workflow_job_templates"] = templateDependencies
	for name, deps := range dependencies {
		res := api.Lookup(name)
		if res == nil {
			continue
		}
		deps := deps
		res.Actions = append(res.Actions, &api.Action{Name: "used_by", Methods: []string{"GET"}, Perm: "view", Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
			id, _ := api.ID(obj["id"])
			out, hidden, err := usedBy(c, name, id, deps)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			api.WriteJSON(w, 200, Obj{"count": len(out), "hidden": hidden, "results": out})
		}})
	}
}

func usedBy(c *Ctx, name string, id int64, deps []dependency) ([]Obj, int, error) {
	out := []Obj{}
	hidden := 0
	seen := map[string]bool{}
	for _, d := range deps {
		rows, err := c.DB().Query(c, d.query+" LIMIT $2", id, maxDependents)
		if err != nil {
			return nil, 0, err
		}
		type dep struct {
			res  string
			id   int64
			name string
		}
		var found []dep
		for rows.Next() {
			var x dep
			if rows.Scan(&x.res, &x.id, &x.name) == nil {
				found = append(found, x)
			}
		}
		rows.Close()
		for _, x := range found {
			key := x.res + "/" + d.field + "/" + strconv.FormatInt(x.id, 10)
			if seen[key] {
				continue
			}
			seen[key] = true
			res := api.Lookup(x.res)
			if res == nil {
				continue
			}
			if ok, _ := api.CanView(c, res, x.id); !ok {
				hidden++
				continue
			}
			out = append(out, Obj{"type": res.Type, "resource": res.Name, "id": x.id, "name": x.name, "field": d.field, "effect": d.effect})
		}
	}
	// The global default execution environment.
	if name == "execution_environments" && (c.User.Superuser || c.User.Auditor) {
		if gid, ok := api.ID(c.App.Settings.Get(c, "DEFAULT_EXECUTION_ENVIRONMENT")); ok && gid == id {
			out = append(out, Obj{"type": "setting", "resource": "settings", "id": nil, "name": "DEFAULT_EXECUTION_ENVIRONMENT", "field": "DEFAULT_EXECUTION_ENVIRONMENT", "effect": "cleared"})
		}
	}
	return out, hidden, nil
}
