package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/ollama"
)

const (
	testVIP      = "6281234567890@s.whatsapp.net"
	testStranger = "6289999999999@s.whatsapp.net"
	testSelf     = "6281111111111@s.whatsapp.net"
)

func testMsg(sender, text string) Message {
	return Message{Sender: sender, Chat: sender, Text: text}
}

type fakeMessenger struct {
	mu       sync.Mutex
	self     string
	sentTo   []string
	sentSelf int
}

func (m *fakeMessenger) Self() string { return m.self }
func (m *fakeMessenger) Send(_ context.Context, chat, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentTo = append(m.sentTo, chat)
	return nil
}
func (m *fakeMessenger) SendSelf(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentSelf++
	return nil
}

type fakeButler struct {
	mu         sync.Mutex
	enabled    bool
	overrides  map[string]bool
	prehandled string
	replied    []string
	replyDelay time.Duration
	replyErr   error
}

func (b *fakeButler) Prehandle(_, text string, _ bool) (string, bool, error) {
	if b.prehandled != "" {
		return b.prehandled, true, nil
	}
	return "", false, nil
}
func (b *fakeButler) Reply(_ context.Context, _, text string, _ bool) (string, error) {
	if b.replyDelay > 0 {
		time.Sleep(b.replyDelay)
	}
	if b.replyErr != nil {
		return "", b.replyErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.replied = append(b.replied, text)
	return "Indubitably.", nil
}
func (b *fakeButler) Relation(_ string) (string, bool) { return "Test (Friend)", true }
func (b *fakeButler) Enabled() bool                    { return b.enabled }
func (b *fakeButler) EnabledFor(jid string) bool {
	if on, ok := b.overrides[jid]; ok {
		return on
	}
	return b.enabled
}

// nonVIPButler mimics a fresh database where no one — not even the Master — is
// registered in the inner circle yet.
type nonVIPButler struct {
	fakeButler
}

func (b *nonVIPButler) Relation(_ string) (string, bool) { return "", false }

func newTestHandler(msgr *fakeMessenger, butler Butler) *Handler {
	return NewHandler("WHATSAPP", msgr, butler, nil, "get him to me")
}

type fakeNotifier struct {
	mu    sync.Mutex
	calls []string
}

func (n *fakeNotifier) Notify(title, body string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, title+"|"+body)
	return nil
}

func TestHandlerCustomBypassPhraseAlerts(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true}
	note := &fakeNotifier{}
	h := NewHandler("WHATSAPP", msgr, butler, note, "summon the butler")

	h.Handle(testMsg(testVIP, "summon the butler"))
	h.Close()

	if len(butler.replied) != 0 {
		t.Fatalf("bypass phrase reached the model %d times, want 0", len(butler.replied))
	}
	if msgr.sentSelf != 1 {
		t.Fatalf("self alerts = %d, want 1", msgr.sentSelf)
	}
	if len(note.calls) != 1 {
		t.Fatalf("notifications = %d, want 1", len(note.calls))
	}
	if len(msgr.sentTo) != 1 {
		t.Fatalf("acknowledgements sent = %d, want 1", len(msgr.sentTo))
	}
}

func TestHandlerDisabledVIPIgnored(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: false}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "hello"))

	if len(butler.replied) != 0 {
		t.Fatalf("disabled butler replied %d times, want 0", len(butler.replied))
	}
	if len(msgr.sentTo) != 0 {
		t.Fatalf("sent %d messages, want 0", len(msgr.sentTo))
	}
}

func TestHandlerDisabledSelfChatStillEngages(t *testing.T) {
	msgr := &fakeMessenger{self: testSelf}
	butler := &fakeButler{enabled: false}
	h := newTestHandler(msgr, butler)

	h.Handle(Message{Sender: testSelf, Chat: testSelf, Text: "status report", IsSelf: true})
	h.Close()

	if len(butler.replied) != 1 {
		t.Fatalf("self-chat butler replied %d times, want 1", len(butler.replied))
	}
	if len(msgr.sentTo) != 2 {
		t.Fatalf("sent %d messages, want ack + reply = 2", len(msgr.sentTo))
	}
}

func TestHandlerFreshBootMasterSelfChatBypassesVIPGate(t *testing.T) {
	msgr := &fakeMessenger{self: testSelf}
	butler := &nonVIPButler{fakeButler: fakeButler{enabled: false}}
	h := newTestHandler(msgr, butler)

	h.Handle(Message{Sender: testSelf, Chat: testSelf, Text: "wake up buddy", IsSelf: true})
	h.Close()

	if len(butler.replied) != 1 {
		t.Fatalf("fresh-boot self-chat engaged %d model replies, want 1", len(butler.replied))
	}
}

func TestHandlerFreshBootNonVIPNonSelfIgnored(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &nonVIPButler{fakeButler: fakeButler{enabled: true}}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testStranger, "hello"))

	if len(butler.replied) != 0 {
		t.Fatalf("non-VIP stranger replied %d times, want 0", len(butler.replied))
	}
	if len(msgr.sentTo) != 0 {
		t.Fatalf("sent %d messages, want 0", len(msgr.sentTo))
	}
}

func TestHandlerEnabledVIPEngages(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "hello"))
	h.Close()

	if len(butler.replied) != 1 {
		t.Fatalf("enabled butler replied %d times, want 1", len(butler.replied))
	}
	if len(msgr.sentTo) != 1 {
		t.Fatalf("sent %d messages, want only the reply = 1 (no ack for VIP)", len(msgr.sentTo))
	}
}

func TestHandlerFastPathHardcodedReply(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true, prehandled: "*Status Updated*"}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "clark status off"))
	h.Close()

	if len(butler.replied) != 0 {
		t.Fatalf("fast path reached the model %d times, want 0", len(butler.replied))
	}
	if len(msgr.sentTo) != 1 {
		t.Fatalf("sent %d messages, want only the hardcoded reply = 1", len(msgr.sentTo))
	}
	if msgr.sentTo[0] != testVIP {
		t.Errorf("fast reply sent to %q, want %q", msgr.sentTo[0], testVIP)
	}
}

func TestHandlerPerSenderOrdering(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true, replyDelay: 5 * time.Millisecond}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "first"))
	h.Handle(testMsg(testVIP, "second"))
	h.Close()

	if len(butler.replied) != 2 {
		t.Fatalf("replied %d times, want 2", len(butler.replied))
	}
	if butler.replied[0] != "first" || butler.replied[1] != "second" {
		t.Errorf("replies out of order: %v", butler.replied)
	}
	if len(msgr.sentTo) != 2 {
		t.Fatalf("sent %d messages, want one reply each (no acks) = 2", len(msgr.sentTo))
	}
}

func TestHandlerCloseDrainsInFlight(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true, replyDelay: 25 * time.Millisecond}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "hello"))

	done := make(chan struct{})
	go func() {
		h.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close returned before draining the in-flight reply")
	}
	if len(butler.replied) != 1 {
		t.Fatalf("in-flight reply was dropped: replied %d times, want 1", len(butler.replied))
	}
	if len(msgr.sentTo) != 1 {
		t.Fatalf("sent %d messages, want only the drained reply = 1 (no ack for VIP)", len(msgr.sentTo))
	}
}

func TestHandlerGroupIgnored(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true}
	h := newTestHandler(msgr, butler)

	h.Handle(Message{Sender: testVIP, Chat: "1234567890@g.us", Text: "hello", IsGroup: true})
	h.Close()

	if len(butler.replied) != 0 {
		t.Fatalf("group message reached butler %d times, want 0", len(butler.replied))
	}
}

func TestHandlerPerVIPOverrideEngagesWhenGlobalOff(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: false, overrides: map[string]bool{testVIP: true}}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "hello"))
	h.Close()

	if len(butler.replied) != 1 {
		t.Fatalf("per-VIP-woken butler replied %d times, want 1", len(butler.replied))
	}
}

func TestHandlerPerVIPOverrideIgnoredWhenGlobalOn(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true, overrides: map[string]bool{testVIP: false}}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "hello"))

	if len(butler.replied) != 0 {
		t.Fatalf("per-VIP-silenced butler replied %d times, want 0", len(butler.replied))
	}
	if len(msgr.sentTo) != 0 {
		t.Fatalf("sent %d messages, want 0", len(msgr.sentTo))
	}
}

func TestHandlerRateLimitAlertsMasterAndApologizes(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{
		enabled:  true,
		replyErr: fmt.Errorf("failed to execute model: %w", ollama.ErrRateLimited),
	}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "hello"))
	h.Close()

	if len(butler.replied) != 0 {
		t.Fatalf("rate-limited butler replied %d times, want 0", len(butler.replied))
	}
	if msgr.sentSelf != 1 {
		t.Fatalf("master alerts = %d, want 1", msgr.sentSelf)
	}
	if len(msgr.sentTo) != 1 {
		t.Fatalf("apologies sent = %d, want 1", len(msgr.sentTo))
	}
	if msgr.sentTo[0] != testVIP {
		t.Errorf("apology sent to %q, want sender %q", msgr.sentTo[0], testVIP)
	}
}

func TestHandlerReplyErrorApologizes(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true, replyErr: errors.New("boom")}
	h := newTestHandler(msgr, butler)

	h.Handle(testMsg(testVIP, "hello"))
	h.Close()

	if msgr.sentSelf != 0 {
		t.Fatalf("master alerts = %d, want 0 (only rate-limit alerts the master)", msgr.sentSelf)
	}
	if len(msgr.sentTo) != 1 {
		t.Fatalf("apologies sent = %d, want 1", len(msgr.sentTo))
	}
}

func TestHandlerEmptyTextDropped(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true}
	h := newTestHandler(msgr, butler)

	h.Handle(Message{Sender: testVIP, Chat: testVIP, Text: ""})

	if len(butler.replied) != 0 {
		t.Fatalf("empty message reached butler %d times, want 0", len(butler.replied))
	}
	if len(msgr.sentTo) != 0 {
		t.Fatalf("sent %d messages, want 0", len(msgr.sentTo))
	}
}
