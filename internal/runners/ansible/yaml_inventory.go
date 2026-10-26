package ansible

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// YAMLInventory reads an Ansible YAML inventory (the format of the yaml
// inventory plugin) that has already been decoded into a map:
//
//	all:
//	  vars: {ntp: pool.ntp.org}
//	  hosts:
//	    db1: {ansible_host: 10.0.0.5}
//	  children:
//	    web:
//	      hosts:
//	        web[01:03]:
func YAMLInventory(content map[string]any) (*InventoryData, error) {
	d := &InventoryData{AllVars: map[string]any{}, Groups: map[string]*InventoryGroup{}, HostVars: map[string]map[string]any{}}
	seen := map[string]bool{}
	var walk func(name string, v any) error
	walk = func(name string, v any) error {
		if v == nil {
			v = map[string]any{}
		}
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("group %q must be a mapping", name)
		}
		var g *InventoryGroup
		if name != "all" && name != "ungrouped" {
			if g = d.Groups[name]; g == nil {
				g = &InventoryGroup{Vars: map[string]any{}}
				d.Groups[name] = g
			}
		}
		for key := range m {
			if key != "vars" && key != "hosts" && key != "children" {
				return fmt.Errorf("group %q: unknown key %q (expected vars, hosts or children)", name, key)
			}
		}
		if vars, ok := m["vars"].(map[string]any); ok {
			target := d.AllVars
			if g != nil {
				target = g.Vars
			}
			for k, x := range vars {
				target[k] = x
			}
		} else if m["vars"] != nil {
			return fmt.Errorf("group %q: vars must be a mapping", name)
		}
		if hosts, ok := m["hosts"].(map[string]any); ok {
			patterns := make([]string, 0, len(hosts))
			for p := range hosts {
				patterns = append(patterns, p)
			}
			sort.Strings(patterns)
			for _, p := range patterns {
				names, err := expandHostPattern(p)
				if err != nil {
					return fmt.Errorf("group %q: %w", name, err)
				}
				hv, _ := hosts[p].(map[string]any)
				if hosts[p] != nil && hv == nil {
					return fmt.Errorf("host %q: variables must be a mapping", p)
				}
				for _, h := range names {
					if !seen[h] {
						seen[h] = true
						d.Hosts = append(d.Hosts, h)
					}
					if d.HostVars[h] == nil {
						d.HostVars[h] = map[string]any{}
					}
					for k, x := range hv {
						d.HostVars[h][k] = x
					}
					if g != nil {
						g.Hosts = append(g.Hosts, h)
					}
				}
			}
		} else if m["hosts"] != nil {
			return fmt.Errorf("group %q: hosts must be a mapping", name)
		}
		if children, ok := m["children"].(map[string]any); ok {
			names := make([]string, 0, len(children))
			for c := range children {
				names = append(names, c)
			}
			sort.Strings(names)
			for _, c := range names {
				if c == "all" {
					return fmt.Errorf("group %q: all cannot be a child group", name)
				}
				if g != nil && c != "ungrouped" && !contains(g.Children, c) {
					g.Children = append(g.Children, c)
				}
				if err := walk(c, children[c]); err != nil {
					return err
				}
			}
		} else if m["children"] != nil {
			return fmt.Errorf("group %q: children must be a mapping", name)
		}
		return nil
	}
	names := make([]string, 0, len(content))
	for k := range content {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := walk(n, content[n]); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var hostRange = regexp.MustCompile(`\[([0-9a-zA-Z]+):([0-9a-zA-Z]+)(?::([0-9]+))?\]`)

// expandHostPattern expands Ansible host ranges such as web[01:10] or db-[a:c].
func expandHostPattern(p string) ([]string, error) {
	loc := hostRange.FindStringSubmatchIndex(p)
	if loc == nil {
		return []string{p}, nil
	}
	head, tail := p[:loc[0]], p[loc[1]:]
	from, to := p[loc[2]:loc[3]], p[loc[4]:loc[5]]
	step := 1
	if loc[6] >= 0 {
		step, _ = strconv.Atoi(p[loc[6]:loc[7]])
		if step < 1 {
			return nil, fmt.Errorf("host range %q: invalid step", p)
		}
	}
	rest, err := expandHostPattern(tail)
	if err != nil {
		return nil, err
	}
	var items []string
	if a, err1 := strconv.Atoi(from); err1 == nil {
		b, err2 := strconv.Atoi(to)
		if err2 != nil || b < a {
			return nil, fmt.Errorf("host range %q: invalid bounds", p)
		}
		width := 0
		if strings.HasPrefix(from, "0") && len(from) > 1 {
			width = len(from)
		}
		for i := a; i <= b; i += step {
			items = append(items, fmt.Sprintf("%0*d", width, i))
		}
	} else {
		if len(from) != 1 || len(to) != 1 || from[0] > to[0] {
			return nil, fmt.Errorf("host range %q: invalid bounds", p)
		}
		for c := from[0]; c <= to[0]; c += byte(step) {
			items = append(items, string(c))
			if int(c)+step > 255 {
				break
			}
		}
	}
	if len(items)*len(rest) > 100000 {
		return nil, fmt.Errorf("host range %q expands to too many hosts", p)
	}
	out := make([]string, 0, len(items)*len(rest))
	for _, it := range items {
		for _, r := range rest {
			out = append(out, head+it+r)
		}
	}
	return out, nil
}
