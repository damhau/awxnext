package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

var mcpToolsets = []string{"job_management", "inventory_management", "system_monitoring", "user_management", "security_compliance",
	"platform_configuration", "content_discovery", "event_automation", "self_service"}

// Each toolset's task tools, and a generated tool of one of its resources.
var mcpToolsetTools = map[string][]string{
	"job_management": {"get_launch_prompts", "launch_job_template", "launch_workflow_job_template", "launch_ad_hoc_command", "get_job_summary",
		"get_job_output", "get_job_changes", "relaunch_job", "cancel_job", "approve_workflow_approval", "deny_workflow_approval", "sync_project", "list_job_templates"},
	"inventory_management":   {"get_host_details", "get_host_facts", "get_host_history", "sync_inventory_source", "list_hosts"},
	"system_monitoring":      {"get_system_health", "run_health_check", "list_running_jobs", "list_recent_failures", "get_report", "list_failing_notifications", "list_activity_stream"},
	"user_management":        {"list_user_roles", "grant_role", "revoke_role", "list_users"},
	"security_compliance":    {"test_credential", "backtest_launch_policy", "get_credential"},
	"platform_configuration": {"get_settings", "update_settings", "get_execution_environment_contents", "list_instance_groups"},
	"content_discovery":      {"get_collection_details", "list_collection_versions"},
	"event_automation":       {"backtest_event_rule", "set_event_rule_mode", "list_event_rules"},
	"self_service": {"get_catalog_item_form", "request_catalog_item", "get_request_progress", "list_deployment_actions", "run_deployment_action",
		"list_pending_approvals", "approve_workflow_approval", "list_catalog_items"},
}

// The task tools named list_.
var mcpListTasks = []string{"list_running_jobs", "list_recent_failures", "list_failing_notifications", "list_user_roles", "list_deployment_actions", "list_pending_approvals"}

// isWriteTool is whether a tool's name says it changes something or needs a POST.
func isWriteTool(name string) bool {
	for _, p := range []string{"create_", "update_", "delete_", "launch_", "relaunch_", "cancel_", "approve_", "deny_", "sync_", "grant_", "revoke_",
		"set_", "request_", "run_", "test_", "backtest_"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Tools listed only when writes are allowed.
var mcpWriteTools = []string{"create_label", "update_label", "delete_label", "launch_job_template", "cancel_job", "grant_role", "update_settings",
	"test_credential", "backtest_event_rule", "request_catalog_item", "run_health_check", "sync_project", "approve_workflow_approval"}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpConnect(t *testing.T, transport *mcp.StreamableClientTransport, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "awxnext-test", Version: "1"}, opts)
	s, err := cl.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect to %s: %v", transport.Endpoint, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mcpSession(t *testing.T, path, token string) *mcp.ClientSession {
	t.Helper()
	return mcpConnect(t, &mcp.StreamableClientTransport{Endpoint: srv.URL + path, HTTPClient: &http.Client{Transport: bearerTransport{token}}}, nil)
}

func mcpTools(t *testing.T, s *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	out := map[string]*mcp.Tool{}
	for tl, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out[tl.Name] = tl
	}
	return out
}

// mcpCall calls a tool: its text, and whether it is an error.
func mcpCall(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// mcpJSON calls a tool that must succeed, and decodes its answer.
func mcpJSON(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	text, isErr := mcpCall(t, s, name, args)
	if isErr {
		t.Fatalf("%s(%v): %s", name, args, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: not a JSON object: %s", name, text)
	}
	return out
}

// mcpFails calls a tool that must fail with the API's answer.
func mcpFails(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	text, isErr := mcpCall(t, s, name, args)
	if !isErr || !strings.Contains(text, want) {
		t.Fatalf("%s(%v): want an error with %q, got %v %s", name, args, want, isErr, text)
	}
}

func newToken(t *testing.T, c *client, scope string) string {
	t.Helper()
	return must(t, c.post("tokens/", map[string]any{"description": "mcp", "scope": scope}), 201).Body["token"].(string)
}

func setSystem(t *testing.T, a *client, key string, v any) {
	t.Helper()
	old := must(t, a.get("settings/system/"), 200).Body[key]
	must(t, a.patch("settings/system/", map[string]any{key: v}), 200)
	t.Cleanup(func() { a.patch("settings/system/", map[string]any{key: old}) })
}

func mcpBase(t *testing.T, a *client) string {
	if base := fmt.Sprint(must(t, a.get("settings/system/"), 200).Body["TOWER_URL_BASE"]); base != "" {
		return strings.TrimRight(base, "/")
	}
	return srv.URL
}

// Each toolset's URL lists its tools, /api/mcp/ all of them; the tools that
// change anything are absent until writes are allowed, and a read-scope
// token never gets them; a request without a token learns where the OAuth
// metadata is.
func TestMCPToolsets(t *testing.T) {
	a := adminClient(t)
	token := newToken(t, a, "write")
	readToken := newToken(t, a, "read")
	setSystem(t, a, "MCP_ALLOW_WRITE_OPERATIONS", false)

	seen := map[string]bool{}
	for _, name := range mcpToolsets {
		tools := mcpTools(t, mcpSession(t, "/api/mcp/"+name+"/", token))
		for n, tl := range tools {
			seen[n] = true
			if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
				t.Errorf("%s: %s is listed while writes are off, and doesn't say it only reads", name, n)
			}
		}
		for _, want := range mcpToolsetTools[name] {
			if _, ok := tools[want]; ok == isWriteTool(want) {
				t.Errorf("%s: %s listed %v while writes are off", name, want, ok)
			}
		}
	}
	all := mcpTools(t, mcpSession(t, "/api/mcp/", token))
	if len(all) != len(seen) {
		t.Errorf("/api/mcp/ has %d tools, the toolsets %d", len(all), len(seen))
	}
	for _, w := range mcpWriteTools {
		if _, ok := all[w]; ok {
			t.Errorf("%s is listed while writes are off", w)
		}
	}
	s := mcpSession(t, "/api/mcp/", token)
	if text, isErr := mcpCall(t, s, "list_labels", map[string]any{"page_size": 1}); isErr {
		t.Fatalf("list_labels: %s", text)
	}

	setSystem(t, a, "MCP_ALLOW_WRITE_OPERATIONS", true)
	for _, name := range mcpToolsets {
		tools := mcpTools(t, mcpSession(t, "/api/mcp/"+name+"/", token))
		for _, want := range mcpToolsetTools[name] {
			if _, ok := tools[want]; !ok {
				t.Errorf("%s lacks %s with writes on", name, want)
			}
		}
	}
	all = mcpTools(t, mcpSession(t, "/api/mcp/", token))
	for _, w := range mcpWriteTools {
		if _, ok := all[w]; !ok {
			t.Errorf("%s is missing with writes on", w)
		}
	}
	if d := all["delete_label"].Annotations; d == nil || d.DestructiveHint == nil || !*d.DestructiveHint || d.ReadOnlyHint {
		t.Errorf("delete_label's annotations: %+v", d)
	}
	if c := all["create_label"].Annotations; c == nil || c.DestructiveHint == nil || *c.DestructiveHint {
		t.Errorf("create_label's annotations: %+v", c)
	}
	in, _ := json.Marshal(all["create_job_template"].InputSchema)
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	json.Unmarshal(in, &schema)
	if !slices.Contains(schema.Required, "name") || schema.Properties["job_type"]["enum"] == nil || schema.Properties["inventory"]["type"] == nil {
		t.Errorf("create_job_template's input schema: %s", in)
	}

	read := mcpTools(t, mcpSession(t, "/api/mcp/", readToken))
	for _, w := range mcpWriteTools {
		if _, ok := read[w]; ok {
			t.Errorf("a read-scope token gets %s", w)
		}
	}
	rs := mcpSession(t, "/api/mcp/", readToken)
	if _, err := rs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_label", Arguments: map[string]any{"name": "x"}}); err == nil {
		t.Error("a read-scope token called create_label")
	}

	base := mcpBase(t, a)
	post := func(path, auth string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	res := post("/api/mcp/job_management/", "")
	if want := fmt.Sprintf(`resource_metadata="%s/.well-known/oauth-protected-resource/api/mcp/job_management/"`, base); res.StatusCode != 401 || !strings.Contains(res.Header.Get("WWW-Authenticate"), want) {
		t.Errorf("no token: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	if res := post("/api/mcp/", "Basic YWRtaW46YWRtaW4="); res.StatusCode != 401 {
		t.Errorf("basic auth: %d", res.StatusCode)
	}
	if res := post("/api/mcp/", "Bearer nope"); res.StatusCode != 401 {
		t.Errorf("a bad token: %d", res.StatusCode)
	}
	if res := post("/api/mcp/nope/", "Bearer "+token); res.StatusCode != 404 {
		t.Errorf("an unknown toolset: %d", res.StatusCode)
	}
	prm := must(t, a.get("/.well-known/oauth-protected-resource/api/mcp/job_management/"), 200).Body
	if prm["resource"] != base+"/api/mcp/job_management/" || prm["authorization_servers"] != nil {
		t.Errorf("protected resource metadata without OIDC: %v", prm)
	}
	must(t, a.get("/.well-known/oauth-protected-resource/api/mcp/nope/"), 404)

	setSystem(t, a, "MCP_ENABLED", false)
	if res := post("/api/mcp/", "Bearer "+token); res.StatusCode != 404 {
		t.Errorf("MCP off: %d", res.StatusCode)
	}
	must(t, a.get("/.well-known/oauth-protected-resource/api/mcp/"), 404)
}

// The generated tools of every resource call the API's handlers: list,
// get, create, update and delete answer as the API does, as the user,
// secrets come back $encrypted$, and the activity stream marks the changes.
func TestMCPGeneratedTools(t *testing.T) {
	a := adminClient(t)
	setSystem(t, a, "MCP_ALLOW_WRITE_OPERATIONS", true)
	s := mcpSession(t, "/api/mcp/", newToken(t, a, "write"))
	tools := mcpTools(t, s)
	user, uid := newUser(t, a)
	us := mcpSession(t, "/api/mcp/", newToken(t, user, "write"))

	const missing = 999999999
	kinds := map[string]int{}
	for name := range tools {
		if !strings.HasPrefix(name, "list_") || slices.Contains(mcpListTasks, name) {
			continue
		}
		if out := mcpJSON(t, s, name, map[string]any{"page_size": 1}); out["count"] == nil {
			t.Errorf("%s: %v", name, out)
		}
		kinds["list"]++
	}
	for name := range tools {
		args := map[string]any{"id": missing}
		switch {
		case strings.HasPrefix(name, "get_") && tools[name].InputSchema != nil && isIDOnly(tools[name]):
		case strings.HasPrefix(name, "update_") && name != "update_settings":
			args["description"] = "x"
		case strings.HasPrefix(name, "delete_"):
		default:
			continue
		}
		mcpFails(t, s, name, args, "The API answered 404")
		kinds[strings.SplitN(name, "_", 2)[0]]++
	}
	for name := range tools {
		if !strings.HasPrefix(name, "create_") {
			continue
		}
		// As a user of no organization, with nothing: refused, or invalid.
		if text, isErr := mcpCall(t, us, name, map[string]any{}); !isErr || !strings.Contains(text, "The API answered 4") {
			t.Errorf("%s by a user with nothing: %v %s", name, isErr, text)
		}
		kinds["create"]++
	}
	t.Logf("generated tools called: %v", kinds)
	if kinds["list"] < 60 || kinds["get"] < 60 || kinds["create"] < 30 || kinds["update"] < 30 || kinds["delete"] < 30 {
		t.Errorf("too few generated tools: %v", kinds)
	}

	org := newOrg(t, a)
	name := uniq("mcp-label")
	label := mcpJSON(t, s, "create_label", map[string]any{"name": name, "organization": org})
	id := label["id"]
	if _, ok := label["related"]; ok {
		t.Error("a tool's answer keeps related")
	}
	if got := mcpJSON(t, s, "list_labels", map[string]any{"filters": map[string]any{"name": name}}); got["count"] != float64(1) {
		t.Errorf("list_labels by name: %v", got)
	}
	if got := mcpJSON(t, s, "list_labels", map[string]any{"filters": map[string]any{"id__in": []any{id, missing}}, "order_by": "-id"}); got["count"] != float64(1) {
		t.Errorf("list_labels by id__in: %v", got)
	}
	mcpJSON(t, s, "update_label", map[string]any{"id": id, "name": name + "-2"})
	if got := mcpJSON(t, s, "get_label", map[string]any{"id": id}); got["name"] != name+"-2" {
		t.Errorf("get_label: %v", got)
	}
	mcpFails(t, s, "update_label", map[string]any{"id": id}, "Give the fields to change")
	mcpFails(t, s, "create_label", map[string]any{"organization": org}, "The API answered 400")
	mcpJSON(t, s, "delete_label", map[string]any{"id": id})
	mcpFails(t, s, "get_label", map[string]any{"id": id}, "404")
	acts := must(t, a.get(fmt.Sprintf("activity_stream/?object1=label&object1_id=%v&order_by=id", id)), 200).results()
	if len(acts) != 3 {
		t.Fatalf("label activity: %v", acts)
	}
	for _, e := range acts {
		if e["via"] != "mcp" {
			t.Errorf("activity entry %v %v: via %q", e["operation"], e["id"], e["via"])
		}
	}
	if n := must(t, a.get("activity_stream/?via=mcp&object1=label&object1_id="+fmt.Sprint(id)), 200).count(); n != 3 {
		t.Errorf("activity_stream filtered by via: %d", n)
	}
	other := must(t, a.post("labels/", map[string]any{"name": uniq("api-label"), "organization": org}), 201).id()
	if e := must(t, a.get(fmt.Sprintf("activity_stream/?object1=label&object1_id=%d", other)), 200).results(); len(e) != 1 || e[0]["via"] != "" {
		t.Errorf("an API change's via: %v", e)
	}

	cred := mcpJSON(t, s, "create_credential", map[string]any{"name": uniq("ssh"), "organization": org, "credential_type": credType(t, a, "ssh"),
		"inputs": map[string]any{"username": "deploy", "password": "mcp-secret"}})
	for _, got := range []map[string]any{cred, mcpJSON(t, s, "get_credential", map[string]any{"id": cred["id"]})} {
		if in := got["inputs"].(map[string]any); in["password"] != "$encrypted$" || in["username"] != "deploy" {
			t.Errorf("credential inputs: %v", in)
		}
	}
	if text, _ := mcpCall(t, s, "list_credentials", map[string]any{"filters": map[string]any{"id": cred["id"]}}); strings.Contains(text, "mcp-secret") || !strings.Contains(text, "$encrypted$") {
		t.Errorf("list_credentials shows a secret: %s", text)
	}

	inv := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": org}), 201).id()
	if got := mcpJSON(t, us, "list_inventories", map[string]any{"filters": map[string]any{"id": inv}}); got["count"] != float64(0) {
		t.Errorf("a user sees an inventory without a role: %v", got)
	}
	mcpFails(t, us, "get_inventory", map[string]any{"id": inv}, "The API answered 40")
	mcpFails(t, us, "update_inventory", map[string]any{"id": inv, "description": "x"}, "The API answered 40")
	rd := roleDef(t, "Inventory Use")
	mcpFails(t, us, "grant_role", map[string]any{"role_definition": rd, "user": uid, "object_id": inv}, "The API answered 4")
	mcpJSON(t, s, "grant_role", map[string]any{"role_definition": rd, "user": uid, "object_id": inv})
	if got := mcpJSON(t, us, "get_inventory", map[string]any{"id": inv}); got["id"] != float64(inv) {
		t.Errorf("get_inventory with Use: %v", got)
	}
	mcpFails(t, us, "update_inventory", map[string]any{"id": inv, "description": "x"}, "The API answered 403")
	roles := mcpJSON(t, s, "list_user_roles", map[string]any{"id": uid})
	if as := roles["assignments"].([]any); len(as) != 1 || fmt.Sprint(as[0].(map[string]any)["object_id"]) != fmt.Sprint(inv) {
		t.Errorf("list_user_roles: %v", roles)
	}
	mcpFails(t, s, "grant_role", map[string]any{"role_definition": rd, "user": uid, "team": 1, "object_id": inv}, "not both")
	mcpJSON(t, s, "revoke_role", map[string]any{"role_definition": rd, "user": uid, "object_id": inv})
	mcpFails(t, us, "get_inventory", map[string]any{"id": inv}, "The API answered 40")
	mcpFails(t, s, "revoke_role", map[string]any{"role_definition": rd, "user": uid, "object_id": inv}, "No such role assignment")

	setSystem(t, a, "MCP_PAGE_SIZE", 2)
	for range 3 {
		a.post("labels/", map[string]any{"name": uniq("page"), "organization": org})
	}
	if got := mcpJSON(t, s, "list_labels", map[string]any{"filters": map[string]any{"organization": org}}); len(got["results"].([]any)) != 2 {
		t.Errorf("MCP_PAGE_SIZE 2: %d results", len(got["results"].([]any)))
	}
	mcpFails(t, s, "list_labels", map[string]any{"page_size": 500}, "page_size goes from 1 to 200")
}

func isIDOnly(tl *mcp.Tool) bool {
	b, _ := json.Marshal(tl.InputSchema)
	var s struct {
		Properties map[string]any `json:"properties"`
	}
	json.Unmarshal(b, &s)
	_, ok := s.Properties["id"]
	return ok && len(s.Properties) == 1
}

// The discover endpoint starts with list_toolsets and load_toolset, and
// loading a toolset adds its tools to the session, which hears of it.
func TestMCPDiscover(t *testing.T) {
	a := adminClient(t)
	setSystem(t, a, "MCP_ALLOW_WRITE_OPERATIONS", false)
	changed := make(chan struct{}, 10)
	s := mcpConnect(t, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/api/mcp/discover/", HTTPClient: &http.Client{Transport: bearerTransport{newToken(t, a, "write")}}},
		&mcp.ClientOptions{ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} }})
	tools := mcpTools(t, s)
	if len(tools) != 2 || tools["list_toolsets"] == nil || tools["load_toolset"] == nil {
		t.Fatalf("discover's tools: %v", tools)
	}
	sets := mcpJSON(t, s, "list_toolsets", nil)["toolsets"].([]any)
	if len(sets) != len(mcpToolsets) {
		t.Fatalf("toolsets: %v", sets)
	}
	base := mcpBase(t, a)
	for _, x := range sets {
		ts := x.(map[string]any)
		if ts["url"] != base+"/api/mcp/"+ts["name"].(string)+"/" || ts["tools"].(float64) < 3 || ts["loaded"] != false {
			t.Errorf("toolset: %v", ts)
		}
	}
	loaded := mcpJSON(t, s, "load_toolset", map[string]any{"toolset": "inventory_management"})
	if names := loaded["tools"].([]any); !slices.Contains(names, any("get_host_details")) || slices.Contains(names, any("sync_inventory_source")) {
		t.Errorf("loaded tools: %v", names)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Error("no notifications/tools/list_changed after load_toolset")
	}
	tools = mcpTools(t, s)
	if tools["get_host_details"] == nil || tools["list_hosts"] == nil || tools["list_job_templates"] != nil {
		t.Errorf("tools after load_toolset: %d", len(tools))
	}
	for _, x := range mcpJSON(t, s, "list_toolsets", nil)["toolsets"].([]any) {
		ts := x.(map[string]any)
		if (ts["name"] == "inventory_management") != (ts["loaded"] == true) {
			t.Errorf("loaded: %v", ts)
		}
	}
	mcpFails(t, s, "load_toolset", map[string]any{"toolset": "nope"}, "toolset is one of")
	if got := mcpJSON(t, s, "list_hosts", map[string]any{"page_size": 1}); got["count"] == nil {
		t.Errorf("list_hosts after loading: %v", got)
	}
}

// A client signs in with OAuth: the 401 names the protected resource
// metadata, which names the OIDC provider; the provider's token for the
// MCP URL works on the MCP server and not on the rest of the API.
func TestMCPOAuth(t *testing.T) {
	a := adminClient(t)
	idp := newFakeIdP(t)
	configureOIDC(t, a, idp, testMapping)
	setSystem(t, a, "TOWER_URL_BASE", srv.URL)
	username := uniq("oauth")
	idp.mu.Lock()
	idp.claims = map[string]any{"sub": "mcp-" + username, "preferred_username": username, "email": username + "@example.com", "groups": []string{}}
	idp.mu.Unlock()

	endpoint := srv.URL + "/api/mcp/job_management/"
	prm := must(t, a.get("/.well-known/oauth-protected-resource/api/mcp/job_management/"), 200).Body
	if prm["resource"] != endpoint || fmt.Sprint(prm["authorization_servers"]) != "["+idp.srv.URL+"]" || !strings.Contains(fmt.Sprint(prm["scopes_supported"]), "openid") {
		t.Fatalf("protected resource metadata: %v", prm)
	}

	var authorized string
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	handler, err := sdkauth.NewAuthorizationCodeHandler(&sdkauth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: &oauthex.ClientCredentials{ClientID: "awx-client"},
		RedirectURL:         "http://127.0.0.1:1/callback",
		AuthorizationCodeFetcher: func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
			authorized = args.URL
			res, err := noRedirect.Get(args.URL)
			if err != nil {
				return nil, err
			}
			res.Body.Close()
			loc, err := url.Parse(res.Header.Get("Location"))
			if err != nil {
				return nil, err
			}
			return &sdkauth.AuthorizationResult{Code: loc.Query().Get("code"), State: loc.Query().Get("state")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := mcpConnect(t, &mcp.StreamableClientTransport{Endpoint: endpoint, OAuthHandler: handler}, nil)
	if u, _ := url.Parse(authorized); u == nil || u.Query().Get("resource") != endpoint || u.Query().Get("code_challenge_method") != "S256" {
		t.Errorf("the authorization request: %s", authorized)
	}
	if tools := mcpTools(t, s); tools["get_job_summary"] == nil {
		t.Errorf("OAuth session tools: %d", len(tools))
	}
	if got := mcpJSON(t, s, "list_job_templates", map[string]any{"page_size": 1}); got["count"] == nil {
		t.Errorf("list_job_templates over OAuth: %v", got)
	}
	if n := must(t, a.get("users/?username="+username), 200).count(); n != 1 {
		t.Errorf("the OAuth user wasn't provisioned: %d", n)
	}

	claims := map[string]any{"sub": "mcp-" + username, "preferred_username": username}
	call := func(path, tok string) int {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	forMCP := idp.sign(t, claims, srv.URL+"/api/mcp/")
	if code := call("/api/mcp/system_monitoring/", forMCP); code != 200 {
		t.Errorf("a token for the MCP server on another toolset: %d", code)
	}
	if code := call("/api/mcp/", idp.sign(t, claims, "https://elsewhere.example.com/api/mcp/")); code != 401 {
		t.Errorf("a token for another server: %d", code)
	}
	if code := call("/api/mcp/", idp.sign(t, claims, "awx-client")); code != 200 {
		t.Errorf("a token for OIDC_AUDIENCES: %d", code)
	}
	if r := (&client{t: t, token: forMCP}).get("me/"); r.Status != 401 {
		t.Errorf("a token for the MCP server on /api/v2/: %d", r.Status)
	}
}

// The task tools of each toolset that need no job runner; those that do
// are in TestMCPJobTools.
func TestMCPTaskTools(t *testing.T) {
	f := newApprovalFixture(t)
	a := f.a
	setSystem(t, a, "MCP_ALLOW_WRITE_OPERATIONS", true)
	s := mcpSession(t, "/api/mcp/", newToken(t, a, "write"))
	const missing = 999999999

	prompts := mcpJSON(t, s, "get_launch_prompts", map[string]any{"template_type": "job_template", "id": f.jt})
	if prompts["launch"].(map[string]any)["survey_enabled"] != true || prompts["survey"] == nil {
		t.Errorf("get_launch_prompts: %v", prompts)
	}
	mcpFails(t, s, "get_launch_prompts", map[string]any{"template_type": "x", "id": f.jt}, "template_type is")
	mcpFails(t, s, "get_job_summary", map[string]any{"id": missing}, "404")
	mcpFails(t, s, "get_job_output", map[string]any{"id": missing}, "404")
	mcpFails(t, s, "get_job_changes", map[string]any{"id": missing}, "404")
	mcpFails(t, s, "relaunch_job", map[string]any{"id": missing}, "404")
	mcpFails(t, s, "cancel_job", map[string]any{"id": missing}, "404")
	mcpFails(t, s, "approve_workflow_approval", map[string]any{"id": missing}, "404")
	mcpFails(t, s, "deny_workflow_approval", map[string]any{"id": missing}, "404")
	mcpFails(t, s, "launch_workflow_job_template", map[string]any{"id": missing}, "404")
	update := mcpJSON(t, s, "sync_project", map[string]any{"id": f.project})
	if update["project_update"] == nil && update["id"] == nil {
		t.Errorf("sync_project: %v", update)
	}

	host := must(t, a.get(fmt.Sprintf("hosts/?inventory=%d&name=h1", f.inv)), 200).results()[0]["id"]
	details := mcpJSON(t, s, "get_host_details", map[string]any{"id": host})
	if h := details["host"].(map[string]any); h["name"] != "h1" || h["related"] != nil || details["groups"] == nil || details["recent_results"] == nil {
		t.Errorf("get_host_details: %v", details)
	}
	mcpJSON(t, s, "get_host_facts", map[string]any{"id": host})
	mcpJSON(t, s, "get_host_history", map[string]any{"id": host})
	src := must(t, a.post("inventory_sources/", map[string]any{"name": uniq("src"), "inventory": f.inv, "source": "scm", "source_project": f.project, "source_path": "inventory.ini"}), 201).id()
	if got := mcpJSON(t, s, "sync_inventory_source", map[string]any{"id": src}); got["id"] == nil && got["inventory_update"] == nil {
		t.Errorf("sync_inventory_source: %v", got)
	}

	health := mcpJSON(t, s, "get_system_health", nil)
	instances := health["instances"].([]any)
	if len(instances) == 0 || health["instance_groups"] == nil {
		t.Fatalf("get_system_health: %v", health)
	}
	mcpJSON(t, s, "run_health_check", map[string]any{"id": instances[0].(map[string]any)["id"]})
	if got := mcpJSON(t, s, "list_running_jobs", nil); got["count"] == nil {
		t.Errorf("list_running_jobs: %v", got)
	}
	if got := mcpJSON(t, s, "list_recent_failures", map[string]any{"hours": 1}); got["count"] == nil {
		t.Errorf("list_recent_failures: %v", got)
	}
	for _, r := range []string{"totals", "templates", "trends", "hosts", "launches"} {
		mcpJSON(t, s, "get_report", map[string]any{"report": r, "period": "7"})
	}
	mcpFails(t, s, "get_report", map[string]any{"report": "nope"}, "report is one of")
	mcpFails(t, s, "get_report", map[string]any{"report": "totals", "period": "8"}, "The API answered 400")
	nt := must(t, a.post("notification_templates/", map[string]any{"name": uniq("hook"), "organization": f.org, "notification_type": "webhook",
		"notification_configuration": map[string]any{"url": "http://127.0.0.1:1/hook", "http_method": "POST"}}), 201).id()
	if _, err := app.DB.Exec(context.Background(), `INSERT INTO notifications (notification_template_id, status, error, notification_type, subject)
		SELECT $1, 'failed', 'connection refused', 'webhook', 'x' FROM generate_series(1, 3)`, nt); err != nil {
		t.Fatal(err)
	}
	failing := mcpJSON(t, s, "list_failing_notifications", nil)
	found := false
	for _, x := range failing["results"].([]any) {
		n := x.(map[string]any)
		found = found || n["id"] == float64(nt) && n["failing"].(map[string]any)["error"] == "connection refused"
	}
	if !found {
		t.Errorf("list_failing_notifications: %v", failing)
	}

	// A machine credential has no secret store to test: the API says so.
	if text, _ := mcpCall(t, s, "test_credential", map[string]any{"id": f.cred}); text == "" {
		t.Error("test_credential answered nothing")
	}
	bt := mcpJSON(t, s, "backtest_launch_policy", map[string]any{"condition": `launch.limit == ""`, "days": 1})
	if _, ok := bt["examined"]; !ok {
		t.Errorf("backtest_launch_policy: %v", bt)
	}

	cats := mcpJSON(t, s, "get_settings", nil)
	if cats["results"] == nil && len(cats) == 0 {
		t.Errorf("get_settings: %v", cats)
	}
	sys := mcpJSON(t, s, "get_settings", map[string]any{"category": "system"})
	if _, ok := sys["MCP_PAGE_SIZE"]; !ok {
		t.Errorf("get_settings system: %v", sys)
	}
	mcpFails(t, s, "get_settings", map[string]any{"category": "../me"}, "category is a settings category")
	if got := mcpJSON(t, s, "update_settings", map[string]any{"category": "system", "values": map[string]any{"MCP_PAGE_SIZE": 30}}); got["MCP_PAGE_SIZE"] != float64(30) {
		t.Errorf("update_settings: %v", got)
	}
	a.patch("settings/system/", map[string]any{"MCP_PAGE_SIZE": 25})
	if n := must(t, a.get("activity_stream/?object1=setting&via=mcp"), 200).count(); n == 0 {
		t.Error("update_settings isn't marked mcp in the activity stream")
	}
	ee := must(t, a.get("execution_environments/?page_size=1"), 200).results()[0]["id"]
	mcpCall(t, s, "get_execution_environment_contents", map[string]any{"id": ee})
	mcpFails(t, s, "get_collection_details", map[string]any{"namespace": "nope", "name": "nope"}, "No collection nope.nope")
	mcpFails(t, s, "get_collection_details", map[string]any{"id": missing}, "404")
	mcpFails(t, s, "get_collection_details", nil, "Give the collection's id")

	stream := newAlertStream(t, a, f.org)
	rule := must(t, a.post("event_rules/", map[string]any{"name": uniq("mcp"), "organization": f.org, "streams": []int64{stream.id}, "mode": "shadow",
		"filter": `event.type == "alert.firing"`, "unified_job_template": f.jt}), 201).id()
	if got := mcpJSON(t, s, "set_event_rule_mode", map[string]any{"id": rule, "mode": "approval"}); got["mode"] != "approval" {
		t.Errorf("set_event_rule_mode: %v", got)
	}
	if got := mcpJSON(t, s, "backtest_event_rule", map[string]any{"id": rule}); len(got) == 0 {
		t.Errorf("backtest_event_rule: %v", got)
	}

	item := f.item(t, map[string]any{"approval_rules": []any{map[string]any{"name": "Platform", "approvers": "team", "team": f.platform}}})
	rs := mcpSession(t, "/api/mcp/self_service/", newToken(t, f.requester, "write"))
	form := mcpJSON(t, rs, "get_catalog_item_form", map[string]any{"id": item})
	if form["questions"] == nil {
		t.Errorf("get_catalog_item_form: %v", form)
	}
	req := mcpJSON(t, rs, "request_catalog_item", map[string]any{"id": item, "answers": map[string]any{"env": "dev", "size": 2}, "reason": "from an assistant"})
	if req["status"] != "awaiting_approval" {
		t.Fatalf("request_catalog_item: %v", req)
	}
	mcpJSON(t, rs, "get_request_progress", map[string]any{"id": req["id"]})
	ps := mcpSession(t, "/api/mcp/self_service/", newToken(t, f.p1, "write"))
	pending := mcpJSON(t, ps, "list_pending_approvals", nil)
	if pending["count"] != float64(1) {
		t.Fatalf("list_pending_approvals: %v", pending)
	}
	ap := approvalIDs(req)[0]
	if got := mcpJSON(t, ps, "approve_workflow_approval", map[string]any{"id": ap, "comment": "fine"}); got["status"] != "successful" {
		t.Errorf("approve_workflow_approval: %v", got)
	}
	mcpFails(t, ps, "deny_workflow_approval", map[string]any{"id": ap}, "needs a comment")
	mcpFails(t, ps, "deny_workflow_approval", map[string]any{"id": ap, "comment": "too late"}, "already answered")
	mcpFails(t, rs, "list_deployment_actions", map[string]any{"id": missing}, "404")
	mcpFails(t, rs, "run_deployment_action", map[string]any{"id": missing, "action": "x"}, "404")
}

// The job tools on jobs that ran: why a job failed, its output around the
// failure, a relaunch on the failed hosts, an ad hoc command, a cancel.
func TestMCPJobTools(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	setSystem(t, a, "MCP_ALLOW_WRITE_OPERATIONS", true)
	s := mcpSession(t, "/api/mcp/job_management/", newToken(t, a, "write"))
	jt := f.template(t, "fail.yml", nil)
	launched := mcpJSON(t, s, "launch_job_template", map[string]any{"id": jt})
	job := waitJob(t, a, fmt.Sprintf("jobs/%v/", launched["id"]))
	mustStatus(t, f, job, "failed")
	waitFor(t, "the job's events", func() bool { return a.get(fmt.Sprintf("jobs/%d/", job.id())).Body["event_processing_finished"] == true })

	sum := mcpJSON(t, s, "get_job_summary", map[string]any{"id": job.id()})
	failed := sum["failed_tasks"].([]any)
	if len(failed) != 1 {
		t.Fatalf("failed tasks: %v", sum)
	}
	ft := failed[0].(map[string]any)
	if fmt.Sprint(ft["hosts"]) != "[h2]" || ft["message"] != "boom on h2" || ft["task"] == "" {
		t.Errorf("failed task: %v", ft)
	}
	if sum["job"].(map[string]any)["status"] != "failed" {
		t.Errorf("job: %v", sum["job"])
	}
	around := mcpJSON(t, s, "get_job_output", map[string]any{"id": job.id(), "around_failure": true, "context_lines": 2})
	if !strings.Contains(around["content"].(string), "boom on h2") || strings.Contains(around["content"].(string), "\x1b[") {
		t.Errorf("output around the failure: %v", around)
	}
	tail := mcpJSON(t, s, "get_job_output", map[string]any{"id": job.id()})
	if tail["total_lines"].(float64) < 5 || tail["end_line"] != tail["total_lines"] {
		t.Errorf("the output's tail: %v", tail)
	}
	head := mcpJSON(t, s, "get_job_output", map[string]any{"id": job.id(), "start_line": 0, "end_line": 2})
	if strings.Count(head["content"].(string), "\n") != 2 {
		t.Errorf("lines 0 to 2: %q", head["content"])
	}
	mcpJSON(t, s, "get_job_changes", map[string]any{"id": job.id()})

	again := mcpJSON(t, s, "relaunch_job", map[string]any{"id": job.id(), "hosts": "failed"})
	if rj := waitJob(t, a, fmt.Sprintf("jobs/%v/", again["id"])); rj.Body["limit"] != "h2" {
		t.Errorf("relaunch on the failed hosts: limit %v", rj.Body["limit"])
	}
	monitoring := mcpSession(t, "/api/mcp/system_monitoring/", newToken(t, a, "read"))
	fails := mcpJSON(t, monitoring, "list_recent_failures", map[string]any{"hours": 1})
	if fails["count"].(float64) < 1 {
		t.Errorf("list_recent_failures: %v", fails)
	}

	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("local"), "organization": f.org, "credential_type": credType(t, a, "ssh"), "inputs": map[string]any{"username": "me"}}), 201).id()
	adhoc := mcpJSON(t, s, "launch_ad_hoc_command", map[string]any{"inventory": f.inv, "credential": cred, "module_name": "ping", "limit": "h1"})
	if done := waitJob(t, a, fmt.Sprintf("ad_hoc_commands/%v/", adhoc["id"])); done.Body["status"] != "successful" {
		t.Errorf("ad hoc command: %v", done.Body["status"])
	}
	mcpJSON(t, s, "get_job_output", map[string]any{"id": adhoc["id"]})

	slowJT := f.template(t, "slow.yml", nil)
	running := mcpJSON(t, s, "launch_job_template", map[string]any{"id": slowJT})
	waitFor(t, "the slow job to run", func() bool { return a.get(fmt.Sprintf("jobs/%v/", running["id"])).Body["status"] == "running" })
	if got := mcpJSON(t, monitoring, "list_running_jobs", nil); got["count"].(float64) < 1 {
		t.Errorf("list_running_jobs: %v", got)
	}
	mcpJSON(t, s, "cancel_job", map[string]any{"id": running["id"]})
	if c := waitJob(t, a, fmt.Sprintf("jobs/%v/", running["id"])); c.Body["status"] != "canceled" {
		t.Errorf("cancel_job: %v", c.Body["status"])
	}
}
