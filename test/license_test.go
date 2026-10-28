package test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// awxnext has no subscriptions: the endpoints answer as an upstream AWX
// does with its OpenLicense.
func TestLicenseEndpoints(t *testing.T) {
	a := adminClient(t)
	orig := must(t, a.get("settings/system/"), 200).Body["TOWER_URL_BASE"]
	t.Cleanup(func() { a.patch("settings/system/", map[string]any{"TOWER_URL_BASE": orig}) })
	if li := must(t, a.get("config/"), 200).Body["license_info"].(map[string]any); li["license_type"] != "open" || li["valid_key"] != true {
		t.Errorf("license_info: %v", li)
	}
	bob, _ := newUser(t, a)
	must(t, bob.get("config/"), 200)
	for _, path := range []string{"config/", "config/subscriptions/", "config/attach/"} {
		must(t, bob.post(path, map[string]any{}), 403)
	}
	must(t, bob.delete("config/"), 403)

	zipped := func(names ...string) string {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for _, n := range names {
			w, _ := z.Create(n)
			w.Write([]byte("x"))
		}
		z.Close()
		return base64.StdEncoding.EncodeToString(b.Bytes())
	}
	for _, tc := range []struct {
		body any
		want string
	}{
		{map[string]any{"license_key": "x"}, "Legacy license submitted. A subscription manifest is now required."},
		{map[string]any{"manifest": base64.StdEncoding.EncodeToString([]byte(`{"license_key": "x"}`))}, "Legacy license submitted."},
		{map[string]any{"manifest": base64.StdEncoding.EncodeToString([]byte("not a zip"))}, "Invalid manifest: a subscription manifest zip file is required."},
		{map[string]any{"manifest": zipped("other")}, "Invalid manifest: missing required files."},
		{map[string]any{"manifest": zipped("consumer_export.zip", "signature")}, "Invalid manifest submitted."},
		{[]any{1}, "Invalid subscription data"},
	} {
		if r := must(t, a.post("config/", tc.body), 400); !strings.Contains(r.Body["error"].(string), tc.want) {
			t.Errorf("%v: %s", tc.body, r.Raw)
		}
	}
	// Without a manifest the open license is valid, and the request sets the URL base.
	if r := must(t, a.post("config/", map[string]any{}), 200); r.Body["license_type"] != "open" {
		t.Errorf("open license: %s", r.Raw)
	}
	if base := must(t, a.get("settings/system/"), 200).Body["TOWER_URL_BASE"].(string); !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Errorf("TOWER_URL_BASE: %q", base)
	}
	must(t, a.delete("config/"), 204)
	if r := must(t, a.post("config/subscriptions/", map[string]any{"subscriptions_username": "u", "subscriptions_password": "p"}), 400); r.Body["error"] != "Invalid Subscription" {
		t.Errorf("subscriptions: %s", r.Raw)
	}
	if r := must(t, a.post("config/attach/", map[string]any{}), 400); r.Body["error"] != "No subscription pool ID provided." {
		t.Errorf("attach: %s", r.Raw)
	}
	must(t, a.post("config/attach/", map[string]any{"pool_id": "1"}), 400)
}
