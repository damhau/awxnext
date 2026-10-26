package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// versionsRes lists object_versions rows; access is checked on the versioned object.
var versionsRes = Register(&Resource{
	Name:         "object_versions",
	Type:         "object_version",
	Table:        "object_versions",
	Verbose:      "Object Version",
	NoTimestamps: true,
	NoName:       true,
	ReadOnly:     true,
	NoActivity:   true,
	Ordering:     "-version",
	SearchFields: []string{"comment"},
	Fields: []*Field{
		{Name: "created", Type: TDateTime, ReadOnly: true},
		{Name: "resource", Type: TString, ReadOnly: true},
		{Name: "object_id", Type: TInt, ReadOnly: true},
		{Name: "version", Type: TInt, ReadOnly: true},
		{Name: "operation", Type: TString, ReadOnly: true},
		{Name: "actor", Type: TFK, Ref: "users", ReadOnly: true},
		{Name: "changes", Type: TJSON, ReadOnly: true},
		{Name: "comment", Type: TString, ReadOnly: true},
	},
	View: func(c *Ctx, arg ArgFunc) string {
		if c.User.Superuser || c.User.Auditor {
			return "TRUE"
		}
		return "FALSE"
	},
	Serialize: func(c *Ctx, o Obj) {
		if res := registry[toStr(o["resource"])]; res != nil {
			id, _ := ID(o["object_id"])
			v, _ := ID(o["version"])
			o["url"] = fmt.Sprintf("%shistory/%d/", res.URL(id), v)
			rel := o["related"].(Obj)
			rel["object"] = res.URL(id)
			rel["revert"] = fmt.Sprintf("%shistory/%d/revert/", res.URL(id), v)
		}
	},
})

// snapshot renders a raw object as JSON-safe data for storage.
func snapshot(c *Ctx, res *Resource, obj Obj) (Obj, error) {
	data := Obj{}
	for _, f := range res.Fields {
		v := obj[f.Name]
		if f.Name == "password" || f.Virtual {
			continue
		}
		data[f.Name] = renderStored(f, v)
	}
	if res.VersionExtra != nil {
		extra, err := res.VersionExtra(c, obj)
		if err != nil {
			return nil, err
		}
		for k, v := range extra {
			data[k] = v
		}
	}
	return data, nil
}

// renderStored keeps secrets in their encrypted form so a revert restores them.
func renderStored(f *Field, v any) any {
	if f.Type == TSecret {
		return v
	}
	return renderValue(f, v)
}

func recordVersion(c *Ctx, res *Resource, obj Obj, op string, changes Obj) error {
	if !res.Versioned {
		return nil
	}
	data, err := snapshot(c, res, obj)
	if err != nil {
		return err
	}
	id, _ := ID(obj["id"])
	var actor *int64
	if !c.User.Anonymous() {
		actor = &c.User.UserID
	}
	comment, _ := c.Extra["version_comment"].(string)
	if v, ok := c.Extra["version_op"].(string); ok && op == "update" {
		op = v
	}
	if changes == nil {
		changes = Obj{}
	}
	_, err = c.DB().Exec(c, `INSERT INTO object_versions (resource, object_id, version, operation, actor_id, data, changes, comment)
		VALUES ($1, $2, (SELECT coalesce(max(version), 0) + 1 FROM object_versions WHERE resource = $1 AND object_id = $2), $3, $4, $5, $6, $7)`,
		res.Name, id, op, actor, data, changes, comment)
	return err
}

// RecordVersion snapshots an object after a change made outside the generic engine.
func RecordVersion(c *Ctx, res *Resource, id int64, op string, changes Obj) error {
	if !res.Versioned {
		return nil
	}
	obj, err := Get(c, res, id)
	if err != nil {
		return err
	}
	return recordVersion(c, res, obj, op, changes)
}

func (a *App) historyRoutes(r chi.Router) {
	r.Get("/api/v2/{res}/{id}/history/{version}/", a.wrap(a.handleVersion))
	r.Post("/api/v2/{res}/{id}/history/{version}/revert/", a.wrap(a.handleRevert))
}

func (a *App) handleHistory(c *Ctx, w http.ResponseWriter, r *http.Request, res *Resource, obj Obj) {
	id, _ := ID(obj["id"])
	WriteList(c, w, r, versionsRes, ListParams{SkipView: true, Extra: []func(ArgFunc) string{func(arg ArgFunc) string {
		return fmt.Sprintf("t.resource = %s AND t.object_id = %s", arg(res.Name), arg(id))
	}}})
}

func (a *App) loadVersion(c *Ctx, r *http.Request) (*Resource, Obj, int64, Obj, error) {
	res, obj, err := a.objectFromPath(c, r)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	if !res.Versioned {
		return nil, nil, 0, nil, ErrNotFound
	}
	v, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 64)
	if err != nil {
		return nil, nil, 0, nil, ErrNotFound
	}
	id, _ := ID(obj["id"])
	var vid int64
	var data []byte
	if err := c.DB().QueryRow(c, "SELECT id, data FROM object_versions WHERE resource = $1 AND object_id = $2 AND version = $3",
		res.Name, id, v).Scan(&vid, &data); err != nil {
		return nil, nil, 0, nil, ErrNotFound
	}
	var snap Obj
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, nil, 0, nil, err
	}
	return res, obj, vid, snap, nil
}

func (a *App) handleVersion(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, _, vid, snap, err := a.loadVersion(c, r)
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := Get(c, versionsRes, vid)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := SerializeOne(c, versionsRes, row)
	if err != nil {
		writeError(w, err)
		return
	}
	// Secrets are never returned, even in their encrypted form.
	for _, f := range res.Fields {
		if f.Redact != nil {
			if v, ok := snap[f.Name]; ok {
				snap[f.Name] = f.Redact(v)
			}
		} else if f.Type == TSecret {
			if s, _ := snap[f.Name].(string); s != "" {
				snap[f.Name] = "$encrypted$"
			}
		}
	}
	out["data"] = snap
	writeJSON(w, 200, out)
}

func (a *App) handleRevert(c *Ctx, w http.ResponseWriter, r *http.Request) {
	res, obj, _, snap, err := a.loadVersion(c, r)
	if err != nil {
		writeError(w, err)
		return
	}
	if res.ReadOnly {
		writeError(w, Detail(405, `Method "POST" not allowed.`))
		return
	}
	data := Obj{}
	for _, f := range res.Fields {
		if f.ReadOnly || f.CreateOnly || f.Name == "password" {
			continue
		}
		if v, ok := snap[f.Name]; ok {
			data[f.Name] = v
		}
	}
	c.Extra["version_op"] = "revert"
	c.Extra["version_comment"] = "Reverted to version " + chi.URLParam(r, "version")
	var updated Obj
	err = c.WithTx(func(c *Ctx) error {
		vals, err := Validate(c, res, data, false, obj)
		if err != nil {
			return err
		}
		updated, err = Apply(c, res, obj, vals)
		if err != nil {
			return err
		}
		if res.RevertExtra != nil {
			if err := res.RevertExtra(c, updated, snap); err != nil {
				return err
			}
			id, _ := ID(updated["id"])
			// Capture restored relations in a final snapshot.
			return RecordVersion(c, res, id, "revert", Obj{})
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := SerializeOne(c, res, updated)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
