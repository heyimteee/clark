package web

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/heyimteee/clark/internal/logging"
)

// staticFS carries the embedded single-page console. There is no build step:
// index.html, app.css, and app.js are served verbatim.
//
//go:embed static
var staticFS embed.FS

// staticSubFS is the embedded static/ directory, suitable for FileServer.
var staticSubFS = func() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("embedded static dir missing: " + err.Error())
	}
	return sub
}()

// handleSPA serves index.html for the console root and any client-side route.
func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(staticFS, "static/index.html")
	if err != nil {
		logging.Log("WEB", logging.SevErr, "SPA", "Embedded index.html missing", "error", err.Error())
		http.Error(w, "console assets unavailable", http.StatusInternalServerError)
		return
	}
	// no-cache: embedded assets carry no validators (no mod time), so without
	// an explicit policy browsers heuristically cache stale JS forever.
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// noCache forces revalidation on every request. Embedded files expose no
// Last-Modified/ETag, so permissive caching would pin old assets on clients
// across deploys.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		next.ServeHTTP(w, r)
	})
}
