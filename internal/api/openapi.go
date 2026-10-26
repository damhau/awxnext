package api

import (
	"net/http"
	"sort"
	"strings"
	"sync"
)

var (
	openapiOnce sync.Once
	openapiDoc  Obj
)

// fieldSchema maps a field to an OpenAPI schema.
func fieldSchema(f *Field) Obj {
	s := Obj{}
	switch f.Type {
	case TString, TText:
		s["type"] = "string"
		if f.MaxLength > 0 {
			s["maxLength"] = f.MaxLength
		}
	case TSecret:
		s["type"] = "string"
		s["format"] = "password"
	case TInt:
		s["type"] = "integer"
		s["format"] = "int64"
	case TFloat:
		s["type"] = "number"
	case TBool:
		s["type"] = "boolean"
	case TJSON:
		// Any JSON value.
	case TFK:
		s["type"] = "integer"
		s["format"] = "int64"
		if f.Ref != "" {
			s["description"] = "ID of a " + f.Ref + " object."
		}
	case TChoice:
		var enum []any
		allInt := true
		for _, ch := range f.Choices {
			enum = append(enum, ch.Value)
			if _, ok := ch.Value.(int); !ok {
				allInt = false
			}
		}
		if allInt {
			s["type"] = "integer"
		} else {
			s["type"] = "string"
		}
		s["enum"] = enum
	case TDateTime:
		s["type"] = "string"
		s["format"] = "date-time"
	}
	if f.Nullable || f.Type == TFK && !f.Required {
		s["nullable"] = true
	}
	if f.Help != "" {
		if d, ok := s["description"].(string); ok {
			s["description"] = f.Help + " " + d
		} else {
			s["description"] = f.Help
		}
	}
	if f.Default != nil {
		s["default"] = f.Default
	}
	return s
}

func schemaName(res *Resource) string {
	return strings.ReplaceAll(title(res.Type), " ", "")
}

func ref(name string) Obj { return Obj{"$ref": "#/components/schemas/" + name} }

func jsonBody(schema Obj) Obj {
	return Obj{"required": true, "content": Obj{"application/json": Obj{"schema": schema}}}
}

func jsonResp(desc string, schema Obj) Obj {
	r := Obj{"description": desc}
	if schema != nil {
		r["content"] = Obj{"application/json": Obj{"schema": schema}}
	}
	return r
}

var errorResponses = Obj{
	"400": jsonResp("Validation error", ref("Error")),
	"401": jsonResp("Not authenticated", ref("Error")),
	"403": jsonResp("Permission denied", ref("Error")),
	"404": jsonResp("Not found", ref("Error")),
}

func withErrors(ok map[string]Obj) Obj {
	out := Obj{}
	for k, v := range errorResponses {
		out[k] = v
	}
	for k, v := range ok {
		out[k] = v
	}
	return out
}

var listParams = []Obj{
	{"name": "page", "in": "query", "schema": Obj{"type": "integer", "minimum": 1}},
	{"name": "page_size", "in": "query", "schema": Obj{"type": "integer", "minimum": 1, "maximum": maxPageSize}},
	{"name": "search", "in": "query", "description": "Case-insensitive search across name/description and other search fields.", "schema": Obj{"type": "string"}},
	{"name": "order_by", "in": "query", "description": "Comma-separated fields; prefix with - for descending.", "schema": Obj{"type": "string"}},
}

// BuildOpenAPI renders an OpenAPI 3.0 document from the resource registry.
func (a *App) BuildOpenAPI() Obj {
	schemas := Obj{
		"Error": Obj{"type": "object", "additionalProperties": true, "properties": Obj{"detail": Obj{"type": "string"}}},
	}
	paths := Obj{}
	names := append([]string(nil), registryOrder...)
	sort.Strings(names)
	for _, n := range names {
		res := registry[n]
		sn := schemaName(res)
		read := Obj{
			"id":             Obj{"type": "integer", "format": "int64", "readOnly": true},
			"type":           Obj{"type": "string", "readOnly": true},
			"url":            Obj{"type": "string", "readOnly": true},
			"related":        Obj{"type": "object", "readOnly": true, "additionalProperties": true},
			"summary_fields": Obj{"type": "object", "readOnly": true, "additionalProperties": true},
		}
		write := Obj{}
		var required []string
		for _, f := range res.Fields {
			if f.Name == "id" || f.Name == "type" {
				continue
			}
			s := fieldSchema(f)
			if !f.Hidden && !f.WriteOnly {
				rs := Obj{}
				for k, v := range s {
					rs[k] = v
				}
				if f.ReadOnly {
					rs["readOnly"] = true
				}
				read[f.Name] = rs
			}
			if !f.ReadOnly {
				write[f.Name] = s
				if f.Required {
					required = append(required, f.Name)
				}
			}
		}
		schemas[sn] = Obj{"type": "object", "properties": read}
		ws := Obj{"type": "object", "properties": write}
		if len(required) > 0 {
			ws["required"] = required
		}
		schemas[sn+"Write"] = ws
		schemas[sn+"List"] = Obj{"type": "object", "properties": Obj{
			"count":    Obj{"type": "integer"},
			"next":     Obj{"type": "string", "nullable": true},
			"previous": Obj{"type": "string", "nullable": true},
			"results":  Obj{"type": "array", "items": ref(sn)},
		}}

		filterParams := append([]Obj(nil), listParams...)
		for _, f := range res.Fields {
			if f.NoFilter || f.Type == TSecret || f.WriteOnly {
				continue
			}
			filterParams = append(filterParams, Obj{"name": f.Name, "in": "query", "required": false,
				"description": "Exact match. Django-style lookups are supported, e.g. " + f.Name + "__icontains, " + f.Name + "__in, not__" + f.Name + ".",
				"schema":      Obj{"type": "string"}})
		}
		tag := []string{res.Name}
		list := Obj{"get": Obj{"operationId": res.Name + "_list", "tags": tag, "parameters": filterParams,
			"responses": withErrors(map[string]Obj{"200": jsonResp("OK", ref(sn+"List"))})}}
		if !res.ReadOnly && !res.NoCreate {
			list["post"] = Obj{"operationId": res.Name + "_create", "tags": tag, "requestBody": jsonBody(ref(sn + "Write")),
				"responses": withErrors(map[string]Obj{"201": jsonResp("Created", ref(sn))})}
		}
		paths["/api/v2/"+res.Name+"/"] = list

		idParam := Obj{"name": "id", "in": "path", "required": true, "description": "Numeric id or named URL (name, or name++organization).", "schema": Obj{"type": "string"}}
		detail := Obj{
			"parameters": []Obj{idParam},
			"get": Obj{"operationId": res.Name + "_read", "tags": tag,
				"responses": withErrors(map[string]Obj{"200": jsonResp("OK", ref(sn))})},
		}
		if !res.ReadOnly {
			detail["put"] = Obj{"operationId": res.Name + "_update", "tags": tag, "requestBody": jsonBody(ref(sn + "Write")),
				"responses": withErrors(map[string]Obj{"200": jsonResp("OK", ref(sn))})}
			detail["patch"] = Obj{"operationId": res.Name + "_partial_update", "tags": tag, "requestBody": jsonBody(ref(sn + "Write")),
				"responses": withErrors(map[string]Obj{"200": jsonResp("OK", ref(sn))})}
			if !res.NoDelete {
				detail["delete"] = Obj{"operationId": res.Name + "_delete", "tags": tag,
					"responses": withErrors(map[string]Obj{"204": jsonResp("Deleted", nil)})}
			}
		}
		base := "/api/v2/" + res.Name + "/{id}/"
		paths[base] = detail

		for _, s := range res.SubLists {
			target := registry[s.Target]
			if target == nil {
				continue
			}
			tn := schemaName(target)
			op := Obj{
				"parameters": []Obj{idParam},
				"get": Obj{"operationId": res.Name + "_" + s.Name + "_list", "tags": tag, "parameters": listParams,
					"responses": withErrors(map[string]Obj{"200": jsonResp("OK", ref(tn+"List"))})},
			}
			if !s.ReadOnly && (s.Associate != nil || !s.NoCreate) {
				var body Obj
				desc := ""
				switch {
				case s.Associate != nil && !s.NoCreate:
					body = Obj{"oneOf": []Obj{ref("Association"), ref(tn + "Write")}}
					desc = "Associate an existing object ({\"id\": N}), disassociate ({\"id\": N, \"disassociate\": true}) or create a new one."
				case s.Associate != nil:
					body = ref("Association")
					desc = "Associate ({\"id\": N}) or disassociate ({\"id\": N, \"disassociate\": true}) an object."
				default:
					body = ref(tn + "Write")
					desc = "Create a new object related to this one."
				}
				op["post"] = Obj{"operationId": res.Name + "_" + s.Name + "_create", "tags": tag, "description": desc,
					"requestBody": jsonBody(body), "responses": withErrors(map[string]Obj{
						"201": jsonResp("Created", ref(tn)), "204": jsonResp("Associated or disassociated", nil)})}
			}
			paths[base+s.Name+"/"] = op
		}
		for _, act := range res.Actions {
			op := Obj{"parameters": []Obj{idParam}}
			methods := act.Methods
			if len(methods) == 0 {
				methods = []string{"GET", "POST"}
			}
			for _, m := range methods {
				o := Obj{"operationId": res.Name + "_" + act.Name + "_" + strings.ToLower(m), "tags": tag,
					"responses": withErrors(map[string]Obj{"200": jsonResp("OK", Obj{"type": "object", "additionalProperties": true})})}
				if m != "GET" {
					o["requestBody"] = Obj{"content": Obj{"application/json": Obj{"schema": Obj{"type": "object", "additionalProperties": true}}}}
				}
				op[strings.ToLower(m)] = o
			}
			paths[base+act.Name+"/"] = op
		}
		if res.Versioned {
			paths[base+"history/"] = Obj{"parameters": []Obj{idParam}, "get": Obj{"operationId": res.Name + "_history_list", "tags": tag,
				"parameters": listParams, "responses": withErrors(map[string]Obj{"200": jsonResp("OK", ref("ObjectVersionList"))})}}
			vParam := Obj{"name": "version", "in": "path", "required": true, "schema": Obj{"type": "integer"}}
			paths[base+"history/{version}/"] = Obj{"parameters": []Obj{idParam, vParam}, "get": Obj{"operationId": res.Name + "_history_read", "tags": tag,
				"responses": withErrors(map[string]Obj{"200": jsonResp("Snapshot", ref("ObjectVersion"))})}}
			paths[base+"history/{version}/revert/"] = Obj{"parameters": []Obj{idParam, vParam}, "post": Obj{"operationId": res.Name + "_history_revert", "tags": tag,
				"responses": withErrors(map[string]Obj{"200": jsonResp("Reverted", ref(sn))})}}
		}
	}
	schemas["Association"] = Obj{"type": "object", "required": []string{"id"}, "properties": Obj{
		"id": Obj{"type": "integer"}, "disassociate": Obj{"type": "boolean"}}}
	for _, rt := range a.Routes {
		p := rt.Pattern
		m := strings.ToLower(rt.Method)
		item, _ := paths[p].(Obj)
		if item == nil {
			item = Obj{}
			paths[p] = item
		}
		if _, exists := item[m]; exists {
			continue
		}
		op := Obj{"operationId": strings.Trim(strings.NewReplacer("/api/v2/", "", "/", "_", "{", "", "}", "").Replace(p), "_") + "_" + m,
			"responses": withErrors(map[string]Obj{"200": jsonResp("OK", Obj{"type": "object", "additionalProperties": true})})}
		if rt.Public {
			op["security"] = []Obj{}
		}
		var params []Obj
		for _, seg := range strings.Split(p, "/") {
			if strings.HasPrefix(seg, "{") {
				params = append(params, Obj{"name": strings.Trim(seg, "{}"), "in": "path", "required": true, "schema": Obj{"type": "string"}})
			}
		}
		if params != nil {
			op["parameters"] = params
		}
		item[m] = op
	}
	return Obj{
		"openapi": "3.0.3",
		"info": Obj{
			"title":       "AWX Next API",
			"version":     ProductVersion,
			"description": "AWX-compatible REST API (v2). Generated from the server's resource definitions.",
		},
		"servers":  []Obj{{"url": "/"}},
		"security": []Obj{{"basicAuth": []any{}}, {"bearerAuth": []any{}}, {"sessionAuth": []any{}}},
		"paths":    paths,
		"components": Obj{
			"schemas": schemas,
			"securitySchemes": Obj{
				"basicAuth":   Obj{"type": "http", "scheme": "basic"},
				"bearerAuth":  Obj{"type": "http", "scheme": "bearer", "description": "Personal access token or OIDC access token."},
				"sessionAuth": Obj{"type": "apiKey", "in": "cookie", "name": "awx_sessionid", "description": "Unsafe methods also require the X-CSRFToken header."},
			},
		},
	}
}

func (a *App) handleOpenAPI(c *Ctx, w http.ResponseWriter, r *http.Request) {
	openapiOnce.Do(func() { openapiDoc = a.BuildOpenAPI() })
	writeJSON(w, 200, openapiDoc)
}
