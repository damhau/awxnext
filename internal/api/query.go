package api

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/damhau/awxnext/internal/rbac"
)

// query accumulates SQL arguments and WHERE clauses.
type query struct {
	args  []any
	where []string
	alias int
	// ext, when set, receives arguments instead of args (filters embedded in another query).
	ext ArgFunc
}

func (q *query) arg(v any) string {
	if q.ext != nil {
		return q.ext(v)
	}
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

var filterAliases atomic.Int64

// FilterSQL builds a predicate on res (alias t) for one AWX-style filter
// expression such as "groups__name__icontains" = "web".
func FilterSQL(res *Resource, arg ArgFunc, key, value string) (string, error) {
	n := filterAliases.Add(1)
	// Distinct alias ranges keep nested subqueries unambiguous within one statement.
	q := &query{ext: arg, alias: int(1000 + (n%100000)*20)}
	parts := strings.Split(key, "__")
	lookup := "exact"
	if len(parts) > 1 && lookups[parts[len(parts)-1]] {
		lookup = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	return q.path(res, "t", parts, lookup, value)
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

// filterCond is one field filter: a path, its lookup and the raw value.
type filterCond struct {
	parts  []string
	lookup string
	raw    string
}

// applyFilters translates query-string filters into WHERE clauses, as AWX's
// FieldLookupBackend does: plain filters go into one filter() call, so those
// that cross the same relation must match the same related row; chain__
// filters are applied one by one, each free to match a different row;
// not__ filters exclude; or__ filters are ORed together.
func (q *query) applyFilters(c *Ctx, res *Resource, params url.Values) error {
	var ors []string
	var plain []filterCond
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys) // stable SQL and argument order
	for _, key := range keys {
		values := params[key]
		if f := res.ParamFilters[key]; f != nil {
			for _, v := range values {
				pred, err := f(c, v, q.arg)
				if err != nil {
					return filterErr("%s: %v", key, err)
				}
				q.where = append(q.where, pred)
			}
			continue
		}
		if key == "role_level" {
			for _, v := range values {
				pred, err := q.roleLevel(c, res, v)
				if err != nil {
					return err
				}
				q.where = append(q.where, pred)
			}
			continue
		}
		if reservedParams[key] {
			continue
		}
		negate, isOr, isChain := false, false, false
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
				isChain = true
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
			if !negate && !isOr && !isChain {
				plain = append(plain, filterCond{parts, lookup, v})
				continue
			}
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
	if len(plain) > 0 {
		pred, err := q.together(res, "t", plain)
		if err != nil {
			return err
		}
		q.where = append(q.where, pred)
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

// together compiles conditions that must hold at once: those crossing the
// same relation share one subquery, so they match the same related row.
func (q *query) together(res *Resource, alias string, conds []filterCond) (string, error) {
	var preds []string
	groups := map[string][]filterCond{}
	var order []string
	for _, fc := range conds {
		name := fc.parts[0]
		f := res.field(name)
		_, isRel := res.Relations[name]
		viaFK := f != nil && !f.NoFilter && f.Type == TFK && len(fc.parts) > 1
		if viaFK || (f == nil && isRel) {
			if _, seen := groups[name]; !seen {
				order = append(order, name)
			}
			groups[name] = append(groups[name], filterCond{fc.parts[1:], fc.lookup, fc.raw})
			continue
		}
		pred, err := q.path(res, alias, fc.parts, fc.lookup, fc.raw)
		if err != nil {
			return "", err
		}
		preds = append(preds, pred)
	}
	for _, name := range order {
		var target *Resource
		var rel Relation
		f := res.field(name)
		if f != nil {
			target = registry[f.Ref]
		} else {
			rel = res.Relations[name]
			target = registry[rel.Target]
		}
		if target == nil {
			return "", filterErr("unknown relation '%s'", name)
		}
		inner := q.nextAlias()
		var sub, direct []filterCond
		for _, fc := range groups[name] {
			if len(fc.parts) == 0 {
				direct = append(direct, fc)
			} else {
				sub = append(sub, fc)
			}
		}
		var inside []string
		// Filtering a relation directly compares ids.
		for _, fc := range direct {
			pred, err := q.leaf(inner+".id", &Field{Name: "id", Type: TInt}, fc.lookup, fc.raw)
			if err != nil {
				return "", err
			}
			inside = append(inside, pred)
		}
		if len(sub) > 0 {
			pred, err := q.together(target, inner, sub)
			if err != nil {
				return "", err
			}
			inside = append(inside, pred)
		}
		where := strings.Join(inside, " AND ")
		if f != nil {
			preds = append(preds, fmt.Sprintf("%s.%s IN (SELECT %s.id FROM %s %s WHERE %s)", alias, quoteIdent(f.column()), inner, target.Table, inner, where))
		} else {
			preds = append(preds, fmt.Sprintf("EXISTS (SELECT 1 FROM %s %s WHERE (%s) AND (%s))", target.Table, inner, rel.Join(alias, inner), where))
		}
	}
	return "(" + strings.Join(preds, ") AND (") + ")", nil
}

// roleLevel keeps the objects on which the user holds an AWX role
// (role_level=use_role): every permission of the managed role that backs it.
func (q *query) roleLevel(c *Ctx, res *Resource, role string) (string, error) {
	if res.ContentType == "" {
		return "", filterErr("Cannot apply role_level filter to this list because its model does not use roles for access control.")
	}
	for _, m := range rbac.LegacyRoles(res.ContentType) {
		if m.LegacyField == role {
			return c.User.PredicateAll(rbac.ArgFunc(q.arg), res.ContentType, m.Permissions, res.Scope("t")), nil
		}
	}
	return "", filterErr("role_level: %s has no %s", res.Verbose, role)
}

func (q *query) path(res *Resource, alias string, parts []string, lookup, raw string) (string, error) {
	name := parts[0]
	if f := res.field(name); f != nil && !f.NoFilter {
		col := alias + "." + quoteIdent(f.column())
		if len(parts) == 1 {
			return q.leaf(col, f, lookup, raw)
		}
		if f.Type == TJSON {
			// JSON path lookup: field__a__b=value compares the text at {a,b}.
			path := strings.Join(parts[1:], ",")
			return q.leaf("("+col+" #>> "+q.arg("{"+path+"}")+"::text[])", &Field{Name: name, Type: TString}, lookup, raw)
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
		// PostgreSQL's default null placement, like AWX: nulls sort as the
		// largest value, so "-started" lists jobs not started yet first.
		parts = append(parts, expr+" "+dir)
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
