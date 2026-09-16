package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/heyimteee/clark/internal/voice"
)

func tailnetTestServer(t *testing.T, mutate func(*Options)) *Server {
	t.Helper()
	st := testStore(t)
	llm := &stubLLM{}
	ast := newAssistant(t, st, llm)
	opts := Options{
		ListenAddr:       ":0",
		WebToken:         testWebToken,
		Butler:           ast,
		Store:            st,
		STTModel:         "whisper-turbo",
		TTSEngine:        "kokoro-remote",
		Voice:            &voice.Engine{},
		TailnetEnabled:   true,
		TailnetAllowCIDR: "100.64.0.0/10",
	}
	if mutate != nil {
		mutate(&opts)
	}
	return New(opts)
}

// doTailnet crafts a request with an explicit TCP peer address, bypassing the
// network so RemoteAddr (not XFF) is fully controlled.
func doTailnet(t *testing.T, srv *Server, remoteAddr string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/web/api/tailnet", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.NewDecoder(rec.Result().Body).Decode(&out)
	return rec.Code, out
}

func TestTailnetLoginDirectTailnetIP(t *testing.T) {
	srv := tailnetTestServer(t, nil)
	ts := newServerFor(t, srv)

	code, out := doTailnet(t, srv, "100.85.1.7:443", nil)
	if code != http.StatusOK {
		t.Fatalf("tailnet login = %d, want 200 (%v)", code, out)
	}
	tok, _ := out["token"].(string)
	if tok == "" {
		t.Fatalf("tailnet login returned no token: %v", out)
	}
	// The minted session must authorize normal API calls.
	if code, _ := getJSON(t, ts, "/web/api/state", tok); code != http.StatusOK {
		t.Fatalf("state with tailnet token = %d, want 200", code)
	}
}

func TestTailnetLoginDisabled(t *testing.T) {
	srv := tailnetTestServer(t, func(o *Options) { o.TailnetEnabled = false })
	code, out := doTailnet(t, srv, "100.85.1.7:443", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("disabled tailnet login = %d, want 401", code)
	}
	if out["token"] != nil {
		t.Fatalf("disabled tailnet login returned a token: %v", out)
	}
}

func TestTailnetLoginPublicIPRejected(t *testing.T) {
	srv := tailnetTestServer(t, nil)
	code, _ := doTailnet(t, srv, "203.0.113.9:443", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("public tailnet login = %d, want 401", code)
	}
}

func TestTailnetLoginIgnoresSpoofedXFF(t *testing.T) {
	srv := tailnetTestServer(t, nil)
	// First-entry XFF parsing would trust 100.64.0.1 here; the tailnet
	// endpoint must not.
	code, _ := doTailnet(t, srv, "203.0.113.9:443", map[string]string{
		"X-Forwarded-For": "100.64.0.1, 203.0.113.9",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("spoofed-XFF tailnet login = %d, want 401", code)
	}
}

func TestTailnetLoginViaNPMRealIP(t *testing.T) {
	srv := tailnetTestServer(t, func(o *Options) { o.NPMpeerCIDR = "127.0.0.0/8" })
	code, out := doTailnet(t, srv, "127.0.0.1:41230", map[string]string{
		"X-Real-IP":       "100.64.0.9",
		"X-Forwarded-For": "203.0.113.9",
	})
	if code != http.StatusOK {
		t.Fatalf("NPM tailnet login = %d, want 200 (%v)", code, out)
	}
	if out["token"] == "" {
		t.Fatalf("NPM tailnet login returned no token: %v", out)
	}
}

func TestTailnetLoginForgedRealIPRejected(t *testing.T) {
	srv := tailnetTestServer(t, func(o *Options) { o.NPMpeerCIDR = "127.0.0.0/8" })
	// X-Real-IP from anyone but the proxy peer is client-controlled noise.
	code, _ := doTailnet(t, srv, "203.0.113.9:443", map[string]string{
		"X-Real-IP": "100.64.0.9",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("forged X-Real-IP tailnet login = %d, want 401", code)
	}
}

func TestTailnetLoginGarbageRealIPFallsBack(t *testing.T) {
	srv := tailnetTestServer(t, func(o *Options) { o.NPMpeerCIDR = "127.0.0.0/8" })
	code, _ := doTailnet(t, srv, "127.0.0.1:41230", map[string]string{
		"X-Real-IP": "not-an-ip",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("garbage X-Real-IP tailnet login = %d, want 401", code)
	}
}

func TestTailnetLoginLockout(t *testing.T) {
	srv := tailnetTestServer(t, nil)
	var code int
	for i := 0; i < loginMaxFails+1; i++ {
		code, _ = doTailnet(t, srv, "203.0.113.9:443", nil)
	}
	if code != http.StatusTooManyRequests {
		t.Fatalf("tailnet login after repeated failures = %d, want 429", code)
	}
}

func TestTrustedClientIP(t *testing.T) {
	proxy := parseIPNet("10.0.0.0/8")
	cases := []struct {
		name   string
		remote string
		want   string
	}{
		{name: "direct tailnet", remote: "100.64.0.5:443", want: "100.64.0.5"},
		{name: "direct public", remote: "203.0.113.9:443", want: "203.0.113.9"},
		{name: "remote without port", remote: "100.64.0.5", want: "100.64.0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/web/api/tailnet", nil)
			req.RemoteAddr = tc.remote
			if got := trustedClientIP(req, nil); got != tc.want {
				t.Errorf("trustedClientIP = %q, want %q", got, tc.want)
			}
		})
	}

	viaProxy := []struct {
		name   string
		remote string
		realIP string
		xff    string
		want   string
	}{
		{name: "proxy real ip wins over xff", remote: "10.1.2.3:80", realIP: "100.64.0.9", xff: "203.0.113.9", want: "100.64.0.9"},
		{name: "proxy without header falls back to peer", remote: "10.1.2.3:80", xff: "100.64.0.9", want: "10.1.2.3"},
		{name: "proxy garbage header falls back to peer", remote: "10.1.2.3:80", realIP: "bogus", want: "10.1.2.3"},
	}
	for _, tc := range viaProxy {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/web/api/tailnet", nil)
			req.RemoteAddr = tc.remote
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := trustedClientIP(req, proxy); got != tc.want {
				t.Errorf("trustedClientIP = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("non-proxy peer ignores real ip", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/web/api/tailnet", nil)
		req.RemoteAddr = "203.0.113.9:443"
		req.Header.Set("X-Real-IP", "100.64.0.9")
		if got := trustedClientIP(req, proxy); got != "203.0.113.9" {
			t.Errorf("trustedClientIP = %q, want 203.0.113.9", got)
		}
	})
}
