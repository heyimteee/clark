package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginLockoutAfterRepeatedFailures(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	// All requests come from httptest's client with no XFF header; they share
	// one source bucket ("192.0.2.1:443"-style RemoteAddr from the transport).
	for i := 0; i < loginMaxFails; i++ {
		code, _ := postJSON(t, ts, "/web/api/login", "", map[string]any{"key": "wrong-key-" + string(rune('a'+i))})
		if code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i+1, code)
		}
	}
	code, _ := postJSON(t, ts, "/web/api/login", "", map[string]any{"key": testWebToken})
	if code != http.StatusTooManyRequests {
		t.Fatalf("correct key during lockout = %d, want 429", code)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, _ := postJSON(t, ts, "/web/api/logout", tok, nil)
	if code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", code)
	}
	if code, _ := getJSON(t, ts, "/web/api/state", tok); code != http.StatusUnauthorized {
		t.Fatalf("state after logout = %d, want 401 (token revoked)", code)
	}
}

func TestSecurityHeadersOnAllResponses(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/web/")
	if err != nil {
		t.Fatalf("get spa: %v", err)
	}
	resp.Body.Close()
	checkSecurityHeaders(t, resp.Header, false)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/web/api/state", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("state unauth: %v", err)
	}
	resp2.Body.Close()
	checkSecurityHeaders(t, resp2.Header, false)
}

func checkSecurityHeaders(t *testing.T, h http.Header, behindTLSProxy bool) {
	t.Helper()
	for name, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP missing default-src 'self': %q", csp)
	}
	hsts := h.Get("Strict-Transport-Security")
	if behindTLSProxy && hsts == "" {
		t.Error("HSTS missing when X-Forwarded-Proto is https")
	}
	if !behindTLSProxy && hsts != "" {
		t.Errorf("HSTS set without https: %q", hsts)
	}
}

func TestSecurityHeadersHSTSBehindProxy(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := withSecurityHeaders(inner)
	req, _ := http.NewRequest(http.MethodGet, "http://clark.example/web/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	checkSecurityHeaders(t, rec.Header(), true)
}

func TestSessionAbsoluteLifetime(t *testing.T) {
	st := testStore(t)
	llm := &stubLLM{}
	ast := newAssistant(t, st, llm)
	srv := New(Options{
		ListenAddr:     ":0",
		WebToken:       testWebToken,
		Butler:         ast,
		Store:          st,
		STTModel:       "whisper-turbo",
		TTSEngine:      "kokoro-remote",
		SessionTTL:     time.Hour,
		SessionMaxLife: 100 * time.Millisecond,
		Voice:          voiceEngine(),
	})
	ts := newServerFor(t, srv)

	tok := login(t, ts)
	time.Sleep(150 * time.Millisecond)
	// Activity used to slide the TTL forward; the absolute cap must still win.
	if code, _ := getJSON(t, ts, "/web/api/state", tok); code != http.StatusUnauthorized {
		t.Fatalf("state after absolute lifetime = %d, want 401", code)
	}
}
