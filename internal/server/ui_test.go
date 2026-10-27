package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A UI served from disk is re-read on each request; a browser holding the
// previous build must get the new page, not "304 Not Modified".
func TestUIFromDirServesRebuiltIndex(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "index.html")
	if err := os.WriteFile(index, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(index, old, old); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(dir)

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest("GET", "/templates", nil))
	stamp := first.Header().Get("Last-Modified")
	if first.Code != http.StatusOK || first.Body.String() != "old" || stamp == "" {
		t.Fatalf("first load: %d %q, Last-Modified %q", first.Code, first.Body.String(), stamp)
	}

	if err := os.WriteFile(index, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/templates", nil)
	req.Header.Set("If-Modified-Since", stamp)
	again := httptest.NewRecorder()
	h.ServeHTTP(again, req)
	if again.Code != http.StatusOK || again.Body.String() != "new" {
		t.Fatalf("after rebuild: %d %q, want 200 \"new\"", again.Code, again.Body.String())
	}
}
