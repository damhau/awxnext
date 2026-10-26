// Package importer copies the configuration of an existing AWX installation
// (read directly from its PostgreSQL database) into awxnext: users,
// organizations, teams, credentials (secrets decrypted with the AWX
// SECRET_KEY and re-encrypted with awxnext's keys), inventories, projects,
// job and workflow templates, schedules, notification templates and role
// grants. Job history is not imported.
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/schedule"
	"github.com/jackc/pgx/v5"
)

// Options configures an import.
type Options struct {
	SourceURL string
	SecretKey string
	// DryRun rolls back after importing (reports what would be imported).
	DryRun bool
	Log    func(format string, args ...any)
}

// Report summarizes an import.
type Report struct {
	Counts   map[string]int `json:"counts"`
	Warnings []string       `json:"warnings"`
}

type row = map[string]any

type imp struct {
	ctx  context.Context
	src  *pgx.Conn
	app  *api.App
	tx   pgx.Tx
	c    *api.Ctx
	opt  Options
	rep  *Report
	ids  map[string]map[int64]int64
	cols map[string]map[string]bool
	ct   map[int64]string // django content type id -> model
	ujt  map[int64]string // AWX unified job template id -> model
}

// Run imports an AWX database into the application database.
func Run(ctx context.Context, app *api.App, opt Options) (*Report, error) {
	if opt.Log == nil {
		opt.Log = func(string, ...any) {}
	}
	src, err := pgx.Connect(ctx, opt.SourceURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to the AWX database: %w", err)
	}
	defer src.Close(context.Background())
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	i := &imp{ctx: ctx, src: src, app: app, tx: tx, opt: opt, rep: &Report{Counts: map[string]int{}},
		ids: map[string]map[int64]int64{}, cols: map[string]map[string]bool{}, ct: map[int64]string{}, ujt: map[int64]string{}}
	i.c = &api.Ctx{Context: ctx, App: app, User: rbac.System(), Tx: tx, Extra: map[string]any{}}
	if err := i.loadSchema(); err != nil {
		return nil, err
	}
	steps := []struct {
		name string
		fn   func() error
	}{
		{"users", i.users}, {"organizations", i.organizations}, {"teams", i.teams},
		{"credential types", i.credentialTypes}, {"credentials", i.credentials}, {"execution environments", i.executionEnvironments},
		{"instance groups", i.instanceGroups}, {"labels", i.labels}, {"inventories", i.inventories},
		{"projects", i.projects}, {"inventory sources", i.inventorySources}, {"job templates", i.jobTemplates},
		{"workflows", i.workflows}, {"system job templates", i.systemJobTemplates}, {"template relations", i.templateRelations},
		{"schedules", i.schedules}, {"notification templates", i.notifications}, {"roles", i.roles},
	}
	for _, s := range steps {
		opt.Log("importing %s", s.name)
		if err := s.fn(); err != nil {
			return i.rep, fmt.Errorf("%s: %w", s.name, err)
		}
	}
	if err := i.finish(); err != nil {
		return i.rep, err
	}
	sort.Strings(i.rep.Warnings)
	if opt.DryRun {
		return i.rep, nil
	}
	return i.rep, tx.Commit(ctx)
}

func (i *imp) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	i.rep.Warnings = append(i.rep.Warnings, msg)
	i.opt.Log("warning: %s", msg)
}

func (i *imp) loadSchema() error {
	rows, err := i.src.Query(i.ctx, "SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema()")
	if err != nil {
		return err
	}
	for rows.Next() {
		var t, c string
		rows.Scan(&t, &c)
		if i.cols[t] == nil {
			i.cols[t] = map[string]bool{}
		}
		i.cols[t][c] = true
	}
	rows.Close()
	if !i.cols["main_organization"]["id"] || !i.cols["auth_user"]["id"] {
		return fmt.Errorf("the source database does not look like an AWX database")
	}
	cts, err := i.query("SELECT id, model FROM django_content_type")
	if err != nil {
		return err
	}
	for _, r := range cts {
		i.ct[id(r["id"])] = str(r["model"])
	}
	ujts, err := i.query("SELECT id, polymorphic_ctype_id FROM main_unifiedjobtemplate")
	if err != nil {
		return err
	}
	for _, r := range ujts {
		i.ujt[id(r["id"])] = i.ct[id(r["polymorphic_ctype_id"])]
	}
	return nil
}

// col returns "t.col" when the source column exists, else a fallback SQL literal.
func (i *imp) col(table, col, fallback string) string {
	if i.cols[table][col] {
		return "t." + col + " AS " + col
	}
	return fallback + " AS " + col
}

func (i *imp) has(table string) bool { return len(i.cols[table]) > 0 }

func (i *imp) query(sql string, args ...any) ([]row, error) {
	rows, err := i.src.Query(i.ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	fields := rows.FieldDescriptions()
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		r := row{}
		for k, f := range fields {
			r[f.Name] = vals[k]
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func id(v any) int64 {
	n, _ := api.ID(v)
	return n
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func boolv(v any) bool { b, _ := v.(bool); return b }

func (i *imp) mapID(table string, old, new int64) {
	if i.ids[table] == nil {
		i.ids[table] = map[int64]int64{}
	}
	i.ids[table][old] = new
}

// ref maps an AWX foreign key to the imported row (nil when absent).
func (i *imp) ref(table string, v any) any {
	old, ok := api.ID(v)
	if !ok {
		return nil
	}
	if n, ok := i.ids[table][old]; ok {
		return n
	}
	return nil
}

// newID keeps the AWX id when it is free in the target table.
func (i *imp) newID(table string, old int64) (int64, error) {
	var taken bool
	if err := i.tx.QueryRow(i.ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1)", old).Scan(&taken); err != nil {
		return 0, err
	}
	if !taken {
		return old, nil
	}
	for {
		var n int64
		if err := i.tx.QueryRow(i.ctx, "SELECT nextval(pg_get_serial_sequence($1, 'id'))", table).Scan(&n); err != nil {
			return 0, err
		}
		if err := i.tx.QueryRow(i.ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1)", n).Scan(&taken); err != nil {
			return 0, err
		}
		if !taken {
			return n, nil
		}
	}
}

// insert writes a row with an id chosen by newID and records the mapping.
func (i *imp) insert(table, mapName string, old int64, values map[string]any) (int64, error) {
	nid, err := i.newID(table, old)
	if err != nil {
		return 0, err
	}
	values["id"] = nid
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cols, phs, args := make([]string, len(keys)), make([]string, len(keys)), make([]any, len(keys))
	for k, key := range keys {
		cols[k] = pgx.Identifier{key}.Sanitize()
		phs[k] = fmt.Sprintf("$%d", k+1)
		args[k] = values[key]
	}
	if _, err := i.tx.Exec(i.ctx, "INSERT INTO "+table+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(phs, ", ")+")", args...); err != nil {
		return 0, fmt.Errorf("inserting %s %d: %w", mapName, old, err)
	}
	i.mapID(mapName, old, nid)
	i.rep.Counts[mapName]++
	return nid, nil
}

func (i *imp) audit(r row) map[string]any {
	return map[string]any{"created": r["created"], "modified": r["modified"],
		"created_by_id": i.ref("users", r["created_by_id"]), "modified_by_id": i.ref("users", r["modified_by_id"])}
}

func merge(a map[string]any, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

// --- users, organizations, teams ---

func (i *imp) users() error {
	rows, err := i.query(`SELECT id, username, first_name, last_name, email, password, is_superuser, is_active, last_login, date_joined FROM auth_user ORDER BY id`)
	if err != nil {
		return err
	}
	auditors := map[int64]bool{}
	if i.has("main_rbac_roles") {
		ar, err := i.query(`SELECT m.user_id FROM main_rbac_roles_members m JOIN main_rbac_roles r ON r.id = m.role_id WHERE r.singleton_name = 'system_auditor'`)
		if err != nil {
			return err
		}
		for _, r := range ar {
			auditors[id(r["user_id"])] = true
		}
	}
	for _, r := range rows {
		old := id(r["id"])
		var existing int64
		if err := i.tx.QueryRow(i.ctx, "SELECT id FROM users WHERE username = $1", r["username"]).Scan(&existing); err == nil {
			i.mapID("users", old, existing)
			i.warn("user %q already exists; kept the existing account", r["username"])
			continue
		}
		if _, err := i.insert("users", "users", old, map[string]any{"username": r["username"], "first_name": str(r["first_name"]),
			"last_name": str(r["last_name"]), "email": str(r["email"]), "password": str(r["password"]), "is_superuser": boolv(r["is_superuser"]),
			"is_active": boolv(r["is_active"]), "is_system_auditor": auditors[old], "last_login": r["last_login"], "created": r["date_joined"], "modified": r["date_joined"]}); err != nil {
			return err
		}
	}
	return nil
}

func (i *imp) organizations() error {
	rows, err := i.query(`SELECT t.id, t.name, t.description, t.created, t.modified, t.created_by_id, t.modified_by_id, ` +
		i.col("main_organization", "max_hosts", "0") + ` FROM main_organization t ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		var existing int64
		if err := i.tx.QueryRow(i.ctx, "SELECT id FROM organizations WHERE name = $1", r["name"]).Scan(&existing); err == nil {
			i.mapID("organizations", id(r["id"]), existing)
			i.warn("organization %q already exists; merged into it", r["name"])
			continue
		}
		if _, err := i.insert("organizations", "organizations", id(r["id"]), merge(i.audit(r), map[string]any{
			"name": r["name"], "description": str(r["description"]), "max_hosts": id(r["max_hosts"])})); err != nil {
			return err
		}
	}
	return nil
}

func (i *imp) teams() error {
	rows, err := i.query(`SELECT id, name, description, organization_id, created, modified, created_by_id, modified_by_id FROM main_team ORDER BY id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		org := i.ref("organizations", r["organization_id"])
		if org == nil {
			continue
		}
		var existing int64
		if err := i.tx.QueryRow(i.ctx, "SELECT id FROM teams WHERE organization_id = $1 AND name = $2", org, r["name"]).Scan(&existing); err == nil {
			i.mapID("teams", id(r["id"]), existing)
			continue
		}
		if _, err := i.insert("teams", "teams", id(r["id"]), merge(i.audit(r), map[string]any{
			"name": r["name"], "description": str(r["description"]), "organization_id": org})); err != nil {
			return err
		}
	}
	return nil
}

// --- credentials ---

func jsonMap(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case string:
		m := map[string]any{}
		json.Unmarshal([]byte(x), &m)
		return m
	case []byte:
		m := map[string]any{}
		json.Unmarshal(x, &m)
		return m
	}
	return map[string]any{}
}

func (i *imp) credentialTypes() error {
	managedCol := i.col("main_credentialtype", "managed", "false")
	if !i.cols["main_credentialtype"]["managed"] && i.cols["main_credentialtype"]["managed_by_tower"] {
		managedCol = "t.managed_by_tower AS managed"
	}
	rows, err := i.query(`SELECT t.id, t.name, t.description, t.kind, t.inputs, t.injectors, t.created, t.modified, t.created_by_id, t.modified_by_id, ` +
		managedCol + `, ` + i.col("main_credentialtype", "namespace", "NULL") + ` FROM main_credentialtype t ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		old := id(r["id"])
		if boolv(r["managed"]) {
			var ours int64
			if err := i.tx.QueryRow(i.ctx, "SELECT id FROM credential_types WHERE managed AND namespace = $1", str(r["namespace"])).Scan(&ours); err != nil {
				i.warn("managed credential type %q (%s) is not supported; its credentials are skipped", r["name"], str(r["namespace"]))
				continue
			}
			i.mapID("credential_types", old, ours)
			continue
		}
		var existing int64
		if err := i.tx.QueryRow(i.ctx, "SELECT id FROM credential_types WHERE name = $1 AND kind = $2", r["name"], r["kind"]).Scan(&existing); err == nil {
			i.mapID("credential_types", old, existing)
			continue
		}
		inputs, _ := json.Marshal(jsonMap(r["inputs"]))
		injectors, _ := json.Marshal(jsonMap(r["injectors"]))
		if _, err := i.insert("credential_types", "credential_types", old, merge(i.audit(r), map[string]any{
			"name": r["name"], "description": str(r["description"]), "kind": r["kind"], "inputs": string(inputs), "injectors": string(injectors)})); err != nil {
			return err
		}
	}
	return nil
}

// reencrypt turns AWX-encrypted strings into awxnext-encrypted ones.
func (i *imp) reencrypt(pk *int64, field, value string) (string, error) {
	if !strings.HasPrefix(value, "$encrypted$") {
		return value, nil
	}
	plain, err := DecryptAWX(i.opt.SecretKey, pk, field, value)
	if err != nil {
		return "", fmt.Errorf("decrypting %s: %w (is --secret-key the AWX SECRET_KEY?)", field, err)
	}
	return i.app.Crypto.Encrypt(plain)
}

func (i *imp) credentials() error {
	rows, err := i.query(`SELECT t.id, t.name, t.description, t.organization_id, t.credential_type_id, t.inputs, t.created, t.modified,
		t.created_by_id, t.modified_by_id, ` + i.col("main_credential", "managed", "false") + ` FROM main_credential t ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		old := id(r["id"])
		ct := i.ref("credential_types", r["credential_type_id"])
		if ct == nil {
			i.warn("credential %q skipped: unsupported credential type", r["name"])
			continue
		}
		inputs := jsonMap(r["inputs"])
		for k, v := range inputs {
			if s, ok := v.(string); ok {
				enc, err := i.reencrypt(&old, k, s)
				if err != nil {
					return fmt.Errorf("credential %q: %w", r["name"], err)
				}
				inputs[k] = enc
			}
		}
		b, _ := json.Marshal(inputs)
		if _, err := i.insert("credentials", "credentials", old, merge(i.audit(r), map[string]any{
			"name": r["name"], "description": str(r["description"]), "organization_id": i.ref("organizations", r["organization_id"]),
			"credential_type_id": ct, "inputs": string(b), "managed": boolv(r["managed"])})); err != nil {
			return err
		}
	}
	if i.has("main_credentialinputsource") {
		srcs, err := i.query(`SELECT id, description, input_field_name, metadata, source_credential_id, target_credential_id, created, modified,
			created_by_id, modified_by_id FROM main_credentialinputsource ORDER BY id`)
		if err != nil {
			return err
		}
		for _, r := range srcs {
			src, dst := i.ref("credentials", r["source_credential_id"]), i.ref("credentials", r["target_credential_id"])
			if src == nil || dst == nil {
				continue
			}
			meta, _ := json.Marshal(jsonMap(r["metadata"]))
			if _, err := i.insert("credential_input_sources", "credential_input_sources", id(r["id"]), merge(i.audit(r), map[string]any{
				"description": str(r["description"]), "input_field_name": r["input_field_name"], "metadata": string(meta),
				"source_credential_id": src, "target_credential_id": dst})); err != nil {
				return err
			}
		}
	}
	if i.has("main_organizationgalaxycredentialmembership") {
		ms, err := i.query("SELECT organization_id, credential_id, position FROM main_organizationgalaxycredentialmembership")
		if err != nil {
			return err
		}
		for _, r := range ms {
			org, cred := i.ref("organizations", r["organization_id"]), i.ref("credentials", r["credential_id"])
			if org != nil && cred != nil {
				i.tx.Exec(i.ctx, "INSERT INTO organization_galaxy_credentials (organization_id, credential_id, position) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
					org, cred, id(r["position"]))
			}
		}
	}
	return nil
}

func (i *imp) executionEnvironments() error {
	if !i.has("main_executionenvironment") {
		return nil
	}
	rows, err := i.query(`SELECT t.id, t.name, t.description, t.image, t.managed, t.organization_id, t.credential_id, t.pull, t.created, t.modified,
		t.created_by_id, t.modified_by_id FROM main_executionenvironment t ORDER BY t.id`)
	if err != nil {
		return err
	}
	var managedEE int64
	i.tx.QueryRow(i.ctx, "SELECT id FROM execution_environments WHERE managed ORDER BY id LIMIT 1").Scan(&managedEE)
	for _, r := range rows {
		old := id(r["id"])
		var existing int64
		if err := i.tx.QueryRow(i.ctx, "SELECT id FROM execution_environments WHERE image = $1 AND name = $2", r["image"], r["name"]).Scan(&existing); err == nil {
			i.mapID("execution_environments", old, existing)
			continue
		}
		if boolv(r["managed"]) && managedEE != 0 {
			i.mapID("execution_environments", old, managedEE)
			continue
		}
		if _, err := i.insert("execution_environments", "execution_environments", old, merge(i.audit(r), map[string]any{
			"name": r["name"], "description": str(r["description"]), "image": r["image"], "pull": str(r["pull"]),
			"organization_id": i.ref("organizations", r["organization_id"]), "credential_id": i.ref("credentials", r["credential_id"])})); err != nil {
			return err
		}
	}
	if i.cols["main_organization"]["default_environment_id"] {
		orgs, err := i.query("SELECT id, default_environment_id FROM main_organization WHERE default_environment_id IS NOT NULL")
		if err != nil {
			return err
		}
		for _, r := range orgs {
			if org, ee := i.ref("organizations", r["id"]), i.ref("execution_environments", r["default_environment_id"]); org != nil && ee != nil {
				i.tx.Exec(i.ctx, "UPDATE organizations SET default_environment_id = $2 WHERE id = $1", org, ee)
			}
		}
	}
	return nil
}

func (i *imp) instanceGroups() error {
	rows, err := i.query(`SELECT t.id, t.name, t.created, t.modified, ` + strings.Join([]string{
		i.col("main_instancegroup", "is_container_group", "false"), i.col("main_instancegroup", "credential_id", "NULL"),
		i.col("main_instancegroup", "pod_spec_override", "''"), i.col("main_instancegroup", "policy_instance_percentage", "0"),
		i.col("main_instancegroup", "policy_instance_minimum", "0"), i.col("main_instancegroup", "max_concurrent_jobs", "0"),
		i.col("main_instancegroup", "max_forks", "0")}, ", ") + ` FROM main_instancegroup t ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		var existing int64
		if err := i.tx.QueryRow(i.ctx, "SELECT id FROM instance_groups WHERE name = $1", r["name"]).Scan(&existing); err == nil {
			i.mapID("instance_groups", id(r["id"]), existing)
			continue
		}
		if _, err := i.insert("instance_groups", "instance_groups", id(r["id"]), map[string]any{"name": r["name"], "created": r["created"], "modified": r["modified"],
			"is_container_group": boolv(r["is_container_group"]), "credential_id": i.ref("credentials", r["credential_id"]),
			"pod_spec_override": str(r["pod_spec_override"]), "policy_instance_percentage": id(r["policy_instance_percentage"]),
			"policy_instance_minimum": id(r["policy_instance_minimum"]), "max_concurrent_jobs": id(r["max_concurrent_jobs"]), "max_forks": id(r["max_forks"])}); err != nil {
			return err
		}
	}
	return nil
}

func (i *imp) labels() error {
	rows, err := i.query(`SELECT id, name, organization_id, created, modified, created_by_id, modified_by_id FROM main_label ORDER BY id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		org := i.ref("organizations", r["organization_id"])
		if org == nil {
			continue
		}
		var existing int64
		if err := i.tx.QueryRow(i.ctx, "SELECT id FROM labels WHERE organization_id = $1 AND name = $2", org, r["name"]).Scan(&existing); err == nil {
			i.mapID("labels", id(r["id"]), existing)
			continue
		}
		if _, err := i.insert("labels", "labels", id(r["id"]), merge(i.audit(r), map[string]any{"name": r["name"], "organization_id": org})); err != nil {
			return err
		}
	}
	return nil
}

// --- inventories ---

func (i *imp) inventories() error {
	where := ""
	if i.cols["main_inventory"]["pending_deletion"] {
		where = " WHERE NOT t.pending_deletion"
	}
	rows, err := i.query(`SELECT t.id, t.name, t.description, t.organization_id, t.variables, t.created, t.modified, t.created_by_id, t.modified_by_id, ` +
		strings.Join([]string{i.col("main_inventory", "kind", "''"), i.col("main_inventory", "host_filter", "''"),
			i.col("main_inventory", "prevent_instance_group_fallback", "false")}, ", ") + ` FROM main_inventory t` + where + ` ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		org := i.ref("organizations", r["organization_id"])
		if org == nil {
			i.warn("inventory %q skipped: no organization", r["name"])
			continue
		}
		values := merge(i.audit(r), map[string]any{"name": r["name"], "description": str(r["description"]), "organization_id": org,
			"variables": str(r["variables"]), "kind": str(r["kind"]), "host_filter": str(r["host_filter"]),
			"prevent_instance_group_fallback": boolv(r["prevent_instance_group_fallback"])})
		if str(r["kind"]) == "smart" {
			compiled, err := resources.SmartFilterSQL(str(r["host_filter"]), api.SQLLiteral)
			if err != nil {
				i.warn("smart inventory %q: host filter %q not supported (%v); imported as a regular inventory", r["name"], r["host_filter"], err)
				values["kind"], values["host_filter"] = "", ""
			} else {
				values["host_filter_sql"] = compiled
			}
		}
		if _, err := i.insert("inventories", "inventories", id(r["id"]), values); err != nil {
			return err
		}
	}
	hosts, err := i.query(`SELECT t.id, t.name, t.description, t.inventory_id, t.enabled, t.instance_id, t.variables, t.created, t.modified,
		t.created_by_id, t.modified_by_id, ` + i.col("main_host", "ansible_facts", "'{}'::jsonb") + `, ` + i.col("main_host", "ansible_facts_modified", "NULL") +
		` FROM main_host t ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range hosts {
		inv := i.ref("inventories", r["inventory_id"])
		if inv == nil {
			continue
		}
		facts, _ := json.Marshal(jsonMap(r["ansible_facts"]))
		if _, err := i.insert("hosts", "hosts", id(r["id"]), merge(i.audit(r), map[string]any{"name": r["name"], "description": str(r["description"]),
			"inventory_id": inv, "enabled": boolv(r["enabled"]), "instance_id": str(r["instance_id"]), "variables": str(r["variables"]),
			"ansible_facts": string(facts), "ansible_facts_modified": r["ansible_facts_modified"]})); err != nil {
			return err
		}
	}
	groups, err := i.query(`SELECT id, name, description, inventory_id, variables, created, modified, created_by_id, modified_by_id FROM main_group ORDER BY id`)
	if err != nil {
		return err
	}
	for _, r := range groups {
		inv := i.ref("inventories", r["inventory_id"])
		if inv == nil {
			continue
		}
		if _, err := i.insert("groups", "groups", id(r["id"]), merge(i.audit(r), map[string]any{"name": r["name"], "description": str(r["description"]),
			"inventory_id": inv, "variables": str(r["variables"])})); err != nil {
			return err
		}
	}
	gh, err := i.query("SELECT group_id, host_id FROM main_group_hosts")
	if err != nil {
		return err
	}
	for _, r := range gh {
		if g, h := i.ref("groups", r["group_id"]), i.ref("hosts", r["host_id"]); g != nil && h != nil {
			i.tx.Exec(i.ctx, "INSERT INTO group_hosts (group_id, host_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", g, h)
		}
	}
	// Django's parents M2M: from_group is the child, to_group the parent.
	gp, err := i.query("SELECT from_group_id, to_group_id FROM main_group_parents")
	if err != nil {
		return err
	}
	for _, r := range gp {
		if child, parent := i.ref("groups", r["from_group_id"]), i.ref("groups", r["to_group_id"]); child != nil && parent != nil {
			i.tx.Exec(i.ctx, "INSERT INTO group_children (parent_id, child_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", parent, child)
		}
	}
	if i.has("main_inventoryconstructedinventorymembership") {
		ms, err := i.query("SELECT constructed_inventory_id, input_inventory_id, position FROM main_inventoryconstructedinventorymembership")
		if err != nil {
			return err
		}
		for _, r := range ms {
			if ci, in := i.ref("inventories", r["constructed_inventory_id"]), i.ref("inventories", r["input_inventory_id"]); ci != nil && in != nil {
				i.tx.Exec(i.ctx, "INSERT INTO inventory_input_inventories (inventory_id, input_inventory_id, position) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
					ci, in, id(r["position"]))
			}
		}
	}
	for _, l := range []struct{ table, col, ct, m string }{
		{"main_organizationinstancegroupmembership", "organization_id", "organization", "organizations"},
		{"main_inventoryinstancegroupmembership", "inventory_id", "inventory", "inventories"},
	} {
		if !i.has(l.table) {
			continue
		}
		ms, err := i.query("SELECT " + l.col + " AS obj, instancegroup_id, position FROM " + l.table)
		if err != nil {
			return err
		}
		for _, r := range ms {
			if obj, ig := i.ref(l.m, r["obj"]), i.ref("instance_groups", r["instancegroup_id"]); obj != nil && ig != nil {
				i.tx.Exec(i.ctx, "INSERT INTO instance_group_links (content_type, object_id, instance_group_id, position) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING",
					l.ct, obj, ig, id(r["position"]))
			}
		}
	}
	return nil
}

// --- unified job templates ---

func (i *imp) ujtBase(table string) string {
	return `SELECT u.id, u.name, u.description, u.created, u.modified, u.created_by_id, u.modified_by_id, ` +
		strings.Replace(i.col("main_unifiedjobtemplate", "organization_id", "NULL"), "t.", "u.", 1) + `, ` +
		strings.Replace(i.col("main_unifiedjobtemplate", "execution_environment_id", "NULL"), "t.", "u.", 1) +
		`, t.* FROM main_unifiedjobtemplate u JOIN ` + table + ` t ON t.unifiedjobtemplate_ptr_id = u.id ORDER BY u.id`
}

func (i *imp) ujtValues(r row, typ string) map[string]any {
	return merge(i.audit(r), map[string]any{"type": typ, "name": r["name"], "description": str(r["description"]),
		"organization_id": i.ref("organizations", r["organization_id"]), "execution_environment_id": i.ref("execution_environments", r["execution_environment_id"]),
		"status": "never updated"})
}

func (i *imp) projects() error {
	rows, err := i.query(i.ujtBase("main_project"))
	if err != nil {
		return err
	}
	for _, r := range rows {
		scm := str(r["scm_type"])
		if scm != "" && scm != "git" {
			i.warn("project %q skipped: %s projects are not supported (git and manual only)", r["name"], scm)
			continue
		}
		if scm == "" {
			i.warn("manual project %q: copy its playbooks to %s on the control nodes", r["name"], str(r["local_path"]))
		}
		v := merge(i.ujtValues(r, "project"), map[string]any{"scm_type": scm, "scm_url": str(r["scm_url"]), "scm_branch": str(r["scm_branch"]),
			"scm_refspec": str(r["scm_refspec"]), "scm_clean": boolv(r["scm_clean"]), "scm_delete_on_update": boolv(r["scm_delete_on_update"]),
			"scm_track_submodules": boolv(r["scm_track_submodules"]), "credential_id": i.ref("credentials", r["credential_id"]),
			"timeout": id(r["timeout"]), "scm_update_on_launch": boolv(r["scm_update_on_launch"]), "scm_update_cache_timeout": id(r["scm_update_cache_timeout"]),
			"allow_override": boolv(r["allow_override"]), "default_environment_id": i.ref("execution_environments", r["default_environment_id"]),
			"signature_validation_credential_id": i.ref("credentials", r["signature_validation_credential_id"])})
		if scm == "" {
			v["local_path"] = str(r["local_path"])
		}
		nid, err := i.insert("unified_job_templates", "projects", id(r["id"]), v)
		if err != nil {
			return err
		}
		i.mapID("ujt", id(r["id"]), nid)
		if scm == "git" {
			i.tx.Exec(i.ctx, "UPDATE unified_job_templates SET local_path = $2 WHERE id = $1", nid, resources.ProjectLocalPath(nid, str(r["name"])))
		}
	}
	return nil
}

var supportedSources = map[string]bool{"scm": true, "constructed": true, "ec2": true, "gce": true, "azure_rm": true, "vmware": true, "satellite6": true,
	"openstack": true, "rhv": true, "controller": true, "insights": true, "terraform": true, "openshift_virtualization": true}

func (i *imp) firstCredential(ujt int64) any {
	rows, err := i.query("SELECT credential_id FROM main_unifiedjobtemplate_credentials WHERE unifiedjobtemplate_id = $1 ORDER BY id LIMIT 1", ujt)
	if err != nil || len(rows) == 0 {
		return nil
	}
	return i.ref("credentials", rows[0]["credential_id"])
}

func (i *imp) inventorySources() error {
	rows, err := i.query(i.ujtBase("main_inventorysource"))
	if err != nil {
		return err
	}
	for _, r := range rows {
		inv := i.ref("inventories", r["inventory_id"])
		if inv == nil {
			continue
		}
		if !supportedSources[str(r["source"])] {
			i.warn("inventory source %q skipped: source %q is not supported", r["name"], r["source"])
			continue
		}
		var org any
		i.tx.QueryRow(i.ctx, "SELECT organization_id FROM inventories WHERE id = $1", inv).Scan(&org)
		v := merge(i.ujtValues(r, "inventory_source"), map[string]any{"inventory_id": inv, "organization_id": org, "source": r["source"],
			"source_path": str(r["source_path"]), "source_vars": str(r["source_vars"]), "overwrite": boolv(r["overwrite"]), "overwrite_vars": boolv(r["overwrite_vars"]),
			"timeout": id(r["timeout"]), "verbosity": id(r["verbosity"]), "update_on_launch": boolv(r["update_on_launch"]),
			"update_cache_timeout": id(r["update_cache_timeout"]), "source_project_id": i.ref("ujt", r["source_project_id"]),
			"enabled_var": str(r["enabled_var"]), "enabled_value": str(r["enabled_value"]), "host_filter": str(r["host_filter"]),
			"limit": str(r["limit"]), "scm_branch": str(r["scm_branch"]), "credential_id": i.firstCredential(id(r["id"]))})
		nid, err := i.insert("unified_job_templates", "inventory_sources", id(r["id"]), v)
		if err != nil {
			return err
		}
		i.mapID("ujt", id(r["id"]), nid)
	}
	return nil
}

// survey re-encrypts password defaults in a survey spec.
func (i *imp) survey(v any) (string, error) {
	spec := jsonMap(v)
	qs, _ := spec["spec"].([]any)
	for _, q := range qs {
		qm, ok := q.(map[string]any)
		if !ok || qm["type"] != "password" {
			continue
		}
		if d, ok := qm["default"].(string); ok && d != "" {
			enc, err := i.reencrypt(nil, "value", d)
			if err != nil {
				return "", err
			}
			qm["default"] = enc
		}
	}
	b, _ := json.Marshal(spec)
	return string(b), nil
}

func (i *imp) webhookKey(v any) (string, error) {
	k := str(v)
	if k == "" {
		return "", nil
	}
	return i.app.Crypto.Encrypt(k)
}

var jtAsk = []string{"ask_scm_branch_on_launch", "ask_diff_mode_on_launch", "ask_variables_on_launch", "ask_limit_on_launch", "ask_tags_on_launch",
	"ask_skip_tags_on_launch", "ask_job_type_on_launch", "ask_verbosity_on_launch", "ask_inventory_on_launch", "ask_credential_on_launch",
	"ask_execution_environment_on_launch", "ask_labels_on_launch", "ask_forks_on_launch", "ask_job_slice_count_on_launch", "ask_timeout_on_launch",
	"ask_instance_groups_on_launch"}

func (i *imp) jobTemplates() error {
	rows, err := i.query(i.ujtBase("main_jobtemplate"))
	if err != nil {
		return err
	}
	for _, r := range rows {
		survey, err := i.survey(r["survey_spec"])
		if err != nil {
			return fmt.Errorf("job template %q survey: %w", r["name"], err)
		}
		key, err := i.webhookKey(r["webhook_key"])
		if err != nil {
			return err
		}
		proj := i.ref("ujt", r["project_id"])
		var org any = i.ref("organizations", r["organization_id"])
		if proj != nil {
			i.tx.QueryRow(i.ctx, "SELECT organization_id FROM unified_job_templates WHERE id = $1", proj).Scan(&org)
		}
		v := merge(i.ujtValues(r, "job_template"), map[string]any{"organization_id": org, "job_type": str(r["job_type"]), "playbook": str(r["playbook"]),
			"forks": id(r["forks"]), "limit": str(r["limit"]), "verbosity": id(r["verbosity"]), "extra_vars": str(r["extra_vars"]),
			"job_tags": str(r["job_tags"]), "force_handlers": boolv(r["force_handlers"]), "skip_tags": str(r["skip_tags"]),
			"start_at_task": str(r["start_at_task"]), "become_enabled": boolv(r["become_enabled"]), "host_config_key": str(r["host_config_key"]),
			"survey_enabled": boolv(r["survey_enabled"]), "survey_spec": survey, "inventory_id": i.ref("inventories", r["inventory_id"]),
			"project_id": proj, "timeout": id(r["timeout"]), "use_fact_cache": boolv(r["use_fact_cache"]),
			"allow_simultaneous": boolv(r["allow_simultaneous"]), "diff_mode": boolv(r["diff_mode"]), "job_slice_count": max(id(r["job_slice_count"]), 1),
			"scm_branch": str(r["scm_branch"]), "webhook_service": str(r["webhook_service"]), "webhook_credential_id": i.ref("credentials", r["webhook_credential_id"]),
			"webhook_key": key, "prevent_instance_group_fallback": boolv(r["prevent_instance_group_fallback"])})
		for _, a := range jtAsk {
			v[a] = boolv(r[a])
		}
		if v["job_type"] == "" {
			v["job_type"] = "run"
		}
		nid, err := i.insert("unified_job_templates", "job_templates", id(r["id"]), v)
		if err != nil {
			return err
		}
		i.mapID("ujt", id(r["id"]), nid)
	}
	return nil
}

func (i *imp) workflows() error {
	if i.has("main_workflowapprovaltemplate") {
		rows, err := i.query(i.ujtBase("main_workflowapprovaltemplate"))
		if err != nil {
			return err
		}
		for _, r := range rows {
			nid, err := i.insert("unified_job_templates", "workflow_approval_templates", id(r["id"]),
				merge(i.ujtValues(r, "workflow_approval_template"), map[string]any{"timeout": id(r["timeout"])}))
			if err != nil {
				return err
			}
			i.mapID("ujt", id(r["id"]), nid)
		}
	}
	rows, err := i.query(i.ujtBase("main_workflowjobtemplate"))
	if err != nil {
		return err
	}
	for _, r := range rows {
		survey, err := i.survey(r["survey_spec"])
		if err != nil {
			return fmt.Errorf("workflow %q survey: %w", r["name"], err)
		}
		key, err := i.webhookKey(r["webhook_key"])
		if err != nil {
			return err
		}
		v := merge(i.ujtValues(r, "workflow_job_template"), map[string]any{"extra_vars": str(r["extra_vars"]), "survey_enabled": boolv(r["survey_enabled"]),
			"survey_spec": survey, "allow_simultaneous": boolv(r["allow_simultaneous"]), "inventory_id": i.ref("inventories", r["inventory_id"]),
			"limit": str(r["limit"]), "scm_branch": str(r["scm_branch"]), "job_tags": str(r["job_tags"]), "skip_tags": str(r["skip_tags"]),
			"webhook_service": str(r["webhook_service"]), "webhook_credential_id": i.ref("credentials", r["webhook_credential_id"]), "webhook_key": key})
		for _, a := range []string{"ask_inventory_on_launch", "ask_variables_on_launch", "ask_limit_on_launch", "ask_scm_branch_on_launch",
			"ask_labels_on_launch", "ask_skip_tags_on_launch", "ask_tags_on_launch"} {
			v[a] = boolv(r[a])
		}
		nid, err := i.insert("unified_job_templates", "workflow_job_templates", id(r["id"]), v)
		if err != nil {
			return err
		}
		i.mapID("ujt", id(r["id"]), nid)
	}
	nodes, err := i.query(`SELECT t.* FROM main_workflowjobtemplatenode t ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range nodes {
		wf := i.ref("ujt", r["workflow_job_template_id"])
		if wf == nil {
			continue
		}
		identifier := str(r["identifier"])
		if identifier == "" {
			identifier = fmt.Sprint(r["id"])
		}
		extra := jsonMap(r["extra_data"])
		for k, val := range extra {
			if s, ok := val.(string); ok && strings.HasPrefix(s, "$encrypted$") {
				enc, err := i.reencrypt(nil, "value", s)
				if err != nil {
					return err
				}
				extra[k] = enc
			}
		}
		ed, _ := json.Marshal(extra)
		v := map[string]any{"workflow_job_template_id": wf, "unified_job_template_id": i.ref("ujt", r["unified_job_template_id"]),
			"identifier": identifier, "all_parents_must_converge": boolv(r["all_parents_must_converge"]), "extra_data": string(ed),
			"inventory_id": i.ref("inventories", r["inventory_id"]), "execution_environment_id": i.ref("execution_environments", r["execution_environment_id"]),
			"created": r["created"], "modified": r["modified"]}
		for _, f := range []string{"scm_branch", "job_type", "job_tags", "skip_tags", "limit", "diff_mode", "verbosity", "forks", "job_slice_count", "timeout"} {
			if val, ok := r[f]; ok {
				v[f] = val
			}
		}
		if _, err := i.insert("workflow_job_template_nodes", "workflow_nodes", id(r["id"]), v); err != nil {
			return err
		}
	}
	for _, kind := range []string{"success", "failure", "always"} {
		table := "main_workflowjobtemplatenode_" + kind + "_nodes"
		if !i.has(table) {
			continue
		}
		edges, err := i.query("SELECT from_workflowjobtemplatenode_id AS f, to_workflowjobtemplatenode_id AS t FROM " + table)
		if err != nil {
			return err
		}
		for _, e := range edges {
			if f, t := i.ref("workflow_nodes", e["f"]), i.ref("workflow_nodes", e["t"]); f != nil && t != nil {
				i.tx.Exec(i.ctx, "INSERT INTO workflow_job_template_node_edges (from_node_id, to_node_id, kind) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", f, t, kind)
			}
		}
	}
	if i.has("main_workflowjobtemplatenode_credentials") {
		cs, err := i.query("SELECT workflowjobtemplatenode_id AS n, credential_id FROM main_workflowjobtemplatenode_credentials")
		if err != nil {
			return err
		}
		for _, r := range cs {
			if n, c := i.ref("workflow_nodes", r["n"]), i.ref("credentials", r["credential_id"]); n != nil && c != nil {
				i.tx.Exec(i.ctx, "INSERT INTO workflow_job_template_node_credentials (node_id, credential_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", n, c)
			}
		}
	}
	return nil
}

func (i *imp) systemJobTemplates() error {
	if !i.has("main_systemjobtemplate") {
		return nil
	}
	rows, err := i.query("SELECT unifiedjobtemplate_ptr_id AS id, job_type FROM main_systemjobtemplate")
	if err != nil {
		return err
	}
	for _, r := range rows {
		var ours int64
		if err := i.tx.QueryRow(i.ctx, "SELECT id FROM unified_job_templates WHERE type = 'system_job_template' AND job_type = $1", r["job_type"]).Scan(&ours); err == nil {
			i.mapID("ujt", id(r["id"]), ours)
			i.mapID("system_job_templates", id(r["id"]), ours)
		}
	}
	return nil
}

func (i *imp) templateRelations() error {
	if i.has("main_unifiedjobtemplate_credentials") {
		rows, err := i.query("SELECT unifiedjobtemplate_id AS u, credential_id FROM main_unifiedjobtemplate_credentials")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if i.ujt[id(r["u"])] != "jobtemplate" {
				continue
			}
			if u, c := i.ref("ujt", r["u"]), i.ref("credentials", r["credential_id"]); u != nil && c != nil {
				i.tx.Exec(i.ctx, "INSERT INTO unified_job_template_credentials (unified_job_template_id, credential_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", u, c)
			}
		}
	}
	if i.has("main_unifiedjobtemplate_labels") {
		rows, err := i.query("SELECT unifiedjobtemplate_id AS u, label_id FROM main_unifiedjobtemplate_labels")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if u, l := i.ref("ujt", r["u"]), i.ref("labels", r["label_id"]); u != nil && l != nil {
				i.tx.Exec(i.ctx, "INSERT INTO unified_job_template_labels (unified_job_template_id, label_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", u, l)
			}
		}
	}
	if i.has("main_unifiedjobtemplateinstancegroupmembership") {
		rows, err := i.query("SELECT unifiedjobtemplate_id AS u, instancegroup_id, position FROM main_unifiedjobtemplateinstancegroupmembership")
		if err != nil {
			return err
		}
		for _, r := range rows {
			ct := map[string]string{"jobtemplate": "jobtemplate", "inventorysource": "inventorysource"}[i.ujt[id(r["u"])]]
			if u, g := i.ref("ujt", r["u"]), i.ref("instance_groups", r["instancegroup_id"]); u != nil && g != nil && ct != "" {
				i.tx.Exec(i.ctx, "INSERT INTO instance_group_links (content_type, object_id, instance_group_id, position) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING",
					ct, u, g, id(r["position"]))
			}
		}
	}
	return nil
}

func (i *imp) schedules() error {
	rows, err := i.query(`SELECT t.* FROM main_schedule t ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		ujt := i.ref("ujt", r["unified_job_template_id"])
		if ujt == nil {
			continue
		}
		enabled := boolv(r["enabled"])
		rule, perr := schedule.Parse(str(r["rrule"]))
		var dtstart, dtend, next any
		if perr != nil {
			i.warn("schedule %q imported disabled: %v", r["name"], perr)
			enabled = false
		} else {
			if first, ok := rule.After(rule.Start.Add(-time.Second)); ok {
				dtstart = first.UTC()
			}
			if last, ok := rule.Last(); ok {
				dtend = last.UTC()
			}
			if enabled {
				if n, ok := rule.After(time.Now()); ok {
					next = n.UTC()
				}
			}
		}
		extra := jsonMap(r["extra_data"])
		for k, val := range extra {
			if s, ok := val.(string); ok && strings.HasPrefix(s, "$encrypted$") {
				enc, err := i.reencrypt(nil, "value", s)
				if err != nil {
					return err
				}
				extra[k] = enc
			}
		}
		ed, _ := json.Marshal(extra)
		if i.ujt[id(r["unified_job_template_id"])] == "systemjobtemplate" {
			// Built-in management jobs keep a single schedule: replace the default one.
			i.tx.Exec(i.ctx, "DELETE FROM schedules WHERE unified_job_template_id = $1", ujt)
		}
		v := merge(i.audit(r), map[string]any{"name": r["name"], "description": str(r["description"]), "unified_job_template_id": ujt,
			"enabled": enabled, "rrule": str(r["rrule"]), "dtstart": dtstart, "dtend": dtend, "next_run": next, "extra_data": string(ed),
			"inventory_id": i.ref("inventories", r["inventory_id"]), "execution_environment_id": i.ref("execution_environments", r["execution_environment_id"])})
		for _, f := range []string{"scm_branch", "job_type", "job_tags", "skip_tags", "limit", "diff_mode", "verbosity", "forks", "job_slice_count", "timeout"} {
			if val, ok := r[f]; ok {
				v[f] = val
			}
		}
		if _, err := i.insert("schedules", "schedules", id(r["id"]), v); err != nil {
			return err
		}
	}
	if i.has("main_schedule_credentials") {
		cs, err := i.query("SELECT schedule_id, credential_id FROM main_schedule_credentials")
		if err != nil {
			return err
		}
		for _, r := range cs {
			if s, c := i.ref("schedules", r["schedule_id"]), i.ref("credentials", r["credential_id"]); s != nil && c != nil {
				i.tx.Exec(i.ctx, "INSERT INTO schedule_credentials (schedule_id, credential_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", s, c)
			}
		}
	}
	return nil
}

func (i *imp) notifications() error {
	rows, err := i.query(`SELECT t.id, t.name, t.description, t.organization_id, t.notification_type, t.notification_configuration, t.created, t.modified,
		t.created_by_id, t.modified_by_id, ` + i.col("main_notificationtemplate", "messages", "NULL") + ` FROM main_notificationtemplate t ORDER BY t.id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		old := id(r["id"])
		if str(r["notification_type"]) != "webhook" {
			i.warn("notification template %q skipped: %s notifications are not supported (webhook only)", r["name"], r["notification_type"])
			continue
		}
		org := i.ref("organizations", r["organization_id"])
		if org == nil {
			continue
		}
		cfg := jsonMap(r["notification_configuration"])
		clean := map[string]any{}
		for _, k := range []string{"url", "username", "password", "http_method", "headers", "disable_ssl_verification"} {
			if v, ok := cfg[k]; ok && v != nil {
				clean[k] = v
			}
		}
		if p, ok := clean["password"].(string); ok && p != "" {
			enc, err := i.reencrypt(&old, "notification_configuration", p)
			if err != nil {
				return fmt.Errorf("notification template %q: %w", r["name"], err)
			}
			clean["password"] = enc
		}
		if m, _ := clean["http_method"].(string); m == "" {
			clean["http_method"] = "POST"
		}
		cb, _ := json.Marshal(clean)
		var msgs any
		if r["messages"] != nil {
			b, _ := json.Marshal(jsonMap(r["messages"]))
			msgs = string(b)
		}
		if _, err := i.insert("notification_templates", "notification_templates", old, merge(i.audit(r), map[string]any{
			"name": r["name"], "description": str(r["description"]), "organization_id": org, "notification_type": "webhook",
			"notification_configuration": string(cb), "messages": msgs})); err != nil {
			return err
		}
	}
	link := func(table, col, ct, mapName, event string, byUJT bool) error {
		if !i.has(table) {
			return nil
		}
		rs, err := i.query("SELECT " + col + " AS obj, notificationtemplate_id FROM " + table)
		if err != nil {
			return err
		}
		for _, r := range rs {
			kind := ct
			if byUJT {
				kind = map[string]string{"jobtemplate": "jobtemplate", "project": "project", "inventorysource": "inventorysource",
					"workflowjobtemplate": "workflowjobtemplate", "systemjobtemplate": "systemjobtemplate"}[i.ujt[id(r["obj"])]]
			}
			obj, nt := i.ref(mapName, r["obj"]), i.ref("notification_templates", r["notificationtemplate_id"])
			if obj != nil && nt != nil && kind != "" {
				i.tx.Exec(i.ctx, `INSERT INTO notification_template_links (content_type, object_id, notification_template_id, event)
					VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, kind, obj, nt, event)
			}
		}
		return nil
	}
	for _, ev := range []struct{ suffix, event string }{{"error", "error"}, {"success", "success"}, {"started", "started"}} {
		if err := link("main_unifiedjobtemplate_notification_templates_for_"+ev.suffix, "unifiedjobtemplate_id", "", "ujt", ev.event, true); err != nil {
			return err
		}
		if err := link("main_organization_notification_templates_for_"+ev.suffix, "organization_id", "organization", "organizations", ev.event, false); err != nil {
			return err
		}
	}
	if err := link("main_organization_notification_templates_for_approvals", "organization_id", "organization", "organizations", "approvals", false); err != nil {
		return err
	}
	return link("main_workflowjobtemplate_notification_templates_for_approvals", "workflowjobtemplate_id", "workflowjobtemplate", "ujt", "approvals", false)
}

// --- roles ---

// awxModels maps AWX content type models to awxnext content types and id maps.
var awxModels = map[string]struct{ ct, ids string }{
	"organization": {"organization", "organizations"}, "team": {"team", "teams"}, "credential": {"credential", "credentials"},
	"project": {"project", "ujt"}, "inventory": {"inventory", "inventories"}, "jobtemplate": {"jobtemplate", "ujt"},
	"workflowjobtemplate": {"workflowjobtemplate", "ujt"}, "instancegroup": {"instancegroup", "instance_groups"},
}

func (i *imp) roles() error {
	if !i.has("main_rbac_roles") {
		i.warn("no main_rbac_roles table: role grants were not imported")
		return nil
	}
	roles, err := i.query("SELECT id, role_field, content_type_id, object_id, implicit_parents FROM main_rbac_roles WHERE content_type_id IS NOT NULL")
	if err != nil {
		return err
	}
	type role struct {
		field, model string
		obj          int64
		implicit     map[int64]bool
	}
	byID := map[int64]role{}
	teamOfMemberRole := map[int64]int64{}
	for _, r := range roles {
		ro := role{field: str(r["role_field"]), model: i.ct[id(r["content_type_id"])], obj: id(r["object_id"]), implicit: map[int64]bool{}}
		var ip []int64
		json.Unmarshal([]byte(str(r["implicit_parents"])), &ip)
		for _, p := range ip {
			ro.implicit[p] = true
		}
		byID[id(r["id"])] = ro
		if ro.model == "team" && ro.field == "member_role" {
			teamOfMemberRole[id(r["id"])] = ro.obj
		}
	}
	defs := map[string]string{} // ct/field -> role definition name
	for _, m := range rbac.Managed {
		if m.LegacyField != "" {
			defs[m.ContentType+"/"+m.LegacyField] = m.Name
		}
	}
	resolve := func(roleID int64) (def, ct string, obj int64, ok bool) {
		ro, found := byID[roleID]
		if !found {
			return "", "", 0, false
		}
		m, known := awxModels[ro.model]
		if !known {
			return "", "", 0, false
		}
		def = defs[m.ct+"/"+ro.field]
		target, mapped := api.ID(i.ref(m.ids, ro.obj))
		if def == "" || !mapped {
			return "", "", 0, false
		}
		return def, m.ct, target, true
	}
	members, err := i.query("SELECT role_id, user_id FROM main_rbac_roles_members")
	if err != nil {
		return err
	}
	for _, r := range members {
		def, ct, obj, ok := resolve(id(r["role_id"]))
		user, uok := api.ID(i.ref("users", r["user_id"]))
		if !ok || !uok {
			continue
		}
		if err := resources.Assign(i.c, def, user, 0, ct, obj); err != nil {
			return err
		}
		i.rep.Counts["user role grants"]++
	}
	parents, err := i.query("SELECT from_role_id, to_role_id FROM main_rbac_roles_parents")
	if err != nil {
		return err
	}
	for _, r := range parents {
		roleID, parentID := id(r["from_role_id"]), id(r["to_role_id"])
		team, isTeam := teamOfMemberRole[parentID]
		if !isTeam || byID[roleID].implicit[parentID] {
			continue
		}
		def, ct, obj, ok := resolve(roleID)
		newTeam, tok := api.ID(i.ref("teams", team))
		if !ok || !tok {
			continue
		}
		if err := resources.Assign(i.c, def, 0, newTeam, ct, obj); err != nil {
			return err
		}
		i.rep.Counts["team role grants"]++
	}
	return nil
}

func (i *imp) finish() error {
	for _, t := range []string{"users", "organizations", "teams", "credential_types", "credentials", "credential_input_sources", "execution_environments",
		"instance_groups", "labels", "inventories", "hosts", "groups", "unified_job_templates", "workflow_job_template_nodes", "schedules", "notification_templates"} {
		if _, err := i.tx.Exec(i.ctx, fmt.Sprintf("SELECT setval(pg_get_serial_sequence('%s', 'id'), greatest((SELECT max(id) FROM %s), 1))", t, t)); err != nil {
			return err
		}
	}
	_, err := i.tx.Exec(i.ctx, `UPDATE unified_job_templates u SET (next_job_run, next_schedule_id) = (
		SELECT next_run, id FROM schedules WHERE unified_job_template_id = u.id AND enabled AND next_run IS NOT NULL ORDER BY next_run LIMIT 1)
		WHERE u.id IN (SELECT unified_job_template_id FROM schedules)`)
	if err != nil {
		return err
	}
	// Object roles for the imported objects (legacy role ids used by AWX clients).
	for _, res := range api.Resources() {
		if res.ContentType == "" || len(rbac.LegacyRoles(res.ContentType)) == 0 {
			continue
		}
		where := ""
		if res.Discriminator {
			where = " WHERE type = '" + res.Type + "'"
		}
		rows, err := i.tx.Query(i.ctx, "SELECT id FROM "+res.Table+where)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var n int64
			rows.Scan(&n)
			ids = append(ids, n)
		}
		rows.Close()
		for _, n := range ids {
			if err := rbac.EnsureObjectRoles(i.ctx, i.tx, res.ContentType, n); err != nil {
				return err
			}
		}
	}
	return nil
}
