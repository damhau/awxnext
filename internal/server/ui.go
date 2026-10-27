package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/damhau/awxnext/ui"
)

// uiHandler serves the web UI: static assets, and index.html for every other
// path so client-side routes survive reloads.
func uiHandler(dir string) http.Handler {
	var files fs.FS
	if dir != "" {
		files = os.DirFS(dir)
	} else if sub, err := fs.Sub(ui.Dist, "dist"); err == nil {
		files = sub
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		index = []byte(`<!doctype html><meta charset="utf-8"><title>awxnext</title>
<p style="font-family:sans-serif">The web UI is not built into this binary. Run <code>make ui</code> and rebuild, or use the API at <a href="/api/v2/">/api/v2/</a>.</p>`)
	}
	modTime := time.Now()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(p, "api/") || p == "api" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			w.Write([]byte(`{"detail":"Not found."}`))
			return
		}
		if p != "" && p != "index.html" && files != nil {
			if f, err := files.Open(p); err == nil {
				st, _ := f.Stat()
				f.Close()
				if st != nil && !st.IsDir() {
					if strings.HasPrefix(p, "assets/") {
						// Asset names carry a content hash.
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					http.ServeFileFS(w, r, files, p)
					return
				}
			}
			if path.Ext(p) != "" && !strings.Contains(path.Base(p), "@") {
				http.NotFound(w, r)
				return
			}
		}
		page, mod := index, modTime
		if dir != "" {
			// Development: pick up rebuilt assets without a restart, and date
			// the page by its file so browsers do not keep a stale copy.
			if b, err := fs.ReadFile(files, "index.html"); err == nil {
				page = b
			}
			if st, err := fs.Stat(files, "index.html"); err == nil {
				mod = st.ModTime()
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		http.ServeContent(w, r, "index.html", mod, bytes.NewReader(page))
	})
}
