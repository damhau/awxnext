package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/jackc/pgx/v5"
)

func selectCols(res *Resource) string {
	cols := make([]string, 0, len(res.Fields))
	for _, f := range res.Fields {
		if f.Virtual {
			continue
		}
		expr := "t." + quoteIdent(f.column())
		if f.Type == TFloat {
			expr += "::float8"
		}
		cols = append(cols, expr)
	}
	return strings.Join(cols, ", ")
}

func scanObjs(res *Resource, rows pgx.Rows) ([]Obj, error) {
	defer rows.Close()
	var out []Obj
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		obj := make(Obj, len(res.Fields))
		i := 0
		for _, f := range res.Fields {
			if f.Virtual {
				continue
			}
			obj[f.Name] = normalize(vals[i])
			i++
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}

func normalize(v any) any {
	switch x := v.(type) {
	case int32:
		return int64(x)
	case int16:
		return int64(x)
	case float32:
		return float64(x)
	}
	return v
}

func (res *Resource) baseWhere(q *query) {
	if res.Discriminator {
		q.where = append(q.where, "t.type = "+q.arg(res.Type))
	}
}

// orgField returns the field holding the organization id, if any.
func (res *Resource) orgField() *Field {
	if res.OrgColumn == "" {
		return nil
	}
	for _, f := range res.Fields {
		if f.column() == res.OrgColumn {
			return f
		}
	}
	return nil
}

// ViewPredicate is the SQL condition (alias t) on rows of res visible to the
// principal, for queries that aggregate rather than list.
func ViewPredicate(c *Ctx, res *Resource, arg ArgFunc) string { return viewPredicate(c, res, arg) }

// viewPredicate restricts rows to those visible to the principal.
func viewPredicate(c *Ctx, res *Resource, arg ArgFunc) string {
	if res.View != nil {
		return res.View(c, arg)
	}
	if res.ContentType != "" {
		return c.User.Predicate(rbac.ArgFunc(arg), res.ContentType, "view_"+res.ContentType, res.Scope("t"))
	}
	return "TRUE"
}

// Scope locates rows of res (under alias) for RBAC predicates.
func (res *Resource) Scope(alias string) rbac.Scope {
	s := rbac.Scope{ID: alias + ".id"}
	if res.OrgColumn != "" {
		s.Org = alias + "." + res.OrgColumn
	}
	if res.FolderColumn != "" {
		s.Folder = alias + "." + res.FolderColumn
	}
	return s
}

// folderField returns the field holding the folder id, if any.
func (res *Resource) folderField() *Field {
	if res.FolderColumn == "" || res.FolderColumn == "id" {
		return nil
	}
	for _, f := range res.Fields {
		if f.column() == res.FolderColumn {
			return f
		}
	}
	return nil
}

// ObjScope extracts the organization and folder ids of an object.
func (res *Resource) ObjScope(obj Obj) (org, folder *int64) {
	if res.OrgColumn == "id" || res.FolderColumn == "id" {
		id, _ := ID(obj["id"])
		if res.OrgColumn == "id" {
			org = &id
		}
		if res.FolderColumn == "id" {
			folder = &id
		}
	}
	if f := res.orgField(); f != nil && res.OrgColumn != "id" {
		if o, ok := ID(obj[f.Name]); ok {
			org = &o
		}
	}
	if f := res.folderField(); f != nil {
		if o, ok := ID(obj[f.Name]); ok {
			folder = &o
		}
	}
	return org, folder
}

// Get loads a raw object by id without access checks.
func Get(c *Ctx, res *Resource, id int64) (Obj, error) {
	q := &query{}
	q.where = append(q.where, "t.id = "+q.arg(id))
	res.baseWhere(q)
	rows, err := c.DB().Query(c, "SELECT "+selectCols(res)+" FROM "+res.Table+" t"+q.whereSQL(), q.args...)
	if err != nil {
		return nil, err
	}
	objs, err := scanObjs(res, rows)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, ErrNotFound
	}
	return objs[0], nil
}

// GetVisible loads an object the principal can view (404 if missing, 403 if not visible).
func GetVisible(c *Ctx, res *Resource, id int64) (Obj, error) {
	obj, err := Get(c, res, id)
	if err != nil {
		return nil, err
	}
	ok, err := CanView(c, res, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrForbidden
	}
	return obj, nil
}

// CanView reports whether the principal can view object id.
func CanView(c *Ctx, res *Resource, id int64) (bool, error) {
	if c.User.Superuser || c.User.Auditor {
		return true, nil
	}
	q := &query{}
	pred := viewPredicate(c, res, q.arg)
	var ok bool
	err := c.DB().QueryRow(c, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s t WHERE t.id = %s AND (%s))", res.Table, q.arg(id), pred), q.args...).Scan(&ok)
	return ok, err
}

// HasPerm checks a permission on an existing object of res.
func HasPerm(c *Ctx, res *Resource, obj Obj, perm string) (bool, error) {
	if c.User.Superuser {
		return true, nil
	}
	if res.ContentType == "" {
		if res.Perm != nil {
			return res.Perm(c, obj, perm)
		}
		return false, nil
	}
	id, _ := ID(obj["id"])
	org, folder := res.ObjScope(obj)
	return c.User.Has(c, c.App.DB, res.ContentType, perm+"_"+res.ContentType, id, org, folder)
}

// HasPermID checks a permission on an object referenced by id.
func HasPermID(c *Ctx, resName string, id int64, perm string) (bool, error) {
	res := registry[resName]
	obj, err := Get(c, res, id)
	if err != nil {
		return false, err
	}
	return HasPerm(c, res, obj, perm)
}

// RequirePermID returns a field error when the principal lacks perm on the referenced object.
func RequirePermID(c *Ctx, field, resName string, v any, perm string) error {
	id, ok := ID(v)
	if !ok {
		return nil
	}
	ok, err := HasPermID(c, resName, id, perm)
	if err != nil {
		if err == ErrNotFound {
			return FieldError(field, fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
		}
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// ListParams controls a list query.
type ListParams struct {
	Params url.Values
	// Extra predicates, built with the query's arg func.
	Extra []func(arg ArgFunc) string
	// SkipView disables RBAC filtering (sublists whose access is checked on the parent).
	SkipView bool
	Page     int
	PageSize int
	NoCount  bool
	// DefaultOrder is an ORDER BY expression (alias t, no parameters) used
	// when the request does not ask for an order.
	DefaultOrder string
	// Decorate adds to the serialized objects of a page (WriteList).
	Decorate func(out []Obj) error
}

// List returns a page of raw objects and the total count.
func List(c *Ctx, res *Resource, lp ListParams) ([]Obj, int64, error) {
	q := &query{}
	res.baseWhere(q)
	if !lp.SkipView {
		if pred := viewPredicate(c, res, q.arg); pred != "TRUE" {
			q.where = append(q.where, pred)
		}
	}
	for _, e := range lp.Extra {
		q.where = append(q.where, e(q.arg))
	}
	if err := q.applyFilters(c, res, lp.Params); err != nil {
		return nil, 0, err
	}
	order, err := q.orderBy(res, lp.Params)
	if err != nil {
		return nil, 0, err
	}
	if lp.DefaultOrder != "" && lp.Params.Get("order_by") == "" && lp.Params.Get("order") == "" {
		order = " ORDER BY " + lp.DefaultOrder + ", t.id"
	}
	var count int64
	if !lp.NoCount {
		if err := c.DB().QueryRow(c, "SELECT count(*) FROM "+res.Table+" t"+q.whereSQL(), q.args...).Scan(&count); err != nil {
			return nil, 0, translateDBError(res, err)
		}
	}
	sql := "SELECT " + selectCols(res) + " FROM " + res.Table + " t" + q.whereSQL() + order
	if lp.PageSize > 0 {
		sql += fmt.Sprintf(" LIMIT %d OFFSET %d", lp.PageSize, (lp.Page-1)*lp.PageSize)
	}
	rows, err := c.DB().Query(c, sql, q.args...)
	if err != nil {
		return nil, 0, translateDBError(res, err)
	}
	objs, err := scanObjs(res, rows)
	return objs, count, err
}

// Serialize renders raw objects to their API representation.
func Serialize(c *Ctx, res *Resource, objs []Obj) ([]Obj, error) {
	out := make([]Obj, len(objs))
	ids := make([]int64, len(objs))
	for i, raw := range objs {
		id, _ := ID(raw["id"])
		ids[i] = id
		typ := res.Type
		if t, ok := raw["type"].(string); ok && t != "" {
			typ = t
		}
		o := Obj{"id": id, "type": typ, "url": res.URL(id)}
		for _, f := range res.Fields {
			if f.Hidden || f.WriteOnly || f.Virtual || f.Name == "id" || f.Name == "type" {
				continue
			}
			o[f.Name] = renderValue(f, raw[f.Name])
		}
		out[i] = o
	}
	if err := addSummaries(c, res, objs, out, ids); err != nil {
		return nil, err
	}
	for i, raw := range objs {
		rel := Obj{}
		for _, f := range res.Fields {
			if f.Type == TFK && f.Ref != "" {
				if v, ok := ID(raw[f.Name]); ok {
					if t := registry[f.Ref]; t != nil {
						rel[f.Name] = t.URL(v)
					}
				}
			}
		}
		base := res.URL(ids[i])
		for _, s := range res.SubLists {
			rel[s.Name] = base + s.Name + "/"
		}
		for _, a := range res.Actions {
			rel[a.Name] = base + a.Name + "/"
		}
		if !res.NoActivity {
			rel["activity_stream"] = base + "activity_stream/"
		}
		if res.ContentType != "" && len(rbac.LegacyRoles(res.ContentType)) > 0 {
			rel["object_roles"] = base + "object_roles/"
			rel["access_list"] = base + "access_list/"
		}
		if res.Copyable {
			rel["copy"] = base + "copy/"
		}
		if res.Versioned {
			rel["history"] = base + "history/"
		}
		if res.Related != nil {
			res.Related(c, raw, rel)
		}
		out[i]["related"] = rel
		if res.Serialize != nil {
			res.Serialize(c, out[i])
		}
	}
	if res.Summary != nil {
		if err := res.Summary(c, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SerializeOne renders a single raw object.
func SerializeOne(c *Ctx, res *Resource, obj Obj) (Obj, error) {
	out, err := Serialize(c, res, []Obj{obj})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

func renderValue(f *Field, v any) any {
	switch f.Type {
	case TSecret:
		if s, _ := v.(string); s != "" {
			return crypto.Placeholder
		}
		return ""
	case TDateTime:
		if t, ok := v.(time.Time); ok {
			return FormatTime(t)
		}
		return nil
	}
	if t, ok := v.(time.Time); ok {
		return FormatTime(t)
	}
	return v
}

func addSummaries(c *Ctx, res *Resource, raws, out []Obj, ids []int64) error {
	summaries := make([]Obj, len(raws))
	for i := range summaries {
		summaries[i] = Obj{}
	}
	// Foreign keys, batched per field.
	for _, f := range res.Fields {
		if f.Type != TFK || f.Ref == "" {
			continue
		}
		target := registry[f.Ref]
		if target == nil {
			continue
		}
		var fkIDs []int64
		for _, raw := range raws {
			if id, ok := ID(raw[f.Name]); ok {
				fkIDs = append(fkIDs, id)
			}
		}
		if len(fkIDs) == 0 {
			continue
		}
		cols := target.SummaryFields
		if f.Ref == "users" {
			cols = []string{"id", "username", "first_name", "last_name"}
		} else if len(cols) == 0 {
			cols = []string{"id", "name", "description"}
		}
		var sel []string
		for _, col := range cols {
			if tf := target.field(col); tf != nil {
				expr := quoteIdent(tf.column())
				if tf.Type == TFloat {
					expr += "::float8"
				}
				sel = append(sel, expr)
			}
		}
		rows, err := c.DB().Query(c, fmt.Sprintf("SELECT %s FROM %s WHERE id = ANY($1)", strings.Join(sel, ", "), target.Table), fkIDs)
		if err != nil {
			return err
		}
		byID := map[int64]Obj{}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				rows.Close()
				return err
			}
			m := Obj{}
			for j, col := range cols[:len(sel)] {
				m[col] = renderValue(target.field(col), normalize(vals[j]))
			}
			id, _ := ID(m["id"])
			byID[id] = m
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, raw := range raws {
			if id, ok := ID(raw[f.Name]); ok {
				if m, ok := byID[id]; ok {
					summaries[i][f.Name] = m
				}
			}
		}
	}
	// Legacy object roles.
	if res.ContentType != "" && len(rbac.LegacyRoles(res.ContentType)) > 0 && len(ids) > 0 {
		rows, err := c.DB().Query(c, `SELECT o.object_id, o.role_field, o.id, rd.description FROM object_roles o
			JOIN role_definitions rd ON rd.id = o.role_definition_id
			WHERE o.content_type = $1 AND o.object_id = ANY($2)`, res.ContentType, ids)
		if err != nil {
			return err
		}
		roles := map[int64]Obj{}
		for rows.Next() {
			var oid, rid int64
			var field, desc string
			if err := rows.Scan(&oid, &field, &rid, &desc); err != nil {
				rows.Close()
				return err
			}
			if roles[oid] == nil {
				roles[oid] = Obj{}
			}
			roles[oid][field] = Obj{"id": rid, "name": LegacyRoleName(field), "description": desc}
		}
		rows.Close()
		for i, id := range ids {
			if r, ok := roles[id]; ok {
				summaries[i]["object_roles"] = r
			}
		}
	}
	caps, err := UserCapabilities(c, res, raws, ids)
	if err != nil {
		return err
	}
	for i := range raws {
		if len(caps[i]) > 0 {
			summaries[i]["user_capabilities"] = caps[i]
		}
		out[i]["summary_fields"] = summaries[i]
	}
	return nil
}

// UserCapabilities computes the user_capabilities of objects of res (raw rows
// with their ids), as res's own serializer reports them.
func UserCapabilities(c *Ctx, res *Resource, raws []Obj, ids []int64) ([]map[string]bool, error) {
	var perms map[int64]map[string]bool
	if res.ContentType != "" && !c.User.Superuser {
		var err error
		perms, err = c.User.PermsFor(c, c.App.DB, res.Table, res.ContentType, res.OrgColumn, res.FolderColumn, ids)
		if err != nil {
			return nil, err
		}
	}
	out := make([]map[string]bool, len(raws))
	for i, raw := range raws {
		p := perms[ids[i]]
		if p == nil {
			p = map[string]bool{}
		}
		caps := defaultCapabilities(c, res, p)
		if res.Capabilities != nil {
			for k, v := range res.Capabilities(c, raw, p) {
				caps[k] = v
			}
		}
		out[i] = caps
	}
	return out, nil
}

func defaultCapabilities(c *Ctx, res *Resource, p map[string]bool) map[string]bool {
	if res.ReadOnly {
		return map[string]bool{}
	}
	su := c.User.Superuser
	ct := res.ContentType
	caps := map[string]bool{
		"edit":   su || (ct != "" && p["change_"+ct]),
		"delete": !res.NoDelete && (su || (ct != "" && p["delete_"+ct])),
	}
	if res.Copyable {
		caps["copy"] = caps["edit"]
	}
	return caps
}

// LegacyRoleName renders an AWX role field as its display name ("job_template_admin_role" -> "Job Template Admin").
func LegacyRoleName(field string) string {
	f := strings.TrimSuffix(field, "_role")
	switch f {
	case "adhoc":
		return "Ad Hoc"
	case "auditor":
		return "Auditor"
	}
	return title(f)
}

// decodeBody parses a JSON request body preserving numbers.
func decodeBody(r *strings.Reader) (Obj, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var data Obj
	if err := dec.Decode(&data); err != nil {
		return nil, BadRequest("JSON parse error - " + err.Error())
	}
	if data == nil {
		data = Obj{}
	}
	return data, nil
}

// coerce validates and converts an incoming value for a field.
func coerce(c *Ctx, f *Field, v any) (any, error) {
	if v == nil {
		if f.Nullable || (f.Type == TFK && !f.Required) || f.Type == TJSON {
			return nil, nil
		}
		// As AWX's CharNullField, ChoiceNullField and BooleanNullField: null
		// is read as an empty string, then validated, or as false. The awx.awx
		// modules send null for every option left unset.
		switch f.Type {
		case TString, TText, TChoice:
			v = ""
		case TBool:
			return false, nil
		default:
			return nil, fmt.Errorf("This field may not be null.")
		}
	}
	switch f.Type {
	case TString, TText, TSecret:
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case json.Number:
			s = x.String()
		case map[string]any, []any:
			if f.Type == TText {
				b, _ := json.Marshal(x)
				s = string(b)
			} else {
				return nil, fmt.Errorf("Not a valid string.")
			}
		default:
			return nil, fmt.Errorf("Not a valid string.")
		}
		if f.Required && strings.TrimSpace(s) == "" && f.Type == TString {
			return nil, fmt.Errorf("This field may not be blank.")
		}
		if f.MaxLength > 0 && len(s) > f.MaxLength {
			return nil, fmt.Errorf("Ensure this field has no more than %d characters.", f.MaxLength)
		}
		return s, nil
	case TInt:
		switch x := v.(type) {
		case json.Number:
			n, err := x.Int64()
			if err != nil {
				return nil, fmt.Errorf("A valid integer is required.")
			}
			return n, nil
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("A valid integer is required.")
			}
			return n, nil
		}
		if n, ok := ID(v); ok {
			return n, nil
		}
		return nil, fmt.Errorf("A valid integer is required.")
	case TFloat:
		switch x := v.(type) {
		case json.Number:
			return x.Float64()
		case float64:
			return x, nil
		case string:
			n, err := strconv.ParseFloat(x, 64)
			if err != nil {
				return nil, fmt.Errorf("A valid number is required.")
			}
			return n, nil
		}
		return nil, fmt.Errorf("A valid number is required.")
	case TBool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			b, err := parseBool(x)
			if err == nil {
				return b, nil
			}
		case json.Number:
			if x.String() == "1" {
				return true, nil
			}
			if x.String() == "0" {
				return false, nil
			}
		}
		return nil, fmt.Errorf("Must be a valid boolean.")
	case TJSON:
		if s, ok := v.(string); ok {
			var out any
			if s == "" {
				return Obj{}, nil
			}
			if err := json.Unmarshal([]byte(s), &out); err != nil {
				return nil, fmt.Errorf("Value must be valid JSON.")
			}
			return out, nil
		}
		return v, nil
	case TFK:
		id, ok := ID(v)
		if !ok {
			return nil, fmt.Errorf("Incorrect type. Expected pk value, received %T.", v)
		}
		target := registry[f.Ref]
		if target != nil {
			q := "SELECT EXISTS(SELECT 1 FROM " + target.Table + " WHERE id = $1"
			args := []any{id}
			if target.Discriminator {
				q += " AND type = $2"
				args = append(args, target.Type)
			}
			var exists bool
			if err := c.DB().QueryRow(c, q+")", args...).Scan(&exists); err != nil {
				return nil, err
			}
			if !exists {
				return nil, fmt.Errorf("Invalid pk \"%d\" - object does not exist.", id)
			}
		}
		return id, nil
	case TChoice:
		for _, ch := range f.Choices {
			switch cv := ch.Value.(type) {
			case string:
				if s, ok := v.(string); ok && s == cv {
					return s, nil
				}
			case int:
				if n, ok := ID(v); ok && n == int64(cv) {
					return n, nil
				}
			}
		}
		return nil, fmt.Errorf("\"%v\" is not a valid choice.", v)
	case TDateTime:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("Datetime has wrong format.")
		}
		if s == "" && f.Nullable {
			return nil, nil
		}
		t, err := parseTime(s)
		if err != nil {
			return nil, fmt.Errorf("Datetime has wrong format.")
		}
		return t, nil
	}
	return v, nil
}

func (c *Ctx) dbValue(f *Field, v any) (any, error) {
	if f.Type == TSecret {
		if s, ok := v.(string); ok {
			return c.App.Crypto.Encrypt(s)
		}
	}
	return v, nil
}

// Validate coerces writable fields present in data. When create is true, missing required fields are errors.
func Validate(c *Ctx, res *Resource, data Obj, create bool, old Obj) (Obj, error) {
	fe := FieldErrors{}
	vals := Obj{}
	for _, f := range res.Fields {
		if f.ReadOnly {
			continue
		}
		v, ok := data[f.Name]
		if !ok {
			if create && f.Required {
				fe.Add(f.Name, "This field is required.")
			}
			if !create || f.Default == nil || f.Virtual {
				continue
			}
			// Store the declared default rather than relying on the column default.
			v = copyDefault(f.Default)
		}
		if !create && f.CreateOnly {
			// Compared after coercion: null sent for an empty kind is no change.
			if cv, err := coerce(c, f, v); old != nil && (err != nil || fmt.Sprint(old[f.Name]) != fmt.Sprint(cv)) {
				fe.Add(f.Name, "This field cannot be changed.")
			}
			continue
		}
		if f.Type == TSecret && v == crypto.Placeholder {
			continue
		}
		cv, err := coerce(c, f, v)
		if err != nil {
			fe.Add(f.Name, err.Error())
			continue
		}
		vals[f.Name] = cv
	}
	if len(fe) > 0 {
		return nil, fe.Err()
	}
	for _, f := range res.Fields {
		if f.Validate == nil {
			continue
		}
		if v, ok := vals[f.Name]; ok {
			if err := f.Validate(c, v, vals); err != nil {
				if _, isAPI := err.(*Error); isAPI {
					return nil, err
				}
				fe.Add(f.Name, err.Error())
			}
		}
	}
	return vals, fe.Err()
}

func copyDefault(v any) any {
	switch v.(type) {
	case map[string]any, []any, []string:
		var out any
		b, _ := json.Marshal(v)
		json.Unmarshal(b, &out)
		return out
	}
	return v
}

// Create validates data and inserts a new object. preset values bypass validation and read-only checks.
func Create(c *Ctx, res *Resource, data Obj, preset Obj) (Obj, error) {
	vals, err := Validate(c, res, data, true, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range preset {
		vals[k] = v
	}
	return Insert(c, res, vals)
}

// Insert creates an object from already-validated values, enforcing add permission.
func Insert(c *Ctx, res *Resource, vals Obj) (Obj, error) {
	// The organization may be derived in BeforeSave (job templates take it
	// from their project); the folder is checked again after it.
	if of := res.orgField(); of == nil || vals[of.Name] != nil {
		if err := checkFolder(c, res, vals); err != nil {
			return nil, err
		}
	}
	if res.CanAdd != nil {
		if err := res.CanAdd(c, vals); err != nil {
			return nil, err
		}
	} else if !c.User.Superuser {
		ok, err := CanAddIn(c, res, vals)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrForbidden
		}
	}
	var obj Obj
	err := c.WithTx(func(c *Ctx) error {
		if res.BeforeSave != nil {
			if err := res.BeforeSave(c, nil, vals); err != nil {
				return err
			}
		}
		if err := checkFolder(c, res, vals); err != nil {
			return err
		}
		q := &query{}
		var cols, phs []string
		for _, f := range res.Fields {
			v, ok := vals[f.Name]
			if !ok || f.Name == "id" || f.Virtual {
				continue
			}
			dv, err := c.dbValue(f, v)
			if err != nil {
				return err
			}
			cols = append(cols, quoteIdent(f.column()))
			phs = append(phs, q.arg(dv))
		}
		if res.Discriminator {
			cols = append(cols, "type")
			phs = append(phs, q.arg(res.Type))
		}
		if !res.NoTimestamps && !c.User.Anonymous() {
			cols = append(cols, "created_by_id", "modified_by_id")
			phs = append(phs, q.arg(c.User.UserID), q.arg(c.User.UserID))
		}
		sql := "INSERT INTO " + res.Table
		if len(cols) == 0 {
			sql += " DEFAULT VALUES RETURNING id"
		} else {
			sql += " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ") RETURNING id"
		}
		var id int64
		if err := c.DB().QueryRow(c, sql, q.args...).Scan(&id); err != nil {
			return translateDBError(res, err)
		}
		var err error
		obj, err = Get(c, res, id)
		if err != nil {
			return err
		}
		if res.ContentType != "" {
			if err := rbac.EnsureObjectRoles(c, c.DB(), res.ContentType, id); err != nil {
				return err
			}
			if err := grantCreator(c, res, id); err != nil {
				return err
			}
		}
		if res.AfterCreate != nil {
			if err := res.AfterCreate(c, obj); err != nil {
				return err
			}
		}
		changes := Obj{}
		for _, f := range res.Fields {
			if v, ok := vals[f.Name]; ok && !f.Virtual {
				changes[f.Name] = activityValue(f, v)
			}
		}
		if !res.NoActivity {
			if err := Activity(c, "create", res, obj, changes, nil, nil, ""); err != nil {
				return err
			}
		}
		return recordVersion(c, res, obj, "create", changes)
	})
	return obj, err
}

// CanAddIn reports whether the principal may create an object of res with the
// given organization/folder placement: add_<type> on the folder (or an ancestor)
// or on the organization.
func CanAddIn(c *Ctx, res *Resource, vals Obj) (bool, error) {
	if c.User.Superuser {
		return true, nil
	}
	if res.ContentType == "" {
		return false, nil
	}
	perm := "add_" + res.ContentType
	var org int64
	if f := res.orgField(); f != nil {
		o, ok := ID(vals[f.Name])
		if !ok {
			return false, nil
		}
		org = o
	} else {
		return false, nil
	}
	if f := res.folderField(); f != nil {
		if folder, ok := ID(vals[f.Name]); ok {
			return c.User.HasFolder(c, c.App.DB, perm, folder, org)
		}
	}
	return c.User.HasOrg(c, c.App.DB, perm, org)
}

// checkFolder ensures an object's folder belongs to the object's organization.
func checkFolder(c *Ctx, res *Resource, vals Obj) error {
	ff := res.folderField()
	of := res.orgField()
	if ff == nil || of == nil {
		return nil
	}
	folder, ok := ID(vals[ff.Name])
	if !ok {
		return nil
	}
	org, ok := ID(vals[of.Name])
	if !ok {
		return FieldError(ff.Name, "An organization is required to place an object in a folder.")
	}
	var forg int64
	if err := c.DB().QueryRow(c, "SELECT organization_id FROM folders WHERE id = $1", folder).Scan(&forg); err != nil {
		return FieldError(ff.Name, fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", folder))
	}
	if forg != org {
		return FieldError(ff.Name, "Folder must belong to the same organization.")
	}
	return nil
}

// grantCreator gives a non-superuser creator the admin role on the new object (DAB "creator defaults").
func grantCreator(c *Ctx, res *Resource, id int64) error {
	if c.User.Superuser || c.User.Anonymous() {
		return nil
	}
	for _, m := range rbac.LegacyRoles(res.ContentType) {
		if m.LegacyField == "admin_role" {
			_, err := c.DB().Exec(c, `INSERT INTO role_assignments (role_definition_id, user_id, content_type, object_id, created_by_id)
				SELECT id, $1, $2, $3, $1 FROM role_definitions WHERE name = $4 ON CONFLICT DO NOTHING`,
				c.User.UserID, res.ContentType, id, m.Name)
			return err
		}
	}
	return nil
}

// Update validates data and applies changes to obj.
func Update(c *Ctx, res *Resource, old Obj, data Obj) (Obj, error) {
	vals, err := Validate(c, res, data, false, old)
	if err != nil {
		return nil, err
	}
	return Apply(c, res, old, vals)
}

// CheckManaged rejects changes to objects owned by an external source
// (unless the change is made by that source, see Ctx.Extra["managed_by"]).
func CheckManaged(c *Ctx, res *Resource, obj Obj) error { return checkManaged(c, res, obj) }

// checkManaged rejects changes to objects owned by an external source.
func checkManaged(c *Ctx, res *Resource, obj Obj) error {
	if !res.Managed {
		return nil
	}
	owner, _ := obj["managed_by"].(string)
	if owner == "" || c.Extra["managed_by"] == owner {
		return nil
	}
	return Detail(409, fmt.Sprintf("This %s is managed by %s. Change it at the source.", strings.ToLower(res.Verbose), owner))
}

// Apply writes already-validated changes, enforcing change permission.
func Apply(c *Ctx, res *Resource, old Obj, vals Obj) (Obj, error) {
	if err := checkManaged(c, res, old); err != nil {
		return nil, err
	}
	merged := Obj{}
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range vals {
		merged[k] = v
	}
	if res.CanChange != nil {
		if err := res.CanChange(c, old, merged); err != nil {
			return nil, err
		}
	} else {
		ok, err := HasPerm(c, res, old, "change")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrForbidden
		}
		// Moving to another organization or folder requires add permission at the destination.
		if !c.User.Superuser && res.ContentType != "" {
			moved := false
			for _, f := range []*Field{res.orgField(), res.folderField()} {
				if f != nil && res.OrgColumn != "id" {
					if nv, ok := vals[f.Name]; ok && fmt.Sprint(nv) != fmt.Sprint(old[f.Name]) {
						moved = true
					}
				}
			}
			if moved {
				ok, err := CanAddIn(c, res, merged)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, ErrForbidden
				}
			}
		}
	}
	if err := checkFolder(c, res, merged); err != nil {
		return nil, err
	}
	id, _ := ID(old["id"])
	var obj Obj
	err := c.WithTx(func(c *Ctx) error {
		if res.BeforeSave != nil {
			if err := res.BeforeSave(c, old, vals); err != nil {
				return err
			}
		}
		q := &query{}
		var sets []string
		changes := Obj{}
		for _, f := range res.Fields {
			v, ok := vals[f.Name]
			if !ok || f.Virtual {
				continue
			}
			if equalValues(old[f.Name], v) {
				continue
			}
			dv, err := c.dbValue(f, v)
			if err != nil {
				return err
			}
			sets = append(sets, quoteIdent(f.column())+" = "+q.arg(dv))
			changes[f.Name] = []any{activityValue(f, old[f.Name]), activityValue(f, v)}
		}
		if len(sets) > 0 {
			if !res.NoTimestamps {
				sets = append(sets, "modified = now()")
				if !c.User.Anonymous() {
					sets = append(sets, "modified_by_id = "+q.arg(c.User.UserID))
				}
			}
			sql := "UPDATE " + res.Table + " SET " + strings.Join(sets, ", ") + " WHERE id = " + q.arg(id)
			if _, err := c.DB().Exec(c, sql, q.args...); err != nil {
				return translateDBError(res, err)
			}
		}
		var err error
		obj, err = Get(c, res, id)
		if err != nil {
			return err
		}
		if res.AfterUpdate != nil {
			if err := res.AfterUpdate(c, old, obj); err != nil {
				return err
			}
		}
		if len(changes) > 0 {
			if !res.NoActivity {
				if err := Activity(c, "update", res, obj, changes, nil, nil, ""); err != nil {
					return err
				}
			}
			return recordVersion(c, res, obj, "update", changes)
		}
		return nil
	})
	return obj, err
}

func equalValues(a, b any) bool {
	if at, ok := a.(time.Time); ok {
		if bt, ok := b.(time.Time); ok {
			return at.Equal(bt)
		}
	}
	if ai, ok := ID(a); ok {
		if bi, ok := ID(b); ok {
			return ai == bi
		}
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func activityValue(f *Field, v any) any {
	if f.Redact != nil {
		return f.Redact(v)
	}
	if f.Type == TSecret || f.Name == "password" {
		if s, _ := v.(string); s != "" {
			return crypto.Placeholder
		}
	}
	if t, ok := v.(time.Time); ok {
		return FormatTime(t)
	}
	return v
}

// Delete removes an object after permission checks.
func Delete(c *Ctx, res *Resource, obj Obj) error {
	if err := checkManaged(c, res, obj); err != nil {
		return err
	}
	if res.CanDelete != nil {
		if err := res.CanDelete(c, obj); err != nil {
			return err
		}
	} else {
		ok, err := HasPerm(c, res, obj, "delete")
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	id, _ := ID(obj["id"])
	return c.WithTx(func(c *Ctx) error {
		if res.BeforeDelete != nil {
			if err := res.BeforeDelete(c, obj); err != nil {
				return err
			}
		}
		if !res.NoActivity {
			if err := Activity(c, "delete", res, obj, Obj{}, nil, nil, ""); err != nil {
				return err
			}
		}
		if err := recordVersion(c, res, obj, "delete", Obj{}); err != nil {
			return err
		}
		if res.SoftDelete != nil {
			return res.SoftDelete(c, obj)
		}
		if _, err := c.DB().Exec(c, "DELETE FROM "+res.Table+" WHERE id = $1", id); err != nil {
			return translateDBError(res, err)
		}
		if res.ContentType != "" {
			for _, sql := range []string{
				"DELETE FROM role_assignments WHERE content_type = $1 AND object_id = $2",
				"DELETE FROM object_roles WHERE content_type = $1 AND object_id = $2",
				"DELETE FROM instance_group_links WHERE content_type = $1 AND object_id = $2",
				"DELETE FROM notification_template_links WHERE content_type = $1 AND object_id = $2",
			} {
				if _, err := c.DB().Exec(c, sql, res.ContentType, id); err != nil {
					return err
				}
			}
		}
		if res.AfterDelete != nil {
			return res.AfterDelete(c, obj)
		}
		return nil
	})
}

// Activity records an activity stream entry, unless ACTIVITY_STREAM_ENABLED
// is off. Version history is recorded separately and does not depend on it.
func Activity(c *Ctx, op string, res *Resource, obj Obj, changes Obj, res2 *Resource, obj2 Obj, relationship string) error {
	if !c.App.Settings.Bool(c, "ACTIVITY_STREAM_ENABLED") {
		return nil
	}
	id, _ := ID(obj["id"])
	summary := Obj{res.Type: []Obj{summaryOf(res, obj)}}
	var o2 string
	var o2id *int64
	if res2 != nil {
		o2 = res2.Type
		i, _ := ID(obj2["id"])
		o2id = &i
		summary[res2.Type] = append(asObjs(summary[res2.Type]), summaryOf(res2, obj2))
	}
	var actor *int64
	if !c.User.Anonymous() {
		actor = &c.User.UserID
	}
	var org *int64
	if f := res.orgField(); f != nil {
		if o, ok := ID(obj[f.Name]); ok {
			org = &o
		}
	}
	if res.Name == "organizations" {
		org = &id
	}
	_, err := c.DB().Exec(c, `INSERT INTO activity_stream (actor_id, operation, object1, object1_id, object2, object2_id, object_relationship_type, changes, summary, organization_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		actor, op, res.Type, id, o2, o2id, relationship, changes, summary, org)
	return err
}

func asObjs(v any) []Obj {
	if l, ok := v.([]Obj); ok {
		return l
	}
	return nil
}

func summaryOf(res *Resource, obj Obj) Obj {
	s := Obj{"id": obj["id"]}
	if n, ok := obj["name"]; ok {
		s["name"] = n
	}
	if n, ok := obj["username"]; ok {
		s["username"] = n
	}
	return s
}
