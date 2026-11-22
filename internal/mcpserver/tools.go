package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/damhau/awxnext/internal/api"
)

type tool struct {
	def *mcp.Tool
	// write tools change something, or need a POST that a read-only token
	// may not make: they are listed only when writes are allowed.
	write bool
	run   func(c *call, args map[string]any) (any, error)
}

type toolset struct {
	name        string
	description string
	tools       []*tool
}

func (s *Server) handler(t *tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p := principal(req.Extra.TokenInfo)
		if p == nil {
			return nil, errors.New("the request has no user")
		}
		if t.write && !s.canWrite(ctx, p) {
			return failure("This tool makes changes: MCP_ALLOW_WRITE_OPERATIONS is off, or the token is read-only."), nil
		}
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 && string(req.Params.Arguments) != "null" {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return failure("The arguments are not a JSON object."), nil
			}
		}
		pageSize := s.app.Settings.Int(ctx, "MCP_PAGE_SIZE")
		if pageSize < 1 || pageSize > maxPageSize {
			pageSize = 25
		}
		c := &call{s: s, ctx: api.WithVia(api.WithPrincipal(ctx, p), Via), pageSize: pageSize}
		out, err := t.run(c, args)
		if err != nil {
			return failure(err.Error()), nil
		}
		text, ok := out.(string)
		if !ok {
			b, err := json.Marshal(trim(out))
			if err != nil {
				return nil, err
			}
			text = string(b)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
}

func failure(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// trim leaves out the related URLs of objects: the tools don't follow them,
// and they would fill the model's context.
func trim(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if rel, ok := x["related"].(map[string]any); ok && relatedURLs(rel) {
			delete(x, "related")
		}
		for _, e := range x {
			trim(e)
		}
	case []any:
		for _, e := range x {
			trim(e)
		}
	case []map[string]any:
		for _, e := range x {
			trim(e)
		}
	}
	return v
}

func relatedURLs(m map[string]any) bool {
	for _, v := range m {
		if s, ok := v.(string); !ok || !strings.HasPrefix(s, "/api/") {
			return false
		}
	}
	return true
}

const (
	maxPageSize = 200
	// The largest answer of the API a tool returns, and the most of an
	// error's body it repeats.
	maxBody      = 4 << 20
	maxErrorBody = 4000
)

// call is one tool call: requests to the API as the token's user.
type call struct {
	s        *Server
	ctx      context.Context
	pageSize int
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("The API answered %d: %s", e.status, e.body) }

func notFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == http.StatusNotFound
}

// do serves a request to /api/v2/<path> through the API's own handlers.
func (c *call) do(method, path string, q url.Values, body any) (any, error) {
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := "/api/v2/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	// The tool's context may come from the MCP request's, whose chi routing
	// state the router would reuse for this request.
	ctx := context.WithValue(c.ctx, chi.RouteCtxKey, nil)
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &recorder{header: http.Header{}}
	c.s.api.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if rec.over {
		return nil, fmt.Errorf("The answer is over %d MiB: narrow the filters, or ask for fewer items or lines.", maxBody>>20)
	}
	raw := rec.body.Bytes()
	if rec.status >= 400 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > maxErrorBody {
			msg = msg[:maxErrorBody] + "…"
		}
		return nil, &apiError{status: rec.status, body: msg}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{"status": rec.status}, nil
	}
	if strings.HasPrefix(rec.header.Get("Content-Type"), "application/json") {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return string(raw), nil
}

func (c *call) get(path string, q url.Values) (any, error) { return c.do("GET", path, q, nil) }

func (c *call) getObj(path string, q url.Values) (map[string]any, error) {
	v, err := c.get(path, q)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	if m == nil {
		return nil, fmt.Errorf("unexpected answer from %s", path)
	}
	return m, nil
}

// list is a page of path's objects.
func (c *call) list(path string, q url.Values) ([]map[string]any, int, error) {
	m, err := c.getObj(path, q)
	if err != nil {
		return nil, 0, err
	}
	out := []map[string]any{}
	for _, x := range anyList(m["results"]) {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	n, _ := api.ID(m["count"])
	return out, int(n), nil
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	over   bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	if r.body.Len()+len(b) > maxBody {
		r.over = true
		return len(b), nil
	}
	return r.body.Write(b)
}

func (r *recorder) Flush() {}

// resourceTools are the generated tools of a resource: list and get, and
// create, update and delete unless the API refuses them.
func resourceTools(res *api.Resource) []*tool {
	out := []*tool{listTool(res), getTool(res)}
	if res.ReadOnly {
		return out
	}
	if !res.NoCreate {
		out = append(out, createTool(res))
	}
	if !res.NoUpdate {
		out = append(out, updateTool(res))
	}
	if !res.NoDelete {
		out = append(out, deleteTool(res))
	}
	return out
}

func verbose(res *api.Resource) string {
	if res.Verbose != "" {
		return strings.ToLower(res.Verbose)
	}
	return strings.ReplaceAll(res.Type, "_", " ")
}

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)}
}

func changes(title string, destructive, idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: ptr(destructive), IdempotentHint: idempotent, OpenWorldHint: ptr(false)}
}

func ptr[T any](v T) *T { return &v }

func listTool(res *api.Resource) *tool {
	var names []string
	for _, f := range res.Fields {
		if !f.NoFilter && !f.WriteOnly && f.Type != api.TSecret && !f.Virtual {
			names = append(names, f.Name)
		}
	}
	desc := fmt.Sprintf("List the %ss the user may see, a page at a time: count is how many there are. "+
		"filters takes the API's field lookups: {\"name__icontains\": \"web\", \"id__in\": [1, 2]}, and related fields through __, such as organization__name. "+
		"Fields: id, %s.", verbose(res), strings.Join(names, ", "))
	schema := object(nil,
		prop{"search", str("Words that the names and descriptions contain.")},
		prop{"filters", &jsonschema.Schema{Type: "object", Description: "Field lookups and their values, ANDed."}},
		prop{"order_by", str("A field to order by, - first for descending: -id is the latest first. Several are separated by commas.")},
		prop{"page", integer("The page, from 1.")},
		prop{"page_size", integer(fmt.Sprintf("Items per page, up to %d.", maxPageSize))},
	)
	name := res.Name
	return &tool{
		def: &mcp.Tool{Name: "list_" + res.Name, Description: desc, InputSchema: schema, Annotations: readOnly("List " + verbose(res) + "s")},
		run: func(c *call, args map[string]any) (any, error) {
			q, err := listQuery(c, args)
			if err != nil {
				return nil, err
			}
			return c.get(name+"/", q)
		},
	}
}

// listQuery is the query of a list tool's arguments.
func listQuery(c *call, args map[string]any) (url.Values, error) {
	q := url.Values{}
	if f, ok := args["filters"]; ok && f != nil {
		m, ok := f.(map[string]any)
		if !ok {
			return nil, errors.New("filters is an object of field lookups and values.")
		}
		for k, v := range m {
			q.Set(k, queryValue(v))
		}
	}
	if s := strArg(args, "search"); s != "" {
		q.Set("search", s)
	}
	if s := strArg(args, "order_by"); s != "" {
		q.Set("order_by", s)
	}
	page := c.pageSize
	if n, ok := api.ID(args["page_size"]); ok {
		if n < 1 || n > maxPageSize {
			return nil, fmt.Errorf("page_size goes from 1 to %d.", maxPageSize)
		}
		page = int(n)
	}
	q.Set("page_size", strconv.Itoa(page))
	if n, ok := api.ID(args["page"]); ok && n > 1 {
		q.Set("page", strconv.FormatInt(n, 10))
	}
	return q, nil
}

func queryValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = queryValue(e)
		}
		return strings.Join(parts, ",")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func getTool(res *api.Resource) *tool {
	name := res.Name
	return &tool{
		def: &mcp.Tool{Name: "get_" + res.Type, Description: fmt.Sprintf("Get a %s by id, with the names of the objects it refers to in summary_fields.", verbose(res)),
			InputSchema: object([]string{"id"}, prop{"id", integer("The " + verbose(res) + "'s id.")}), Annotations: readOnly("Get a " + verbose(res))},
		run: func(c *call, args map[string]any) (any, error) {
			id, err := idArg(args, "id")
			if err != nil {
				return nil, err
			}
			return c.get(fmt.Sprintf("%s/%d/", name, id), nil)
		},
	}
}

func createTool(res *api.Resource) *tool {
	props, required := fieldProps(res, false)
	name := res.Name
	return &tool{
		write: true,
		def: &mcp.Tool{Name: "create_" + res.Type, Description: fmt.Sprintf("Create a %s. Fields left out take their defaults.", verbose(res)),
			InputSchema: &jsonschema.Schema{Type: "object", Properties: props, Required: required}, Annotations: changes("Create a "+verbose(res), false, false)},
		run: func(c *call, args map[string]any) (any, error) {
			return c.do("POST", name+"/", nil, args)
		},
	}
}

func updateTool(res *api.Resource) *tool {
	props, _ := fieldProps(res, true)
	props["id"] = integer("The " + verbose(res) + "'s id.")
	name := res.Name
	return &tool{
		write: true,
		def: &mcp.Tool{Name: "update_" + res.Type, Description: fmt.Sprintf("Change fields of a %s; the others keep their values.", verbose(res)),
			InputSchema: &jsonschema.Schema{Type: "object", Properties: props, Required: []string{"id"}}, Annotations: changes("Update a "+verbose(res), true, true)},
		run: func(c *call, args map[string]any) (any, error) {
			id, err := idArg(args, "id")
			if err != nil {
				return nil, err
			}
			delete(args, "id")
			if len(args) == 0 {
				return nil, errors.New("Give the fields to change.")
			}
			return c.do("PATCH", fmt.Sprintf("%s/%d/", name, id), nil, args)
		},
	}
}

func deleteTool(res *api.Resource) *tool {
	name := res.Name
	return &tool{
		write: true,
		def: &mcp.Tool{Name: "delete_" + res.Type, Description: fmt.Sprintf("Delete a %s.", verbose(res)),
			InputSchema: object([]string{"id"}, prop{"id", integer("The " + verbose(res) + "'s id.")}), Annotations: changes("Delete a "+verbose(res), true, true)},
		run: func(c *call, args map[string]any) (any, error) {
			id, err := idArg(args, "id")
			if err != nil {
				return nil, err
			}
			if _, err := c.do("DELETE", fmt.Sprintf("%s/%d/", name, id), nil, nil); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": id}, nil
		},
	}
}

// fieldProps are the input schemas of the fields a create (or update)
// sets, and those a create needs.
func fieldProps(res *api.Resource, update bool) (map[string]*jsonschema.Schema, []string) {
	props := map[string]*jsonschema.Schema{}
	var required []string
	for _, f := range res.Fields {
		if f.ReadOnly || (update && f.CreateOnly) {
			continue
		}
		props[f.Name] = fieldSchema(f)
		if !update && f.Required && f.Default == nil {
			required = append(required, f.Name)
		}
	}
	sort.Strings(required)
	return props, required
}

func fieldSchema(f *api.Field) *jsonschema.Schema {
	desc := f.Help
	if desc == "" {
		desc = f.Label
	}
	s := &jsonschema.Schema{Description: desc}
	switch f.Type {
	case api.TString, api.TText:
		s.Type = "string"
		if f.MaxLength > 0 {
			s.MaxLength = ptr(f.MaxLength)
		}
	case api.TSecret:
		s.Type = "string"
		s.WriteOnly = true
		s.Description = strings.TrimSpace(desc + " A secret: reads give $encrypted$.")
	case api.TInt:
		s.Type = "integer"
	case api.TFloat:
		s.Type = "number"
	case api.TBool:
		s.Type = "boolean"
	case api.TFK:
		s.Type = "integer"
		if ref := api.Lookup(f.Ref); ref != nil {
			s.Description = strings.TrimSpace(desc + " The id of a " + verbose(ref) + ".")
		}
	case api.TChoice:
		if len(f.Choices) > 0 {
			for _, ch := range f.Choices {
				s.Enum = append(s.Enum, ch.Value)
			}
			if f.Nullable {
				s.Enum = append(s.Enum, nil)
			}
		} else {
			s.Type = "string"
		}
	case api.TDateTime:
		s.Type = "string"
		s.Format = "date-time"
	}
	if f.Nullable && s.Type != "" {
		s.Types = []string{s.Type, "null"}
		s.Type = ""
	}
	return s
}

type prop struct {
	name   string
	schema *jsonschema.Schema
}

func object(required []string, props ...prop) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}, Required: required}
	for _, p := range props {
		s.Properties[p.name] = p.schema
	}
	return s
}

func str(desc string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: desc}
}
func integer(desc string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "integer", Description: desc}
}
func boolean(desc string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "boolean", Description: desc}
}
func anyValue(desc string) *jsonschema.Schema {
	return &jsonschema.Schema{Description: desc}
}

func enum(desc string, values ...string) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "string", Description: desc}
	for _, v := range values {
		s.Enum = append(s.Enum, v)
	}
	return s
}

func idArg(args map[string]any, name string) (int64, error) {
	n, ok := api.ID(args[name])
	if !ok || n < 1 {
		return 0, fmt.Errorf("%s is the id of an object: a whole number.", name)
	}
	return n, nil
}

func strArg(args map[string]any, name string) string {
	s, _ := args[name].(string)
	return strings.TrimSpace(s)
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
