package auth

import (
	"fmt"
	"net/http"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/settings"
)

// formSettings are a settings page's values not saved yet, over the saved
// ones: what the map tester and the connection tests run with, so a
// configuration is tried before it is saved.
type formSettings struct {
	c     *api.Ctx
	given map[string]any
	// plain are the secrets given in clear, not encrypted as saved ones are.
	plain map[string]bool
}

// readFormSettings reads the request's "settings": only keys allowed
// accepts, each checked as saving it would be. A secret left as it is
// ($encrypted$, as the API shows a saved one) is the saved value. On a bad
// value the error is written and ok is false.
func readFormSettings(c *api.Ctx, w http.ResponseWriter, data map[string]any, allowed func(string) bool, what string) (f *formSettings, ok bool) {
	f = &formSettings{c: c, given: map[string]any{}, plain: map[string]bool{}}
	v, has := data["settings"]
	if !has || v == nil {
		return f, true
	}
	m, isMap := normalizeJSON(v).(map[string]any)
	if !isMap {
		api.WriteError(w, api.FieldError("settings", "Expected an object of setting values."))
		return nil, false
	}
	for k, x := range m {
		d := settings.Lookup(k)
		if d == nil || !allowed(k) {
			api.WriteError(w, api.FieldError("settings", fmt.Sprintf("%s is not a %s.", k, what)))
			return nil, false
		}
		if d.Secret {
			s, _ := x.(string)
			if s == "$encrypted$" {
				continue
			}
			if s != "" {
				f.plain[s] = true
			}
		}
		if d.Validate != nil && x != nil {
			if err := d.Validate(x); err != nil {
				api.WriteError(w, api.FieldError(k, err.Error()))
				return nil, false
			}
		}
		f.given[k] = x
	}
	return f, true
}

// get is the value given, else the saved one.
func (f *formSettings) get(k string) any {
	if v, ok := f.given[k]; ok {
		return v
	}
	return f.c.App.Settings.Get(f.c, k)
}

// decrypt reads a secret: as given in clear, or decrypted as saved.
func (f *formSettings) decrypt(s string) (string, error) {
	if f.plain[s] {
		return s, nil
	}
	return f.c.App.Crypto.Decrypt(s)
}
