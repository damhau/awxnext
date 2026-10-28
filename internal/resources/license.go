package resources

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	"github.com/damhau/awxnext/internal/api"
)

// openLicense is what upstream AWX's OpenLicense reports: AWX without a
// subscription.
var openLicense = Obj{"license_type": "open", "valid_key": true, "subscription_name": "OPEN", "product_name": "AWX"}

// installLicense answers the subscription endpoints as an upstream AWX does,
// where the licenser is OpenLicense: nothing can be attached, so the
// collection's license and subscriptions modules behave the same.
func installLicense(app *api.App) {
	app.Handle("POST", "/api/v2/config/", superuserOnly(postConfig))
	app.Handle("DELETE", "/api/v2/config/", superuserOnly(func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	app.Handle("POST", "/api/v2/config/subscriptions/", superuserOnly(func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		licenseError(w, "Invalid Subscription")
	}))
	app.Handle("POST", "/api/v2/config/attach/", superuserOnly(func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		body, _ := api.ReadBody(r)
		if str(body["pool_id"]) == "" {
			licenseError(w, "No subscription pool ID provided.")
			return
		}
		licenseError(w, "Invalid Subscription")
	}))
}

func superuserOnly(h func(c *Ctx, w http.ResponseWriter, r *http.Request)) func(c *Ctx, w http.ResponseWriter, r *http.Request) {
	return func(c *Ctx, w http.ResponseWriter, r *http.Request) {
		if !c.User.Superuser {
			api.WriteError(w, api.ErrForbidden)
			return
		}
		h(c, w, r)
	}
}

func licenseError(w http.ResponseWriter, msg string) {
	api.WriteJSON(w, 400, Obj{"error": msg})
}

// postConfig is AWX's ApiV2ConfigView.post with OpenLicense: a manifest
// cannot be read (upstream AWX has no Red Hat CA to verify it with), and
// without one the open license is valid and TOWER_URL_BASE is set from the
// request.
func postConfig(c *Ctx, w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		licenseError(w, "Invalid subscription data")
		return
	}
	const legacy = "Legacy license submitted. A subscription manifest is now required."
	if _, ok := data["license_key"]; ok {
		licenseError(w, legacy)
		return
	}
	if m, ok := data["manifest"]; ok {
		decoded, _ := base64.StdEncoding.DecodeString(str(m))
		var old map[string]any
		if json.Unmarshal(decoded, &old) == nil && old["license_key"] != nil {
			licenseError(w, legacy)
			return
		}
		z, err := zip.NewReader(bytes.NewReader(decoded), int64(len(decoded)))
		if err != nil {
			licenseError(w, "Invalid manifest: a subscription manifest zip file is required.")
			return
		}
		files := map[string]bool{}
		for _, f := range z.File {
			files[f.Name] = true
		}
		if !files["consumer_export.zip"] || !files["signature"] {
			licenseError(w, "Invalid manifest: missing required files.")
			return
		}
		licenseError(w, "Invalid manifest submitted.")
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	if err := c.App.Settings.Set(c, "TOWER_URL_BASE", scheme+"://"+r.Host); err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, openLicense)
}
