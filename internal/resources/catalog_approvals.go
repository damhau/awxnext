package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/schedule"
)

// Approvals before a request runs (awxnext, roadmap M15-04): an item's
// rules hold a request for their approvers, and a window that lists the item
// refuses its requests or holds them for an exception. Each gate is a
// workflow approval, so the approvals page, workflow_approvals/ and their
// notifications serve it; the answers, of which a gate may need several,
// are kept beside it.

const (
	requestAwaiting = "awaiting_approval"
	requestDenied   = "denied"
	requestNew      = "new"

	approversTeamAdmins = "team_admins"
	approversTeam       = "team"
	approversUsers      = "users"

	gateRule   = "rule"
	gateFreeze = "freeze"

	maxApprovalRules       = 20
	maxRequiredApprovals   = 20
	maxRuleApprovers       = 200
	defaultApprovalTimeout = 7 * 24 * 3600
	maxApprovalTimeout     = 90 * 24 * 3600
	maxApprovalComment     = 2000
)

var approvalRuleKeys = map[string]bool{"name": true, "condition": true, "approvers": true, "team": true, "users_from": true,
	"required": true, "timeout": true}

// conditionNames are what a rule's expressions read besides the answers,
// which they read by variable too.
var conditionNames = map[string]bool{"answers": true, "team": true, "kind": true, "user": true, "request": true}

type itemRule struct {
	Name      string `json:"name"`
	Condition string `json:"condition"`
	Approvers string `json:"approvers"`
	Team      int64  `json:"team,omitempty"`
	UsersFrom string `json:"users_from,omitempty"`
	Required  int    `json:"required"`
	Timeout   int64  `json:"timeout"`
}

// ruleEnv is what a rule's condition and users_from read.
type ruleEnv struct {
	answers map[string]any
	picked  map[string]any
	team    map[string]any
	user    map[string]any
	request map[string]any
	kind    string
}

func (e *ruleEnv) vars(variables map[string]any) map[string]any {
	env := map[string]any{}
	for v, zero := range variables {
		env[v] = e.answers[v]
		if env[v] == nil {
			env[v] = zero
		}
	}
	env["answers"], env["user"], env["kind"], env["team"], env["request"] = e.answers, e.user, e.kind, nil, nil
	if e.team != nil {
		env["team"] = e.team
	}
	if e.request != nil {
		env["request"] = e.request
	}
	return env
}

// conditionVariables are the survey's variables that rules read by name,
// each with what an unanswered one reads as, of its question's type: not
// passwords, which rules never read, nor those named as what else they
// read.
func conditionVariables(spec map[string]any) map[string]any {
	out := map[string]any{}
	for _, q := range surveyQuestions(spec) {
		v := str(q["variable"])
		if q["type"] == "password" || conditionNames[v] {
			continue
		}
		switch q["type"] {
		case "integer":
			out[v] = int64(0)
		case "float":
			out[v] = float64(0)
		case "multiselect":
			out[v] = []any{}
		default:
			out[v] = ""
		}
	}
	return out
}

func ruleOptions(variables map[string]any, e *ruleEnv) []expr.Option {
	env := map[string]any{"answers": map[string]any{}, "user": map[string]any{}, "team": map[string]any{}, "request": map[string]any{}, "kind": ""}
	maps.Copy(env, variables)
	return []expr.Option{
		expr.Env(env),
		expr.Function("item", func(params ...any) (any, error) {
			name, _ := params[0].(string)
			return e.picked[name], nil
		}, new(func(string) any)),
	}
}

// checkApprovalRules checks an item's approval rules when it is saved.
func checkApprovalRules(c *Ctx, v any, org int64, spec map[string]any) ([]any, error) {
	bad := func(msg string) error { return api.FieldError("approval_rules", msg) }
	list, ok := v.([]any)
	if v == nil {
		list, ok = []any{}, true
	}
	if !ok {
		return nil, bad("Expected a list of rules.")
	}
	if len(list) > maxApprovalRules {
		return nil, bad(fmt.Sprintf("An item has at most %d approval rules.", maxApprovalRules))
	}
	variables := conditionVariables(spec)
	names := map[string]bool{}
	out := []any{}
	for i, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, bad(fmt.Sprintf("Rule %d: expected an object.", i))
		}
		for k := range m {
			if !approvalRuleKeys[k] {
				return nil, bad(fmt.Sprintf("Rule %d: %s is not one of name, condition, approvers, team, users_from, required, timeout.", i, k))
			}
		}
		r := itemRule{Name: strings.TrimSpace(str(m["name"])), Condition: strings.TrimSpace(str(m["condition"])), Approvers: str(m["approvers"]),
			UsersFrom: strings.TrimSpace(str(m["users_from"])), Required: 1, Timeout: defaultApprovalTimeout}
		if r.Name == "" || len(r.Name) > 128 {
			return nil, bad(fmt.Sprintf("Rule %d: a name of 1 to 128 characters is required.", i))
		}
		if names[r.Name] {
			return nil, bad(fmt.Sprintf("Two rules are named %q.", r.Name))
		}
		names[r.Name] = true
		if r.Condition != "" {
			if _, err := expr.Compile(r.Condition, append(ruleOptions(variables, &ruleEnv{}), expr.AsBool())...); err != nil {
				return nil, bad(fmt.Sprintf("%s: condition: %v", r.Name, err))
			}
		}
		switch r.Approvers {
		case approversTeamAdmins:
		case approversTeam:
			team, ok := api.ID(m["team"])
			var torg int64
			if !ok || c.DB().QueryRow(c, "SELECT organization_id FROM teams WHERE id = $1", team).Scan(&torg) != nil || torg != org {
				return nil, bad(fmt.Sprintf("%s: team must be a team of the item's organization.", r.Name))
			}
			r.Team = team
		case approversUsers:
			if r.UsersFrom == "" {
				return nil, bad(fmt.Sprintf("%s: users_from is required: an expression giving the approvers' usernames or email addresses.", r.Name))
			}
			if _, err := expr.Compile(r.UsersFrom, ruleOptions(variables, &ruleEnv{})...); err != nil {
				return nil, bad(fmt.Sprintf("%s: users_from: %v", r.Name, err))
			}
		default:
			return nil, bad(fmt.Sprintf("%s: approvers must be team_admins, team or users.", r.Name))
		}
		if r.Approvers != approversTeam && m["team"] != nil {
			return nil, bad(fmt.Sprintf("%s: team is for approvers: team.", r.Name))
		}
		if r.Approvers != approversUsers && r.UsersFrom != "" {
			return nil, bad(fmt.Sprintf("%s: users_from is for approvers: users.", r.Name))
		}
		if x, given := m["required"]; given && x != nil {
			n, ok := wholeNumber(x)
			if !ok || n < 1 || n > maxRequiredApprovals {
				return nil, bad(fmt.Sprintf("%s: required is a number of approvals, 1 to %d.", r.Name, maxRequiredApprovals))
			}
			r.Required = int(n)
		}
		if x, given := m["timeout"]; given && x != nil {
			n, ok := wholeNumber(x)
			if !ok || n < 0 || n > maxApprovalTimeout {
				return nil, bad(fmt.Sprintf("%s: timeout is in seconds, 0 (none) to %d.", r.Name, maxApprovalTimeout))
			}
			r.Timeout = int64(n)
		}
		var o map[string]any
		b, _ := json.Marshal(r)
		json.Unmarshal(b, &o)
		out = append(out, o)
	}
	return out, nil
}

func wholeNumber(v any) (float64, bool) {
	n, ok := num(v)
	return n, ok && n == math.Trunc(n)
}

func itemRules(v any) []itemRule {
	var out []itemRule
	b, _ := json.Marshal(v)
	json.Unmarshal(b, &out)
	return out
}

// gate is what holds a request: a rule that applies, or a freeze.
type gate struct {
	kind, rule, condition string
	approvers             []int64
	fallback              bool
	required              int
	timeout               int64
	why                   string
}

// requestGates are the gates a request waits for: the item's rules whose
// condition holds and the freeze it is in, if the item has an exception
// for it; a freeze without one refuses the request.
func requestGates(c *Ctx, ci *catalogItem, env *ruleEnv, requester int64, at time.Time) ([]*gate, error) {
	org, _ := api.ID(ci.item["organization"])
	ujt, _ := api.ID(ci.item["unified_job_template"])
	variables := conditionVariables(itemRuleSpec(c, ujt, itemActions(ci.item["actions"])))
	vars := env.vars(variables)
	broken := func(r itemRule, what string, err error) error {
		return api.Detail(400, fmt.Sprintf("The item's approval rule %q no longer applies (%s: %v): its owners must update it.", r.Name, what, err))
	}
	var gates []*gate
	for _, r := range itemRules(ci.item["approval_rules"]) {
		if r.Condition != "" {
			prog, err := expr.Compile(r.Condition, append(ruleOptions(variables, env), expr.AsBool())...)
			if err != nil {
				return nil, broken(r, "condition", err)
			}
			out, err := vm.Run(prog, vars)
			if err != nil {
				return nil, broken(r, "condition", err)
			}
			if b, _ := out.(bool); !b {
				continue
			}
		}
		var ids []int64
		var err error
		switch r.Approvers {
		case approversTeamAdmins:
			if tid, ok := api.ID(env.team["id"]); ok && env.team != nil {
				ids, err = permHolders(c, "team", tid, "change_team")
			}
		case approversTeam:
			ids, err = permHolders(c, "team", r.Team, "member_team")
		case approversUsers:
			var names []string
			if names, err = usersFrom(r.UsersFrom, variables, env, vars); err != nil {
				return nil, broken(r, "users_from", err)
			}
			ids, err = idList(c, "SELECT id FROM users WHERE is_active AND (lower(username) = ANY($1) OR (email <> '' AND lower(email) = ANY($1))) ORDER BY id", names)
		}
		if err != nil {
			return nil, err
		}
		g := &gate{kind: gateRule, rule: r.Name, condition: r.Condition, timeout: r.Timeout, why: "Rule " + r.Name}
		if r.Condition != "" {
			g.why += ": " + r.Condition
		}
		if err := g.resolve(c, ids, requester, org, r.Required); err != nil {
			return nil, err
		}
		gates = append(gates, g)
	}
	if env.kind == requestExtend {
		// An extension changes nothing that a freeze protects, and refusing
		// it would let the deployment expire inside the freeze.
		return gates, nil
	}
	f, err := itemFreeze(c, ci.item["id"], org, at)
	if err != nil || f == nil {
		return gates, err
	}
	what := "change freeze"
	if f.kind == windowMaintenance {
		what = "maintenance window"
	}
	team, ok := api.ID(ci.item["freeze_exception_team"])
	if !ok {
		return nil, &api.Error{Status: 400, Body: Obj{"detail": fmt.Sprintf("Requests for this item are refused inside %s %s, until %s.", what, f.name, api.FormatTime(f.end)),
			"window": Obj{"id": f.id, "name": f.name, "kind": f.kind, "until": api.FormatTime(f.end)}}}
	}
	ids, err := permHolders(c, "team", team, "member_team")
	if err != nil {
		return nil, err
	}
	g := &gate{kind: gateFreeze, rule: fmt.Sprintf("Exception to %s %s", what, f.name), timeout: int64(math.Ceil(f.end.Sub(at).Seconds())),
		why: fmt.Sprintf("Inside %s %s, until %s.", what, f.name, api.FormatTime(f.end))}
	g.timeout = max(g.timeout, 60)
	if err := g.resolve(c, ids, requester, org, 1); err != nil {
		return nil, err
	}
	return append(gates, g), nil
}

// resolve sets the gate's approvers: the group without the requester, or,
// with nobody left, the organization's admins (the superusers, if it has
// none).
func (g *gate) resolve(c *Ctx, group []int64, requester, org int64, required int) error {
	g.approvers = slices.DeleteFunc(slices.Clone(group), func(id int64) bool { return id == requester })
	if len(g.approvers) == 0 {
		admins, err := permHolders(c, "organization", org, "change_organization")
		if err != nil {
			return err
		}
		if len(admins) == 0 || (len(admins) == 1 && admins[0] == requester) {
			if admins, err = idList(c, "SELECT id FROM users WHERE is_superuser AND is_active ORDER BY id"); err != nil {
				return err
			}
		}
		g.approvers = slices.DeleteFunc(admins, func(id int64) bool { return id == requester })
		g.fallback = true
	}
	g.required = max(min(required, len(g.approvers)), 1)
	return nil
}

// usersFrom runs a rule's users_from: the lowercase usernames or email
// addresses it gives.
func usersFrom(src string, variables map[string]any, env *ruleEnv, vars map[string]any) ([]string, error) {
	prog, err := expr.Compile(src, ruleOptions(variables, env)...)
	if err != nil {
		return nil, err
	}
	out, err := vm.Run(prog, vars)
	if err != nil {
		return nil, err
	}
	var list []any
	switch x := out.(type) {
	case nil:
	case string:
		list = []any{x}
	case []any:
		list = x
	case []string:
		list = toAnyList(x)
	default:
		return nil, fmt.Errorf("gives %s, not a name or a list of names", kindOf(out))
	}
	if len(list) > maxRuleApprovers {
		return nil, fmt.Errorf("gives %d names, more than %d", len(list), maxRuleApprovers)
	}
	names := []string{}
	for _, x := range list {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("gives %s in its list, not a name", kindOf(x))
		}
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			names = append(names, s)
		}
	}
	return names, nil
}

// permHolders are the active users with perm on an object, by a role of
// their own or of a team they are in.
func permHolders(c *Ctx, ct string, obj int64, perm string) ([]int64, error) {
	return idList(c, `WITH RECURSIVE grantees(team) AS (
			SELECT ra.team_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
			WHERE ra.content_type = $1 AND ra.object_id = $2 AND $3 = ANY(rd.permissions) AND ra.team_id IS NOT NULL
			UNION
			SELECT ra.team_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id JOIN grantees g ON ra.object_id = g.team
			WHERE ra.content_type = 'team' AND 'member_team' = ANY(rd.permissions) AND ra.team_id IS NOT NULL)
		SELECT u.id FROM users u WHERE u.is_active AND u.id IN (
			SELECT ra.user_id FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
			WHERE ra.user_id IS NOT NULL AND ((ra.content_type = $1 AND ra.object_id = $2 AND $3 = ANY(rd.permissions))
				OR (ra.content_type = 'team' AND ra.object_id IN (SELECT team FROM grantees) AND 'member_team' = ANY(rd.permissions))))
		ORDER BY u.id`, ct, obj, perm)
}

type activeWindow struct {
	id         int64
	name, kind string
	end        time.Time
}

// itemFreeze is the enabled window of the organization that lists the item
// and is going on: a change freeze before a maintenance window.
func itemFreeze(c *Ctx, item any, org int64, at time.Time) (*activeWindow, error) {
	rows, err := c.DB().Query(c, `SELECT id, name, kind, rrule, duration FROM maintenance_windows
		WHERE enabled AND organization_id = $2 AND catalog_items @> jsonb_build_array($1::bigint) ORDER BY kind = 'freeze' DESC, id`, item, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w activeWindow
		var rrule string
		var duration int64
		if err := rows.Scan(&w.id, &w.name, &w.kind, &rrule, &duration); err != nil {
			return nil, err
		}
		rule, err := schedule.Parse(rrule)
		if err != nil {
			continue
		}
		if _, end, ok := rule.Active(at, time.Duration(duration)*time.Second); ok {
			w.end = end
			return &w, nil
		}
	}
	return nil, rows.Err()
}

// requestLaunch is what a held request launches once approved: the
// item's template, or for a request on a deployment, Template.
type requestLaunch struct {
	Vars         map[string]any  `json:"vars"`
	Passwords    []string        `json:"passwords"`
	ActivityVars map[string]bool `json:"activity_vars"`
	Template     int64           `json:"template,omitempty"`
}

// holdRequest creates the request's approvals, one per gate. what says
// what a request on a deployment asks.
func holdRequest(c *Ctx, ci *catalogItem, id int64, gates []*gate, team any, reason, what string) ([]int64, error) {
	who := c.User.Username
	if t, ok := api.ID(team); ok {
		var name string
		c.DB().QueryRow(c, "SELECT name FROM teams WHERE id = $1", t).Scan(&name)
		who += " for team " + name
	}
	var ids []int64
	for _, g := range gates {
		name := fmt.Sprintf("%s, request %d: %s", str(ci.item["name"]), id, g.rule)
		if len(name) > 512 {
			name = name[:509] + "..."
		}
		desc := fmt.Sprintf("Requested by %s. %s.", who, strings.TrimSuffix(g.why, "."))
		if what != "" {
			desc = fmt.Sprintf("Requested by %s: %s. %s.", who, what, strings.TrimSuffix(g.why, "."))
		}
		if reason != "" {
			desc += " Reason: " + reason
		}
		aid, err := jobs.Create(c, c.DB(), map[string]any{"type": "workflow_approval", "name": name, "description": desc,
			"approval_timeout": g.timeout, "status": "pending", "launch_type": "manual", "catalog_request_id": id,
			"organization_id": ci.item["organization"], "created_by_id": nullID(c.User.UserID)})
		if err != nil {
			return nil, err
		}
		if _, err := c.DB().Exec(c, `INSERT INTO catalog_request_approvals (approval_id, request_id, kind, rule, condition, approvers, fallback, required)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, aid, id, g.kind, g.rule, g.condition, g.approvers, g.fallback, g.required); err != nil {
			return nil, err
		}
		if err := jobLaunched(c, "workflow_approvals", aid); err != nil {
			return nil, err
		}
		ids = append(ids, aid)
	}
	return ids, nil
}

// announceApprovals sends a held request's approval notifications: the
// organization's, and the item's email to each gate's approvers.
func announceApprovals(c *Ctx, ci *catalogItem, request int64, approvals []int64) {
	for _, id := range approvals {
		notifyApproval(c, id, "running")
		var approvers []int64
		var rule, desc string
		var required int
		if c.App.DB.QueryRow(c, `SELECT g.approvers, g.rule, g.required, a.description FROM catalog_request_approvals g
			JOIN unified_jobs a ON a.id = g.approval_id WHERE g.approval_id = $1`, id).Scan(&approvers, &rule, &required, &desc) != nil {
			continue
		}
		need := "One approval is enough."
		if required > 1 {
			need = fmt.Sprintf("It needs %d approvals.", required)
		}
		body := fmt.Sprintf("%s\n\n%s\n\nApprove or deny it: %s\n", desc, need, portalApprovalURL(c.App, id))
		mailUsers(c, ci.item["notification_template"], &id, approvers, fmt.Sprintf("Approval needed: %s, request %d (%s)", str(ci.item["name"]), request, rule), body)
	}
}

func approvalsURL(app *api.App) string {
	return strings.TrimRight(app.Cfg.BaseURL, "/") + "/approvals"
}

// mailUsers emails users through an email notification template, to their
// own addresses rather than the template's recipients.
func mailUsers(c *Ctx, template any, job *int64, users []int64, subject, body string) {
	nt, ok := api.ID(template)
	if !ok || len(users) == 0 {
		return
	}
	var addrs []string
	rows, err := c.App.DB.Query(c, "SELECT email FROM users WHERE id = ANY($1) AND is_active AND email <> '' ORDER BY id", users)
	if err != nil {
		return
	}
	for rows.Next() {
		var a string
		rows.Scan(&a)
		addrs = append(addrs, a)
	}
	rows.Close()
	if len(addrs) == 0 {
		return
	}
	id, err := createNotification(c, c.App.DB, nt, job, subject, body)
	if err != nil {
		slog.Warn("catalog request email", "notification_template", nt, "err", err)
		return
	}
	c.App.DB.Exec(c, "UPDATE notifications SET addresses = $2, recipients = $3 WHERE id = $1", id, addrs, emailRecipients(Obj{"recipients": toAnyList(addrs)}))
	go deliver(context.Background(), c.App, id)
}

// CatalogTick resolves the requests whose approvals were decided while no
// hook could (a node stopped between an approval and its request), then
// reminds and expires deployments.
func CatalogTick(ctx context.Context, m *jobs.Manager) {
	defer DeploymentTick(ctx, m.App)
	c := &Ctx{Context: ctx, App: m.App, User: rbac.System(), Extra: map[string]any{}}
	ids, err := idList(c, `SELECT q.id FROM catalog_requests q WHERE q.status = 'awaiting_approval' AND (
			NOT EXISTS (SELECT 1 FROM catalog_request_approvals g WHERE g.request_id = q.id) OR
			EXISTS (SELECT 1 FROM catalog_request_approvals g JOIN unified_jobs a ON a.id = g.approval_id WHERE g.request_id = q.id AND a.status <> 'pending'))
		ORDER BY q.id LIMIT 50`)
	if err != nil {
		return
	}
	for _, id := range ids {
		if err := resolveRequest(ctx, m.App, id); err != nil && ctx.Err() == nil {
			slog.Warn("resolving a catalog request", "catalog_request", id, "err", err)
		}
	}
}

// outcome is what resolving a request decided, for after its commit.
type requestOutcome struct {
	status, explanation string
	requester, item     int64
	itemName            string
	cancel              []int64
	template            any
}

// resolveRequest acts on a held request once its approvals are decided:
// it runs it when every gate approves, and denies it at the first that
// doesn't. Exactly once: the request is locked, and only a request still
// awaiting approval is acted on.
func resolveRequest(ctx context.Context, app *api.App, id int64) error {
	sys := &Ctx{Context: ctx, App: app, User: rbac.System(), Extra: map[string]any{}}
	var out *requestOutcome
	err := sys.WithTx(func(c *Ctx) error {
		var status, itemName string
		var requester, item *int64
		var launch []byte
		err := c.DB().QueryRow(c, "SELECT status, created_by_id, catalog_item_id, item_name, launch FROM catalog_requests WHERE id = $1 FOR UPDATE",
			id).Scan(&status, &requester, &item, &itemName, &launch)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != requestAwaiting) {
			return nil
		}
		if err != nil {
			return err
		}
		denied, pending, err := requestApprovalState(c, id)
		if err != nil || (denied == "" && len(pending) > 0) {
			return err
		}
		out = &requestOutcome{status: requestDenied, explanation: denied, itemName: itemName}
		if requester != nil {
			out.requester = *requester
		}
		if item != nil {
			out.item = *item
			c.DB().QueryRow(c, "SELECT notification_template_id FROM catalog_items WHERE id = $1", *item).Scan(&out.template)
		}
		if denied != "" {
			out.cancel = pending
			_, err := c.DB().Exec(c, "UPDATE catalog_requests SET status = $2, explanation = $3, launch = NULL, modified = now() WHERE id = $1",
				id, requestDenied, denied)
			return err
		}
		job, why := launchApproved(c, id, out.requester, out.item, launch)
		out.status, out.explanation = requestPending, "Approved."
		switch {
		case why != "":
			out.status, out.explanation = requestFailed, why
		case job == 0:
			out.status = requestSuccessful
		}
		_, err = c.DB().Exec(c, "UPDATE catalog_requests SET status = $2, explanation = $3, unified_job_id = $4, launch = NULL, modified = now() WHERE id = $1",
			id, out.status, out.explanation, nullID(job))
		return err
	})
	if err != nil || out == nil {
		return err
	}
	for _, a := range out.cancel {
		if Jobs != nil {
			Jobs.Cancel(ctx, a)
		}
	}
	subject := fmt.Sprintf("Your request %d for %s is approved", id, out.itemName)
	switch out.status {
	case requestDenied:
		subject = fmt.Sprintf("Your request %d for %s is denied", id, out.itemName)
	case requestFailed:
		subject = fmt.Sprintf("Your request %d for %s is approved but could not run", id, out.itemName)
	}
	mailUsers(sys, out.template, nil, []int64{out.requester}, subject, out.explanation+"\n\n"+portalRequestURL(app, id)+"\n")
	return nil
}

// requestApprovalState says why a request is denied (empty while it
// isn't) and which of its approvals still wait.
func requestApprovalState(c *Ctx, request int64) (string, []int64, error) {
	rows, err := c.DB().Query(c, `SELECT a.id, a.status, a.timed_out, g.rule, d.username, d.comment, d.as_admin
		FROM catalog_request_approvals g JOIN unified_jobs a ON a.id = g.approval_id
		LEFT JOIN LATERAL (SELECT x.username, x.comment, x.as_admin FROM catalog_approval_answers x
			WHERE x.approval_id = a.id AND NOT x.approved ORDER BY x.id DESC LIMIT 1) d ON true
		WHERE g.request_id = $1 ORDER BY a.id`, request)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var denied string
	var pending []int64
	n := 0
	for rows.Next() {
		var aid int64
		var status, rule string
		var timedOut bool
		var by, comment *string
		var asAdmin *bool
		if err := rows.Scan(&aid, &status, &timedOut, &rule, &by, &comment, &asAdmin); err != nil {
			return "", nil, err
		}
		n++
		switch {
		case status == "pending":
			pending = append(pending, aid)
		case status == "successful" || denied != "":
		case timedOut:
			denied = fmt.Sprintf("The approval for %s timed out.", rule)
		case status == "failed" && by != nil:
			who := *by
			if asAdmin != nil && *asAdmin {
				who += " (as an organization admin)"
			}
			denied = fmt.Sprintf("Denied by %s for %s: %s", who, rule, *comment)
		case status == "canceled":
			denied = fmt.Sprintf("The approval for %s was canceled.", rule)
		default:
			denied = fmt.Sprintf("The approval for %s was denied.", rule)
		}
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if n == 0 {
		denied = "Its approvals were deleted."
	}
	return denied, pending, nil
}

// launchApproved launches an approved request as its requester, who must
// still be able to request the item, or for a request on a deployment, still
// own it. It returns the job, or why it could not run.
func launchApproved(c *Ctx, id, requester, item int64, launch []byte) (int64, string) {
	if item == 0 {
		return 0, "Approved, but the catalog item was deleted."
	}
	p, err := auth.LoadPrincipal(c, c.App, requester)
	if err != nil {
		return 0, "Approved, but its requester could not be loaded: " + err.Error()
	}
	if p == nil {
		return 0, "Approved, but its requester's account is gone or disabled."
	}
	rc := *c
	rc.User = p
	var kind string
	var deployment *int64
	if err := c.DB().QueryRow(c, "SELECT kind, deployment_id FROM catalog_requests WHERE id = $1", id).Scan(&kind, &deployment); err != nil {
		return 0, "Approved, but " + err.Error()
	}
	var l requestLaunch
	if err := json.Unmarshal(launch, &l); err != nil || l.Vars == nil {
		return 0, "Approved, but what it launches was lost."
	}
	obj, err := api.Get(&rc, api.Lookup("catalog_items"), item)
	if err != nil {
		return 0, "Approved, but the catalog item was deleted."
	}
	ci := &catalogItem{item: obj}
	if kind == requestNew {
		ok, err := api.HasPerm(&rc, api.Lookup("catalog_items"), obj, "request")
		if err != nil || !ok {
			return 0, "Approved, but its requester can no longer request the item."
		}
		if ci, err = loadCatalogItem(&rc, obj); err != nil {
			return 0, "Approved, but " + errorText(err)
		}
	} else if why := deploymentRequestValid(&rc, deployment, kind, obj, &l); why != "" {
		return 0, "Approved, but " + why
	}
	if _, err := c.DB().Exec(c, "SAVEPOINT approved_launch"); err != nil {
		return 0, "Approved, but " + err.Error()
	}
	job, err := startRequest(&rc, id, kind, deployment, ci, l)
	if err != nil {
		c.DB().Exec(c, "ROLLBACK TO SAVEPOINT approved_launch")
		return 0, "Approved, but it could not run: " + errorText(err)
	}
	return job, ""
}

func errorText(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		if m, ok := ae.Body.(Obj); ok {
			if d, ok := m["detail"].(string); ok {
				return d
			}
		}
		b, _ := json.Marshal(ae.Body)
		return string(b)
	}
	return err.Error()
}

// approvalRequest is the catalog request an approval holds, or 0.
func approvalRequest(c *Ctx, job Obj) int64 {
	var id int64
	c.DB().QueryRow(c, "SELECT coalesce(catalog_request_id, 0) FROM unified_jobs WHERE id = $1 AND type = 'workflow_approval'", job["id"]).Scan(&id)
	return id
}

type requestGate struct {
	approvers           []int64
	required            int
	org, requester      int64
	request             int64
	rule                string
	answered, admin, in bool
}

func loadRequestGate(c *Ctx, approval any) (*requestGate, error) {
	g := &requestGate{}
	err := c.DB().QueryRow(c, `SELECT g.approvers, g.required, g.rule, q.id, q.organization_id, coalesce(q.created_by_id, 0),
			EXISTS (SELECT 1 FROM catalog_approval_answers x WHERE x.approval_id = g.approval_id AND x.user_id = $2)
		FROM catalog_request_approvals g JOIN catalog_requests q ON q.id = g.request_id WHERE g.approval_id = $1`, approval, c.User.UserID).
		Scan(&g.approvers, &g.required, &g.rule, &g.request, &g.org, &g.requester, &g.answered)
	if err != nil {
		return nil, err
	}
	g.in = slices.Contains(g.approvers, c.User.UserID)
	return g, nil
}

// canAnswer says whether the user may answer: nobody answers their own
// request; the gate's approvers answer once, and the organization's admins
// may answer any, as admins.
func (g *requestGate) canAnswer(c *Ctx) error {
	if g.requester == c.User.UserID {
		return api.Detail(403, "Nobody approves their own request.")
	}
	if g.answered {
		return api.Detail(400, "You already answered this approval.")
	}
	if g.in {
		return nil
	}
	ok := c.User.Superuser
	if !ok {
		var err error
		if ok, err = api.HasPermID(c, "organizations", g.org, "change"); err != nil && !errors.Is(err, api.ErrNotFound) {
			return err
		}
	}
	if !ok {
		return api.ErrForbidden
	}
	g.admin = true
	return nil
}

// decideRequestApproval records an answer to a request's approval, and
// decides the approval on a deny, an admin's answer or enough approvals.
func decideRequestApproval(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj, approve bool) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	comment, isStr := body["comment"].(string)
	if body["comment"] != nil && !isStr {
		api.WriteError(w, api.FieldError("comment", "Not a valid string."))
		return
	}
	comment = strings.TrimSpace(comment)
	if !approve && comment == "" {
		api.WriteError(w, api.FieldError("comment", "A deny needs a comment: say why."))
		return
	}
	if len(comment) > maxApprovalComment {
		api.WriteError(w, api.FieldError("comment", fmt.Sprintf("At most %d characters.", maxApprovalComment)))
		return
	}
	id, _ := api.ID(obj["id"])
	decision := ""
	err = c.WithTx(func(tc *Ctx) error {
		if _, err := tc.DB().Exec(tc, "SELECT 1 FROM catalog_request_approvals WHERE approval_id = $1 FOR UPDATE", id); err != nil {
			return err
		}
		g, err := loadRequestGate(tc, id)
		if err != nil {
			return err
		}
		if err := g.canAnswer(tc); err != nil {
			return err
		}
		var status string
		if err := tc.DB().QueryRow(tc, "SELECT status FROM unified_jobs WHERE id = $1", id).Scan(&status); err != nil {
			return err
		}
		if status != "pending" {
			return api.BadRequest("This workflow step has already been approved or denied.")
		}
		if _, err := tc.DB().Exec(tc, `INSERT INTO catalog_approval_answers (approval_id, user_id, username, approved, comment, as_admin)
			VALUES ($1, $2, $3, $4, $5, $6)`, id, tc.User.UserID, tc.User.Username, approve, comment, g.admin); err != nil {
			return err
		}
		var yes int
		if err := tc.DB().QueryRow(tc, "SELECT count(*) FROM catalog_approval_answers WHERE approval_id = $1 AND approved", id).Scan(&yes); err != nil {
			return err
		}
		switch {
		case !approve:
			decision = "failed"
		case g.admin || yes >= g.required:
			decision = "successful"
		}
		if decision != "" {
			tag, err := tc.DB().Exec(tc, `UPDATE unified_jobs SET approved_or_denied_by_id = $2, started = created
				WHERE id = $1 AND status = 'pending' AND approved_or_denied_by_id IS NULL`, id, nullID(tc.User.UserID))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return api.BadRequest("This workflow step has already been approved or denied.")
			}
		}
		res := api.Lookup("catalog_requests")
		sys := *tc
		sys.User = rbac.System()
		req, err := api.Get(&sys, res, g.request)
		if err != nil {
			return err
		}
		answer := Obj{"approval": id, "rule": g.rule, "answer": map[bool]string{true: "approved", false: "denied"}[approve], "as_admin": g.admin}
		if comment != "" {
			answer["comment"] = comment
		}
		return api.Activity(tc, "update", res, req, Obj{"approval_answer": answer}, nil, nil, "")
	})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if decision != "" {
		if err := Jobs.Complete(c, id, jobs.Result{Status: decision}); err != nil {
			api.WriteError(w, err)
			return
		}
		notifyApproval(c, id, map[bool]string{true: "approved", false: "denied"}[approve])
	}
	w.WriteHeader(204)
}

// requestApprovalInfo is what a request's approval shows besides the
// approval itself: its gate, approvers and answers.
func requestApprovalInfo(c *Ctx, approval any) Obj {
	var kind, rule, condition string
	var approvers []int64
	var fallback bool
	var required int
	if c.DB().QueryRow(c, "SELECT kind, rule, condition, approvers, fallback, required FROM catalog_request_approvals WHERE approval_id = $1",
		approval).Scan(&kind, &rule, &condition, &approvers, &fallback, &required) != nil {
		return nil
	}
	who, err := people(c, approvers)
	if err != nil {
		who = []Obj{}
	}
	answers := []Obj{}
	approved := 0
	if rows, err := c.DB().Query(c, `SELECT user_id, username, approved, comment, as_admin, created FROM catalog_approval_answers
		WHERE approval_id = $1 ORDER BY id`, approval); err == nil {
		for rows.Next() {
			var uid *int64
			var name, comment string
			var ok, admin bool
			var at time.Time
			rows.Scan(&uid, &name, &ok, &comment, &admin, &at)
			if ok {
				approved++
			}
			answers = append(answers, Obj{"user": uid, "username": name, "approved": ok, "comment": comment, "as_admin": admin, "created": api.FormatTime(at)})
		}
		rows.Close()
	}
	return Obj{"kind": kind, "rule": rule, "condition": condition, "approvers": who, "fallback": fallback, "required": required,
		"approved": approved, "answers": answers}
}

// requestApprovals lists a request's approvals for its approvals field.
func requestApprovals(c *Ctx, request any) []Obj {
	out := []Obj{}
	rows, err := c.DB().Query(c, `SELECT a.id, a.name, a.status, a.timed_out, a.approval_timeout, a.created FROM catalog_request_approvals g
		JOIN unified_jobs a ON a.id = g.approval_id WHERE g.request_id = $1 ORDER BY a.id`, request)
	if err != nil {
		return out
	}
	var list []Obj
	for rows.Next() {
		var id, timeout int64
		var name, status string
		var timedOut bool
		var created time.Time
		if rows.Scan(&id, &name, &status, &timedOut, &timeout, &created) != nil {
			continue
		}
		o := Obj{"id": id, "name": name, "status": status, "timed_out": timedOut, "expires": nil}
		if timeout > 0 {
			o["expires"] = api.FormatTime(created.Add(time.Duration(timeout) * time.Second))
		}
		list = append(list, o)
	}
	rows.Close()
	for _, o := range list {
		maps.Copy(o, requestApprovalInfo(c, o["id"]))
		out = append(out, o)
	}
	return out
}
