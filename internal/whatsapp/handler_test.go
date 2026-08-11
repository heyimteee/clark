package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
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

// fakeButler is a minimal gateway.Butler for the adapter mapping tests. It
// never engages the model, so no real connection is required.
type fakeButler struct{}

func (b *fakeButler) Prehandle(_, _ string, _ bool) (string, bool, error) { return "", false, nil }
func (b *fakeButler) Reply(_ context.Context, _, _ string, _ bool) (string, error) {
	return "", nil
}
func (b *fakeButler) Relation(_ string) (string, bool) { return "Test (Friend)", true }
func (b *fakeButler) Enabled() bool                    { return true }
func (b *fakeButler) EnabledFor(_ string) bool         { return true }

// newTestAdapter builds a Handler whose WAMessenger is backed by an in-memory
// device store, so toGateway can be exercised without a real connection.
func newTestAdapter() *Handler {
	self := types.JID{User: "6281111111111", Server: "s.whatsapp.net"}
	dev := &store.Device{ID: &self}
	client := whatsmeow.NewClient(dev, nil)
	msgr := NewMessenger(client, gateway.NewEchoTracker())
	return NewHandler(msgr, &fakeButler{}, nil, gateway.NewEchoTracker(), time.Now().Add(-time.Hour), "get him to me")
}

func hmsg(chat, sender types.JID, fromMe bool, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe},
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func TestToGatewayMapsPrivateMessage(t *testing.T) {
	h := newTestAdapter()
	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}

	got, ok := h.toGateway(hmsg(vip, vip, false, "hello"))
	if !ok {
		t.Fatal("private message dropped by adapter")
	}
	if got.Sender != vip.String() {
		t.Errorf("Sender = %q, want %q", got.Sender, vip.String())
	}
	if got.Chat != vip.String() {
		t.Errorf("Chat = %q, want %q", got.Chat, vip.String())
	}
	if got.Text != "hello" {
		t.Errorf("Text = %q, want hello", got.Text)
	}
	if got.IsSelf || got.IsGroup {
		t.Errorf("IsSelf=%v IsGroup=%v, want both false", got.IsSelf, got.IsGroup)
	}
}

func TestToGatewaySelfChat(t *testing.T) {
	h := newTestAdapter()
	self := h.msgr.SelfJID()

	got, ok := h.toGateway(hmsg(self, self, true, "status report"))
	if !ok {
		t.Fatal("self-chat message dropped by adapter")
	}
	if !got.IsSelf {
		t.Error("IsSelf = false, want true for the Master's own chat")
	}
}

func TestToGatewayDropsOutboundToOthers(t *testing.T) {
	h := newTestAdapter()
	other := types.JID{User: "6289999999999", Server: "s.whatsapp.net"}

	if _, ok := h.toGateway(hmsg(other, other, true, "hi")); ok {
		t.Fatal("outbound message to a non-self chat was forwarded")
	}
}

func TestToGatewayExtractsExtendedText(t *testing.T) {
	h := newTestAdapter()
	vip := types.JID{User: "6281234567890", Server: "s.whatsapp.net"}

	msg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: vip, Sender: vip},
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("extended")},
		},
	}

	got, ok := h.toGateway(msg)
	if !ok {
		t.Fatal("extended-text message dropped by adapter")
	}
	if got.Text != "extended" {
		t.Errorf("Text = %q, want extended", got.Text)
	}
}
