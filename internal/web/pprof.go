package web

import (
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on the returned mux
	"os"
)

// pprofEnabled reports whether the operator opted into the profiling
// endpoint via CLARK_PPROF. Off by default: it is a diagnostic surface, not
// a production feature.
func pprofEnabled() bool {
	v := os.Getenv("CLARK_PPROF")
	return v == "1" || v == "true" || v == "on"
}

// pprofHandler returns the default-mux-backed pprof handler (nil when
// disabled). Callers must bind it to loopback only, never the public mux.
func pprofHandler() http.Handler {
	if !pprofEnabled() {
		return nil
	}
	return http.DefaultServeMux
}
