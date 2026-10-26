package resources

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/damhau/awxnext/internal/api"
)

// SmartFilterSQL compiles an AWX smart inventory host_filter into a SQL
// predicate on hosts (alias t).
//
//	name__icontains=web and (groups__name=db or not enabled=false)
//	ansible_facts__ansible_distribution="Red Hat Enterprise Linux"
//	ansible_facts__ansible_all_ipv4_addresses[]="10.0.0.5"
//
// Precedence follows AWX: not > and > or. A bare word matches the host name.
func SmartFilterSQL(filter string, arg ArgFunc) (string, error) {
	toks, err := tokenize(filter)
	if err != nil {
		return "", err
	}
	if len(toks) == 0 {
		return "TRUE", nil
	}
	p := &sfParser{toks: toks, arg: arg, hosts: api.Lookup("hosts")}
	sql, err := p.or()
	if err != nil {
		return "", err
	}
	if p.pos != len(p.toks) {
		return "", fmt.Errorf("unexpected %q", p.toks[p.pos])
	}
	return sql, nil
}

func tokenize(s string) ([]string, error) {
	var out []string
	i := 0
	for i < len(s) {
		c := rune(s[i])
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '(' || c == ')':
			out = append(out, string(c))
			i++
		default:
			var b strings.Builder
			for i < len(s) && !unicode.IsSpace(rune(s[i])) && s[i] != '(' && s[i] != ')' {
				if s[i] == '"' || s[i] == '\'' {
					q := s[i]
					j := i + 1
					for j < len(s) && s[j] != q {
						if s[j] == '\\' && j+1 < len(s) {
							j++
						}
						j++
					}
					if j >= len(s) {
						return nil, fmt.Errorf("unterminated quote")
					}
					b.WriteString(s[i : j+1])
					i = j + 1
					continue
				}
				b.WriteByte(s[i])
				i++
			}
			out = append(out, b.String())
		}
	}
	return out, nil
}

type sfParser struct {
	toks  []string
	pos   int
	arg   ArgFunc
	hosts *Resource
}

func (p *sfParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *sfParser) or() (string, error) {
	left, err := p.and()
	if err != nil {
		return "", err
	}
	for strings.EqualFold(p.peek(), "or") {
		p.pos++
		right, err := p.and()
		if err != nil {
			return "", err
		}
		left = "(" + left + ") OR (" + right + ")"
	}
	return left, nil
}

func (p *sfParser) and() (string, error) {
	left, err := p.not()
	if err != nil {
		return "", err
	}
	for strings.EqualFold(p.peek(), "and") {
		p.pos++
		right, err := p.not()
		if err != nil {
			return "", err
		}
		left = "(" + left + ") AND (" + right + ")"
	}
	return left, nil
}

func (p *sfParser) not() (string, error) {
	if strings.EqualFold(p.peek(), "not") {
		p.pos++
		inner, err := p.not()
		if err != nil {
			return "", err
		}
		return "NOT (" + inner + ")", nil
	}
	return p.atom()
}

func (p *sfParser) atom() (string, error) {
	t := p.peek()
	switch t {
	case "":
		return "", fmt.Errorf("unexpected end of filter")
	case "(":
		p.pos++
		inner, err := p.or()
		if err != nil {
			return "", err
		}
		if p.peek() != ")" {
			return "", fmt.Errorf("missing )")
		}
		p.pos++
		return inner, nil
	case ")":
		return "", fmt.Errorf("unexpected )")
	}
	p.pos++
	key, value, ok := strings.Cut(t, "=")
	if !ok {
		return api.FilterSQL(p.hosts, p.arg, "name__icontains", unquote(t))
	}
	value = unquote(value)
	if strings.HasPrefix(key, "ansible_facts__") {
		return p.facts(strings.TrimPrefix(key, "ansible_facts__"), value)
	}
	return api.FilterSQL(p.hosts, p.arg, key, value)
}

// facts compiles ansible_facts__a__b[]=v and ansible_facts__a__b__lookup=v.
func (p *sfParser) facts(path, value string) (string, error) {
	parts := strings.Split(path, "__")
	contains := false
	if last := parts[len(parts)-1]; strings.HasSuffix(last, "[]") {
		contains = true
		parts[len(parts)-1] = strings.TrimSuffix(last, "[]")
	}
	if contains {
		return fmt.Sprintf("(t.ansible_facts #> %s::text[]) @> %s::jsonb", p.arg("{"+strings.Join(parts, ",")+"}"), p.arg(api.JSONText([]any{jsonScalar(value)}))), nil
	}
	return api.FilterSQL(p.hosts, p.arg, "ansible_facts__"+strings.Join(parts, "__"), value)
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`)
	}
	return s
}

// jsonScalar interprets true/false/null/numbers like AWX does for fact comparisons.
func jsonScalar(s string) any {
	switch s {
	case "true", "True":
		return true
	case "false", "False":
		return false
	case "null", "None":
		return nil
	}
	return s
}
