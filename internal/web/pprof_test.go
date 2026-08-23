package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPprofDisabledByDefault(t *testing.T) {
	if pprofEnabled() {
		t.Fatal("pprof enabled without CLARK_PPROF")
	}
}

func TestPprofGate(t *testing.T) {
	t.Setenv("CLARK_PPROF", "1")
	if !pprofEnabled() {
		t.Fatal("CLARK_PPROF=1 not honored")
	}
	h := pprofHandler()
	if h == nil {
		t.Fatal("nil handler when enabled")
	}
	// Loopback-only binding is enforced by the caller; assert handler serves.
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/debug/pprof/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pprof index = %d", rec.Code)
	}
}
