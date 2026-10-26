package resources

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/tmpl"
)

// Notification types: v1 delivers webhooks only.
var notificationTypeChoices = labelled("webhook", "Webhook")

// webhookFields describes the webhook notification_configuration.
var webhookFields = map[string]string{
	"url": "string", "http_method": "string", "headers": "object", "username": "string", "password": "password",
	"disable_ssl_verification": "boolean",
}

var messageEvents = []string{"started", "success", "error"}

const defaultMessage = "{{ job_friendly_name }} #{{ job.id }} '{{ job.name }}' {{ job.status }}: {{ url }}"

// validateNotificationConfig checks a webhook configuration and encrypts its password.
func validateNotificationConfig(c *Ctx, cfg map[string]any, old map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range cfg {
		typ, ok := webhookFields[k]
		if !ok {
			return nil, api.FieldError("notification_configuration", fmt.Sprintf("Unexpected field %q for webhook notifications.", k))
		}
		switch typ {
		case "string", "password":
			s, ok := v.(string)
			if !ok {
				return nil, api.FieldError("notification_configuration", fmt.Sprintf("%s must be a string.", k))
			}
			if typ == "password" && s != "" {
				switch {
				case s == crypto.Placeholder:
					s, _ = old[k].(string)
				case !crypto.IsEncrypted(s):
					enc, err := c.App.Crypto.Encrypt(s)
					if err != nil {
						return nil, err
					}
					s = enc
				}
			}
			out[k] = s
		case "boolean":
			b, ok := v.(bool)
			if !ok {
				return nil, api.FieldError("notification_configuration", fmt.Sprintf("%s must be a boolean.", k))
			}
			out[k] = b
		case "object":
			m, ok := v.(map[string]any)
			if !ok {
				return nil, api.FieldError("notification_configuration", fmt.Sprintf("%s must be an object.", k))
			}
			for hk, hv := range m {
				if _, ok := hv.(string); !ok {
					return nil, api.FieldError("notification_configuration", fmt.Sprintf("Header %q must be a string.", hk))
				}
			}
			out[k] = m
		}
	}
	u, _ := out["url"].(string)
	pu, err := url.Parse(u)
	if u == "" || err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return nil, api.FieldError("notification_configuration", "url must be an http(s) URL.")
	}
	m, _ := out["http_method"].(string)
	switch strings.ToUpper(m) {
	case "":
		out["http_method"] = "POST"
	case "POST", "PUT":
		out["http_method"] = strings.ToUpper(m)
	default:
		return nil, api.FieldError("notification_configuration", "http_method must be POST or PUT.")
	}
	if _, ok := out["headers"]; !ok {
		out["headers"] = map[string]any{}
	}
	return out, nil
}

// validateMessages checks custom messages: templates must parse and webhook bodies render to JSON.
func validateMessages(v any) error {
	if v == nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return api.FieldError("messages", "Expected a dict of messages.")
	}
	sample := sampleContext()
	for ev, raw := range m {
		if !contains(append(messageEvents, "workflow_approval"), ev) {
			return api.FieldError("messages", fmt.Sprintf("Unexpected message event %q.", ev))
		}
		if raw == nil {
			continue
		}
		entries := map[string]any{}
		msg, ok := raw.(map[string]any)
		if !ok {
			return api.FieldError("messages", fmt.Sprintf("Expected a dict for %s.", ev))
		}
		if ev == "workflow_approval" {
			for k, sub := range msg {
				entries[ev+"."+k] = sub
			}
		} else {
			entries[ev] = msg
		}
		for name, e := range entries {
			em, ok := e.(map[string]any)
			if !ok && e != nil {
				return api.FieldError("messages", fmt.Sprintf("Expected a dict for %s.", name))
			}
			for k, t := range em {
				s, ok := t.(string)
				if t != nil && !ok || (k != "message" && k != "body") {
					return api.FieldError("messages", fmt.Sprintf("Invalid entry %s.%s.", name, k))
				}
				out, err := tmpl.Render(s, sample)
				if err != nil {
					return api.FieldError("messages", fmt.Sprintf("Unable to render message for %s: %v", name, err))
				}
				if k == "body" && strings.TrimSpace(s) != "" {
					var js any
					if err := json.Unmarshal([]byte(out), &js); err != nil {
						return api.FieldError("messages", fmt.Sprintf("Webhook body for %s should be valid JSON: %v", name, err))
					}
				}
			}
		}
	}
	return nil
}

func sampleContext() map[string]any {
	job := map[string]any{"id": float64(42), "name": "Example", "status": "successful", "type": "job", "summary_fields": map[string]any{}}
	return map[string]any{"job": job, "job_friendly_name": "Job", "url": "https://example.com/jobs/42", "job_metadata": `{"id": 42}`,
		"approval_status": "approved", "approval_node_name": "Approve", "workflow_url": "https://example.com/jobs/41"}
}

func registerNotifications() {
	nt := api.Register(&Resource{
		Name:         "notification_templates",
		Type:         "notification_template",
		Table:        "notification_templates",
		Verbose:      "Notification Template",
		Ordering:     "name",
		ContentType:  "notificationtemplate",
		OrgColumn:    "organization_id",
		FolderColumn: "folder_id",
		Versioned:    true,
		Managed:      true,
		Copyable:     true,
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true},
			{Name: "notification_type", Type: api.TChoice, Required: true, Choices: notificationTypeChoices},
			{Name: "notification_configuration", Type: api.TJSON, Default: map[string]any{}},
			{Name: "messages", Type: api.TJSON, Nullable: true, Validate: func(c *Ctx, v any, data Obj) error { return validateMessages(v) },
				Help: "Optional custom messages for notification template."},
			folderField(),
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			cfg, given := data["notification_configuration"]
			if !given && old != nil {
				return nil
			}
			m, _ := cfg.(map[string]any)
			var oldCfg map[string]any
			if old != nil {
				oldCfg, _ = old["notification_configuration"].(map[string]any)
			}
			out, err := validateNotificationConfig(c, m, oldCfg)
			if err != nil {
				return err
			}
			data["notification_configuration"] = out
			return nil
		},
		Serialize: func(c *Ctx, o Obj) {
			if cfg, ok := o["notification_configuration"].(map[string]any); ok {
				if p, _ := cfg["password"].(string); p != "" {
					cfg["password"] = crypto.Placeholder
				}
			}
		},
		Summary: func(c *Ctx, objs []Obj) error {
			for _, o := range objs {
				recent := []Obj{}
				rows, err := c.DB().Query(c, `SELECT id, status, created, error FROM notifications WHERE notification_template_id = $1 ORDER BY id DESC LIMIT 5`, o["id"])
				if err != nil {
					return err
				}
				for rows.Next() {
					var id int64
					var status, errText string
					var created time.Time
					rows.Scan(&id, &status, &created, &errText)
					recent = append(recent, Obj{"id": id, "status": status, "created": api.FormatTime(created), "error": errText})
				}
				rows.Close()
				o["summary_fields"].(Obj)["recent_notifications"] = recent
			}
			return nil
		},
	})
	nt.SubLists = append(nt.SubLists, readOnlyChildSub("notifications", "notifications", "notification_template_id"))
	nt.Actions = append(nt.Actions, &api.Action{Name: "test", Methods: []string{"POST"}, Perm: "change", Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		id, _ := api.ID(obj["id"])
		base := c.App.Cfg.BaseURL
		subject := fmt.Sprintf("Tower Notification Test %d %s", id, base)
		nid, err := createNotification(c, c.App.DB, id, nil, subject, Obj{"body": fmt.Sprintf("Ansible Tower Test Notification %d %s", id, base)})
		if err != nil {
			api.WriteError(w, err)
			return
		}
		go deliver(context.Background(), c.App, nid)
		api.WriteJSON(w, 202, Obj{"notification": nid})
	}})

	notifView := api.Lookup("notification_templates")
	api.Register(&Resource{
		Name:         "notifications",
		Type:         "notification",
		Table:        "notifications",
		Verbose:      "Notification",
		Ordering:     "-id",
		NoName:       true,
		ReadOnly:     true,
		NoActivity:   true,
		SearchFields: []string{"subject"},
		Fields: []*Field{
			{Name: "notification_template", Type: api.TFK, Ref: "notification_templates", ReadOnly: true},
			{Name: "error", Type: api.TText, ReadOnly: true},
			{Name: "status", Type: api.TChoice, ReadOnly: true, Choices: labelled("pending", "Pending", "successful", "Successful", "failed", "Failed")},
			{Name: "notifications_sent", Type: api.TInt, ReadOnly: true},
			{Name: "notification_type", Type: api.TChoice, ReadOnly: true, Choices: notificationTypeChoices},
			{Name: "recipients", Type: api.TText, ReadOnly: true},
			{Name: "subject", Type: api.TText, ReadOnly: true},
			{Name: "body", Type: api.TJSON, ReadOnly: true},
		},
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return "t.notification_template_id IN (SELECT t.id FROM notification_templates t WHERE " + viewOf(c, notifView, arg) + ")"
		},
	})

	// Attaching a template to an object needs change on both.
	for _, res := range api.Resources() {
		for _, s := range res.SubLists {
			if strings.HasPrefix(s.Name, "notification_templates_") && s.Associate != nil && s.CanAttach == nil {
				s.CanAttach = func(c *Ctx, parent Obj, child int64) error {
					return api.RequirePermID(c, "id", "notification_templates", child, "change")
				}
			}
		}
	}
}

// createNotification records a pending notification.
func createNotification(ctx context.Context, db api.DBTX, ntID int64, jobID *int64, subject string, body any) (int64, error) {
	var typ string
	var cfg map[string]any
	if err := db.QueryRow(ctx, "SELECT notification_type, notification_configuration FROM notification_templates WHERE id = $1", ntID).Scan(&typ, &cfg); err != nil {
		return 0, err
	}
	b, _ := json.Marshal(body)
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO notifications (notification_template_id, unified_job_id, notification_type, recipients, subject, body)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, ntID, jobID, typ, str(cfg["url"]), subject, string(b)).Scan(&id)
	return id, err
}

// deliver sends a notification and records the outcome.
func deliver(ctx context.Context, app *api.App, id int64) {
	var cfg map[string]any
	var body json.RawMessage
	err := app.DB.QueryRow(ctx, `SELECT t.notification_configuration, n.body FROM notifications n
		JOIN notification_templates t ON t.id = n.notification_template_id WHERE n.id = $1`, id).Scan(&cfg, &body)
	if err == nil {
		err = sendWebhook(ctx, app, cfg, body)
	}
	status, errText, sent := "successful", "", 1
	if err != nil {
		status, errText, sent = "failed", err.Error(), 0
		slog.Warn("notification failed", "notification", id, "err", err)
	}
	app.DB.Exec(context.Background(), "UPDATE notifications SET status = $2, error = $3, notifications_sent = $4, modified = now() WHERE id = $1",
		id, status, errText, sent)
}

func sendWebhook(ctx context.Context, app *api.App, cfg map[string]any, body []byte) error {
	method := str(cfg["http_method"])
	if method == "" {
		method = "POST"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, str(cfg["url"]), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", api.ProductName+"/"+api.ProductVersion)
	if h, ok := cfg["headers"].(map[string]any); ok {
		for k, v := range h {
			req.Header.Set(k, str(v))
		}
	}
	if user := str(cfg["username"]); user != "" || str(cfg["password"]) != "" {
		pw := str(cfg["password"])
		if crypto.IsEncrypted(pw) {
			if pw, err = app.Crypto.Decrypt(pw); err != nil {
				return fmt.Errorf("decrypting password: %w", err)
			}
		}
		req.SetBasicAuth(user, pw)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if boolv(cfg["disable_ssl_verification"]) {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicitly requested by the template
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return fmt.Errorf("Error sending notification webhook: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Error sending notification webhook: %s", resp.Status)
	}
	return nil
}

// jobContentTypes lists the objects whose notification templates apply to a job.
func jobNotificationTargets(ctx context.Context, db api.DBTX, row jobs.Row) [][2]any {
	var out [][2]any
	ujt := row.Int("unified_job_template_id")
	var ujtType string
	if ujt != 0 {
		db.QueryRow(ctx, "SELECT type FROM unified_job_templates WHERE id = $1", ujt).Scan(&ujtType)
	}
	ct := map[string]string{"job_template": "jobtemplate", "project": "project", "inventory_source": "inventorysource",
		"system_job_template": "systemjobtemplate", "workflow_job_template": "workflowjobtemplate"}[ujtType]
	if ct != "" {
		out = append(out, [2]any{ct, ujt})
	}
	if row.Str("type") == "job" && row.Int("project_id") != 0 {
		out = append(out, [2]any{"project", row.Int("project_id")})
	}
	var org int64
	switch row.Str("type") {
	case "inventory_update", "ad_hoc_command":
		db.QueryRow(ctx, "SELECT coalesce(organization_id, 0) FROM inventories WHERE id = $1", row.Int("inventory_id")).Scan(&org)
	case "system_job":
	default:
		org = row.Int("organization_id")
	}
	if org != 0 {
		out = append(out, [2]any{"organization", org})
	}
	return out
}

var friendlyNames = map[string]string{"job": "Job", "project_update": "Project Update", "inventory_update": "Inventory Update",
	"ad_hoc_command": "AdHoc Command", "system_job": "Management Job", "workflow_job": "Workflow Job"}

// JobUIURL is the UI address of a job's output.
func JobUIURL(app *api.App, id int64) string {
	return strings.TrimRight(app.Cfg.BaseURL, "/") + fmt.Sprintf("/jobs/%d", id)
}

// notificationContext builds the template context for a job.
func notificationContext(c *Ctx, row jobs.Row) (map[string]any, map[string]any) {
	typ := row.Str("type")
	id := row.ID()
	job := map[string]any{}
	if res := api.LookupType(typ); res != nil {
		if obj, err := api.Get(c, res, id); err == nil {
			if out, err := api.SerializeOne(c, res, obj); err == nil {
				b, _ := json.Marshal(out)
				json.Unmarshal(b, &job)
			}
		}
	}
	for _, k := range []string{"job_env", "job_args", "job_cwd", "related", "result_traceback"} {
		delete(job, k)
	}
	if sf, ok := job["summary_fields"].(map[string]any); ok {
		delete(sf, "user_capabilities")
	}
	url := JobUIURL(c.App, id)
	meta := map[string]any{"id": id, "name": row.Str("name"), "url": url, "status": row.Str("status"),
		"started": job["started"], "finished": job["finished"], "traceback": row.Str("result_traceback"), "friendly_name": friendlyNames[typ]}
	if sf, ok := job["summary_fields"].(map[string]any); ok {
		if cb, ok := sf["created_by"].(map[string]any); ok {
			meta["created_by"] = cb["username"]
		}
		for _, k := range []string{"inventory", "project"} {
			if o, ok := sf[k].(map[string]any); ok {
				meta[k] = o["name"]
			}
		}
	}
	if typ == "job" {
		meta["playbook"], meta["limit"], meta["extra_vars"] = row.Str("playbook"), row.Str("limit"), maskSurveyPasswords(row.Str("extra_vars"))
		hosts := map[string]any{}
		rows, err := c.DB().Query(c, `SELECT host_name, failed, changed, dark, failures, ok, processed, skipped, rescued, ignored
			FROM job_host_summaries WHERE job_id = $1`, id)
		if err == nil {
			for rows.Next() {
				var name string
				var failed bool
				var changed, dark, failures, ok, processed, skipped, rescued, ignored int
				rows.Scan(&name, &failed, &changed, &dark, &failures, &ok, &processed, &skipped, &rescued, &ignored)
				hosts[name] = map[string]any{"failed": failed, "changed": changed, "dark": dark, "failures": failures, "ok": ok,
					"processed": processed, "skipped": skipped, "rescued": rescued, "ignored": ignored}
			}
			rows.Close()
		}
		meta["hosts"] = hosts
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "    ")
	return map[string]any{"job": job, "job_friendly_name": friendlyNames[typ], "url": url, "job_metadata": string(metaJSON)}, meta
}

var approvalMessages = map[string]string{
	"running":   `The approval node "{{ approval_node_name }}" needs review. This node can be viewed at: {{ workflow_url }}`,
	"approved":  `The approval node "{{ approval_node_name }}" was approved. {{ workflow_url }}`,
	"denied":    `The approval node "{{ approval_node_name }}" was denied. {{ workflow_url }}`,
	"timed_out": `The approval node "{{ approval_node_name }}" has timed out. {{ workflow_url }}`,
}

// notifyApproval sends "approvals" notifications of the workflow (and its
// organization) for an approval reaching status running, approved, denied or timed_out.
func notifyApproval(c *Ctx, approvalID int64, status string) {
	var wfJob, wfjt, org int64
	var name string
	if err := c.App.DB.QueryRow(c, `SELECT w.id, coalesce(w.unified_job_template_id, 0), coalesce(w.organization_id, 0), a.name
		FROM unified_jobs a JOIN workflow_job_nodes n ON n.job_id = a.id JOIN unified_jobs w ON w.id = n.workflow_job_id WHERE a.id = $1`, approvalID).
		Scan(&wfJob, &wfjt, &org, &name); err != nil {
		return
	}
	rows, err := c.App.DB.Query(c, `SELECT DISTINCT notification_template_id FROM notification_template_links WHERE event = 'approvals' AND
		((content_type = 'workflowjobtemplate' AND object_id = $1) OR (content_type = 'organization' AND object_id = $2)) ORDER BY 1`, wfjt, org)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	workflowURL := JobUIURL(c.App, wfJob)
	tctx := map[string]any{"approval_status": status, "approval_node_name": name, "workflow_url": workflowURL,
		"job": map[string]any{"id": approvalID, "name": name, "status": status, "type": "workflow_approval"}, "job_friendly_name": "Workflow Approval",
		"url": workflowURL}
	for _, ntID := range ids {
		var messages map[string]any
		c.App.DB.QueryRow(c, "SELECT messages FROM notification_templates WHERE id = $1", ntID).Scan(&messages)
		subjectT, bodyT := approvalMessages[status], ""
		if wa, ok := messages["workflow_approval"].(map[string]any); ok {
			if msg, ok := wa[status].(map[string]any); ok {
				if s := str(msg["message"]); s != "" {
					subjectT = s
				}
				bodyT = str(msg["body"])
			}
		}
		subject, err := tmpl.Render(subjectT, tctx)
		if err != nil {
			subject = "Unable to render message: " + err.Error()
		}
		var body any = map[string]any{"body": subject, "approval_status": status, "approval_node_name": name, "workflow_url": workflowURL, "id": approvalID}
		if strings.TrimSpace(bodyT) != "" {
			var parsed any
			rendered, err := tmpl.Render(bodyT, tctx)
			if err == nil {
				err = json.Unmarshal([]byte(rendered), &parsed)
			}
			if err == nil {
				body = parsed
			}
		}
		id := approvalID
		nid, err := createNotification(c, c.App.DB, ntID, &id, subject, body)
		if err != nil {
			continue
		}
		go deliver(context.Background(), c.App, nid)
	}
}

// notifyJob sends the job's notifications for an event (started, success, error).
func notifyJob(ctx context.Context, m *jobs.Manager, row jobs.Row, event string) {
	c := &api.Ctx{Context: ctx, App: m.App, User: rbac.System(), Extra: map[string]any{}}
	targets := jobNotificationTargets(ctx, m.DB(), row)
	if len(targets) == 0 {
		return
	}
	seen := map[int64]bool{}
	var ids []int64
	for _, t := range targets {
		rows, err := m.DB().Query(ctx, `SELECT notification_template_id FROM notification_template_links
			WHERE content_type = $1 AND object_id = $2 AND event = $3 ORDER BY notification_template_id`, t[0], t[1], event)
		if err != nil {
			return
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	if len(ids) == 0 {
		return
	}
	tctx, meta := notificationContext(c, row)
	jobID := row.ID()
	for _, ntID := range ids {
		var messages map[string]any
		m.DB().QueryRow(ctx, "SELECT messages FROM notification_templates WHERE id = $1", ntID).Scan(&messages)
		subjectT, bodyT := defaultMessage, ""
		if msg, ok := messages[event].(map[string]any); ok {
			if s := str(msg["message"]); s != "" {
				subjectT = s
			}
			bodyT = str(msg["body"])
		}
		subject, err := tmpl.Render(subjectT, tctx)
		if err != nil {
			subject = "Unable to render message: " + err.Error()
		}
		var body any = meta
		if strings.TrimSpace(bodyT) != "" {
			rendered, err := tmpl.Render(bodyT, tctx)
			var parsed any
			if err == nil {
				err = json.Unmarshal([]byte(rendered), &parsed)
			}
			if err != nil {
				body = map[string]any{"error": "Unable to render webhook body: " + err.Error()}
			} else {
				body = parsed
			}
		}
		nid, err := createNotification(ctx, m.DB(), ntID, &jobID, subject, body)
		if err != nil {
			slog.Error("creating notification", "template", ntID, "err", err)
			continue
		}
		go deliver(context.Background(), m.App, nid)
	}
}

// JobStartedNotifications and JobFinishedNotifications are manager hooks.
func JobStartedNotifications(ctx context.Context, m *jobs.Manager, row jobs.Row) {
	notifyJob(ctx, m, row, "started")
}

func JobFinishedNotifications(ctx context.Context, m *jobs.Manager, row jobs.Row) {
	event := "error"
	if row.Str("status") == "successful" {
		event = "success"
	}
	notifyJob(ctx, m, row, event)
}
