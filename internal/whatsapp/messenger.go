package whatsapp

import (
	"context"
	"fmt"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// WAMessenger sends and resolves messages through whatsmeow. It implements
// gateway.Messenger so it plugs straight into the shared pipeline.
type WAMessenger struct {
	client *whatsmeow.Client
	echo   *gateway.EchoTracker
}

// NewMessenger wraps a whatsmeow client with echo tracking.
func NewMessenger(client *whatsmeow.Client, echo *gateway.EchoTracker) *WAMessenger {
	return &WAMessenger{client: client, echo: echo}
}

// Self returns clark's own JID as a string.
func (m *WAMessenger) Self() string {
	return m.SelfJID().String()
}

// Send delivers a message to a chat and tracks its ID as an echo.
func (m *WAMessenger) Send(ctx context.Context, chat, text string) error {
	if m.client == nil || m.client.Store == nil {
		return fmt.Errorf("whatsapp client not initialized")
	}
	if !m.client.IsConnected() {
		return fmt.Errorf("whatsapp client is not connected to the server")
	}
	to, err := types.ParseJID(chat)
	if err != nil {
		logging.Log("WHATSAPP", logging.SevErr, "SEND", "Failed to parse JID", "to", chat, "error", err)
		return err
	}
	text = gateway.PrefixMessage(text)
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
	text = gateway.PrefixMessage(text)
	resp, err := m.client.SendMessage(ctx, m.SelfJID(), &waE2E.Message{
		Conversation: proto.String(text),
	})
	if err != nil {
		logging.Log("WHATSAPP", logging.SevErr, "SEND", "Failed to send self message", "error", err)
		return err
	}
	m.echo.Mark(string(resp.ID))
	return nil
}

// SelfJID returns clark's own JID.
func (m *WAMessenger) SelfJID() types.JID {
	if m.client == nil || m.client.Store == nil || m.client.Store.ID == nil {
		return types.JID{}
	}
	return m.client.Store.ID.ToNonAD()
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

// DownloadMedia downloads image/document bytes for vision. Returns nil when not
// downloadable or vision is not applicable (e.g. video/sticker). Caller caps size.
func (m *WAMessenger) DownloadMedia(ctx context.Context, v *events.Message) ([]byte, string, error) {
	if v == nil || v.Message == nil || m.client == nil {
		return nil, "", nil
	}
	msg := v.Message
	// Unwrap ephemeral/view-once wrappers.
	for {
		if em := msg.GetEphemeralMessage(); em != nil && em.GetMessage() != nil {
			msg = em.GetMessage()
			continue
		}
		if vom := msg.GetViewOnceMessage(); vom != nil && vom.GetMessage() != nil {
			msg = vom.GetMessage()
			continue
		}
		if v2 := msg.GetViewOnceMessageV2(); v2 != nil && v2.GetMessage() != nil {
			msg = v2.GetMessage()
			continue
		}
		break
	}
	var dl func() ([]byte, error)
	var mime string
	if im := msg.GetImageMessage(); im != nil {
		mime = im.GetMimetype()
		dl = func() ([]byte, error) { return m.client.Download(ctx, im) }
	} else if dm := msg.GetDocumentMessage(); dm != nil {
		// Only download documents that are images (e.g. forwarded photos as files).
		mime = dm.GetMimetype()
		if len(mime) >= 6 && mime[:6] == "image/" {
			dl = func() ([]byte, error) { return m.client.Download(ctx, dm) }
		} else {
			return nil, "", nil
		}
	} else {
		return nil, "", nil
	}
	if dl == nil {
		return nil, "", nil
	}
	data, err := dl()
	if err != nil {
		return nil, "", err
	}
	if len(data) > 10<<20 {
		data = data[:10<<20]
	}
	if mime == "" {
		mime = "image/jpeg"
	}
	return data, mime, nil
}
