// Package ws implements AWX's /websocket/ endpoint: clients subscribe to
// groups such as "jobs-status_changed" or "job_events-42" and receive JSON
// messages as jobs change state and produce output.
package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/metrics"
)

// eventResources maps event group names to the job resource whose visibility gates them.
var eventResources = map[string]string{
	"job_events":              "jobs",
	"project_update_events":   "project_updates",
	"inventory_update_events": "inventory_updates",
	"ad_hoc_command_events":   "ad_hoc_commands",
	"system_job_events":       "system_jobs",
	"workflow_events":         "workflow_jobs",
}

var wsConnections = metrics.Default.NewGauge("awxnext_websocket_connections", "Open /websocket/ connections on this node.")

// Install registers the websocket endpoint. hub returns the node's hub once jobs are started.
func Install(app *api.App, hub func() *jobs.Hub) {
	h := &handler{app: app, hub: hub}
	app.HandlePublic("GET", "/websocket/", h.serve)
}

type handler struct {
	app *api.App
	hub func() *jobs.Hub
}

type conn struct {
	c       *api.Ctx
	ws      *websocket.Conn
	hub     *jobs.Hub
	ch      chan []byte
	xrf     string // expected xrftoken; "" when the client authenticated with a header
	mu      sync.Mutex
	groups  map[string]bool
	visible map[int64]bool
}

func (h *handler) serve(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	if c.User.Anonymous() {
		api.WriteError(w, api.ErrUnauthorized)
		return
	}
	hub := h.hub()
	if hub == nil {
		api.WriteError(w, api.Detail(503, "Job events are not available on this node."))
		return
	}
	opts := &websocket.AcceptOptions{}
	if u, err := url.Parse(h.app.Cfg.BaseURL); err == nil && u.Host != "" {
		opts.OriginPatterns = []string{u.Host}
	}
	if h.app.Cfg.DevMode {
		// UI dev servers on other ports; session clients still prove access with the CSRF token.
		opts.OriginPatterns = append(opts.OriginPatterns, "localhost:*", "127.0.0.1:*")
	}
	ws, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	cn := &conn{c: c, ws: ws, hub: hub, ch: make(chan []byte, 512), groups: map[string]bool{}, visible: map[int64]bool{}}
	if r.Header.Get("Authorization") == "" {
		// Session clients prove same-origin access by echoing the CSRF cookie.
		cn.xrf = "-"
		if ck, err := r.Cookie(auth.CSRFCookie); err == nil && ck.Value != "" {
			cn.xrf = ck.Value
		}
	}
	wsConnections.With().Add(1)
	defer wsConnections.With().Add(-1)
	defer hub.Unsubscribe(cn.ch)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c.Context = ctx
	go cn.read(ctx, cancel)
	cn.write(ctx)
	ws.Close(websocket.StatusNormalClosure, "")
}

func (cn *conn) send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return cn.ws.Write(wctx, websocket.MessageText, b)
}

func (cn *conn) write(ctx context.Context) {
	if err := cn.send(ctx, map[string]any{"accept": true, "user": cn.c.User.UserID}); err != nil {
		return
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := cn.ws.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case msg := <-cn.ch:
			if !cn.allowed(msg) {
				continue
			}
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := cn.ws.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// allowed filters job status messages to jobs the user can see.
func (cn *conn) allowed(msg []byte) bool {
	u := cn.c.User
	if u.Superuser || u.Auditor {
		return true
	}
	var m struct {
		Group string `json:"group_name"`
		Job   int64  `json:"unified_job_id"`
	}
	if json.Unmarshal(msg, &m) != nil || m.Group != "jobs" || m.Job == 0 {
		return true
	}
	cn.mu.Lock()
	ok, cached := cn.visible[m.Job]
	cn.mu.Unlock()
	if !cached {
		ok, _ = api.CanView(cn.c, api.Lookup("unified_jobs"), m.Job)
		cn.mu.Lock()
		if len(cn.visible) > 10000 {
			cn.visible = map[int64]bool{}
		}
		cn.visible[m.Job] = ok
		cn.mu.Unlock()
	}
	return ok
}

func (cn *conn) read(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	for {
		var data map[string]any
		if err := wsjson.Read(ctx, cn.ws, &data); err != nil {
			return
		}
		if cn.xrf != "" {
			if tok, _ := data["xrftoken"].(string); tok == "" || tok != cn.xrf {
				cn.send(ctx, map[string]any{"error": "access denied to channel"})
				continue
			}
		}
		groups, ok := data["groups"].(map[string]any)
		if !ok {
			continue
		}
		if err := cn.subscribe(ctx, groups); err != nil {
			return
		}
	}
}

// subscribe replaces the connection's groups, checking access to each.
func (cn *conn) subscribe(ctx context.Context, groups map[string]any) error {
	next := map[string]bool{}
	for name, v := range groups {
		list, ok := v.([]any)
		if !ok {
			return cn.send(ctx, map[string]any{"error": "access denied to channel"})
		}
		for _, item := range list {
			oid := fmt.Sprint(item)
			if f, ok := item.(float64); ok {
				oid = fmt.Sprint(int64(f))
			}
			if err := cn.authorize(name, item); err != "" {
				if serr := cn.send(ctx, map[string]any{"error": err}); serr != nil {
					return serr
				}
				continue
			}
			next[name+"-"+oid] = true
		}
	}
	var joined, left, current []string
	cn.mu.Lock()
	for g := range cn.groups {
		if !next[g] {
			left = append(left, g)
			cn.hub.UnsubscribeGroup(g, cn.ch)
		}
	}
	for g := range next {
		current = append(current, g)
		if !cn.groups[g] {
			joined = append(joined, g)
			cn.hub.Subscribe(g, cn.ch)
		}
	}
	cn.groups = next
	cn.mu.Unlock()
	sort.Strings(joined)
	sort.Strings(left)
	sort.Strings(current)
	return cn.send(ctx, map[string]any{"groups_current": nonNil(current), "groups_left": nonNil(left), "groups_joined": nonNil(joined)})
}

// authorize returns an error message when the user may not join group-oid.
func (cn *conn) authorize(group string, oid any) string {
	switch group {
	case "jobs":
		if s := fmt.Sprint(oid); s == "status_changed" || s == "summary" {
			return ""
		}
	case "schedules":
		if fmt.Sprint(oid) == "changed" {
			return ""
		}
	case "control":
		if fmt.Sprint(oid) == fmt.Sprintf("limit_reached_%d", cn.c.User.UserID) {
			return ""
		}
	default:
		resName, ok := eventResources[group]
		res := api.Lookup(resName)
		f, isNum := oid.(float64)
		if ok && res != nil && isNum {
			if visible, _ := api.CanView(cn.c, res, int64(f)); visible {
				return ""
			}
		}
	}
	return fmt.Sprintf("access denied to channel %s for resource id %v", group, oid)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
