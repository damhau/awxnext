// Package vars parses Ansible variable documents (YAML or JSON).
package vars

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse reads a YAML or JSON mapping. Empty input is an empty map.
// Ansible Vault values ("!vault |" in YAML) become {"__ansible_vault": "..."},
// the JSON form ansible-inventory and ansible-playbook understand.
func Parse(text string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" || strings.TrimSpace(text) == "---" {
		return map[string]any{}, nil
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(text), &node); err != nil {
		return nil, fmt.Errorf("Cannot parse as JSON (%s) or YAML (%s).", jsonErr(text), err)
	}
	if len(node.Content) == 0 {
		return map[string]any{}, nil
	}
	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("Input type `" + kindName(root) + "` is not a dictionary")
	}
	v, err := convert(root)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

func jsonErr(text string) string {
	var x any
	if err := json.Unmarshal([]byte(text), &x); err != nil {
		return err.Error()
	}
	return "ok"
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.SequenceNode:
		return "list"
	case yaml.ScalarNode:
		return "str"
	}
	return "unknown"
}

func convert(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return convert(n.Content[0])
	case yaml.AliasNode:
		return convert(n.Alias)
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Tag == "!!merge" {
				merged, err := convert(n.Content[i+1])
				if err != nil {
					return nil, err
				}
				if mm, ok := merged.(map[string]any); ok {
					for mk, mv := range mm {
						if _, exists := m[mk]; !exists {
							m[mk] = mv
						}
					}
				}
				continue
			}
			v, err := convert(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[k.Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := convert(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
		if n.Tag == "!vault" || n.Tag == "!vault-encrypted" {
			return map[string]any{"__ansible_vault": n.Value}, nil
		}
		if n.Tag == "!unsafe" {
			return n.Value, nil
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, nil
}

// MustJSON renders variables as a JSON string (for extra vars files and API output).
func MustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Merge overlays b onto a (top-level keys), returning a new map.
func Merge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
