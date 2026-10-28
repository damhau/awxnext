package resources

import (
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/settings"
)

// registerNamedURLs declares AWX's named URL graph: what identifies an object
// of each resource (its unique fields, then the objects it belongs to), and
// AWX's read-only NAMED_URL_FORMATS and NAMED_URL_GRAPH_NODES settings.
func registerNamedURLs() {
	org := api.NamedFK{Field: "organization", Column: "organization_id", Target: "organizations"}
	inv := api.NamedFK{Field: "inventory", Column: "inventory_id", Target: "inventories"}
	api.SetNamedURL("organizations", []string{"name"})
	api.SetNamedURL("users", []string{"username"})
	api.SetNamedURL("instances", []string{"hostname"})
	api.SetNamedURL("instance_groups", []string{"name"})
	api.SetNamedURL("credential_types", []string{"name", "kind"})
	api.SetNamedURL("credentials", []string{"name"},
		api.NamedFK{Field: "credential_type", Column: "credential_type_id", Target: "credential_types"}, org)
	for _, res := range []string{"teams", "inventories", "projects", "job_templates", "workflow_job_templates", "system_job_templates",
		"notification_templates", "labels"} {
		api.SetNamedURL(res, []string{"name"}, org)
	}
	for _, res := range []string{"hosts", "groups", "inventory_sources"} {
		api.SetNamedURL(res, []string{"name"}, inv)
	}
	api.SetNamedURL("workflow_job_template_nodes", []string{"identifier"},
		api.NamedFK{Field: "workflow_job_template", Column: "workflow_job_template_id", Target: "workflow_job_templates"})

	settings.Register(&settings.Def{Key: "NAMED_URL_FORMATS", Category: "named-url", Type: "nested object", ReadOnly: true,
		Default: api.NamedURLFormats(), Label: "Formats of all available named urls",
		Help: "Read-only list of key-value pairs that shows the standard format of all available named URLs."})
	settings.Register(&settings.Def{Key: "NAMED_URL_GRAPH_NODES", Category: "named-url", Type: "nested object", ReadOnly: true,
		Default: api.NamedURLGraphNodes(), Label: "List of all named url graph nodes.",
		Help: "Read-only list of key-value pairs that exposes named URL graph topology. Use this list to programmatically generate named URLs for resources"})
}
