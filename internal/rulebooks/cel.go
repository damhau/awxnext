package rulebooks

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The kinds of event a ruleset reads, by the source that sends them: each
// has its own shape under ansible-rulebook, and its own place in the
// CloudEvent awxnext keeps.
const (
	// KindConnector: a source plugin run by a connector, or an event a rule
	// posted. The CloudEvent's data is the whole event ansible-rulebook sees.
	KindConnector = "connector"
	// KindWebhook: ansible.eda.webhook, a generic stream. The data is the
	// request's body, ansible-rulebook's event.payload.
	KindWebhook = "webhook"
	// KindAlertmanager: ansible.eda.alertmanager, an Alertmanager stream.
	// The data is one alert, ansible-rulebook's event.alert.
	KindAlertmanager = "alertmanager"
)

// postedPrefix starts the source of the events rules post.
const postedPrefix = "/api/v2/event_rules/"

// untranslatable is why a piece of a rulebook has no translation.
type untranslatable struct{ reason string }

func (u *untranslatable) Error() string { return u.reason }

func refuse(format string, args ...any) error {
	return &untranslatable{reason: fmt.Sprintf(format, args...)}
}

// view says where ansible-rulebook's event lives in an awxnext event.
type view struct {
	kind string
	// hostPath is where an Alertmanager alert names its host: its
	// data_host_path.
	hostPath []string
	// base is the CEL variable the event is in: event, or e in a
	// comprehension over events.
	base string
}

func (v view) on(base string) view {
	v.base = base
	return v
}

// ref is where a path of the rulebook leads in a view.
type ref struct {
	absent bool
	// expr and guard, for a value computed rather than read.
	expr, guard string
	root        string
	parts       []any
}

func (v view) resolve(parts []any) (ref, error) {
	data := ref{root: v.base, parts: []any{"data"}}
	head := func() string {
		if len(parts) == 0 {
			return ""
		}
		s, _ := parts[0].(string)
		return s
	}
	switch v.kind {
	case KindConnector:
		data.parts = append(data.parts, parts...)
		return data, nil
	case KindWebhook:
		switch head() {
		case "payload":
			data.parts = append(data.parts, parts[1:]...)
			return data, nil
		case "meta":
			if len(parts) > 1 && (parts[1] == "endpoint" || parts[1] == "headers") {
				return ref{}, refuse("event.meta.%v: an awxnext stream doesn't keep the webhook request's path and headers.", parts[1])
			}
			return ref{absent: true}, nil
		case "":
			return ref{}, refuse("the whole event: a webhook stream's event is event.payload.")
		}
		return ref{absent: true}, nil
	case KindAlertmanager:
		switch head() {
		case "alert":
			data.parts = append(data.parts, parts[1:]...)
			return data, nil
		case "meta":
			if len(parts) > 1 && parts[1] == "hosts" {
				if len(parts) > 2 {
					return ref{}, refuse("an index into event.meta.hosts.")
				}
				return ref{expr: v.hosts(), guard: "true"}, nil
			}
			if len(parts) > 1 && (parts[1] == "endpoint" || parts[1] == "headers") {
				return ref{}, refuse("event.meta.%v: an awxnext stream doesn't keep Alertmanager's request path and headers.", parts[1])
			}
			return ref{absent: true}, nil
		case "payload":
			return ref{}, refuse("event.payload: an awxnext Alertmanager stream gives rules each alert, not the notification that carries them.")
		case "":
			return ref{}, refuse("the whole event: an Alertmanager stream's event is event.alert.")
		}
		return ref{absent: true}, nil
	}
	return ref{}, refuse("an unknown kind of source")
}

// hosts is the host list the Alertmanager source puts in event.meta.hosts:
// the alert's data_host_path, without its port.
func (v view) hosts() string {
	if len(v.hostPath) == 0 {
		return "[]"
	}
	r := ref{root: v.base, parts: append([]any{"data"}, toAny(v.hostPath)...)}
	return fmt.Sprintf("(%s && type(%s) == string ? [%s.split(\":\")[0]] : [])", r.guardExpr(), r.value(), r.value())
}

// whole is the event as a playbook gets it in ansible_eda.event.
func (v view) whole() string {
	data := v.base + ".data"
	switch v.kind {
	case KindWebhook:
		return fmt.Sprintf(`{"payload": %s}`, data)
	case KindAlertmanager:
		return fmt.Sprintf(`{"alert": %s, "meta": {"hosts": %s}}`, data, v.hosts())
	}
	return data
}

// limit is the limit ansible-rulebook launches with when the action sets
// none: the event's hosts, else the ruleset's.
func (v view) limit(hosts string) string {
	switch v.kind {
	case KindAlertmanager:
		return fmt.Sprintf(`%s.join(",")`, v.hosts())
	case KindConnector:
		r := ref{root: v.base, parts: []any{"data", "meta", "hosts"}}
		return fmt.Sprintf(`(%s ? (type(%s) == string ? %s : %s.join(",")) : %s)`, r.guardExpr(), r.value(), r.value(), r.value(), celString(hosts))
	}
	return celString(hosts)
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

var celIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var celReserved = map[string]bool{"in": true, "as": true, "break": true, "const": true, "continue": true, "else": true, "for": true,
	"function": true, "if": true, "import": true, "let": true, "loop": true, "package": true, "namespace": true, "null": true,
	"return": true, "var": true, "void": true, "while": true, "true": true, "false": true}

func (r ref) value() string {
	if r.expr != "" {
		return r.expr
	}
	var b strings.Builder
	b.WriteString(r.root)
	for _, p := range r.parts {
		switch k := p.(type) {
		case string:
			if celIdent.MatchString(k) && !celReserved[k] {
				b.WriteString("." + k)
			} else {
				b.WriteString("[" + celString(k) + "]")
			}
		case int64:
			b.WriteString(fmt.Sprintf("[%d]", k))
		}
	}
	return b.String()
}

// guardExpr holds when the path leads to a value: ansible-rulebook finds no
// match in a comparison with a missing attribute.
func (r ref) guardExpr() string {
	if r.expr != "" {
		return r.guard
	}
	var b strings.Builder
	b.WriteString(r.root)
	for _, p := range r.parts {
		switch k := p.(type) {
		case string:
			if celIdent.MatchString(k) && !celReserved[k] {
				b.WriteString(".?" + k)
			} else {
				b.WriteString("[?" + celString(k) + "]")
			}
		case int64:
			b.WriteString(fmt.Sprintf("[?%d]", k))
		}
	}
	return b.String() + ".hasValue()"
}

// celString is a CEL string literal.
func celString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// celLiteral writes a YAML or JSON value as a CEL literal.
func celLiteral(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(x), nil
	case string:
		return celString(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		if x > math.MaxInt64 {
			return strconv.FormatUint(x, 10) + "u", nil
		}
		return strconv.FormatUint(x, 10), nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return "", refuse("the number %v", x)
		}
		s := strconv.FormatFloat(x, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eEn") {
			s += ".0"
		}
		return s, nil
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return strconv.FormatInt(n, 10), nil
		}
		f, err := x.Float64()
		if err != nil {
			return "", refuse("the number %s", x)
		}
		return celLiteral(f)
	case time.Time:
		return celString(yamlTime(x)), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := celLiteral(e)
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
			s, err := celLiteral(x[k])
			if err != nil {
				return "", err
			}
			parts[i] = celString(k) + ": " + s
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	}
	return "", refuse("a value of type %T", v)
}

// condCtx translates the conditions of one rule.
type condCtx struct {
	v    view
	vars map[string]any
	// notes are the differences of the translation, for the report.
	notes *[]string
}

func and(a, b string) string {
	switch {
	case a == "false" || b == "false":
		return "false"
	case a == "true":
		return b
	case b == "true":
		return a
	}
	return "(" + a + " && " + b + ")"
}

func or(a, b string) string {
	switch {
	case a == "true" || b == "true":
		return "true"
	case a == "false":
		return b
	case b == "false":
		return a
	}
	return "(" + a + " || " + b + ")"
}

func not(a string) string {
	switch a {
	case "true":
		return "false"
	case "false":
		return "true"
	}
	return "!" + a
}

func andAll(parts ...string) string {
	out := "true"
	for _, p := range parts {
		out = and(out, p)
	}
	return out
}

// operand is a value a condition compares.
type operand struct {
	expr   string
	guards []string
	absent bool
	lit    *literal
}

func (c *condCtx) boolean(n node) (string, error) {
	switch x := n.(type) {
	case *literal:
		if b, ok := x.v.(bool); ok {
			return strconv.FormatBool(b), nil
		}
		return "", refuse("a condition that is only a value (%v)", x.v)
	case *assignNode:
		return c.boolean(x.x)
	case *binaryNode:
		switch x.op {
		case "and", "or":
			l, err := c.boolean(x.l)
			if err != nil {
				return "", err
			}
			r, err := c.boolean(x.r)
			if err != nil {
				return "", err
			}
			if x.op == "and" {
				return and(l, r), nil
			}
			return or(l, r), nil
		case "==", "!=", "<", ">", "<=", ">=":
			return c.compare(x)
		case "in", "not in":
			return c.in(x)
		case "contains", "not contains":
			return c.contains(x)
		}
		return "", refuse("%s as a condition", x.op)
	case *unaryNode:
		inner, err := c.boolean(x.x)
		if err != nil {
			return "", err
		}
		guards, err := c.guardsOf(x.x)
		if err != nil {
			return "", err
		}
		return and(andAll(guards...), not(paren(inner))), nil
	case *isNode:
		p, ok := x.x.(*pathNode)
		if !ok {
			return "", refuse("is defined on something other than an attribute")
		}
		o, err := c.operand(p)
		if err != nil {
			return "", err
		}
		defined := "true"
		if o.absent {
			defined = "false"
		} else if o.lit == nil {
			defined = andAll(o.guards...)
		}
		if x.not {
			return not(paren(defined)), nil
		}
		return defined, nil
	case *searchNode:
		return c.search(x)
	case *selectNode:
		return c.selectList(x.x, x.not, func(item string) (string, error) { return c.itemTest(item, x.op, x.v) })
	case *selectAttrNode:
		parts := strings.Split(x.key, ".")
		return c.selectList(x.x, x.not, func(item string) (string, error) {
			r := ref{root: item, parts: toAny(parts)}
			test, err := c.itemTest(r.value(), x.op, x.v)
			if err != nil {
				return "", err
			}
			return and(r.guardExpr(), test), nil
		})
	case *pathNode:
		// ansible-rulebook matches a path alone when it is true.
		return c.compare(&binaryNode{op: "==", l: x, r: &literal{v: true}})
	case *listNode:
		return "", refuse("a list as a condition")
	}
	return "", refuse("this condition")
}

func paren(s string) string {
	if s == "true" || s == "false" || strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") && balanced(s[1:len(s)-1]) {
		return s
	}
	return "(" + s + ")"
}

func balanced(s string) bool {
	depth := 0
	inStr := false
	for i := 0; i < len(s); i++ {
		switch {
		case inStr && s[i] == '\\':
			i++
		case s[i] == '"':
			inStr = !inStr
		case inStr:
		case s[i] == '(':
			depth++
		case s[i] == ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// guardsOf are the presence checks of every attribute a condition reads.
func (c *condCtx) guardsOf(n node) ([]string, error) {
	var out []string
	var walk func(n node) error
	walk = func(n node) error {
		switch x := n.(type) {
		case *pathNode:
			o, err := c.operand(x)
			if err != nil {
				return err
			}
			if o.absent {
				out = append(out, "false")
			}
			out = append(out, o.guards...)
		case *binaryNode:
			if err := walk(x.l); err != nil {
				return err
			}
			return walk(x.r)
		case *unaryNode:
			return walk(x.x)
		case *assignNode:
			return walk(x.x)
		case *searchNode:
			return walk(x.x)
		case *selectNode:
			return walk(x.x)
		case *selectAttrNode:
			return walk(x.x)
		case *listNode:
			for _, i := range x.items {
				if err := walk(i); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if _, ok := n.(*isNode); ok {
		return nil, nil
	}
	return out, walk(n)
}

func (c *condCtx) operand(n node) (operand, error) {
	switch x := n.(type) {
	case *literal:
		s, err := celLiteral(x.v)
		return operand{expr: s, lit: x}, err
	case *listNode:
		parts := make([]string, len(x.items))
		var guards []string
		for i, item := range x.items {
			o, err := c.operand(item)
			if err != nil {
				return operand{}, err
			}
			if o.absent {
				return operand{absent: true}, nil
			}
			parts[i] = o.expr
			guards = append(guards, o.guards...)
		}
		return operand{expr: "[" + strings.Join(parts, ", ") + "]", guards: guards}, nil
	case *pathNode:
		switch x.root {
		case "vars":
			v, ok := lookupVar(c.vars, x.parts)
			if !ok {
				return operand{}, refuse("%s: the rulebook's variables don't set it.", x.text)
			}
			s, err := celLiteral(v)
			return operand{expr: s, lit: &literal{v: v}}, err
		case "events", "facts":
			return operand{}, refuse("%s: awxnext's conditions read the event they test, not the other events of the rule.", x.text)
		}
		r, err := c.v.resolve(x.parts)
		if err != nil {
			return operand{}, err
		}
		if r.absent {
			return operand{absent: true}, nil
		}
		g := r.guardExpr()
		var guards []string
		if g != "true" {
			guards = []string{g}
		}
		return operand{expr: r.value(), guards: guards}, nil
	case *binaryNode:
		switch x.op {
		case "+", "-":
			l, err := c.operand(x.l)
			if err != nil {
				return operand{}, err
			}
			r, err := c.operand(x.r)
			if err != nil {
				return operand{}, err
			}
			if l.absent || r.absent {
				return operand{absent: true}, nil
			}
			if isString(l.lit) || isString(r.lit) {
				return operand{}, refuse("%s on strings", x.op)
			}
			return operand{expr: fmt.Sprintf("dyn(double(%s) %s double(%s))", l.expr, x.op, r.expr), guards: append(l.guards, r.guards...)}, nil
		case "*", "/":
			return operand{}, refuse("%s: ansible-rulebook 1.3 parses it but doesn't evaluate it.", x.op)
		}
		inner, err := c.boolean(x)
		return operand{expr: inner}, err
	}
	inner, err := c.boolean(n)
	return operand{expr: inner}, err
}

func isString(l *literal) bool {
	if l == nil {
		return false
	}
	_, ok := l.v.(string)
	return ok
}

func lookupVar(vars map[string]any, parts []any) (any, bool) {
	var cur any = vars
	for _, p := range parts {
		switch k := p.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = m[k]; !ok {
				return nil, false
			}
		case int64:
			l, ok := cur.([]any)
			if !ok || k < 0 || k >= int64(len(l)) {
				return nil, false
			}
			cur = l[k]
		}
	}
	return cur, len(parts) > 0
}

func (c *condCtx) compare(x *binaryNode) (string, error) {
	l, err := c.operand(x.l)
	if err != nil {
		return "", err
	}
	r, err := c.operand(x.r)
	if err != nil {
		return "", err
	}
	if l.absent || r.absent {
		return "false", nil
	}
	return and(andAll(append(l.guards, r.guards...)...), fmt.Sprintf("%s %s %s", l.expr, x.op, r.expr)), nil
}

func (c *condCtx) in(x *binaryNode) (string, error) {
	l, err := c.operand(x.l)
	if err != nil {
		return "", err
	}
	r, err := c.operand(x.r)
	if err != nil {
		return "", err
	}
	if l.absent || r.absent {
		return "false", nil
	}
	test := fmt.Sprintf("%s in %s", l.expr, r.expr)
	if x.op == "not in" {
		test = "!(" + test + ")"
	}
	_, isList := x.r.(*listNode)
	switch {
	case isList:
	case r.lit != nil:
		if _, ok := r.lit.v.([]any); !ok {
			return "", refuse("%s needs a list on its right", x.op)
		}
	default:
		test = fmt.Sprintf("type(%s) == list && %s", r.expr, test)
	}
	return and(andAll(append(l.guards, r.guards...)...), "("+test+")"), nil
}

func (c *condCtx) contains(x *binaryNode) (string, error) {
	l, err := c.operand(x.l)
	if err != nil {
		return "", err
	}
	r, err := c.operand(x.r)
	if err != nil {
		return "", err
	}
	if l.absent || r.absent {
		return "false", nil
	}
	test := fmt.Sprintf("%s in %s", r.expr, l.expr)
	if x.op == "not contains" {
		test = "!(" + test + ")"
	}
	if l.lit == nil {
		test = fmt.Sprintf("type(%s) == list && %s", l.expr, test)
	} else if _, ok := l.lit.v.([]any); !ok {
		return "false", nil
	}
	return and(andAll(append(l.guards, r.guards...)...), "("+test+")"), nil
}

// pattern is a match, search or regex as RE2, as CEL's matches takes it.
func pattern(kind, p string, options map[string]any) (string, error) {
	flags := ""
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		on, _ := options[k].(bool)
		switch k {
		case "ignorecase":
			if on {
				flags += "i"
			}
		case "multiline":
			if on {
				flags += "m"
			}
		default:
			return "", refuse("the %s option %s", kind, k)
		}
	}
	if _, err := regexp.Compile(p); err != nil {
		return "", refuse("the pattern %q: Go's regular expressions (RE2) don't take it: %v", p, err)
	}
	if kind == "match" {
		p = "^(?:" + p + ")"
	}
	if flags != "" {
		f := []byte(flags)
		sort.Slice(f, func(i, j int) bool { return f[i] < f[j] })
		p = "(?" + string(f) + ")" + p
	}
	return p, nil
}

func (c *condCtx) search(x *searchNode) (string, error) {
	pat, err := c.operand(x.pattern)
	if err != nil {
		return "", err
	}
	s, ok := "", false
	if pat.lit != nil {
		s, ok = pat.lit.v.(string)
	}
	if !ok {
		return "", refuse("a %s whose pattern is not a string", x.kind)
	}
	re, err := pattern(x.kind, s, x.options)
	if err != nil {
		return "", err
	}
	l, err := c.operand(x.x)
	if err != nil {
		return "", err
	}
	if l.absent {
		return "false", nil
	}
	m := func(v string) string {
		if x.not {
			return fmt.Sprintf("!%s.matches(%s)", v, celString(re))
		}
		return fmt.Sprintf("%s.matches(%s)", v, celString(re))
	}
	test := fmt.Sprintf("(type(%s) == string ? %s : type(%s) == list && %s.exists(i, type(i) == string && %s))", l.expr, m(l.expr), l.expr, l.expr, m("i"))
	if l.lit != nil {
		if _, isStr := l.lit.v.(string); !isStr {
			return "false", nil
		}
		test = m(l.expr)
	}
	return and(andAll(l.guards...), test), nil
}

func (c *condCtx) selectList(list node, negate bool, test func(item string) (string, error)) (string, error) {
	l, err := c.operand(list)
	if err != nil {
		return "", err
	}
	if l.absent {
		return "false", nil
	}
	t, err := test("i")
	if err != nil {
		return "", err
	}
	if negate {
		t = not(paren(t))
	}
	return and(andAll(l.guards...), fmt.Sprintf("(type(%s) == list && %s.exists(i, %s))", l.expr, l.expr, t)), nil
}

// itemTest is a select's or selectattr's test of one item.
func (c *condCtx) itemTest(item, op string, v node) (string, error) {
	val, err := c.operand(v)
	if err != nil {
		return "", err
	}
	if val.absent {
		return "false", nil
	}
	if len(val.guards) > 0 {
		return "", refuse("a select whose value reads the event")
	}
	switch op {
	case "==", "!=", ">", ">=", "<", "<=":
		return fmt.Sprintf("%s %s %s", item, op, val.expr), nil
	case "in":
		return fmt.Sprintf("%s in %s", item, val.expr), nil
	case "not in":
		return fmt.Sprintf("!(%s in %s)", item, val.expr), nil
	case "contains":
		return fmt.Sprintf("(type(%s) == list && %s in %s)", item, val.expr, item), nil
	case "not contains":
		return fmt.Sprintf("(type(%s) == list && !(%s in %s))", item, val.expr, item), nil
	case "regex", "search", "match":
		s, ok := "", false
		if val.lit != nil {
			s, ok = val.lit.v.(string)
		}
		if !ok {
			return "", refuse("a %s whose pattern is not a string", op)
		}
		re, err := pattern(op, s, nil)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(type(%s) == string && %s.matches(%s))", item, item, celString(re)), nil
	}
	return "", refuse("the operator %s", op)
}
