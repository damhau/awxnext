package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/settings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Version reported to clients. The awx.awx collection compares against this.
const (
	ProductName    = "AWX"
	ProductVersion = "24.6.1"
)

type Principal = rbac.Principal

// DBTX is implemented by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// App holds shared server state.
type App struct {
	DB       *pgxpool.Pool
	Cfg      *config.Config
	Crypto   *crypto.Box
	Settings *settings.Store
	// Notify is called after commits that change job state (wired to the dispatcher/websocket hub).
	Notify func(channel string, payload any)
	// Extra routes registered by other packages.
	Routes []Route
}

type Route struct {
	Method  string
	Pattern string
	Handler func(c *Ctx, w http.ResponseWriter, r *http.Request)
	// Public routes do not require authentication.
	Public bool
}

func (a *App) Handle(method, pattern string, h func(c *Ctx, w http.ResponseWriter, r *http.Request)) {
	a.Routes = append(a.Routes, Route{Method: method, Pattern: pattern, Handler: h})
}

func (a *App) HandlePublic(method, pattern string, h func(c *Ctx, w http.ResponseWriter, r *http.Request)) {
	a.Routes = append(a.Routes, Route{Method: method, Pattern: pattern, Handler: h, Public: true})
}

var (
	registry      = map[string]*Resource{}
	registryOrder []string
	// byType maps AWX object type ("job_template") to its resource.
	byType = map[string]*Resource{}
)

// Lookup returns a registered resource by name.
func Lookup(name string) *Resource { return registry[name] }

// LookupType returns a registered resource by AWX type.
func LookupType(t string) *Resource { return byType[t] }

// Register adds a resource to the registry, adding implicit fields.
func Register(r *Resource) *Resource {
	implicit := []*Field{{Name: "id", Type: TInt, ReadOnly: true, Label: "ID"}}
	if r.Discriminator {
		implicit = append(implicit, &Field{Name: "type", Type: TString, ReadOnly: true, Hidden: true})
	}
	if !r.NoTimestamps {
		implicit = append(implicit,
			&Field{Name: "created", Type: TDateTime, ReadOnly: true, Help: "Timestamp when this object was created."},
			&Field{Name: "modified", Type: TDateTime, ReadOnly: true, Help: "Timestamp when this object was last modified."},
			&Field{Name: "created_by", Type: TFK, Ref: "users", ReadOnly: true, Hidden: true},
			&Field{Name: "modified_by", Type: TFK, Ref: "users", ReadOnly: true, Hidden: true},
		)
	}
	if !r.NoName {
		implicit = append(implicit,
			&Field{Name: "name", Type: TString, Required: true, MaxLength: 512, Help: "Name of this " + r.Verbose + "."},
			&Field{Name: "description", Type: TText, Default: "", Help: "Optional description of this " + r.Verbose + "."},
		)
	}
	// Explicit fields override implicit ones with the same name.
	explicit := map[string]bool{}
	for _, f := range r.Fields {
		explicit[f.Name] = true
	}
	var fields []*Field
	for _, f := range implicit {
		if !explicit[f.Name] {
			fields = append(fields, f)
		}
	}
	r.Fields = append(fields, r.Fields...)
	if r.Managed {
		r.Fields = append(r.Fields, &Field{Name: "managed_by", Type: TString, ReadOnly: true,
			Help: "External source that owns this object (e.g. a Git repository). Managed objects cannot be edited through the API."})
	}
	r.fieldMap = map[string]*Field{}
	for _, f := range r.Fields {
		if f.Label == "" {
			f.Label = title(f.Name)
		}
		if f.Virtual {
			f.NoFilter = true
		}
		r.fieldMap[f.Name] = f
	}
	if r.Verbose == "" {
		r.Verbose = title(r.Type)
	}
	registry[r.Name] = r
	registryOrder = append(registryOrder, r.Name)
	if _, ok := byType[r.Type]; !ok {
		byType[r.Type] = r
	}
	return r
}

// Resources returns all registered resources in registration order.
func Resources() []*Resource {
	out := make([]*Resource, 0, len(registryOrder))
	for _, n := range registryOrder {
		out = append(out, registry[n])
	}
	return out
}

// Tx runs fn in a transaction bound to c.
func (c *Ctx) WithTx(fn func(c *Ctx) error) error {
	if c.Tx != nil {
		return fn(c)
	}
	tx, err := c.App.DB.Begin(c)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	cc := *c
	cc.Tx = tx
	if err := fn(&cc); err != nil {
		return err
	}
	return tx.Commit(c)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// WriteJSON is exported for handlers in other packages.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// WriteError is exported for handlers in other packages.
func WriteError(w http.ResponseWriter, err error) { writeError(w, err) }

func toStr(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// FormatTime renders a timestamp like Django REST framework: microseconds only when non-zero.
func FormatTime(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05.000000Z")
}

// ID extracts an int64 id from a decoded value.
func ID(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case float64:
		if x == float64(int64(x)) {
			return int64(x), true
		}
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// JSONText marshals v to a JSON string.
func JSONText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// SQLLiteral renders a value as a SQL literal for statements that cannot use
// bind parameters (e.g. SQL stored and executed later). Strings are quoted with
// standard_conforming_strings semantics.
func SQLLiteral(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case time.Time:
		return "'" + x.UTC().Format(time.RFC3339Nano) + "'::timestamptz"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = SQLLiteral(e)
		}
		return "ARRAY[" + strings.Join(parts, ", ") + "]"
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	}
	return SQLLiteral(fmt.Sprint(v))
}
