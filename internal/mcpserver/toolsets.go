package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/damhau/awxnext/internal/api"
)

// The toolsets: AAP's MCP server's names, and awxnext's event_automation
// and self_service. Each has its resources' generated tools and its tasks.
var toolsetDefs = []struct {
	name, description string
	resources         []string
	tasks             func() []*tool
}{
	{"job_management", "Launch job templates, workflows and ad hoc commands, follow jobs and read why they failed, relaunch and cancel them, decide approvals, sync projects, and manage templates, schedules and labels.",
		[]string{"job_templates", "workflow_job_templates", "workflow_job_template_nodes", "workflow_approval_templates", "jobs", "workflow_jobs", "workflow_job_nodes",
			"ad_hoc_commands", "workflow_approvals", "schedules", "projects", "project_updates", "labels", "unified_jobs", "unified_job_templates",
			"system_job_templates", "system_jobs", "job_events", "job_host_summaries", "ad_hoc_command_events", "project_update_events"},
		jobTools},
	{"inventory_management", "Inventories, hosts and groups: a host's details, facts, groups, recent results and task history, drift, and inventory source syncs.",
		[]string{"inventories", "constructed_inventories", "hosts", "groups", "inventory_sources", "inventory_updates", "inventory_update_events", "host_drift", "host_metrics", "host_metric_summary_monthly"},
		inventoryTools},
	{"system_monitoring", "The instances and their health, what is running and waiting, recent failures, the activity stream, reports of template runs, and notifications that keep failing.",
		[]string{"instances", "activity_stream", "notification_templates", "notifications", "object_versions"},
		monitoringTools},
	{"user_management", "Users, teams and organizations, and the roles they are granted.",
		[]string{"users", "teams", "organizations", "role_definitions", "role_user_assignments", "role_team_assignments", "roles"},
		userTools},
	{"security_compliance", "Credentials and credential types (secrets come back $encrypted$), credential tests, and launch policies with their decisions and backtests.",
		[]string{"credentials", "credential_types", "credential_input_sources", "launch_policies", "launch_policy_decisions"},
		securityTools},
	{"platform_configuration", "Settings by category, execution environments and their contents, instance groups, maintenance windows and folders.",
		[]string{"execution_environments", "instance_groups", "maintenance_windows", "folders"},
		configurationTools},
	{"content_discovery", "The collection hub: namespaces, collections with their versions, signatures and repositories, remotes and syncs.",
		[]string{"collection_namespaces", "collections", "collection_versions", "collection_imports", "collection_signing_keys", "collection_version_signatures",
			"collection_remotes", "collection_syncs", "collection_sync_events"},
		contentTools},
	{"event_automation", "Event streams and their events, event rules with their decisions, breakers and checks, a rule's backtest and mode, connectors and rulebooks.",
		[]string{"event_streams", "stream_events", "event_rules", "event_rule_decisions", "event_rule_breakers", "event_rule_patterns", "event_rule_checks",
			"event_connectors", "event_connector_runs", "rulebooks"},
		eventTools},
	{"self_service", "The self-service portal: catalog items and their forms, requests with their answers and progress, deployments and their day-2 actions, and the requests waiting for the user's approval.",
		[]string{"catalog_items", "catalog_requests", "deployments", "choice_sources"},
		selfServiceTools},
}

// toolsets builds the toolsets from the registered resources: a resource
// the API doesn't have is a mistake in toolsetDefs.
func toolsets() []*toolset {
	var out []*toolset
	for _, d := range toolsetDefs {
		t := &toolset{name: d.name, description: d.description}
		for _, name := range d.resources {
			res := api.Lookup(name)
			if res == nil {
				panic(fmt.Sprintf("mcpserver: toolset %s names resource %s, which the API doesn't have", d.name, name))
			}
			t.tools = append(t.tools, resourceTools(res)...)
		}
		t.tools = append(t.tools, d.tasks()...)
		out = append(out, t)
	}
	return out
}

// discoverServer is a new discover session's server: it lists the
// toolsets, and load_toolset adds one's tools to the session, which then
// gets notifications/tools/list_changed.
func (s *Server) discoverServer(r *http.Request) *mcp.Server {
	d := &discovery{s: s, srv: s.newServer(instructions("discover")), base: baseURL(s.app, r), loaded: map[string]bool{}}
	d.srv.AddTool(&mcp.Tool{Name: "list_toolsets", Description: "The toolsets: what each is for, its own URL, how many tools it has for this user, and whether this session has loaded it.",
		InputSchema: object(nil), Annotations: readOnly("List toolsets")}, d.list)
	d.srv.AddTool(&mcp.Tool{Name: "load_toolset", Description: "Add a toolset's tools to this session. The tool list changes: list the tools again to see them.",
		InputSchema: object([]string{"toolset"}, prop{"toolset", enum("The toolset's name.", s.names()...)}),
		Annotations: &mcp.ToolAnnotations{Title: "Load a toolset", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)}}, d.load)
	return d.srv
}

func (s *Server) names() []string {
	var out []string
	for _, t := range s.sets {
		out = append(out, t.name)
	}
	return out
}

// discovery is a discover session: its server, and the toolsets it loaded.
type discovery struct {
	s      *Server
	srv    *mcp.Server
	base   string
	mu     sync.Mutex
	loaded map[string]bool
}

func (d *discovery) list(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	write := d.s.canWrite(ctx, principal(req.Extra.TokenInfo))
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []map[string]any
	for _, t := range d.s.sets {
		out = append(out, map[string]any{"name": t.name, "description": t.description, "url": d.base + resourcePath(t.name),
			"tools": len(available(t, write)), "loaded": d.loaded[t.name]})
	}
	return textResult(map[string]any{"toolsets": out, "writes_allowed": write})
}

func (d *discovery) load(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Toolset string `json:"toolset"`
	}
	json.Unmarshal(req.Params.Arguments, &args)
	t := d.s.set(args.Toolset)
	if t == nil {
		return failure("toolset is one of " + strings.Join(d.s.names(), ", ") + "."), nil
	}
	write := d.s.canWrite(ctx, principal(req.Extra.TokenInfo))
	d.mu.Lock()
	d.loaded[t.name] = true
	d.mu.Unlock()
	d.s.addTools(d.srv, t, write)
	var names []string
	for _, tl := range available(t, write) {
		names = append(names, tl.def.Name)
	}
	sort.Strings(names)
	return textResult(map[string]any{"loaded": t.name, "tools": names})
}

func available(t *toolset, write bool) []*tool {
	var out []*tool
	for _, tl := range t.tools {
		if !tl.write || write {
			out = append(out, tl)
		}
	}
	return out
}

func textResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
}
