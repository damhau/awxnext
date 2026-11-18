package resources

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

const maxPortalApprovals = 100

// waitingForUser selects the pending request approvals, as g, that the user
// approves and hasn't answered yet. $1 is the user.
const waitingForUser = `FROM catalog_request_approvals g JOIN unified_jobs a ON a.id = g.approval_id
	WHERE a.status = 'pending' AND $1 = ANY(g.approvers)
	AND NOT EXISTS (SELECT 1 FROM catalog_approval_answers x WHERE x.approval_id = g.approval_id AND x.user_id = $1)`

func portalApprovalURL(app *api.App, id int64) string {
	return fmt.Sprintf("%s/portal/approvals/%d", strings.TrimRight(app.Cfg.BaseURL, "/"), id)
}

func portalRequestURL(app *api.App, id int64) string {
	return fmt.Sprintf("%s/portal/requests/%d", strings.TrimRight(app.Cfg.BaseURL, "/"), id)
}

func portalApprovals(c *Ctx, w http.ResponseWriter, r *http.Request) {
	out := []Obj{}
	if c.User.Anonymous() {
		api.WriteJSON(w, 200, Obj{"count": 0, "results": out})
		return
	}
	ids, err := idList(c, "SELECT g.approval_id "+waitingForUser+" ORDER BY a.created, a.id LIMIT $2", c.User.UserID, maxPortalApprovals)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	for _, id := range ids {
		o, err := portalApproval(c, id)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out = append(out, o)
	}
	api.WriteJSON(w, 200, Obj{"count": len(out), "results": out})
}

func portalApprovalDetail(c *Ctx, w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		api.WriteError(w, api.ErrNotFound)
		return
	}
	if ok, err := api.CanView(c, api.Lookup("workflow_approvals"), id); err != nil || !ok {
		api.WriteError(w, cmpErr(err, api.ErrNotFound))
		return
	}
	o, err := portalApproval(c, id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = api.ErrNotFound
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, o)
}

func cmpErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// portalApproval is a request's approval as its approvers decide it: the
// gate, whether the user can answer, and the request with its answers, its
// deployment and its quota.
func portalApproval(c *Ctx, id int64) (Obj, error) {
	var reqID int64
	var status string
	var timedOut bool
	var timeout int64
	var created time.Time
	if err := c.DB().QueryRow(c, `SELECT g.request_id, a.status, a.timed_out, a.approval_timeout, a.created FROM catalog_request_approvals g
		JOIN unified_jobs a ON a.id = g.approval_id WHERE g.approval_id = $1`, id).Scan(&reqID, &status, &timedOut, &timeout, &created); err != nil {
		return nil, err
	}
	out := Obj{"id": id, "status": status, "timed_out": timedOut, "created": api.FormatTime(created), "expires": nil}
	if timeout > 0 {
		out["expires"] = api.FormatTime(created.Add(time.Duration(timeout) * time.Second))
	}
	maps.Copy(out, requestApprovalInfo(c, id))
	g, err := loadRequestGate(c, id)
	if err != nil {
		return nil, err
	}
	out["can_answer"], out["cannot_answer"] = false, ""
	switch err := g.canAnswer(c); {
	case status != "pending":
		out["cannot_answer"] = "It was already decided."
	case err == nil:
		out["can_answer"] = true
	default:
		var ae *api.Error
		if !errors.As(err, &ae) {
			return nil, err
		}
		out["cannot_answer"] = errorText(err)
	}
	out["own"] = g.requester == c.User.UserID
	sys := *c
	sys.User = rbac.System()
	res := api.Lookup("catalog_requests")
	req, err := api.Get(&sys, res, reqID)
	if err != nil {
		return nil, err
	}
	if out["request"], out["quota"], err = approverView(c, req); err != nil {
		return nil, err
	}
	return out, nil
}

// approverView is what an approver sees of a request: who asked, for
// which team and why, the answers by question, and for a request on a
// deployment, the deployment. The quota is the request's team's, or its
// requester's, and counts the request itself.
func approverView(c *Ctx, req Obj) (Obj, any, error) {
	sys := *c
	sys.User = rbac.System()
	kind := str(req["kind"])
	out := Obj{"id": req["id"], "kind": kind, "status": req["status"], "created": fmtTime(req["created"]), "reason": req["reason"],
		"lifetime_days": req["lifetime_days"], "item": nil, "team": nil, "requester": nil, "deployment": nil, "action": nil, "answers": []Obj{}}
	if t, ok := api.ID(req["team"]); ok {
		teams, err := teamRefs(c, []int64{t})
		if err != nil {
			return nil, nil, err
		}
		if len(teams) == 1 {
			out["team"] = teams[0]
		}
	}
	if uid, ok := api.ID(req["created_by"]); ok {
		if who, err := people(c, []int64{uid}); err == nil && len(who) == 1 {
			out["requester"] = who[0]
		}
	}
	var item Obj
	if id, ok := api.ID(req["catalog_item"]); ok {
		var err error
		if item, err = api.Get(&sys, api.Lookup("catalog_items"), id); err != nil && !errors.Is(err, api.ErrNotFound) {
			return nil, nil, err
		}
	}
	out["item"] = Obj{"id": req["catalog_item"], "name": req["item_name"], "kind": nil, "icon": ""}
	names := map[string]string{}
	var order []string
	if item != nil {
		out["item"] = Obj{"id": item["id"], "name": item["name"], "kind": item["kind"], "icon": item["icon"]}
		var spec map[string]any
		switch kind {
		case requestNew:
			if ci, err := loadCatalogItem(c, item); err == nil {
				spec = ci.view.spec
			}
		case requestExtend, requestDelete:
		default:
			if act, ok := findAction(item, kind); ok {
				out["action"] = Obj{"id": act.ID, "name": act.Name}
				spec = enabledSurvey(c, act.Template)
			}
		}
		for _, q := range surveyQuestions(spec) {
			v := str(q["variable"])
			names[v] = str(q["question_name"])
			order = append(order, v)
		}
	}
	answers, _ := req["answers"].(map[string]any)
	labels, _ := req["answer_labels"].(map[string]any)
	list := []Obj{}
	add := func(v string) {
		val, ok := answers[v]
		if !ok {
			return
		}
		if l, ok := labels[v]; ok {
			val = l
		}
		name := names[v]
		if name == "" {
			name = v
		}
		list = append(list, Obj{"variable": v, "question": name, "value": val})
		delete(answers, v)
	}
	answers = maps.Clone(answers)
	for _, v := range order {
		add(v)
	}
	for _, v := range slices.Sorted(maps.Keys(answers)) {
		add(v)
	}
	out["answers"] = list
	if id, ok := api.ID(req["deployment"]); ok {
		d, err := api.Get(&sys, api.Lookup("deployments"), id)
		if err != nil && !errors.Is(err, api.ErrNotFound) {
			return nil, nil, err
		}
		if d != nil {
			dep := Obj{"id": d["id"], "name": d["name"], "status": d["status"], "created": fmtTime(d["created"]), "expires_at": fmtTime(d["expires_at"]),
				"extensions": d["extensions"], "max_extensions": nil, "until": nil}
			if item != nil {
				dep["max_extensions"] = item["max_extensions"]
				lt, err := loadLifetime(c, int64Of(d["id"]))
				if err != nil {
					return nil, nil, err
				}
				if until := lt.until(item); until != nil {
					dep["until"] = api.FormatTime(*until)
				}
			}
			out["deployment"] = dep
		}
	}
	if kind != requestNew || item == nil {
		return out, nil, nil
	}
	if _, _, limited := itemQuotas(item); !limited {
		return out, nil, nil
	}
	h := quotaHolder{}
	if t, ok := api.ID(req["team"]); ok {
		h.team = t
	} else if u, ok := api.ID(req["created_by"]); ok {
		h.user = u
	}
	u, err := holderUsage(c, item, h, time.Now())
	if err != nil {
		return nil, nil, err
	}
	return out, u.obj(item), nil
}
