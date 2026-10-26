package api

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// query accumulates SQL arguments and WHERE clauses.
type query struct {
	args  []any
	where []string
	alias int
}

func (q *query) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

func (q *query) nextAlias() string {
	q.alias++
	return "t" + strconv.Itoa(q.alias)
}

func (q *query) whereSQL() string {
	if len(q.where) == 0 {
		return ""
	}
	return " WHERE (" + strings.Join(q.where, ") AND (") + ")"
}

// Relation is a reverse or many-to-many relation usable in filters (e.g. hosts?groups__name=web).
type Relation struct {
	Target string
	// Join returns a SQL predicate linking the outer row (alias a) to the target row (alias b).
	Join func(a, b string) string
}

var lookups = map[string]bool{
	"exact": true, "iexact": true, "contains": true, "icontains": true,
	"startswith": true, "istartswith": true, "endswith": true, "iendswith": true,
	"in": true, "isnull": true, "gt": true, "gte": true, "lt": true, "lte": true,
	"regex": true, "iregex": true,
}

// Query parameters that are not field filters.
var reservedParams = map[string]bool{
	"page": true, "page_size": true, "order_by": true, "order": true, "search": true,
	"format": true, "role_level": true, "count_disabled": true, "fields": true,
	"host_filter": true, "not_in": true,
}

// applyFilters translates query-string filters into WHERE clauses.
func (q *query) applyFilters(c *Ctx, res *Resource, params url.Values) error {
	var ors []string
	for key, values := range params {
		if reservedParams[key] {
			continue
		}
		negate, isOr := false, false
		k := key
		for {
			switch {
			case strings.HasPrefix(k, "not__"):
				negate = !negate
				k = k[5:]
				continue
			case strings.HasPrefix(k, "or__"):
				isOr = true
				k = k[4:]
				continue
			case strings.HasPrefix(k, "chain__"):
				k = k[7:]
				continue
			}
			break
		}
		parts := strings.Split(k, "__")
		lookup := "exact"
		if len(parts) > 1 && lookups[parts[len(parts)-1]] {
			lookup = parts[len(parts)-1]
			parts = parts[:len(parts)-1]
		}
		for _, v := range values {
			pred, err := q.path(res, "t", parts, lookup, v)
			if err != nil {
				return err
			}
			if negate {
				pred = "NOT (" + pred + ")"
			}
			if isOr {
				ors = append(ors, pred)
			} else {
				q.where = append(q.where, pred)
			}
		}
	}
	if len(ors) > 0 {
		q.where = append(q.where, strings.Join(ors, " OR "))
	}
	if s := params.Get("search"); s != "" {
		fields := res.SearchFields
		if len(fields) == 0 && !res.NoName {
			fields = []string{"name", "description"}
		}
		for _, term := range strings.Split(s, ",") {
			term = strings.TrimSpace(term)
			if term == "" || len(fields) == 0 {
				continue
			}
			var preds []string
			p := q.arg("%" + likeEscape(term) + "%")
			for _, f := range fields {
				preds = append(preds, fmt.Sprintf("t.%s::text ILIKE %s", quoteIdent(res.field(f).column()), p))
			}
			q.where = append(q.where, strings.Join(preds, " OR "))
		}
	}
	return nil
}

func (q *query) path(res *Resource, alias string, parts []string, lookup, raw string) (string, error) {
	name := parts[0]
	if f := res.field(name); f != nil && !f.NoFilter {
		col := alias + "." + quoteIdent(f.column())
		if len(parts) == 1 {
			return q.leaf(col, f, lookup, raw)
		}
		if f.Type != TFK {
			return "", filterErr("'%s' is not a relation", name)
		}
		target := registry[f.Ref]
		if target == nil {
			return "", filterErr("unknown relation '%s'", name)
		}
		inner := q.nextAlias()
		pred, err := q.path(target, inner, parts[1:], lookup, raw)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s IN (SELECT %s.id FROM %s %s WHERE %s)", col, inner, target.Table, inner, pred), nil
	}
	if rel, ok := res.Relations[name]; ok {
		target := registry[rel.Target]
		inner := q.nextAlias()
		var pred string
		var err error
		if len(parts) == 1 {
			// Filtering a relation directly compares ids.
			pred, err = q.leaf(inner+".id", &Field{Name: "id", Type: TInt}, lookup, raw)
		} else {
			pred, err = q.path(target, inner, parts[1:], lookup, raw)
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("EXISTS (SELECT 1 FROM %s %s WHERE (%s) AND (%s))", target.Table, inner, rel.Join(alias, inner), pred), nil
	}
	return "", filterErr("cannot filter on '%s'", strings.Join(parts, "__"))
}

func (q *query) leaf(col string, f *Field, lookup, raw string) (string, error) {
	if f.Type == TSecret || f.WriteOnly {
		return "", filterErr("filtering on field '%s' is not allowed", f.Name)
	}
	if lookup == "isnull" {
		b, err := parseBool(raw)
		if err != nil {
			return "", filterErr("invalid boolean '%s'", raw)
		}
		if b {
			return col + " IS NULL", nil
		}
		return col + " IS NOT NULL", nil
	}
	if f.Type == TJSON {
		if lookup == "exact" {
			return col + "::text = " + q.arg(raw), nil
		}
		if lookup == "contains" || lookup == "icontains" {
			return col + "::text ILIKE " + q.arg("%"+likeEscape(raw)+"%"), nil
		}
		return "", filterErr("unsupported lookup '%s' on json field '%s'", lookup, f.Name)
	}
	textual := f.Type == TString || f.Type == TText || f.Type == TChoice
	tcol := col
	if !textual {
		tcol = col + "::text"
	}
	switch lookup {
	case "exact":
		if (raw == "null" || raw == "None") && (f.Nullable || f.Type == TFK) {
			return col + " IS NULL", nil
		}
		v, err := coerceFilter(f, raw)
		if err != nil {
			return "", err
		}
		return col + " = " + q.arg(v), nil
	case "iexact":
		return "lower(" + tcol + ") = lower(" + q.arg(raw) + ")", nil
	case "contains":
		return tcol + " LIKE " + q.arg("%"+likeEscape(raw)+"%"), nil
	case "icontains":
		return tcol + " ILIKE " + q.arg("%"+likeEscape(raw)+"%"), nil
	case "startswith":
		return tcol + " LIKE " + q.arg(likeEscape(raw)+"%"), nil
	case "istartswith":
		return tcol + " ILIKE " + q.arg(likeEscape(raw)+"%"), nil
	case "endswith":
		return tcol + " LIKE " + q.arg("%"+likeEscape(raw)), nil
	case "iendswith":
		return tcol + " ILIKE " + q.arg("%"+likeEscape(raw)), nil
	case "regex":
		return tcol + " ~ " + q.arg(raw), nil
	case "iregex":
		return tcol + " ~* " + q.arg(raw), nil
	case "in":
		var vals []any
		for _, s := range strings.Split(raw, ",") {
			v, err := coerceFilter(f, strings.TrimSpace(s))
			if err != nil {
				return "", err
			}
			vals = append(vals, v)
		}
		return col + " = ANY(" + q.arg(vals) + ")", nil
	case "gt", "gte", "lt", "lte":
		v, err := coerceFilter(f, raw)
		if err != nil {
			return "", err
		}
		op := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[lookup]
		return col + " " + op + " " + q.arg(v), nil
	}
	return "", filterErr("unsupported lookup '%s'", lookup)
}

func coerceFilter(f *Field, raw string) (any, error) {
	switch f.Type {
	case TInt, TFK:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, filterErr("invalid integer '%s' for field '%s'", raw, f.Name)
		}
		return n, nil
	case TFloat:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, filterErr("invalid number '%s' for field '%s'", raw, f.Name)
		}
		return n, nil
	case TBool:
		b, err := parseBool(raw)
		if err != nil {
			return nil, filterErr("invalid boolean '%s' for field '%s'", raw, f.Name)
		}
		return b, nil
	case TDateTime:
		t, err := parseTime(raw)
		if err != nil {
			return nil, filterErr("invalid datetime '%s' for field '%s'", raw, f.Name)
		}
		return t, nil
	case TChoice:
		if len(f.Choices) > 0 {
			if _, ok := f.Choices[0].Value.(int); ok {
				n, err := strconv.Atoi(raw)
				if err != nil {
					return nil, filterErr("invalid choice '%s' for field '%s'", raw, f.Name)
				}
				return n, nil
			}
		}
	}
	return raw, nil
}

// orderBy builds the ORDER BY clause from ?order_by= (AWX also accepts ?order=).
func (q *query) orderBy(res *Resource, params url.Values) (string, error) {
	spec := params.Get("order_by")
	if spec == "" {
		spec = params.Get("order")
	}
	if spec == "" {
		spec = res.Ordering
	}
	if spec == "" {
		spec = "id"
	}
	var parts []string
	hasID := false
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		dir := "ASC"
		if strings.HasPrefix(item, "-") {
			dir = "DESC"
			item = item[1:]
		}
		if item == "random" || item == "?" {
			parts = append(parts, "random()")
			continue
		}
		path := strings.Split(item, "__")
		f := res.field(path[0])
		if f == nil || f.NoFilter || f.Type == TSecret {
			return "", filterErr("cannot order by '%s'", item)
		}
		expr := "t." + quoteIdent(f.column())
		if len(path) == 2 && f.Type == TFK {
			target := registry[f.Ref]
			tf := target.field(path[1])
			if tf == nil {
				return "", filterErr("cannot order by '%s'", item)
			}
			expr = fmt.Sprintf("(SELECT o.%s FROM %s o WHERE o.id = t.%s)", quoteIdent(tf.column()), target.Table, quoteIdent(f.column()))
		} else if len(path) > 1 {
			return "", filterErr("cannot order by '%s'", item)
		}
		if path[0] == "id" {
			hasID = true
		}
		nulls := ""
		if dir == "DESC" {
			nulls = " NULLS LAST"
		}
		parts = append(parts, expr+" "+dir+nulls)
	}
	if !hasID {
		parts = append(parts, "t.id ASC")
	}
	return " ORDER BY " + strings.Join(parts, ", "), nil
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func quoteIdent(s string) string {
	switch s {
	case "limit", "user", "group", "order", "position":
		return `"` + s + `"`
	}
	return s
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1", "t", "yes", "on":
		return true, nil
	case "false", "0", "f", "no", "off", "":
		return false, nil
	}
	return false, fmt.Errorf("invalid bool")
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time")
}

func filterErr(format string, a ...any) error {
	return &Error{Status: 400, Body: Obj{"detail": fmt.Sprintf(format, a...)}}
}
