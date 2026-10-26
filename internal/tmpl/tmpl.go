// Package tmpl renders the Jinja subset used in notification messages:
// {{ expression | filter }} where expressions are dotted paths into the
// context (job.summary_fields.inventory.name) or quoted strings, and
// {% if expr %}...{% elif expr %}...{% else %}...{% endif %} blocks with
// truthiness, ==, != and "not" tests.
package tmpl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var tokenRe = regexp.MustCompile(`(?s)\{\{.*?\}\}|\{%.*?%\}`)

type node interface{}

type textNode string
type exprNode string
type ifNode struct {
	branches []branch
}
type branch struct {
	cond string // "" for else
	body []node
}

// Validate parses a template without rendering it.
func Validate(src string) error {
	_, err := parse(src)
	return err
}

// Render evaluates src against ctx.
func Render(src string, ctx map[string]any) (string, error) {
	nodes, err := parse(src)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := render(&b, nodes, ctx); err != nil {
		return "", err
	}
	return b.String(), nil
}

func parse(src string) ([]node, error) {
	type frame struct {
		ifn  *ifNode
		prev *[]node
	}
	var root []node
	cur := &root
	var stack []frame
	pos := 0
	for _, loc := range tokenRe.FindAllStringIndex(src, -1) {
		if loc[0] > pos {
			*cur = append(*cur, textNode(src[pos:loc[0]]))
		}
		tok := src[loc[0]:loc[1]]
		pos = loc[1]
		inner := strings.TrimSpace(tok[2 : len(tok)-2])
		if strings.HasPrefix(tok, "{{") {
			if inner == "" {
				return nil, fmt.Errorf("empty expression")
			}
			*cur = append(*cur, exprNode(inner))
			continue
		}
		inner = strings.Trim(inner, "-")
		inner = strings.TrimSpace(inner)
		word, rest, _ := strings.Cut(inner, " ")
		rest = strings.TrimSpace(rest)
		switch word {
		case "if":
			if rest == "" {
				return nil, fmt.Errorf("if without condition")
			}
			n := &ifNode{branches: []branch{{cond: rest}}}
			*cur = append(*cur, n)
			stack = append(stack, frame{ifn: n, prev: cur})
			cur = &n.branches[0].body
		case "elif", "else":
			if len(stack) == 0 {
				return nil, fmt.Errorf("%s without if", word)
			}
			top := stack[len(stack)-1]
			cond := rest
			if word == "else" {
				cond = ""
			} else if cond == "" {
				return nil, fmt.Errorf("elif without condition")
			}
			top.ifn.branches = append(top.ifn.branches, branch{cond: cond})
			cur = &top.ifn.branches[len(top.ifn.branches)-1].body
		case "endif":
			if len(stack) == 0 {
				return nil, fmt.Errorf("endif without if")
			}
			cur = stack[len(stack)-1].prev
			stack = stack[:len(stack)-1]
		default:
			return nil, fmt.Errorf("unsupported tag %q", word)
		}
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("unclosed if block")
	}
	if pos < len(src) {
		*cur = append(*cur, textNode(src[pos:]))
	}
	return root, nil
}

func render(b *strings.Builder, nodes []node, ctx map[string]any) error {
	for _, n := range nodes {
		switch x := n.(type) {
		case textNode:
			b.WriteString(string(x))
		case exprNode:
			v, err := evalExpr(string(x), ctx)
			if err != nil {
				return err
			}
			b.WriteString(toString(v))
		case *ifNode:
			for _, br := range x.branches {
				ok := br.cond == ""
				if !ok {
					var err error
					if ok, err = evalCond(br.cond, ctx); err != nil {
						return err
					}
				}
				if ok {
					if err := render(b, br.body, ctx); err != nil {
						return err
					}
					break
				}
			}
		}
	}
	return nil
}

func evalCond(cond string, ctx map[string]any) (bool, error) {
	cond = strings.TrimSpace(cond)
	if strings.HasPrefix(cond, "not ") {
		ok, err := evalCond(cond[4:], ctx)
		return !ok, err
	}
	for _, op := range []string{"==", "!="} {
		if l, r, found := strings.Cut(cond, op); found {
			a, err := evalExpr(l, ctx)
			if err != nil {
				return false, err
			}
			bv, err := evalExpr(r, ctx)
			if err != nil {
				return false, err
			}
			eq := toString(a) == toString(bv)
			if op == "!=" {
				return !eq, nil
			}
			return eq, nil
		}
	}
	v, err := evalExpr(cond, ctx)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

func evalExpr(expr string, ctx map[string]any) (any, error) {
	parts := splitFilters(expr)
	v, err := value(strings.TrimSpace(parts[0]), ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range parts[1:] {
		if v, err = filter(strings.TrimSpace(f), v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// splitFilters splits on | outside quotes.
func splitFilters(s string) []string {
	var out []string
	var quote rune
	start := 0
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '|':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z0-9_]+|\[['"][^'"]*['"]\])*$`)
var segRe = regexp.MustCompile(`\.([A-Za-z0-9_]+)|\[['"]([^'"]*)['"]\]`)

func value(s string, ctx map[string]any) (any, error) {
	if s == "" {
		return nil, fmt.Errorf("empty expression")
	}
	if (strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") || strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`)) && len(s) >= 2 {
		return s[1 : len(s)-1], nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n, nil
	}
	switch s {
	case "true", "True":
		return true, nil
	case "false", "False":
		return false, nil
	case "none", "None":
		return nil, nil
	}
	if !identRe.MatchString(s) {
		return nil, fmt.Errorf("unsupported expression %q", s)
	}
	head := s
	if i := strings.IndexAny(s, ".["); i >= 0 {
		head = s[:i]
	}
	var cur any = ctx[head]
	for _, m := range segRe.FindAllStringSubmatch(s[len(head):], -1) {
		key := m[1]
		if key == "" {
			key = m[2]
		}
		switch c := cur.(type) {
		case map[string]any:
			cur = c[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(c) {
				cur = nil
			} else {
				cur = c[i]
			}
		default:
			cur = nil
		}
	}
	return cur, nil
}

var filterArgRe = regexp.MustCompile(`^(\w+)\s*(?:\((.*)\))?$`)

func filter(f string, v any) (any, error) {
	m := filterArgRe.FindStringSubmatch(f)
	if m == nil {
		return nil, fmt.Errorf("unsupported filter %q", f)
	}
	var args []string
	if strings.TrimSpace(m[2]) != "" {
		for _, a := range strings.Split(m[2], ",") {
			a = strings.TrimSpace(a)
			args = append(args, strings.Trim(a, `'"`))
		}
	}
	switch m[1] {
	case "default", "d":
		if v == nil || v == "" {
			if len(args) > 0 {
				return args[0], nil
			}
			return "", nil
		}
		return v, nil
	case "tojson", "to_json":
		b, err := json.Marshal(v)
		return rawJSON(b), err
	case "to_nice_json":
		b, err := json.MarshalIndent(v, "", "    ")
		return rawJSON(b), err
	case "lower":
		return strings.ToLower(toString(v)), nil
	case "upper":
		return strings.ToUpper(toString(v)), nil
	case "title":
		return strings.Title(strings.ToLower(toString(v))), nil //nolint:staticcheck // ASCII titles are enough here
	case "capitalize":
		s := strings.ToLower(toString(v))
		if s == "" {
			return s, nil
		}
		return strings.ToUpper(s[:1]) + s[1:], nil
	case "trim":
		return strings.TrimSpace(toString(v)), nil
	case "string":
		return toString(v), nil
	case "length", "count":
		switch x := v.(type) {
		case []any:
			return float64(len(x)), nil
		case map[string]any:
			return float64(len(x)), nil
		}
		return float64(len(toString(v))), nil
	case "replace":
		if len(args) != 2 {
			return nil, fmt.Errorf("replace needs two arguments")
		}
		return strings.ReplaceAll(toString(v), args[0], args[1]), nil
	}
	return nil, fmt.Errorf("unsupported filter %q", m[1])
}

type rawJSON []byte

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case rawJSON:
		return string(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}
