package ansible

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/vars"
)

func registerAdHoc() {
	jobs.Register(&jobs.TypeSpec{
		Type:   "ad_hoc_command",
		Runner: jobs.RunnerFunc(runAdHoc),
		Impact: jobImpact,
		Dependencies: func(ctx context.Context, m *jobs.Manager, row jobs.Row) ([]int64, error) {
			return inventorySourceDependencies(ctx, m, row.Int("inventory_id"))
		},
	})
}

func runAdHoc(ctx context.Context, rc *jobs.RunContext) jobs.Result {
	row, app := rc.Job, rc.M.App
	id := row.ID()
	run, err := NewRunFor(ctx, app, fmt.Sprint(id), row)
	if err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}
	defer run.Cleanup()
	update := map[string]any{"start_args": ""}

	if err := writeInventory(ctx, app, run, row.Int("inventory_id"), 0, 0); err != nil {
		return fail("Inventory: " + err.Error())
	}
	sa := LoadStartArgs(app, row)
	var creds []*resources.ResolvedCredential
	if cid := row.Int("credential_id"); cid != 0 {
		cred, err := resources.ResolveCredential(ctx, app, cid)
		if err != nil {
			return fail(err.Error())
		}
		creds = append(creds, cred)
	}
	inj, err := Inject(run, creds, sa.CredentialPasswords)
	if err != nil {
		return fail(err.Error())
	}
	ev, err := vars.Parse(row.Str("extra_vars"))
	if err != nil {
		return fail("Extra vars: " + err.Error())
	}
	for k, v := range awxVars(ctx, app, row) {
		if !strings.Contains(k, "job_template") && !strings.Contains(k, "project_") {
			ev[k] = v
		}
	}
	for k, v := range inj.ExtraVars {
		ev[k] = v
	}
	if err := writeExtraVars(run, ev, unsafeVars(ctx, app, sa)); err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}

	args := append([]string{}, inj.Args...)
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
	if row.Bool("become_enabled") {
		args = append(args, "--become")
	}
	if err := run.WriteFile("env/cmdline", []byte(ShellQuote(args)), 0o600); err != nil {
		return jobs.Result{Status: "error", Explanation: err.Error()}
	}
	env := baseEnv(ctx, app, run, row)
	delete(env, "JOB_ID")
	delete(env, "PROJECT_REVISION")
	env["AD_HOC_COMMAND_ID"] = fmt.Sprint(id)
	for k, v := range inj.Env {
		env[k] = v
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
	pattern := row.Str("limit")
	if pattern == "" {
		pattern = "all"
	}
	module := row.Str("module_name")
	run.Kwargs = map[string]any{"ident": fmt.Sprint(id), "module": module, "module_args": row.Str("module_args"), "host_pattern": pattern}
	if err := setImage(ctx, app, run, row); err != nil {
		return fail(err.Error())
	}
	run.Timeout = time.Duration(app.Settings.Int(ctx, "DEFAULT_JOB_TIMEOUT")) * time.Second

	cmdArgs := append([]string{pattern, "-m", module}, args...)
	if a := row.Str("module_args"); a != "" {
		cmdArgs = append(cmdArgs, "-a", a)
	}
	update["job_args"] = "ansible " + ShellQuote(cmdArgs)
	update["job_cwd"] = run.Root
	update["job_env"] = env

	oc, err := run.Execute(ctx, app, rc.Out)
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
