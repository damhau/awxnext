// Package mcpserver serves awxnext's tools to assistants over MCP's
// streamable HTTP transport (roadmap M12-03): every tool at /api/mcp/, one
// toolset at /api/mcp/<toolset>/, and /api/mcp/discover/, whose sessions
// load toolsets on demand. Each tool calls the API's own handlers as the
// token's user.
package mcpserver

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/version"
)

// Via marks the activity stream entries of the changes made through the tools.
const Via = "mcp"

// Path is where the MCP server is served.
const Path = "/api/mcp/"

const metadataPrefix = "/.well-known/oauth-protected-resource"

// A discover session that sees no request for this long is closed.
const sessionTimeout = 30 * time.Minute

type Server struct {
	app       *api.App
	api       http.Handler
	sets      []*toolset
	mu        sync.Mutex
	servers   map[string]*mcp.Server
	stateless http.Handler
	discover  http.Handler
}

// Mount serves the MCP server and its OAuth protected resource metadata
// on r, whose /api/v2/ routes the tools call.
func Mount(app *api.App, r chi.Router) *Server {
	s := &Server{app: app, api: r, sets: toolsets(), servers: map[string]*mcp.Server{}}
	// Every request carries a bearer token, which a DNS-rebinding page
	// doesn't have, and the SDK's check would refuse a reverse proxy on the
	// same host that passes the public Host.
	s.stateless = mcp.NewStreamableHTTPHandler(s.serverFor, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	s.discover = mcp.NewStreamableHTTPHandler(s.discoverServer, &mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout, DisableLocalhostProtection: true})
	r.Handle(Path+"*", http.HandlerFunc(s.serve))
	r.Get(metadataPrefix+Path+"*", s.metadata)
	return s
}

// endpoint is the toolset name of an MCP path: "" for every tool,
// "discover", or a toolset's; ok is false for any other path.
func (s *Server) endpoint(path string) (name string, ok bool) {
	name = strings.Trim(strings.TrimPrefix(path, Path), "/")
	if name == "" || name == "discover" || s.set(name) != nil {
		return name, true
	}
	return "", false
}

func (s *Server) set(name string) *toolset {
	for _, t := range s.sets {
		if t.name == name {
			return t
		}
	}
	return nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name, ok := s.endpoint(r.URL.Path)
	if !ok || !s.app.Settings.Bool(ctx, "MCP_ENABLED") {
		api.WriteError(w, api.ErrNotFound)
		return
	}
	base := baseURL(s.app, r)
	r = r.WithContext(auth.WithAudiencePrefix(ctx, base+Path))
	h := s.stateless
	if name == "discover" {
		h = s.discover
	}
	sdkauth.RequireBearerToken(s.verify, &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL:    base + metadataPrefix + resourcePath(name),
		AllowMissingExpiration: true,
	})(h).ServeHTTP(w, r)
}

func resourcePath(name string) string {
	if name == "" {
		return Path
	}
	return Path + name + "/"
}

// verify authenticates a bearer token as the API does: a personal access
// token, or an OIDC provider's JWT.
func (s *Server) verify(ctx context.Context, token string, r *http.Request) (*sdkauth.TokenInfo, error) {
	p, err := api.Authenticate(r, discard{})
	if err != nil || p == nil || p.Anonymous() {
		return nil, sdkauth.ErrInvalidToken
	}
	scope := p.TokenScope
	if scope == "" {
		scope = "write"
	}
	return &sdkauth.TokenInfo{UserID: strconv.FormatInt(p.UserID, 10), Scopes: []string{scope}, Extra: map[string]any{"principal": p}}, nil
}

// metadata is the OAuth protected resource metadata (RFC 9728) of an MCP
// URL: the OIDC provider that issues the bearer tokens awxnext accepts.
func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name, ok := s.endpoint(strings.TrimPrefix(r.URL.Path, metadataPrefix))
	if !ok || !s.app.Settings.Bool(ctx, "MCP_ENABLED") {
		api.WriteError(w, api.ErrNotFound)
		return
	}
	base := baseURL(s.app, r)
	issuer, scopes := auth.BearerIssuer(ctx, s.app)
	prm := &oauthex.ProtectedResourceMetadata{
		Resource:               base + resourcePath(name),
		ResourceName:           "awxnext",
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        scopes,
	}
	if issuer != "" {
		prm.AuthorizationServers = []string{issuer}
	}
	sdkauth.ProtectedResourceMetadataHandler(prm).ServeHTTP(w, r)
}

// baseURL is awxnext's public URL: TOWER_URL_BASE, or the request's.
func baseURL(app *api.App, r *http.Request) string {
	if base := app.Settings.String(r.Context(), "TOWER_URL_BASE"); base != "" {
		return strings.TrimRight(base, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

// serverFor is the server of a stateless request: its toolset's tools, or
// every tool, with the write tools when the token may use them.
func (s *Server) serverFor(r *http.Request) *mcp.Server {
	name, _ := s.endpoint(r.URL.Path)
	sets := s.sets
	if name != "" {
		sets = []*toolset{s.set(name)}
	}
	write := s.canWrite(r.Context(), principal(sdkauth.TokenInfoFromContext(r.Context())))
	key := name + "|" + strconv.FormatBool(write)
	s.mu.Lock()
	defer s.mu.Unlock()
	if srv := s.servers[key]; srv != nil {
		return srv
	}
	srv := s.newServer(instructions(name))
	for _, t := range sets {
		s.addTools(srv, t, write)
	}
	s.servers[key] = srv
	return srv
}

func (s *Server) newServer(instructions string) *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "awxnext", Title: "awxnext", Version: version.Version},
		&mcp.ServerOptions{Instructions: instructions, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}}})
}

func (s *Server) addTools(srv *mcp.Server, t *toolset, write bool) {
	for _, tl := range available(t, write) {
		srv.AddTool(tl.def, s.handler(tl))
	}
}

// canWrite is whether p's tools include those that change anything.
func (s *Server) canWrite(ctx context.Context, p *api.Principal) bool {
	return p != nil && p.TokenScope != "read" && s.app.Settings.Bool(ctx, "MCP_ALLOW_WRITE_OPERATIONS")
}

func principal(ti *sdkauth.TokenInfo) *api.Principal {
	if ti == nil {
		return nil
	}
	p, _ := ti.Extra["principal"].(*api.Principal)
	return p
}

func instructions(name string) string {
	const common = "awxnext is an automation controller compatible with AWX: job templates run Ansible playbooks on inventories' hosts. " +
		"Each tool acts with the rights of the token's user. List tools take the API's filters, such as {\"name__icontains\": \"web\"}, and are paginated. " +
		"Secrets come back as $encrypted$."
	if name == "discover" {
		return common + " This endpoint starts with two tools: list_toolsets says what each toolset is for, and load_toolset adds a toolset's tools."
	}
	return common
}

// discard is the response writer of an authentication that writes nothing
// the client sees.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}
