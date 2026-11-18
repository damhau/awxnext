package resources

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
)

// The portal (awxnext, roadmap M15-08): what the requesters' shell needs
// beyond the catalog's resources: whether the user only requests, the
// organization's name and logo, what needs the user, the approvals a
// request would wait for, and a request's progress.

const (
	maxPortalLogo   = 64 * 1024
	expiringWithin  = 3 * 24 * time.Hour
	maxNeedsYouRows = 20
)

// requestOnlyPerms are the permissions of the roles a requester holds: an
// organization's or a team's membership, and the requester roles.
var requestOnlyPerms = []string{"view_organization", "member_organization", "view_team", "member_team", "view_executionenvironment", "view_folder",
	"view_collectionnamespace", "view_catalogitem", "request_catalogitem"}

var portalLogoTypes = []string{"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/webp;base64,", "data:image/svg+xml;base64,"}

func installPortal(app *api.App) {
	app.Handle("GET", "/api/v2/portal/", portalView)
}

func portalView(c *Ctx, w http.ResponseWriter, r *http.Request) {
	only, err := requestOnly(c)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	brand, err := portalBrand(c)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	needs, err := needsYou(c)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, Obj{"request_only": only, "brand": brand, "needs_you": needs})
}

// requestOnly is whether the user holds roles, directly or through their
// teams, and every one only lets them request catalog items: they land on
// the portal.
func requestOnly(c *Ctx) (bool, error) {
	if c.User.Superuser || c.User.Auditor || c.User.Anonymous() {
		return false, nil
	}
	var requests, other bool
	err := c.DB().QueryRow(c, `SELECT coalesce(bool_or('request_catalogitem' = ANY(rd.permissions)), false), coalesce(bool_or(NOT rd.permissions <@ $2::text[]), false)
		FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
		WHERE ra.user_id = $1 OR ra.team_id IN (SELECT user_teams($1))`, c.User.UserID, requestOnlyPerms).Scan(&requests, &other)
	return requests && !other, err
}

// portalBrand is the name and logo of the first organization the user sees
// that sets them.
func portalBrand(c *Ctx) (any, error) {
	rows, err := c.DB().Query(c, "SELECT id, name, portal_title, portal_logo FROM organizations WHERE portal_title <> '' OR portal_logo <> '' ORDER BY id")
	if err != nil {
		return nil, err
	}
	type brand struct {
		id                int64
		name, title, logo string
	}
	var list []brand
	for rows.Next() {
		var b brand
		if err := rows.Scan(&b.id, &b.name, &b.title, &b.logo); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, b)
	}
	rows.Close()
	for _, b := range list {
		ok, err := c.User.HasOrg(c, c.App.DB, "view_organization", b.id)
		if err != nil {
			return nil, err
		}
		if ok {
			title := b.title
			if title == "" {
				title = b.name
			}
			return Obj{"organization": b.id, "title": title, "logo": b.logo}, nil
		}
	}
	return nil, nil
}

// needsYou is what waits on the user: their requests not yet done, their
// deployments that expire within 3 days, and the requests they can approve.
func needsYou(c *Ctx) (Obj, error) {
	out := Obj{"requests": []Obj{}, "deployments": []Obj{}, "approvals": 0}
	if c.User.Anonymous() {
		return out, nil
	}
	rows, err := c.DB().Query(c, `SELECT id, item_name, kind, status, created FROM catalog_requests WHERE created_by_id = $1 AND status = ANY($2)
		ORDER BY created DESC LIMIT $3`, c.User.UserID, unfinishedRequests, maxNeedsYouRows)
	if err != nil {
		return nil, err
	}
	var requests []Obj
	for rows.Next() {
		var id int64
		var item, kind, status string
		var created time.Time
		if err := rows.Scan(&id, &item, &kind, &status, &created); err != nil {
			rows.Close()
			return nil, err
		}
		requests = append(requests, Obj{"id": id, "item_name": item, "kind": kind, "status": status, "created": api.FormatTime(created)})
	}
	rows.Close()
	if requests != nil {
		out["requests"] = requests
	}
	rows, err = c.DB().Query(c, `SELECT id, name, expires_at FROM deployments WHERE status = 'active' AND expires_at < $2
		AND (owner_id = $1 OR team_id IN (SELECT user_teams($1))) ORDER BY expires_at LIMIT $3`, c.User.UserID, time.Now().Add(expiringWithin), maxNeedsYouRows)
	if err != nil {
		return nil, err
	}
	var deployments []Obj
	for rows.Next() {
		var id int64
		var name string
		var expires time.Time
		if err := rows.Scan(&id, &name, &expires); err != nil {
			rows.Close()
			return nil, err
		}
		deployments = append(deployments, Obj{"id": id, "name": name, "expires_at": api.FormatTime(expires)})
	}
	rows.Close()
	if deployments != nil {
		out["deployments"] = deployments
	}
	var approvals int64
	if err := c.DB().QueryRow(c, `SELECT count(*) FROM catalog_request_approvals g JOIN unified_jobs a ON a.id = g.approval_id
		WHERE a.status = 'pending' AND $1 = ANY(g.approvers)
		AND NOT EXISTS (SELECT 1 FROM catalog_approval_answers x WHERE x.approval_id = g.approval_id AND x.user_id = $1)`, c.User.UserID).Scan(&approvals); err != nil {
		return nil, err
	}
	out["approvals"] = approvals
	return out, nil
}

func checkPortalLogo(c *Ctx, v any, data Obj) error {
	s, _ := v.(string)
	if s == "" {
		return nil
	}
	if len(s) > maxPortalLogo {
		return fmt.Errorf("A logo of %d KB at most.", maxPortalLogo/1024)
	}
	for _, p := range portalLogoTypes {
		if strings.HasPrefix(s, p) {
			return nil
		}
	}
	return errors.New("A data URL of a PNG, JPEG, WebP or SVG image, in base64.")
}

// people names users for requesters: their full name, or their username.
func people(c *Ctx, ids []int64) ([]Obj, error) {
	rows, err := c.DB().Query(c, `SELECT id, username, trim(first_name || ' ' || last_name) FROM users WHERE id = ANY($1) ORDER BY username`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Obj{}
	for rows.Next() {
		var id int64
		var username, name string
		if err := rows.Scan(&id, &username, &name); err != nil {
			return nil, err
		}
		if name == "" {
			name = username
		}
		out = append(out, Obj{"id": id, "username": username, "name": name})
	}
	return out, rows.Err()
}

// gatePreview is a gate as a requester sees it before sending.
func gatePreview(c *Ctx, g *gate) (Obj, error) {
	who, err := people(c, g.approvers)
	if err != nil {
		return nil, err
	}
	return Obj{"kind": g.kind, "rule": g.rule, "why": g.why, "approvers": who, "required": g.required, "fallback": g.fallback, "timeout": g.timeout}, nil
}

// handleRequestPreview answers what a request would do, without sending
// it: the approvals it would wait for, or why it would be refused, and the
// quota of the team it is for.
func handleRequestPreview(c *Ctx, w http.ResponseWriter, r *http.Request, item Obj) {
	ci, err := loadCatalogItem(c, item)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	p, err := prepareRequest(c, ci, normalizeObj(body), 0, now)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := Obj{"approvals": []Obj{}, "refused": nil, "quota": nil}
	gates, err := requestGates(c, ci, p.env, c.User.UserID, now)
	if err == nil {
		var g *gate
		if g, err = quotaGate(c, ci, p.team, c.User.UserID, now); g != nil {
			gates = append(gates, g)
		}
	}
	var refusal *api.Error
	switch {
	case errors.As(err, &refusal) && refusal.Status == 400:
		out["refused"] = refusal.Body
	case err != nil:
		api.WriteError(w, err)
		return
	}
	if out["refused"] == nil {
		for _, g := range gates {
			o, err := gatePreview(c, g)
			if err != nil {
				api.WriteError(w, err)
				return
			}
			out["approvals"] = append(out["approvals"].([]Obj), o)
		}
	}
	if _, _, limited := itemQuotas(ci.item); limited {
		u, err := holderUsage(c, ci.item, requestHolder(p.team, c.User.UserID), now)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out["quota"] = u.obj(ci.item)
	}
	api.WriteJSON(w, 200, out)
}

type progressStep struct {
	name  string
	state string
}

// handleRequestProgress gives a request's steps, the playbook's plays (or
// the workflow's nodes) as the item names them, and how far it is: its
// tasks against those of the template's last successful run.
func handleRequestProgress(c *Ctx, w http.ResponseWriter, r *http.Request, req Obj) {
	var jobID, ujt int64
	var typ, status string
	var labels map[string]any
	err := c.DB().QueryRow(c, `SELECT coalesce(q.unified_job_id, 0), coalesce(j.type, ''), coalesce(j.status, ''), coalesce(j.unified_job_template_id, 0), coalesce(i.step_labels, '{}')
		FROM catalog_requests q LEFT JOIN unified_jobs j ON j.id = q.unified_job_id LEFT JOIN catalog_items i ON i.id = q.catalog_item_id WHERE q.id = $1`,
		req["id"]).Scan(&jobID, &typ, &status, &ujt, &labels)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := Obj{"steps": []Obj{}, "percent": nil, "job": nil}
	if jobID == 0 {
		api.WriteJSON(w, 200, out)
		return
	}
	var steps []progressStep
	var done, expected int64
	if typ == "workflow_job" {
		steps, done, expected, err = workflowProgress(c, jobID)
	} else {
		steps, done, expected, err = playbookProgress(c, jobID, ujt, status)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	list := []Obj{}
	for _, s := range steps {
		label, _ := labels[s.name].(string)
		if label == "" {
			label = s.name
		}
		list = append(list, Obj{"name": s.name, "label": label, "state": s.state})
	}
	out["steps"] = list
	switch {
	case status == "successful":
		out["percent"] = 100
	case expected > 0 && !finishedStatus(status):
		out["percent"] = min(99, done*100/expected)
	}
	res := api.Lookup("jobs")
	if typ == "workflow_job" {
		res = api.Lookup("workflow_jobs")
	}
	canSee, err := api.CanView(c, res, jobID)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out["job"] = Obj{"id": jobID, "type": typ, "status": status, "can_see": canSee}
	api.WriteJSON(w, 200, out)
}

func playNames(c *Ctx, job int64) ([]string, error) {
	rows, err := c.DB().Query(c, "SELECT play FROM job_events WHERE job_id = $1 AND event = 'playbook_on_play_start' ORDER BY counter", job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func taskCount(c *Ctx, job int64) (int64, error) {
	var n int64
	err := c.DB().QueryRow(c, "SELECT count(*) FROM job_events WHERE job_id = $1 AND event = 'playbook_on_task_start'", job).Scan(&n)
	return n, err
}

func lastSuccessful(c *Ctx, ujt, besides int64) (int64, error) {
	var id int64
	err := c.DB().QueryRow(c, `SELECT id FROM unified_jobs WHERE unified_job_template_id = $1 AND status = 'successful' AND id <> $2
		ORDER BY finished DESC NULLS LAST, id DESC LIMIT 1`, ujt, besides).Scan(&id)
	if err != nil {
		return 0, nil
	}
	return id, nil
}

// playbookProgress: the plays that started, the last one running (or
// failed), then the plays of the last successful run still to come.
func playbookProgress(c *Ctx, job, ujt int64, status string) ([]progressStep, int64, int64, error) {
	plays, err := playNames(c, job)
	if err != nil {
		return nil, 0, 0, err
	}
	done, err := taskCount(c, job)
	if err != nil {
		return nil, 0, 0, err
	}
	var expected int64
	var upcoming []string
	if last, _ := lastSuccessful(c, ujt, job); last != 0 {
		if expected, err = taskCount(c, last); err != nil {
			return nil, 0, 0, err
		}
		before, err := playNames(c, last)
		if err != nil {
			return nil, 0, 0, err
		}
		if len(before) > len(plays) {
			upcoming = before[len(plays):]
		}
	}
	var steps []progressStep
	for i, p := range plays {
		state := "done"
		if i == len(plays)-1 {
			switch status {
			case "successful":
			case "failed", "error":
				state = "failed"
			case "canceled":
				state = "canceled"
			default:
				state = "running"
			}
		}
		steps = append(steps, progressStep{name: p, state: state})
	}
	if !finishedStatus(status) {
		for _, p := range upcoming {
			steps = append(steps, progressStep{name: p, state: "pending"})
		}
	}
	return steps, done, expected, nil
}

// workflowProgress: the workflow's nodes, in the order they ran, each with
// its job's state.
func workflowProgress(c *Ctx, job int64) ([]progressStep, int64, int64, error) {
	rows, err := c.DB().Query(c, `SELECT coalesce(j.name, t.name, n.identifier), coalesce(j.status, '') FROM workflow_job_nodes n
		LEFT JOIN unified_jobs j ON j.id = n.job_id LEFT JOIN unified_job_templates t ON t.id = n.unified_job_template_id
		WHERE n.workflow_job_id = $1 ORDER BY j.started NULLS LAST, n.id`, job)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var steps []progressStep
	var done int64
	for rows.Next() {
		var name, status string
		if err := rows.Scan(&name, &status); err != nil {
			return nil, 0, 0, err
		}
		state := "pending"
		switch status {
		case "successful":
			state = "done"
			done++
		case "failed", "error":
			state = "failed"
			done++
		case "canceled":
			state = "canceled"
		case "running", "waiting":
			state = "running"
		}
		steps = append(steps, progressStep{name: name, state: state})
	}
	return steps, done, int64(len(steps)), rows.Err()
}

func checkStepLabels(c *Ctx, v any, data Obj) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("Expected an object of labels by play or node name.")
	}
	if len(m) > 200 {
		return errors.New("200 steps at most.")
	}
	for k, l := range m {
		s, ok := l.(string)
		if !ok || s == "" || len(s) > 256 {
			return fmt.Errorf("The label of %q: text of 256 characters at most.", k)
		}
	}
	return nil
}
