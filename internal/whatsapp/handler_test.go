package whatsapp

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/ollama"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func msgFixture(timestamp time.Time, isFromMe bool) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     types.JID{User: "6281234567890", Server: "s.whatsapp.net"},
				Sender:   types.JID{User: "6281234567890", Server: "s.whatsapp.net"},
				IsFromMe: isFromMe,
			},
			Timestamp: timestamp,
		},
		Message: &waE2E.Message{Conversation: proto.String("hi")},
	}
}

func TestFilterMessageNil(t *testing.T) {
	skip, reason := filterMessage(nil, time.Now())
	if !skip {
		t.Fatal("nil message not skipped")
	}
	if reason == "" {
		t.Fatal("nil message skipped without reason")
	}
}

func TestFilterMessageOldTimestamp(t *testing.T) {
	now := time.Now()
	connectedAt := now.Add(-time.Minute)
	v := msgFixture(now.Add(-time.Hour), false)

	skip, reason := filterMessage(v, connectedAt)
	if !skip {
		t.Fatal("old message not skipped")
	}
	if reason != "" {
		t.Fatalf("old message skipped with reason %q, want silent", reason)
	}
}

func TestFilterMessageFresh(t *testing.T) {
	now := time.Now()
	connectedAt := now.Add(-time.Minute)
	v := msgFixture(now, false)

	if skip, _ := filterMessage(v, connectedAt); skip {
		t.Fatal("fresh message skipped")
	}
}

func TestFilterMessageZeroTimestamp(t *testing.T) {
	v := msgFixture(time.Time{}, false)

	if skip, _ := filterMessage(v, time.Now()); !skip {
		t.Fatal("zero-timestamp message not skipped")
	}
}

func TestEchoTracker(t *testing.T) {
	e := NewEchoTracker()

	if e.Consume("missing") {
		t.Fatal("Consume matched unknown id")
	}

	e.Mark("abc")
	if !e.Consume("abc") {
		t.Fatal("Consume missed tracked id")
	}
	if e.Consume("abc") {
		t.Fatal("Consume matched id after first consume")
	}
}

type fakeMessenger struct {
	mu       sync.Mutex
	self     types.JID
	sentTo   []types.JID
	sentSelf int
}

func (m *fakeMessenger) Self() types.JID { return m.self }
func (m *fakeMessenger) Send(_ context.Context, to types.JID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentTo = append(m.sentTo, to)
	return nil
}
func (m *fakeMessenger) SendSelf(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentSelf++
	return nil
}

func (m *fakeMessenger) ResolveSender(v *events.Message) types.JID {
	return v.Info.Sender.ToNonAD()
}
func (m *fakeMessenger) IsSelfChat(chat types.JID) bool {
	return chat.User == m.self.User && chat.Server == m.self.Server
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

func hmsg(chat, sender types.JID, fromMe bool, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe},
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func newTestHandler(msgr *fakeMessenger, butler Butler) *Handler {
	return NewHandler(msgr, butler, nil, NewEchoTracker(), time.Now().Add(-time.Hour), "get him to me")
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
	h := NewHandler(msgr, butler, note, NewEchoTracker(), time.Now().Add(-time.Hour), "summon the butler")

	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	h.OnEvent(hmsg(vip, vip, false, "summon the butler"))
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

	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	h.OnEvent(hmsg(vip, vip, false, "hello"))

	if len(butler.replied) != 0 {
		t.Fatalf("disabled butler replied %d times, want 0", len(butler.replied))
	}
	if len(msgr.sentTo) != 0 {
		t.Fatalf("sent %d messages, want 0", len(msgr.sentTo))
	}
}

func TestHandlerDisabledSelfChatStillEngages(t *testing.T) {
	self := types.JID{User: "6281111111111", Server: "s.whatsapp.net"}
	msgr := &fakeMessenger{self: self}
	butler := &fakeButler{enabled: false}
	h := newTestHandler(msgr, butler)

	h.OnEvent(hmsg(self, self, true, "status report"))
	h.Close()

	if len(butler.replied) != 1 {
		t.Fatalf("self-chat butler replied %d times, want 1", len(butler.replied))
	}
	if len(msgr.sentTo) != 2 {
		t.Fatalf("sent %d messages, want ack + reply = 2", len(msgr.sentTo))
	}
}

func TestHandlerFreshBootMasterSelfChatBypassesVIPGate(t *testing.T) {
	self := types.JID{User: "6281111111111", Server: "s.whatsapp.net"}
	msgr := &fakeMessenger{self: self}
	butler := &nonVIPButler{fakeButler: fakeButler{enabled: false}}
	h := newTestHandler(msgr, butler)

	h.OnEvent(hmsg(self, self, true, "wake up buddy"))
	h.Close()

	if len(butler.replied) != 1 {
		t.Fatalf("fresh-boot self-chat engaged %d model replies, want 1", len(butler.replied))
	}
}

func TestHandlerFreshBootNonVIPNonSelfIgnored(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &nonVIPButler{fakeButler: fakeButler{enabled: true}}
	h := newTestHandler(msgr, butler)

	stranger := types.JID{User: "6289999999999", Server: "s.whatsapp.net"}
	h.OnEvent(hmsg(stranger, stranger, false, "hello"))

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

	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	h.OnEvent(hmsg(vip, vip, false, "hello"))
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

	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	h.OnEvent(hmsg(vip, vip, false, "clark status off"))
	h.Close()

	if len(butler.replied) != 0 {
		t.Fatalf("fast path reached the model %d times, want 0", len(butler.replied))
	}
	if len(msgr.sentTo) != 1 {
		t.Fatalf("sent %d messages, want only the hardcoded reply = 1", len(msgr.sentTo))
	}
	if msgr.sentTo[0] != vip {
		t.Errorf("fast reply sent to %v, want %v", msgr.sentTo[0], vip)
	}
}

func TestHandlerPerSenderOrdering(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true, replyDelay: 5 * time.Millisecond}
	h := newTestHandler(msgr, butler)

	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	h.OnEvent(hmsg(vip, vip, false, "first"))
	h.OnEvent(hmsg(vip, vip, false, "second"))
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

	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	h.OnEvent(hmsg(vip, vip, false, "hello"))

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

	group := types.JID{User: "1234567890", Server: "g.us"}
	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	msg := hmsg(group, vip, false, "hello")
	msg.Info.IsGroup = true
	h.OnEvent(msg)
	h.Close()

	if len(butler.replied) != 0 {
		t.Fatalf("group message reached butler %d times, want 0", len(butler.replied))
	}
}

func TestHandlerPerVIPOverrideEngagesWhenGlobalOff(t *testing.T) {
	msgr := &fakeMessenger{}
	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	butler := &fakeButler{enabled: false, overrides: map[string]bool{vip.String(): true}}
	h := newTestHandler(msgr, butler)

	h.OnEvent(hmsg(vip, vip, false, "hello"))
	h.Close()

	if len(butler.replied) != 1 {
		t.Fatalf("per-VIP-woken butler replied %d times, want 1", len(butler.replied))
	}
}

func TestHandlerPerVIPOverrideIgnoredWhenGlobalOn(t *testing.T) {
	msgr := &fakeMessenger{}
	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	butler := &fakeButler{enabled: true, overrides: map[string]bool{vip.String(): false}}
	h := newTestHandler(msgr, butler)

	h.OnEvent(hmsg(vip, vip, false, "hello"))

	if len(butler.replied) != 0 {
		t.Fatalf("per-VIP-silenced butler replied %d times, want 0", len(butler.replied))
	}
	if len(msgr.sentTo) != 0 {
		t.Fatalf("sent %d messages, want 0", len(msgr.sentTo))
	}
}

func TestHandlerRateLimitAlertsMasterAndApologizes(t *testing.T) {
	msgr := &fakeMessenger{}
	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}
	butler := &fakeButler{
		enabled:  true,
		replyErr: fmt.Errorf("failed to execute model: %w", ollama.ErrRateLimited),
	}
	h := newTestHandler(msgr, butler)

	h.OnEvent(hmsg(vip, vip, false, "hello"))
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
	if msgr.sentTo[0] != vip {
		t.Errorf("apology sent to %v, want sender %v", msgr.sentTo[0], vip)
	}
}
