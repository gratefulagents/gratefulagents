package dashboard

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// webDistEnv optionally points at a built web UI (platform-app's web/dist).
// The UI is no longer embedded in the binary; without this the dashboard is
// API-only and non-API routes get a small placeholder page.
const webDistEnv = "DASHBOARD_WEB_DIST"

const placeholderPage = `<!doctype html>
<html>
<head><meta charset="utf-8"><title>Operator</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; line-height: 1.5">
<h1>Operator backend is running</h1>
<p>This build does not bundle the web UI. Use the desktop app, run the
frontend dev server from platform-app/, or set
<code>DASHBOARD_WEB_DIST</code> to a built <code>web/dist</code> directory.</p>
</body>
</html>
`

// StaticHandler serves the web UI with SPA fallback when DASHBOARD_WEB_DIST
// points at a built frontend; otherwise it serves a placeholder page.
func StaticHandler() http.Handler {
	dir := os.Getenv(webDistEnv)
	if dir == "" {
		return placeholderHandler()
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return placeholderHandler()
	}

	dist := os.DirFS(dir)
	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to serve the file directly
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		if _, err := fs.Stat(dist, path); err == nil {
			if strings.HasPrefix(path, "assets/") {
				// Vite hashed bundles: content-addressed, safe to cache forever.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if path == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: serve index.html for client-side routing
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

func placeholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholderPage))
	})
}
