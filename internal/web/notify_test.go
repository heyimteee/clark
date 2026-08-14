package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/alert"
	"github.com/heyimteee/clark/internal/voice"
)

const testAlertToken = "test-alert-token"

// newAlertTestServer builds a console wired to a recording alert service.
// The WA sender is wired before New (untouched by the console); the web
// broadcast recorder replaces the console's own hub hook after construction.
func newAlertTestServer(t *testing.T) (*httptest.Server, *alert.Service, *alertRecorder) {
	t.Helper()
	st := testStore(t)
	llm := &stubLLM{}
	ast := newAssistant(t, st, llm)

	rec := &alertRecorder{}
	svc := alert.New(nil)
	svc.SetWASender(func(_ context.Context, text string) error { rec.wa = append(rec.wa, text); return nil })

	srv := New(Options{
		ListenAddr: ":0",
		WebToken:   testWebToken,
		AlertToken: testAlertToken,
		Butler:     ast,
		Store:      st,
		STTModel:   "whisper-turbo",
		TTSEngine:  "kokoro-remote",
		Voice:      &voice.Engine{},
		Alerts:     svc,
	})
	// Re-point the broadcast hook at the recorder so we can observe it.
	svc.SetBroadcast(func(text string, speak bool) { rec.web = append(rec.web, text); rec.speak = append(rec.speak, speak) })
	ts := newServerFor(t, srv)
	return ts, svc, rec
}

type alertRecorder struct {
	wa    []string
	web   []string
	speak []bool
}

// postAlert sends a notify webhook with the alert token header.
func postAlert(t *testing.T, ts *httptest.Server, body any) (int, map[string]any) {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/web/api/notify", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Clark-Alert-Token", testAlertToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestNotifyUnauthorized(t *testing.T) {
	ts, _, _ := newAlertTestServer(t)
	code, _ := postJSON(t, ts, "/web/api/notify", "", map[string]any{"kind": "reboot", "title": "x", "body": "y"})
	if code != http.StatusUnauthorized {
		t.Fatalf("notify without token = %d, want 401", code)
	}
}

func TestNotifyDeliversToWhatsAppAndWeb(t *testing.T) {
	ts, _, rec := newAlertTestServer(t)
	code, _ := postAlert(t, ts, map[string]any{"kind": "reboot", "title": "Server", "body": "uptime reset"})
	if code != http.StatusOK {
		t.Fatalf("notify = %d, want 200", code)
	}
	if len(rec.wa) != 1 {
		t.Fatalf("whatsapp deliveries = %d, want 1", len(rec.wa))
	}
	if !strings.Contains(rec.wa[0], "rebooted") {
		t.Errorf("wa text = %q, want reboot template", rec.wa[0])
	}
	if len(rec.web) != 1 || rec.web[0] != rec.wa[0] {
		t.Errorf("web broadcast mismatch: %v vs %v", rec.web, rec.wa)
	}
	if len(rec.speak) != 1 || !rec.speak[0] {
		t.Errorf("voice-mode broadcast speak = %v, want true", rec.speak)
	}
}

func TestNotifySilentModeShowsButDoesNotSpeak(t *testing.T) {
	ts, svc, rec := newAlertTestServer(t)
	svc.SetModeReader(func() string { return "silent" })
	code, _ := postAlert(t, ts, map[string]any{"kind": "bypass", "title": "Sir", "body": "you are needed"})
	if code != http.StatusOK {
		t.Fatalf("notify = %d, want 200", code)
	}
	if len(rec.web) != 1 {
		t.Fatalf("web broadcast = %d, want 1", len(rec.web))
	}
	if len(rec.speak) != 1 || rec.speak[0] {
		t.Errorf("silent-mode broadcast speak = %v, want false", rec.speak)
	}
}

func TestNotifyMissingBody(t *testing.T) {
	ts, _, _ := newAlertTestServer(t)
	code, _ := postAlert(t, ts, map[string]any{"kind": "reboot", "title": "x"})
	if code != http.StatusBadRequest {
		t.Fatalf("notify without body = %d, want 400", code)
	}
}
