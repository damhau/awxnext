package test

import (
	"strings"
	"testing"
)

// The About dialog (roadmap M7-15) reads config/: the AWX version that
// clients compare against, and awxnext's own version and build beside it.
func TestAboutVersions(t *testing.T) {
	a := adminClient(t)
	r := must(t, a.get("config/"), 200)
	if r.Body["version"] != "24.6.1" {
		t.Fatalf("the AWX version clients see: %v", r.Body["version"])
	}
	own, _ := r.Body["awxnext"].(map[string]any)
	goVersion, _ := own["go"].(string)
	if own == nil || own["version"] == "" || !strings.HasPrefix(goVersion, "go1.") {
		t.Fatalf("awxnext's version: %v", r.Body["awxnext"])
	}
	for _, k := range []string{"commit", "built", "modified"} {
		if _, ok := own[k]; !ok {
			t.Errorf("config/ awxnext has no %s: %v", k, own)
		}
	}
	// Any user may read it, as in AWX; not anonymously.
	user, _ := newUser(t, a)
	must(t, user.get("config/"), 200)
	must(t, (&client{t: t}).get("config/"), 401)
}
