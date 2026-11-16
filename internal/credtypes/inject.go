package credtypes

import "strings"

// Vars are the template variables of a credential's injectors. As in AWX,
// unset text fields stay undefined (they render empty, and default()
// applies), unset booleans are false, and private keys end with a newline.
func Vars(s *Schema, inputs map[string]any) map[string]any {
	vars := map[string]any{}
	if s == nil {
		return vars
	}
	for _, f := range s.Fields {
		if v, ok := inputs[f.ID]; ok {
			vars[f.ID] = v
		} else if f.Default != nil {
			vars[f.ID] = f.Default
		} else if f.Type == "boolean" {
			vars[f.ID] = false
		}
		if v, ok := vars[f.ID].(string); ok && f.Format == "ssh_private_key" && v != "" && !strings.HasSuffix(v, "\n") {
			vars[f.ID] = v + "\n"
		}
	}
	return vars
}

// RenderAny renders the strings of an extra_vars injector, keys included,
// as AWX does.
func RenderAny(v any, vars map[string]any, files map[string]string) (any, error) {
	switch x := v.(type) {
	case string:
		return Render(x, vars, files)
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			key, err := Render(k, vars, files)
			if err != nil {
				return nil, err
			}
			r, err := RenderAny(e, vars, files)
			if err != nil {
				return nil, err
			}
			out[key] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := RenderAny(e, vars, files)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}
