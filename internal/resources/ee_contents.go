package resources

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/eecontents"
)

// eeContentsTimeout bounds a read (downloading a large image's layers).
const eeContentsTimeout = 30 * time.Minute

// eeContentsAction is execution_environments/N/contents/ (awxnext's): GET
// the image's contents as last read, POST to read them again, in the
// background. A read reuses the contents of the same digest when they were
// read before, for this environment or another.
func eeContentsAction() *api.Action {
	return &api.Action{Name: "contents", Methods: []string{"GET", "POST"}, Perm: "view", Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		id, _ := api.ID(obj["id"])
		if r.Method == "POST" {
			// One read at a time: only the request that sets the row running
			// starts one (a read left running too long can be replaced).
			tag, err := c.DB().Exec(c, `INSERT INTO execution_environment_contents (execution_environment_id, image, status, started)
				VALUES ($1, $2, 'running', now()) ON CONFLICT (execution_environment_id) DO UPDATE
				SET image = $2, status = 'running', error = '', started = now(), finished = NULL
				WHERE execution_environment_contents.status <> 'running' OR execution_environment_contents.started < now() - $3::int * interval '1 second'`,
				id, str(obj["image"]), int((eeContentsTimeout + 5*time.Minute).Seconds()))
			if err != nil {
				api.WriteError(w, err)
				return
			}
			if tag.RowsAffected() > 0 {
				var cred *int64
				if v, ok := api.ID(obj["credential"]); ok {
					cred = &v
				}
				go readEEContents(c.App, id, str(obj["image"]), cred)
			}
			api.WriteJSON(w, 202, eeContentsRow(c, id))
			return
		}
		row := eeContentsRow(c, id)
		row["current_image"] = obj["image"]
		api.WriteJSON(w, 200, row)
	}}
}

// eeContentsRow is the environment's last read: status "none" before any.
func eeContentsRow(c *Ctx, id int64) Obj {
	var image, digest, status, errText string
	var contents []byte
	var started time.Time
	var finished *time.Time
	err := c.DB().QueryRow(c, `SELECT image, digest, status, error, contents, started, finished FROM execution_environment_contents
		WHERE execution_environment_id = $1`, id).Scan(&image, &digest, &status, &errText, &contents, &started, &finished)
	if err != nil {
		return Obj{"status": "none"}
	}
	// A read left running by a node that stopped.
	if status == "running" && time.Since(started) > eeContentsTimeout+5*time.Minute {
		status, errText = "failed", "The read did not finish: the node reading the image may have stopped. Read it again."
	}
	out := Obj{"status": status, "image": image, "digest": digest, "error": errText, "started": started, "finished": finished, "contents": nil}
	if len(contents) > 0 {
		var v any
		if json.Unmarshal(contents, &v) == nil {
			out["contents"] = v
		}
	}
	return out
}

// readEEContents reads the image's contents and stores the outcome.
func readEEContents(app *api.App, id int64, image string, cred *int64) {
	ctx, cancel := context.WithTimeout(context.Background(), eeContentsTimeout)
	defer cancel()
	finish := func(digest string, contents *eecontents.Contents, err error) {
		status, errText := "successful", ""
		var body []byte
		if err != nil {
			status, errText = "failed", err.Error()
		} else {
			body, _ = json.Marshal(contents)
		}
		if _, err := app.DB.Exec(context.Background(), `UPDATE execution_environment_contents SET digest = $2, status = $3, error = $4,
			contents = $5, finished = now() WHERE execution_environment_id = $1 AND image = $6`, id, digest, status, errText, body, image); err != nil {
			slog.Warn("storing execution environment contents", "id", id, "err", err)
		}
	}
	reg := eecontents.Registry{}
	if cred != nil {
		rc, err := ResolveCredential(ctx, app, *cred)
		if err != nil {
			finish("", nil, err)
			return
		}
		reg.Username, _ = rc.Inputs["username"].(string)
		reg.Password, _ = rc.Inputs["password"].(string)
		if v, ok := rc.Inputs["verify_ssl"].(bool); ok && !v {
			reg.Insecure = true
		}
	}
	digest, err := eecontents.Digest(ctx, image, reg)
	if err != nil {
		finish("", nil, err)
		return
	}
	// Read before for this digest: the same files.
	var known []byte
	app.DB.QueryRow(ctx, `SELECT contents FROM execution_environment_contents WHERE digest = $1 AND status = 'successful'
		AND contents IS NOT NULL ORDER BY finished DESC LIMIT 1`, digest).Scan(&known)
	if len(known) > 0 {
		var c eecontents.Contents
		if json.Unmarshal(known, &c) == nil {
			c.Image = image
			finish(digest, &c, nil)
			return
		}
	}
	contents, err := eecontents.Read(ctx, image, reg)
	if err != nil {
		finish(digest, nil, err)
		return
	}
	finish(contents.Digest, contents, nil)
}
