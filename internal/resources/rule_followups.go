package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/eventstreams"
)

// Follow-ups of launches (roadmap M14-09): what ansible-rulebook's
// run_job_template does once its job ends. A rule with retries launches
// a failed job again, retry_delay seconds after it ended; a rule with
// post_artifacts posts the artifacts of the job it ends with (set_stats) as
// an event. Each launch is a row of event_rule_followups, opened in the
// transaction of the action; FollowJobs moves those whose job ended or
// whose delay passed, on whichever node runs it.

const (
	followWaiting  = "waiting"
	followRetrying = "retrying"
	followDone     = "done"
	followBatch    = 50
)

// openFollowup records a launch the rule follows.
func openFollowup(ctx context.Context, db pgx.Tx, r *eventRule, members []member, out outcome) error {
	if out.name != "launched" || out.job == nil || r.Template == nil || r.Retries == 0 && r.PostArtifacts == "" {
		return nil
	}
	depth, _ := api.ID(members[0].ev.Extensions[depthExtension])
	prompts := out.prompts
	if prompts == nil {
		prompts = Obj{}
	}
	_, err := db.Exec(ctx, `INSERT INTO event_rule_followups (rule_id, job_id, template_id, prompts, event, depth) VALUES ($1, $2, $3, $4, $5, $6)`,
		r.ID, *out.job, *r.Template, prompts, eventRef(members), depth)
	return err
}

// FollowJobs moves the follow-ups that are due: those whose job ended, and
// those whose retry delay passed.
func (e *RuleEngine) FollowJobs(ctx context.Context) error {
	rows, err := e.app.DB.Query(ctx, `SELECT f.id FROM event_rule_followups f LEFT JOIN unified_jobs j ON j.id = f.job_id
		WHERE f.state <> 'done' AND CASE f.state WHEN 'waiting' THEN j.id IS NULL OR j.status = ANY($1) ELSE f.due <= now() END
		ORDER BY f.id LIMIT `+strconv.Itoa(followBatch), finalJobStatus)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := e.follow(ctx, id); err != nil {
			if ctx.Err() != nil {
				return err
			}
			slog.Warn("event rules: following a job failed", "followup", id, "err", err)
		}
	}
	return nil
}

type followup struct {
	id, rule, template int64
	job                *int64
	prompts            Obj
	event              map[string]any
	depth, attempt     int64
	state, status      string
	artifacts          map[string]any
}

// follow moves one follow-up, in a transaction of its own; one another
// node moves is skipped.
func (e *RuleEngine) follow(ctx context.Context, id int64) error {
	tx, err := e.app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	f := &followup{id: id}
	err = tx.QueryRow(ctx, `SELECT f.rule_id, f.job_id, f.template_id, f.prompts, f.event, f.depth, f.attempt, f.state, coalesce(j.status, ''), j.artifacts
		FROM event_rule_followups f LEFT JOIN unified_jobs j ON j.id = f.job_id
		WHERE f.id = $1 AND f.state <> 'done' FOR UPDATE OF f SKIP LOCKED`, id).Scan(&f.rule, &f.job, &f.template, &f.prompts, &f.event,
		&f.depth, &f.attempt, &f.state, &f.status, &f.artifacts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	r, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleColumns+` FROM event_rules WHERE id = $1`, f.rule))
	if err != nil {
		return err
	}
	if err := e.followUp(ctx, tx, r, f); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *RuleEngine) followUp(ctx context.Context, tx pgx.Tx, r *eventRule, f *followup) error {
	done := func(result string, next *int64) error {
		_, err := tx.Exec(ctx, `UPDATE event_rule_followups SET state = 'done', result = $2, next_job_id = $3, modified = now() WHERE id = $1`,
			f.id, result, next)
		return err
	}
	if f.job == nil {
		return done("The job was deleted.", nil)
	}
	if f.status == "failed" && f.attempt < r.Retries {
		if f.state == followWaiting && r.RetryDelay > 0 {
			_, err := tx.Exec(ctx, `UPDATE event_rule_followups SET state = $2, due = now() + make_interval(secs => $3), modified = now() WHERE id = $1`,
				f.id, followRetrying, r.RetryDelay)
			return err
		}
		job, reason, err := e.relaunch(ctx, tx, r, f)
		if err != nil {
			return err
		}
		if reason != "" {
			return done("Not launched again: "+reason, nil)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO event_rule_followups (rule_id, job_id, template_id, prompts, event, depth, attempt)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, r.ID, job, f.template, f.prompts, f.event, f.depth, f.attempt+1); err != nil {
			return err
		}
		return done(fmt.Sprintf("Retry %d of %d: job %d.", f.attempt+1, r.Retries, job), &job)
	}
	if r.PostArtifacts == "" {
		return done("", nil)
	}
	result, err := e.postArtifacts(ctx, tx, r, f)
	if err != nil {
		return err
	}
	return done(result, nil)
}

// relaunch launches the template of a failed job again, as the rule's
// owner, with the same prompts. A reason says why it could not.
func (e *RuleEngine) relaunch(ctx context.Context, tx pgx.Tx, r *eventRule, f *followup) (int64, string, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, "SELECT enabled FROM event_rules WHERE id = $1", r.ID).Scan(&enabled); err != nil {
		return 0, "", err
	}
	if !enabled {
		return 0, "the rule was disabled.", nil
	}
	c, reason, err := e.ownerCtx(ctx, tx, r)
	if err != nil || reason != "" {
		return 0, reason, err
	}
	ok, err := ujtPerm(c, f.template, "execute")
	if errors.Is(err, api.ErrNotFound) {
		return 0, "the template no longer exists.", nil
	}
	if err != nil {
		return 0, "", err
	}
	if !ok {
		return 0, fmt.Sprintf("%s may not use template %d.", c.User.Username, f.template), nil
	}
	typ := ujtType(c, f.template)
	res := api.LookupType(typ)
	if res == nil || (typ != "job_template" && typ != "workflow_job_template") {
		return 0, "the template is not a job or workflow template.", nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer sp.Rollback(context.Background())
	c.Tx = sp
	tpl, err := api.Get(c, res, f.template)
	if err != nil {
		return 0, "", err
	}
	body := Obj{}
	for k, v := range f.prompts {
		body[k] = v
	}
	ov := &LaunchOverrides{ExtraJob: map[string]any{"event_rule_id": r.ID, "stream_event": f.event}}
	var job int64
	if typ == "job_template" {
		job, _, err = LaunchJobTemplate(c, tpl, body, "manual", ov)
	} else {
		job, _, err = LaunchWorkflowJobTemplate(c, tpl, body, "manual", ov)
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", err
		}
		return 0, describeError(err), nil
	}
	return job, "", sp.Commit(ctx)
}

// postArtifacts posts the artifacts the job set as an event, from the
// rule, as ansible-rulebook posts them to its ruleset. It says what it
// did.
func (e *RuleEngine) postArtifacts(ctx context.Context, tx pgx.Tx, r *eventRule, f *followup) (string, error) {
	if len(f.artifacts) == 0 {
		return "The job set no artifacts to post.", nil
	}
	if f.depth >= maxPostDepth {
		return fmt.Sprintf("The artifacts are not posted: the event went through %d rules already.", f.depth), nil
	}
	c, reason, err := e.ownerCtx(ctx, tx, r)
	if err != nil || reason != "" {
		return "The artifacts are not posted: " + reason, err
	}
	if reason, err := artifactsRefusal(c, r); err != nil || reason != "" {
		return "The artifacts are not posted: " + reason, err
	}
	stream := artifactsStream(r)
	var test bool
	if err := tx.QueryRow(ctx, "SELECT test_mode FROM event_streams WHERE id = $1", stream).Scan(&test); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "The artifacts are not posted: the stream no longer exists.", nil
		}
		return "", err
	}
	data, err := json.Marshal(f.artifacts)
	if err != nil {
		return "", err
	}
	typ := "post_event"
	if r.PostArtifacts == "fact" {
		typ = "set_fact"
	}
	now := time.Now().UTC()
	ev := eventstreams.Event{
		ID:              fmt.Sprintf("rule-%d-job-%d", r.ID, *f.job),
		Source:          api.Lookup("event_rules").URL(r.ID),
		Type:            typ,
		Time:            &now,
		DataContentType: "application/json",
		Data:            data,
		Extensions:      map[string]any{depthExtension: f.depth + 1},
	}
	res, err := eventstreams.Ingest(ctx, tx, stream, test, []eventstreams.Event{ev})
	if err != nil {
		return "", err
	}
	WakeRules(ctx, tx, stream)
	return fmt.Sprintf("Posted the job's artifacts: event %d of stream %d.", res[0].Sequence, stream), nil
}
