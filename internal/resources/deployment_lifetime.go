package resources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

const (
	requestExtend = "extend"

	maxExtensionSteps = 10
	day               = 24 * time.Hour
)

// reminderHours are how long before expiry the owners are reminded,
// earliest first.
var reminderHours = []int64{24, 2}

var extendKeys = map[string]bool{"days": true, "reason": true}

// checkLifetimeFields checks an item's extension steps, and that only an
// item that creates a deployment sets its lifetime fields.
func checkLifetimeFields(pick func(string) any, data Obj) error {
	steps, err := checkExtensionDays(pick("extension_days"))
	if err != nil {
		return err
	}
	if pick("kind") != itemDeployment {
		waits, _ := pick("expiry_waits_for_freeze").(bool)
		for field, set := range map[string]bool{"extension_days": len(steps) > 0, "max_extensions": pick("max_extensions") != nil, "expiry_waits_for_freeze": waits} {
			if set {
				return api.FieldError(field, "Only an item that creates a deployment has this.")
			}
		}
	}
	data["extension_days"] = steps
	return nil
}

func checkExtensionDays(v any) ([]any, error) {
	if v == nil {
		return []any{}, nil
	}
	bad := func(msg string) error { return api.FieldError("extension_days", msg) }
	list, ok := v.([]any)
	if !ok {
		return nil, bad("Expected a list of numbers of days, such as [1, 3, 7].")
	}
	if len(list) > maxExtensionSteps {
		return nil, bad(fmt.Sprintf("At most %d extensions.", maxExtensionSteps))
	}
	out := []any{}
	for _, x := range list {
		_, isStr := x.(string)
		n, ok := api.ID(x)
		if isStr || !ok || n < 1 || n > maxLifetime {
			return nil, bad(fmt.Sprintf("Each extension is a number of days, from 1 to %d.", maxLifetime))
		}
		if slices.Contains(out, any(n)) {
			return nil, bad(fmt.Sprintf("%d days is listed twice.", n))
		}
		out = append(out, n)
	}
	return out, nil
}

func extensionSteps(item Obj) []int64 {
	list, _ := item["extension_days"].([]any)
	var out []int64
	for _, x := range list {
		if n, ok := api.ID(x); ok {
			out = append(out, n)
		}
	}
	return out
}

// lifetime is what a deployment used of its item's lifetime: its expiry,
// its extensions, and when its lifetime started, its request's creation, as
// the request's expires_at counts it.
type lifetime struct {
	expires    *time.Time
	extensions int64
	start      time.Time
}

func loadLifetime(c *Ctx, id int64) (lifetime, error) {
	var lt lifetime
	err := c.DB().QueryRow(c, `SELECT d.expires_at, d.extensions, coalesce(q.created, d.created) FROM deployments d
		LEFT JOIN catalog_requests q ON q.id = d.request_id WHERE d.id = $1`, id).Scan(&lt.expires, &lt.extensions, &lt.start)
	return lt, err
}

// until is the latest the item lets the deployment be kept, or nil.
func (lt lifetime) until(item Obj) *time.Time {
	days, ok := api.ID(item["max_lifetime_days"])
	if !ok {
		return nil
	}
	t := lt.start.Add(time.Duration(days) * day)
	return &t
}

// extensionRefusal says why the deployment can't be extended by days.
func extensionRefusal(d Obj, lt lifetime, item Obj, days int64, now time.Time) string {
	if s := str(d["status"]); s != deploymentActive {
		return fmt.Sprintf("The deployment is %s.", strings.ReplaceAll(s, "_", " "))
	}
	if lt.expires == nil {
		return "It doesn't expire."
	}
	if !lt.expires.After(now) {
		return "It has expired."
	}
	steps := extensionSteps(item)
	if len(steps) == 0 {
		return "The item has no extensions."
	}
	if !slices.Contains(steps, days) {
		return fmt.Sprintf("Not one of the item's extensions: %s.", daysList(steps))
	}
	if max, ok := api.ID(item["max_extensions"]); ok && lt.extensions >= max {
		return fmt.Sprintf("It was extended %d of %d times: no extension is left.", lt.extensions, max)
	}
	if until := lt.until(item); until != nil && lt.expires.Add(time.Duration(days)*day).After(*until) {
		return fmt.Sprintf("That keeps it past %s: the item keeps a deployment %v days at most.", api.FormatTime(*until), item["max_lifetime_days"])
	}
	return ""
}

func daysList(steps []int64) string {
	parts := make([]string, len(steps))
	for i, n := range steps {
		parts[i] = fmt.Sprint(n)
	}
	s := strings.Join(parts, ", ")
	if i := strings.LastIndex(s, ", "); i >= 0 {
		s = s[:i] + " or " + s[i+2:]
	}
	if len(steps) == 1 && steps[0] == 1 {
		return s + " day"
	}
	return s + " days"
}

func moreDays(days int64) string {
	if days == 1 {
		return "1 more day"
	}
	return fmt.Sprintf("%d more days", days)
}

// handleExtend serves deployments/N/extend/: GET says which extensions are
// left and who approves each; POST, by an owner, asks for one.
func handleExtend(c *Ctx, w http.ResponseWriter, r *http.Request, d Obj) {
	admin, err := deploymentAdmin(c, d)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	owns := ownsDeployment(c, d)
	if !owns && (!admin || r.Method != "GET") {
		api.WriteError(w, api.Detail(403, "Only the deployment's owners extend it."))
		return
	}
	item, err := systemItem(c, d)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if r.Method == "GET" {
		out, err := extensionOptions(c, d, item)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		api.WriteJSON(w, 200, out)
		return
	}
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	req, err := RequestExtension(c, d, item, normalizeObj(body))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out, err := api.SerializeOne(c, api.Lookup("catalog_requests"), req)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 201, out)
}

// extensionOptions lists the item's extensions with the expiry each gives,
// why it is refused, or the approvals it would wait for.
func extensionOptions(c *Ctx, d, item Obj) (Obj, error) {
	id, _ := api.ID(d["id"])
	lt, err := loadLifetime(c, id)
	if err != nil {
		return nil, err
	}
	busy := ""
	if str(d["status"]) == deploymentActive {
		if err := deploymentReady(c, d, deploymentActive); err != nil {
			var ae *api.Error
			if !errors.As(err, &ae) {
				return nil, err
			}
			busy = errorText(err)
		}
	}
	now := time.Now()
	steps := []Obj{}
	for _, days := range extensionSteps(item) {
		step := Obj{"days": days, "expires_at": nil, "refused": "", "approvals": []Obj{}}
		if lt.expires != nil {
			step["expires_at"] = api.FormatTime(lt.expires.Add(time.Duration(days) * day))
		}
		why := extensionRefusal(d, lt, item, days, now)
		if why == "" {
			why = busy
		}
		if why != "" {
			step["refused"] = why
			steps = append(steps, step)
			continue
		}
		gates, err := requestGates(c, &catalogItem{item: item}, extensionEnv(c, d, 0, days, lt.expires.Add(time.Duration(days)*day)), c.User.UserID, now)
		if err != nil {
			step["refused"] = errorText(err)
		}
		for _, g := range gates {
			o, err := gatePreview(c, g)
			if err != nil {
				return nil, err
			}
			step["approvals"] = append(step["approvals"].([]Obj), o)
		}
		steps = append(steps, step)
	}
	out := Obj{"expires_at": nil, "extensions": lt.extensions, "max_extensions": item["max_extensions"], "until": nil, "steps": steps}
	if lt.expires != nil {
		out["expires_at"] = api.FormatTime(*lt.expires)
	}
	if until := lt.until(item); until != nil {
		out["until"] = api.FormatTime(*until)
	}
	return out, nil
}

// extensionEnv is what the item's rules read of an extension: its kind,
// and as the request, the days it adds and the expiry it gives.
func extensionEnv(c *Ctx, d Obj, id, days int64, expires time.Time) *ruleEnv {
	return &ruleEnv{answers: map[string]any{}, picked: map[string]any{}, team: teamEnv(c, d["team"]), user: userEnv(c, c.User.UserID),
		request: map[string]any{"id": id, "lifetime_days": days, "expires_at": api.FormatTime(expires)}, kind: requestExtend}
}

// RequestExtension asks for one of the item's extensions of a deployment
// that c's user owns: it applies at once unless the item's rules hold it.
func RequestExtension(c *Ctx, d, item Obj, body Obj) (Obj, error) {
	if !ownsDeployment(c, d) {
		return nil, api.Detail(403, "Only the deployment's owners extend it.")
	}
	fe := api.FieldErrors{}
	for _, k := range slices.Sorted(maps.Keys(body)) {
		if !extendKeys[k] {
			fe.Add(k, "Not a field of an extension: days, reason.")
		}
	}
	_, isStr := body["days"].(string)
	days, ok := api.ID(body["days"])
	if isStr || !ok {
		fe.Add("days", "A number of days, one of the item's extensions.")
	}
	reason, isStr := body["reason"].(string)
	if body["reason"] != nil && !isStr {
		fe.Add("reason", "Not a valid string.")
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	if err := deploymentReady(c, d, deploymentActive); err != nil {
		return nil, err
	}
	dep, _ := api.ID(d["id"])
	lt, err := loadLifetime(c, dep)
	if err != nil {
		return nil, err
	}
	if why := extensionRefusal(d, lt, item, days, time.Now()); why != "" {
		return nil, api.BadRequest(why)
	}
	id, created, err := nextRequest(c)
	if err != nil {
		return nil, err
	}
	p := &pendingRequest{
		ci: &catalogItem{item: item}, kind: requestExtend, deployment: d["id"], statuses: []string{deploymentActive},
		team: d["team"], reason: strings.TrimSpace(reason), lifetime: days, shown: map[string]any{}, labels: map[string]any{},
		launch: requestLaunch{Vars: map[string]any{}, ActivityVars: map[string]bool{}},
		env:    extensionEnv(c, d, id, days, lt.expires.Add(time.Duration(days)*day)),
		what:   moreDays(days) + " for " + str(d["name"]),
	}
	req, approvals, err := p.submit(c, id, created)
	if err != nil {
		return nil, err
	}
	announceApprovals(c, p.ci, id, approvals)
	return req, nil
}

// nextRequest reserves a catalog request's id, so that its rules can read
// it before it is recorded.
func nextRequest(c *Ctx) (int64, time.Time, error) {
	var id int64
	err := c.DB().QueryRow(c, "SELECT nextval(pg_get_serial_sequence('catalog_requests', 'id'))").Scan(&id)
	return id, time.Now().UTC().Truncate(time.Microsecond), err
}

// extendDeployment applies an extension request, within the item's caps
// as they are now.
func extendDeployment(c *Ctx, request, dep int64, item Obj) error {
	var days int64
	if err := c.DB().QueryRow(c, "SELECT coalesce(lifetime_days, 0) FROM catalog_requests WHERE id = $1", request).Scan(&days); err != nil {
		return err
	}
	d, err := lockDeployment(c, dep)
	if err != nil {
		return err
	}
	lt, err := loadLifetime(c, dep)
	if err != nil {
		return err
	}
	if why := extensionRefusal(d, lt, item, days, time.Now()); why != "" {
		return api.BadRequest(why)
	}
	expires := lt.expires.Add(time.Duration(days) * day)
	if _, err := c.DB().Exec(c, "UPDATE deployments SET expires_at = $2, extensions = extensions + 1, reminded_hours = NULL, modified = now() WHERE id = $1",
		dep, expires); err != nil {
		return err
	}
	if _, err := c.DB().Exec(c, "UPDATE catalog_requests SET status = $2, modified = now() WHERE id = $1", request, requestSuccessful); err != nil {
		return err
	}
	return api.Activity(c, "update", api.Lookup("deployments"), d,
		Obj{"expires_at": []any{api.FormatTime(*lt.expires), api.FormatTime(expires)}, "extensions": []any{lt.extensions, lt.extensions + 1}}, nil, nil, "")
}

// checkExpiryChange lets only the item's admins set when an active
// deployment expires, past the item's extensions and caps.
func checkExpiryChange(c *Ctx, old, data Obj) error {
	v, given := data["expires_at"]
	if !given || old == nil || sameTime(old["expires_at"], v) {
		return nil
	}
	if ok, err := deploymentAdmin(c, old); err != nil || !ok {
		if err != nil {
			return err
		}
		return api.FieldError("expires_at", "Only the item's admins set when it expires: extend it instead.")
	}
	if old["status"] != deploymentActive {
		return api.FieldError("expires_at", "Only an active deployment expires.")
	}
	if t, ok := v.(time.Time); ok && !t.After(time.Now()) {
		return api.FieldError("expires_at", "A time to come: to delete it now, delete it.")
	}
	return nil
}

func sameTime(a, b any) bool {
	ta, tb := timeValue(a), timeValue(b)
	if ta == nil || tb == nil {
		return ta == nil && tb == nil
	}
	return ta.Equal(*tb)
}

func timeValue(v any) *time.Time {
	switch x := v.(type) {
	case time.Time:
		return &x
	case *time.Time:
		return x
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return &t
		}
	}
	return nil
}

// DeploymentTick reminds the owners of the deployments about to expire, and
// deletes those that have.
func DeploymentTick(ctx context.Context, app *api.App) {
	c := &Ctx{Context: ctx, App: app, User: rbac.System(), Extra: map[string]any{}}
	defer sendRequestEvents(c)
	remindDeployments(c)
	ids, err := idList(c, "SELECT id FROM deployments WHERE status = 'active' AND expires_at <= now() ORDER BY expires_at, id LIMIT 100")
	if err != nil {
		return
	}
	for _, id := range ids {
		if err := expireDeployment(c, id); err != nil && ctx.Err() == nil {
			slog.Warn("expiring a deployment", "deployment", id, "err", err)
		}
	}
}

// expireDeployment runs the teardown of an expired deployment, as the
// system: its rules don't apply, and a freeze only when the item says so.
// A request on it that waits for approval is canceled; one that runs is
// waited for.
func expireDeployment(c *Ctx, id int64) error {
	var cancel []int64
	err := c.WithTx(func(c *Ctx) error {
		d, err := lockDeployment(c, id)
		if errors.Is(err, api.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		lt, err := loadLifetime(c, id)
		if err != nil {
			return err
		}
		now := time.Now()
		if d["status"] != deploymentActive || lt.expires == nil || lt.expires.After(now) {
			return nil
		}
		if d["catalog_item"] == nil {
			return setDeploymentStatus(c, id, deploymentDeleteFailed,
				"It expired, but its catalog item was deleted, so no teardown runs: delete it once what it provisioned is gone.", nil)
		}
		item, err := systemItem(c, d)
		if err != nil {
			return err
		}
		if waits, _ := item["expiry_waits_for_freeze"].(bool); waits {
			org, _ := api.ID(item["organization"])
			if f, err := itemFreeze(c, item["id"], org, now); err != nil || f != nil {
				return err
			}
		}
		var open int64
		var status string
		err = c.DB().QueryRow(c, "SELECT id, status FROM catalog_requests WHERE deployment_id = $1 AND status = ANY($2) ORDER BY id LIMIT 1",
			id, unfinishedRequests).Scan(&open, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case status != requestAwaiting:
			return nil
		default:
			if cancel, err = idList(c, `SELECT g.approval_id FROM catalog_request_approvals g JOIN unified_jobs a ON a.id = g.approval_id
				WHERE g.request_id = $1 AND a.status = 'pending'`, open); err != nil {
				return err
			}
			if _, err := c.DB().Exec(c, "UPDATE catalog_requests SET status = $2, explanation = $3, launch = NULL, modified = now() WHERE id = $1",
				open, requestCanceled, "The deployment expired before it was approved."); err != nil {
				return err
			}
		}
		req, created, err := nextRequest(c)
		if err != nil {
			return err
		}
		p := deletionRequest(c, d, item, req, created)
		p.ungated, p.reason = true, "It expired."
		_, _, err = p.submit(c, req, created)
		return err
	})
	if err == nil && Jobs != nil {
		for _, a := range cancel {
			Jobs.Cancel(c, a)
		}
	}
	return err
}

// remindDeployments sends each reminder once per expiry: the 24-hour one,
// then the 2-hour one. A deployment found already inside 2 hours gets only
// the latter.
func remindDeployments(c *Ctx) {
	rows, err := c.DB().Query(c, `SELECT id, expires_at, due FROM (
			SELECT id, expires_at, reminded_hours,
				(SELECT min(h) FROM unnest($1::int[]) h WHERE d.expires_at <= now() + make_interval(hours => h)) AS due
			FROM deployments d WHERE status = 'active' AND expires_at > now() AND expires_at <= now() + make_interval(hours => $2::int)) x
		WHERE due IS NOT NULL AND (reminded_hours IS NULL OR reminded_hours > due) ORDER BY expires_at LIMIT 50`,
		reminderHours, reminderHours[0])
	if err != nil {
		return
	}
	type due struct {
		id, hours int64
		expires   time.Time
	}
	var list []due
	for rows.Next() {
		var x due
		if rows.Scan(&x.id, &x.expires, &x.hours) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	for _, x := range list {
		tag, err := c.DB().Exec(c, `UPDATE deployments SET reminded_hours = $2 WHERE id = $1 AND status = 'active' AND expires_at = $3
			AND (reminded_hours IS NULL OR reminded_hours > $2)`, x.id, x.hours, x.expires)
		if err != nil || tag.RowsAffected() == 0 {
			continue
		}
		if err := remind(c, x.id, x.hours, x.expires); err != nil && c.Err() == nil {
			slog.Warn("deployment reminder", "deployment", x.id, "err", err)
		}
	}
}

// remind tells a deployment's owners, at their own addresses through the
// item's email template, and the item's reminder templates, that it
// expires in hours.
func remind(c *Ctx, id, hours int64, expires time.Time) error {
	d, err := api.Get(c, api.Lookup("deployments"), id)
	if err != nil {
		return err
	}
	if d["catalog_item"] == nil {
		return nil
	}
	item, err := systemItem(c, d)
	if err != nil {
		return err
	}
	lt, err := loadLifetime(c, id)
	if err != nil {
		return err
	}
	link := deploymentURL(c.App, id)
	subject := fmt.Sprintf("%s expires in %d hours", str(d["name"]), hours)
	text := fmt.Sprintf("%s expires on %s, then it is deleted.\n\n", str(d["name"]), expires.UTC().Format("Monday 2 January 2006 at 15:04 UTC"))
	extendable := false
	for _, days := range extensionSteps(item) {
		if extensionRefusal(d, lt, item, days, time.Now()) == "" {
			extendable = true
		}
	}
	if extendable {
		text += "To keep it longer, extend it: " + link + "\n"
	} else {
		who := "the item's owners"
		if team, ok := api.ID(item["owner_team"]); ok {
			var name string
			if c.DB().QueryRow(c, "SELECT name FROM teams WHERE id = $1", team).Scan(&name) == nil {
				who = name
			}
		}
		text += fmt.Sprintf("No extension is left: %s can keep it longer.\n\n%s\n", who, link)
	}
	mailUsers(c, item["notification_template"], nil, deploymentOwners(c, d), subject, text)
	hookExpiring(c, d, item, hours)
	nts, err := idList(c, `SELECT notification_template_id FROM notification_template_links WHERE content_type = 'catalogitem' AND object_id = $1
		AND event = 'reminders' ORDER BY notification_template_id`, item["id"])
	if err != nil || len(nts) == 0 {
		return err
	}
	payload := Obj{"subject": subject, "body": text, "hours_left": hours, "deployment": reminderDeployment(c, d, link)}
	for _, nt := range nts {
		var typ string
		if err := c.DB().QueryRow(c, "SELECT notification_type FROM notification_templates WHERE id = $1", nt).Scan(&typ); err != nil {
			continue
		}
		var body any = payload
		switch typ {
		case "email":
			body = text
		case "teams":
			dep := payload["deployment"].(Obj)
			body = teamsCard{color: "Warning", facts: [][2]string{{"Expires", expires.UTC().Format("2006-01-02 15:04:05 UTC")}, {"Owner", str(dep["owner"])}, {"Team", str(dep["team"])}},
				link: link, action: "Open the deployment"}
		}
		nid, err := createNotification(c, c.App.DB, nt, nil, subject, body)
		if err != nil {
			slog.Warn("deployment reminder", "notification_template", nt, "err", err)
			continue
		}
		go deliver(context.Background(), c.App, nid)
	}
	return nil
}

// reminderDeployment is the deployment as a reminder's webhook gets it.
func reminderDeployment(c *Ctx, d Obj, link string) Obj {
	out := Obj{"id": d["id"], "name": d["name"], "url": link, "expires_at": d["expires_at"], "catalog_item": d["catalog_item"],
		"item_name": d["item_name"], "owner": nil, "team": nil}
	var name string
	if c.DB().QueryRow(c, "SELECT username FROM users WHERE id = $1", d["owner"]).Scan(&name) == nil {
		out["owner"] = name
	}
	if c.DB().QueryRow(c, "SELECT name FROM teams WHERE id = $1", d["team"]).Scan(&name) == nil {
		out["team"] = name
	}
	return out
}
