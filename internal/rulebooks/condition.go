package rulebooks

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// The condition language of ansible-rulebook 1.3 (its condition_parser.py),
// parsed into a tree the translation to CEL walks.

type node interface{}

type literal struct{ v any } // string, int64, float64, bool or nil

type listNode struct{ items []node }

// pathNode is event.a.b, events.m_0.x, fact.y, vars.z: a root and the keys
// and indexes after it.
type pathNode struct {
	root  string
	parts []any // string or int64
	text  string
}

type unaryNode struct {
	op string // "not"
	x  node
}

type binaryNode struct {
	op   string
	l, r node
}

// isNode is `x is [not] defined`.
type isNode struct {
	x   node
	not bool
}

// searchNode is `x is [not] match|search|regex(pattern, ignorecase=true)`.
type searchNode struct {
	x       node
	kind    string
	pattern node
	options map[string]any
	not     bool
}

// selectNode is `x is [not] select(op, value)`.
type selectNode struct {
	x   node
	op  string
	v   node
	not bool
}

// selectAttrNode is `x is [not] selectattr(key, op, value)`.
type selectAttrNode struct {
	x   node
	key string
	op  string
	v   node
	not bool
}

// assignNode is `events.name << condition`.
type assignNode struct {
	name string
	x    node
}

type token struct {
	kind string // str, num, word, op, end
	text string
	val  any
	pos  int
}

var operators = []string{"<<", "==", "!=", ">=", "<=", "<", ">", "+", "-", "*", "/", "(", ")", "[", "]", ",", ".", "="}

func lex(s string) ([]token, error) {
	var out []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"' || c == '\'':
			j := i + 1
			var b strings.Builder
			for j < len(s) && s[j] != c {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("the string at %d is not closed", i+1)
			}
			out = append(out, token{kind: "str", text: s[i : j+1], val: b.String(), pos: i})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && (isAlnum(s[j]) || s[j] == '.' && j+1 < len(s) && s[j+1] >= '0' && s[j+1] <= '9') {
				j++
			}
			if j+1 < len(s) && (s[j-1] == 'e' || s[j-1] == 'E') && (s[j] == '+' || s[j] == '-') && s[j+1] >= '0' && s[j+1] <= '9' {
				j += 2
				for j < len(s) && s[j] >= '0' && s[j] <= '9' {
					j++
				}
			}
			text := s[i:j]
			if n, err := strconv.ParseInt(text, 10, 64); err == nil {
				out = append(out, token{kind: "num", text: text, val: n, pos: i})
			} else if f, err := strconv.ParseFloat(text, 64); err == nil {
				out = append(out, token{kind: "num", text: text, val: f, pos: i})
			} else {
				// A word starting with a digit is a plain string.
				out = append(out, token{kind: "word", text: text, pos: i})
			}
			i = j
		case isAlnum(c) || c == '_':
			j := i
			for j < len(s) && (isAlnum(s[j]) || s[j] == '_') {
				j++
			}
			out = append(out, token{kind: "word", text: s[i:j], pos: i})
			i = j
		default:
			matched := false
			for _, op := range operators {
				if strings.HasPrefix(s[i:], op) {
					out = append(out, token{kind: "op", text: op, pos: i})
					i += len(op)
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("unexpected %q at %d", string(c), i+1)
			}
		}
	}
	return append(out, token{kind: "end", pos: len(s)}), nil
}

func isAlnum(c byte) bool {
	return c < 128 && (unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)))
}

type parser struct {
	toks []token
	i    int
	src  string
}

// parseCondition parses one condition of a rule.
func parseCondition(s string) (node, error) {
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, src: s}
	n, err := p.assignment()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != "end" {
		return nil, fmt.Errorf("unexpected %q at %d", t.text, t.pos+1)
	}
	return n, nil
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) peekAt(k int) token {
	if p.i+k < len(p.toks) {
		return p.toks[p.i+k]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != "end" {
		p.i++
	}
	return t
}

func (p *parser) isOp(text string) bool {
	t := p.peek()
	return t.kind == "op" && t.text == text
}

func (p *parser) isWord(text string) bool {
	t := p.peek()
	return t.kind == "word" && strings.EqualFold(t.text, text)
}

func (p *parser) expect(text string) error {
	if !p.isOp(text) {
		t := p.peek()
		if t.kind == "end" {
			return fmt.Errorf("expected %q at the end", text)
		}
		return fmt.Errorf("expected %q at %d, found %q", text, t.pos+1, t.text)
	}
	p.next()
	return nil
}

func (p *parser) assignment() (node, error) {
	l, err := p.logical()
	if err != nil {
		return nil, err
	}
	for p.isOp("<<") {
		p.next()
		r, err := p.logical()
		if err != nil {
			return nil, err
		}
		pn, ok := l.(*pathNode)
		if !ok || pn.root != "events" && pn.root != "facts" || len(pn.parts) != 1 {
			return nil, errors.New("an assignment names its event as events.name << condition")
		}
		name, _ := pn.parts[0].(string)
		l = &assignNode{name: name, x: r}
	}
	return l, nil
}

// logical: and and or have the same precedence and group from the left, as
// in ansible-rulebook.
func (p *parser) logical() (node, error) {
	l, err := p.negation()
	if err != nil {
		return nil, err
	}
	for p.isWord("and") || p.isWord("or") {
		op := strings.ToLower(p.next().text)
		r, err := p.negation()
		if err != nil {
			return nil, err
		}
		l = &binaryNode{op: op, l: l, r: r}
	}
	return l, nil
}

func (p *parser) negation() (node, error) {
	if p.isWord("not") && !(p.peekAt(1).kind == "word" && (strings.EqualFold(p.peekAt(1).text, "in") || strings.EqualFold(p.peekAt(1).text, "contains"))) {
		p.next()
		x, err := p.negation()
		if err != nil {
			return nil, err
		}
		return &unaryNode{op: "not", x: x}, nil
	}
	return p.membership()
}

func (p *parser) membership() (node, error) {
	l, err := p.is()
	if err != nil {
		return nil, err
	}
	for {
		var op string
		switch {
		case p.isWord("in"):
			op = "in"
		case p.isWord("contains"):
			op = "contains"
		case p.isWord("not") && p.peekAt(1).kind == "word" && strings.EqualFold(p.peekAt(1).text, "in"):
			op = "not in"
		case p.isWord("not") && p.peekAt(1).kind == "word" && strings.EqualFold(p.peekAt(1).text, "contains"):
			op = "not contains"
		default:
			return l, nil
		}
		p.next()
		if strings.HasPrefix(op, "not ") {
			p.next()
		}
		r, err := p.is()
		if err != nil {
			return nil, err
		}
		l = &binaryNode{op: op, l: l, r: r}
	}
}

func (p *parser) is() (node, error) {
	l, err := p.binary(0)
	if err != nil {
		return nil, err
	}
	for p.isWord("is") {
		p.next()
		not := false
		if p.isWord("not") {
			p.next()
			not = true
		}
		t := p.peek()
		if t.kind != "word" {
			return nil, fmt.Errorf("expected defined, match, search, regex, select or selectattr after is, at %d", t.pos+1)
		}
		switch t.text {
		case "defined":
			p.next()
			l = &isNode{x: l, not: not}
		case "match", "search", "regex":
			p.next()
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			sn := &searchNode{x: l, kind: t.text, not: not, options: map[string]any{}}
			for i, a := range args {
				if kv, ok := a.(kwArg); ok {
					sn.options[kv.name] = kv.v
				} else if i == 0 {
					sn.pattern = a
				} else {
					return nil, fmt.Errorf("%s takes a pattern and options such as ignorecase=true", t.text)
				}
			}
			if sn.pattern == nil {
				return nil, fmt.Errorf("%s needs a pattern", t.text)
			}
			l = sn
		case "select":
			p.next()
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			if len(args) != 2 {
				return nil, errors.New("select takes an operator and a value")
			}
			op, ok := stringLit(args[0])
			if !ok || !validSelectOp(op, false) {
				return nil, fmt.Errorf("select's operator %v is not supported", args[0])
			}
			l = &selectNode{x: l, op: op, v: args[1], not: not}
		case "selectattr":
			p.next()
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			if len(args) != 3 {
				return nil, errors.New("selectattr takes a key, an operator and a value")
			}
			key, ok := stringLit(args[0])
			if !ok {
				return nil, errors.New("selectattr's key must be a string")
			}
			op, ok := stringLit(args[1])
			if !ok || !validSelectOp(op, true) {
				return nil, fmt.Errorf("selectattr's operator %v is not supported", args[1])
			}
			l = &selectAttrNode{x: l, key: key, op: op, v: args[2], not: not}
		default:
			return nil, fmt.Errorf("%q after is is not supported: expected defined, match, search, regex, select or selectattr", t.text)
		}
	}
	return l, nil
}

type kwArg struct {
	name string
	v    any
}

func (p *parser) args() ([]node, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	var out []node
	for !p.isOp(")") {
		if len(out) > 0 {
			if err := p.expect(","); err != nil {
				return nil, err
			}
		}
		if t := p.peek(); t.kind == "word" && p.peekAt(1).kind == "op" && p.peekAt(1).text == "=" {
			p.next()
			p.next()
			v, err := p.atom()
			if err != nil {
				return nil, err
			}
			lit, ok := v.(*literal)
			if !ok {
				return nil, fmt.Errorf("option %s takes a value", t.text)
			}
			out = append(out, kwArg{name: t.text, v: lit.v})
			continue
		}
		a, err := p.atom()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	p.next()
	return out, nil
}

// indexAhead says whether [ starts an index, ["key"] or [0], rather than a
// list.
func (p *parser) indexAhead() bool {
	k := 1
	if t := p.peekAt(k); t.kind == "op" && t.text == "-" {
		k++
	}
	t := p.peekAt(k)
	if t.kind != "str" && t.kind != "num" {
		return false
	}
	c := p.peekAt(k + 1)
	return c.kind == "op" && c.text == "]"
}

func validSelectOp(op string, attr bool) bool {
	switch op {
	case "==", "!=", ">", ">=", "<", "<=", "regex", "search", "match":
		return true
	case "in", "not in", "contains", "not contains":
		return attr
	}
	return false
}

func stringLit(n node) (string, bool) {
	if l, ok := n.(*literal); ok {
		s, ok := l.v.(string)
		return s, ok
	}
	return "", false
}

// The binary operators above is, from the loosest: ==, !=, < and >, <=,
// >=, + and -, * and /, each its own level as in ansible-rulebook.
var levels = [][]string{{"=="}, {"!="}, {"<", ">"}, {"<="}, {">="}, {"+", "-"}, {"*", "/"}}

func (p *parser) binary(level int) (node, error) {
	if level == len(levels) {
		return p.atom()
	}
	l, err := p.binary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != "op" || !contains(levels[level], t.text) {
			return l, nil
		}
		p.next()
		r, err := p.binary(level + 1)
		if err != nil {
			return nil, err
		}
		l = &binaryNode{op: t.text, l: l, r: r}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var prefixes = map[string]bool{"event": true, "events": true, "fact": true, "facts": true, "vars": true}

func (p *parser) atom() (node, error) {
	t := p.peek()
	switch t.kind {
	case "end":
		return nil, errors.New("the condition ends too early")
	case "str":
		p.next()
		return &literal{v: t.val}, nil
	case "num":
		p.next()
		return &literal{v: t.val}, nil
	case "op":
		switch t.text {
		case "(":
			p.next()
			n, err := p.assignment()
			if err != nil {
				return nil, err
			}
			return n, p.expect(")")
		case "[":
			p.next()
			l := &listNode{}
			for !p.isOp("]") {
				if len(l.items) > 0 {
					if err := p.expect(","); err != nil {
						return nil, err
					}
				}
				a, err := p.atom()
				if err != nil {
					return nil, err
				}
				l.items = append(l.items, a)
			}
			p.next()
			return l, nil
		case "-":
			if n := p.peekAt(1); n.kind == "num" {
				p.next()
				p.next()
				switch v := n.val.(type) {
				case int64:
					return &literal{v: -v}, nil
				case float64:
					return &literal{v: -v}, nil
				}
			}
		}
		return nil, fmt.Errorf("unexpected %q at %d", t.text, t.pos+1)
	}
	p.next()
	switch t.text {
	case "true", "True":
		return &literal{v: true}, nil
	case "false", "False":
		return &literal{v: false}, nil
	case "null":
		return &literal{v: nil}, nil
	}
	if !prefixes[t.text] {
		// ansible-rulebook reads any other word as a string.
		return &literal{v: t.text}, nil
	}
	pn := &pathNode{root: t.text}
	start := t.pos
	end := t.pos + len(t.text)
	for {
		switch {
		case p.isOp(".") && p.peekAt(1).kind == "word":
			p.next()
			w := p.next()
			pn.parts = append(pn.parts, w.text)
			end = w.pos + len(w.text)
		case p.isOp("[") && p.indexAhead():
			p.next()
			k, err := p.atom()
			if err != nil {
				return nil, err
			}
			lit, ok := k.(*literal)
			if !ok {
				return nil, errors.New("an index is a string or an integer")
			}
			switch v := lit.v.(type) {
			case string, int64:
				pn.parts = append(pn.parts, v)
			default:
				return nil, errors.New("an index is a string or an integer")
			}
			cl := p.peek()
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			end = cl.pos + 1
		default:
			pn.text = p.src[start:end]
			return pn, nil
		}
	}
}
