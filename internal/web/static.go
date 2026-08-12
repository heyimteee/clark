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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
