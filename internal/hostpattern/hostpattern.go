// Package hostpattern resolves an Ansible host pattern, such as a job's
// limit, against an inventory, as ansible-playbook's --limit does: terms
// separated by commas (or colons), groups with their descendants, host
// names, shell wildcards, ~regular expressions, &intersections and
// !exclusions. Subscripts (web[0:2]) and @files are not resolved: a term
// using one selects more hosts than Ansible would, never fewer.
package hostpattern

import (
	"regexp"
	"slices"
	"strings"
)

// Inventory is what a pattern is resolved against: its hosts' names, and
// its groups' hosts and child groups.
type Inventory struct {
	Hosts    []string
	Groups   map[string][]string
	Children map[string][]string
}

// Set is a set of host names.
type Set map[string]bool

// Resolve returns the hosts of inv that pattern selects. approx is true when
// the result may hold hosts Ansible would not select. An empty pattern
// selects every host.
func Resolve(inv *Inventory, pattern string) (hosts Set, approx bool) {
	terms := Split(pattern)
	if len(terms) == 0 {
		return inv.all(), false
	}
	var regular, and, not []string
	for _, t := range terms {
		switch t[0] {
		case '!':
			not = append(not, t[1:])
		case '&':
			and = append(and, t[1:])
		default:
			regular = append(regular, t)
		}
	}
	if len(regular) == 0 {
		regular = []string{"all"}
	}
	hosts = Set{}
	for _, t := range regular {
		m, a := inv.match(t)
		approx = approx || a
		for h := range m {
			hosts[h] = true
		}
	}
	for _, t := range and {
		m, a := inv.match(t)
		approx = approx || a
		for h := range hosts {
			if !m[h] {
				delete(hosts, h)
			}
		}
	}
	for _, t := range not {
		m, a := inv.match(t)
		if a {
			// Excluding more than Ansible would could miss a host.
			approx = true
			continue
		}
		for h := range m {
			delete(hosts, h)
		}
	}
	return hosts, approx
}

// Split splits a pattern into its terms, as Ansible does: on commas, or,
// without any, on colons outside brackets.
func Split(pattern string) []string {
	var parts []string
	if strings.Contains(pattern, ",") {
		parts = strings.Split(pattern, ",")
	} else {
		depth, start := 0, 0
		for i, r := range pattern {
			switch r {
			case '[':
				depth++
			case ']':
				if depth > 0 {
					depth--
				}
			case ':':
				if depth == 0 {
					parts = append(parts, pattern[start:i])
					start = i + 1
				}
			}
		}
		parts = append(parts, pattern[start:])
	}
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" && p != "!" && p != "&" {
			out = append(out, p)
		}
	}
	return out
}

func (inv *Inventory) all() Set {
	s := Set{}
	for _, h := range inv.Hosts {
		s[h] = true
	}
	return s
}

// GroupHosts returns the hosts of a group and of its descendants. "all" is
// every host, "ungrouped" the hosts in no group.
func (inv *Inventory) GroupHosts(name string) Set {
	switch name {
	case "all":
		return inv.all()
	case "ungrouped":
		if _, ok := inv.Groups[name]; !ok {
			grouped := Set{}
			for _, hs := range inv.Groups {
				for _, h := range hs {
					grouped[h] = true
				}
			}
			s := Set{}
			for _, h := range inv.Hosts {
				if !grouped[h] {
					s[h] = true
				}
			}
			return s
		}
	}
	known := inv.all()
	s := Set{}
	seen := map[string]bool{}
	var walk func(g string)
	walk = func(g string) {
		if seen[g] {
			return
		}
		seen[g] = true
		for _, h := range inv.Groups[g] {
			if known[h] {
				s[h] = true
			}
		}
		for _, c := range inv.Children[g] {
			walk(c)
		}
	}
	walk(name)
	return s
}

func (inv *Inventory) groupNames() []string {
	names := []string{"all", "ungrouped"}
	for g := range inv.Groups {
		if g != "all" && g != "ungrouped" {
			names = append(names, g)
		}
	}
	return names
}

var subscriptRe = regexp.MustCompile(`^(.+)\[(?:-?[0-9]+|[0-9]+[:-][0-9]*)\]$`)

// match returns the hosts one term selects: the hosts of the groups it
// matches and, when it matches no group or is a wildcard or a regular
// expression, the hosts it matches.
func (inv *Inventory) match(term string) (Set, bool) {
	approx := false
	if term[0] == '@' {
		return inv.all(), true
	}
	if term[0] != '~' {
		if m := subscriptRe.FindStringSubmatch(term); m != nil {
			term, approx = m[1], true
		}
	}
	known := inv.all()
	if known[term] {
		return Set{term: true}, approx
	}
	re, err := compile(term)
	if err != nil {
		return inv.all(), true
	}
	out := Set{}
	groups := false
	for _, g := range inv.groupNames() {
		if re.MatchString(g) {
			groups = true
			for h := range inv.GroupHosts(g) {
				out[h] = true
			}
		}
	}
	if !groups || term[0] == '~' || strings.ContainsAny(term, ".?*[") {
		for _, h := range inv.Hosts {
			if re.MatchString(h) {
				out[h] = true
			}
		}
	}
	return out, approx
}

// compile turns a term into the expression Ansible matches names with: a
// ~regular expression anchored at the start, or a shell wildcard.
func compile(term string) (*regexp.Regexp, error) {
	if term[0] == '~' {
		return regexp.Compile(`^(?:` + term[1:] + `)`)
	}
	var b strings.Builder
	b.WriteString(`^(?s:`)
	rs := []rune(term)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '*':
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		case '[':
			j := slices.Index(rs[i+1:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			set := string(rs[i+1 : i+1+j])
			if strings.HasPrefix(set, "!") {
				set = "^" + set[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(set, `\`, `\\`) + "]")
			i += j + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`)$`)
	return regexp.Compile(b.String())
}
