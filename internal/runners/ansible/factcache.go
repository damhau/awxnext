package ansible

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// Ansible's jsonfile cache keeps one file per host. ansible-core 2.19 and later
// name it s1_<host> and wrap the facts in a "__payload__" string with data tags;
// earlier versions name it <host> and store plain JSON. Both are seeded, so the
// cache works whatever ansible-core the execution environment ships.
const factCachePrefix = "s1_"

// seededFacts is what prepareFactCache wrote: host → file name → contents.
type seededFacts map[string]map[string][]byte

// prepareFactCache writes cached facts and points Ansible's jsonfile cache at them.
func prepareFactCache(ctx context.Context, app *api.App, run *Run, row jobs.Row, env map[string]string) (seededFacts, error) {
	rel := filepath.Join("artifacts", fmt.Sprint(row.ID()), "fact_cache")
	timeout := app.Settings.Int(ctx, "ANSIBLE_FACT_CACHE_TIMEOUT")
	q := "SELECT name, ansible_facts FROM hosts WHERE inventory_id = $1 AND ansible_facts <> '{}'"
	args := []any{row.Int("inventory_id")}
	if timeout > 0 {
		q += " AND ansible_facts_modified > now() - make_interval(secs => $2)"
		args = append(args, timeout)
	}
	rows, err := app.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seeded := seededFacts{}
	for rows.Next() {
		var name string
		var facts map[string]any
		rows.Scan(&name, &facts)
		if strings.ContainsAny(name, "/\\") || name == ".." {
			continue
		}
		plain, _ := json.Marshal(facts)
		wrapped, _ := json.Marshal(map[string]string{"__payload__": string(plain)})
		files := map[string][]byte{name: plain, factCachePrefix + name: wrapped}
		for file, data := range files {
			if err := run.WriteFile(filepath.Join(rel, file), data, 0o600); err != nil {
				return nil, err
			}
		}
		seeded[name] = files
	}
	os.MkdirAll(filepath.Join(run.Dir, rel), 0o700)
	env["ANSIBLE_CACHE_PLUGIN"] = "jsonfile"
	env["ANSIBLE_CACHE_PLUGIN_CONNECTION"] = run.Path(filepath.ToSlash(rel))
	env["ANSIBLE_CACHE_PLUGIN_TIMEOUT"] = fmt.Sprint(max(timeout, 0))
	return seeded, rows.Err()
}

// saveFactCache stores the facts a run changed back on the hosts, and clears
// the facts of hosts whose cache file the run removed (meta: clear_facts).
// Files the run left as they were seeded are skipped, so their hosts keep
// their facts' age.
func saveFactCache(ctx context.Context, app *api.App, row jobs.Row, artifacts, ident string, seeded seededFacts) {
	// The worker returns the contents of artifacts/<ident>/ as the artifacts root.
	dir := filepath.Join(artifacts, "fact_cache")
	if _, err := os.Stat(dir); err != nil {
		dir = filepath.Join(artifacts, ident, "fact_cache")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	inv := row.Int("inventory_id")
	present := map[string]bool{}
	for _, e := range entries {
		present[e.Name()] = true
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		host, facts, ok := parseFactCache(e.Name(), b)
		if !ok || bytes.Equal(b, seeded[host][e.Name()]) {
			continue
		}
		app.DB.Exec(ctx, `UPDATE hosts SET ansible_facts = $3, ansible_facts_modified = now() WHERE inventory_id = $1 AND name = $2`,
			inv, host, facts)
	}
	for host, files := range seeded {
		for file := range files {
			if !present[file] {
				app.DB.Exec(ctx, `UPDATE hosts SET ansible_facts = '{}', ansible_facts_modified = now() WHERE inventory_id = $1 AND name = $2`,
					inv, host)
				break
			}
		}
	}
}

// parseFactCache reads one jsonfile cache entry in either format.
func parseFactCache(file string, b []byte) (host string, facts map[string]any, ok bool) {
	var wrapped struct {
		Payload *string `json:"__payload__"`
	}
	if strings.HasPrefix(file, factCachePrefix) && json.Unmarshal(b, &wrapped) == nil && wrapped.Payload != nil {
		if json.Unmarshal([]byte(*wrapped.Payload), &facts) != nil {
			return "", nil, false
		}
		untagged, _ := untagFacts(facts).(map[string]any)
		return strings.TrimPrefix(file, factCachePrefix), untagged, true
	}
	if json.Unmarshal(b, &facts) != nil {
		return "", nil, false
	}
	return file, facts, true
}

// untagFacts replaces ansible-core's tagged values
// ({"value": v, "tags": [...], "__ansible_type": "_AnsibleTaggedStr"}) with v.
func untagFacts(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if _, tagged := x["__ansible_type"]; tagged {
			if inner, ok := x["value"]; ok {
				return untagFacts(inner)
			}
		}
		for k, e := range x {
			x[k] = untagFacts(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = untagFacts(e)
		}
		return x
	}
	return v
}
