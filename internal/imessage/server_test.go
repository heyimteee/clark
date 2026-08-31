package imessage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/store"
)

// recMessenger records the reply the gateway pipeline produced.
type recMessenger struct {
	mu      sync.Mutex
	handled bool
	text    string
}

func (m *recMessenger) Self() string { return "6281111111111@s.whatsapp.net" }
func (m *recMessenger) Send(_ context.Context, _, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handled = true
	m.text = text
	return nil
}
func (m *recMessenger) SendSelf(context.Context, string) error { return nil }

// recButler lets the gateway pipeline generate replies immediately.
type recButler struct{}

func (b *recButler) Prehandle(_, _ string, _ bool) (string, bool, error) { return "", false, nil }
func (b *recButler) Reply(_ context.Context, _, _ string, _ bool) (string, error) {
	return "Indubitably.", nil
}
func (b *recButler) Relation(_ string) (string, bool) { return "Test (Friend)", true }
func (b *recButler) Enabled() bool                    { return true }
func (b *recButler) EnabledFor(_ string) bool         { return true }

func waitHandled(t *testing.T, m *recMessenger) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		done := m.handled
		m.mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("gateway pipeline never produced a reply")
}

func newTestServer(t *testing.T, token string, out OutboundStore) (*Server, *recMessenger) {
	t.Helper()
	msgr := &recMessenger{}
	h := gateway.NewHandler("IMESSAGE", msgr, &recButler{}, nil, "get him to me")
	return NewServer(token, "+6281111111111", out, h), msgr
}

func doRequest(ts *Server, method, path, token, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	}
	if token != "" {
		r.Header.Set("X-Clark-Bridge-Token", token)
	}
	rec := httptest.NewRecorder()
	ts.Routes().ServeHTTP(rec, r)
	return rec
}

func TestServerRequiresToken(t *testing.T) {
	ts, _ := newTestServer(t, "secret", &fakeOutbound{})

	if rec := doRequest(ts, "POST", "/inbound", "", `{"handle":"+6281267858909","text":"hi"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token = %d, want 401", rec.Code)
	}
	if rec := doRequest(ts, "POST", "/inbound", "wrong", `{"handle":"+6281267858909","text":"hi"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", rec.Code)
	}
	if rec := doRequest(ts, "POST", "/inbound", "secret", `{"handle":"+6281267858909","text":"hi"}`); rec.Code != http.StatusOK {
		t.Fatalf("correct token = %d, want 200", rec.Code)
	}
}

func TestServerInboundFeedsGateway(t *testing.T) {
	ts, msgr := newTestServer(t, "", &fakeOutbound{})

	body := `{"id":"42","handle":"+6281267858909","text":"good evening","is_self":false}`
	if rec := doRequest(ts, "POST", "/inbound", "", body); rec.Code != http.StatusOK {
		t.Fatalf("inbound = %d, want 200", rec.Code)
	}
	waitHandled(t, msgr)
	msgr.mu.Lock()
	defer msgr.mu.Unlock()
	if !strings.Contains(msgr.text, "Indubitably.") {
		t.Errorf("reply = %q, want the butler's reply", msgr.text)
	}
}

func TestServerInboundDropsSelf(t *testing.T) {
	ts, msgr := newTestServer(t, "", &fakeOutbound{})

	// Bridge-marked self message (the echo-loop source).
	if rec := doRequest(ts, "POST", "/inbound", "", `{"id":"1","handle":"+628117705636","text":"*Status Updated* Clark is now On","is_self":true}`); rec.Code != http.StatusOK {
		t.Fatalf("marked self inbound = %d, want 200", rec.Code)
	}
	// Own handle with is_self unset: still the Master's self-chat (defense in depth).
	if rec := doRequest(ts, "POST", "/inbound", "", `{"id":"2","handle":"+6281111111111","text":"status off","is_self":false}`); rec.Code != http.StatusOK {
		t.Fatalf("own-handle inbound = %d, want 200", rec.Code)
	}

	// Give any (buggy) gateway invocation time to surface, then assert none ran.
	time.Sleep(150 * time.Millisecond)
	msgr.mu.Lock()
	defer msgr.mu.Unlock()
	if msgr.handled {
		t.Fatalf("gateway pipeline invoked for self messages; want it skipped")
	}
}

func TestServerInboundInvalid(t *testing.T) {
	ts, _ := newTestServer(t, "", &fakeOutbound{})

	for _, body := range []string{"not json", `{"handle":"","text":"hi"}`, `{"handle":"+6281267858909","text":""}`} {
		if rec := doRequest(ts, "POST", "/inbound", "", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, rec.Code)
		}
	}
}

func TestServerOutboundClaimsAndAcks(t *testing.T) {
	out := &fakeOutbound{}
	if _, err := out.EnqueueIMessage("+6281267858909", "hello"); err != nil {
		t.Fatalf("EnqueueIMessage: %v", err)
	}
	ts, _ := newTestServer(t, "", out)

	rec := doRequest(ts, "GET", "/outbound", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("outbound = %d, want 200", rec.Code)
	}
	var msg store.OutboundMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode outbound: %v", err)
	}
	if msg.Recipient != "+6281267858909" || msg.Text != "hello" {
		t.Errorf("outbound = %+v, want queued message", msg)
	}

	// Once claimed, the queue is empty.
	if rec := doRequest(ts, "GET", "/outbound", "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("outbound after claim = %d, want 204", rec.Code)
	}

	// Acking an unknown id is a no-op success.
	if rec := doRequest(ts, "POST", "/ack", "", `{"id":999}`); rec.Code != http.StatusOK {
		t.Fatalf("ack = %d, want 200", rec.Code)
	}
}

func TestServerAckInvalid(t *testing.T) {
	ts, _ := newTestServer(t, "", &fakeOutbound{})
	if rec := doRequest(ts, "POST", "/ack", "", `{"id":0}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("ack id=0 = %d, want 400", rec.Code)
	}
	if rec := doRequest(ts, "POST", "/ack", "", `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("ack bad json = %d, want 400", rec.Code)
	}
}

// TestServerRejectsOversizedBodies guards against memory exhaustion: ack bodies
// beyond maxBodyBytes must be rejected with 413. Inbound now allows up to
// 55 MiB for base64 media, so a 300 KiB text payload is accepted.
func TestServerRejectsOversizedBodies(t *testing.T) {
	ts, _ := newTestServer(t, "secret", &fakeOutbound{})

	huge := `{"handle":"+6281267858909","text":"` + strings.Repeat("a", 300<<10) + `"}`
	if rec := doRequest(ts, "POST", "/inbound", "secret", huge); rec.Code != http.StatusOK {
		t.Fatalf("inbound 300 KiB = %d, want 200 (media cap is 55 MiB)", rec.Code)
	}

	hugeAck := `{"id":1,"pad":"` + strings.Repeat("a", 300<<10) + `"}`
	if rec := doRequest(ts, "POST", "/ack", "secret", hugeAck); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized ack = %d, want 413", rec.Code)
	}
}

func TestToGateway(t *testing.T) {
	in := InboundMessage{ID: "7", Handle: "+6281267858909", Text: "ping", IsSelf: true}
	msg := toGateway(in)
	if msg.ID != "7" || msg.Chat != "6281267858909@s.whatsapp.net" || msg.Sender != "6281267858909@s.whatsapp.net" {
		t.Errorf("toGateway = %+v, want canonical self message", msg)
	}
	if !msg.IsSelf || msg.IsGroup {
		t.Errorf("toGateway isSelf=%v isGroup=%v, want self, non-group", msg.IsSelf, msg.IsGroup)
	}
}
