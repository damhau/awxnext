package resources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/rules"
)

// Checks of fixes (roadmap M14-08): once a rule's action ran, a check
// waits for an event resolving the key, or runs a check template, and
// records whether the action fixed the key. Each check is a row of
// event_rule_checks, opened in the transaction of the action: a batch that
// reads a resolving event settles the checks it resolves, and SettleChecks
// moves the others as their jobs finish and their deadlines pass, on
// whichever node runs it. A key whose fix didn't work escalates: the
// rule's notification, an approval before its next action, and one more
// firing for its circuit breaker.

const (
	checkEvent    = "event"
	checkTemplate = "template"

	checkActing   = "acting"
	checkWatching = "watching"
	checkChecking = "checking"
	checkSettled  = "settled"

	checkPending      = "pending"
	checkFixed        = "fixed"
	checkNotFixed     = "not_fixed"
	checkActionFailed = "action_failed"
	checkError        = "error"
	checkCanceled     = "canceled"
	checkResolved     = "resolved"
	checkUnresolved   = "unresolved"

	defaultSuccessDays = 30
	settleBatch        = 50
)

var finalJobStatus = []string{"successful", "failed", "error", "canceled"}

func checkOutcomeChoices(none bool) []api.Choice {
	pairs := []string{checkPending, "Pending", checkFixed, "Fixed", checkNotFixed, "Not fixed", checkActionFailed, "Action failed",
		checkError, "Error", checkCanceled, "Canceled", checkResolved, "Resolved by itself", checkUnresolved, "Not resolved"}
	if none {
		pairs = append([]string{"", "None"}, pairs...)
	}
	return labelled(pairs...)
}

func registerRuleChecks(ruleRes *Resource) {
	api.Register(&Resource{
		Name:         "event_rule_checks",
		Type:         "event_rule_check",
		Table:        "event_rule_checks",
		Verbose:      "Event Rule Check",
		NoTimestamps: true,
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		Ordering:     "-id",
		SearchFields: []string{"key", "reason"},
		Fields: []*Field{
			{Name: "created", Type: api.TDateTime, ReadOnly: true},
			{Name: "modified", Type: api.TDateTime, ReadOnly: true},
			{Name: "rule", Type: api.TFK, Ref: "event_rules", ReadOnly: true},
			{Name: "shadow", Type: api.TBool, ReadOnly: true,
				Help: "The check of the rule in shadow mode: no action ran, and it only says whether the key resolved by itself."},
			{Name: "mode", Type: api.TChoice, ReadOnly: true, Choices: labelled(modeShadow, "Shadow", modeApproval, "Approval", modeAuto, "Auto"),
				Help: "The rule's mode when it acted."},
			{Name: "kind", Type: api.TChoice, ReadOnly: true, Choices: labelled(checkEvent, "A resolving event", checkTemplate, "A check template")},
			{Name: "key", Type: api.TString, ReadOnly: true},
			{Name: "decisions", Type: api.TJSON, ReadOnly: true, Help: "The decisions the action fired for: their id and received time."},
			{Name: "event", Type: api.TJSON, ReadOnly: true, Help: "The event that fired the action: its stream, sequence, id, source and type."},
			{Name: "action", Type: api.TString, ReadOnly: true, Help: "The outcome of the action: launched, notified, posted, failed, error or shadow."},
			{Name: "job", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Nullable: true, Help: "The job the action launched."},
			{Name: "check_job", Type: api.TFK, Ref: "unified_jobs", ReadOnly: true, Nullable: true, Help: "The job of the check template."},
			{Name: "state", Type: api.TChoice, ReadOnly: true, Choices: labelled(checkActing, "Acting", checkWatching, "Watching", checkChecking, "Checking", checkSettled, "Settled"),
				Help: "acting: the action's job runs. watching: waiting for the resolving event until the deadline. checking: the check template launches at the deadline, then runs. settled: the outcome is final."},
			{Name: "outcome", Type: api.TChoice, ReadOnly: true, Choices: checkOutcomeChoices(false),
				Help: "fixed, not_fixed, action_failed (the action or its job failed), error (the check could not decide), canceled; in shadow mode, resolved (by itself: a fix would have looked fixed anyway) or unresolved. pending until settled."},
			{Name: "reason", Type: api.TText, ReadOnly: true},
			{Name: "opened", Type: api.TDateTime, ReadOnly: true, Help: "When the event that fired the action was received: resolving events count from then."},
			{Name: "action_finished", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "deadline", Type: api.TDateTime, ReadOnly: true, Nullable: true,
				Help: "Until when the check waits for the resolving event, or when it launches the check template."},
			{Name: "resolved_by", Type: api.TJSON, ReadOnly: true, Nullable: true, Help: "The event that resolved the key: its stream, sequence, id, type and received time."},
			{Name: "settled", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "escalated", Type: api.TBool, ReadOnly: true,
				Help: "The check found the key not fixed and escalated: the rule's check notification, or an approval before the key's next action."},
			{Name: "details", Type: api.TJSON, ReadOnly: true, Help: "prompts: the launch's variables and limit, which the check template launches with."},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			return fmt.Sprintf("t.rule_id IN (SELECT t.id FROM event_rules t WHERE %s)", api.ViewPredicate(c, ruleRes, arg))
		},
	})
}

// checkRuleCheck checks what a rule's check needs.
func checkRuleCheck(r *eventRule, fe api.FieldErrors) {
	switch r.Check {
	case "":
		if r.CheckCondition != "" {
			fe.Add("check_condition", "Only a rule with an event check has a check condition.")
		}
		if r.CheckTemplate != nil {
			fe.Add("check_template", "Only a rule with a template check has a check template.")
		}
		if r.CheckNotification != nil {
			fe.Add("check_notification_template", "Only a rule that checks its fixes notifies when one doesn't work.")
		}
		if r.CheckApproval {
			fe.Add("check_approval", "Only a rule that checks its fixes asks for approval when one doesn't work.")
		}
		return
	case checkEvent:
		if strings.TrimSpace(r.CheckCondition) == "" {
			fe.Add("check_condition", "An event check needs the condition of the event that resolves the key.")
		}
		if r.CheckWindow < 1 {
			fe.Add("check_window", "An event check waits one second at least for the resolving event.")
		}
		if r.CheckTemplate != nil {
			fe.Add("check_template", "Only a rule with a template check has a check template.")
		}
	case checkTemplate:
		if r.CheckTemplate == nil {
			fe.Add("check_template", "A template check needs the job or workflow template that checks the fix.")
		}
		if r.CheckCondition != "" {
			fe.Add("check_condition", "Only a rule with an event check has a check condition.")
		}
	}
}

// checkCheckTemplate checks, as a rule is saved, that its check template
// is a job or workflow template that prompts for what the action launches
// with.
func checkCheckTemplate(c *Ctx, r *eventRule) error {
	if r.Check != checkTemplate || r.CheckTemplate == nil {
		return nil
	}
	prompts := &eventRule{}
	if r.Action == actionLaunch {
		prompts = r
	}
	missing, noLimit, ok, err := templatePrompts(c, *r.CheckTemplate, prompts)
	if err != nil {
		return err
	}
	switch {
	case !ok:
		return api.FieldError("check_template", "A check launches a job template or a workflow template.")
	case len(missing) > 0:
		return api.FieldError("check_template", fmt.Sprintf("The check template does not prompt for %s: it launches with the action's variables. Turn on its Prompt on launch for variables, or ask for them in its survey.", strings.Join(missing, ", ")))
	case noLimit:
		return api.FieldError("check_template", "The check template does not prompt for a limit: it launches with the action's limit. Turn on its Prompt on launch for the limit.")
	}
	return nil
}

// checkDecision is a decision a check settles: its partition is by
// received.
type checkDecision struct {
	Decision int64     `json:"decision"`
	Received time.Time `json:"received"`
}

type ruleCheck struct {
	id, rule        int64
	shadow          bool
	mode, kind, key string
	decisions       []checkDecision
	event           map[string]any
	action          string
	job, checkJob   *int64
	state, outcome  string
	opened          time.Time
	actionFinished  *time.Time
	deadline        *time.Time
	resolvedBy      map[string]any
	details         map[string]any
}

const checkColumns = `c.id, c.rule_id, c.shadow, c.mode, c.kind, c.key, c.decisions, c.event, c.action, c.job_id, c.check_job_id,
	c.state, c.outcome, c.opened, c.action_finished, c.deadline, c.resolved_by, c.details`

func (k *ruleCheck) fields() []any {
	return []any{&k.id, &k.rule, &k.shadow, &k.mode, &k.kind, &k.key, &k.decisions, &k.event, &k.action, &k.job, &k.checkJob,
		&k.state, &k.outcome, &k.opened, &k.actionFinished, &k.deadline, &k.resolvedBy, &k.details}
}

// openChecks opens the checks of an action, one per key it ran for: an
// action that failed settles its checks at once. A rule in shadow mode
// watches for the resolving event without an action, and has no template
// check: it launches nothing.
func (e *RuleEngine) openChecks(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, members []member, out outcome) error {
	if r.Check == "" {
		return nil
	}
	now := time.Now()
	wait := now.Add(time.Duration(r.CheckWindow) * time.Second)
	state, settle := checkActing, ""
	var deadline, finished *time.Time
	switch out.name {
	case "launched":
	case "notified", "posted", "logged":
		state, deadline, finished = checkWatching, &wait, &now
		if r.Check == checkTemplate {
			state = checkChecking
		}
	case "shadow":
		if r.Check != checkEvent {
			return nil
		}
		state, deadline, finished = checkWatching, &wait, &now
	case "failed", "error":
		settle = checkActionFailed
	default:
		return nil
	}
	var keys []string
	byKey := map[string][]member{}
	for _, m := range members {
		if _, ok := byKey[m.key]; !ok {
			keys = append(keys, m.key)
		}
		byKey[m.key] = append(byKey[m.key], m)
	}
	for _, key := range keys {
		ms := byKey[key]
		ck := &ruleCheck{rule: r.ID, shadow: r.shadow(), mode: r.Mode, kind: r.Check, key: key, event: eventRef(ms), action: out.name,
			job: out.job, state: state, opened: ms[0].ev.Received, actionFinished: finished, deadline: deadline, details: map[string]any{}}
		for _, m := range ms {
			ck.decisions = append(ck.decisions, checkDecision{Decision: m.decision, Received: m.ev.Received})
			if m.ev.Received.After(ck.opened) {
				ck.opened = m.ev.Received
			}
		}
		if len(out.prompts) > 0 {
			ck.details["prompts"] = out.prompts
		}
		err := db.QueryRow(ctx, `INSERT INTO event_rule_checks (rule_id, shadow, mode, kind, key, decisions, event, action, job_id, state, opened, action_finished, deadline, details)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
			ck.rule, ck.shadow, ck.mode, ck.kind, ck.key, ck.decisions, ck.event, ck.action, ck.job, ck.state, ck.opened, finished, deadline, ck.details).Scan(&ck.id)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `UPDATE event_rule_decisions d SET check_id = $1, check_outcome = 'pending'
			FROM jsonb_to_recordset($2::jsonb) AS x(decision bigint, received timestamptz) WHERE d.id = x.decision AND d.received = x.received`,
			ck.id, ck.decisions); err != nil {
			return err
		}
		if settle != "" {
			if err := e.settleCheck(ctx, db, after, r, ck, settle, "The action failed: "+out.reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveChecks marks resolved the open event checks of the key an event
// resolves, received after the event that fired the action and before the
// check's deadline. A check whose action still runs settles when the
// action's job finishes.
func (e *RuleEngine) resolveChecks(ctx context.Context, tx pgx.Tx, after *afterCommit, r *eventRule, ev eventstreams.Stored, val map[string]any) error {
	vars := rules.Vars{Event: val}
	if ok, err := rules.Match(ctx, r.CheckCondition, vars); err != nil || !ok {
		return nil
	}
	key := ""
	if r.Key != "" {
		k, err := rules.Text(ctx, r.Key, vars)
		if err != nil {
			return nil
		}
		key = k
	}
	by := map[string]any{"stream": ev.Stream, "sequence": ev.Sequence, "id": ev.ID, "type": ev.Type, "received": api.FormatTime(ev.Received)}
	rows, err := tx.Query(ctx, `UPDATE event_rule_checks c SET resolved_by = $4, modified = now()
		WHERE c.rule_id = $1 AND c.key = $2 AND c.kind = 'event' AND c.resolved_by IS NULL AND c.opened <= $3
			AND (c.state = 'acting' OR c.state = 'watching' AND c.deadline >= $3)
		RETURNING `+checkColumns, r.ID, key, ev.Received, by)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*ruleCheck, error) {
		k := &ruleCheck{}
		return k, row.Scan(k.fields()...)
	})
	if err != nil {
		return err
	}
	for _, ck := range list {
		if ck.state != checkWatching {
			continue
		}
		outcome, reason := checkFixed, fmt.Sprintf("Event %d of stream %d (%s) resolved the key %s after the action.", ev.Sequence, ev.Stream, ev.Type,
			ev.Received.Sub(ck.opened).Round(time.Second))
		if ck.shadow {
			outcome, reason = checkResolved, fmt.Sprintf("Event %d of stream %d (%s) resolved the key %s after the event, without an action: a fix would have looked fixed anyway.",
				ev.Sequence, ev.Stream, ev.Type, ev.Received.Sub(ck.opened).Round(time.Second))
		}
		if err := e.settleCheck(ctx, tx, after, r, ck, outcome, reason); err != nil {
			return err
		}
	}
	return nil
}

// settleCheck gives a check its outcome, and its decisions. A check that
// found the key not fixed, outside shadow mode, escalates.
func (e *RuleEngine) settleCheck(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, ck *ruleCheck, outcome, reason string) error {
	failed := !ck.shadow && (outcome == checkNotFixed || outcome == checkActionFailed)
	escalated := failed && (r.CheckNotification != nil || r.CheckApproval)
	if _, err := db.Exec(ctx, `UPDATE event_rule_checks SET state = 'settled', outcome = $2, reason = $3, settled = now(), escalated = $4, modified = now()
		WHERE id = $1`, ck.id, outcome, reason, escalated); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `UPDATE event_rule_decisions d SET check_outcome = $1
		FROM jsonb_to_recordset($2::jsonb) AS x(decision bigint, received timestamptz) WHERE d.id = x.decision AND d.received = x.received`,
		outcome, ck.decisions); err != nil {
		return err
	}
	after.checked(r.ID, outcome)
	if !failed {
		return nil
	}
	slog.Info("event rule fix not fixed", "rule", r.ID, "check", ck.id, "outcome", outcome)
	if r.CheckNotification != nil {
		subject := fmt.Sprintf("Event rule %s: the fix of key %q didn't work", r.Name, ck.key)
		if outcome == checkActionFailed {
			subject = fmt.Sprintf("Event rule %s: the action for key %q failed", r.Name, ck.key)
		}
		about := map[string]any{"rule": Obj{"id": r.ID, "name": r.Name}, "key": ck.key, "event": ck.event, "job": ck.job,
			"check": Obj{"id": ck.id, "outcome": outcome, "reason": reason, "url": api.Lookup("event_rule_checks").URL(ck.id)}}
		if err := ruleNotice(ctx, db, after, *r.CheckNotification, ck.job, subject, about); err != nil {
			return err
		}
	}
	if r.BreakerFirings <= 0 {
		return nil
	}
	at := time.Now()
	open, n, err := breakerState(ctx, db, r, false, ck.key, at)
	if err != nil || !open || n == 0 {
		return err
	}
	return tripBreaker(ctx, db, after, r, false, ck.key, at, n)
}

// cancelChecks settles a rule's open checks as canceled.
func cancelChecks(ctx context.Context, db api.DBTX, rule int64, reason string) error {
	_, err := db.Exec(ctx, `WITH c AS (UPDATE event_rule_checks SET state = 'settled', outcome = 'canceled', reason = $2, settled = now(), modified = now()
			WHERE rule_id = $1 AND state <> 'settled' RETURNING decisions)
		UPDATE event_rule_decisions d SET check_outcome = 'canceled' FROM c, jsonb_to_recordset(c.decisions) AS x(decision bigint, received timestamptz)
		WHERE d.id = x.decision AND d.received = x.received`, rule, reason)
	return err
}

// escalation is what makes an action wait for approval when the rule's
// last checked fix of one of its keys didn't work.
func escalation(ctx context.Context, db api.DBTX, r *eventRule, members []member) (*hold, error) {
	var keys []string
	for _, m := range members {
		keys = append(keys, m.key)
	}
	var id int64
	var key, outcome string
	err := db.QueryRow(ctx, `SELECT id, key, outcome FROM (SELECT DISTINCT ON (key) id, key, outcome FROM event_rule_checks
			WHERE rule_id = $1 AND key = ANY($2) AND NOT shadow AND state = 'settled' AND outcome IN ('fixed', 'not_fixed', 'action_failed')
			ORDER BY key, settled DESC, id DESC) x
		WHERE outcome <> 'fixed' ORDER BY key LIMIT 1`, r.ID, keys).Scan(&id, &key, &outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	what := "found the key not fixed"
	if outcome == checkActionFailed {
		what = "found the action failed"
	}
	return &hold{guardrail: guardEscalation, approve: true, details: map[string]any{"escalation": Obj{"check": id, "key": key, "outcome": outcome}},
		reason: fmt.Sprintf("The rule's last check of key %q (check %d) %s: its next action waits for approval.", key, id, what)}, nil
}

// SettleChecks moves the checks that are due: those whose action's job
// finished, those whose deadline passed, and those whose check job
// finished. An event check's deadline counts once the rule's streams have
// no unread event received before it, as a window's does: the event that
// resolves it in time is read first.
func (e *RuleEngine) SettleChecks(ctx context.Context) error {
	rows, err := e.app.DB.Query(ctx, `SELECT c.id FROM event_rule_checks c JOIN event_rules r ON r.id = c.rule_id
		LEFT JOIN unified_jobs j ON j.id = c.job_id
		LEFT JOIN unified_jobs k ON k.id = c.check_job_id
		CROSS JOIN LATERAL (SELECT least(now(), min(f.received)) AS horizon FROM jsonb_array_elements(r.streams) s
			JOIN event_consumer_positions p ON p.consumer = $1 AND p.stream_id = (s #>> '{}')::bigint
			CROSS JOIN LATERAL (SELECT ev.received FROM stream_events ev WHERE ev.stream_id = p.stream_id AND ev.sequence > p.position
				AND NOT ev.test_mode ORDER BY ev.sequence LIMIT 1) f) h
		WHERE c.state <> 'settled' AND CASE c.state
			WHEN 'acting' THEN j.id IS NULL OR j.status = ANY($2)
			WHEN 'watching' THEN c.deadline <= h.horizon
			ELSE CASE WHEN c.check_job_id IS NULL THEN c.deadline <= now() ELSE k.id IS NULL OR k.status = ANY($2) END END
		ORDER BY c.id LIMIT `+fmt.Sprint(settleBatch), rulesConsumer, finalJobStatus)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := e.settleDue(ctx, id); err != nil {
			if ctx.Err() != nil {
				return err
			}
			slog.Warn("event rules: settling a check failed", "check", id, "err", err)
		}
	}
	return nil
}

// settleDue moves one check, in a transaction of its own; a check another
// node moves is skipped.
func (e *RuleEngine) settleDue(ctx context.Context, id int64) error {
	tx, err := e.app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	ck := &ruleCheck{}
	var job, checkJob string
	var jobFinished *time.Time
	var artifacts map[string]any
	err = tx.QueryRow(ctx, `SELECT `+checkColumns+`, coalesce(j.status, ''), j.finished, coalesce(k.status, ''), k.artifacts
		FROM event_rule_checks c LEFT JOIN unified_jobs j ON j.id = c.job_id LEFT JOIN unified_jobs k ON k.id = c.check_job_id
		WHERE c.id = $1 AND c.state <> 'settled' FOR UPDATE OF c SKIP LOCKED`, id).Scan(append(ck.fields(), &job, &jobFinished, &checkJob, &artifacts)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	r, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleColumns+` FROM event_rules WHERE id = $1`, ck.rule))
	if err != nil {
		return err
	}
	after := &afterCommit{}
	if err := e.moveCheck(ctx, tx, after, r, ck, job, jobFinished, checkJob, artifacts); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	after.run(ctx, e.app)
	return nil
}

func isFinal(status string) bool {
	for _, s := range finalJobStatus {
		if s == status {
			return true
		}
	}
	return false
}

func (e *RuleEngine) moveCheck(ctx context.Context, tx pgx.Tx, after *afterCommit, r *eventRule, ck *ruleCheck, job string, jobFinished *time.Time,
	checkJob string, artifacts map[string]any) error {
	window := time.Duration(r.CheckWindow) * time.Second
	switch ck.state {
	case checkActing:
		switch {
		case job == "":
			return e.settleCheck(ctx, tx, after, r, ck, checkError, "The action's job was deleted before it finished.")
		case !isFinal(job):
			return nil
		case job == "canceled":
			return e.settleCheck(ctx, tx, after, r, ck, checkCanceled, fmt.Sprintf("The action's job %d was canceled.", *ck.job))
		case job != "successful":
			return e.settleCheck(ctx, tx, after, r, ck, checkActionFailed, fmt.Sprintf("The action's job %d %s.", *ck.job, jobEnd(job)))
		}
		finished := time.Now()
		if jobFinished != nil {
			finished = *jobFinished
		}
		if ck.kind == checkEvent && ck.resolvedBy != nil {
			return e.settleCheck(ctx, tx, after, r, ck, checkFixed, fmt.Sprintf("Event %v of stream %v (%v) resolved the key while the action ran.",
				ck.resolvedBy["sequence"], ck.resolvedBy["stream"], ck.resolvedBy["type"]))
		}
		next := checkWatching
		if ck.kind == checkTemplate {
			next = checkChecking
		}
		_, err := tx.Exec(ctx, `UPDATE event_rule_checks SET state = $2, action_finished = $3, deadline = $4, modified = now() WHERE id = $1`,
			ck.id, next, finished, finished.Add(window))
		return err
	case checkWatching:
		if ck.shadow {
			return e.settleCheck(ctx, tx, after, r, ck, checkUnresolved, fmt.Sprintf("No event resolved the key within %s of the decision.", window))
		}
		return e.settleCheck(ctx, tx, after, r, ck, checkNotFixed, fmt.Sprintf("No event resolved the key within %s after the action finished.", window))
	}
	if ck.checkJob == nil {
		if ck.deadline != nil && ck.deadline.After(time.Now()) {
			return nil
		}
		id, reason, err := e.launchCheck(ctx, tx, r, ck)
		if err != nil {
			return err
		}
		if reason != "" {
			return e.settleCheck(ctx, tx, after, r, ck, checkError, reason)
		}
		_, err = tx.Exec(ctx, `UPDATE event_rule_checks SET check_job_id = $2, modified = now() WHERE id = $1`, ck.id, id)
		return err
	}
	switch {
	case checkJob == "":
		return e.settleCheck(ctx, tx, after, r, ck, checkError, "The check's job was deleted before it finished.")
	case !isFinal(checkJob):
		return nil
	case checkJob == "error" || checkJob == "canceled":
		return e.settleCheck(ctx, tx, after, r, ck, checkError, fmt.Sprintf("The check's job %d %s: it could not decide.", *ck.checkJob, jobEnd(checkJob)))
	}
	if fixed, ok := artifacts["fixed"].(bool); ok {
		outcome := checkNotFixed
		if fixed {
			outcome = checkFixed
		}
		return e.settleCheck(ctx, tx, after, r, ck, outcome, fmt.Sprintf("The check's job %d set fixed to %t.", *ck.checkJob, fixed))
	}
	if checkJob == "successful" {
		return e.settleCheck(ctx, tx, after, r, ck, checkFixed, fmt.Sprintf("The check's job %d succeeded.", *ck.checkJob))
	}
	return e.settleCheck(ctx, tx, after, r, ck, checkNotFixed, fmt.Sprintf("The check's job %d failed.", *ck.checkJob))
}

func jobEnd(status string) string {
	switch status {
	case "error":
		return "ended in error"
	case "canceled":
		return "was canceled"
	}
	return status
}

// launchCheck launches a check's template as the rule's owner, with the
// action's variables and limit. A reason says why it could not.
func (e *RuleEngine) launchCheck(ctx context.Context, tx pgx.Tx, r *eventRule, ck *ruleCheck) (int64, string, error) {
	c, reason, err := e.ownerCtx(ctx, tx, r)
	if err != nil || reason != "" {
		return 0, reason, err
	}
	if r.CheckTemplate == nil {
		return 0, "The rule's check template was deleted.", nil
	}
	ok, err := ujtPerm(c, *r.CheckTemplate, "execute")
	if errors.Is(err, api.ErrNotFound) {
		return 0, "The check template no longer exists.", nil
	}
	if err != nil {
		return 0, "", err
	}
	if !ok {
		return 0, fmt.Sprintf("%s may not use check template %d.", c.User.Username, *r.CheckTemplate), nil
	}
	typ := ujtType(c, *r.CheckTemplate)
	res := api.LookupType(typ)
	if res == nil || (typ != "job_template" && typ != "workflow_job_template") {
		return 0, "The check template is not a job or workflow template.", nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0, "", err
	}
	defer sp.Rollback(context.Background())
	c.Tx = sp
	tpl, err := api.Get(c, res, *r.CheckTemplate)
	if err != nil {
		return 0, "", err
	}
	body := Obj{}
	if p, ok := ck.details["prompts"].(map[string]any); ok {
		for k, v := range p {
			body[k] = v
		}
	}
	ov := &LaunchOverrides{ExtraJob: map[string]any{"event_rule_id": r.ID, "stream_event": ck.event}}
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
		return 0, "The check template could not launch: " + describeError(err), nil
	}
	return job, "", sp.Commit(ctx)
}

// checkSummary gives a page of rules their success rate and their checks'
// counts over the last EVENT_RULE_SUCCESS_RATE_DAYS days.
func checkSummary(c *Ctx, objs []Obj) error {
	ids := make([]int64, 0, len(objs))
	for _, o := range objs {
		if id, ok := api.ID(o["id"]); ok {
			ids = append(ids, id)
		}
	}
	days := defaultSuccessDays
	if c.App.Settings != nil {
		if d := c.App.Settings.Int(c, "EVENT_RULE_SUCCESS_RATE_DAYS"); d > 0 {
			days = d
		}
	}
	type counts struct{ fixed, notFixed, failed, errors, pending, shadow, resolved int64 }
	byRule := map[int64]counts{}
	if len(ids) > 0 {
		rows, err := c.DB().Query(c, `SELECT rule_id,
				count(*) FILTER (WHERE NOT shadow AND outcome = 'fixed'),
				count(*) FILTER (WHERE NOT shadow AND outcome = 'not_fixed'),
				count(*) FILTER (WHERE NOT shadow AND outcome = 'action_failed'),
				count(*) FILTER (WHERE NOT shadow AND outcome IN ('error', 'canceled')),
				count(*) FILTER (WHERE state <> 'settled'),
				count(*) FILTER (WHERE shadow AND outcome IN ('resolved', 'unresolved')),
				count(*) FILTER (WHERE shadow AND outcome = 'resolved')
			FROM event_rule_checks WHERE rule_id = ANY($1) AND (state <> 'settled' OR settled > now() - make_interval(days => $2))
			GROUP BY rule_id`, ids, days)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var k counts
			if err := rows.Scan(&id, &k.fixed, &k.notFixed, &k.failed, &k.errors, &k.pending, &k.shadow, &k.resolved); err != nil {
				rows.Close()
				return err
			}
			byRule[id] = k
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, o := range objs {
		id, _ := api.ID(o["id"])
		k := byRule[id]
		checked := k.fixed + k.notFixed + k.failed
		var rate any
		if checked > 0 {
			rate = float64(k.fixed) / float64(checked)
		}
		var shadowRate any
		if k.shadow > 0 {
			shadowRate = float64(k.resolved) / float64(k.shadow)
		}
		o["success_rate"] = rate
		o["summary_fields"].(Obj)["checks"] = Obj{"days": days, "checked": checked, "fixed": k.fixed, "not_fixed": k.notFixed,
			"action_failed": k.failed, "errors": k.errors, "pending": k.pending, "success_rate": rate,
			"shadow": Obj{"checked": k.shadow, "resolved": k.resolved, "resolved_rate": shadowRate}}
	}
	return nil
}

// jobChecks are the checks of jobs: as the action checked, or as the
// check's own job.
func jobChecks(c *Ctx, ids []int64) (map[int64][]Obj, error) {
	out := map[int64][]Obj{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := c.DB().Query(c, `SELECT id, rule_id, key, kind, state, outcome, job_id, check_job_id FROM event_rule_checks
		WHERE job_id = ANY($1) OR check_job_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, rule int64
		var key, kind, state, outcome string
		var job, checkJob *int64
		if err := rows.Scan(&id, &rule, &key, &kind, &state, &outcome, &job, &checkJob); err != nil {
			return nil, err
		}
		o := Obj{"id": id, "rule": rule, "key": key, "kind": kind, "state": state, "outcome": outcome}
		if job != nil {
			x := Obj{"role": "action"}
			for k, v := range o {
				x[k] = v
			}
			out[*job] = append(out[*job], x)
		}
		if checkJob != nil {
			x := Obj{"role": "check"}
			for k, v := range o {
				x[k] = v
			}
			out[*checkJob] = append(out[*checkJob], x)
		}
	}
	return out, rows.Err()
}
