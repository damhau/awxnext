package resources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/logship"
	"github.com/damhau/awxnext/internal/settings"
	"github.com/go-chi/chi/v5"
)

func installSettings(app *api.App) {
	app.Handle("GET", "/api/v2/settings/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		var results []Obj
		for _, cat := range settings.Categories {
			results = append(results, Obj{"url": "/api/v2/settings/" + cat.Slug + "/", "slug": cat.Slug, "name": cat.Name})
		}
		api.WriteJSON(w, 200, Obj{"count": len(results), "next": nil, "previous": nil, "results": results})
	})
	// awxkit reads it to build `awx settings`.
	app.Handle("OPTIONS", "/api/v2/settings/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		field := func(label, help string) Obj {
			return Obj{"type": "string", "label": label, "help_text": help, "filterable": false}
		}
		api.WriteJSON(w, 200, Obj{"name": "Setting Category List", "description": "", "renders": []string{"application/json"}, "parses": []string{"application/json"},
			"actions": Obj{"GET": Obj{
				"url":  field("Url", "URL of the category's settings."),
				"slug": field("Slug", "Name of the category in URLs."),
				"name": field("Name", "Name of the category."),
			}}})
	})
	app.Handle("POST", "/api/v2/settings/logging/test/", loggingTest)
	app.Handle("GET", "/api/v2/settings/{cat}/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		defs, err := settingsCategory(c, chi.URLParam(r, "cat"), false)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		api.WriteJSON(w, 200, settingsValues(c, chi.URLParam(r, "cat"), defs))
	})
	update := func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		cat := chi.URLParam(r, "cat")
		defs, err := settingsCategory(c, cat, true)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		allowed := map[string]*settings.Def{}
		for _, d := range defs {
			allowed[d.Key] = d
		}
		data, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		fe := api.FieldErrors{}
		for k, v := range data {
			d, ok := allowed[k]
			if !ok {
				continue
			}
			if d.ReadOnly {
				fe.Add(k, "This setting is read-only.")
				continue
			}
			// As AWX: the collection sends null for an omitted value.
			if v == nil && !d.Nullable {
				fe.Add(k, "This field may not be null.")
				continue
			}
			if err := validateSetting(d, v); err != nil {
				fe.Add(k, err.Error())
			} else if d.Validate != nil {
				if err := d.Validate(normalize(v)); err != nil {
					fe.Add(k, err.Error())
				}
			}
		}
		if err := fe.Err(); err != nil {
			api.WriteError(w, err)
			return
		}
		if cat == "logging" || cat == "all" {
			if err := checkLogging(c, data); err != nil {
				api.WriteError(w, err)
				return
			}
		}
		if cat == "jobs" || cat == "all" {
			if err := checkArchiveCredential(c, data); err != nil {
				api.WriteError(w, err)
				return
			}
		}
		streamWasOn := c.App.Settings.Bool(c, "ACTIVITY_STREAM_ENABLED")
		changes := Obj{}
		for k, v := range data {
			d, ok := allowed[k]
			if !ok {
				continue
			}
			if d.Secret {
				s, _ := v.(string)
				if s == crypto.Placeholder {
					continue
				}
				enc, err := c.App.Crypto.Encrypt(s)
				if err != nil {
					api.WriteError(w, err)
					return
				}
				v = enc
				changes[k] = crypto.Placeholder
			} else {
				changes[k] = v
			}
			if err := c.App.Settings.Set(c, k, v); err != nil {
				api.WriteError(w, err)
				return
			}
		}
		// Turning the activity stream off is itself recorded.
		if len(changes) > 0 && (streamWasOn || c.App.Settings.Bool(c, "ACTIVITY_STREAM_ENABLED")) {
			if _, err := c.DB().Exec(c, `INSERT INTO activity_stream (actor_id, operation, object1, changes, summary) VALUES ($1, 'update', 'setting', $2, '{}')`, c.User.UserID, changes); err == nil {
				api.ActivityLog(c, "update", "setting", "", "", c.User.Username, changes, Obj{})
			}
		}
		api.WriteJSON(w, 200, settingsValues(c, cat, defs))
	}
	app.Handle("PUT", "/api/v2/settings/{cat}/", update)
	app.Handle("PATCH", "/api/v2/settings/{cat}/", update)
	app.Handle("DELETE", "/api/v2/settings/{cat}/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		defs, err := settingsCategory(c, chi.URLParam(r, "cat"), true)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		for _, d := range defs {
			if !d.ReadOnly {
				c.App.Settings.Reset(c, d.Key)
			}
		}
		w.WriteHeader(204)
	})
	app.Handle("OPTIONS", "/api/v2/settings/{cat}/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		defs, err := settingsCategory(c, chi.URLParam(r, "cat"), false)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		get, put := Obj{}, Obj{}
		for _, d := range defs {
			m := Obj{"type": d.Type, "label": d.Label, "help_text": d.Help, "category": d.Category, "category_slug": d.Category, "defined_in_file": false, "default": d.Default}
			if d.Choices != nil {
				m["choices"] = d.Choices
			}
			get[d.Key] = m
			if !d.ReadOnly {
				pm := Obj{}
				for k, v := range m {
					pm[k] = v
				}
				pm["required"] = false
				put[d.Key] = pm
			}
		}
		actions := Obj{"GET": get}
		if c.User.Superuser {
			actions["PUT"] = put
		}
		api.WriteJSON(w, 200, Obj{"name": "Setting Detail", "description": "", "renders": []string{"application/json"}, "parses": []string{"application/json"}, "actions": actions})
	})
}

func settingsCategory(c *Ctx, slug string, write bool) ([]*settings.Def, error) {
	found := false
	for _, cat := range settings.Categories {
		if cat.Slug == slug {
			found = true
		}
	}
	if !found {
		return nil, api.ErrNotFound
	}
	defs := settings.InCategory(slug)
	if write {
		if !c.User.Superuser {
			return nil, api.ErrForbidden
		}
		return defs, nil
	}
	if c.User.Superuser || c.User.Auditor {
		return defs, nil
	}
	var public []*settings.Def
	for _, d := range defs {
		if d.Public {
			public = append(public, d)
		}
	}
	if len(public) == 0 {
		return nil, api.ErrForbidden
	}
	return public, nil
}

func settingsValues(c *Ctx, slug string, defs []*settings.Def) Obj {
	out := Obj{}
	for _, d := range defs {
		if slug == "changed" && !c.App.Settings.IsSet(c, d.Key) {
			continue
		}
		v := c.App.Settings.Get(c, d.Key)
		if d.Secret {
			if s, _ := v.(string); s != "" {
				v = crypto.Placeholder
			}
		}
		out[d.Key] = v
	}
	return out
}

// checkLogging is AWX's logging_validate: enabling the log aggregator
// needs a host and a type, stored or given.
func checkLogging(c *Ctx, data Obj) error {
	if on, _ := data["LOG_AGGREGATOR_ENABLED"].(bool); !on {
		return nil
	}
	var errs []string
	for _, f := range []struct{ key, what string }{{"LOG_AGGREGATOR_HOST", "host"}, {"LOG_AGGREGATOR_TYPE", "type"}} {
		stored := c.App.Settings.String(c, f.key) != ""
		v, given := data[f.key]
		cleared := given && (v == nil || v == "")
		if (!stored && (!given || cleared)) || (stored && cleared) {
			errs = append(errs, "Cannot enable log aggregator without providing "+f.what+".")
		}
	}
	if len(errs) > 0 {
		return &api.Error{Status: 400, Body: Obj{"non_field_errors": []string{strings.Join(errs, "\n")}}}
	}
	return nil
}

// checkArchiveCredential: the job event archive credential is an Amazon Web
// Services or Microsoft Azure Resource Manager credential.
func checkArchiveCredential(c *Ctx, data Obj) error {
	v, ok := data["JOB_EVENTS_ARCHIVE_CREDENTIAL"]
	if !ok || v == nil {
		return nil
	}
	id, _ := api.ID(v)
	var ns *string
	if err := c.DB().QueryRow(c, "SELECT ct.namespace FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1", id).Scan(&ns); err != nil {
		return api.FieldError("JOB_EVENTS_ARCHIVE_CREDENTIAL", fmt.Sprintf("Invalid pk \"%d\" - object does not exist.", id))
	}
	if ns == nil || (*ns != "aws" && *ns != "azure_rm") {
		return api.FieldError("JOB_EVENTS_ARCHIVE_CREDENTIAL", "Choose an Amazon Web Services or Microsoft Azure Resource Manager credential.")
	}
	return nil
}

// loggingTest is AWX's /settings/logging/test/, for system administrators
// and auditors.
func loggingTest(c *Ctx, w http.ResponseWriter, r *http.Request) {
	if !c.User.Superuser && !c.User.Auditor {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	if logship.Default == nil {
		api.WriteJSON(w, 409, Obj{"error": "Logging not enabled"})
		return
	}
	status, body := logship.Default.Test(c)
	if body == nil {
		w.WriteHeader(status)
		return
	}
	api.WriteJSON(w, status, body)
}

func validateSetting(d *settings.Def, v any) error {
	bad := func() error { return errInvalid("Invalid value for a " + d.Type + " setting.") }
	if v == nil {
		return nil
	}
	switch d.Type {
	case "boolean":
		if _, ok := v.(bool); !ok {
			return bad()
		}
	case "integer":
		if _, ok := api.ID(v); !ok {
			return bad()
		}
	case "string":
		if _, ok := v.(string); !ok {
			return bad()
		}
	case "list":
		if _, ok := v.([]any); !ok {
			return bad()
		}
	case "nested object":
		if _, ok := v.(map[string]any); !ok {
			return bad()
		}
	case "choice":
		for _, ch := range d.Choices {
			if ch[0] == v {
				return nil
			}
		}
		return bad()
	}
	return nil
}

type errInvalid string

func (e errInvalid) Error() string { return string(e) }

// normalize converts json.Number values to plain JSON types.
func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal(b, &out)
	return out
}
