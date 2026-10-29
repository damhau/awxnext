// Package outputs checks what a job template's playbook publishes with
// set_stats against the outputs the template declares (docs/workflows.md):
// each output has a name, a type and whether it is required. A job whose
// artifacts do not match fails; matching artifacts are converted to their
// declared types, so the workflow nodes after it get typed values.
package outputs

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Types are the output types, in the order the UI offers them.
var Types = []string{"string", "integer", "float", "boolean", "list", "object"}

// Decl is a declared output.
type Decl struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

var nameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Parse reads a declaration list as stored in a template's outputs field.
// An empty object (an empty form value) is an empty list.
func Parse(v any) ([]Decl, error) {
	if m, ok := v.(map[string]any); ok && len(m) == 0 || v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("Expected a list of outputs.")
	}
	out := make([]Decl, 0, len(items))
	seen := map[string]bool{}
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Output %d: expected an object with name, type and required.", i+1)
		}
		for k := range m {
			if k != "name" && k != "type" && k != "required" {
				return nil, fmt.Errorf("Output %d: unknown key %q (expected name, type and required).", i+1, k)
			}
		}
		name, _ := m["name"].(string)
		if !nameRe.MatchString(name) {
			return nil, fmt.Errorf("Output %d: %q is not a valid variable name.", i+1, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("Output %q is declared twice.", name)
		}
		seen[name] = true
		typ, _ := m["type"].(string)
		if !validType(typ) {
			return nil, fmt.Errorf("Output %q: type must be one of %s.", name, strings.Join(Types, ", "))
		}
		req := false
		if r, ok := m["required"]; ok && r != nil {
			if req, ok = r.(bool); !ok {
				return nil, fmt.Errorf("Output %q: required must be true or false.", name)
			}
		}
		out = append(out, Decl{Name: name, Type: typ, Required: req})
	}
	return out, nil
}

// Load reads declarations stored as JSON; invalid ones are ignored, as they
// can only come from outside the API.
func Load(raw []byte) []Decl {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	d, _ := Parse(v)
	return d
}

// Encode gives declarations in their stored form, every key present.
func Encode(decls []Decl) []any {
	out := make([]any, len(decls))
	for i, d := range decls {
		out[i] = map[string]any{"name": d.Name, "type": d.Type, "required": d.Required}
	}
	return out
}

func validType(t string) bool {
	for _, x := range Types {
		if x == t {
			return true
		}
	}
	return false
}

// Find returns the declaration of an output.
func Find(decls []Decl, name string) (Decl, bool) {
	for _, d := range decls {
		if d.Name == name {
			return d, true
		}
	}
	return Decl{}, false
}

// Names lists the declared output names, sorted.
func Names(decls []Decl) []string {
	out := make([]string, len(decls))
	for i, d := range decls {
		out[i] = d.Name
	}
	sort.Strings(out)
	return out
}

// Check checks artifacts against declarations. It returns the artifacts with
// declared outputs converted to their types (outputs that are not declared
// pass unchanged), or an error listing every mismatch.
func Check(decls []Decl, artifacts map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(artifacts))
	for k, v := range artifacts {
		out[k] = v
	}
	var problems []string
	for _, d := range decls {
		v, ok := artifacts[d.Name]
		if !ok || v == nil {
			if d.Required {
				problems = append(problems, fmt.Sprintf("%q is required but was not set", d.Name))
			}
			continue
		}
		cv, ok := Convert(d.Type, v)
		if !ok {
			problems = append(problems, fmt.Sprintf("%q must be %s, not %s", d.Name, Article(d.Type), Describe(v)))
			continue
		}
		out[d.Name] = cv
	}
	if len(problems) > 0 {
		return nil, errors.New("The playbook succeeded but its outputs do not match the template: " + strings.Join(problems, "; ") + ".")
	}
	return out, nil
}

// Convert gives v as type t. Strings convert to numbers and booleans when
// they read as one, and numbers to strings; nothing converts to or from a
// list or an object.
func Convert(t string, v any) (any, bool) {
	switch t {
	case "string":
		switch x := v.(type) {
		case string:
			return x, true
		case bool:
			return nil, false
		}
		if f, ok := number(v); ok {
			return strconv.FormatFloat(f, 'f', -1, 64), true
		}
	case "integer":
		if s, ok := v.(string); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			return n, err == nil
		}
		if f, ok := number(v); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return int64(f), true
		}
	case "float":
		if s, ok := v.(string); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			return f, err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
		}
		if f, ok := number(v); ok {
			return f, true
		}
	case "boolean":
		switch x := v.(type) {
		case bool:
			return x, true
		case string:
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "true":
				return true, true
			case "false":
				return false, true
			}
		}
	case "list":
		if l, ok := v.([]any); ok {
			return l, true
		}
	case "object":
		if m, ok := v.(map[string]any); ok {
			return m, true
		}
	}
	return nil, false
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// Article names a type with its article: "a list", "an integer".
func Article(t string) string {
	if t == "integer" || t == "object" {
		return "an " + t
	}
	return "a " + t
}

// Describe names the type of a value for a message, with short values shown:
// a string ("eu,us"), a list, an integer (3).
func Describe(v any) string {
	switch x := v.(type) {
	case string:
		if len(x) > 40 {
			x = x[:40] + "…"
		}
		return fmt.Sprintf("a string (%q)", x)
	case bool:
		return fmt.Sprintf("a boolean (%v)", x)
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	if f, ok := number(v); ok {
		if f == math.Trunc(f) {
			return fmt.Sprintf("an integer (%s)", strconv.FormatFloat(f, 'f', -1, 64))
		}
		return fmt.Sprintf("a float (%s)", strconv.FormatFloat(f, 'f', -1, 64))
	}
	return fmt.Sprintf("%T", v)
}
