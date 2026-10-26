package resources

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/vars"
	"github.com/go-chi/chi/v5"
)

// webhookCredentialTypes maps a webhook service to the token credential used for status updates.
var webhookCredentialTypes = map[string]string{"github": "github_token", "gitlab": "gitlab_token", "bitbucket_dc": "bitbucket_dc_token"}

func generateWebhookKey() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 50)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// webhookBeforeSave generates or clears the webhook key and checks the webhook credential.
func webhookBeforeSave(c *Ctx, old, data Obj) error {
	merged := Obj{}
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}
	service := str(merged["webhook_service"])
	if service == "" {
		if old != nil && str(old["webhook_service"]) != "" {
			data["webhook_credential"] = nil
			if id, ok := api.ID(old["id"]); ok {
				c.DB().Exec(c, "UPDATE unified_job_templates SET webhook_key = '' WHERE id = $1", id)
			}
		} else if _, ok := api.ID(data["webhook_credential"]); ok {
			return api.FieldError("webhook_credential", "Must set webhook service before setting webhook credential.")
		}
		return nil
	}
	if cred, ok := api.ID(merged["webhook_credential"]); ok {
		var ns string
		c.DB().QueryRow(c, `SELECT coalesce(ct.namespace, '') FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1`, cred).Scan(&ns)
		if want := webhookCredentialTypes[service]; ns != want {
			return api.FieldError("webhook_credential", fmt.Sprintf("Must match the selected webhook service (a %s credential is required).", want))
		}
		if fmt.Sprint(old["webhook_credential"]) != fmt.Sprint(cred) {
			if err := api.RequirePermID(c, "webhook_credential", "credentials", cred, "use"); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureWebhookKey sets a key when a template has a webhook service but no key yet.
func ensureWebhookKey(c *Ctx, obj Obj) error {
	if str(obj["webhook_service"]) == "" {
		return nil
	}
	enc, err := c.App.Crypto.Encrypt(generateWebhookKey())
	if err != nil {
		return err
	}
	_, err = c.DB().Exec(c, "UPDATE unified_job_templates SET webhook_key = $2 WHERE id = $1 AND webhook_key = ''", obj["id"], enc)
	return err
}

func webhookKey(c *Ctx, id any) string {
	var key string
	c.DB().QueryRow(c, "SELECT webhook_key FROM unified_job_templates WHERE id = $1", id).Scan(&key)
	if crypto.IsEncrypted(key) {
		if plain, err := c.App.Crypto.Decrypt(key); err == nil {
			return plain
		}
		return ""
	}
	return key
}

func handleWebhookKey(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	if r.Method == "GET" {
		api.WriteJSON(w, 200, Obj{"webhook_key": webhookKey(c, obj["id"])})
		return
	}
	if str(obj["webhook_service"]) == "" {
		api.WriteError(w, api.BadRequest("Webhook service is not configured for this template."))
		return
	}
	key := generateWebhookKey()
	enc, err := c.App.Crypto.Encrypt(key)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	c.DB().Exec(c, "UPDATE unified_job_templates SET webhook_key = $2 WHERE id = $1", obj["id"], enc)
	api.WriteJSON(w, 201, Obj{"webhook_key": key})
}

// --- generic webhook mapping ---

var jsonPathRe = regexp.MustCompile(`^\$((\.[A-Za-z0-9_-]+)|(\[\d+\])|(\['[^']*'\])|(\["[^"]*"\]))*$`)
var jsonPathSeg = regexp.MustCompile(`\.([A-Za-z0-9_-]+)|\[(\d+)\]|\['([^']*)'\]|\["([^"]*)"\]`)

// JSONPath evaluates a simple path ($.a.b[0]['c']) against a decoded JSON document.
func JSONPath(doc any, path string) (any, bool) {
	if !jsonPathRe.MatchString(path) {
		return nil, false
	}
	cur := doc
	for _, m := range jsonPathSeg.FindAllStringSubmatch(path[1:], -1) {
		switch {
		case m[2] != "":
			list, ok := cur.([]any)
			i, _ := strconv.Atoi(m[2])
			if !ok || i >= len(list) {
				return nil, false
			}
			cur = list[i]
		default:
			key := m[1] + m[3] + m[4]
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = obj[key]; !ok {
				return nil, false
			}
		}
	}
	return cur, true
}

var mappingPrompts = map[string]bool{"limit": true, "scm_branch": true, "job_tags": true, "skip_tags": true, "inventory": true}

// validateWebhookMapping checks {"filter": {path: value}, "extra_vars": {var: path}, "<prompt>": path}.
func validateWebhookMapping(c *Ctx, v any, data Obj) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("Expected an object.")
	}
	checkPath := func(p any) error {
		s, ok := p.(string)
		if !ok || !jsonPathRe.MatchString(s) {
			return fmt.Errorf("Invalid JSONPath %v (use $.a.b[0]['c']).", p)
		}
		return nil
	}
	for k, val := range m {
		switch {
		case k == "filter":
			f, ok := val.(map[string]any)
			if !ok {
				return fmt.Errorf("filter must map JSONPaths to expected values.")
			}
			for p := range f {
				if err := checkPath(p); err != nil {
					return err
				}
			}
		case k == "extra_vars":
			ev, ok := val.(map[string]any)
			if !ok {
				return fmt.Errorf("extra_vars must map variable names to JSONPaths.")
			}
			for _, p := range ev {
				if err := checkPath(p); err != nil {
					return err
				}
			}
		case mappingPrompts[k]:
			if err := checkPath(val); err != nil {
				return err
			}
		default:
			return fmt.Errorf("Unsupported mapping key %q.", k)
		}
	}
	return nil
}

// --- receivers ---

type webhookEvent struct {
	eventType, guid, ref, statusAPI string
	payload                         any
}

func hmacHex(h func() hash.Hash, key string, body []byte) string {
	m := hmac.New(h, []byte(key))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func equalSecret(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// verifyWebhook authenticates a request for a service and extracts the event.
func verifyWebhook(service, key string, r *http.Request, body []byte) (*webhookEvent, error) {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, api.BadRequest("Invalid JSON payload.")
	}
	ev := &webhookEvent{payload: payload}
	get := func(path string) string {
		v, _ := JSONPath(payload, path)
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	sha256Sig := func(header string) bool {
		sig := strings.TrimPrefix(r.Header.Get(header), "sha256=")
		return equalSecret(sig, hmacHex(sha256.New, key, body))
	}
	switch service {
	case "github":
		ok := sha256Sig("X-Hub-Signature-256")
		if !ok && r.Header.Get("X-Hub-Signature") != "" {
			ok = equalSecret(strings.TrimPrefix(r.Header.Get("X-Hub-Signature"), "sha1="), hmacHex(sha1.New, key, body))
		}
		if !ok {
			return nil, api.Detail(403, "Signature validation failed.")
		}
		ev.eventType, ev.guid = r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
		switch ev.eventType {
		case "pull_request":
			ev.ref, ev.statusAPI = get("$.pull_request.head.sha"), get("$.pull_request.statuses_url")
		case "push":
			ev.ref = get("$.after")
		}
	case "gitlab":
		if !equalSecret(r.Header.Get("X-Gitlab-Token"), key) {
			return nil, api.Detail(403, "Token validation failed.")
		}
		ev.eventType, ev.guid = r.Header.Get("X-Gitlab-Event"), r.Header.Get("X-Gitlab-Event-UUID")
		switch ev.eventType {
		case "Push Hook", "Tag Push Hook":
			ev.ref = get("$.checkout_sha")
		case "Merge Request Hook":
			ev.ref = get("$.object_attributes.last_commit.id")
			if u, err := url.Parse(get("$.project.web_url")); err == nil && u.Host != "" {
				ev.statusAPI = fmt.Sprintf("%s://%s/api/v4/projects/%s/statuses/%s", u.Scheme, u.Host, get("$.project.id"), ev.ref)
			}
		}
	case "bitbucket_dc":
		if !sha256Sig("X-Hub-Signature") {
			return nil, api.Detail(403, "Signature validation failed.")
		}
		ev.eventType, ev.guid = r.Header.Get("X-Event-Key"), r.Header.Get("X-Request-Id")
		switch {
		case ev.eventType == "repo:refs_changed":
			ev.ref = get("$.changes[0].toHash")
		case strings.HasPrefix(ev.eventType, "pr:"):
			ev.ref = get("$.pullRequest.fromRef.latestCommit")
			if links, ok := JSONPath(payload, "$.pullRequest.links.self[0].href"); ok {
				if u, err := url.Parse(fmt.Sprint(links)); err == nil {
					ev.statusAPI = fmt.Sprintf("%s://%s/rest/build-status/1.0/commits/%s", u.Scheme, u.Host, ev.ref)
				}
			}
		}
	case "generic":
		ok := sha256Sig("X-Hub-Signature-256")
		if !ok {
			ok = equalSecret(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), key) || equalSecret(r.Header.Get("X-Webhook-Token"), key)
		}
		if !ok {
			return nil, api.Detail(403, "Signature validation failed.")
		}
		ev.eventType = r.Header.Get("X-Event-Type")
		if ev.eventType == "" {
			ev.eventType = "generic"
		}
		ev.guid = r.Header.Get("X-Request-Id")
		if ev.guid == "" {
			ev.guid = r.Header.Get("X-Delivery-Id")
		}
	default:
		return nil, api.ErrNotFound
	}
	if ev.guid == "" {
		sum := sha256.Sum256(body)
		ev.guid = hex.EncodeToString(sum[:])
	}
	return ev, nil
}

func installWebhookRoutes(app *api.App) {
	for _, res := range []string{"job_templates", "workflow_job_templates"} {
		for _, service := range []string{"github", "gitlab", "bitbucket_dc"} {
			res, service := res, service
			app.HandlePublic("POST", "/api/v2/"+res+"/{id}/"+service+"/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
				receiveWebhook(c, w, r, res, service)
			})
		}
		res := res
		app.HandlePublic("POST", "/api/v2/"+res+"/{id}/webhook/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
			receiveWebhook(c, w, r, res, "generic")
		})
	}
}

func receiveWebhook(c *Ctx, w http.ResponseWriter, r *http.Request, resName, service string) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		api.WriteError(w, api.ErrNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	sys := &api.Ctx{Context: c.Context, App: c.App, User: rbac.System(), Req: r, Extra: map[string]any{}}
	res := api.Lookup(resName)
	tpl, err := api.Get(sys, res, id)
	if err != nil || str(tpl["webhook_service"]) != service {
		api.WriteError(w, api.ErrNotFound)
		return
	}
	key := webhookKey(sys, id)
	if key == "" {
		api.WriteError(w, api.Detail(403, "Webhook key is not configured."))
		return
	}
	ev, err := verifyWebhook(service, key, r, body)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if service == "github" && ev.eventType == "ping" {
		api.WriteJSON(w, 200, Obj{"message": "pong"})
		return
	}
	var dup bool
	sys.DB().QueryRow(sys, "SELECT EXISTS (SELECT 1 FROM unified_jobs WHERE unified_job_template_id = $1 AND webhook_guid = $2)", id, ev.guid).Scan(&dup)
	if dup {
		api.WriteJSON(w, 202, Obj{"message": "Webhook previously received, aborting."})
		return
	}
	extra := map[string]any{}
	for _, prefix := range []string{"awx", "tower"} {
		extra[prefix+"_webhook_event_type"] = ev.eventType
		extra[prefix+"_webhook_event_guid"] = ev.guid
		extra[prefix+"_webhook_event_ref"] = ev.ref
		extra[prefix+"_webhook_status_api"] = ev.statusAPI
		extra[prefix+"_webhook_payload"] = ev.payload
	}
	trusted := Obj{}
	if service == "generic" {
		mapping, _ := tpl["webhook_mapping"].(map[string]any)
		if f, ok := mapping["filter"].(map[string]any); ok {
			for path, want := range f {
				got, ok := JSONPath(ev.payload, path)
				if !ok || fmt.Sprint(got) != fmt.Sprint(want) {
					api.WriteJSON(w, 200, Obj{"message": "Webhook ignored: filter did not match."})
					return
				}
			}
		}
		if m, ok := mapping["extra_vars"].(map[string]any); ok {
			for name, path := range m {
				if v, ok := JSONPath(ev.payload, str(path)); ok {
					extra[name] = v
				}
			}
		}
		for k := range mappingPrompts {
			if p, ok := mapping[k].(string); ok {
				if v, ok := JSONPath(ev.payload, p); ok {
					if k == "inventory" {
						trusted[k] = v
					} else {
						trusted[k] = fmt.Sprint(v)
					}
				}
			}
		}
	}
	ov := &LaunchOverrides{LaunchType: "webhook", Trusted: trusted, ExtraJob: map[string]any{
		"webhook_service": service, "webhook_credential_id": tpl["webhook_credential"], "webhook_guid": ev.guid}}
	launchBody := Obj{"extra_vars": extra}
	var jobID int64
	if resName == "job_templates" {
		jobID, _, err = LaunchJobTemplate(sys, tpl, launchBody, "webhook", ov)
	} else {
		jobID, _, err = LaunchWorkflowJobTemplate(sys, tpl, launchBody, "webhook", ov)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 202, Obj{"message": "Job queued.", "job": jobID})
}

// --- status updates back to the SCM ---

func webhookStatus(ctx context.Context, m *jobs.Manager, row jobs.Row, status string) {
	service := row.Str("webhook_service")
	credID := row.Int("webhook_credential_id")
	if service == "" || service == "generic" || credID == 0 {
		return
	}
	ev, _ := vars.Parse(row.Str("extra_vars"))
	statusAPI, _ := ev["awx_webhook_status_api"].(string)
	if statusAPI == "" {
		return
	}
	cred, err := ResolveCredential(ctx, m.App, credID)
	if err != nil {
		return
	}
	token, _ := cred.Inputs["token"].(string)
	target := JobUIURL(m.App, row.ID())
	var payload map[string]any
	header := http.Header{"Content-Type": {"application/json"}}
	switch service {
	case "github":
		state := map[string]string{"pending": "pending", "successful": "success", "failed": "failure", "canceled": "failure", "error": "error"}[status]
		payload = map[string]any{"state": state, "context": "ansible/awx", "target_url": target}
		header.Set("Authorization", "token "+token)
	case "gitlab":
		state := map[string]string{"pending": "pending", "running": "running", "successful": "success", "failed": "failed", "canceled": "canceled", "error": "failed"}[status]
		payload = map[string]any{"state": state, "context": "ansible/awx", "target_url": target}
		header.Set("PRIVATE-TOKEN", token)
	case "bitbucket_dc":
		state := map[string]string{"pending": "INPROGRESS", "successful": "SUCCESSFUL", "failed": "FAILED", "canceled": "FAILED", "error": "FAILED"}[status]
		payload = map[string]any{"state": state, "key": "ansible/awx", "url": target}
		header.Set("Authorization", "Bearer "+token)
	default:
		return
	}
	b, _ := json.Marshal(payload)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "POST", statusAPI, bytes.NewReader(b))
		if err != nil {
			return
		}
		req.Header = header
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Warn("webhook status update failed", "job", row.ID(), "err", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Warn("webhook status update rejected", "job", row.ID(), "status", resp.Status)
		}
	}()
}

// WebhookStatusStarted and WebhookStatusFinished report job progress to the SCM.
func WebhookStatusStarted(ctx context.Context, m *jobs.Manager, row jobs.Row) {
	webhookStatus(ctx, m, row, "pending")
}

func WebhookStatusFinished(ctx context.Context, m *jobs.Manager, row jobs.Row) {
	webhookStatus(ctx, m, row, row.Str("status"))
}

// registerWebhooks adds webhook handling to job and workflow templates.
func registerWebhooks() {
	for _, name := range []string{"job_templates", "workflow_job_templates"} {
		res := api.Lookup(name)
		for _, f := range res.Fields {
			if f.Name == "webhook_service" {
				f.Choices = webhookServices
			}
		}
		if name == "job_templates" {
			res.Fields = append(res.Fields, &Field{Name: "webhook_mapping", Type: api.TJSON, Default: map[string]any{}, Validate: validateWebhookMapping})
			res.Actions = append(res.Actions, &api.Action{Name: "webhook_key", Methods: []string{"GET", "POST"}, Perm: "change", Handler: handleWebhookKey})
		}
		prevSave := res.BeforeSave
		res.BeforeSave = func(c *Ctx, old, data Obj) error {
			if prevSave != nil {
				if err := prevSave(c, old, data); err != nil {
					return err
				}
			}
			return webhookBeforeSave(c, old, data)
		}
		prevCreate := res.AfterCreate
		res.AfterCreate = func(c *Ctx, obj Obj) error {
			if prevCreate != nil {
				if err := prevCreate(c, obj); err != nil {
					return err
				}
			}
			return ensureWebhookKey(c, obj)
		}
		prevUpdate := res.AfterUpdate
		res.AfterUpdate = func(c *Ctx, old, obj Obj) error {
			if prevUpdate != nil {
				if err := prevUpdate(c, old, obj); err != nil {
					return err
				}
			}
			return ensureWebhookKey(c, obj)
		}
		prevSummary := res.Summary
		res.Summary = func(c *Ctx, objs []Obj) error {
			if prevSummary != nil {
				if err := prevSummary(c, objs); err != nil {
					return err
				}
			}
			for _, o := range objs {
				id, _ := api.ID(o["id"])
				rel := o["related"].(Obj)
				if str(o["webhook_service"]) != "" {
					rel["webhook_key"] = res.URL(id) + "webhook_key/"
					svc := str(o["webhook_service"])
					if svc == "generic" {
						svc = "webhook"
					}
					rel["webhook_receiver"] = res.URL(id) + svc + "/"
				} else {
					rel["webhook_receiver"] = ""
				}
			}
			return nil
		}
	}
}
