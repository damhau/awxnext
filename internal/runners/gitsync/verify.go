package gitsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/projectsign"
	"github.com/damhau/awxnext/internal/resources"
)

// verifyPlay is the name of AWX's verification play in project_update.yml.
const verifyPlay = "Perform project signature/checksum verification"

// verifyContent runs AWX's verification play when the project has a
// signature validation credential: the GPG signature of the ansible-sign
// manifest, then the checkout's files against it. Like AWX it reads the
// credential from the project when the update runs, and only project
// updates verify, not the checkouts of jobs.
func verifyContent(ctx context.Context, rc *jobs.RunContext, project int64, dir string) error {
	app := rc.M.App
	var credID *int64
	if err := app.DB.QueryRow(ctx, `SELECT signature_validation_credential_id FROM unified_job_templates WHERE id = $1`, project).Scan(&credID); err != nil {
		return err
	}
	if credID == nil {
		return nil
	}
	cred, err := resources.ResolveCredential(ctx, app, *credID)
	if err != nil {
		return err
	}
	key, _ := cred.Inputs["gpg_public_key"].(string)
	emit := func(event, task, stdout string, failed bool) {
		rc.Out.Emit(jobs.Event{Event: event, Playbook: "project_update.yml", Play: verifyPlay, Task: task,
			HostName: "localhost", Stdout: stdout, Failed: failed})
	}
	emit("playbook_on_play_start", "", "PLAY ["+verifyPlay+"] "+strings.Repeat("*", 20), false)
	for _, step := range []struct {
		task  string
		check func() error
	}{
		{"Verify project content using GPG signature", func() error { return projectsign.VerifyGPG(dir, key) }},
		{"Verify project content against checksum manifest", func() error { return projectsign.VerifyChecksums(dir) }},
	} {
		emit("playbook_on_task_start", step.task, "TASK ["+step.task+"] "+strings.Repeat("*", 20), false)
		if err := step.check(); err != nil {
			msg, _ := json.Marshal(map[string]any{"changed": false, "msg": err.Error()})
			emit("runner_on_failed", step.task, fmt.Sprintf("fatal: [localhost]: FAILED! => %s", msg), true)
			return err
		}
		emit("runner_on_ok", step.task, "ok: [localhost]", false)
	}
	return nil
}
