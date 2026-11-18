package resources

// Request webhooks (roadmap M15-10): other front doors, such as ServiceNow,
// request items through the API and hear back through the item's
// notification_templates_requests when a request changes state. A trigger
// records each change (migration 0051); the leader sends them here.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/damhau/awxnext/internal/api"
)

const (
	maxRequestEvents = 100
	// readyWait is how long a successful request's webhook waits for its
	// deployment: the job's status is written before the deployment.
	readyWait = time.Minute
)

type requestChange struct {
	id, request      int64
	status, previous string
	at               time.Time
}

// requestEvents names a change of a request's status for its webhooks:
// leaving awaiting_approval otherwise than denied or canceled is approved,
// and a request that needs no approval says nothing until it runs.
func requestEvents(kind, itemKind, status, previous string) []string {
	var out []string
	if previous == requestAwaiting && status != requestDenied && status != requestCanceled {
		out = append(out, "approved")
	}
	switch status {
	case requestAwaiting:
		out = append(out, "waiting")
	case requestDenied:
		out = append(out, "denied")
	case requestRunning:
		out = append(out, "running")
	case requestFailed:
		out = append(out, "failed")
	case requestCanceled:
		out = append(out, "canceled")
	case requestSuccessful:
		switch {
		case kind == requestNew && itemKind == itemDeployment:
			out = append(out, "ready")
		case kind == requestDelete:
			out = append(out, "deleted")
		default:
			out = append(out, "done")
		}
	}
	return out
}

// requestHookTemplates are the notification templates an item sends its
// requests' changes to.
func requestHookTemplates(c *Ctx, item any) ([]int64, error) {
	return idList(c, `SELECT notification_template_id FROM notification_template_links WHERE content_type = 'catalogitem' AND object_id = $1
		AND event = 'requests' ORDER BY notification_template_id`, item)
}

// sendRequestEvents sends the recorded changes of requests' status to their
// items' request webhooks, oldest first, once each: the changes are taken
// under lock, and their notifications made in the same transaction. A
// request's changes wait behind one that waits for its deployment.
func sendRequestEvents(c *Ctx) {
	var sent []int64
	err := c.WithTx(func(c *Ctx) error {
		rows, err := c.DB().Query(c, `SELECT id, request_id, status, previous, created FROM catalog_request_events
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, maxRequestEvents)
		if err != nil {
			return err
		}
		var changes []requestChange
		for rows.Next() {
			var x requestChange
			if err := rows.Scan(&x.id, &x.request, &x.status, &x.previous, &x.at); err != nil {
				rows.Close()
				return err
			}
			changes = append(changes, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		held := map[int64]bool{}
		var done []int64
		for _, x := range changes {
			if held[x.request] {
				continue
			}
			ids, wait, err := requestChangeNotifications(c, x)
			if err != nil {
				slog.Warn("request webhook", "catalog_request", x.request, "err", err)
			}
			if wait {
				held[x.request] = true
				continue
			}
			sent = append(sent, ids...)
			done = append(done, x.id)
		}
		if len(done) == 0 {
			return nil
		}
		_, err = c.DB().Exec(c, "DELETE FROM catalog_request_events WHERE id = ANY($1)", done)
		return err
	})
	if err != nil {
		if c.Err() == nil {
			slog.Warn("request webhooks", "err", err)
		}
		return
	}
	if len(sent) > 0 {
		app := c.App
		go func() {
			for _, id := range sent {
				deliver(context.Background(), app, id)
			}
		}()
	}
}

// requestChangeNotifications makes the notifications of one change, or
// says to wait: a successful request for a deployment waits for the
// deployment, a minute at most.
func requestChangeNotifications(c *Ctx, x requestChange) ([]int64, bool, error) {
	req, err := api.Get(c, api.Lookup("catalog_requests"), x.request)
	if errors.Is(err, api.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	nts, err := requestHookTemplates(c, req["catalog_item"])
	if err != nil || len(nts) == 0 {
		return nil, false, err
	}
	var itemKind string
	c.DB().QueryRow(c, "SELECT kind FROM catalog_items WHERE id = $1", req["catalog_item"]).Scan(&itemKind)
	events := requestEvents(str(req["kind"]), itemKind, x.status, x.previous)
	if len(events) == 0 {
		return nil, false, nil
	}
	if events[len(events)-1] == "ready" && req["deployment"] == nil && time.Since(x.at) < readyWait {
		return nil, true, nil
	}
	var ids []int64
	for _, ev := range events {
		subject, text, body := requestHookBody(c, ev, req, x.status, x.at)
		ids = append(ids, notifyTemplates(c, nts, subject, text, body)...)
	}
	return ids, false, nil
}

// notifyTemplates makes a notification per template: the text for an email
// template, the body as JSON for a webhook.
func notifyTemplates(c *Ctx, nts []int64, subject, text string, body Obj) []int64 {
	var ids []int64
	for _, nt := range nts {
		var typ string
		if err := c.DB().QueryRow(c, "SELECT notification_type FROM notification_templates WHERE id = $1", nt).Scan(&typ); err != nil {
			continue
		}
		var payload any = body
		if typ == "email" {
			payload = text
		}
		id, err := createNotification(c, c.DB(), nt, nil, subject, payload)
		if err != nil {
			slog.Warn("request webhook", "notification_template", nt, "err", err)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// requestHookBody is what a request's webhook gets: the event, when it
// happened, the request with its status then, and its deployment as it is
// now, secrets masked as the API masks them.
func requestHookBody(c *Ctx, event string, req Obj, status string, at time.Time) (string, string, Obj) {
	id := int64Of(req["id"])
	link := portalRequestURL(c.App, id)
	r := Obj{"id": id, "url": link, "kind": req["kind"], "status": status, "catalog_item": req["catalog_item"], "item_name": req["item_name"],
		"requester": nil, "team": nil, "reason": req["reason"], "explanation": req["explanation"], "lifetime_days": req["lifetime_days"],
		"created": fmtTime(req["created"]), "deployment": req["deployment"]}
	var name string
	if c.DB().QueryRow(c, "SELECT username FROM users WHERE id = $1", req["created_by"]).Scan(&name) == nil {
		r["requester"] = name
	}
	if c.DB().QueryRow(c, "SELECT name FROM teams WHERE id = $1", req["team"]).Scan(&name) == nil {
		r["team"] = name
	}
	body := Obj{"event": event, "time": api.FormatTime(at), "request": r, "deployment": nil}
	if dep, ok := api.ID(req["deployment"]); ok {
		if d, err := api.Get(c, api.Lookup("deployments"), dep); err == nil {
			body["deployment"] = hookDeployment(c, d)
		}
	}
	subject := fmt.Sprintf("Request %d for %s: %s", id, str(req["item_name"]), event)
	text := subject + "\n\n"
	if e := str(req["explanation"]); e != "" {
		text += e + "\n\n"
	}
	return subject, text + link + "\n", body
}

// hookDeployment is a deployment as request webhooks get it.
func hookDeployment(c *Ctx, d Obj) Obj {
	out := reminderDeployment(c, d, deploymentURL(c.App, d["id"]))
	out["expires_at"] = fmtTime(d["expires_at"])
	out["status"] = d["status"]
	out["outputs"] = redactOutputs(d["outputs"])
	out["connection"] = deploymentConnection(c, d, itemLayout(c, d["catalog_item"], map[int64][]connectionEntry{}))
	return out
}

// hookExpiring tells the item's request webhooks that a deployment expires
// in hours, with the request that made it.
func hookExpiring(c *Ctx, d, item Obj, hours int64) {
	request, ok := api.ID(d["request"])
	if !ok {
		return
	}
	nts, err := requestHookTemplates(c, item["id"])
	if err != nil || len(nts) == 0 {
		return
	}
	req, err := api.Get(c, api.Lookup("catalog_requests"), request)
	if err != nil {
		return
	}
	_, _, body := requestHookBody(c, "expiring", req, str(req["status"]), time.Now())
	body["hours_left"] = hours
	subject := fmt.Sprintf("%s expires in %d hours", str(d["name"]), hours)
	app := c.App
	ids := notifyTemplates(c, nts, subject, subject+"\n\n"+deploymentURL(app, d["id"])+"\n", body)
	go func() {
		for _, id := range ids {
			deliver(context.Background(), app, id)
		}
	}()
}
