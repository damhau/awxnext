package resources

import (
	"fmt"
	"net/http"
	"time"

	"github.com/damhau/awxnext/internal/api"
)

// Quotas (awxnext, roadmap M15-07): an item limits the deployments each
// team, or each user requesting for no team, has at once, and the requests
// they make in 24 hours. Over quota, a request is refused or waits for the
// item's owner team.

const (
	gateQuota        = "quota"
	overQuotaRefuse  = "refuse"
	overQuotaApprove = "approve"
	maxQuota         = 10000
	quotaWindow      = 24 * time.Hour
)

// quotaHolder is whom a quota counts for: the request's team, or the
// requester when it is for no team. A deployment counts for its team, or
// for its owner without one.
type quotaHolder struct {
	team, user int64
}

func requestHolder(team any, user int64) quotaHolder {
	if t, ok := api.ID(team); ok {
		return quotaHolder{team: t}
	}
	return quotaHolder{user: user}
}

func (h quotaHolder) args() (any, any) {
	if h.team != 0 {
		return h.team, nil
	}
	return nil, h.user
}

type quotaUsage struct {
	holder      quotaHolder
	name        string
	deployments int64
	requests    int64
}

// quotaUsages counts an item's usage by holder: the deployments not yet
// deleted (a failed teardown keeps its place) with the new requests on
// their way to one, and the new requests of the last 24 hours that weren't
// denied. A failed provisioning makes no deployment, so its place is free
// again. With only, it counts that holder alone.
func quotaUsages(c *Ctx, item Obj, at time.Time, only *quotaHolder) ([]quotaUsage, error) {
	var team, user any
	if only != nil {
		team, user = only.args()
	}
	rows, err := c.DB().Query(c, `SELECT coalesce(x.team_id, 0), coalesce(x.user_id, 0), coalesce(t.name, u.username, ''), sum(x.deployments), sum(x.requests)
		FROM (
			SELECT team_id, CASE WHEN team_id IS NULL THEN owner_id END AS user_id, 1 AS deployments, 0 AS requests
				FROM deployments WHERE catalog_item_id = $1 AND status <> 'deleted'
			UNION ALL
			SELECT team_id, CASE WHEN team_id IS NULL THEN created_by_id END,
				CASE WHEN $3 AND deployment_id IS NULL AND status = ANY($4) THEN 1 ELSE 0 END,
				CASE WHEN status <> 'denied' AND created > $2 THEN 1 ELSE 0 END
				FROM catalog_requests WHERE catalog_item_id = $1 AND kind = 'new' AND (created > $2 OR status = ANY($4))
		) x LEFT JOIN teams t ON t.id = x.team_id LEFT JOIN users u ON u.id = x.user_id
		WHERE ($5::bigint IS NULL AND $6::bigint IS NULL) OR x.team_id = $5 OR ($5 IS NULL AND x.team_id IS NULL AND x.user_id = $6)
		GROUP BY 1, 2, 3 HAVING sum(x.deployments) + sum(x.requests) > 0 ORDER BY 4 DESC, 5 DESC, 3, 1, 2`,
		item["id"], at.Add(-quotaWindow), item["kind"] == itemDeployment, unfinishedRequests, team, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quotaUsage
	for rows.Next() {
		var u quotaUsage
		if err := rows.Scan(&u.holder.team, &u.holder.user, &u.name, &u.deployments, &u.requests); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func holderUsage(c *Ctx, item Obj, h quotaHolder, at time.Time) (quotaUsage, error) {
	us, err := quotaUsages(c, item, at, &h)
	if err != nil || len(us) > 0 {
		return firstUsage(us, h), err
	}
	u := quotaUsage{holder: h}
	if h.team != 0 {
		err = c.DB().QueryRow(c, "SELECT name FROM teams WHERE id = $1", h.team).Scan(&u.name)
	} else {
		err = c.DB().QueryRow(c, "SELECT username FROM users WHERE id = $1", h.user).Scan(&u.name)
	}
	return u, err
}

func firstUsage(us []quotaUsage, h quotaHolder) quotaUsage {
	if len(us) == 0 {
		return quotaUsage{holder: h}
	}
	return us[0]
}

func itemQuotas(item Obj) (deployments, requests int64, limited bool) {
	if v, ok := api.ID(item["max_deployments"]); ok && item["kind"] == itemDeployment {
		deployments, limited = v, true
	}
	if v, ok := api.ID(item["max_requests_per_day"]); ok {
		requests, limited = v, true
	}
	return deployments, requests, limited
}

func (u quotaUsage) obj(item Obj) Obj {
	maxDeployments, maxRequests, _ := itemQuotas(item)
	out := Obj{"team": nil, "user": nil, "deployments": u.deployments, "requests": u.requests,
		"max_deployments": quotaLimit(maxDeployments), "max_requests_per_day": quotaLimit(maxRequests)}
	if u.holder.team != 0 {
		out["team"] = Obj{"id": u.holder.team, "name": u.name}
	} else {
		out["user"] = Obj{"id": u.holder.user, "username": u.name}
	}
	return out
}

func quotaLimit(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// quotaGate checks a new request against the item's quotas, in the
// request's transaction: within them, nothing; over one, a refusal, or a
// gate for the item's owner team when the item says so.
func quotaGate(c *Ctx, ci *catalogItem, team any, requester int64, at time.Time) (*gate, error) {
	maxDeployments, maxRequests, limited := itemQuotas(ci.item)
	if !limited {
		return nil, nil
	}
	// Two requests counted at once would both fit.
	if _, err := c.DB().Exec(c, "SELECT 1 FROM catalog_items WHERE id = $1 FOR NO KEY UPDATE", ci.item["id"]); err != nil {
		return nil, err
	}
	h := requestHolder(team, requester)
	u, err := holderUsage(c, ci.item, h, at)
	if err != nil {
		return nil, err
	}
	var over string
	switch {
	case maxDeployments > 0 && u.deployments >= maxDeployments:
		over = fmt.Sprintf("%s has %d of its %d deployments of %s.", u.name, u.deployments, maxDeployments, str(ci.item["name"]))
	case maxRequests > 0 && u.requests >= maxRequests:
		over = fmt.Sprintf("%s made %d of its %d requests of %s in 24 hours.", u.name, u.requests, maxRequests, str(ci.item["name"]))
		next, err := nextRequestAt(c, ci.item, h, at, u.requests-maxRequests)
		if err != nil {
			return nil, err
		}
		if next != nil {
			over += fmt.Sprintf(" The next one fits from %s.", next.UTC().Format("Monday 2 January at 15:04 UTC"))
		}
	default:
		return nil, nil
	}
	if ci.item["over_quota"] != overQuotaApprove {
		return nil, &api.Error{Status: 400, Body: Obj{"detail": "Over quota: " + over, "quota": u.obj(ci.item)}}
	}
	var group []int64
	if t, ok := api.ID(ci.item["owner_team"]); ok {
		if group, err = permHolders(c, "team", t, "member_team"); err != nil {
			return nil, err
		}
	}
	g := &gate{kind: gateQuota, rule: "Over quota", timeout: defaultApprovalTimeout, why: "Over quota: " + over}
	org, _ := api.ID(ci.item["organization"])
	if err := g.resolve(c, group, requester, org, 1); err != nil {
		return nil, err
	}
	return g, nil
}

// nextRequestAt is when the request that makes room leaves the 24 hours:
// the skip-th oldest of those counted.
func nextRequestAt(c *Ctx, item Obj, h quotaHolder, at time.Time, skip int64) (*time.Time, error) {
	team, user := h.args()
	var t time.Time
	err := c.DB().QueryRow(c, `SELECT created FROM catalog_requests WHERE catalog_item_id = $1 AND kind = 'new' AND status <> 'denied' AND created > $2
		AND (team_id = $3 OR ($3::bigint IS NULL AND team_id IS NULL AND created_by_id = $4)) ORDER BY created OFFSET $5 LIMIT 1`,
		item["id"], at.Add(-quotaWindow), team, user, skip).Scan(&t)
	if err != nil {
		return nil, nil
	}
	t = t.Add(quotaWindow)
	return &t, nil
}

// handOverRoom refuses a hand-over to a holder with no room left in the
// item's deployment quota. The item's admins hand over past it.
func handOverRoom(c *Ctx, d, item Obj, owner, team any, admin bool) error {
	maxDeployments, _, _ := itemQuotas(item)
	if maxDeployments == 0 || admin {
		return nil
	}
	ownerID, _ := api.ID(owner)
	h := requestHolder(team, ownerID)
	if h == requestHolder(d["team"], int64Of(d["owner"])) {
		return nil
	}
	if _, err := c.DB().Exec(c, "SELECT 1 FROM catalog_items WHERE id = $1 FOR NO KEY UPDATE", item["id"]); err != nil {
		return err
	}
	u, err := holderUsage(c, item, h, time.Now())
	if err != nil {
		return err
	}
	if u.deployments >= maxDeployments {
		return &api.Error{Status: 400, Body: Obj{"detail": fmt.Sprintf("Over quota: %s has %d of its %d deployments of %s, so it can't take this one.",
			u.name, u.deployments, maxDeployments, str(item["name"])), "quota": u.obj(item)}}
	}
	return nil
}

// formQuota is the usage the form shows for each team the request may be
// for, or for the requester without a team.
func formQuota(c *Ctx, item Obj, teams []Obj) (any, error) {
	maxDeployments, maxRequests, limited := itemQuotas(item)
	if !limited {
		return nil, nil
	}
	holders := []quotaHolder{{user: c.User.UserID}}
	if len(teams) > 0 {
		holders = holders[:0]
		for _, t := range teams {
			id, _ := api.ID(t["id"])
			holders = append(holders, quotaHolder{team: id})
		}
	}
	usage := []Obj{}
	now := time.Now()
	for _, h := range holders {
		u, err := holderUsage(c, item, h, now)
		if err != nil {
			return nil, err
		}
		usage = append(usage, u.obj(item))
	}
	return Obj{"max_deployments": quotaLimit(maxDeployments), "max_requests_per_day": quotaLimit(maxRequests), "over_quota": item["over_quota"],
		"usage": usage}, nil
}

// handleQuotaUsage lists an item's usage against its quotas, for its admins.
func handleQuotaUsage(c *Ctx, w http.ResponseWriter, r *http.Request, item Obj) {
	maxDeployments, maxRequests, _ := itemQuotas(item)
	us, err := quotaUsages(c, item, time.Now(), nil)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	results := []Obj{}
	for _, u := range us {
		results = append(results, u.obj(item))
	}
	api.WriteJSON(w, 200, Obj{"max_deployments": quotaLimit(maxDeployments), "max_requests_per_day": quotaLimit(maxRequests),
		"over_quota": item["over_quota"], "count": len(results), "results": results})
}

func checkQuotaFields(pick func(string) any, data Obj) error {
	if pick("kind") == itemDeployment {
		return nil
	}
	if v, ok := data["max_deployments"]; ok && v != nil {
		return api.FieldError("max_deployments", "Only an item that creates a deployment has this.")
	}
	return nil
}
