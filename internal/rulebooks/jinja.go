package rulebooks

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// actCtx translates the arguments of a rule's action, which
// ansible-rulebook renders with Jinja over event (or events), the
// rulebook's variables and the facts.
type actCtx struct {
	v    view
	vars map[string]any
	// conds is how many conditions the rule's pattern has: 0 for a rule
	// that fires with one event.
	conds int
	// names are the pattern's events named by assignments.
	names map[string]int
}

// value translates an argument: a mapping, a list, or a scalar that may
// hold {{ expressions }}.
func (a *actCtx) value(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return a.text(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := a.value(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			if strings.Contains(k, "{{") {
				return "", refuse("a Jinja expression in the key %q", k)
			}
			s, err := a.value(x[k])
			if err != nil {
				return "", err
			}
			parts[i] = celString(k) + ": " + s
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	}
	return celLiteral(v)
}

// text translates a string: as is, the value of its one {{ expression }},
// or the concatenation of its text and expressions.
func (a *actCtx) text(s string) (string, error) {
	if !strings.Contains(s, "{{") && !strings.Contains(s, "{%") {
		return celString(s), nil
	}
	if strings.Contains(s, "{%") || strings.Contains(s, "{#") {
		return "", refuse("the Jinja statement in %q: awxnext translates {{ expressions }} only", s)
	}
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "{{") && strings.HasSuffix(t, "}}") && strings.Count(t, "{{") == 1 {
		// A string that is one expression keeps its value's type, as
		// ansible-rulebook's native Jinja does.
		return a.expr(strings.TrimSpace(t[2 : len(t)-2]))
	}
	var parts []string
	rest := s
	for rest != "" {
		i := strings.Index(rest, "{{")
		if i < 0 {
			parts = append(parts, celString(rest))
			break
		}
		if i > 0 {
			parts = append(parts, celString(rest[:i]))
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			return "", refuse("the Jinja expression in %q is not closed", s)
		}
		e, err := a.expr(strings.TrimSpace(rest[i+2 : i+j]))
		if err != nil {
			return "", err
		}
		parts = append(parts, "string("+e+")")
		rest = rest[i+j+2:]
	}
	return strings.Join(parts, " + "), nil
}

// expr translates one Jinja expression: an attribute path, with
// default(value) as its only filter.
func (a *actCtx) expr(s string) (string, error) {
	var def *string
	if i := strings.Index(s, "|"); i >= 0 {
		filter := strings.TrimSpace(s[i+1:])
		s = strings.TrimSpace(s[:i])
		if !strings.HasPrefix(filter, "default(") || !strings.HasSuffix(filter, ")") {
			return "", refuse("the Jinja filter %q: awxnext translates attribute paths, with default(value)", filter)
		}
		arg := strings.TrimSpace(filter[len("default(") : len(filter)-1])
		n, err := parseCondition(arg)
		if err != nil {
			return "", refuse("the default of %q: %v", s, err)
		}
		lit, ok := n.(*literal)
		if !ok {
			return "", refuse("the default of %q is not a value", s)
		}
		d, err := celLiteral(lit.v)
		if err != nil {
			return "", err
		}
		def = &d
	}
	root, parts, err := jinjaPath(s)
	if err != nil {
		return "", err
	}
	var r ref
	switch root {
	case "event", "fact":
		if a.conds > 0 {
			return "", refuse("%s in the action of a rule with several conditions: use events.m_0 and the others", s)
		}
		if len(parts) == 0 {
			return a.v.whole(), nil
		}
		if r, err = a.v.resolve(parts); err != nil {
			return "", err
		}
	case "events", "facts":
		if a.conds == 0 || len(parts) == 0 {
			return "", refuse("%s in the action of a rule with one condition", s)
		}
		name, _ := parts[0].(string)
		n, ok := strings.CutPrefix(name, "m_")
		i, err := strconv.Atoi(n)
		if j, named := a.names[name]; named {
			i, ok, err = j, true, nil
		}
		if !ok || err != nil || i < 0 || i >= a.conds {
			return "", refuse("%s: awxnext names a pattern's events m_0 to m_%d, in the order they came", s, a.conds-1)
		}
		v := a.v.on(fmt.Sprintf("events[%d]", i))
		if len(parts) == 1 {
			return v.whole(), nil
		}
		if r, err = v.resolve(parts[1:]); err != nil {
			return "", err
		}
	default:
		val, ok := lookupVar(a.vars, append([]any{root}, parts...))
		if !ok {
			if def != nil {
				return *def, nil
			}
			return "", refuse("the Jinja variable %s: the rulebook's variables don't set it", s)
		}
		return celLiteral(val)
	}
	if r.absent {
		if def != nil {
			return *def, nil
		}
		return "", refuse("%s: this source's events never have it", s)
	}
	if def != nil {
		return fmt.Sprintf("(%s ? %s : %s)", r.guardExpr(), r.value(), *def), nil
	}
	return r.value(), nil
}

// jinjaPath splits a.b["c"][0] into its root and parts.
func jinjaPath(s string) (string, []any, error) {
	toks, err := lex(s)
	if err != nil {
		return "", nil, refuse("the Jinja expression %q: %v", s, err)
	}
	p := &parser{toks: toks, src: s}
	t := p.next()
	if t.kind != "word" || !celIdent.MatchString(t.text) {
		return "", nil, refuse("the Jinja expression %q: awxnext translates attribute paths", s)
	}
	var parts []any
	for p.peek().kind != "end" {
		switch {
		case p.isOp(".") && p.peekAt(1).kind == "word":
			p.next()
			parts = append(parts, p.next().text)
		case p.isOp("[") && p.indexAhead():
			p.next()
			k, err := p.atom()
			if err != nil {
				return "", nil, refuse("the Jinja expression %q: %v", s, err)
			}
			parts = append(parts, k.(*literal).v)
			p.next()
		default:
			return "", nil, refuse("the Jinja expression %q: awxnext translates attribute paths", s)
		}
	}
	return t.text, parts, nil
}
