package whatsapp

import (
	"context"

	"github.com/heyimteee/clark/internal/logging"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Messenger abstracts the WhatsApp transport the handler needs.
type Messenger interface {
	Self() types.JID
	Send(ctx context.Context, to types.JID, text string) error
	SendSelf(ctx context.Context, text string) error
	ResolveSender(v *events.Message) types.JID
	IsSelfChat(chat types.JID) bool
}

// WAMessenger sends and resolves messages through whatsmeow.
type WAMessenger struct {
	client *whatsmeow.Client
	echo   *EchoTracker
}

// NewMessenger wraps a whatsmeow client with echo tracking.
func NewMessenger(client *whatsmeow.Client, echo *EchoTracker) *WAMessenger {
	return &WAMessenger{client: client, echo: echo}
}

// Self returns clark's own JID.
func (m *WAMessenger) Self() types.JID {
	if m.client == nil || m.client.Store == nil || m.client.Store.ID == nil {
		return types.JID{}
	}
	return m.client.Store.ID.ToNonAD()
}

// Send delivers a message and tracks its ID as an echo.
func (m *WAMessenger) Send(ctx context.Context, to types.JID, text string) error {
	resp, err := m.client.SendMessage(ctx, to, &waE2E.Message{
		Conversation: proto.String(text),
	})
	if err != nil {
		logging.Log("WHATSAPP", logging.SevErr, "SEND", "Failed to send message", "to", to.User, "error", err)
		return err
	}
	m.echo.Mark(string(resp.ID))
	logReply(to.User, text)
	return nil
}

// SendSelf delivers a message to clark's own chat.
func (m *WAMessenger) SendSelf(ctx context.Context, text string) error {
	resp, err := m.client.SendMessage(ctx, m.Self(), &waE2E.Message{
		Conversation: proto.String(text),
	})
	if err != nil {
		logging.Log("WHATSAPP", logging.SevErr, "SEND", "Failed to send self message", "error", err)
		return err
	}
	m.echo.Mark(string(resp.ID))
	return nil
}

// ResolveSender maps the message sender to a phone-number JID where possible.
func (m *WAMessenger) ResolveSender(v *events.Message) types.JID {
	sender := v.Info.Sender.ToNonAD()
	if sender.Server != types.HiddenUserServer {
		return sender
	}

	if pn, err := m.client.Store.LIDs.GetPNForLID(context.Background(), sender); err == nil && !pn.IsEmpty() {
		return pn.ToNonAD()
	}
	if v.Info.IsFromMe && m.client.Store.ID != nil {
		return m.client.Store.ID.ToNonAD()
	}
	return sender
}

// IsSelfChat reports whether chat is clark's own chat.
func (m *WAMessenger) IsSelfChat(chat types.JID) bool {
	if m.client == nil || m.client.Store == nil || m.client.Store.ID == nil {
		return false
	}

	self := m.resolvePhone(m.client.Store.ID.ToNonAD())
	chat = m.resolvePhone(chat.ToNonAD())
	return chat.User == self.User && chat.Server == self.Server
}

func (m *WAMessenger) resolvePhone(jid types.JID) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if pn, err := m.client.Store.LIDs.GetPNForLID(context.Background(), jid); err == nil && !pn.IsEmpty() {
		return pn.ToNonAD()
	}
	return jid
}
