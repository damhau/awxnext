package credtypes

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Render evaluates the Jinja subset used by credential injectors:
//
//	{{ name }}                    an input field (missing values render as "")
//	{{ tower.filename }}          path of the unnamed file injector
//	{{ tower.filename.NAME }}     path of a named file injector (awx.filename.* is an alias)
//	{{ name | default('x') }}     fallback for empty values
//	{{ name | tojson }}           JSON-encode the value
//	{{ name | lower }} / upper / trim
func Render(tmpl string, vars map[string]any, files map[string]string) (string, error) {
	var outErr error
	out := exprRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		inner := strings.TrimSpace(m[2 : len(m)-2])
		parts := strings.Split(inner, "|")
		name := strings.TrimSpace(parts[0])
		var v any
		switch {
		case name == "tower.filename" || name == "awx.filename":
			v = files[""]
		case strings.HasPrefix(name, "tower.filename.") || strings.HasPrefix(name, "awx.filename."):
			v = files[name[strings.LastIndex(name, ".")+1:]]
		default:
			v = vars[name]
		}
		for _, raw := range parts[1:] {
			filter := strings.TrimSpace(raw)
			switch {
			case filter == "tojson":
				b, err := json.Marshal(v)
				if err != nil {
					outErr = err
				}
				v = rawJSON(b)
			case strings.HasPrefix(filter, "default(") && strings.HasSuffix(filter, ")"):
				if isEmpty(v) {
					v = strings.Trim(strings.TrimSpace(filter[8:len(filter)-1]), `'"`)
				}
			case filter == "lower":
				v = strings.ToLower(toString(v))
			case filter == "upper":
				v = strings.ToUpper(toString(v))
			case filter == "trim":
				v = strings.TrimSpace(toString(v))
			default:
				outErr = fmt.Errorf("unsupported filter %q", filter)
			}
		}
		return toString(v)
	})
	return out, outErr
}

var exprRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

type rawJSON []byte

func isEmpty(v any) bool {
	return v == nil || v == ""
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case rawJSON:
		return string(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	return fmt.Sprint(v)
}
