// Package settings stores runtime-configurable settings (AWX /api/v2/settings/) in the database.
package settings

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Def describes a setting.
type Def struct {
	Key      string
	Category string
	Type     string // string, integer, boolean, list, nested object, float
	Default  any
	Label    string
	Help     string
	Secret   bool
	ReadOnly bool
	// Public settings are readable by any authenticated user.
	Public bool
	// Choices for choice-type settings.
	Choices [][]any
	// Validate performs extra validation on write.
	Validate func(v any) error
	// CompatOnly settings are accepted so AWX clients and imports keep
	// working, but nothing in awxnext reads them; their help says why.
	CompatOnly bool
	// Nullable settings accept null, as those AWX registers with allow_null.
	Nullable bool
}

// Category is a group of settings exposed at /api/v2/settings/<slug>/.
type Category struct {
	Slug string
	Name string
}

var (
	defs       = map[string]*Def{}
	order      []string
	Categories = []Category{
		{"all", "All"},
		{"authentication", "Authentication"},
		{"oidc", "OIDC"},
		{"ldap", "LDAP"},
		{"jobs", "Jobs"},
		{"system", "System"},
		{"ui", "UI"},
		{"logging", "Logging"},
		{"named-url", "Named URL"},
		{"changed", "Changed"},
	}
)

func Register(d *Def) {
	if d.Label == "" {
		d.Label = d.Key
	}
	defs[d.Key] = d
	order = append(order, d.Key)
}

func Lookup(key string) *Def { return defs[key] }

// InCategory returns definitions in a category in registration order ("all" returns everything).
func InCategory(slug string) []*Def {
	var out []*Def
	for _, k := range order {
		d := defs[k]
		if slug == "all" || slug == "changed" || d.Category == slug {
			out = append(out, d)
		}
	}
	return out
}

// Keys returns all setting keys sorted.
func Keys() []string {
	ks := append([]string(nil), order...)
	sort.Strings(ks)
	return ks
}

// Store reads and writes settings with a short-lived cache.
type Store struct {
	db    *pgxpool.Pool
	mu    sync.RWMutex
	cache map[string]json.RawMessage
	at    time.Time
}

const cacheTTL = 5 * time.Second

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) load(ctx context.Context) (map[string]json.RawMessage, error) {
	s.mu.RLock()
	if s.cache != nil && time.Since(s.at) < cacheTTL {
		c := s.cache
		s.mu.RUnlock()
		return c, nil
	}
	s.mu.RUnlock()
	rows, err := s.db.Query(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	s.mu.Lock()
	s.cache, s.at = m, time.Now()
	s.mu.Unlock()
	return m, rows.Err()
}

// Invalidate drops the cache (called after writes and on change notifications).
func (s *Store) Invalidate() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// Get returns the raw value of a setting (default if unset).
func (s *Store) Get(ctx context.Context, key string) any {
	m, err := s.load(ctx)
	if err == nil {
		if raw, ok := m[key]; ok {
			var v any
			if json.Unmarshal(raw, &v) == nil {
				return v
			}
		}
	}
	if d := defs[key]; d != nil {
		return d.Default
	}
	return nil
}

// IsSet reports whether a setting has a stored value.
func (s *Store) IsSet(ctx context.Context, key string) bool {
	m, err := s.load(ctx)
	if err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// Decode unmarshals a setting into out.
func (s *Store) Decode(ctx context.Context, key string, out any) error {
	b, err := json.Marshal(s.Get(ctx, key))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (s *Store) String(ctx context.Context, key string) string {
	v, _ := s.Get(ctx, key).(string)
	return v
}

func (s *Store) Bool(ctx context.Context, key string) bool {
	v, _ := s.Get(ctx, key).(bool)
	return v
}

func (s *Store) Int(ctx context.Context, key string) int {
	switch v := s.Get(ctx, key).(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// Set stores a value.
func (s *Store) Set(ctx context.Context, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO settings (key, value, modified) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, modified = now()`, key, b)
	s.Invalidate()
	return err
}

// Reset deletes a stored value, reverting to the default.
func (s *Store) Reset(ctx context.Context, key string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM settings WHERE key = $1", key)
	s.Invalidate()
	return err
}
