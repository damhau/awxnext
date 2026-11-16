package resources

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
)

// EDA's API (roadmap M14-10): /api/eda/v1/, the endpoints the ansible.eda
// collection calls, as a thin layer over awxnext's own objects. Each
// endpoint maps EDA's fields, filters and pagination onto a resource and
// back; RBAC, validation and side effects are the resource's.
const edaRoot = "/api/eda/v1/"

// edaReceiverRoot is where EDA's senders post a stream's events: awxnext
// takes them there too, as at ReceiverPath.
const edaReceiverRoot = edaRoot + "external_event_stream/"

const (
	edaDefaultPageSize = 20
	edaMaxPageSize     = 200
)

// edaSpec is one of EDA's collections over an awxnext resource.
type edaSpec struct {
	path string
	res  string
	// filters maps EDA's query parameters to awxnext's lookups.
	filters map[string]string
	// where restricts the objects EDA shows, as an extra predicate.
	where func(c *Ctx, arg ArgFunc) string
	// names maps awxnext's field names to EDA's in validation errors.
	names map[string]string
	out   func(c *Ctx, o Obj) (Obj, error)
	in    func(c *Ctx, body, old Obj) (Obj, error)
	// query rewrites EDA's query before the filters read it.
	query func(q url.Values)
	// readOnly: GET only.
	readOnly bool
	// remove deletes an object, when the resource's delete isn't enough.
	remove func(c *Ctx, obj Obj) error
	// created runs after an object is created, in its transaction.
	created func(c *Ctx, obj Obj) error
	// listFields, when set, are the only fields a list shows, as EDA's
	// list serializers show fewer than its detail ones.
	listFields []string
}

func installEDA(app *api.App) {
	app.HandleAnonymous("GET", "/api/eda/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, Obj{"current_version": strings.TrimRight(c.App.Cfg.BaseURL, "/") + edaRoot, "available_versions": Obj{"v1": edaRoot}})
	})
	app.Handle("GET", edaRoot+"config/", edaConfig)
	app.HandleAnonymous("POST", edaReceiverRoot+"{uuid}/post/", receiveEvents)
	for _, s := range edaSpecs() {
		s.install(app)
	}
	installEDATokens(app)
	installEDACredentialActions(app)
	installEDAProjectActions(app)
	installEDARulebookRoutes(app)
	installEDAActivations(app)
}

func edaSpecs() []*edaSpec {
	return []*edaSpec{
		edaOrganizations(), edaTeams(), edaUsers(),
		edaRoleDefinitions(), edaRoleAssignments("role_team_assignments", "team"), edaRoleAssignments("role_user_assignments", "user"),
		edaCredentialTypes(), edaCredentials(), edaInputSources(),
		edaDecisionEnvironments(), edaProjects(), edaEventStreams(), edaRulebooks(),
	}
}

func (s *edaSpec) install(app *api.App) {
	base := edaRoot + s.path + "/"
	app.Handle("GET", base, s.list)
	app.Handle("GET", base+"{id}/", s.detail)
	if s.readOnly {
		return
	}
	app.Handle("POST", base, s.create)
	app.Handle("PATCH", base+"{id}/", s.update)
	app.Handle("DELETE", base+"{id}/", s.delete)
}

// edaConfig is EDA's config/: the collection's modules read it to tell
// EDA 2.5 and later, which have organizations, from 2.4.
func edaConfig(c *Ctx, w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, 200, Obj{"time_zone": "UTC", "version": api.ProductVersion, "deployment_type": "awxnext", "server_name": c.App.Cfg.NodeName})
}

// edaPage reads EDA's page and page_size.
func edaPage(q url.Values) (int, int, error) {
	page, size := 1, edaDefaultPageSize
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, 0, api.Detail(404, "Invalid page.")
		}
		page = n
	}
	if v := q.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			size = min(n, edaMaxPageSize)
		}
	}
	return page, size, nil
}

// writeEDAPage writes a page as EDA's paginator does.
func writeEDAPage(w http.ResponseWriter, r *http.Request, count int64, page, size int, results []Obj) {
	link := func(p int) any {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p))
		q.Set("page_size", strconv.Itoa(size))
		return r.URL.Path + "?" + q.Encode()
	}
	var next, prev any
	if int64(page*size) < count {
		next = link(page + 1)
	}
	if page > 1 {
		prev = link(page - 1)
	}
	if results == nil {
		results = []Obj{}
	}
	api.WriteJSON(w, 200, Obj{"count": count, "next": next, "previous": prev, "page_size": size, "page": page, "results": results})
}

// writeEDAError writes err with awxnext's field names replaced by EDA's.
func writeEDAError(w http.ResponseWriter, err error, names map[string]string) {
	var ae *api.Error
	if errors.As(err, &ae) {
		if body, ok := ae.Body.(Obj); ok && len(names) > 0 {
			out := Obj{}
			for k, v := range body {
				if n, ok := names[k]; ok {
					k = n
				}
				out[k] = v
			}
			err = &api.Error{Status: ae.Status, Body: out}
		}
	}
	api.WriteError(w, err)
}

func edaID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		return 0, api.Detail(404, "Not found.")
	}
	return id, nil
}

func (s *edaSpec) params(q url.Values) url.Values {
	params := url.Values{}
	for k, lookup := range s.filters {
		if v := q.Get(k); v != "" {
			params.Set(lookup, v)
		}
	}
	return params
}

func (s *edaSpec) list(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res := api.Lookup(s.res)
	page, size, err := edaPage(r.URL.Query())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	q := r.URL.Query()
	if s.query != nil {
		s.query(q)
	}
	lp := api.ListParams{Params: s.params(q), Page: page, PageSize: size, DefaultOrder: "t.id"}
	if s.where != nil {
		lp.Extra = append(lp.Extra, func(arg ArgFunc) string { return s.where(c, arg) })
	}
	objs, count, err := api.List(c, res, lp)
	if err != nil {
		writeEDAError(w, err, s.names)
		return
	}
	results, err := s.render(c, objs)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if s.listFields != nil {
		for i, o := range results {
			results[i] = Obj{}
			for _, k := range s.listFields {
				results[i][k] = o[k]
			}
		}
	}
	writeEDAPage(w, r, count, page, size, results)
}

func (s *edaSpec) render(c *Ctx, objs []Obj) ([]Obj, error) {
	res := api.Lookup(s.res)
	ser, err := api.Serialize(c, res, objs)
	if err != nil {
		return nil, err
	}
	out := make([]Obj, 0, len(ser))
	for _, o := range ser {
		e, err := s.out(c, o)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *edaSpec) renderOne(c *Ctx, obj Obj) (Obj, error) {
	out, err := s.render(c, []Obj{obj})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// object is the visible object of the request's id: 404 when it doesn't
// exist or isn't one of the endpoint's, 403 when it can't be seen.
func (s *edaSpec) object(c *Ctx, r *http.Request) (Obj, error) {
	id, err := edaID(r)
	if err != nil {
		return nil, err
	}
	res := api.Lookup(s.res)
	if s.where != nil {
		lp := api.ListParams{Params: url.Values{}, SkipView: true, Page: 1, PageSize: 1, NoCount: true,
			Extra: []func(arg ArgFunc) string{func(arg ArgFunc) string { return s.where(c, arg) + " AND t.id = " + arg(id) }}}
		objs, _, err := api.List(c, res, lp)
		if err != nil {
			return nil, err
		}
		if len(objs) == 0 {
			return nil, api.Detail(404, "Not found.")
		}
	}
	obj, err := api.GetVisible(c, res, id)
	if errors.Is(err, api.ErrNotFound) {
		return nil, api.Detail(404, "Not found.")
	}
	return obj, err
}

func (s *edaSpec) detail(c *Ctx, w http.ResponseWriter, r *http.Request) {
	obj, err := s.object(c, r)
	if err == nil {
		obj, err = s.renderOne(c, obj)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, obj)
}

func (s *edaSpec) create(c *Ctx, w http.ResponseWriter, r *http.Request) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	res := api.Lookup(s.res)
	var out Obj
	err = c.WithTx(func(c *Ctx) error {
		data, err := s.in(c, body, nil)
		if err != nil {
			return err
		}
		c.Extra["body"] = data
		obj, err := api.Create(c, res, data, nil)
		if err != nil {
			return err
		}
		if s.created != nil {
			if err := s.created(c, obj); err != nil {
				return err
			}
		}
		out, err = s.renderOne(c, obj)
		return err
	})
	if err != nil {
		writeEDAError(w, err, s.names)
		return
	}
	api.WriteJSON(w, 201, out)
}

func (s *edaSpec) update(c *Ctx, w http.ResponseWriter, r *http.Request) {
	old, err := s.object(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	res := api.Lookup(s.res)
	var out Obj
	err = c.WithTx(func(c *Ctx) error {
		data, err := s.in(c, body, old)
		if err != nil {
			return err
		}
		obj, err := api.Update(c, res, old, data)
		if err != nil {
			return err
		}
		out, err = s.renderOne(c, obj)
		return err
	})
	if err != nil {
		writeEDAError(w, err, s.names)
		return
	}
	api.WriteJSON(w, 200, out)
}

func (s *edaSpec) delete(c *Ctx, w http.ResponseWriter, r *http.Request) {
	obj, err := s.object(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	err = c.WithTx(func(c *Ctx) error {
		if s.remove != nil {
			return s.remove(c, obj)
		}
		return api.Delete(c, api.Lookup(s.res), obj)
	})
	if err != nil {
		writeEDAError(w, err, s.names)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// edaCopy copies the fields EDA sends: those of keys, renamed by
// names (EDA's name: awxnext's).
func edaCopy(body Obj, names map[string]string) Obj {
	data := Obj{}
	for k, v := range body {
		if n, ok := names[k]; ok {
			data[n] = v
		}
	}
	return data
}

func edaInvert(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[v] = k
	}
	return out
}

func edaUser(o Obj, key string) any {
	sf, _ := o["summary_fields"].(Obj)
	if u, ok := sf[key].(Obj); ok {
		return Obj{"id": u["id"], "username": u["username"], "first_name": u["first_name"], "last_name": u["last_name"]}
	}
	return nil
}

// edaCommon are the fields EDA gives each object.
func edaCommon(o Obj, out Obj) Obj {
	out["id"] = o["id"]
	out["created_at"] = o["created"]
	out["modified_at"] = o["modified"]
	out["created_by"] = edaUser(o, "created_by")
	out["modified_by"] = edaUser(o, "modified_by")
	return out
}

func edaOrgRef(c *Ctx, id any) (any, error) {
	oid, ok := api.ID(id)
	if !ok {
		return nil, nil
	}
	var name, desc string
	if err := c.DB().QueryRow(c, "SELECT name, description FROM organizations WHERE id = $1", oid).Scan(&name, &desc); err != nil {
		return nil, err
	}
	return Obj{"id": oid, "name": name, "description": desc}, nil
}

func edaOrganizations() *edaSpec {
	names := map[string]string{"name": "name", "description": "description"}
	return &edaSpec{
		path: "organizations", res: "organizations",
		filters: map[string]string{"name": "name__istartswith"},
		names:   edaInvert(names),
		out: func(c *Ctx, o Obj) (Obj, error) {
			return edaCommon(o, Obj{"name": o["name"], "description": o["description"], "managed": false}), nil
		},
		in: func(c *Ctx, body, old Obj) (Obj, error) { return edaCopy(body, names), nil },
	}
}

func edaTeams() *edaSpec {
	names := map[string]string{"name": "name", "description": "description", "organization_id": "organization"}
	return &edaSpec{
		path: "teams", res: "teams",
		filters: map[string]string{"name": "name__istartswith", "organization_id": "organization"},
		names:   edaInvert(names),
		out: func(c *Ctx, o Obj) (Obj, error) {
			return edaCommon(o, Obj{"name": o["name"], "description": o["description"], "organization_id": o["organization"]}), nil
		},
		in: func(c *Ctx, body, old Obj) (Obj, error) { return edaCopy(body, names), nil },
	}
}

func edaUsers() *edaSpec {
	names := map[string]string{"username": "username", "first_name": "first_name", "last_name": "last_name", "email": "email",
		"password": "password", "is_superuser": "is_superuser"}
	return &edaSpec{
		path: "users", res: "users",
		filters: map[string]string{"username": "username", "is_superuser": "is_superuser"},
		names:   edaInvert(names),
		out: func(c *Ctx, o Obj) (Obj, error) {
			return edaCommon(o, Obj{"username": o["username"], "first_name": o["first_name"], "last_name": o["last_name"],
				"email": o["email"], "is_superuser": o["is_superuser"], "resource": Obj{}}), nil
		},
		in:         func(c *Ctx, body, old Obj) (Obj, error) { return edaCopy(body, names), nil },
		listFields: []string{"id", "username", "first_name", "last_name", "is_superuser", "resource"},
	}
}

// edaRoleNames are EDA's names of awxnext's managed roles, where they
// differ.
var edaRoleNames = map[string]string{"Organization Auditor": "Organization Audit"}

func edaRoleDefinitions() *edaSpec {
	return &edaSpec{
		path: "role_definitions", res: "role_definitions", readOnly: true,
		filters: map[string]string{"name": "name", "content_type": "content_type", "managed": "managed"},
		query: func(q url.Values) {
			if own, ok := edaRoleNames[q.Get("name")]; ok {
				q.Set("name", own)
			}
		},
		out: func(c *Ctx, o Obj) (Obj, error) {
			out := Obj{}
			for k, v := range o {
				if k != "related" && k != "summary_fields" && k != "url" {
					out[k] = v
				}
			}
			for eda, own := range edaRoleNames {
				if o["name"] == own {
					out["name"] = eda
				}
			}
			out["created_at"], out["modified_at"] = o["created"], o["modified"]
			return out, nil
		},
	}
}

func edaRoleAssignments(path, actor string) *edaSpec {
	fields := []string{"role_definition", actor, actor + "_ansible_id", "object_id", "object_ansible_id"}
	filters := map[string]string{"content_type": "content_type"}
	names := map[string]string{}
	for _, f := range fields {
		filters[f] = f
		names[f] = f
	}
	return &edaSpec{
		path: path, res: path, filters: filters,
		out: func(c *Ctx, o Obj) (Obj, error) {
			out := Obj{}
			for k, v := range o {
				if k != "related" && k != "summary_fields" && k != "url" {
					out[k] = v
				}
			}
			out["created_at"] = o["created"]
			return out, nil
		},
		in: func(c *Ctx, body, old Obj) (Obj, error) {
			data := edaCopy(body, names)
			if v, ok := data["object_id"]; ok && v != nil {
				data["object_id"] = fmt.Sprint(v)
			}
			return data, nil
		},
	}
}

// installEDATokens serves users/me/awx-tokens/: EDA's controller tokens,
// which ansible-rulebook uses to launch templates. awxnext launches as
// each rule's owner, so they are stored, encrypted, and not used.
func installEDATokens(app *api.App) {
	base := edaRoot + "users/me/awx-tokens/"
	app.Handle("GET", base, edaTokenList)
	app.Handle("POST", base, edaTokenCreate)
	app.Handle("GET", base+"{id}/", edaTokenDetail)
	app.Handle("DELETE", base+"{id}/", edaTokenDelete)
	app.Handle("GET", edaRoot+"users/me/", edaMe)
}

func edaMe(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res := api.Lookup("users")
	obj, err := api.Get(c, res, c.User.UserID)
	if err == nil {
		obj, err = edaUsers().renderOne(c, obj)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, obj)
}

const edaTokenCols = "id, name, description, user_id, created, modified"

func scanEDAToken(row interface{ Scan(...any) error }) (Obj, error) {
	var id, user int64
	var name, desc string
	var created, modified any
	if err := row.Scan(&id, &name, &desc, &user, &created, &modified); err != nil {
		return nil, err
	}
	return Obj{"id": id, "name": name, "description": desc, "user_id": user, "created_at": created, "modified_at": modified}, nil
}

func edaTokenList(c *Ctx, w http.ResponseWriter, r *http.Request) {
	page, size, err := edaPage(r.URL.Query())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	where, args := " WHERE user_id = $1", []any{c.User.UserID}
	if name := r.URL.Query().Get("name"); name != "" {
		where, args = where+" AND name = $2", append(args, name)
	}
	var count int64
	if err := c.DB().QueryRow(c, "SELECT count(*) FROM eda_controller_tokens"+where, args...).Scan(&count); err != nil {
		api.WriteError(w, err)
		return
	}
	rows, err := c.DB().Query(c, fmt.Sprintf("SELECT %s FROM eda_controller_tokens%s ORDER BY id LIMIT %d OFFSET %d", edaTokenCols, where, size, (page-1)*size), args...)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	defer rows.Close()
	var out []Obj
	for rows.Next() {
		t, err := scanEDAToken(rows)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		api.WriteError(w, err)
		return
	}
	writeEDAPage(w, r, count, page, size, out)
}

func edaTokenCreate(c *Ctx, w http.ResponseWriter, r *http.Request) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	fe := api.FieldErrors{}
	name, token := strings.TrimSpace(str(body["name"])), str(body["token"])
	if name == "" {
		fe.Add("name", "This field is required.")
	}
	if token == "" {
		fe.Add("token", "This field is required.")
	}
	if err := fe.Err(); err != nil {
		api.WriteError(w, err)
		return
	}
	enc, err := c.App.Crypto.Encrypt(token)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	var taken bool
	if err := c.DB().QueryRow(c, "SELECT EXISTS (SELECT 1 FROM eda_controller_tokens WHERE user_id = $1 AND name = $2)", c.User.UserID, name).Scan(&taken); err != nil {
		api.WriteError(w, err)
		return
	}
	if taken {
		api.WriteError(w, api.FieldError("name", "Token with this name already exists."))
		return
	}
	t, err := scanEDAToken(c.DB().QueryRow(c, `INSERT INTO eda_controller_tokens (user_id, name, description, token) VALUES ($1, $2, $3, $4)
		RETURNING `+edaTokenCols, c.User.UserID, name, str(body["description"]), enc))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 201, t)
}

func edaTokenDetail(c *Ctx, w http.ResponseWriter, r *http.Request) {
	id, err := edaID(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	t, err := scanEDAToken(c.DB().QueryRow(c, "SELECT "+edaTokenCols+" FROM eda_controller_tokens WHERE id = $1 AND user_id = $2", id, c.User.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		err = api.Detail(404, "Not found.")
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, t)
}

func edaTokenDelete(c *Ctx, w http.ResponseWriter, r *http.Request) {
	id, err := edaID(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	tag, err := c.DB().Exec(c, "DELETE FROM eda_controller_tokens WHERE id = $1 AND user_id = $2", id, c.User.UserID)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		api.WriteError(w, api.Detail(404, "Not found."))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
