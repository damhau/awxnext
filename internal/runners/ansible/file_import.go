package ansible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
)

// FileImport is `awxnext inventory-import`, the equivalent of AWX's
// `awx-manage inventory_import`: it loads an inventory file, directory or
// script with ansible-inventory on this machine and imports it as a `file`
// inventory source, recording an inventory update with its output.
type FileImport struct {
	InventoryName      string
	InventoryID        int64
	Source             string
	Overwrite          bool
	OverwriteVars      bool
	EnabledVar         string
	EnabledValue       string
	GroupFilter        string
	HostFilter         string
	ExcludeEmptyGroups bool
	InstanceIDVar      string
	Verbosity          int
	// Args is the command line, recorded on the update as AWX does.
	Args []string
}

// usageError is an error in the command's options or inventory (AWX's CommandError).
type usageError string

func (e usageError) Error() string { return string(e) }

// ImportFile runs a FileImport, logging to log. It returns the inventory
// update's id once one is recorded (also on failure).
func ImportFile(ctx context.Context, app *api.App, o FileImport, log io.Writer) (int64, error) {
	fail := func(format string, a ...any) (int64, error) {
		return 0, usageError(fmt.Sprintf(format, a...))
	}
	switch {
	case o.InventoryName != "" && o.InventoryID != 0:
		return fail("--inventory-name and --inventory-id are mutually exclusive")
	case o.InventoryName == "" && o.InventoryID == 0:
		return fail("--inventory-name or --inventory-id is required")
	case o.Source == "":
		return fail("--source is required")
	}
	for flag, re := range map[string]string{"--group-filter": o.GroupFilter, "--host-filter": o.HostFilter} {
		if _, err := regexp.Compile(re); err != nil {
			return fail("invalid regular expression for %s", flag)
		}
	}
	src, err := filepath.Abs(o.Source)
	if err != nil {
		return 0, err
	}
	if _, err := os.Stat(src); err != nil {
		return fail("Source does not exist: %s", o.Source)
	}

	var invID int64
	var invName string
	var org *int64
	q, arg, field := "SELECT id, name, organization_id FROM inventories WHERE id = $1", any(o.InventoryID), "id"
	if o.InventoryName != "" {
		q, arg, field = "SELECT id, name, organization_id FROM inventories WHERE name = $1", o.InventoryName, "name"
	}
	rows, err := app.DB.Query(ctx, q+" LIMIT 2", arg)
	if err != nil {
		return 0, err
	}
	found := 0
	for rows.Next() {
		found++
		rows.Scan(&invID, &invName, &org)
	}
	rows.Close()
	switch found {
	case 0:
		return fail("Inventory with %s = %v cannot be found", field, arg)
	case 2:
		return fail("Inventory with %s = %v returned multiple results", field, arg)
	}
	fmt.Fprintf(log, "Updating inventory %d: %s\n", invID, invName)

	// The source: one per inventory, path and overwrite options, as AWX's get_or_create.
	var sourceID int64
	err = app.DB.QueryRow(ctx, `SELECT id FROM unified_job_templates WHERE type = 'inventory_source' AND inventory_id = $1
		AND source = 'file' AND source_path = $2 AND overwrite = $3 AND overwrite_vars = $4 ORDER BY id LIMIT 1`,
		invID, src, o.Overwrite, o.OverwriteVars).Scan(&sourceID)
	if err != nil {
		err = app.DB.QueryRow(ctx, `INSERT INTO unified_job_templates (type, name, organization_id, inventory_id, source, source_path, overwrite, overwrite_vars, status)
			VALUES ('inventory_source', $1, $2, $3, 'file', $4, $5, $6, 'never updated') RETURNING id`,
			fmt.Sprintf("%s (%s - pending)", filepath.Base(src), invName), org, invID, src, o.Overwrite, o.OverwriteVars).Scan(&sourceID)
		if err != nil {
			return 0, err
		}
		app.DB.Exec(ctx, "UPDATE unified_job_templates SET name = $2 WHERE id = $1", sourceID, fmt.Sprintf("%s (%s - %d)", filepath.Base(src), invName, sourceID))
	}

	args, _ := json.Marshal(o.Args)
	cwd, _ := os.Getwd()
	env := map[string]string{}
	for _, kv := range os.Environ() {
		// Only Ansible's settings: the rest of the environment can hold secrets.
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "ANSIBLE_") {
			env[k] = v
		}
	}
	var name string
	app.DB.QueryRow(ctx, "SELECT name FROM unified_job_templates WHERE id = $1", sourceID).Scan(&name)
	updateID, err := jobs.Create(ctx, app.DB, map[string]any{
		"type": "inventory_update", "name": name, "unified_job_template_id": sourceID, "organization_id": org, "inventory_id": invID,
		"source": "file", "source_path": src, "overwrite": o.Overwrite, "overwrite_vars": o.OverwriteVars,
		"enabled_var": o.EnabledVar, "enabled_value": o.EnabledValue, "host_filter": o.HostFilter, "verbosity": o.Verbosity,
		"launch_type": "manual", "status": "running", "started": time.Now(),
		"job_args": string(args), "job_cwd": cwd, "job_env": env,
	})
	if err != nil {
		return 0, err
	}
	var created time.Time
	app.DB.QueryRow(ctx, "SELECT created FROM unified_jobs WHERE id = $1", updateID).Scan(&created)
	out := jobs.NewOutput(app.DB, updateID, created, nil, nil, nil)
	say := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		fmt.Fprintln(log, line)
		out.Line(line)
	}
	begin := time.Now()
	status, explanation := "successful", ""
	stats, err := loadAndImport(ctx, app, invID, sourceID, src, o, say)
	if err != nil {
		status, explanation = "failed", err.Error()
		say("Inventory import failed: %v", err)
	} else {
		say("Loaded %d groups, %d hosts", stats.Groups, stats.Hosts)
		say("Inventory import completed for %s in %.1fs", name, time.Since(begin).Seconds())
	}
	if err := out.Close(); err != nil {
		fmt.Fprintf(log, "Could not store the output: %v\n", err)
	}
	failed := status != "successful"
	app.DB.Exec(ctx, `UPDATE unified_jobs SET status = $2, failed = $3, finished = now(), modified = now(), event_processing_finished = true,
		elapsed = coalesce(extract(epoch FROM now() - started), 0), job_explanation = $4 WHERE id = $1`, updateID, status, failed, explanation)
	app.DB.Exec(ctx, `UPDATE unified_job_templates SET last_job_id = $2, last_job_failed = $3, last_job_run = now(), status = $4,
		current_job_id = NULL WHERE id = $1`, sourceID, updateID, failed, status)
	if err != nil {
		return updateID, err
	}
	return updateID, nil
}

func loadAndImport(ctx context.Context, app *api.App, invID, sourceID int64, src string, o FileImport, say func(string, ...any)) (*ImportStats, error) {
	args := []string{"-i", src, "--list", "--export", "--playbook-dir", functioningDir(src)}
	if o.Verbosity > 0 {
		args = append(args, "-"+strings.Repeat("v", min(5, o.Verbosity*2+1)))
	}
	cmd := exec.CommandContext(ctx, "ansible-inventory", args...)
	cmd.Env = append(os.Environ(), "ANSIBLE_INVENTORY_UNPARSED_FAILED=True")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	for _, l := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if l != "" {
			say("%s", l)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("ansible-inventory failed: %v", err)
	}
	data, err := ParseInventoryJSON(b)
	if err != nil {
		return nil, fmt.Errorf("invalid ansible-inventory output: %v", err)
	}
	stats, err := ImportInventory(ctx, app.DB, invID, sourceID, data, ImportOptions{
		Overwrite: o.Overwrite, OverwriteVars: o.OverwriteVars, EnabledVar: o.EnabledVar, EnabledValue: o.EnabledValue,
		GroupFilter: o.GroupFilter, HostFilter: o.HostFilter, InstanceIDVar: o.InstanceIDVar, ExcludeEmptyGroups: o.ExcludeEmptyGroups,
	})
	if err != nil {
		return nil, err
	}
	if err := recordSyncActivity(ctx, app, invID, stats); err != nil {
		say("Could not record the changes in the activity stream: %v", err)
	}
	return stats, nil
}

// functioningDir is where ansible-inventory looks for playbook-relative
// files: the source's directory, or the source itself when it is one.
func functioningDir(path string) string {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return path
	}
	return filepath.Dir(path)
}

// IsUsageError tells whether an ImportFile error is about its options.
func IsUsageError(err error) bool {
	var u usageError
	return errors.As(err, &u)
}
