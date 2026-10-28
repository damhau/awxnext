package api

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Named URLs name an object by what identifies it instead of its id, as
// AWX's named URL graph does: the object's fields joined by "+", then the
// segments of the objects it belongs to, joined by "++"
// (/api/v2/hosts/web1++prod++Default/). A "+" inside a value is written
// "[+]"; a missing parent is an empty segment.

// NamedFK is a foreign key a named URL goes through.
type NamedFK struct {
	Field  string // the API field (organization)
	Column string // its column (organization_id)
	Target string // the resource it points to (organizations)
}

type namedNode struct {
	fields []string
	fks    []NamedFK
}

var namedGraph = map[string]*namedNode{}

// SetNamedURL declares the named URL of a resource: the columns that
// identify its objects, then the foreign keys that complete them, in AWX's
// order (foreign keys sorted by name).
func SetNamedURL(res string, fields []string, fks ...NamedFK) {
	namedGraph[res] = &namedNode{fields: fields, fks: fks}
}

// NamedURLFormats is AWX's NAMED_URL_FORMATS setting.
func NamedURLFormats() map[string]any {
	out := map[string]any{}
	for res := range namedGraph {
		var parts []string
		var walk func(res, prefix string)
		walk = func(res, prefix string) {
			n := namedGraph[res]
			var fs []string
			for _, f := range n.fields {
				fs = append(fs, "<"+prefix+f+">")
			}
			parts = append(parts, strings.Join(fs, "+"))
			for _, fk := range n.fks {
				walk(fk.Target, fk.Field+".")
			}
		}
		walk(res, "")
		out[res] = strings.Join(parts, "++")
	}
	return out
}

// NamedURLGraphNodes is AWX's NAMED_URL_GRAPH_NODES setting.
func NamedURLGraphNodes() map[string]any {
	out := map[string]any{}
	for res, n := range namedGraph {
		adj := [][]string{}
		for _, fk := range n.fks {
			adj = append(adj, []string{fk.Field, fk.Target})
		}
		out[res] = map[string]any{"fields": n.fields, "adj_list": adj}
	}
	return out
}

// encodeNamed writes a value as AWX does in a named URL: the URL-reserved
// characters escaped, and "+" as "[+]".
func encodeNamed(v any) string {
	s := fmt.Sprint(v)
	if v == nil {
		s = ""
	}
	for _, ch := range ";/?:@=&[]" {
		s = strings.ReplaceAll(s, string(ch), url.PathEscape(string(ch)))
	}
	return strings.ReplaceAll(s, "+", "[+]")
}

// NamedURL is the named URL of an object, or "" if its resource has none.
func NamedURL(c *Ctx, res *Resource, id int64) string {
	if namedGraph[res.Name] == nil {
		return ""
	}
	var segs []string
	var walk func(res *Resource, id int64) error
	walk = func(res *Resource, id int64) error {
		n := namedGraph[res.Name]
		cols := append([]string{}, n.fields...)
		for _, fk := range n.fks {
			cols = append(cols, fk.Column)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		quoted := make([]string, len(cols))
		for i, col := range cols {
			quoted[i] = "t." + quoteIdent(col)
		}
		if err := c.DB().QueryRow(c, "SELECT "+strings.Join(quoted, ", ")+" FROM "+res.Table+" t WHERE t.id = $1", id).Scan(ptrs...); err != nil {
			return err
		}
		var fs []string
		for i := range n.fields {
			fs = append(fs, encodeNamed(vals[i]))
		}
		segs = append(segs, strings.Join(fs, "+"))
		for i, fk := range n.fks {
			pid, ok := ID(vals[len(n.fields)+i])
			if !ok {
				segs = append(segs, "")
				continue
			}
			if err := walk(registry[fk.Target], pid); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(res, id); err != nil {
		return ""
	}
	return "/api/v2/" + res.Name + "/" + strings.Join(segs, "++") + "/"
}

// namedURLWhere turns a named URL into conditions on alias t, as AWX's
// populate_named_url_query_kwargs: ok is false when it does not fit the
// resource's format. An empty parent segment adds no condition.
func namedURLWhere(res *Resource, seg string, arg ArgFunc) (string, bool) {
	segs := strings.Split(strings.ReplaceAll(seg, "[+]", "%2B"), "++")
	idx := 0
	alias := 0
	var build func(res *Resource, a string) (string, bool)
	build = func(res *Resource, a string) (string, bool) {
		n := namedGraph[res.Name]
		if idx >= len(segs) {
			return "", false
		}
		parts := strings.Split(segs[idx], "+")
		if len(parts) != len(n.fields) {
			return "", false
		}
		idx++
		var conds []string
		if res.Discriminator {
			conds = append(conds, a+".type = "+arg(res.Type))
		}
		for i, f := range n.fields {
			v, err := url.PathUnescape(parts[i])
			if err != nil {
				return "", false
			}
			conds = append(conds, a+"."+quoteIdent(f)+" = "+arg(v))
		}
		for _, fk := range n.fks {
			if idx >= len(segs) {
				return "", false
			}
			if segs[idx] == "" {
				idx++
				continue
			}
			target := registry[fk.Target]
			alias++
			b := fmt.Sprintf("n%d", alias)
			sub, ok := build(target, b)
			if !ok {
				return "", false
			}
			conds = append(conds, fmt.Sprintf("%s.%s IN (SELECT %s.id FROM %s %s WHERE %s)", a, quoteIdent(fk.Column), b, target.Table, b, sub))
		}
		return strings.Join(conds, " AND "), true
	}
	where, ok := build(res, "t")
	return where, ok && idx == len(segs)
}

// NamedURLResources lists the resources that have a named URL, sorted.
func NamedURLResources() []string {
	out := make([]string, 0, len(namedGraph))
	for res := range namedGraph {
		out = append(out, res)
	}
	sort.Strings(out)
	return out
}
