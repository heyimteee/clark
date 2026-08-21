package web

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLoginGoodKey(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	code, out := postJSON(t, ts, "/web/api/login", "", map[string]any{"key": testWebToken})
	if code != http.StatusOK {
		t.Fatalf("login = %d, want 200: %v", code, out)
	}
	if out["token"] == "" {
		t.Error("login returned no token")
	}
	if exp, _ := out["expires_in"].(float64); exp <= 0 {
		t.Errorf("expires_in = %v, want positive", out["expires_in"])
	}
}

func TestLoginBadKey(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	code, out := postJSON(t, ts, "/web/api/login", "", map[string]any{"key": "wrong"})
	if code != http.StatusUnauthorized {
		t.Fatalf("login = %d, want 401", code)
	}
	if out["error"] == "" {
		t.Error("login returned no error message")
	}
}

func TestLoginEmptyKey(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	code, _ := postJSON(t, ts, "/web/api/login", "", map[string]any{"key": ""})
	if code != http.StatusUnauthorized {
		t.Fatalf("login = %d, want 401", code)
	}
}

func TestStateRequiresAuth(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	code, out := getJSON(t, ts, "/web/api/state", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("state without token = %d, want 401", code)
	}
	if out["error"] == "" {
		t.Error("unauthorized state returned no error")
	}
}

func TestStateRejectsBadToken(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	code, _ := getJSON(t, ts, "/web/api/state", "bogus")
	if code != http.StatusUnauthorized {
		t.Fatalf("state with bad token = %d, want 401", code)
	}
}

func TestSessionExpires(t *testing.T) {
	st := testStore(t)
	llm := &stubLLM{}
	ast := newAssistant(t, st, llm)

	srv := New(Options{
		ListenAddr: ":0",
		WebToken:   testWebToken,
		Butler:     ast,
		Store:      st,
		STTModel:   "whisper-turbo",
		TTSEngine:  "kokoro-remote",
		SessionTTL: 50 * time.Millisecond,
		Voice:      voiceEngine(),
	})
	ts := newServerFor(t, srv)

	tok := login(t, ts)
	if code, _ := getJSON(t, ts, "/web/api/state", tok); code != http.StatusOK {
		t.Fatalf("state before expiry = %d, want 200", code)
	}

	time.Sleep(120 * time.Millisecond)
	if code, _ := getJSON(t, ts, "/web/api/state", tok); code != http.StatusUnauthorized {
		t.Fatalf("state after expiry = %d, want 401", code)
	}
}

func TestSessionSlides(t *testing.T) {
	st := testStore(t)
	llm := &stubLLM{}
	ast := newAssistant(t, st, llm)

	srv := New(Options{
		ListenAddr: ":0",
		WebToken:   testWebToken,
		Butler:     ast,
		Store:      st,
		STTModel:   "whisper-turbo",
		TTSEngine:  "kokoro-remote",
		SessionTTL: time.Second,
		Voice:      voiceEngine(),
	})
	ts := newServerFor(t, srv)

	tok := login(t, ts)
	// Activity keeps the sliding TTL alive past half the window.
	for i := 0; i < 3; i++ {
		time.Sleep(400 * time.Millisecond)
		if code, _ := getJSON(t, ts, "/web/api/state", tok); code != http.StatusOK {
			t.Fatalf("state at %dms = %d, want 200 (sliding TTL)", i*400, code)
		}
	}
}

// TestBridgeRoutesNotMounted guards the security boundary fixed in #57: the
// bridge API must never be reachable through the public console mux. The
// bridge runs on its own listener; /inbound here must 404.
func TestBridgeRoutesNotMounted(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/inbound", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bridge request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("console /inbound = %d, want 404 (bridge must not be mounted under the console)", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/outbound", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("outbound request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("console /outbound = %d, want 404", resp2.StatusCode)
	}
}
