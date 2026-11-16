package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/hostpattern"
	"github.com/damhau/awxnext/internal/schedule"
)

// Guardrails (roadmap M14-03) hold a rule's action back, or make it wait
// for approval: the circuit breaker of its key, the maintenance windows
// and change freezes it is in, and its blast radius (its concurrent runs,
// the hosts running jobs use, the share of an inventory group under
// remediation). The checks only read: shadow mode, and backtests, run them
// without acting.

const (
	maxRuleRuns       = 1000
	maxBreakerFirings = 10000

	guardBreaker     = "circuit_breaker"
	guardMaintenance = "maintenance_window"
	guardFreeze      = "change_freeze"
	guardRuns        = "max_concurrent_runs"
	guardHostLock    = "host_lock"
	guardGroupShare  = "group_share"
	guardEscalation  = "check_escalation"

	// maxHeldHosts bounds the hosts a decision lists.
	maxHeldHosts = 20
)

var activeJobStatus = []string{"new", "pending", "waiting", "running"}

// hold is a guardrail holding an action back, or, with approve, making it
// wait for an approval.
type hold struct {
	guardrail string
	reason    string
	details   map[string]any
	approve   bool
}

func (h *hold) outcome() outcome {
	d := map[string]any{"guardrail": h.guardrail}
	for k, v := range h.details {
		d[k] = v
	}
	return outcome{name: "held", guardrail: h.guardrail, reason: h.reason, details: d}
}

// checkRuleGuardrails checks, as a rule is saved, that its owner may use
// the notification templates of its breaker and of its checks.
func checkRuleGuardrails(c *Ctx, r *eventRule) error {
	for _, n := range []struct {
		field string
		id    *int64
	}{{"breaker_notification_template", r.BreakerNotification}, {"check_notification_template", r.CheckNotification}} {
		if n.id == nil {
			continue
		}
		ok, err := api.HasPermID(c, "notification_templates", *n.id, "change")
		if errors.Is(err, api.ErrNotFound) {
			return api.FieldError(n.field, fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", *n.id))
		}
		if err != nil {
			return err
		}
		if !ok {
			return api.Detail(403, fmt.Sprintf("%s may not use notification template %d.", c.User.Username, *n.id))
		}
	}
	return nil
}

// breakerState says whether a key's circuit breaker is open for an event
// received at at, and how many times the key fired within the breaker's
// window before it, since the breaker was last reset.
func breakerState(ctx context.Context, db api.DBTX, r *eventRule, shadow bool, key string, at time.Time) (open bool, prior int64, err error) {
	if r.BreakerFirings <= 0 {
		return false, 0, nil
	}
	var tripped, reset *time.Time
	err = db.QueryRow(ctx, `SELECT tripped_at, reset_at FROM event_rule_breakers WHERE rule_id = $1 AND shadow = $2 AND key = $3`,
		r.ID, shadow, key).Scan(&tripped, &reset)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, 0, err
	}
	if tripped != nil {
		return true, 0, nil
	}
	// A reset counts the firings decided after it, and a rule entering
	// shadow mode its shadow firings from then on. A check that found the
	// key not fixed counts as one more firing, when it settled.
	err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM event_rule_decisions WHERE rule_id = $1 AND key = $2 AND fired AND (mode = 'shadow') = $3
			AND received > $4 AND received <= $5 AND ($6::timestamptz IS NULL OR created > $6)
			AND (NOT $3 OR created > coalesce((SELECT shadow_since FROM event_rules WHERE id = $1), '-infinity')))
		+ (SELECT count(*) FROM event_rule_checks WHERE rule_id = $1 AND key = $2 AND NOT $3 AND state = 'settled'
			AND outcome IN ('not_fixed', 'action_failed') AND settled > $4 AND ($6::timestamptz IS NULL OR settled > $6))`,
		r.ID, key, shadow, at.Add(-time.Duration(r.BreakerWindow)*time.Second), at, reset).Scan(&prior)
	return prior >= r.BreakerFirings, prior, err
}

func breakerHold(r *eventRule, key string) outcome {
	what := "the key fired"
	if r.Check != "" {
		what = "the key fired, or its fix didn't work,"
	}
	return (&hold{guardrail: guardBreaker, details: map[string]any{"key": key},
		reason: fmt.Sprintf("The circuit breaker of key %q is open: %s %d times within %d seconds. Reset it to act again.",
			key, what, r.BreakerFirings, r.BreakerWindow)}).outcome()
}

// tripBreaker opens a key's circuit breaker and, outside shadow mode, sends
// the rule's breaker notification once committed.
func tripBreaker(ctx context.Context, db pgx.Tx, after *afterCommit, r *eventRule, shadow bool, key string, at time.Time, firings int64) error {
	tag, err := db.Exec(ctx, `INSERT INTO event_rule_breakers AS b (rule_id, shadow, key, tripped_at, firings) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (rule_id, shadow, key) DO UPDATE SET tripped_at = EXCLUDED.tripped_at, firings = EXCLUDED.firings, modified = now()
		WHERE b.tripped_at IS NULL`, r.ID, shadow, key, at, firings)
	if err != nil || tag.RowsAffected() == 0 || shadow || r.BreakerNotification == nil {
		return err
	}
	slog.Warn("event rule circuit breaker tripped", "rule", r.ID)
	subject := fmt.Sprintf("Event rule %s: the circuit breaker of key %q tripped", r.Name, key)
	about := map[string]any{"rule": Obj{"id": r.ID, "name": r.Name}, "key": key, "firings": firings, "window": r.BreakerWindow,
		"reset": api.Lookup("event_rules").URL(r.ID) + "reset_breaker/"}
	return ruleNotice(ctx, db, after, *r.BreakerNotification, nil, subject, about)
}

// ruleNotice sends one of a rule's own notifications once committed: about
// as the body, in text for an email.
func ruleNotice(ctx context.Context, db pgx.Tx, after *afterCommit, tpl int64, job *int64, subject string, about map[string]any) error {
	var body any = about
	var typ string
	if err := db.QueryRow(ctx, "SELECT notification_type FROM notification_templates WHERE id = $1", tpl).Scan(&typ); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if typ == "email" {
		b, _ := json.MarshalIndent(about, "", "  ")
		body = string(b)
	}
	id, err := createNotification(ctx, db, tpl, job, subject, body)
	if err != nil {
		return err
	}
	after.notifications = append(after.notifications, id)
	return nil
}

// resetBreakerView serves POST /event_rules/<id>/reset_breaker/: it closes
// the circuit breaker of the key given, or of every key.
func resetBreakerView(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	key, given := body["key"]
	if given {
		if _, ok := key.(string); !ok {
			api.WriteError(w, api.FieldError("key", "Expected the key whose breaker to reset."))
			return
		}
	}
	tag, err := c.DB().Exec(c, `UPDATE event_rule_breakers SET tripped_at = NULL, reset_at = now(), reset_by_id = $3, modified = now()
		WHERE rule_id = $1 AND tripped_at IS NOT NULL AND ($2::text IS NULL OR key = $2)`, obj["id"], key, nullID(c.User.UserID))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, Obj{"reset": tag.RowsAffected()})
}

// guards checks guardrails against the state of the database, caching the
// inventories it resolves.
type guards struct {
	ctx    context.Context
	db     api.DBTX
	invs   map[int64]*hostpattern.Inventory
	invOrg map[int64]int64
	jobs   []runningJob
	done   bool
	wins   map[int64][]window
	// past, in a backtest, stands for the jobs running at the time at.
	past *pastRuns
	at   time.Time
}

func newGuards(ctx context.Context, db api.DBTX) *guards {
	return &guards{ctx: ctx, db: db, invs: map[int64]*hostpattern.Inventory{}, invOrg: map[int64]int64{}}
}

type runningJob struct {
	// id is 0 for a launch a backtest simulated.
	id, inventory int64
	// org is the organization of the job's inventory: hosts are the same
	// by name within one.
	org   int64
	limit string
	// rule: launched by an event rule, or by a workflow one launched.
	rule bool
}

// inventory loads an inventory's enabled hosts and its groups, by name.
func (g *guards) inventory(id int64) (*hostpattern.Inventory, error) {
	if inv, ok := g.invs[id]; ok {
		return inv, nil
	}
	inv := &hostpattern.Inventory{Groups: map[string][]string{}, Children: map[string][]string{}}
	g.invs[id] = inv
	var kind string
	var org int64
	if err := g.db.QueryRow(g.ctx, "SELECT kind, organization_id FROM inventories WHERE id = $1", id).Scan(&kind, &org); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return inv, nil
		}
		return nil, err
	}
	g.invOrg[id] = org
	hostSQL := "SELECT name FROM hosts WHERE inventory_id = $1 AND enabled ORDER BY name"
	if kind == "smart" {
		hostSQL = "SELECT name FROM hosts WHERE enabled AND id IN (SELECT host_id FROM smart_inventory_hosts($1)) ORDER BY name"
	}
	rows, err := g.db.Query(g.ctx, hostSQL, id)
	if err != nil {
		return nil, err
	}
	if inv.Hosts, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	pairs := func(sql string, into map[string][]string) error {
		rows, err := g.db.Query(g.ctx, sql, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				return err
			}
			into[a] = append(into[a], b)
		}
		return rows.Err()
	}
	if err := pairs(`SELECT g.name, h.name FROM group_hosts gh JOIN groups g ON g.id = gh.group_id JOIN hosts h ON h.id = gh.host_id
		WHERE g.inventory_id = $1`, inv.Groups); err != nil {
		return nil, err
	}
	if err := pairs(`SELECT p.name, ch.name FROM group_children gc JOIN groups p ON p.id = gc.parent_id JOIN groups ch ON ch.id = gc.child_id
		WHERE p.inventory_id = $1`, inv.Children); err != nil {
		return nil, err
	}
	rows, err = g.db.Query(g.ctx, "SELECT name FROM groups WHERE inventory_id = $1", id)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if _, ok := inv.Groups[n]; !ok {
			inv.Groups[n] = nil
		}
	}
	return inv, nil
}

// hosts resolves a limit against an inventory.
func (g *guards) hosts(inventory int64, limit string) (hostpattern.Set, bool, error) {
	inv, err := g.inventory(inventory)
	if err != nil {
		return nil, false, err
	}
	s, approx := hostpattern.Resolve(inv, limit)
	return s, approx, nil
}

// targets are the hosts a launch would run on, by name, and the
// organizations of their inventories.
func (g *guards) targets(p *plan) (hostpattern.Set, map[int64]bool, bool, error) {
	all, orgs, approx := hostpattern.Set{}, map[int64]bool{}, false
	for _, t := range p.runs {
		s, a, err := g.hosts(t.inventory, t.limit)
		if err != nil {
			return nil, nil, false, err
		}
		approx = approx || a
		for h := range s {
			all[h] = true
		}
		if len(s) > 0 {
			orgs[g.invOrg[t.inventory]] = true
		}
	}
	return all, orgs, approx, nil
}

// running lists the jobs and ad hoc commands that are not finished, those
// of event rules marked, a workflow's jobs included.
func (g *guards) running() ([]runningJob, error) {
	if g.past != nil {
		return g.past.running(g.at), nil
	}
	if g.done {
		return g.jobs, nil
	}
	rows, err := g.db.Query(g.ctx, `WITH RECURSIVE ruled(id) AS (
			SELECT id FROM unified_jobs WHERE event_rule_id IS NOT NULL AND type <> 'workflow_approval' AND status = ANY($1)
			UNION SELECT n.job_id FROM workflow_node_jobs n JOIN ruled ON n.workflow_job_id = ruled.id)
		SELECT j.id, j.inventory_id, i.organization_id, j."limit", j.id IN (SELECT id FROM ruled) FROM unified_jobs j
		JOIN inventories i ON i.id = j.inventory_id
		WHERE j.type IN ('job', 'ad_hoc_command') AND j.status = ANY($1) ORDER BY j.id`, activeJobStatus)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var j runningJob
		if err := rows.Scan(&j.id, &j.inventory, &j.org, &j.limit, &j.rule); err != nil {
			return nil, err
		}
		g.jobs = append(g.jobs, j)
	}
	g.done = true
	return g.jobs, rows.Err()
}

func sortedHosts(s hostpattern.Set) []string {
	out := make([]string, 0, len(s))
	for h := range s {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func listHosts(hosts []string) string {
	if len(hosts) > 5 {
		return strings.Join(hosts[:5], ", ") + fmt.Sprintf(" and %d more", len(hosts)-5)
	}
	return strings.Join(hosts, ", ")
}

func capHosts(hosts []string) []string {
	if len(hosts) > maxHeldHosts {
		return hosts[:maxHeldHosts]
	}
	return hosts
}

// blastRadius holds a launch back beyond the rule's concurrent runs, on a
// host a running job uses, or beyond the share of an inventory group.
func (g *guards) blastRadius(r *eventRule, p *plan) (*hold, error) {
	if p.action != actionLaunch {
		return nil, nil
	}
	if r.MaxRuns > 0 {
		var n int64
		if g.past != nil {
			n = g.past.launches(g.at)
		} else if err := g.db.QueryRow(g.ctx, `SELECT count(*) FROM unified_jobs WHERE event_rule_id = $1 AND type <> 'workflow_approval' AND status = ANY($2)`,
			r.ID, activeJobStatus).Scan(&n); err != nil {
			return nil, err
		}
		if n >= r.MaxRuns {
			runs := "runs"
			if r.MaxRuns == 1 {
				runs = "run"
			}
			return &hold{guardrail: guardRuns, details: map[string]any{"running": n, "max": r.MaxRuns},
				reason: fmt.Sprintf("The rule is at its limit of %d %s at once.", r.MaxRuns, runs)}, nil
		}
	}
	if !r.HostLock && r.GroupShare <= 0 {
		return nil, nil
	}
	targets, orgs, approx, err := g.targets(p)
	if err != nil || len(targets) == 0 {
		return nil, err
	}
	all, err := g.running()
	if err != nil {
		return nil, err
	}
	var jobs []runningJob
	for _, j := range all {
		if orgs[j.org] {
			jobs = append(jobs, j)
		}
	}
	if r.HostLock {
		busy := hostpattern.Set{}
		var by []int64
		for _, j := range jobs {
			hs, _, err := g.hosts(j.inventory, j.limit)
			if err != nil {
				return nil, err
			}
			found := false
			for h := range hs {
				if targets[h] {
					busy[h], found = true, true
				}
			}
			if found {
				by = append(by, j.id)
			}
		}
		if len(busy) > 0 {
			list := sortedHosts(busy)
			reason := fmt.Sprintf("Job %d, still running, uses %s.", by[0], listHosts(list))
			if by[0] == 0 {
				reason = fmt.Sprintf("A launch of the rule, still running, uses %s.", listHosts(list))
			}
			return &hold{guardrail: guardHostLock, details: map[string]any{"hosts": capHosts(list), "jobs": by, "hosts_approximate": approx},
				reason: reason}, nil
		}
	}
	if r.GroupShare > 0 {
		return g.groupShare(r, p, targets, jobs)
	}
	return nil, nil
}

// groupShare holds a launch back that would put more than the rule's share
// of an inventory group (all of it included) under remediation at once:
// its hosts and those the jobs of event rules run on.
func (g *guards) groupShare(r *eventRule, p *plan, targets hostpattern.Set, jobs []runningJob) (*hold, error) {
	under := hostpattern.Set{}
	for h := range targets {
		under[h] = true
	}
	for _, j := range jobs {
		if !j.rule {
			continue
		}
		hs, _, err := g.hosts(j.inventory, j.limit)
		if err != nil {
			return nil, err
		}
		for h := range hs {
			under[h] = true
		}
	}
	for _, t := range p.runs {
		inv, err := g.inventory(t.inventory)
		if err != nil {
			return nil, err
		}
		names := []string{"all"}
		for n := range inv.Groups {
			if n != "all" {
				names = append(names, n)
			}
		}
		sort.Strings(names[1:])
		for _, n := range names {
			members := inv.GroupHosts(n)
			touched, count := false, int64(0)
			for h := range members {
				touched = touched || targets[h]
				if under[h] {
					count++
				}
			}
			if !touched {
				continue
			}
			size := int64(len(members))
			allowed := max(size*r.GroupShare/100, 1)
			if count > allowed {
				return &hold{guardrail: guardGroupShare,
					details: map[string]any{"group": n, "inventory": t.inventory, "hosts": size, "under_remediation": count, "allowed": allowed},
					reason: fmt.Sprintf("Launching would put %d of the %d hosts of group %s under remediation at once, more than %d%%.",
						count, size, n, r.GroupShare)}, nil
			}
		}
	}
	return nil, nil
}

// window is a maintenance window or a change freeze of the rule's
// organization.
type window struct {
	id           int64
	name, kind   string
	rrule        string
	duration     int64
	groups       []int64
	listsRule    bool
	organization bool
}

// windows holds back, or sends for approval as the rule says, an action
// inside a maintenance window or a change freeze that covers the rule: by
// listing it, by listing none (its organization's), or by listing a group
// with a host the launch runs on.
func (g *guards) windows(r *eventRule, p *plan, at time.Time) (*hold, error) {
	list, err := g.windowList(r)
	if err != nil {
		return nil, err
	}
	var targets hostpattern.Set
	for _, w := range list {
		rule, err := schedule.Parse(w.rrule)
		if err != nil {
			continue
		}
		_, end, active := rule.Active(at, time.Duration(w.duration)*time.Second)
		if !active {
			continue
		}
		details := map[string]any{"window": Obj{"id": w.id, "name": w.name, "kind": w.kind, "until": api.FormatTime(end)}}
		if !w.listsRule && !w.organization {
			if p.action != actionLaunch {
				continue
			}
			if targets == nil {
				if targets, _, _, err = g.targets(p); err != nil {
					return nil, err
				}
			}
			in, err := g.groupHosts(w.groups)
			if err != nil {
				return nil, err
			}
			var hosts []string
			for _, h := range sortedHosts(in) {
				if targets[h] {
					hosts = append(hosts, h)
				}
			}
			if len(hosts) == 0 {
				continue
			}
			details["hosts"] = capHosts(hosts)
		}
		guardrail, what := guardMaintenance, "maintenance window"
		if w.kind == windowFreeze {
			guardrail, what = guardFreeze, "change freeze"
		}
		h := &hold{guardrail: guardrail, details: details, approve: r.WindowAction == windowApproval,
			reason: fmt.Sprintf("Inside %s %s, until %s.", what, w.name, end.UTC().Format(time.RFC3339))}
		return h, nil
	}
	return nil, nil
}

// windowList loads the windows that may cover the rule, once.
func (g *guards) windowList(r *eventRule) ([]window, error) {
	if list, ok := g.wins[r.ID]; ok {
		return list, nil
	}
	rows, err := g.db.Query(g.ctx, `SELECT id, name, kind, rrule, duration, inventory_groups, event_rules @> jsonb_build_array($1::bigint),
			event_rules = '[]'::jsonb AND inventory_groups = '[]'::jsonb
		FROM maintenance_windows WHERE enabled AND organization_id = $2 AND (event_rules @> jsonb_build_array($1::bigint)
			OR event_rules = '[]'::jsonb OR inventory_groups <> '[]'::jsonb)
		ORDER BY kind, id`, r.ID, r.Org)
	if err != nil {
		return nil, err
	}
	var list []window
	for rows.Next() {
		var w window
		var groups []int64
		if err := rows.Scan(&w.id, &w.name, &w.kind, &w.rrule, &w.duration, &groups, &w.listsRule, &w.organization); err != nil {
			rows.Close()
			return nil, err
		}
		w.groups = groups
		list = append(list, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if g.wins == nil {
		g.wins = map[int64][]window{}
	}
	g.wins[r.ID] = list
	return list, nil
}

// groupHosts returns the hosts of groups, their descendants' included.
func (g *guards) groupHosts(ids []int64) (hostpattern.Set, error) {
	rows, err := g.db.Query(g.ctx, "SELECT inventory_id, name FROM groups WHERE id = ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	type ref struct {
		inv  int64
		name string
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ref, error) {
		var x ref
		return x, row.Scan(&x.inv, &x.name)
	})
	if err != nil {
		return nil, err
	}
	out := hostpattern.Set{}
	for _, x := range refs {
		inv, err := g.inventory(x.inv)
		if err != nil {
			return nil, err
		}
		for h := range inv.GroupHosts(x.name) {
			out[h] = true
		}
	}
	return out, nil
}

// launchRuns are the inventories and limits a launch runs with: a job
// template's, or those of each job template a workflow runs, its prompts
// applied.
func launchRuns(c *Ctx, typ string, tpl Obj, limit *string) ([]runTarget, error) {
	if typ == "job_template" {
		inv, ok := api.ID(tpl["inventory"])
		if !ok {
			return nil, nil
		}
		l := str(tpl["limit"])
		if limit != nil {
			l = *limit
		}
		return []runTarget{{inventory: inv, limit: l}}, nil
	}
	rows, err := c.DB().Query(c, `SELECT CASE WHEN j.ask_inventory_on_launch THEN coalesce(w.inventory_id, n.inventory_id, j.inventory_id) ELSE j.inventory_id END,
			j.ask_limit_on_launch, coalesce(nullif(w."limit", ''), nullif(n."limit", ''), j."limit")
		FROM workflow_job_template_nodes n JOIN unified_job_templates j ON j.id = n.unified_job_template_id AND j.type = 'job_template'
		JOIN unified_job_templates w ON w.id = n.workflow_job_template_id
		WHERE n.workflow_job_template_id = $1 ORDER BY n.id`, tpl["id"])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runTarget
	for rows.Next() {
		var inv *int64
		var ask bool
		var l string
		if err := rows.Scan(&inv, &ask, &l); err != nil {
			return nil, err
		}
		if inv == nil {
			continue
		}
		if ask && limit != nil {
			l = *limit
		}
		if !slices.Contains(out, runTarget{inventory: *inv, limit: l}) {
			out = append(out, runTarget{inventory: *inv, limit: l})
		}
	}
	return out, rows.Err()
}

// cancelRuleApprovals cancels the approvals a rule deleted waits for.
func cancelRuleApprovals(c *Ctx, obj Obj) error {
	if Jobs == nil {
		return nil
	}
	ids, err := idList(c, `SELECT id FROM unified_jobs WHERE event_rule_id = $1 AND type = 'workflow_approval' AND status = 'pending'`, obj["id"])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := Jobs.Cancel(c, id); err != nil {
			return err
		}
	}
	return nil
}
