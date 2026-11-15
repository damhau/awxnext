package rules

import (
	"math"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
)

// A shape is what the events seen so far say a value is: a string, a
// number, an object with these fields, a list of these items. Values that
// disagree between events, or that no event had, are dynamic.
type shape struct {
	kind   string
	fields map[string]*shape
	item   *shape
}

const (
	kindDyn    = "dyn"
	kindNull   = "null"
	kindString = "string"
	kindInt    = "int"
	// kindNumber is a number with a fraction. It is dynamic to the check:
	// an event's 1.5 may be another's 2, which compares with 2 as an
	// integer does.
	kindNumber    = "number"
	kindBool      = "bool"
	kindTimestamp = "timestamp"
	kindObject    = "object"
	kindList      = "list"
	// kindMap is an object whose values all have one type: labels, headers.
	kindMap = "map"
)

func shapeOf(v any) *shape {
	switch x := v.(type) {
	case nil:
		return &shape{kind: kindNull}
	case string:
		return &shape{kind: kindString}
	case bool:
		return &shape{kind: kindBool}
	case int64, int:
		return &shape{kind: kindInt}
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return &shape{kind: kindInt}
		}
		return &shape{kind: kindNumber}
	case time.Time:
		return &shape{kind: kindTimestamp}
	case map[string]any:
		s := &shape{kind: kindObject, fields: map[string]*shape{}}
		for k, e := range x {
			s.fields[k] = shapeOf(e)
		}
		return s
	case []any:
		s := &shape{kind: kindList}
		for _, e := range x {
			s.item = merge(s.item, shapeOf(e))
		}
		return s
	}
	return &shape{kind: kindDyn}
}

// merge combines what two events say of the same value.
func merge(a, b *shape) *shape {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.kind == kindNull:
		return b
	case b.kind == kindNull:
		return a
	case a.kind == kindInt && b.kind == kindNumber, a.kind == kindNumber && b.kind == kindInt:
		return &shape{kind: kindNumber}
	case a.kind != b.kind:
		return &shape{kind: kindDyn}
	}
	switch a.kind {
	case kindObject:
		out := &shape{kind: kindObject, fields: map[string]*shape{}}
		for k, v := range a.fields {
			out.fields[k] = v
		}
		for k, v := range b.fields {
			out.fields[k] = merge(out.fields[k], v)
		}
		return out
	case kindList:
		return &shape{kind: kindList, item: merge(a.item, b.item)}
	}
	return a
}

// Schema is the shape of a stream's events: the CloudEvent attributes,
// and data as the stream's recent events had it.
type Schema struct {
	event *shape
	// Events is how many events the schema was inferred from.
	Events int
}

// envelope is a CloudEvent's attributes, which every event has.
func envelope() *shape {
	return &shape{kind: kindObject, fields: map[string]*shape{
		"id":              {kind: kindString},
		"source":          {kind: kindString},
		"type":            {kind: kindString},
		"specversion":     {kind: kindString},
		"subject":         {kind: kindString},
		"time":            {kind: kindTimestamp},
		"datacontenttype": {kind: kindString},
		"dataschema":      {kind: kindString},
		"data":            {kind: kindDyn},
	}}
}

// Known shapes of data, by stream type, for a stream that has no events yet.
var knownData = map[string]func() *shape{
	"alertmanager": alertData,
	"grafana":      alertData,
}

func alertData() *shape {
	text := func() *shape { return &shape{kind: kindString} }
	labels := func() *shape { return &shape{kind: kindMap, item: text()} }
	return &shape{kind: kindObject, fields: map[string]*shape{
		"status": text(), "labels": labels(), "annotations": labels(), "startsAt": text(), "endsAt": text(),
		"generatorURL": text(), "fingerprint": text(), "receiver": text(), "groupKey": text(),
		"groupLabels": labels(), "commonLabels": labels(), "commonAnnotations": labels(), "externalURL": text(),
	}}
}

// Infer builds the schema of events, given as Activation makes them. With
// no events, data has the shape the stream type's format gives it, or is
// dynamic.
func Infer(streamTypes []string, events []map[string]any) *Schema {
	s := &Schema{event: envelope(), Events: len(events)}
	var data *shape
	for _, ev := range events {
		for k, v := range ev {
			if k == "data" {
				data = merge(data, shapeOf(v))
			} else if _, core := s.event.fields[k]; !core {
				s.event.fields[k] = merge(s.event.fields[k], shapeOf(v))
			}
		}
	}
	if data == nil && len(streamTypes) == 1 {
		if known, ok := knownData[streamTypes[0]]; ok {
			data = known()
		}
	}
	if data != nil && data.kind != kindNull {
		s.event.fields["data"] = data
	}
	return s
}

// JSON describes the schema for the API: each value's type, with an
// object's fields and a list's items.
func (s *Schema) JSON() map[string]any {
	return map[string]any{"events": s.Events, "event": describe(s.event)}
}

func describe(s *shape) map[string]any {
	if s == nil {
		return map[string]any{"type": kindDyn}
	}
	kind := s.kind
	if kind == kindNull {
		kind = kindDyn
	}
	out := map[string]any{"type": kind}
	if s.fields != nil {
		f := map[string]any{}
		for k, v := range s.fields {
			f[k] = describe(v)
		}
		out["fields"] = f
	}
	if s.item != nil {
		key := "items"
		if s.kind == kindMap {
			key = "values"
		}
		out[key] = describe(s.item)
	}
	return out
}

// typer turns shapes into CEL types: objects become struct types whose
// unknown fields are dynamic, so an expression may name fields no event
// had yet; objects whose values all have one primitive type become maps.
type typer struct {
	*types.Registry
	objects map[string]*shape
	names   []string
}

func newTyper() *typer {
	reg, err := types.NewRegistry()
	if err != nil {
		panic(err)
	}
	return &typer{Registry: reg, objects: map[string]*shape{}}
}

// The names of struct types can't be written as identifiers, so the
// checker never takes event.data for a type name.
func objectName(path string) string { return "object(" + path + ")" }

func (t *typer) celType(s *shape, path string) *types.Type {
	if s == nil {
		return types.DynType
	}
	switch s.kind {
	case kindString:
		return types.StringType
	case kindBool:
		return types.BoolType
	case kindInt:
		return types.IntType
	case kindTimestamp:
		return types.TimestampType
	case kindList:
		return types.NewListType(t.celType(s.item, path+"[]"))
	case kindMap:
		return types.NewMapType(types.StringType, t.celType(s.item, path+"[]"))
	case kindObject:
		if v := uniform(s); v != nil {
			return types.NewMapType(types.StringType, t.celType(v, path+"[]"))
		}
		name := objectName(path)
		if _, ok := t.objects[name]; !ok {
			t.objects[name] = s
			t.names = append(t.names, name)
		}
		return types.NewObjectType(name)
	}
	return types.DynType
}

// uniform is the shape of an object's values when they all have the same
// primitive one.
func uniform(s *shape) *shape {
	var kind string
	for _, f := range s.fields {
		switch f.kind {
		case kindString, kindBool, kindInt:
		default:
			return nil
		}
		if kind != "" && f.kind != kind {
			return nil
		}
		kind = f.kind
	}
	if kind == "" {
		return nil
	}
	return &shape{kind: kind}
}

func (t *typer) path(name string) string {
	return strings.TrimSuffix(strings.TrimPrefix(name, "object("), ")")
}

func (t *typer) FindStructType(name string) (*types.Type, bool) {
	if _, ok := t.objects[name]; ok {
		return types.NewTypeTypeWithParam(types.NewObjectType(name)), true
	}
	return t.Registry.FindStructType(name)
}

func (t *typer) FindStructFieldNames(name string) ([]string, bool) {
	s, ok := t.objects[name]
	if !ok {
		return t.Registry.FindStructFieldNames(name)
	}
	names := make([]string, 0, len(s.fields))
	for k := range s.fields {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, true
}

func (t *typer) FindStructFieldType(name, field string) (*types.FieldType, bool) {
	s, ok := t.objects[name]
	if !ok {
		return t.Registry.FindStructFieldType(name, field)
	}
	f, known := s.fields[field]
	if !known {
		return &types.FieldType{Type: types.DynType}, true
	}
	return &types.FieldType{Type: t.celType(f, t.path(name)+"."+field)}, true
}

// options are the declarations of a schema's environment: event and
// events typed by the schema, and, for each object type, the indexing and
// membership a map has (event.data["a-b"], "a" in event.data), dynamic.
func (t *typer) options(s *Schema) []cel.EnvOption {
	ev := t.celType(s.event, "event")
	// Field types are made as the checker asks for them; walk the schema
	// once so that every object type is known before declaring overloads.
	t.walk(s.event, "event")
	opts := []cel.EnvOption{
		cel.CustomTypeProvider(t),
		cel.Variable("event", ev),
		cel.Variable("events", types.NewListType(ev)),
	}
	for _, name := range t.names {
		obj := types.NewObjectType(name)
		id := strings.NewReplacer("(", "_", ")", "_", ".", "_", "[", "_", "]", "_").Replace(name)
		opts = append(opts,
			cel.Function("_[_]", cel.Overload("index_"+id, []*cel.Type{obj, cel.StringType}, cel.DynType)),
			cel.Function("@in", cel.Overload("in_"+id, []*cel.Type{cel.StringType, obj}, cel.BoolType)),
			cel.Function("size", cel.Overload("size_"+id, []*cel.Type{obj}, cel.IntType)),
		)
	}
	return opts
}

func (t *typer) walk(s *shape, path string) {
	if s == nil {
		return
	}
	t.celType(s, path)
	for k, f := range s.fields {
		t.walk(f, path+"."+k)
	}
	if s.item != nil {
		t.walk(s.item, path+"[]")
	}
}
