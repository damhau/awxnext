package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/eventstreams"
	"github.com/damhau/awxnext/internal/rules"
)

// A backtest runs a rule, saved or a draft, over a period of the event
// log, as shadow mode would have: in memory, in a read-only transaction,
// so that it writes no decision and holds no throttle key. It answers at
// once, within bounds: the events it reads and the time it takes.

const (
	backtestEvents   = 10000
	backtestTimeout  = 30 * time.Second
	backtestPeriod   = 24 * time.Hour
	backtestListed   = 200
	backtestTop      = 100
	backtestMisses   = 20
	backtestPastJobs = 10000
	// defaultRunTime is how long a simulated launch runs when its template
	// has no finished job to go by.
	defaultRunTime = 5 * time.Minute
)

// backtestView serves POST /event_rules/<id>/backtest/: the rule, with the
// fields the body sets instead of its own (a draft of it).
func backtestView(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	runBacktest(c, w, r, obj)
}

// backtestDraft serves POST /event_rules/backtest/: a rule not saved yet.
func backtestDraft(c *Ctx, w http.ResponseWriter, r *http.Request) {
	runBacktest(c, w, r, nil)
}

func runBacktest(c *Ctx, w http.ResponseWriter, r *http.Request, saved Obj) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	since, until, limit, err := backtestBounds(body)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	rule, err := backtestRule(c, body, saved)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	res, err := backtest(c, rule, since, until, limit)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, res)
}

// backtestBounds reads the period and the most events to read.
func backtestBounds(body Obj) (since, until time.Time, limit int, err error) {
	fe := api.FieldErrors{}
	at := func(name string, def time.Time) time.Time {
		v, ok := body[name]
		delete(body, name)
		if !ok || v == nil || v == "" {
			return def
		}
		s, _ := v.(string)
		t, perr := time.Parse(time.RFC3339Nano, s)
		if perr != nil {
			fe.Add(name, "Expected a date and time, such as 2026-11-15T08:00:00Z.")
		}
		return t
	}
	until = at("until", time.Now())
	since = at("since", until.Add(-backtestPeriod))
	limit = backtestEvents
	if v, ok := body["max_events"]; ok {
		delete(body, "max_events")
		n, isNum := api.ID(v)
		if !isNum || n < 1 || n > backtestEvents {
			fe.Add("max_events", fmt.Sprintf("Expected a whole number from 1 to %d.", backtestEvents))
		} else {
			limit = int(n)
		}
	}
	if err := fe.Err(); err != nil {
		return since, until, 0, err
	}
	if !since.Before(until) {
		return since, until, 0, api.FieldError("since", "The period must start before it ends.")
	}
	return since, until, limit, nil
}

// backtestRule is the rule to run: the saved one with the body's fields, or
// the draft the body is, checked as a save checks it. The user must see
// what the rule reads and runs: its streams and its template.
func backtestRule(c *Ctx, body, saved Obj) (*eventRule, error) {
	res := api.Lookup("event_rules")
	if saved == nil {
		if _, ok := body["name"]; !ok {
			body["name"] = "Draft"
		}
	}
	vals, err := api.Validate(c, res, body, saved == nil, saved)
	if err != nil {
		return nil, err
	}
	merged := Obj{}
	for k, v := range saved {
		merged[k] = v
	}
	for k, v := range vals {
		merged[k] = v
	}
	r := ruleFromObj(merged)
	if saved == nil {
		r.ID = 0
	}
	if err := checkRuleShape(r); err != nil {
		return nil, err
	}
	see := func(field, resName string, id *int64) error {
		if id == nil {
			return nil
		}
		target := api.Lookup(resName)
		if _, err := api.Get(c, target, *id); errors.Is(err, api.ErrNotFound) {
			return api.FieldError(field, fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", *id))
		} else if err != nil {
			return err
		}
		ok, err := api.CanView(c, target, *id)
		if err != nil {
			return err
		}
		if !ok {
			return api.Detail(403, fmt.Sprintf("You may not see %s %d, which the backtest reads.", strings.ToLower(target.Verbose), *id))
		}
		return nil
	}
	for _, s := range r.Streams {
		if err := see("streams", "event_streams", &s); err != nil {
			return nil, err
		}
	}
	switch r.Action {
	case actionLaunch:
		err = see("unified_job_template", "unified_job_templates", r.Template)
	case actionNotify:
		err = see("notification_template", "notification_templates", r.Notification)
	case actionPost:
		err = see("target_stream", "event_streams", r.Target)
	}
	if err != nil {
		return nil, err
	}
	s, err := streamSchema(c, r.Streams)
	if err != nil {
		return nil, err
	}
	return r, checkRuleExprs(s, r)
}

// pastRuns stands for the jobs running during a backtest: those the job
// history shows, but the rule's own, and the launches it simulates.
type pastRuns struct {
	jobs, sim []pastJob
}

type pastJob struct {
	runningJob
	start time.Time
	// end is zero for a job still running.
	end time.Time
}

func (j pastJob) runs(at time.Time) bool {
	return !at.Before(j.start) && (j.end.IsZero() || at.Before(j.end))
}

func (p *pastRuns) running(at time.Time) []runningJob {
	var out []runningJob
	for _, list := range [][]pastJob{p.jobs, p.sim} {
		for _, j := range list {
			if j.runs(at) {
				out = append(out, j.runningJob)
			}
		}
	}
	return out
}

// launches counts the rule's simulated launches running at at.
func (p *pastRuns) launches(at time.Time) int64 {
	var n int64
	seen := map[time.Time]bool{}
	for _, j := range p.sim {
		// A launch running on several inventories counts once.
		if j.runs(at) && !seen[j.start] {
			seen[j.start] = true
			n++
		}
	}
	return n
}

func loadPastRuns(c *Ctx, r *eventRule, since, until time.Time) (*pastRuns, error) {
	rows, err := c.DB().Query(c, `WITH RECURSIVE own(id) AS (
			SELECT id FROM unified_jobs WHERE event_rule_id = $1
			UNION SELECT n.job_id FROM workflow_node_jobs n JOIN own ON n.workflow_job_id = own.id),
		ruled(id) AS (
			SELECT id FROM unified_jobs WHERE event_rule_id IS NOT NULL AND type <> 'workflow_approval'
				AND coalesce(started, created) < $3 AND (finished IS NULL OR finished > $2)
			UNION SELECT n.job_id FROM workflow_node_jobs n JOIN ruled ON n.workflow_job_id = ruled.id)
		SELECT j.id, j.inventory_id, i.organization_id, j."limit", j.id IN (SELECT id FROM ruled), coalesce(j.started, j.created), j.finished
		FROM unified_jobs j JOIN inventories i ON i.id = j.inventory_id
		WHERE j.type IN ('job', 'ad_hoc_command') AND coalesce(j.started, j.created) < $3 AND (j.finished IS NULL OR j.finished > $2)
			AND j.id NOT IN (SELECT id FROM own)
		ORDER BY j.id LIMIT $4`, r.ID, since, until, backtestPastJobs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := &pastRuns{}
	for rows.Next() {
		var j pastJob
		var end *time.Time
		if err := rows.Scan(&j.id, &j.inventory, &j.org, &j.limit, &j.rule, &j.start, &end); err != nil {
			return nil, err
		}
		if end != nil {
			j.end = *end
		}
		p.jobs = append(p.jobs, j)
	}
	return p, rows.Err()
}

// runTime is how long a launch of the rule's template takes: the average
// of its last finished jobs.
func runTime(c *Ctx, r *eventRule) (time.Duration, error) {
	if r.Template == nil {
		return defaultRunTime, nil
	}
	var secs float64
	err := c.DB().QueryRow(c, `SELECT coalesce(avg(elapsed), 0)::float8 FROM (SELECT elapsed FROM unified_jobs
		WHERE unified_job_template_id = $1 AND finished IS NOT NULL AND elapsed > 0 ORDER BY finished DESC LIMIT 20) x`, *r.Template).Scan(&secs)
	if err != nil || secs <= 0 {
		return defaultRunTime, err
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// simRule is what the simulator needs of r, its launches running took.
func simRule(r *eventRule, took time.Duration) rules.SimRule {
	sr := rules.SimRule{Filter: r.Filter, Key: r.Key, Throttle: r.Throttle, Window: time.Duration(r.Window) * time.Second,
		BreakerFirings: r.BreakerFirings, BreakerWindow: time.Duration(r.BreakerWindow) * time.Second}
	if r.Check == checkEvent {
		sr.Check = rules.SimCheck{Condition: r.CheckCondition, Window: time.Duration(r.CheckWindow) * time.Second}
		if r.Action == actionLaunch {
			sr.Check.RunTime = took
		}
	}
	if r.Pattern != "" {
		sr.Pattern = r.pattern()
		if r.hasConditions() {
			sr.Conditions = r.Conditions
		}
	}
	return sr
}

type keyStats struct {
	Key          string `json:"key"`
	Events       int    `json:"events"`
	Fired        int    `json:"fired"`
	Held         int    `json:"held"`
	Throttled    int    `json:"throttled"`
	Deduplicated int    `json:"deduplicated"`
}

type missStats struct {
	Expression string `json:"expression"`
	Count      int    `json:"count"`
	Example    Obj    `json:"example"`
}

// backtest runs r over the events of its streams received in [since,
// until), at most limit.
func backtest(c *Ctx, r *eventRule, since, until time.Time, limit int) (Obj, error) {
	ctx, cancel := context.WithTimeout(c, backtestTimeout)
	defer cancel()
	tx, err := c.App.DB.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	bc := *c
	bc.Context, bc.Tx = ctx, tx
	events, err := eventstreams.Between(ctx, tx, r.Streams, since, until, limit+1)
	if err != nil {
		return nil, err
	}
	truncated := len(events) > limit
	if truncated {
		events = events[:limit]
		until = events[limit-1].Received
	}
	past, err := loadPastRuns(&bc, r, since, until)
	if err != nil {
		return nil, err
	}
	took, err := runTime(&bc, r)
	if err != nil {
		return nil, err
	}
	g := newGuards(ctx, tx)
	g.past = past
	hosts := map[string]int{}
	eng := &RuleEngine{app: c.App}
	act := func(ms []rules.Member, at time.Time) rules.Outcome {
		members := make([]member, len(ms))
		vars := rules.Vars{Event: ms[0].Event.Value}
		for i, m := range ms {
			members[i] = member{ev: events[m.Index], val: m.Event.Value, key: m.Key}
			vars.Events = append(vars.Events, m.Event.Value)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return rules.Outcome{Name: rules.Failed, Reason: describeError(err)}
		}
		defer sp.Rollback(context.Background())
		sc := bc
		sc.Tx = sp
		g.db = sp
		out, err := backtestAct(&sc, eng, g, r, members, vars, at, took, hosts)
		if err != nil {
			name := rules.Failed
			if errors.Is(err, errRuleEval) {
				name = rules.Error
			}
			return rules.Outcome{Name: name, Reason: describeError(err)}
		}
		return out
	}
	sim := rules.NewSimulator(simRule(r, took), act)
	for i, ev := range events {
		if ctx.Err() != nil {
			truncated, events = true, events[:i]
			if i > 0 {
				until = events[i-1].Received
			} else {
				until = since
			}
			break
		}
		sim.Add(ctx, rules.SimEvent{Value: rules.Value(ev), Received: ev.Received})
	}
	if ctx.Err() == nil {
		sim.Finish(until)
	}
	return backtestResult(r, sim, events, since, until, truncated, hosts, took), nil
}

// backtestAct applies the guardrails to the action and says what it would
// do, as guard does in shadow mode. The blast radius looks at the jobs
// running at at; the windows, as the engine does, at the first event's
// receipt, or when the rule's pattern fired.
func backtestAct(c *Ctx, eng *RuleEngine, g *guards, r *eventRule, members []member, vars rules.Vars, at time.Time,
	took time.Duration, hosts map[string]int) (rules.Outcome, error) {
	p, err := eng.plan(c, r, members, vars)
	if err != nil {
		return rules.Outcome{}, err
	}
	windowAt := members[0].ev.Received
	if r.Pattern != "" {
		windowAt = at
	}
	window, err := g.windows(r, p, windowAt)
	if err != nil {
		return rules.Outcome{}, err
	}
	if window != nil && !window.approve {
		return simHold(window), nil
	}
	approval := r.Mode == modeApproval || window != nil
	g.at = at
	h, err := g.blastRadius(r, p)
	if err != nil {
		return rules.Outcome{}, err
	}
	if h != nil {
		return simHold(h), nil
	}
	details := map[string]any{"action": p.describe()}
	guardrail := ""
	if window != nil {
		guardrail = window.guardrail
		for k, v := range window.details {
			details[k] = v
		}
	}
	if approval {
		return rules.Outcome{Name: rules.Approval, Guardrail: guardrail, Details: details,
			Reason: "The rule would have asked for approval to " + p.summary() + "."}, nil
	}
	if p.action == actionLaunch {
		targets, _, _, err := g.targets(p)
		if err != nil {
			return rules.Outcome{}, err
		}
		for h := range targets {
			hosts[h]++
		}
		for _, t := range p.runs {
			if _, err := g.inventory(t.inventory); err != nil {
				return rules.Outcome{}, err
			}
			g.past.sim = append(g.past.sim, pastJob{runningJob: runningJob{inventory: t.inventory, org: g.invOrg[t.inventory], limit: t.limit, rule: true},
				start: at, end: at.Add(took)})
		}
	}
	return rules.Outcome{Name: rules.Fired, Details: details, Reason: "The rule would have " + p.done() + "."}, nil
}

func simHold(h *hold) rules.Outcome {
	o := h.outcome()
	return rules.Outcome{Name: rules.Held, Guardrail: o.guardrail, Reason: o.reason, Details: o.details}
}

func backtestResult(r *eventRule, sim *rules.Simulator, events []eventstreams.Stored, since, until time.Time, truncated bool,
	hosts map[string]int, took time.Duration) Obj {
	outcomes := map[string]int{}
	held := map[string]int{}
	keys := map[string]*keyStats{}
	misses := map[string]*missStats{}
	listed := []Obj{}
	checks := map[string]int{}
	for i, d := range sim.Decisions {
		ev := events[i]
		outcomes[d.Outcome]++
		if d.Check != "" {
			checks[d.Check]++
		}
		if d.Outcome == rules.NotMatched {
			expr, example := conditionsExpr(r), Obj{"stream": ev.Stream, "sequence": ev.Sequence, "event_id": ev.ID,
				"received": api.FormatTime(ev.Received), "reason": d.Reason, "conditions": d.Details["conditions"]}
			if d.Miss != nil {
				expr = d.Miss.Expression
				example["reason"], example["filter"] = d.Miss.String(), d.Miss.Details()
				delete(example, "conditions")
			}
			m := misses[expr]
			if m == nil {
				m = &missStats{Expression: expr, Example: example}
				misses[expr] = m
			}
			m.Count++
			continue
		}
		if d.Outcome == rules.Held {
			held[d.Guardrail]++
		}
		k := keys[d.Key]
		if k == nil {
			k = &keyStats{Key: d.Key}
			keys[d.Key] = k
		}
		k.Events++
		switch d.Outcome {
		case rules.Fired, rules.Approval:
			k.Fired++
		case rules.Held:
			k.Held++
		case rules.Throttled:
			k.Throttled++
		case rules.Deduplicated:
			k.Deduplicated++
		}
		if len(listed) < backtestListed {
			o := Obj{"stream": ev.Stream, "sequence": ev.Sequence, "event_id": ev.ID, "event_type": ev.Type, "received": api.FormatTime(ev.Received),
				"outcome": d.Outcome, "key": d.Key, "reason": d.Reason, "guardrail": d.Guardrail, "details": d.Details}
			if !d.At.IsZero() {
				o["at"] = api.FormatTime(d.At)
			}
			if d.Check != "" {
				o["check_outcome"] = d.Check
			}
			if d.ResolvedBy != nil {
				by := events[*d.ResolvedBy]
				o["resolved_by"] = Obj{"stream": by.Stream, "sequence": by.Sequence, "event_type": by.Type, "received": api.FormatTime(by.Received)}
			}
			listed = append(listed, o)
		}
	}
	keyList := make([]*keyStats, 0, len(keys))
	for _, k := range keys {
		keyList = append(keyList, k)
	}
	sort.Slice(keyList, func(i, j int) bool {
		if keyList[i].Events != keyList[j].Events {
			return keyList[i].Events > keyList[j].Events
		}
		return keyList[i].Key < keyList[j].Key
	})
	missList := make([]*missStats, 0, len(misses))
	for _, m := range misses {
		missList = append(missList, m)
	}
	sort.Slice(missList, func(i, j int) bool {
		if missList[i].Count != missList[j].Count {
			return missList[i].Count > missList[j].Count
		}
		return missList[i].Expression < missList[j].Expression
	})
	hostList := make([]Obj, 0, len(hosts))
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	sort.Slice(names, func(i, j int) bool {
		if hosts[names[i]] != hosts[names[j]] {
			return hosts[names[i]] > hosts[names[j]]
		}
		return names[i] < names[j]
	})
	for _, h := range names[:min(len(names), backtestTop)] {
		hostList = append(hostList, Obj{"host": h, "fired": hosts[h]})
	}
	breakers := make([]Obj, 0, len(sim.Tripped))
	for k, t := range sim.Tripped {
		breakers = append(breakers, Obj{"key": k, "tripped_at": api.FormatTime(t)})
	}
	sort.Slice(breakers, func(i, j int) bool {
		return fmt.Sprint(breakers[i]["tripped_at"], breakers[i]["key"]) < fmt.Sprint(breakers[j]["tripped_at"], breakers[j]["key"])
	})
	var rule any
	if r.ID != 0 {
		rule = r.ID
	}
	approximations := []string{
		"Throttles, windows, patterns and circuit breakers start empty and closed at the start of the period.",
		"Maintenance windows, change freezes, inventories and templates are today's; a window is checked at each event's time, or when a pattern fires.",
		fmt.Sprintf("Running jobs are those the job history shows running at the time, but the rule's own; the launches the backtest simulates run %s each, the average of the template's last finished jobs.", took.Round(time.Second)),
		"An action that would ask for approval counts as awaiting_approval, and as a firing: the approval is not simulated.",
	}
	var checked any
	switch r.Check {
	case checkEvent:
		// The events in the log resolved the keys without the rule's
		// actions: the rate is how many would have looked fixed anyway.
		decided := checks[rules.CheckFixed] + checks[rules.CheckNotFixed] + checks[rules.CheckActionFailed]
		var rate any
		if decided > 0 {
			rate = float64(checks[rules.CheckFixed]) / float64(decided)
		}
		checked = Obj{"checked": decided, "resolved": checks[rules.CheckFixed], "unresolved": checks[rules.CheckNotFixed],
			"action_failed": checks[rules.CheckActionFailed], "pending": checks[rules.CheckPending], "resolved_rate": rate}
		approximations = append(approximations,
			"A firing's check counts the events of the log that resolved its key within the check's window: the alerts resolved without the rule's actions, or by the actions taken then. An approval counts as given at once, and a key not fixed doesn't escalate.")
	case checkTemplate:
		approximations = append(approximations, "Check templates are not run: the backtest leaves the checks out.")
	}
	return Obj{
		"rule": rule, "since": api.FormatTime(since), "until": api.FormatTime(until), "truncated": truncated,
		"events": len(events), "matched": len(events) - outcomes[rules.NotMatched], "fired": outcomes[rules.Fired] + outcomes[rules.Approval],
		"firings":  sim.Firings,
		"outcomes": outcomes, "held": held,
		"keys": keyList[:min(len(keyList), backtestTop)], "key_count": len(keyList),
		"hosts": hostList, "host_count": len(hosts),
		"breakers":       breakers[:min(len(breakers), backtestTop)],
		"misses":         missList[:min(len(missList), backtestMisses)],
		"decisions":      listed,
		"checks":         checked,
		"approximations": approximations,
	}
}

// conditionsExpr is how the misses of a pattern's conditions are grouped:
// the event matched none of them.
func conditionsExpr(r *eventRule) string {
	parts := make([]string, len(r.Conditions))
	for i, c := range r.Conditions {
		parts[i] = "(" + c + ")"
	}
	return strings.Join(parts, " || ")
}
