// Package rulebooks translates ansible-rulebook's rulebooks (roadmap
// M14-09) into what awxnext runs them with: event streams for the webhook
// and Alertmanager sources, connectors for the other source plugins, and
// event rules with CEL filters for the rules. The translation reads the
// YAML alone; internal/resources applies its plan to the database and
// looks up the templates it names.
package rulebooks

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// MaxSize bounds a rulebook's YAML.
const MaxSize = 1 << 20

// ruleset is one play of a rulebook, as its YAML has it.
type ruleset struct {
	Name    string
	Hosts   any
	Sources []source
	Rules   []rule
	Options map[string]any
}

type source struct {
	Name    string
	Plugin  string
	Args    map[string]any
	Filters []any
	err     error
}

type rule struct {
	Name      string
	Condition any
	Actions   []action
	Enabled   bool
	Throttle  map[string]any
	err       error
}

type action struct {
	Kind string
	Args map[string]any
	err  error
}

// parse reads a rulebook: a YAML list of rulesets. A piece it can't read
// is kept with its error, for the report.
func parse(text []byte) ([]ruleset, error) {
	if len(text) > MaxSize {
		return nil, fmt.Errorf("the rulebook is larger than %d bytes", MaxSize)
	}
	var doc any
	dec := yaml.NewDecoder(bytes.NewReader(text))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("the rulebook is not valid YAML: %v", err)
	}
	doc = normalize(doc)
	list, ok := doc.([]any)
	if !ok {
		return nil, errors.New("a rulebook is a list of rulesets")
	}
	if len(list) == 0 {
		return nil, errors.New("the rulebook has no ruleset")
	}
	var out []ruleset
	seen := map[string]bool{}
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ruleset %d is not a mapping", i+1)
		}
		rs := ruleset{Options: map[string]any{}}
		rs.Name = strings.TrimSpace(str(m["name"]))
		if rs.Name == "" {
			return nil, fmt.Errorf("ruleset %d has no name", i+1)
		}
		if seen[rs.Name] {
			return nil, fmt.Errorf("two rulesets are named %q", rs.Name)
		}
		seen[rs.Name] = true
		rs.Hosts = m["hosts"]
		for k, v := range m {
			switch k {
			case "name", "hosts", "sources", "rules":
			default:
				rs.Options[k] = v
			}
		}
		srcs, _ := m["sources"].([]any)
		if m["sources"] != nil && srcs == nil {
			return nil, fmt.Errorf("ruleset %q: sources is not a list", rs.Name)
		}
		for _, s := range srcs {
			rs.Sources = append(rs.Sources, parseSource(s))
		}
		rules, _ := m["rules"].([]any)
		if m["rules"] != nil && rules == nil {
			return nil, fmt.Errorf("ruleset %q: rules is not a list", rs.Name)
		}
		for j, r := range rules {
			rs.Rules = append(rs.Rules, parseRule(j, r))
		}
		out = append(out, rs)
	}
	return out, nil
}

func parseSource(v any) source {
	m, ok := v.(map[string]any)
	if !ok {
		return source{err: errors.New("the source is not a mapping")}
	}
	s := source{Name: str(m["name"]), Args: map[string]any{}}
	var plugins []string
	for k := range m {
		if k != "name" && k != "filters" {
			plugins = append(plugins, k)
		}
	}
	if len(plugins) != 1 {
		s.err = errors.New("a source names one plugin")
		return s
	}
	s.Plugin = plugins[0]
	switch a := m[s.Plugin].(type) {
	case map[string]any:
		s.Args = a
	case nil:
	default:
		s.err = errors.New("the plugin's arguments are not a mapping")
	}
	if f, ok := m["filters"].([]any); ok {
		s.Filters = f
	} else if m["filters"] != nil {
		s.err = errors.New("filters is not a list")
	}
	return s
}

func parseRule(i int, v any) rule {
	m, ok := v.(map[string]any)
	if !ok {
		return rule{Name: fmt.Sprintf("rule %d", i+1), err: errors.New("the rule is not a mapping")}
	}
	r := rule{Name: strings.TrimSpace(str(m["name"])), Condition: m["condition"], Enabled: true}
	if r.Name == "" {
		r.Name = fmt.Sprintf("rule %d", i+1)
		r.err = errors.New("the rule has no name")
		return r
	}
	if e, ok := m["enabled"].(bool); ok {
		r.Enabled = e
	}
	if t, ok := m["throttle"].(map[string]any); ok {
		r.Throttle = t
	} else if m["throttle"] != nil {
		r.err = errors.New("throttle is not a mapping")
		return r
	}
	if m["condition"] == nil {
		r.err = errors.New("the rule has no condition")
		return r
	}
	var acts []any
	switch {
	case m["actions"] != nil:
		acts, ok = m["actions"].([]any)
		if !ok {
			r.err = errors.New("actions is not a list")
			return r
		}
	case m["action"] != nil:
		acts = []any{m["action"]}
	default:
		r.err = errors.New("the rule has no action")
		return r
	}
	for _, a := range acts {
		r.Actions = append(r.Actions, parseAction(a))
	}
	return r
}

func parseAction(v any) action {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return action{Kind: "?", err: errors.New("an action is a mapping of one action's name to its arguments")}
	}
	for k, a := range m {
		act := action{Kind: k, Args: map[string]any{}}
		switch x := a.(type) {
		case map[string]any:
			act.Args = x
		case nil:
		default:
			act.err = errors.New("the action's arguments are not a mapping")
		}
		return act
	}
	return action{}
}

// normalize turns YAML's values into JSON's: string keys, int64 numbers,
// and timestamps back into the text they were written as.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalize(e)
		}
		return x
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[fmt.Sprint(k)] = normalize(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = normalize(e)
		}
		return x
	case int:
		return int64(x)
	case uint64:
		return x
	case time.Time:
		return yamlTime(x)
	}
	return v
}

func yamlTime(t time.Time) string {
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format(time.RFC3339Nano)
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return fmt.Sprint(v)
}
