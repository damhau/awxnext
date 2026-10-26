package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/metrics"
	"github.com/damhau/awxnext/internal/tracing"
	"github.com/go-chi/chi/v5"
)

const (
	defaultPageSize = 25
	maxPageSize     = 200
)

// Router builds the HTTP handler for the API.
func (a *App) Router() chi.Router {
	r := chi.NewRouter()
	r.Use(a.headers)
	r.Use(tracing.Middleware)
	r.Use(instrument)
	r.Use(a.recoverer)
	r.Use(trailingSlash)

	r.Get("/api/", a.wrapPublic(func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, Obj{
			"description":             "AWX REST API",
			"current_version":         "/api/v2/",
			"available_versions":      Obj{"v2": "/api/v2/"},
			"oauth2":                  "/api/o/",
			"custom_logo":             "",
			"custom_login_info":       "",
			"login_redirect_override": "",
		})
	}))
	r.Get("/api/v2/", a.wrap(func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		root := Obj{}
		for _, res := range Resources() {
			root[res.Name] = "/api/v2/" + res.Name + "/"
		}
		for _, rt := range a.Routes {
			p := strings.TrimPrefix(rt.Pattern, "/api/v2/")
			if rt.Method == "GET" && p != rt.Pattern && strings.Count(p, "/") == 1 && !strings.Contains(p, "{") {
				root[strings.TrimSuffix(p, "/")] = rt.Pattern
			}
		}
		writeJSON(w, 200, root)
	}))

	r.Get("/api/v2/openapi.json", a.wrapPublic(a.handleOpenAPI))
	r.Get("/api/v2/schema/", a.wrapPublic(a.handleOpenAPI))

	for _, rt := range a.Routes {
		h := a.wrap(rt.Handler)
		if rt.Public {
			h = a.wrapPublic(rt.Handler)
		}
		r.Method(rt.Method, rt.Pattern, h)
	}

	r.Get("/api/v2/{res}/", a.wrap(a.handleList))
	r.Post("/api/v2/{res}/", a.wrap(a.handleCreate))
	r.Options("/api/v2/{res}/", a.wrap(a.handleOptions))
	r.Get("/api/v2/{res}/{id}/", a.wrap(a.handleDetail))
	r.Put("/api/v2/{res}/{id}/", a.wrap(a.handleUpdate))
	r.Patch("/api/v2/{res}/{id}/", a.wrap(a.handleUpdate))
	r.Delete("/api/v2/{res}/{id}/", a.wrap(a.handleDelete))
	r.Options("/api/v2/{res}/{id}/", a.wrap(a.handleOptions))
	r.HandleFunc("/api/v2/{res}/{id}/{sub}/", a.wrap(a.handleSub))
	a.historyRoutes(r)
	return r
}

func (a *App) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h := w.Header()
		h.Set("X-API-Product-Name", ProductName)
		h.Set("X-API-Product-Version", ProductVersion)
		h.Set("X-API-Node", a.Cfg.NodeName)
		next.ServeHTTP(&timedWriter{ResponseWriter: w, start: start}, r)
	})
}

type timedWriter struct {
	http.ResponseWriter
	start       time.Time
	wroteHeader bool
	status      int
}

var (
	httpRequests = metrics.Default.NewCounter("awxnext_http_requests_total", "HTTP requests by route, method and status code.", "method", "route", "code")
	httpDuration = metrics.Default.NewHistogram("awxnext_http_request_duration_seconds", "HTTP request latency by route.", nil, "method", "route")
)

// instrument records request counts and latencies per route pattern.
func instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
			// Only known resource and sublist names become label values (bounded cardinality).
			if res := registry[rc.URLParam("res")]; res != nil {
				route = strings.Replace(route, "{res}", res.Name, 1)
				if sub := rc.URLParam("sub"); sub != "" && res.knownSub(sub) {
					route = strings.Replace(route, "{sub}", sub, 1)
				}
			}
		}
		code := 200
		if tw, ok := w.(*timedWriter); ok && tw.status != 0 {
			code = tw.status
		}
		httpRequests.With(r.Method, route, strconv.Itoa(code)).Inc()
		httpDuration.With(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

func (t *timedWriter) WriteHeader(code int) {
	if !t.wroteHeader {
		t.wroteHeader = true
		t.status = code
		t.Header().Set("X-API-Time", fmt.Sprintf("%.3fs", time.Since(t.start).Seconds()))
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *timedWriter) Write(b []byte) (int, error) {
	if !t.wroteHeader {
		t.WriteHeader(200)
	}
	return t.ResponseWriter.Write(b)
}

func (t *timedWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *timedWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// Hijack supports websocket upgrades through the wrapper.
func (t *timedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := t.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hj.Hijack()
}

func (a *App) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic", "path", r.URL.Path, "panic", v)
				writeJSON(w, 500, Obj{"detail": "A server error has occurred."})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// trailingSlash makes API paths without a trailing slash behave like AWX (which requires one).
func trailingSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api") && !strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(r.URL.Path, ".json") {
			r.URL.Path += "/"
			if r.URL.RawPath != "" {
				r.URL.RawPath += "/"
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Authenticator resolves the principal of a request; nil principal means anonymous.
type Authenticator func(r *http.Request, w http.ResponseWriter) (*Principal, error)

var authenticate Authenticator = func(r *http.Request, w http.ResponseWriter) (*Principal, error) { return nil, nil }

// SetAuthenticator installs the request authenticator.
func SetAuthenticator(f Authenticator) { authenticate = f }

func (a *App) newCtx(r *http.Request, p *Principal) *Ctx {
	if p == nil {
		p = &Principal{}
	}
	c := &Ctx{Context: r.Context(), App: a, User: p, Req: r, Extra: map[string]any{}}
	// Clients may describe a change; it is stored with the version it creates.
	if comment := strings.TrimSpace(r.Header.Get("X-Change-Comment")); comment != "" {
		if len(comment) > 1000 {
			comment = comment[:1000]
		}
		c.Extra["version_comment"] = comment
	}
	return c
}

func (a *App) wrap(h func(c *Ctx, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := authenticate(r, w)
		if err != nil {
			writeError(w, err)
			return
		}
		if p == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api" authorization_url="/api/login/"`)
			writeError(w, ErrUnauthorized)
			return
		}
		if p.TokenScope == "read" && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			writeError(w, ErrForbidden)
			return
		}
		h(a.newCtx(r, p), w, r)
	}
}

func (a *App) wrapPublic(h func(c *Ctx, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := authenticate(r, w)
		h(a.newCtx(r, p), w, r)
	}
}

func resourceFromPath(r *http.Request) (*Resource, error) {
	res := registry[chi.URLParam(r, "res")]
	if res == nil {
		return nil, ErrNotFound
	}
	return res, nil
}

// ResolveID resolves a numeric id or an AWX named URL ("name" or "name++org").
func ResolveID(c *Ctx, res *Resource, seg string) (int64, error) {
	seg, _ = url.PathUnescape(seg)
	if id, err := strconv.ParseInt(seg, 10, 64); err == nil {
		return id, nil
	}
	nameField := "name"
	if res.field("username") != nil {
		nameField = "username"
	} else if res.field("hostname") != nil {
		nameField = "hostname"
	} else if res.field("name") == nil {
		return 0, ErrNotFound
	}
	parts := strings.Split(seg, "++")
	q := &query{}
	res.baseWhere(q)
	q.where = append(q.where, "t."+nameField+" = "+q.arg(parts[0]))
	if len(parts) > 1 {
		if f := res.orgField(); f != nil {
			if parts[1] == "" {
				q.where = append(q.where, "t."+res.OrgColumn+" IS NULL")
			} else {
				q.where = append(q.where, "t."+res.OrgColumn+" IN (SELECT id FROM organizations WHERE name = "+q.arg(parts[1])+")")
			}
		} else if f := res.field("inventory"); f != nil {
			inv := strings.Join(parts[1:], "++")
			invParts := strings.Split(inv, "++")
			sub := "SELECT i.id FROM inventories i WHERE i.name = " + q.arg(invParts[0])
			if len(invParts) > 1 {
				sub += " AND i.organization_id IN (SELECT id FROM organizations WHERE name = " + q.arg(invParts[1]) + ")"
			}
			q.where = append(q.where, "t.inventory_id IN ("+sub+")")
		}
	}
	rows, err := c.DB().Query(c, "SELECT t.id FROM "+res.Table+" t"+q.whereSQL()+" LIMIT 2", q.args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		return 0, ErrNotFound
	}
	return ids[0], nil
}

func (a *App) objectFromPath(c *Ctx, r *http.Request) (*Resource, Obj, error) {
	res, err := resourceFromPath(r)
	if err != nil {
		return nil, nil, err
	}
	id, err := ResolveID(c, res, chi.URLParam(r, "id"))
	if err != nil {
		return nil, nil, err
	}
	obj, err := GetVisible(c, res, id)
	return res, obj, err
}

func pageParams(params url.Values) (int, int, error) {
	page, pageSize := 1, defaultPageSize
	if s := params.Get("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0, 0, Detail(404, "Invalid page.")
		}
		page = n
	}
	if s := params.Get("page_size"); s != "" {
		n, err := strconv.Atoi(s)
		if err == nil && n > 0 {
			pageSize = min(n, maxPageSize)
		}
	}
	return page, pageSize, nil
}

// WriteList runs a list query and writes an AWX paginated response.
func WriteList(c *Ctx, w http.ResponseWriter, r *http.Request, res *Resource, lp ListParams) {
	params := r.URL.Query()
	page, pageSize, err := pageParams(params)
	if err != nil {
		writeError(w, err)
		return
	}
	lp.Params = params
	lp.Page, lp.PageSize = page, pageSize
	objs, count, err := List(c, res, lp)
	if err != nil {
		writeError(w, err)
		return
	}
	if page > 1 && int64((page-1)*pageSize) >= count {
		writeError(w, Detail(404, "Invalid page."))
		return
	}
	out, err := Serialize(c, res, objs)
	if err != nil {
		writeError(w, err)
		return
	}
	if out == nil {
		out = []Obj{}
	}
	link := func(p int) any {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p))
		return r.URL.Path + "?" + q.Encode()
	}
	var next, prev any
	if int64(page*pageSize) < count {
		next = link(page + 1)
	}
	if page > 1 {
		prev = link(page - 1)
	}
	writeJSON(w, 200, Obj{"count": count, "next": next, "previous": prev, "results": out})
}

func (a *App) handleList(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, err := resourceFromPath(r)
	if err != nil {
		writeError(w, err)
		return
	}
	WriteList(c, w, r, res, ListParams{})
}

// ReadBody decodes the JSON request body.
func ReadBody(r *http.Request) (Obj, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return Obj{}, nil
	}
	return decodeBody(strings.NewReader(string(b)))
}

func (a *App) handleCreate(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, err := resourceFromPath(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if res.ReadOnly || res.NoCreate {
		writeError(w, Detail(405, `Method "POST" not allowed.`))
		return
	}
	data, err := ReadBody(r)
	if err != nil {
		writeError(w, err)
		return
	}
	c.Extra["body"] = data
	obj, err := Create(c, res, data, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := SerializeOne(c, res, obj)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func (a *App) handleDetail(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, obj, err := a.objectFromPath(c, r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := SerializeOne(c, res, obj)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) handleUpdate(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, obj, err := a.objectFromPath(c, r)
	if err != nil {
		writeError(w, err)
		return
	}
	if res.ReadOnly || res.NoUpdate {
		writeError(w, Detail(405, fmt.Sprintf("Method %q not allowed.", r.Method)))
		return
	}
	data, err := ReadBody(r)
	if err != nil {
		writeError(w, err)
		return
	}
	obj, err = Update(c, res, obj, data)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := SerializeOne(c, res, obj)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) handleDelete(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, obj, err := a.objectFromPath(c, r)
	if err != nil {
		writeError(w, err)
		return
	}
	if res.ReadOnly || res.NoDelete {
		writeError(w, Detail(405, `Method "DELETE" not allowed.`))
		return
	}
	if err := Delete(c, res, obj); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (a *App) handleSub(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, obj, err := a.objectFromPath(c, r)
	if err != nil {
		writeError(w, err)
		return
	}
	name := chi.URLParam(r, "sub")
	id, _ := ID(obj["id"])
	for _, act := range res.Actions {
		if act.Name != name {
			continue
		}
		allowed := len(act.Methods) == 0
		for _, m := range act.Methods {
			if m == r.Method {
				allowed = true
			}
		}
		if r.Method == "OPTIONS" && !allowed {
			writeJSON(w, 200, Obj{"name": title(name), "actions": Obj{}})
			return
		}
		if !allowed {
			writeError(w, Detail(405, fmt.Sprintf("Method %q not allowed.", r.Method)))
			return
		}
		if act.Perm != "" {
			ok, err := HasPerm(c, res, obj, act.Perm)
			if err != nil {
				writeError(w, err)
				return
			}
			if !ok {
				writeError(w, ErrForbidden)
				return
			}
		}
		act.Handler(c, w, r, obj)
		return
	}
	for _, s := range res.SubLists {
		if s.Name == name {
			a.handleSubList(c, w, r, res, obj, s)
			return
		}
	}
	switch name {
	case "activity_stream":
		if !res.NoActivity && r.Method == "GET" {
			as := registry["activity_stream"]
			typ := res.Type
			if t, ok := obj["type"].(string); ok && t != "" {
				typ = t
			}
			WriteList(c, w, r, as, ListParams{Extra: []func(ArgFunc) string{func(arg ArgFunc) string {
				return fmt.Sprintf("(t.object1 = %[1]s AND t.object1_id = %[2]s) OR (t.object2 = %[1]s AND t.object2_id = %[2]s)", arg(typ), arg(id))
			}}})
			return
		}
	case "object_roles", "access_list":
		if res.ContentType != "" && r.Method == "GET" && objectRoleHandlers[name] != nil {
			objectRoleHandlers[name](c, w, r, res, obj)
			return
		}
	case "history":
		if res.Versioned && r.Method == "GET" {
			a.handleHistory(c, w, r, res, obj)
			return
		}
	case "copy":
		if res.Copyable {
			a.handleCopy(c, w, r, res, obj)
			return
		}
	}
	writeError(w, ErrNotFound)
}

// objectRoleHandlers are installed by the RBAC API (avoids an import cycle).
var objectRoleHandlers = map[string]func(c *Ctx, w http.ResponseWriter, r *http.Request, res *Resource, obj Obj){}

// SetObjectRoleHandler installs the handler for /object_roles/ or /access_list/.
func SetObjectRoleHandler(name string, h func(c *Ctx, w http.ResponseWriter, r *http.Request, res *Resource, obj Obj)) {
	objectRoleHandlers[name] = h
}

func (a *App) handleSubList(c *Ctx, w http.ResponseWriter, r *http.Request, res *Resource, parent Obj, s *SubList) {
	target := registry[s.Target]
	if target == nil {
		writeError(w, ErrNotFound)
		return
	}
	pid, _ := ID(parent["id"])
	switch r.Method {
	case "GET":
		if s.BeforeList != nil {
			if err := s.BeforeList(c, pid); err != nil {
				writeError(w, err)
				return
			}
		}
		WriteList(c, w, r, target, ListParams{SkipView: s.SkipView, Extra: []func(ArgFunc) string{func(arg ArgFunc) string { return s.Filter(arg, pid) }}})
		return
	case "OPTIONS":
		writeJSON(w, 200, metadata(c, target, true, !s.ReadOnly && (!s.NoCreate || s.Associate != nil)))
		return
	case "POST":
	default:
		writeError(w, Detail(405, fmt.Sprintf("Method %q not allowed.", r.Method)))
		return
	}
	if s.ReadOnly {
		writeError(w, Detail(405, `Method "POST" not allowed.`))
		return
	}
	data, err := ReadBody(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if rawID, ok := data["id"]; ok && s.Associate != nil {
		childID, ok := ID(rawID)
		if !ok {
			writeError(w, FieldError("id", "A valid integer is required."))
			return
		}
		disassociate := false
		if v, ok := data["disassociate"]; ok {
			b, err := coerce(c, &Field{Name: "disassociate", Type: TBool}, v)
			disassociate = err == nil && b == true
		}
		if err := a.attach(c, res, parent, s, target, childID, disassociate); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(204)
		return
	}
	if s.FindExisting != nil && s.Associate != nil {
		if existing, ok, err := s.FindExisting(c, parent, data); err != nil {
			writeError(w, err)
			return
		} else if ok {
			if err := a.attach(c, res, parent, s, target, existing, false); err != nil {
				writeError(w, err)
				return
			}
			w.WriteHeader(204)
			return
		}
	}
	if s.NoCreate {
		writeError(w, FieldError("id", "This field is required."))
		return
	}
	if s.CreateDefaults != nil {
		for k, v := range s.CreateDefaults(c, parent) {
			if _, given := data[k]; !given {
				data[k] = v
			}
		}
	}
	var preset Obj
	if s.ParentField != "" {
		preset = Obj{s.ParentField: pid}
		// Creating through the parent counts as permission on the parent.
		data[s.ParentField] = pid
	}
	c.Extra["body"] = data
	obj, err := Create(c, target, data, preset)
	if err != nil {
		writeError(w, err)
		return
	}
	if s.ParentField == "" && s.Associate != nil {
		cid, _ := ID(obj["id"])
		if err := a.attach(c, res, parent, s, target, cid, false); err != nil {
			writeError(w, err)
			return
		}
	}
	out, err := SerializeOne(c, target, obj)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func (a *App) attach(c *Ctx, res *Resource, parent Obj, s *SubList, target *Resource, childID int64, disassociate bool) error {
	if err := checkManaged(c, res, parent); err != nil {
		return err
	}
	perm := s.AttachPerm
	if perm == "" {
		perm = "change"
	}
	if perm != "none" {
		ok, err := HasPerm(c, res, parent, perm)
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	child, err := Get(c, target, childID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return FieldError("id", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", childID))
		}
		return err
	}
	// CanAttach validates adding (types, cycles, use permission on the child);
	// removing only needs change permission on the parent, checked above.
	if s.CanAttach != nil && !disassociate {
		if err := s.CanAttach(c, parent, childID); err != nil {
			return err
		}
	}
	pid, _ := ID(parent["id"])
	return c.WithTx(func(c *Ctx) error {
		op := "associate"
		if disassociate {
			op = "disassociate"
			if s.Disassociate == nil {
				return BadRequest("Disassociation not allowed.")
			}
			if err := s.Disassociate(c, pid, childID); err != nil {
				return err
			}
		} else if err := s.Associate(c, pid, childID); err != nil {
			return translateDBError(target, err)
		}
		if err := Activity(c, op, res, parent, Obj{}, target, child, res.Type+"."+s.Name); err != nil {
			return err
		}
		return RecordVersion(c, res, pid, "update", Obj{s.Name: Obj{op: childID}})
	})
}

// handleCopy implements /copy/ for copyable resources.
func (a *App) handleCopy(c *Ctx, w http.ResponseWriter, r *http.Request, res *Resource, obj Obj) {
	switch r.Method {
	case "GET":
		ok, err := HasPerm(c, res, obj, "change")
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, Obj{"can_copy": ok, "can_copy_without_user_input": ok,
			"templates_unable_to_copy": []any{}, "credentials_unable_to_copy": []any{}, "inventories_unable_to_copy": []any{}})
	case "POST":
		data, err := ReadBody(r)
		if err != nil {
			writeError(w, err)
			return
		}
		name, _ := data["name"].(string)
		if strings.TrimSpace(name) == "" {
			writeError(w, FieldError("name", "This field is required."))
			return
		}
		copyData := Obj{}
		for _, f := range res.Fields {
			if f.ReadOnly || f.Type == TSecret {
				continue
			}
			v := obj[f.Name]
			if t, ok := v.(time.Time); ok {
				v = FormatTime(t)
			}
			copyData[f.Name] = v
		}
		copyData["name"] = name
		var created Obj
		err = c.WithTx(func(c *Ctx) error {
			vals, err := Validate(c, res, copyData, true, nil)
			if err != nil {
				return err
			}
			// Secrets are copied as stored (already encrypted).
			for _, f := range res.Fields {
				if f.Type == TSecret {
					vals[f.Name] = obj[f.Name]
				}
			}
			created, err = Insert(c, res, vals)
			if err != nil {
				return err
			}
			if res.AfterCopy != nil {
				return res.AfterCopy(c, obj, created)
			}
			return nil
		})
		if err != nil {
			writeError(w, err)
			return
		}
		out, err := SerializeOne(c, res, created)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, out)
	default:
		writeError(w, Detail(405, fmt.Sprintf("Method %q not allowed.", r.Method)))
	}
}

func (a *App) handleOptions(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, err := resourceFromPath(r)
	if err != nil {
		writeError(w, err)
		return
	}
	isList := chi.URLParam(r, "id") == ""
	writable := !res.ReadOnly && !(isList && res.NoCreate) && !(!isList && res.NoUpdate)
	writeJSON(w, 200, metadata(c, res, isList, writable))
}

// metadata renders DRF-style OPTIONS metadata used by awxkit / the awx CLI.
func metadata(c *Ctx, res *Resource, isList, writable bool) Obj {
	get := Obj{
		"id":             Obj{"type": "integer", "label": "ID", "help_text": "Database ID for this " + strings.ToLower(res.Verbose) + ".", "filterable": true},
		"type":           Obj{"type": "choice", "label": "Type", "help_text": "Data type for this " + strings.ToLower(res.Verbose) + ".", "choices": [][]any{{res.Type, res.Verbose}}, "filterable": false},
		"url":            Obj{"type": "string", "label": "Url", "help_text": "URL for this " + strings.ToLower(res.Verbose) + ".", "filterable": false},
		"related":        Obj{"type": "object", "label": "Related", "help_text": "Data structure with URLs of related resources.", "filterable": false},
		"summary_fields": Obj{"type": "object", "label": "Summary fields", "help_text": "Data structure with name/description for related resources.", "filterable": false},
	}
	write := Obj{}
	for _, f := range res.Fields {
		if f.Name == "id" || f.Name == "type" {
			continue
		}
		m := Obj{"type": f.Type.metadataType(), "label": f.Label, "help_text": f.Help, "filterable": !f.NoFilter && f.Type != TSecret}
		choices := f.Choices
		if f.ChoicesFunc != nil {
			choices = f.ChoicesFunc(c)
		}
		if len(choices) > 0 {
			ch := make([][]any, len(choices))
			for i, x := range choices {
				ch[i] = []any{x.Value, x.Label}
			}
			m["choices"] = ch
		}
		if !f.Hidden && !f.WriteOnly {
			get[f.Name] = m
		}
		if !f.ReadOnly {
			wm := Obj{}
			for k, v := range m {
				wm[k] = v
			}
			wm["required"] = f.Required
			if f.Default != nil {
				wm["default"] = f.Default
			}
			if f.MaxLength > 0 {
				wm["max_length"] = f.MaxLength
			}
			write[f.Name] = wm
		}
	}
	actions := Obj{"GET": get}
	if writable && len(write) > 0 {
		if isList {
			actions["POST"] = write
		} else {
			actions["PUT"] = write
		}
	}
	var search []string
	for _, f := range res.SearchFields {
		search = append(search, f)
	}
	if len(search) == 0 && !res.NoName {
		search = []string{"name", "description"}
	}
	var related []string
	for _, f := range res.Fields {
		if f.Type == TFK {
			related = append(related, f.Name)
		}
	}
	for n := range res.Relations {
		related = append(related, n)
	}
	sort.Strings(related)
	name := res.Verbose
	if isList {
		name += " List"
	}
	return Obj{
		"name":                  name,
		"description":           "",
		"renders":               []string{"application/json", "text/html"},
		"parses":                []string{"application/json"},
		"actions":               actions,
		"types":                 []string{res.Type},
		"search_fields":         search,
		"related_search_fields": related,
		"max_page_size":         maxPageSize,
	}
}

// knownSub reports whether name is a sublist, action or built-in sub-path of res.
func (res *Resource) knownSub(name string) bool {
	for _, s := range res.SubLists {
		if s.Name == name {
			return true
		}
	}
	for _, a := range res.Actions {
		if a.Name == name {
			return true
		}
	}
	switch name {
	case "activity_stream", "object_roles", "access_list", "history", "copy", "revert":
		return true
	}
	return false
}
