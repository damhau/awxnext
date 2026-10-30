package resources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/damhau/awxnext/internal/api"
)

// A user's UI preferences, awxnext's /api/v2/me/preferences/: GET them all,
// PATCH some (a null value removes one).
var prefKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

const (
	maxPrefValue = 64 << 10
	maxPrefs     = 100
)

func installPreferences(app *api.App) {
	app.Handle("GET", "/api/v2/me/preferences/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		if c.User.Anonymous() {
			api.WriteError(w, api.ErrForbidden)
			return
		}
		api.WriteJSON(w, 200, userPreferences(c))
	})
	app.Handle("PATCH", "/api/v2/me/preferences/", func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		if c.User.Anonymous() {
			api.WriteError(w, api.ErrForbidden)
			return
		}
		body, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		fe := api.FieldErrors{}
		values := map[string][]byte{}
		for k, v := range body {
			if !prefKey.MatchString(k) {
				fe.Add(k, "Not a valid preference name: lowercase letters, digits, '.', '_' and '-', up to 64.")
				continue
			}
			if v == nil {
				values[k] = nil
				continue
			}
			b, err := json.Marshal(v)
			if err != nil || len(b) > maxPrefValue {
				fe.Add(k, fmt.Sprintf("Too large: at most %d KiB.", maxPrefValue>>10))
				continue
			}
			values[k] = b
		}
		if err := fe.Err(); err != nil {
			api.WriteError(w, err)
			return
		}
		tx, err := c.App.DB.Begin(c)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		defer tx.Rollback(c)
		for k, b := range values {
			if b == nil {
				_, err = tx.Exec(c, "DELETE FROM user_preferences WHERE user_id = $1 AND key = $2", c.User.UserID, k)
			} else {
				_, err = tx.Exec(c, `INSERT INTO user_preferences (user_id, key, value) VALUES ($1, $2, $3)
					ON CONFLICT (user_id, key) DO UPDATE SET value = $3, modified = now()`, c.User.UserID, k, b)
			}
			if err != nil {
				api.WriteError(w, err)
				return
			}
		}
		var n int
		tx.QueryRow(c, "SELECT count(*) FROM user_preferences WHERE user_id = $1", c.User.UserID).Scan(&n)
		if n > maxPrefs {
			api.WriteError(w, api.BadRequest(fmt.Sprintf("At most %d preferences per user.", maxPrefs)))
			return
		}
		if err := tx.Commit(c); err != nil {
			api.WriteError(w, err)
			return
		}
		api.WriteJSON(w, 200, userPreferences(c))
	})
}

func userPreferences(c *Ctx) Obj {
	out := Obj{}
	rows, err := c.DB().Query(c, "SELECT key, value FROM user_preferences WHERE user_id = $1 ORDER BY key", c.User.UserID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var b []byte
		if rows.Scan(&k, &b) == nil {
			var v any
			if json.Unmarshal(b, &v) == nil {
				out[k] = v
			}
		}
	}
	return out
}
