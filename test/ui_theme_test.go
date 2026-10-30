package test

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Dark mode (roadmap M7-13) is applied before the first paint by
// ui/public/theme.js. The page's Content-Security-Policy allows only
// same-origin scripts, so the UI's page must load it, and every script, from
// a file: an inline script would be blocked and the page would flash light.
func TestUIThemeScriptAllowedByCSP(t *testing.T) {
	resp, err := http.Get(srv.URL + "/templates")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "script-src") {
		t.Fatalf("the page's CSP: %q", csp)
	}

	index, err := os.ReadFile("../ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(page, -1) {
		if !strings.Contains(tag, `src="/`) {
			t.Errorf("a script the CSP blocks (inline or from elsewhere): %s", tag)
		}
	}
	theme := strings.Index(page, `<script src="/theme.js"></script>`)
	if theme < 0 || theme > strings.Index(page, "</head>") || theme > strings.Index(page, "/src/main.tsx") {
		t.Fatalf("theme.js is not loaded in the head, before the app:\n%s", page)
	}
	if _, err := os.Stat("../ui/public/theme.js"); err != nil {
		t.Fatal(err)
	}
}
