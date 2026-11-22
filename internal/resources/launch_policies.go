package resources

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/launchpolicy"
	"github.com/damhau/awxnext/internal/vars"
)

const (
	policyShadow  = "shadow"
	policyEnforce = "enforce"

	policyAllowed     = "allowed"
	policyRefused     = "refused"
	policyWouldRefuse = "would_refuse"
)

// launchFacts is what a launch policy reads of a launch: the unified_jobs
// columns of the job it makes, and its relations. At launch the job doesn't
// exist yet; a backtest reads them from a past job.
type launchFacts struct {
	typ  string
	cols map[string]any
	// template is the content type of the template launched.
	templateCT  string
	credentials []int64
	// labels and groups are the job's; nil: the template's.
	labels, groups []int64
	branch         string
	user           int64
	workflow       int64
	at             time.Time
}

func (f *launchFacts) id(col string) int64 {
	n, _ := api.ID(f.cols[col])
	return n
}

func (f *launchFacts) text(col string) string {
	switch v := f.cols[col].(type) {
	case nil:
		return ""
	case string:
		return v
	case *string:
		if v != nil {
			return *v
		}
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func names(c *Ctx, sql string, args ...any) []any {
	out := []any{}
	rows, err := c.DB().Query(c, sql, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// input is the launch as a condition sees it. Every field is there, empty
// when the launch has none, so that a condition never fails on a launch it
// doesn't concern.
func (f *launchFacts) input(c *Ctx) map[string]any {
	in := map[string]any{
		"type": f.typ, "playbook": f.text("playbook"), "limit": f.text("limit"), "job_type": f.text("job_type"),
		"verbosity": f.id("verbosity"), "diff_mode": boolv(f.cols["diff_mode"]), "forks": f.id("forks"),
		"branch": f.branch, "launch_type": f.text("launch_type"), "schedule": "", "webhook": "", "event_rule": "", "image": "",
		"module": map[string]any{"name": f.text("module_name"), "args": f.text("module_args")},
	}
	tpl := f.id("unified_job_template_id")
	t := map[string]any{"id": tpl, "name": "", "type": "", "organization": "", "labels": []any{}, "branch": ""}
	if tpl != 0 {
		var name, typ, org, branch string
		c.DB().QueryRow(c, `SELECT t.name, t.type, coalesce(o.name, ''), t.scm_branch FROM unified_job_templates t
			LEFT JOIN organizations o ON o.id = t.organization_id WHERE t.id = $1`, tpl).Scan(&name, &typ, &org, &branch)
		t["name"], t["type"], t["organization"], t["branch"] = name, typ, org, branch
		t["labels"] = names(c, `SELECT l.name FROM unified_job_template_labels x JOIN labels l ON l.id = x.label_id
			WHERE x.unified_job_template_id = $1 ORDER BY l.name`, tpl)
	}
	in["template"] = t
	if f.labels != nil {
		in["labels"] = names(c, "SELECT name FROM labels WHERE id = ANY($1) ORDER BY name", f.labels)
	} else {
		in["labels"] = t["labels"]
	}
	p := map[string]any{"id": f.id("project_id"), "name": "", "url": "", "branch": "", "revision": ""}
	if pid := f.id("project_id"); pid != 0 {
		var name, url, branch, rev string
		c.DB().QueryRow(c, "SELECT name, scm_url, scm_branch, scm_revision FROM unified_job_templates WHERE id = $1", pid).Scan(&name, &url, &branch, &rev)
		p["name"], p["url"], p["branch"], p["revision"] = name, url, branch, rev
	}
	in["project"] = p
	inv := map[string]any{"id": f.id("inventory_id"), "name": "", "organization": "", "labels": []any{}, "hosts": int64(0)}
	if iid := f.id("inventory_id"); iid != 0 {
		var name, org string
		var hosts int64
		c.DB().QueryRow(c, `SELECT i.name, coalesce(o.name, ''), CASE WHEN i.kind = 'smart' THEN (SELECT count(*) FROM smart_inventory_hosts(i.id))
			ELSE (SELECT count(*) FROM hosts WHERE inventory_id = i.id) END
			FROM inventories i LEFT JOIN organizations o ON o.id = i.organization_id WHERE i.id = $1`, iid).Scan(&name, &org, &hosts)
		inv["name"], inv["organization"], inv["hosts"] = name, org, hosts
		inv["labels"] = names(c, "SELECT l.name FROM inventory_labels x JOIN labels l ON l.id = x.label_id WHERE x.inventory_id = $1 ORDER BY l.name", iid)
	}
	in["inventory"] = inv
	if ee := f.id("execution_environment_id"); ee != 0 {
		var image string
		c.DB().QueryRow(c, "SELECT image FROM execution_environments WHERE id = $1", ee).Scan(&image)
		in["image"] = image
	}
	creds := []any{}
	if len(f.credentials) > 0 {
		rows, err := c.DB().Query(c, `SELECT c.name, t.name, t.kind FROM credentials c JOIN credential_types t ON t.id = c.credential_type_id
			WHERE c.id = ANY($1) ORDER BY c.name, c.id`, f.credentials)
		if err == nil {
			for rows.Next() {
				var name, typ, kind string
				if rows.Scan(&name, &typ, &kind) == nil {
					creds = append(creds, map[string]any{"name": name, "type": typ, "kind": kind})
				}
			}
			rows.Close()
		}
	}
	in["credentials"] = creds
	if f.groups != nil {
		in["instance_groups"] = names(c, "SELECT g.name FROM unnest($1::bigint[]) WITH ORDINALITY x(id, n) JOIN instance_groups g ON g.id = x.id ORDER BY x.n", f.groups)
	} else {
		in["instance_groups"] = names(c, `SELECT g.name FROM instance_group_links x JOIN instance_groups g ON g.id = x.instance_group_id
			WHERE x.content_type = $1 AND x.object_id = $2 ORDER BY x.position`, f.templateCT, tpl)
	}
	wf := map[string]any{"id": f.workflow, "name": ""}
	if f.workflow != 0 {
		var name string
		c.DB().QueryRow(c, "SELECT name FROM unified_jobs WHERE id = $1", f.workflow).Scan(&name)
		wf["name"] = name
	}
	in["workflow"] = wf
	in["extra_vars"] = f.extraVars()
	u := map[string]any{"id": f.user, "username": "", "teams": []any{}, "superuser": false}
	if f.user != 0 {
		var username string
		var super bool
		c.DB().QueryRow(c, "SELECT username, is_superuser FROM users WHERE id = $1", f.user).Scan(&username, &super)
		u["username"], u["superuser"] = username, super
		u["teams"] = names(c, "SELECT name FROM teams WHERE id IN (SELECT user_teams($1)) ORDER BY name", f.user)
	}
	in["user"] = u
	if s := f.id("schedule_id"); s != 0 {
		var name string
		c.DB().QueryRow(c, "SELECT name FROM schedules WHERE id = $1", s).Scan(&name)
		in["schedule"] = name
	}
	if in["launch_type"] == "webhook" {
		in["webhook"] = f.text("webhook_service")
	}
	// A request's preview has no request yet: only its item's name.
	req := map[string]any{"id": f.id("catalog_request_id"), "item": f.text("catalog_item_name")}
	if r := f.id("catalog_request_id"); r != 0 {
		var item string
		c.DB().QueryRow(c, "SELECT i.name FROM catalog_requests q JOIN catalog_items i ON i.id = q.catalog_item_id WHERE q.id = $1", r).Scan(&item)
		req["item"] = item
	}
	in["request"] = req
	if r := f.id("event_rule_id"); r != 0 {
		var name string
		c.DB().QueryRow(c, "SELECT name FROM event_rules WHERE id = $1", r).Scan(&name)
		in["event_rule"] = name
	}
	return in
}

// extraVars are the job's extra vars with its survey passwords, and any
// value still encrypted, as $encrypted$.
func (f *launchFacts) extraVars() map[string]any {
	ev := map[string]any{}
	switch v := f.cols["extra_vars"].(type) {
	case string:
		ev, _ = vars.Parse(v)
	case map[string]any:
		for k, x := range v {
			ev[k] = x
		}
	}
	if ev == nil {
		ev = map[string]any{}
	}
	pw, _ := f.cols["survey_passwords"].(map[string]any)
	for k, x := range ev {
		if _, secret := pw[k]; secret {
			ev[k] = "$encrypted$"
		} else if s, ok := x.(string); ok && crypto.IsEncrypted(s) {
			ev[k] = "$encrypted$"
		}
	}
	return ev
}

type launchPolicy struct {
	id                             int64
	name, condition, message, mode string
}

// policiesFor are the policies that check a launch of template (of content
// type ct) on inventory: the global ones, and those linked to either or to
// their organizations.
func policiesFor(c *Ctx, template int64, ct string, inventory int64) ([]launchPolicy, error) {
	rows, err := c.DB().Query(c, `SELECT p.id, p.name, p.condition, p.message, p.mode FROM launch_policies p
		WHERE p.organization_id IS NULL OR p.id IN (SELECT l.launch_policy_id FROM launch_policy_links l
			WHERE (l.content_type = $2 AND l.object_id = $1) OR (l.content_type = 'inventory' AND l.object_id = $3)
				OR (l.content_type = 'organization' AND l.object_id IN (SELECT organization_id FROM unified_job_templates WHERE id = $1
					UNION SELECT organization_id FROM inventories WHERE id = $3)))
		ORDER BY p.name, p.id`, template, ct, inventory)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []launchPolicy
	for rows.Next() {
		var p launchPolicy
		if err := rows.Scan(&p.id, &p.name, &p.condition, &p.message, &p.mode); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// policyDecision is one policy's answer to a launch.
type policyDecision struct {
	Policy  int64  `json:"id"`
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Outcome string `json:"outcome"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

// policyVerdict is what the policies of a launch decided.
type policyVerdict struct {
	facts     *launchFacts
	decisions []policyDecision
}

func (v *policyVerdict) refusals() []policyDecision {
	var out []policyDecision
	if v == nil {
		return out
	}
	for _, d := range v.decisions {
		if d.Outcome == policyRefused {
			out = append(out, d)
		}
	}
	return out
}

func refusalText(d policyDecision) string {
	s := fmt.Sprintf("Refused by launch policy %q", d.Name)
	if d.Message != "" {
		s += ": " + d.Message
	}
	if d.Error != "" {
		s += fmt.Sprintf(" (its condition failed: %s)", d.Error)
	}
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

// explanation says why a launch was refused, naming each policy.
func (v *policyVerdict) explanation() string {
	var parts []string
	for _, d := range v.refusals() {
		parts = append(parts, refusalText(d))
	}
	return strings.Join(parts, " ")
}

func evalPolicies(c *Ctx, pols []launchPolicy, in map[string]any, at time.Time) []policyDecision {
	out := make([]policyDecision, 0, len(pols))
	for _, p := range pols {
		d := policyDecision{Policy: p.id, Name: p.name, Mode: p.mode, Outcome: policyAllowed, Message: p.message}
		refuses, err := launchpolicy.Refuses(c, p.condition, in, at)
		if err != nil {
			d.Error, refuses = err.Error(), true
		}
		if refuses {
			d.Outcome = policyWouldRefuse
			if p.mode == policyEnforce {
				d.Outcome = policyRefused
			}
		}
		out = append(out, d)
	}
	return out
}

// checkLaunch asks a launch's policies.
func checkLaunch(c *Ctx, f *launchFacts) (*policyVerdict, error) {
	pols, err := policiesFor(c, f.id("unified_job_template_id"), f.templateCT, f.id("inventory_id"))
	if err != nil || len(pols) == 0 {
		return &policyVerdict{facts: f}, err
	}
	return &policyVerdict{facts: f, decisions: evalPolicies(c, pols, f.input(c), f.at)}, nil
}

// errLaunchChecked stops a launch that was only to be checked.
var errLaunchChecked = errors.New("the launch was only checked")

// unattended launches have no caller to answer: a refusal makes a job that
// fails at once, so that it shows in the job list and notifies.
func unattended(f *launchFacts, ov *LaunchOverrides) bool {
	switch f.text("launch_type") {
	case "scheduled", "webhook", "callback", "workflow", "dependency":
		return true
	}
	return f.id("event_rule_id") != 0 || (ov != nil && ov.Unattended)
}

// gateLaunch checks a launch against its policies before its job is
// created. With ov.PolicyCheck, it only answers. A refused launch with a
// caller is an error, its decisions recorded apart from the launch's
// transaction; without one, the job is created to fail with the
// explanation, and its decisions are recorded with it (recordDecisions).
func gateLaunch(c *Ctx, f *launchFacts, ov *LaunchOverrides) (*policyVerdict, error) {
	if f.at.IsZero() {
		f.at = time.Now()
	}
	v, err := checkLaunch(c, f)
	if err != nil {
		return nil, err
	}
	if ov != nil && ov.PolicyCheck != nil {
		*ov.PolicyCheck = v
		return v, errLaunchChecked
	}
	refused := v.refusals()
	if len(refused) == 0 {
		return v, nil
	}
	if unattended(f, ov) {
		f.cols["job_explanation"] = v.explanation()
		return v, nil
	}
	if err := recordDecisions(c, c.App.DB, v, 0); err != nil {
		return nil, err
	}
	list := make([]any, len(refused))
	for i, d := range refused {
		list[i] = Obj{"id": d.Policy, "name": d.Name, "message": d.Message, "error": d.Error}
	}
	return nil, &api.Error{Status: 400, Body: Obj{"detail": v.explanation(), "launch_policies": list}}
}

// recordDecisions logs a verdict's decisions, with the job they let run
// or failed (0: none, the launch was refused at its caller).
func recordDecisions(ctx context.Context, db api.DBTX, v *policyVerdict, job int64) error {
	if v == nil || len(v.decisions) == 0 {
		return nil
	}
	f := v.facts
	var tplName string
	if tpl := f.id("unified_job_template_id"); tpl != 0 {
		db.QueryRow(ctx, "SELECT name FROM unified_job_templates WHERE id = $1", tpl).Scan(&tplName)
	}
	for _, d := range v.decisions {
		if _, err := db.Exec(ctx, `INSERT INTO launch_policy_decisions (launch_policy_id, policy_name, mode, outcome, message, error,
			unified_job_id, job_type, unified_job_template_id, template_name, inventory_id, user_id, launch_type)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			d.Policy, d.Name, d.Mode, d.Outcome, d.Message, d.Error, nullID(job), f.typ, nullID(f.id("unified_job_template_id")), tplName,
			nullID(f.id("inventory_id")), nullID(f.user), f.text("launch_type")); err != nil {
			return err
		}
	}
	return nil
}

// LaunchRefusal is why a pending job must fail before it runs: the launch
// policies that refused it when it was created without a caller.
func LaunchRefusal(ctx context.Context, m *jobs.Manager, job jobs.Row) string {
	var refused bool
	m.DB().QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM launch_policy_decisions WHERE unified_job_id = $1 AND outcome = $2)", job.ID(), policyRefused).Scan(&refused)
	if !refused {
		return ""
	}
	if e := job.Str("job_explanation"); e != "" {
		return e
	}
	return "Refused by a launch policy."
}

func idsOf(v any) []int64 {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []int64{}
	for _, x := range list {
		if id, ok := api.ID(x); ok {
			out = append(out, id)
		}
	}
	return out
}

// launchLabels are a launch's labels: its template's and those given.
func launchLabels(c *Ctx, template int64, given any) []int64 {
	out := []int64{}
	rows, err := c.DB().Query(c, "SELECT label_id FROM unified_job_template_labels WHERE unified_job_template_id = $1", template)
	if err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				out = append(out, id)
			}
		}
		rows.Close()
	}
	return append(out, idsOf(given)...)
}

func launchUser(c *Ctx, values map[string]any, ov *LaunchOverrides) int64 {
	if id, ok := api.ID(values["created_by_id"]); ok {
		return id
	}
	if ov != nil {
		return ov.UserID
	}
	return 0
}

func launchWorkflow(ov *LaunchOverrides) int64 {
	if ov == nil {
		return 0
	}
	return ov.Workflow
}

const adHocVerdict = "launch_policy_verdict"

// gateAdHoc checks an ad hoc command, its fields as the API names them,
// before it is created; AfterCreate records the decisions.
func gateAdHoc(c *Ctx, data Obj) error {
	var creds []int64
	if id, ok := api.ID(data["credential"]); ok {
		creds = append(creds, id)
	}
	var user int64
	if !c.User.Anonymous() {
		user = c.User.UserID
	}
	f := &launchFacts{typ: "ad_hoc_command", user: user, credentials: creds, cols: map[string]any{
		"inventory_id": data["inventory"], "limit": data["limit"], "job_type": data["job_type"], "verbosity": data["verbosity"],
		"diff_mode": data["diff_mode"], "forks": data["forks"], "execution_environment_id": data["execution_environment"],
		"extra_vars": data["extra_vars"], "launch_type": data["launch_type"], "module_name": data["module_name"], "module_args": data["module_args"],
	}}
	v, err := gateLaunch(c, f, nil)
	if err != nil {
		return err
	}
	if c.Extra != nil {
		c.Extra[adHocVerdict] = v
	}
	return nil
}
