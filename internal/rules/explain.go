package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/interpreter"
	"cel.dev/cel-go/parser"
)

// Miss is the part of a filter that kept it from holding for an event: the
// first of the conditions joined by && that was false, or else the first
// that failed, or the whole filter.
type Miss struct {
	// Expression is the part as the filter writes it.
	Expression string
	// Start and End are where the part is in the filter, in characters;
	// -1 when its text could not be found there.
	Start, End int
	// Error is why the part could not be evaluated; empty when it was false.
	Error string
	// Values are the values of the part's operands on the event.
	Values []Seen
}

// Seen is the value of an operand of the part that failed. A value too
// large to keep is summed up instead, so the decision log doesn't keep the
// event a second time.
type Seen struct {
	Expression string
	Value      any
	Summary    string
	Error      string
}

const (
	maxSeen      = 5
	maxSeenBytes = 256
	maxSeenText  = 200
	// maxMissText bounds the expression a decision keeps.
	maxMissText = 512
)

// Details is the miss as a decision's details keep it.
func (m *Miss) Details() map[string]any {
	d := map[string]any{"expression": clip(m.Expression, maxMissText)}
	if m.Start >= 0 {
		d["start"], d["end"] = m.Start, m.End
	}
	if m.Error != "" {
		d["error"] = m.Error
	}
	values := make([]map[string]any, 0, len(m.Values))
	for _, s := range m.Values {
		v := map[string]any{"expression": clip(s.Expression, maxMissText)}
		switch {
		case s.Error != "":
			v["error"] = s.Error
		case s.Summary != "":
			v["summary"] = s.Summary
		default:
			v["value"] = s.Value
		}
		values = append(values, v)
	}
	d["values"] = values
	return d
}

// Reason says in a sentence why the filter didn't hold; Details has the
// values it saw.
func (m *Miss) Reason() string {
	part := clip(m.Expression, maxSeenText)
	if m.Error != "" {
		return fmt.Sprintf("The filter's condition %s could not be evaluated: %s.", part, clip(m.Error, maxSeenText))
	}
	return fmt.Sprintf("The filter's condition %s is false.", part)
}

// String is the reason with the values the part saw.
func (m *Miss) String() string {
	var with []string
	for _, s := range m.Values {
		switch {
		case s.Error != "":
			with = append(with, fmt.Sprintf("%s failing (%s)", clip(s.Expression, 80), clip(s.Error, 80)))
		case s.Summary != "":
			with = append(with, fmt.Sprintf("%s %s", clip(s.Expression, 80), s.Summary))
		default:
			b, _ := json.Marshal(s.Value)
			with = append(with, fmt.Sprintf("%s = %s", clip(s.Expression, 80), clip(string(b), 80)))
		}
	}
	reason := m.Reason()
	if len(with) == 0 || m.Error != "" {
		return reason
	}
	return strings.TrimSuffix(reason, ".") + ", with " + strings.Join(with, ", ") + "."
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// explainEnv is runtimeEnv with the macro calls kept, so that a part of a
// filter prints as written: has(...), exists(...).
var explainEnv = func() *cel.Env {
	env, err := runtimeEnv.Extend(cel.EnableMacroCallTracking())
	if err != nil {
		panic(err)
	}
	return env
}()

// filterProgram is a filter compiled to record the value of each of its
// parts, and the parts a miss can name.
type filterProgram struct {
	prg   cel.Program
	root  *part
	parts []*part
}

type part struct {
	id         int64
	text       string
	start, end int
	operands   []operand
}

type operand struct {
	id   int64
	text string
	// keys: has() failed, and what helps is the keys the map has.
	keys bool
}

var filters = struct {
	sync.Mutex
	m map[string]*filterProgram
}{m: map[string]*filterProgram{}}

func compileFilter(expr string) (*filterProgram, error) {
	filters.Lock()
	fp, ok := filters.m[expr]
	filters.Unlock()
	if ok {
		return fp, nil
	}
	ast, iss := explainEnv.Compile(expr)
	if iss.Err() != nil {
		return nil, errors.New(iss.Err().Error())
	}
	prg, err := explainEnv.Program(ast, cel.CostLimit(costLimit), cel.InterruptCheckFrequency(100), cel.EvalOptions(cel.OptTrackState))
	if err != nil {
		return nil, err
	}
	na := ast.NativeRep()
	b := &builder{ast: na, info: na.SourceInfo(), src: []rune(expr)}
	fp = &filterProgram{prg: prg, root: b.part(na.Expr())}
	for _, e := range conjuncts(na.Expr()) {
		fp.parts = append(fp.parts, b.part(e))
	}
	filters.Lock()
	if len(filters.m) > 10000 {
		filters.m = map[string]*filterProgram{}
	}
	filters.m[expr] = fp
	filters.Unlock()
	return fp, nil
}

// Filter evaluates a filter on an event. When it doesn't hold, or fails,
// the miss names the part that failed and the values it saw.
func Filter(ctx context.Context, expr string, v Vars) (bool, *Miss) {
	fp, err := compileFilter(expr)
	if err != nil {
		return false, &Miss{Expression: expr, Start: -1, End: -1, Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()
	out, det, err := fp.prg.ContextEval(ctx, v.activation())
	if err == nil {
		if b, ok := out.(types.Bool); ok && bool(b) {
			return true, nil
		}
	}
	var st interpreter.EvalState
	if det != nil {
		st = det.State()
	}
	return false, fp.explain(st, out, err)
}

// explain names the first part that was false, else the first that
// failed, else the whole filter.
func (fp *filterProgram) explain(st interpreter.EvalState, out ref.Val, err error) *Miss {
	if st != nil {
		var failed *part
		for _, p := range fp.parts {
			v, ok := st.Value(p.id)
			if !ok {
				continue
			}
			if b, isBool := v.(types.Bool); isBool && !bool(b) {
				return p.miss(st, "")
			}
			if failed == nil && types.IsUnknownOrError(v) {
				failed = p
			}
		}
		if failed != nil {
			v, _ := st.Value(failed.id)
			return failed.miss(st, fmt.Sprint(v))
		}
	}
	msg := ""
	switch {
	case err != nil:
		msg = err.Error()
	case out != nil && out.Type() != types.BoolType:
		msg = fmt.Sprintf("the filter gave %s, not true or false", out.Type().TypeName())
	}
	return fp.root.miss(st, msg)
}

func (p *part) miss(st interpreter.EvalState, errMsg string) *Miss {
	m := &Miss{Expression: p.text, Start: p.start, End: p.end, Error: errMsg}
	if st == nil {
		return m
	}
	for _, o := range p.operands {
		v, ok := st.Value(o.id)
		if !ok {
			continue
		}
		m.Values = append(m.Values, seen(o.text, v, o.keys))
		if len(m.Values) == maxSeen {
			break
		}
	}
	return m
}

func seen(text string, v ref.Val, keys bool) Seen {
	s := Seen{Expression: text}
	if types.IsUnknownOrError(v) {
		s.Error = fmt.Sprint(v)
		return s
	}
	n, err := native(v)
	if err != nil {
		s.Summary = "is a " + v.Type().TypeName()
		return s
	}
	if m, ok := n.(map[string]any); ok && keys {
		s.Summary = describeMap(m)
		return s
	}
	b, _ := json.Marshal(n)
	if len(b) <= maxSeenBytes {
		s.Value = n
		return s
	}
	switch x := n.(type) {
	case string:
		s.Summary = fmt.Sprintf("is a string of %d characters, %q…", len([]rune(x)), clip(x, 60))
	case map[string]any:
		s.Summary = describeMap(x)
	case []any:
		s.Summary = fmt.Sprintf("is a list of %d items", len(x))
	default:
		s.Summary = "is a large " + v.Type().TypeName()
	}
	return s
}

func describeMap(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, clip(k, 40))
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "is an empty map"
	}
	more := ""
	if len(keys) > 10 {
		more = fmt.Sprintf(" and %d more", len(keys)-10)
		keys = keys[:10]
	}
	return fmt.Sprintf("is a map with the keys %s%s", strings.Join(keys, ", "), more)
}

// conjuncts are the conditions an expression joins with &&, in order.
func conjuncts(e celast.Expr) []celast.Expr {
	if e.Kind() == celast.CallKind && e.AsCall().FunctionName() == operators.LogicalAnd {
		var out []celast.Expr
		for _, a := range e.AsCall().Args() {
			out = append(out, conjuncts(a)...)
		}
		return out
	}
	return []celast.Expr{e}
}

// builder finds the text of an expression's parts and their operands.
type builder struct {
	ast  *celast.AST
	info *celast.SourceInfo
	src  []rune
}

func (b *builder) part(e celast.Expr) *part {
	text, start, end := b.text(e)
	return &part{id: e.ID(), text: text, start: start, end: end, operands: b.operands(e, map[string]bool{})}
}

// operands are the parts of a condition whose values explain it: the
// arguments of its function or operator, but the constants.
func (b *builder) operands(e celast.Expr, seen map[string]bool) []operand {
	var out []operand
	add := func(x celast.Expr, keys bool) {
		if b.constant(x) {
			return
		}
		text, _, _ := b.text(x)
		if text == "" || seen[text] {
			return
		}
		seen[text] = true
		out = append(out, operand{id: x.ID(), text: text, keys: keys})
	}
	switch e.Kind() {
	case celast.CallKind:
		c := e.AsCall()
		switch c.FunctionName() {
		case operators.LogicalAnd, operators.LogicalOr, operators.LogicalNot:
			for _, a := range c.Args() {
				if access(a) {
					add(a, false)
				} else {
					out = append(out, b.operands(a, seen)...)
				}
			}
			return out
		}
		if c.IsMemberFunction() {
			add(c.Target(), false)
		}
		for _, a := range c.Args() {
			add(a, false)
		}
	case celast.ComprehensionKind:
		add(e.AsComprehension().IterRange(), false)
	case celast.SelectKind:
		if s := e.AsSelect(); s.IsTestOnly() {
			add(s.Operand(), true)
		} else {
			add(e, false)
		}
	default:
		add(e, false)
	}
	return out
}

// access says whether an expression reads a value, rather than tests one.
func access(e celast.Expr) bool {
	switch e.Kind() {
	case celast.IdentKind, celast.LiteralKind:
		return true
	case celast.SelectKind:
		return !e.AsSelect().IsTestOnly()
	case celast.CallKind:
		f := e.AsCall().FunctionName()
		return f == operators.Index || f == operators.OptIndex || f == operators.OptSelect
	}
	return false
}

// constant says whether an expression reads no variable.
func (b *builder) constant(e celast.Expr) bool {
	if e.Kind() == celast.IdentKind {
		return false
	}
	for _, c := range celast.NavigateExpr(b.ast, e).Children() {
		if !b.constant(c) {
			return false
		}
	}
	return true
}

// text is an expression as the filter writes it, and where; or, when its
// text can't be found there, as CEL prints it.
func (b *builder) text(e celast.Expr) (string, int, int) {
	printed, err := parser.Unparse(e, b.info)
	if start, end, ok := b.span(e); ok {
		s := string(b.src[start:end])
		if err != nil || b.prints(s, printed) {
			return s, start, end
		}
	}
	return printed, -1, -1
}

// prints says whether src parses to what CEL prints as printed.
func (b *builder) prints(src, printed string) bool {
	ast, iss := explainEnv.Parse(src)
	if iss.Err() != nil {
		return false
	}
	na := ast.NativeRep()
	s, err := parser.Unparse(na.Expr(), na.SourceInfo())
	return err == nil && s == printed
}

// span is where an expression is in the filter: CEL records where each
// node's operator or name is, so the span runs from the first to the last
// of its nodes, then over the field names and the closing brackets they
// leave out.
func (b *builder) span(e celast.Expr) (int, int, bool) {
	start, end := -1, -1
	var walk func(x celast.Expr)
	walk = func(x celast.Expr) {
		if r, ok := b.info.GetOffsetRange(x.ID()); ok && r.Stop > r.Start {
			if start < 0 || int(r.Start) < start {
				start = int(r.Start)
			}
			end = max(end, int(r.Stop))
		}
		for _, c := range celast.NavigateExpr(b.ast, x).Children() {
			walk(c)
		}
		if x.Kind() == celast.SelectKind && end >= 0 {
			end = b.afterField(end, x.AsSelect().FieldName())
		}
	}
	walk(e)
	if start < 0 || end > len(b.src) {
		return 0, 0, false
	}
	if call, ok := b.info.GetMacroCall(e.ID()); ok && call.Kind() == celast.CallKind && !call.AsCall().IsMemberFunction() {
		start = b.beforeCall(start, call.AsCall().FunctionName())
	}
	return start, b.closeBrackets(start, end), true
}

func (b *builder) skipSpace(i int) int {
	for i < len(b.src) && unicode.IsSpace(b.src[i]) {
		i++
	}
	return i
}

// afterField moves past ".field" or "?.field" when they follow i.
func (b *builder) afterField(i int, field string) int {
	j := b.skipSpace(i)
	switch {
	case j+1 < len(b.src) && b.src[j] == '?' && b.src[j+1] == '.':
		j += 2
	case j < len(b.src) && b.src[j] == '.':
		j++
	}
	j = b.skipSpace(j)
	f := []rune(field)
	if j+len(f) <= len(b.src) && string(b.src[j:j+len(f)]) == field {
		return j + len(f)
	}
	if q := []rune("`" + field + "`"); j+len(q) <= len(b.src) && string(b.src[j:j+len(q)]) == string(q) {
		return j + len(q)
	}
	return i
}

// beforeCall moves back over "name(" before i: a global macro such as has.
func (b *builder) beforeCall(i int, name string) int {
	j := i - 1
	for j >= 0 && unicode.IsSpace(b.src[j]) {
		j--
	}
	if j < 0 || b.src[j] != '(' {
		return i
	}
	j--
	for j >= 0 && unicode.IsSpace(b.src[j]) {
		j--
	}
	n := []rune(name)
	if j+1 >= len(n) && string(b.src[j+1-len(n):j+1]) == name {
		return j + 1 - len(n)
	}
	return i
}

// closeBrackets extends end over the brackets opened in [start, end).
func (b *builder) closeBrackets(start, end int) int {
	depth := 0
	var quote rune
	for i := start; i < end; i++ {
		c := b.src[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		}
	}
	for depth > 0 {
		j := b.skipSpace(end)
		if j >= len(b.src) || !strings.ContainsRune(")]}", b.src[j]) {
			break
		}
		end, depth = j+1, depth-1
	}
	return end
}
