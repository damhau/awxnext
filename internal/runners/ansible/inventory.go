package ansible

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/vars"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

func registerInventoryUpdate() {
	jobs.Register(&jobs.TypeSpec{
		Type:         "inventory_update",
		TemplateType: "inventory_source",
		Runner:       jobs.RunnerFunc(runInventoryUpdate),
		Serial:       true,
		Impact:       func(context.Context, *jobs.Manager, jobs.Row) int { return 1 },
		Dependencies: inventoryUpdateDependencies,
	})
}

// inventoryUpdateDependencies syncs the source project of scm sources (and,
// for constructed inventories, the update-on-launch sources of the inputs).
func inventoryUpdateDependencies(ctx context.Context, m *jobs.Manager, row jobs.Row) ([]int64, error) {
	var deps []int64
	if row.Str("source") == "scm" {
		ids, err := jobDependencies(ctx, m, row)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			m.DB().Exec(ctx, "UPDATE unified_jobs SET source_project_update_id = $2 WHERE id = $1", row.ID(), ids[0])
		}
		deps = append(deps, ids...)
	}
	if row.Str("source") == "constructed" {
		rows, err := m.DB().Query(ctx, "SELECT input_inventory_id FROM inventory_input_inventories WHERE inventory_id = $1", row.Int("inventory_id"))
		if err != nil {
			return nil, err
		}
		var inputs []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			inputs = append(inputs, id)
		}
		rows.Close()
		for _, inv := range inputs {
			ids, err := inventorySourceDependencies(ctx, m, inv)
			if err != nil {
				return nil, err
			}
			deps = append(deps, ids...)
		}
	}
	return deps, nil
}

// inventorySourceDependencies launches updates for an inventory's
// update-on-launch sources whose last update is older than their cache timeout.
func inventorySourceDependencies(ctx context.Context, m *jobs.Manager, inv int64) ([]int64, error) {
	if inv == 0 {
		return nil, nil
	}
	c := systemCtx(ctx, m.App)
	res := api.Lookup("inventory_sources")
	rows, err := m.DB().Query(ctx, `SELECT id, update_cache_timeout FROM unified_job_templates
		WHERE type = 'inventory_source' AND inventory_id = $1 AND update_on_launch ORDER BY id`, inv)
	if err != nil {
		return nil, err
	}
	type src struct{ id, cache int64 }
	var srcs []src
	for rows.Next() {
		var s src
		rows.Scan(&s.id, &s.cache)
		srcs = append(srcs, s)
	}
	rows.Close()
	var deps []int64
	for _, s := range srcs {
		var existing int64
		if err := m.DB().QueryRow(ctx, `SELECT id FROM unified_jobs WHERE type = 'inventory_update' AND unified_job_template_id = $1
			AND status IN ('pending', 'waiting', 'running') ORDER BY id DESC LIMIT 1`, s.id).Scan(&existing); err == nil {
			deps = append(deps, existing)
			continue
		}
		if s.cache > 0 {
			var recent bool
			m.DB().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM unified_jobs WHERE type = 'inventory_update' AND unified_job_template_id = $1
				AND status = 'successful' AND finished > now() - make_interval(secs => $2))`, s.id, s.cache).Scan(&recent)
			if recent {
				continue
			}
		}
		obj, err := api.Get(c, res, s.id)
		if err != nil {
			continue
		}
		id, err := resources.LaunchInventoryUpdate(c, obj, "dependency")
		if err != nil {
			return nil, err
		}
		deps = append(deps, id)
	}
	return deps, nil
}

func runInventoryUpdate(ctx context.Context, rc *jobs.RunContext) jobs.Result {
	row, app := rc.Job, rc.M.App
	id := row.ID()
	run, err := NewRunFor(ctx, app, fmt.Sprint(id), row)
	if err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}
	defer run.Cleanup()
	update := map[string]any{}
	source := row.Str("source")
	invID := row.Int("inventory_id")
	if invID == 0 {
		return fail("The inventory for this update no longer exists.")
	}

	// Inventory to parse.
	var invPath string
	switch source {
	case "scm":
		dir, rev, err := checkoutProject(ctx, app, row)
		if err != nil {
			return fail(err.Error())
		}
		update["scm_revision"] = rev
		sp := strings.Trim(path.Clean("/"+row.Str("source_path")), "/")
		if strings.Contains(row.Str("source_path"), "..") {
			return fail("Invalid source path.")
		}
		if _, err := os.Stat(filepath.Join(dir, sp)); err != nil {
			return fail(fmt.Sprintf("Inventory source path %q not found in project.", row.Str("source_path")))
		}
		run.Mounts = append(run.Mounts, Mount{Prefix: "project", Path: dir})
		req, err := InstallRequirements(ctx, app, rc.Out, row, row.Int("project_id"), dir)
		if err != nil {
			return fail("Project requirements: " + err.Error())
		}
		req.Mount(run)
		invPath = run.Path(path.Join("project", sp))
	case "constructed":
		rows, err := app.DB.Query(ctx, "SELECT input_inventory_id FROM inventory_input_inventories WHERE inventory_id = $1 ORDER BY position, input_inventory_id", invID)
		if err != nil {
			return jobs.Result{Status: "error", Explanation: err.Error()}
		}
		var inputs []int64
		for rows.Next() {
			var in int64
			rows.Scan(&in)
			inputs = append(inputs, in)
		}
		rows.Close()
		if len(inputs) == 0 {
			return fail("The constructed inventory has no input inventories.")
		}
		// Ansible reads a directory source in name order: inputs first, then the constructed config.
		for i, in := range inputs {
			script, err := resources.InventoryScript(ctx, app, in, resources.ScriptOptions{HostVars: true, TowerVars: true})
			if err != nil {
				return fail(fmt.Sprintf("Input inventory %d: %v", in, err))
			}
			b, _ := json.Marshal(script)
			body := "#!/bin/sh\ncat <<'AWXNEXT_INVENTORY_EOF'\n" + string(b) + "\nAWXNEXT_INVENTORY_EOF\n"
			if err := run.WriteFile(fmt.Sprintf("inventory/input_%04d_%d", i, in), []byte(body), 0o700); err != nil {
				return jobs.Result{Status: "error", Explanation: err.Error()}
			}
		}
		cfg, err := pluginConfig("constructed", row.Str("source_vars"))
		if err != nil {
			return fail("Source variables: " + err.Error())
		}
		if err := run.WriteFile("inventory/zz_constructed.yml", cfg, 0o600); err != nil {
			return jobs.Result{Status: "error", Explanation: err.Error()}
		}
		invPath = run.Path("inventory")
	default:
		p, ok := resources.InventoryPlugins[source]
		if !ok {
			return fail(fmt.Sprintf("Unsupported inventory source %q.", source))
		}
		cfg, err := pluginConfig(p.Plugin, row.Str("source_vars"))
		if err != nil {
			return fail("Source variables: " + err.Error())
		}
		if err := run.WriteFile("inventory/"+p.File, cfg, 0o600); err != nil {
			return jobs.Result{Status: "error", Explanation: err.Error()}
		}
		invPath = run.Path("inventory/" + p.File)
	}

	var creds []*resources.ResolvedCredential
	if cid := row.Int("credential_id"); cid != 0 {
		cred, err := resources.ResolveCredential(ctx, app, cid)
		if err != nil {
			return fail(err.Error())
		}
		creds = append(creds, cred)
	}
	inj, err := Inject(run, creds, nil)
	if err != nil {
		return fail(err.Error())
	}
	env := baseEnv(ctx, app, run, row)
	delete(env, "JOB_ID")
	env["INVENTORY_UPDATE_ID"] = fmt.Sprint(id)
	env["ANSIBLE_INVENTORY_UNPARSED_FAILED"] = "True"
	if source != "scm" && source != "constructed" {
		env["ANSIBLE_INVENTORY_ENABLED"] = "auto"
	}
	for k, v := range inj.Env {
		env[k] = v
	}
	if err := run.WriteJSON("env/envvars", env); err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}
	run.WriteJSON("env/settings", map[string]any{"suppress_ansible_output": false, "suppress_output_file": true, "pexpect_timeout": 10})

	outFile := run.Path(fmt.Sprintf("artifacts/%d/output.json", id))
	args := []string{"--list", "--export", "--output", outFile}
	if l := row.Str("limit"); l != "" {
		args = append(args, "--limit", l)
	}
	if v := row.Int("verbosity"); v > 0 {
		args = append(args, "-"+strings.Repeat("v", int(min(5, v*2+1))))
	}
	run.Kwargs = map[string]any{"ident": fmt.Sprint(id), "binary": "ansible-inventory", "cmdline": ShellQuote(args), "inventory": invPath}
	if err := setImage(ctx, app, run, row); err != nil {
		return fail(err.Error())
	}
	timeout := time.Duration(row.Int("timeout")) * time.Second
	if timeout == 0 {
		timeout = time.Duration(app.Settings.Int(ctx, "DEFAULT_INVENTORY_UPDATE_TIMEOUT")) * time.Second
	}
	run.Timeout = timeout
	update["job_args"] = "ansible-inventory " + ShellQuote(append(args, "-i", invPath))
	update["job_cwd"] = run.Root
	update["job_env"] = env

	oc, err := run.Execute(ctx, app, rc.Out)
	if err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error(), Update: update}
	}
	switch oc.Status {
	case "successful":
	case "timeout":
		return jobs.Result{Status: "failed", Explanation: "Job terminated due to timeout", Update: update}
	case "canceled":
		return jobs.Result{Status: "canceled", Update: update}
	case "failed":
		return jobs.Result{Status: "failed", Update: update}
	default:
		return jobs.Result{Status: "error", Update: update}
	}

	b, err := os.ReadFile(filepath.Join(oc.Artifacts, "output.json"))
	if err != nil {
		b, err = os.ReadFile(filepath.Join(oc.Artifacts, fmt.Sprint(id), "output.json"))
	}
	if err != nil {
		return jobs.Result{Status: "error", Explanation: "ansible-inventory produced no output.", Update: update}
	}
	data, err := ParseInventoryJSON(b)
	if err != nil {
		return jobs.Result{Status: "error", Explanation: "Invalid ansible-inventory output: " + err.Error(), Update: update}
	}
	start := time.Now()
	opts := ImportOptions{
		Overwrite:     row.Bool("overwrite"),
		OverwriteVars: row.Bool("overwrite_vars"),
		EnabledVar:    row.Str("enabled_var"),
		EnabledValue:  row.Str("enabled_value"),
		HostFilter:    row.Str("host_filter"),
	}
	stats, err := ImportInventory(ctx, app.DB, invID, row.Int("unified_job_template_id"), data, opts)
	if err != nil {
		rc.Out.Line("Inventory import failed: " + err.Error())
		return jobs.Result{Status: "failed", Explanation: err.Error(), Update: update}
	}
	if err := recordSyncActivity(ctx, app, invID, stats); err != nil {
		rc.Out.Line("Could not record the changes in the activity stream: " + err.Error())
	}
	rc.Out.Line(fmt.Sprintf("Loaded %d groups, %d hosts", stats.Groups, stats.Hosts))
	if stats.HostsDeleted+stats.GroupsDeleted > 0 {
		rc.Out.Line(fmt.Sprintf("Removed %d groups, %d hosts no longer in the source", stats.GroupsDeleted, stats.HostsDeleted))
	}
	rc.Out.Line(fmt.Sprintf("Inventory import completed for %s in %.1fs", row.Str("name"), time.Since(start).Seconds()))
	return jobs.Result{Status: "successful", Update: update}
}

// pluginConfig renders an inventory plugin config from source_vars, forcing the plugin name.
func pluginConfig(plugin, sourceVars string) ([]byte, error) {
	sv, err := vars.Parse(sourceVars)
	if err != nil {
		return nil, err
	}
	delete(sv, "plugin")
	keys := make([]string, 0, len(sv))
	for k := range sv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	root := &yaml.Node{Kind: yaml.MappingNode}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "plugin"}, &yaml.Node{Kind: yaml.ScalarNode, Value: plugin})
	for _, k := range keys {
		var v yaml.Node
		if err := v.Encode(sv[k]); err != nil {
			return nil, err
		}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &v)
	}
	return yaml.Marshal(root)
}

// InventoryData is parsed `ansible-inventory --list --export` output.
type InventoryData struct {
	AllVars  map[string]any
	Groups   map[string]*InventoryGroup
	HostVars map[string]map[string]any
	// Hosts lists every host name (in groups, ungrouped or with hostvars).
	Hosts []string
}

// InventoryGroup is a group in imported inventory data.
type InventoryGroup struct {
	Vars     map[string]any
	Hosts    []string
	Children []string
}

// ParseInventoryJSON reads ansible-inventory --list output.
func ParseInventoryJSON(b []byte) (*InventoryData, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	d := &InventoryData{AllVars: map[string]any{}, Groups: map[string]*InventoryGroup{}, HostVars: map[string]map[string]any{}}
	seen := map[string]bool{}
	addHost := func(h string) {
		if !seen[h] {
			seen[h] = true
			d.Hosts = append(d.Hosts, h)
		}
	}
	if m, ok := raw["_meta"]; ok {
		var meta struct {
			HostVars map[string]map[string]any `json:"hostvars"`
		}
		if err := json.Unmarshal(m, &meta); err != nil {
			return nil, fmt.Errorf("_meta: %w", err)
		}
		for h, v := range meta.HostVars {
			if v == nil {
				v = map[string]any{}
			}
			d.HostVars[h] = v
		}
	}
	names := make([]string, 0, len(raw))
	for k := range raw {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "_meta" {
			continue
		}
		var g struct {
			Hosts    []string       `json:"hosts"`
			Vars     map[string]any `json:"vars"`
			Children []string       `json:"children"`
		}
		if err := json.Unmarshal(raw[name], &g); err != nil {
			// Old-style script output: a bare list of hosts.
			var hosts []string
			if json.Unmarshal(raw[name], &hosts) != nil {
				return nil, fmt.Errorf("group %q: %w", name, err)
			}
			g.Hosts = hosts
		}
		for _, h := range g.Hosts {
			addHost(h)
		}
		switch name {
		case "all":
			for k, v := range g.Vars {
				d.AllVars[k] = v
			}
			continue
		case "ungrouped":
			continue
		}
		if g.Vars == nil {
			g.Vars = map[string]any{}
		}
		children := make([]string, 0, len(g.Children))
		for _, c := range g.Children {
			if c != "ungrouped" && c != "all" {
				children = append(children, c)
			}
		}
		d.Groups[name] = &InventoryGroup{Vars: g.Vars, Hosts: g.Hosts, Children: children}
	}
	// Children may be referenced without their own entry.
	for _, g := range d.Groups {
		for _, c := range g.Children {
			if d.Groups[c] == nil {
				d.Groups[c] = &InventoryGroup{Vars: map[string]any{}}
			}
		}
	}
	hv := make([]string, 0, len(d.HostVars))
	for h := range d.HostVars {
		hv = append(hv, h)
	}
	sort.Strings(hv)
	for _, h := range hv {
		addHost(h)
	}
	return d, nil
}

// ImportOptions control how imported data merges with existing inventory content.
type ImportOptions struct {
	Overwrite, OverwriteVars bool
	EnabledVar, EnabledValue string
	HostFilter               string
}

// ImportStats summarizes an import.
type ImportStats struct {
	Hosts, Groups, HostsDeleted, GroupsDeleted int
	// Added and Removed list the hosts and groups the import created and deleted.
	Added, Removed []ImportedObject
}

// ImportedObject is a host or group touched by an import.
type ImportedObject struct {
	Type string // "host" or "group"
	ID   int64
	Name string
}

// ImportInventory writes inventory data into an inventory in one transaction,
// following AWX's inventory_import semantics. sourceID links imported hosts
// and groups to their inventory source (0 for none).
func ImportInventory(ctx context.Context, db interface {
	Begin(context.Context) (pgx.Tx, error)
}, invID, sourceID int64, d *InventoryData, opt ImportOptions) (*ImportStats, error) {
	var filter *regexp.Regexp
	if opt.HostFilter != "" {
		var err error
		if filter, err = regexp.Compile(opt.HostFilter); err != nil {
			return nil, fmt.Errorf("invalid host_filter: %w", err)
		}
	}
	hostOK := func(h string) bool { return filter == nil || filter.MatchString(h) }
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Serialize imports into the same inventory.
	if _, err := tx.Exec(ctx, "SELECT id FROM inventories WHERE id = $1 FOR UPDATE", invID); err != nil {
		return nil, err
	}
	stats := &ImportStats{}

	// Inventory-level variables from the "all" group.
	if len(d.AllVars) > 0 || opt.OverwriteVars {
		var cur string
		if err := tx.QueryRow(ctx, "SELECT variables FROM inventories WHERE id = $1", invID).Scan(&cur); err != nil {
			return nil, err
		}
		next, changed := mergeVars(cur, d.AllVars, opt.OverwriteVars)
		if changed {
			if _, err := tx.Exec(ctx, "UPDATE inventories SET variables = $2, modified = now() WHERE id = $1", invID, next); err != nil {
				return nil, err
			}
		}
	}

	// Hosts.
	var hostNames []string
	for _, h := range d.Hosts {
		if hostOK(h) {
			hostNames = append(hostNames, h)
		}
	}
	existing := map[string]struct {
		id      int64
		vars    string
		enabled bool
	}{}
	rows, err := tx.Query(ctx, "SELECT id, name, variables, enabled FROM hosts WHERE inventory_id = $1", invID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name, v string
		var en bool
		if err := rows.Scan(&id, &name, &v, &en); err != nil {
			rows.Close()
			return nil, err
		}
		existing[name] = struct {
			id      int64
			vars    string
			enabled bool
		}{id, v, en}
	}
	rows.Close()
	hostIDs := map[string]int64{}
	var newNames, newVars []string
	var newEnabled []bool
	var updIDs []int64
	var updVars []string
	var updEnabled []bool
	for _, h := range hostNames {
		hv := d.HostVars[h]
		enabled, hasEnabled := hostEnabled(hv, opt.EnabledVar, opt.EnabledValue)
		if cur, ok := existing[h]; ok {
			hostIDs[h] = cur.id
			next, changed := mergeVars(cur.vars, hv, opt.OverwriteVars)
			en := cur.enabled
			if hasEnabled && en != enabled {
				en, changed = enabled, true
			}
			if changed {
				updIDs, updVars, updEnabled = append(updIDs, cur.id), append(updVars, next), append(updEnabled, en)
			}
			continue
		}
		if !hasEnabled {
			enabled = true
		}
		newNames, newVars, newEnabled = append(newNames, h), append(newVars, varsText(hv)), append(newEnabled, enabled)
	}
	if len(newNames) > 0 {
		rows, err := tx.Query(ctx, `INSERT INTO hosts (inventory_id, name, variables, enabled)
			SELECT $1, n, v, e FROM unnest($2::text[], $3::text[], $4::boolean[]) AS x(n, v, e) RETURNING id, name`, invID, newNames, newVars, newEnabled)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var name string
			rows.Scan(&id, &name)
			hostIDs[name] = id
			stats.Added = append(stats.Added, ImportedObject{"host", id, name})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(updIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE hosts h SET variables = x.v, enabled = x.e, modified = now()
			FROM unnest($1::bigint[], $2::text[], $3::boolean[]) AS x(id, v, e) WHERE h.id = x.id`, updIDs, updVars, updEnabled); err != nil {
			return nil, err
		}
	}
	stats.Hosts = len(hostNames)

	// Groups.
	groupNames := make([]string, 0, len(d.Groups))
	for g := range d.Groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)
	existingG := map[string]struct {
		id   int64
		vars string
	}{}
	rows, err = tx.Query(ctx, "SELECT id, name, variables FROM groups WHERE inventory_id = $1", invID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name, v string
		if err := rows.Scan(&id, &name, &v); err != nil {
			rows.Close()
			return nil, err
		}
		existingG[name] = struct {
			id   int64
			vars string
		}{id, v}
	}
	rows.Close()
	groupIDs := map[string]int64{}
	newNames, newVars, updIDs, updVars = nil, nil, nil, nil
	for _, g := range groupNames {
		gv := d.Groups[g].Vars
		if cur, ok := existingG[g]; ok {
			groupIDs[g] = cur.id
			if next, changed := mergeVars(cur.vars, gv, opt.OverwriteVars); changed {
				updIDs, updVars = append(updIDs, cur.id), append(updVars, next)
			}
			continue
		}
		newNames, newVars = append(newNames, g), append(newVars, varsText(gv))
	}
	if len(newNames) > 0 {
		rows, err := tx.Query(ctx, `INSERT INTO groups (inventory_id, name, variables)
			SELECT $1, n, v FROM unnest($2::text[], $3::text[]) AS x(n, v) RETURNING id, name`, invID, newNames, newVars)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var name string
			rows.Scan(&id, &name)
			groupIDs[name] = id
			stats.Added = append(stats.Added, ImportedObject{"group", id, name})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(updIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE groups g SET variables = x.v, modified = now()
			FROM unnest($1::bigint[], $2::text[]) AS x(id, v) WHERE g.id = x.id`, updIDs, updVars); err != nil {
			return nil, err
		}
	}
	stats.Groups = len(groupNames)

	// Memberships.
	var ghGroup, ghHost, gcParent, gcChild []int64
	for _, g := range groupNames {
		gid := groupIDs[g]
		for _, h := range d.Groups[g].Hosts {
			if hid, ok := hostIDs[h]; ok {
				ghGroup, ghHost = append(ghGroup, gid), append(ghHost, hid)
			}
		}
		for _, c := range d.Groups[g].Children {
			if cid, ok := groupIDs[c]; ok && cid != gid {
				gcParent, gcChild = append(gcParent, gid), append(gcChild, cid)
			}
		}
	}
	allGroupIDs := make([]int64, 0, len(groupIDs))
	for _, id := range groupIDs {
		allGroupIDs = append(allGroupIDs, id)
	}
	allHostIDs := make([]int64, 0, len(hostIDs))
	for _, id := range hostIDs {
		allHostIDs = append(allHostIDs, id)
	}
	if opt.Overwrite {
		// Groups from this import lose hosts and children the source no longer lists.
		if _, err := tx.Exec(ctx, `DELETE FROM group_hosts WHERE group_id = ANY($1) AND (group_id, host_id) NOT IN
			(SELECT g, h FROM unnest($2::bigint[], $3::bigint[]) AS x(g, h))`, allGroupIDs, ghGroup, ghHost); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM group_children WHERE parent_id = ANY($1) AND (parent_id, child_id) NOT IN
			(SELECT p, c FROM unnest($2::bigint[], $3::bigint[]) AS x(p, c))`, allGroupIDs, gcParent, gcChild); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO group_hosts (group_id, host_id) SELECT g, h FROM unnest($1::bigint[], $2::bigint[]) AS x(g, h)
		ON CONFLICT DO NOTHING`, ghGroup, ghHost); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO group_children (parent_id, child_id) SELECT p, c FROM unnest($1::bigint[], $2::bigint[]) AS x(p, c)
		ON CONFLICT DO NOTHING`, gcParent, gcChild); err != nil {
		return nil, err
	}

	if sourceID != 0 {
		if opt.Overwrite {
			// Hosts and groups this source imported before and no longer lists are
			// removed, unless another source still provides them.
			gone, err := deleteReturning(ctx, tx, "host", `DELETE FROM hosts WHERE id IN (
				SELECT host_id FROM host_inventory_sources WHERE inventory_source_id = $1 AND host_id <> ALL($2)
				AND NOT EXISTS (SELECT 1 FROM host_inventory_sources o WHERE o.host_id = host_inventory_sources.host_id AND o.inventory_source_id <> $1))
				RETURNING id, name`, sourceID, allHostIDs)
			if err != nil {
				return nil, err
			}
			stats.HostsDeleted = len(gone)
			stats.Removed = append(stats.Removed, gone...)
			gone, err = deleteReturning(ctx, tx, "group", `DELETE FROM groups WHERE id IN (
				SELECT group_id FROM group_inventory_sources WHERE inventory_source_id = $1 AND group_id <> ALL($2)
				AND NOT EXISTS (SELECT 1 FROM group_inventory_sources o WHERE o.group_id = group_inventory_sources.group_id AND o.inventory_source_id <> $1))
				RETURNING id, name`, sourceID, allGroupIDs)
			if err != nil {
				return nil, err
			}
			stats.GroupsDeleted = len(gone)
			stats.Removed = append(stats.Removed, gone...)
			if _, err := tx.Exec(ctx, "DELETE FROM host_inventory_sources WHERE inventory_source_id = $1 AND host_id <> ALL($2)", sourceID, allHostIDs); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, "DELETE FROM group_inventory_sources WHERE inventory_source_id = $1 AND group_id <> ALL($2)", sourceID, allGroupIDs); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO host_inventory_sources (host_id, inventory_source_id) SELECT unnest($1::bigint[]), $2
			ON CONFLICT DO NOTHING`, allHostIDs, sourceID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO group_inventory_sources (group_id, inventory_source_id) SELECT unnest($1::bigint[]), $2
			ON CONFLICT DO NOTHING`, allGroupIDs, sourceID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return stats, nil
}

// deleteReturning runs a DELETE ... RETURNING id, name and lists what it removed.
func deleteReturning(ctx context.Context, tx pgx.Tx, typ, sql string, args ...any) ([]ImportedObject, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportedObject
	for rows.Next() {
		o := ImportedObject{Type: typ}
		if err := rows.Scan(&o.ID, &o.Name); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// recordSyncActivity records the hosts and groups an inventory sync added and
// removed, when ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC is on. It is off
// by default, as in AWX: a sync can touch thousands of hosts.
func recordSyncActivity(ctx context.Context, app *api.App, invID int64, stats *ImportStats) error {
	if !app.Settings.Bool(ctx, "ACTIVITY_STREAM_ENABLED") || !app.Settings.Bool(ctx, "ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC") {
		return nil
	}
	var ops, types, names []string
	var ids []int64
	for _, x := range []struct {
		op   string
		objs []ImportedObject
	}{{"create", stats.Added}, {"delete", stats.Removed}} {
		for _, o := range x.objs {
			ops, types, ids, names = append(ops, x.op), append(types, o.Type), append(ids, o.ID), append(names, o.Name)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := app.DB.Exec(ctx, `INSERT INTO activity_stream (operation, object1, object1_id, object2, object2_id, changes, summary, organization_id)
		SELECT x.op, x.typ, x.id, 'inventory', i.id, jsonb_build_object('name', x.name),
			jsonb_build_object(x.typ, jsonb_build_array(jsonb_build_object('id', x.id, 'name', x.name)),
				'inventory', jsonb_build_array(jsonb_build_object('id', i.id, 'name', i.name))), i.organization_id
		FROM unnest($2::text[], $3::text[], $4::bigint[], $5::text[]) AS x(op, typ, id, name), inventories i WHERE i.id = $1`,
		invID, ops, types, ids, names)
	return err
}

// mergeVars merges imported variables into stored variables text. It reports
// whether the result differs from what is stored.
func mergeVars(cur string, in map[string]any, overwrite bool) (string, bool) {
	existing, err := vars.Parse(cur)
	if err != nil {
		existing = map[string]any{}
	}
	next := map[string]any{}
	if !overwrite {
		for k, v := range existing {
			next[k] = v
		}
	}
	for k, v := range in {
		next[k] = v
	}
	a, _ := json.Marshal(existing)
	b, _ := json.Marshal(next)
	if string(a) == string(b) {
		return cur, false
	}
	return varsText(next), true
}

func varsText(v map[string]any) string {
	if len(v) == 0 {
		return ""
	}
	return vars.MustJSON(v)
}

// hostEnabled evaluates enabled_var/enabled_value against host variables.
// enabled_var may be a dotted path into nested dicts.
func hostEnabled(hv map[string]any, enabledVar, enabledValue string) (bool, bool) {
	if enabledVar == "" {
		return false, false
	}
	var cur any = hv
	for _, part := range strings.Split(enabledVar, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false, false
		}
		if cur, ok = m[part]; !ok {
			return false, false
		}
	}
	if enabledValue != "" {
		return fmt.Sprint(cur) == enabledValue, true
	}
	switch x := cur.(type) {
	case bool:
		return x, true
	case string:
		s := strings.ToLower(x)
		return s != "" && s != "false" && s != "0" && s != "no" && s != "off", true
	case float64:
		return x != 0, true
	case nil:
		return false, true
	}
	return true, true
}
