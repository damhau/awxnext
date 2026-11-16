package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/rulebooks"
)

// EDA's projects are awxnext's git projects; its decision environments
// are execution environments, which connectors run in; its event streams
// are awxnext's.

var edaProjectNames = map[string]string{"name": "name", "description": "description", "url": "scm_url", "scm_branch": "scm_branch",
	"scm_refspec": "scm_refspec", "eda_credential_id": "credential", "signature_validation_credential_id": "signature_validation_credential",
	"organization_id": "organization", "update_revision_on_launch": "scm_update_on_launch",
	"scm_update_cache_timeout": "scm_update_cache_timeout", "proxy": "scm_proxy", "verify_ssl": "scm_verify_ssl"}

func edaProjects() *edaSpec {
	return &edaSpec{
		path: "projects", res: "projects",
		filters: map[string]string{"name": "name__istartswith", "url": "scm_url__icontains"},
		where:   func(c *Ctx, arg ArgFunc) string { return "t.scm_type = 'git'" },
		names:   edaInvert(edaProjectNames),
		out:     edaProjectOut,
		in:      edaProjectIn,
	}
}

var proxyPassword = regexp.MustCompile(`^([a-z][a-z0-9+.-]*://[^:/@]*:)([^@]*)@`)

// maskProxy hides a proxy URL's password, as EDA shows it.
func maskProxy(p string) string {
	return proxyPassword.ReplaceAllString(p, "${1}"+strings.ReplaceAll(crypto.Placeholder, "$", "$$")+"@")
}

func projectProxy(c *Ctx, id any) (string, error) {
	var enc string
	if err := c.DB().QueryRow(c, "SELECT scm_proxy FROM unified_job_templates WHERE id = $1", id).Scan(&enc); err != nil {
		return "", err
	}
	if enc == "" {
		return "", nil
	}
	return c.App.Crypto.Decrypt(enc)
}

func edaProjectIn(c *Ctx, body, old Obj) (Obj, error) {
	data := edaCopy(body, edaProjectNames)
	for _, f := range []string{"description", "scm_branch", "scm_refspec", "scm_proxy"} {
		if v, ok := data[f]; ok && v == nil {
			data[f] = ""
		}
	}
	if old == nil {
		data["scm_type"] = "git"
	}
	if v, ok := data["scm_proxy"]; ok {
		p := str(v)
		var cur string
		if old != nil {
			var err error
			if cur, err = projectProxy(c, old["id"]); err != nil {
				return nil, err
			}
		}
		switch {
		case old != nil && (p == cur || p == maskProxy(cur)):
			// Unchanged: saving it again would sync the project again.
			delete(data, "scm_proxy")
		case strings.Contains(p, crypto.Placeholder):
			return nil, api.FieldError("scm_proxy", "The password in the proxy field should be unencrypted")
		}
	}
	return data, nil
}

// edaImportState is EDA's import_state of a project: its sync's status.
func edaImportState(status string) string {
	switch status {
	case "running":
		return "running"
	case "successful":
		return "completed"
	case "failed", "error", "canceled":
		return "failed"
	}
	return "pending"
}

func edaProjectOut(c *Ctx, o Obj) (Obj, error) {
	proxy, err := projectProxy(c, o["id"])
	if err != nil {
		return nil, err
	}
	state := edaImportState(str(o["status"]))
	var why string
	var synced *time.Time
	err = c.DB().QueryRow(c, `SELECT job_explanation, greatest(created, started, finished) FROM unified_jobs WHERE unified_job_template_id = $1 AND type = 'project_update'
		ORDER BY id DESC LIMIT 1`, o["id"]).Scan(&why, &synced)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var importErr any
	if state == "failed" {
		importErr = why
	}
	out := edaCommon(o, Obj{"name": o["name"], "description": o["description"], "url": o["scm_url"], "proxy": maskProxy(proxy),
		"scm_type": "git", "scm_branch": o["scm_branch"], "scm_refspec": o["scm_refspec"], "git_hash": o["scm_revision"],
		"verify_ssl": o["scm_verify_ssl"], "organization_id": o["organization"], "eda_credential_id": o["credential"],
		"signature_validation_credential_id": o["signature_validation_credential"],
		"update_revision_on_launch":          o["scm_update_on_launch"], "scm_update_cache_timeout": o["scm_update_cache_timeout"],
		"import_state": state, "import_error": importErr, "last_synced_at": o["last_job_run"]})
	// A sync changes EDA's project (its import_state, git_hash): EDA's
	// modified_at is the later of the project's change and its last sync.
	if m, err := time.Parse(time.RFC3339Nano, str(o["modified"])); err == nil && synced != nil && synced.After(m) {
		out["modified_at"] = api.FormatTime(*synced)
	}
	return out, nil
}

func installEDAProjectActions(app *api.App) {
	app.Handle("POST", edaRoot+"projects/{id}/sync/", edaProjectSync)
}

// edaProjectSync syncs a project, as its update/ action does: 409 while a
// sync runs.
func edaProjectSync(c *Ctx, w http.ResponseWriter, r *http.Request) {
	s := edaProjects()
	obj, err := s.object(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	ok, err := api.HasPerm(c, api.Lookup("projects"), obj, "update")
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if !ok {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	var busy bool
	if err := c.DB().QueryRow(c, `SELECT EXISTS (SELECT 1 FROM unified_jobs WHERE unified_job_template_id = $1 AND type = 'project_update'
		AND status IN ('new', 'pending', 'waiting', 'running'))`, obj["id"]).Scan(&busy); err != nil {
		api.WriteError(w, err)
		return
	}
	if busy {
		api.WriteError(w, api.Detail(409, "Project is already being synced."))
		return
	}
	if _, err := LaunchProjectUpdate(c, obj, "manual", "check", ""); err != nil {
		api.WriteError(w, err)
		return
	}
	fresh, err := api.Get(c, api.Lookup("projects"), mustID(obj["id"]))
	if err == nil {
		fresh, err = s.renderOne(c, fresh)
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 202, fresh)
}

func mustID(v any) int64 {
	id, _ := api.ID(v)
	return id
}

var edaDENames = map[string]string{"name": "name", "description": "description", "image_url": "image", "pull_policy": "pull",
	"organization_id": "organization", "eda_credential_id": "credential"}

func edaDecisionEnvironments() *edaSpec {
	return &edaSpec{
		path: "decision-environments", res: "execution_environments",
		filters: map[string]string{"name": "name__istartswith"},
		names:   edaInvert(edaDENames),
		out: func(c *Ctx, o Obj) (Obj, error) {
			pull := str(o["pull"])
			if pull == "" {
				pull = "missing"
			}
			return edaCommon(o, Obj{"name": o["name"], "description": o["description"], "image_url": o["image"], "pull_policy": pull,
				"organization_id": o["organization"], "eda_credential_id": o["credential"]}), nil
		},
		in: func(c *Ctx, body, old Obj) (Obj, error) {
			data := edaCopy(body, edaDENames)
			if v, ok := data["description"]; ok && v == nil {
				data["description"] = ""
			}
			if old == nil && str(data["pull"]) == "" {
				data["pull"] = "always"
			}
			return data, nil
		},
	}
}

var edaStreamNames = map[string]string{"name": "name", "test_mode": "test_mode", "additional_data_headers": "additional_data_headers",
	"eda_credential_id": "credential", "organization_id": "organization"}

var uuidForm = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func edaEventStreams() *edaSpec {
	s := &edaSpec{
		path: "event-streams", res: "event_streams",
		filters: map[string]string{"name": "name__istartswith", "test_mode": "test_mode"},
		names:   edaInvert(edaStreamNames),
		out:     edaStreamOut,
		created: edaStreamUUID,
	}
	s.in = func(c *Ctx, body, old Obj) (Obj, error) {
		data := edaCopy(body, edaStreamNames)
		if v, ok := data["additional_data_headers"]; ok && v == nil {
			data["additional_data_headers"] = ""
		}
		v, ok := body["uuid"]
		if !ok || v == nil || str(v) == "" {
			return data, nil
		}
		u := strings.ToLower(str(v))
		if !uuidForm.MatchString(u) {
			return nil, api.FieldError("uuid", "Must be a valid UUID.")
		}
		if old != nil {
			if u != str(old["uuid"]) {
				return nil, api.FieldError("uuid", "An event stream's UUID can't change: its senders post to it.")
			}
			return data, nil
		}
		var taken bool
		if err := c.DB().QueryRow(c, "SELECT EXISTS (SELECT 1 FROM event_streams WHERE uuid = $1)", u).Scan(&taken); err != nil {
			return nil, err
		}
		if taken {
			return nil, api.FieldError("uuid", "Event stream with this uuid already exists.")
		}
		c.Extra["eda_uuid"] = u
		return data, nil
	}
	return s
}

// edaStreamUUID gives a stream created through EDA the UUID its body
// named.
func edaStreamUUID(c *Ctx, obj Obj) error {
	u, ok := c.Extra["eda_uuid"].(string)
	if !ok {
		return nil
	}
	if _, err := c.DB().Exec(c, "UPDATE event_streams SET uuid = $2 WHERE id = $1", obj["id"], u); err != nil {
		return err
	}
	obj["uuid"] = u
	return nil
}

func edaStreamOut(c *Ctx, o Obj) (Obj, error) {
	typ := ""
	if cid, ok := api.ID(o["credential"]); ok {
		if err := c.DB().QueryRow(c, `SELECT coalesce(ct.namespace, '') FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id
			WHERE cr.id = $1`, cid).Scan(&typ); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	return edaCommon(o, Obj{"name": o["name"], "test_mode": o["test_mode"], "additional_data_headers": o["additional_data_headers"],
		"organization_id": o["organization"], "eda_credential_id": o["credential"], "event_stream_type": strings.TrimPrefix(typ, "event_stream_"),
		"uuid": o["uuid"], "url": strings.TrimRight(c.App.Cfg.BaseURL, "/") + edaReceiverRoot + str(o["uuid"]) + "/post/",
		"events_received": o["last_sequence"], "last_event_received_at": o["last_event_at"]}), nil
}

// EDA's rulebooks are a project's rulebooks, not the copies that
// activations import.

const edaNotActivation = "NOT EXISTS (SELECT 1 FROM eda_activations a WHERE a.rulebook_id = t.id)"

func edaRulebooks() *edaSpec {
	return &edaSpec{
		path: "rulebooks", res: "rulebooks", readOnly: true,
		filters: map[string]string{"name": "name__icontains", "project_id": "project"},
		where:   func(c *Ctx, arg ArgFunc) string { return edaNotActivation },
		out: func(c *Ctx, o Obj) (Obj, error) {
			return edaCommon(o, Obj{"name": o["name"], "description": o["description"], "rulesets": o["rulesets"],
				"project_id": o["project"], "organization_id": o["organization"]}), nil
		},
	}
}

func installEDARulebookRoutes(app *api.App) {
	app.Handle("GET", edaRoot+"rulebooks/{id}/sources/", edaRulebookSources)
}

// rulebookHash is EDA's hash of a rulebook: its text's SHA-256.
func rulebookHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// edaSourceList is a rulebook's sources as EDA lists them.
func edaSourceList(text string) ([]Obj, error) {
	sources, err := rulebooks.Sources([]byte(text))
	if err != nil {
		return nil, err
	}
	hash := rulebookHash(text)
	out := []Obj{}
	for _, s := range sources {
		info, err := yaml.Marshal(map[string]any{s.Plugin: s.Args})
		if err != nil {
			return nil, err
		}
		out = append(out, Obj{"name": s.Name, "source_info": string(info), "rulebook_hash": hash})
	}
	return out, nil
}

func edaRulebookSources(c *Ctx, w http.ResponseWriter, r *http.Request) {
	obj, err := edaRulebooks().object(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	list, err := edaSourceList(str(obj["rulesets"]))
	if err != nil {
		api.WriteJSON(w, 400, Obj{"errors": err.Error()})
		return
	}
	page, size, err := edaPage(r.URL.Query())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	from := min((page-1)*size, len(list))
	to := min(from+size, len(list))
	writeEDAPage(w, r, int64(len(list)), page, size, list[from:to])
}

// edaFirstError is a validation error's first message, for the messages
// EDA builds from it.
func edaFirstError(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		if body, ok := ae.Body.(Obj); ok {
			for k, v := range body {
				if l, ok := v.([]string); ok && len(l) > 0 {
					return fmt.Sprintf("%s: %s", k, l[0])
				}
				return fmt.Sprintf("%s: %v", k, v)
			}
		}
	}
	return err.Error()
}
