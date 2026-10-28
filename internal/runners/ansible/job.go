package ansible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/runnercmd"
	"github.com/damhau/awxnext/internal/runners/gitsync"
	"github.com/damhau/awxnext/internal/settings"
	"github.com/damhau/awxnext/internal/vars"
	"gopkg.in/yaml.v3"
)

// Register installs the job type.
func Register() {
	jobs.Register(&jobs.TypeSpec{
		Type:         "job",
		TemplateType: "job_template",
		Runner:       jobs.RunnerFunc(runJob),
		Serial:       true,
		Impact:       jobImpact,
		Dependencies: func(ctx context.Context, m *jobs.Manager, row jobs.Row) ([]int64, error) {
			deps, err := jobDependencies(ctx, m, row)
			if err != nil {
				return nil, err
			}
			inv, err := inventorySourceDependencies(ctx, m, row.Int("inventory_id"))
			return append(deps, inv...), err
		},
	})
	registerInventoryUpdate()
	registerAdHoc()
	if d := settings.Lookup("AWX_ISOLATION_SHOW_PATHS"); d != nil {
		d.Validate = func(v any) error {
			list, _ := v.([]any)
			for _, p := range list {
				if _, _, _, err := runnercmd.Mount(fmt.Sprint(p)); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

// LoadStartArgs decrypts a job's start_args.
func LoadStartArgs(app *api.App, row jobs.Row) resources.StartArgs {
	var sa resources.StartArgs
	if enc := row.Str("start_args"); enc != "" {
		if plain, err := app.Crypto.Decrypt(enc); err == nil {
			json.Unmarshal([]byte(plain), &sa)
		}
	}
	return sa
}

func systemCtx(ctx context.Context, app *api.App) *api.Ctx {
	return &api.Ctx{Context: ctx, App: app, User: rbac.System(), Extra: map[string]any{}}
}

// jobImpact follows AWX: min(forks, hosts) + 1.
func jobImpact(ctx context.Context, m *jobs.Manager, row jobs.Row) int {
	forks := int(row.Int("forks"))
	if forks <= 0 {
		forks = 5
	}
	var hosts int
	if inv := row.Int("inventory_id"); inv != 0 {
		m.DB().QueryRow(ctx, `SELECT (SELECT count(*) FROM hosts WHERE inventory_id = $1 AND enabled)
			+ (SELECT count(*) FROM smart_inventory_hosts($1))`, inv).Scan(&hosts)
	}
	if hosts < forks {
		forks = hosts
	}
	return forks + 1
}

// jobDependencies launches a project sync when the project updates on launch
// (respecting its cache timeout) or has never been synced.
func jobDependencies(ctx context.Context, m *jobs.Manager, row jobs.Row) ([]int64, error) {
	pid := row.Int("project_id")
	if pid == 0 {
		return nil, nil
	}
	c := systemCtx(ctx, m.App)
	proj, err := api.Get(c, api.Lookup("projects"), pid)
	if err != nil {
		return nil, nil
	}
	if proj["scm_type"] != "git" {
		return nil, nil
	}
	needed := proj["scm_revision"] == ""
	if !needed && proj["scm_update_on_launch"] == true {
		needed = true
		if cache, _ := api.ID(proj["scm_update_cache_timeout"]); cache > 0 {
			var recent bool
			m.DB().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM unified_jobs WHERE type = 'project_update' AND unified_job_template_id = $1
				AND job_type = 'check' AND status = 'successful' AND finished > now() - make_interval(secs => $2))`, pid, cache).Scan(&recent)
			needed = !recent
		}
	}
	if !needed {
		return nil, nil
	}
	// Reuse a sync that is already queued or running.
	var existing int64
	if err := m.DB().QueryRow(ctx, `SELECT id FROM unified_jobs WHERE type = 'project_update' AND unified_job_template_id = $1
		AND job_type = 'check' AND status IN ('pending', 'waiting', 'running') ORDER BY id DESC LIMIT 1`, pid).Scan(&existing); err == nil {
		return []int64{existing}, nil
	}
	id, err := resources.LaunchProjectUpdate(c, proj, "dependency", "check", "")
	if err != nil {
		return nil, err
	}
	return []int64{id}, nil
}

func fail(explanation string) jobs.Result {
	return jobs.Result{Status: "failed", Explanation: explanation}
}

func runJob(ctx context.Context, rc *jobs.RunContext) jobs.Result {
	row, app := rc.Job, rc.M.App
	id := row.ID()
	run, err := NewRunFor(ctx, app, fmt.Sprint(id), row)
	if err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}
	defer run.Cleanup()
	update := map[string]any{"start_args": ""}

	// Project content.
	projectDir, rev, err := checkoutProject(ctx, app, row)
	if err != nil {
		return fail(err.Error())
	}
	if rev != "" {
		update["scm_revision"] = rev
	}
	playbook := row.Str("playbook")
	if _, err := os.Stat(filepath.Join(projectDir, playbook)); err != nil || strings.Contains(playbook, "..") {
		return fail(fmt.Sprintf("Playbook %q not found in project.", playbook))
	}
	run.Mounts = append(run.Mounts, Mount{Prefix: "project", Path: projectDir})
	if rev != "" {
		req, err := InstallRequirements(ctx, app, rc.Out, row, row.Int("project_id"), projectDir)
		if err != nil {
			return fail("Project requirements: " + err.Error())
		}
		req.Mount(run)
	}

	// Inventory as an executable script (works in any execution environment).
	if err := writeInventory(ctx, app, run, row.Int("inventory_id"), int(row.Int("job_slice_number")), int(row.Int("job_slice_count"))); err != nil {
		return fail("Inventory: " + err.Error())
	}

	sa := LoadStartArgs(app, row)
	creds, err := jobCredentials(ctx, app, id)
	if err != nil {
		return fail(err.Error())
	}
	inj, err := Inject(run, creds, sa.CredentialPasswords)
	if err != nil {
		return fail(err.Error())
	}
	extra, err := jobExtraVars(ctx, app, row, inj)
	if err != nil {
		return fail("Extra vars: " + err.Error())
	}
	if err := writeExtraVars(run, extra, unsafeVars(ctx, app, sa)); err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}

	args := append([]string{}, inj.Args...)
	if l := row.Str("limit"); l != "" {
		args = append(args, "--limit", l)
	}
	if t := row.Str("job_tags"); t != "" {
		args = append(args, "--tags", t)
	}
	if t := row.Str("skip_tags"); t != "" {
		args = append(args, "--skip-tags", t)
	}
	if t := row.Str("start_at_task"); t != "" {
		args = append(args, "--start-at-task", t)
	}
	if f := row.Int("forks"); f > 0 {
		args = append(args, "--forks", fmt.Sprint(f))
	}
	if v := row.Int("verbosity"); v > 0 {
		args = append(args, "-"+strings.Repeat("v", int(min(v, 5))))
	}
	if row.Str("job_type") == "check" {
		args = append(args, "--check")
	}
	if row.Bool("diff_mode") {
		args = append(args, "--diff")
	}
	if row.Bool("force_handlers") {
		args = append(args, "--force-handlers")
	}
	if row.Bool("become_enabled") {
		args = append(args, "--become")
	}
	if err := run.WriteFile("env/cmdline", []byte(ShellQuote(args)), 0o600); err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}
	env := baseEnv(ctx, app, run, row)
	for k, v := range inj.Env {
		env[k] = v
	}
	var facts seededFacts
	if row.Bool("use_fact_cache") {
		var err error
		if facts, err = prepareFactCache(ctx, app, run, row, env); err != nil {
			return jobs.Result{Status: "error", Explanation: err.Error()}
		}
	}
	if err := run.WriteJSON("env/envvars", env); err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}
	if len(inj.Passwords) > 0 {
		run.WriteJSON("env/passwords", inj.Passwords)
	}
	if inj.SSHKey != "" {
		run.WriteFile("env/ssh_key", []byte(inj.SSHKey), 0o600)
	}
	run.WriteJSON("env/settings", runnerSettings(ctx, app))
	run.Kwargs = map[string]any{"ident": fmt.Sprint(id), "playbook": playbook}
	if err := setImage(ctx, app, run, row); err != nil {
		return fail(err.Error())
	}
	timeout := time.Duration(row.Int("timeout")) * time.Second
	if timeout == 0 {
		timeout = time.Duration(app.Settings.Int(ctx, "DEFAULT_JOB_TIMEOUT")) * time.Second
	}
	run.Timeout = timeout

	update["job_args"] = "ansible-playbook " + ShellQuote(redactArgs(args)) + " " + playbook
	update["job_cwd"] = run.Path("project")
	update["job_env"] = env

	oc, err := run.Execute(ctx, app, rc.Out)
	if oc != nil && oc.Stats != nil {
		if serr := recordHostSummaries(ctx, app, row, oc.Stats); serr != nil {
			rc.Out.Emit(jobs.Event{Event: "system_warning", Stdout: "Could not record host summaries: " + serr.Error()})
		}
		if ad, ok := oc.Stats["artifact_data"].(map[string]any); ok {
			update["artifacts"] = ad
		}
	}
	if oc != nil && row.Bool("use_fact_cache") {
		saveFactCache(ctx, app, row, oc.Artifacts, fmt.Sprint(id), facts)
	}
	if err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error(), Update: update}
	}
	res := jobs.Result{Update: update}
	switch oc.Status {
	case "successful":
		res.Status = "successful"
	case "timeout":
		res.Status, res.Explanation = "failed", "Job terminated due to timeout"
	case "canceled":
		res.Status = "canceled"
	case "failed":
		res.Status = "failed"
	default:
		res.Status = "error"
	}
	return res
}

func redactArgs(args []string) []string {
	return args // passwords never appear on the command line; they go through env/passwords
}

// checkoutProject returns the directory holding the project content for a job.
func checkoutProject(ctx context.Context, app *api.App, row jobs.Row) (string, string, error) {
	pid := row.Int("project_id")
	if pid == 0 {
		return "", "", errors.New("Job has no project.")
	}
	var scmType, url, localPath, branch, revision, refspec string
	var credID *int64
	var track, clean bool
	err := app.DB.QueryRow(ctx, `SELECT scm_type, scm_url, local_path, scm_branch, scm_revision, credential_id, scm_track_submodules, scm_refspec, scm_clean
		FROM unified_job_templates WHERE id = $1`, pid).Scan(&scmType, &url, &localPath, &branch, &revision, &credID, &track, &refspec, &clean)
	if err != nil {
		return "", "", errors.New("The project for this job no longer exists.")
	}
	if scmType == "" {
		dir := filepath.Join(app.Cfg.ProjectsRoot, localPath)
		if localPath == "" || strings.Contains(localPath, "..") {
			return "", "", errors.New("The manual project has no local path.")
		}
		return dir, "", nil
	}
	want := row.Str("scm_branch")
	if want == "" || want == branch {
		// Use exactly the revision the project was last synced to.
		want = revision
	}
	if want == "" {
		return "", "", errors.New("The project has not been synced yet.")
	}
	// The refspec fetches refs a branch override may name, such as pull requests.
	src := gitsync.Source{ProjectID: pid, URL: url, Branch: want, Refspec: refspec, TrackSubmodules: track, Clean: clean}
	if credID != nil {
		src.CredentialID = *credID
	}
	var log []string
	sha, dir, err := gitsync.Sync(ctx, app, src, func(s string) { log = append(log, s) })
	if err != nil {
		return "", "", fmt.Errorf("Project checkout of %q failed: %v\n%s", want, err, strings.Join(log, "\n"))
	}
	return dir, sha, nil
}

func writeInventory(ctx context.Context, app *api.App, run *Run, inv int64, sliceNumber, sliceCount int) error {
	if inv == 0 {
		return errors.New("Job has no inventory.")
	}
	script, err := resources.InventoryScript(ctx, app, inv, resources.ScriptOptions{HostVars: true, TowerVars: true,
		SliceNumber: sliceNumber, SliceCount: sliceCount})
	if err != nil {
		return err
	}
	b, err := json.Marshal(script)
	if err != nil {
		return err
	}
	body := "#!/bin/sh\ncat <<'AWXNEXT_INVENTORY_EOF'\n" + string(b) + "\nAWXNEXT_INVENTORY_EOF\n"
	return run.WriteFile("inventory/hosts", []byte(body), 0o700)
}

func jobCredentials(ctx context.Context, app *api.App, jobID int64) ([]*resources.ResolvedCredential, error) {
	rows, err := app.DB.Query(ctx, "SELECT credential_id FROM unified_job_credentials WHERE unified_job_id = $1 ORDER BY credential_id", jobID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	var out []*resources.ResolvedCredential
	for _, id := range ids {
		rc, err := resources.ResolveCredential(ctx, app, id)
		if err != nil {
			return nil, err
		}
		out = append(out, rc)
	}
	return out, nil
}

// jobExtraVars merges the job's extra vars with AWX variables and credential-injected vars.
func jobExtraVars(ctx context.Context, app *api.App, row jobs.Row, inj *Injection) (map[string]any, error) {
	ev, err := vars.Parse(row.Str("extra_vars"))
	if err != nil {
		return nil, err
	}
	// Survey password answers are stored encrypted.
	for k, v := range ev {
		if s, ok := v.(string); ok && crypto.IsEncrypted(s) {
			if plain, err := app.Crypto.Decrypt(s); err == nil {
				ev[k] = plain
			}
		}
	}
	for k, v := range awxVars(ctx, app, row) {
		ev[k] = v
	}
	for k, v := range inj.ExtraVars {
		ev[k] = v
	}
	return ev, nil
}

func awxVars(ctx context.Context, app *api.App, row jobs.Row) map[string]any {
	v := map[string]any{
		"job_id":             row.ID(),
		"job_launch_type":    row.Str("launch_type"),
		"project_revision":   row.Str("scm_revision"),
		"project_scm_branch": row.Str("scm_branch"),
		"execution_node":     row.Str("execution_node"),
		"inventory_id":       row["inventory_id"],
		"job_template_id":    row["unified_job_template_id"],
		"job_template_name":  row.Str("name"),
	}
	var invName string
	app.DB.QueryRow(ctx, "SELECT name FROM inventories WHERE id = $1", row["inventory_id"]).Scan(&invName)
	v["inventory_name"] = invName
	if uid := row.Int("created_by_id"); uid != 0 {
		var name, email, first, last string
		app.DB.QueryRow(ctx, "SELECT username, email, first_name, last_name FROM users WHERE id = $1", uid).Scan(&name, &email, &first, &last)
		v["user_id"], v["user_name"], v["user_email"], v["user_first_name"], v["user_last_name"] = uid, name, email, first, last
	}
	if sched := row.Int("schedule_id"); sched != 0 {
		v["schedule_id"] = sched
	}
	out := map[string]any{}
	for k, val := range v {
		out["awx_"+k] = val
		out["tower_"+k] = val
	}
	return out
}

func unsafeVars(ctx context.Context, app *api.App, sa resources.StartArgs) map[string]bool {
	out := map[string]bool{}
	switch app.Settings.String(ctx, "ALLOW_JINJA_IN_EXTRA_VARS") {
	case "always":
		return out
	}
	for _, k := range sa.LaunchVars {
		out[k] = true
	}
	return out
}

// writeExtraVars writes env/extravars as YAML, tagging launch-provided values
// containing Jinja as !unsafe so they are not templated.
func writeExtraVars(run *Run, ev map[string]any, unsafe map[string]bool) error {
	root := &yaml.Node{Kind: yaml.MappingNode}
	keys := make([]string, 0, len(ev))
	for k := range ev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var val yaml.Node
		if err := val.Encode(ev[k]); err != nil {
			return err
		}
		if unsafe[k] {
			markUnsafe(&val)
		}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &val)
	}
	b, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return run.WriteFile("env/extravars", b, 0o600)
}

func markUnsafe(n *yaml.Node) {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!str" || n.Tag == "" {
			if strings.Contains(n.Value, "{{") || strings.Contains(n.Value, "{%") || strings.Contains(n.Value, "{#") {
				n.Tag = "!unsafe"
				n.Style = yaml.SingleQuotedStyle
			}
		}
	default:
		for _, c := range n.Content {
			markUnsafe(c)
		}
	}
}

// runnerSettings are ansible-runner's settings for playbook and ad hoc runs.
// DEFAULT_JOB_IDLE_TIMEOUT stops a run that printed nothing for that long;
// the runner checks it each time it stops waiting for output, every
// pexpect_timeout seconds at most.
func runnerSettings(ctx context.Context, app *api.App) map[string]any {
	s := map[string]any{"suppress_ansible_output": true, "suppress_output_file": true, "pexpect_timeout": 10}
	if n := app.Settings.Int(ctx, "DEFAULT_JOB_IDLE_TIMEOUT"); n > 0 {
		s["idle_timeout"], s["pexpect_timeout"] = n, min(n, 10)
	}
	return s
}

func baseEnv(ctx context.Context, app *api.App, run *Run, row jobs.Row) map[string]string {
	env := map[string]string{
		"ANSIBLE_FORCE_COLOR":               "True",
		"ANSIBLE_HOST_KEY_CHECKING":         "False",
		"ANSIBLE_INVENTORY_UNPARSED_FAILED": "True",
		"ANSIBLE_PARAMIKO_RECORD_HOST_KEYS": "False",
		"ANSIBLE_RETRY_FILES_ENABLED":       "False",
		"ANSIBLE_SSH_CONTROL_PATH_DIR":      run.Path("cp"),
		"ANSIBLE_ROLES_PATH":                run.Path("requirements_roles") + ":" + run.Path("project/roles") + ":~/.ansible/roles:/usr/share/ansible/roles:/etc/ansible/roles",
		"ANSIBLE_COLLECTIONS_PATH":          run.Path("requirements_collections") + ":~/.ansible/collections:/usr/share/ansible/collections",
		"ANSIBLE_DEPRECATION_WARNINGS":      "False",
		"JOB_ID":                            fmt.Sprint(row.ID()),
		"INVENTORY_ID":                      fmt.Sprint(row.Int("inventory_id")),
		"PROJECT_REVISION":                  row.Str("scm_revision"),
		"AWX_HOST":                          app.Settings.String(ctx, "TOWER_URL_BASE"),
	}
	if env["AWX_HOST"] == "" {
		env["AWX_HOST"] = app.Cfg.BaseURL
	}
	// AWX_ANSIBLE_CALLBACK_PLUGINS: more directories with callback plugins.
	if dirs, _ := app.Settings.Get(ctx, "AWX_ANSIBLE_CALLBACK_PLUGINS").([]any); len(dirs) > 0 {
		var paths []string
		for _, d := range dirs {
			paths = append(paths, fmt.Sprint(d))
		}
		env["ANSIBLE_CALLBACK_PLUGINS"] = strings.Join(paths, ":")
	}
	var extra map[string]any
	app.Settings.Decode(ctx, "AWX_TASK_ENV", &extra)
	for k, v := range extra {
		env[k] = fmt.Sprint(v)
	}
	return env
}

func setImage(ctx context.Context, app *api.App, run *Run, row jobs.Row) error {
	if run.runtime == "none" || run.runtime == "process" || run.runtime == "" {
		return nil
	}
	eeID := row.Int("execution_environment_id")
	var image, pull string
	var credID *int64
	if eeID != 0 {
		if err := app.DB.QueryRow(ctx, "SELECT image, pull, credential_id FROM execution_environments WHERE id = $1", eeID).Scan(&image, &pull, &credID); err != nil {
			return errors.New("The execution environment for this job no longer exists.")
		}
	} else {
		image = app.Cfg.DefaultEEImage
	}
	run.Image, run.Pull = image, pull
	if credID != nil {
		rc, err := resources.ResolveCredential(ctx, app, *credID)
		if err != nil {
			return err
		}
		run.RegistryHost, _ = rc.Inputs["host"].(string)
		run.RegistryUser, _ = rc.Inputs["username"].(string)
		run.RegistryPassword, _ = rc.Inputs["password"].(string)
	}
	return nil
}

// recordHostSummaries stores per-host results from playbook_on_stats.
func recordHostSummaries(ctx context.Context, app *api.App, row jobs.Row, stats map[string]any) error {
	counts := map[string]map[string]int{}
	for _, k := range []string{"changed", "dark", "failures", "ignored", "ok", "processed", "rescued", "skipped"} {
		m, _ := stats[k].(map[string]any)
		for host, v := range m {
			if counts[host] == nil {
				counts[host] = map[string]int{}
			}
			n, _ := v.(float64)
			counts[host][k] = int(n)
		}
	}
	jobID := row.ID()
	inv := row.Int("inventory_id")
	for host, c := range counts {
		failed := c["failures"] > 0 || c["dark"] > 0
		var hostID *int64
		var hid int64
		if err := app.DB.QueryRow(ctx, `SELECT id FROM hosts WHERE inventory_id = $1 AND name = $2
			UNION ALL SELECT h.id FROM hosts h WHERE h.name = $2 AND h.id IN (SELECT host_id FROM smart_inventory_hosts($1)) LIMIT 1`, inv, host).Scan(&hid); err == nil {
			hostID = &hid
		}
		var sid int64
		err := app.DB.QueryRow(ctx, `INSERT INTO job_host_summaries (job_id, host_id, host_name, changed, dark, failures, ignored, ok, processed, rescued, skipped, failed)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (job_id, host_name) DO UPDATE SET changed = EXCLUDED.changed, dark = EXCLUDED.dark, failures = EXCLUDED.failures,
				ignored = EXCLUDED.ignored, ok = EXCLUDED.ok, processed = EXCLUDED.processed, rescued = EXCLUDED.rescued,
				skipped = EXCLUDED.skipped, failed = EXCLUDED.failed, modified = now()
			RETURNING id`,
			jobID, hostID, host, c["changed"], c["dark"], c["failures"], c["ignored"], c["ok"], c["processed"], c["rescued"], c["skipped"], failed).Scan(&sid)
		if err != nil {
			return err
		}
		if hostID != nil {
			app.DB.Exec(ctx, "UPDATE hosts SET last_job_id = $2, last_job_host_summary_id = $3 WHERE id = $1", *hostID, jobID, sid)
		}
		app.DB.Exec(ctx, `INSERT INTO host_metrics (hostname, automated_counter) VALUES ($1, 1)
			ON CONFLICT (hostname) DO UPDATE SET last_automation = now(), automated_counter = host_metrics.automated_counter + 1, deleted = false`, host)
	}
	return nil
}
