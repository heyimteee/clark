package whatsapp

import (
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Handler adapts whatsmeow events into the shared gateway pipeline.
type Handler struct {
	msgr        *WAMessenger
	butler      gateway.Butler
	echo        *gateway.EchoTracker
	connectedAt time.Time
	gw          *gateway.Handler
}

// NewHandler wires the adapter around its dependencies. bypassPhrase is the
// urgent-alert command word (default "get him to me").
func NewHandler(msgr *WAMessenger, butler gateway.Butler, notifier gateway.Notifier, echo *gateway.EchoTracker, connectedAt time.Time, bypassPhrase string) *Handler {
	return &Handler{
		msgr:        msgr,
		butler:      butler,
		echo:        echo,
		connectedAt: connectedAt,
		gw:          gateway.NewHandler("WHATSAPP", msgr, butler, notifier, bypassPhrase),
	}
}

// Close stops the background dispatcher and waits for in-flight replies.
func (h *Handler) Close() {
	h.gw.Close()
}

// OnEvent is the whatsmeow event sink.
func (h *Handler) OnEvent(evt any) {
	v, ok := evt.(*events.Message)
	if !ok {
		return
	}

	if skip, reason := filterMessage(v, h.connectedAt); skip {
		if reason != "" {
			logging.Log("WHATSAPP", logging.SevWarn, "MESSAGE", "Message discarded", "reason", reason)
		}
		return
	}

	if h.echo.Consume(string(v.Info.ID)) {
		return
	}

	msg, ok := h.toGateway(v)
	if !ok {
		return
	}
	h.gw.Handle(msg)
}

// toGateway maps a whatsmeow message to a neutral gateway.Message. It reports
// false when the transport itself must drop the message (outbound echoes to a
// chat that is not clark's own).
func (h *Handler) toGateway(v *events.Message) (gateway.Message, bool) {
	isSelf := false
	if v.Info.IsFromMe {
		if !h.msgr.IsSelfChat(v.Info.Chat) {
			return gateway.Message{}, false
		}
		isSelf = true
	}

	sender := h.msgr.ResolveSender(v)
	senderStr := sender.String()
	relation, isVIP := h.butler.Relation(senderStr)

	userMsg, mediaType := extractTextAndMedia(v)

	who := "Unknown"
	if isVIP {
		who = relation
	}
	// Log only private messages; group chatter is filtered out so the logs
	// show just the people Clark actually talks to.
	if !v.Info.IsGroup {
		logIncoming(v, sender, who, isVIP, userMsg)
	}

	return gateway.Message{
		ID:        string(v.Info.ID),
		Sender:    senderStr,
		Chat:      v.Info.Chat.String(),
		Text:      userMsg,
		MediaType: mediaType,
		IsSelf:    isSelf,
		IsGroup:   v.Info.IsGroup,
	}, true
}

// extractTextAndMedia returns the user-visible text and a media classification.
// Captions from image/video/document messages are treated as text so Clark
// can answer; uncaptioned media still reports its kind for a polite ack.
func extractTextAndMedia(v *events.Message) (string, string) {
	if v == nil || v.Message == nil {
		return "", ""
	}
	if t := v.Message.GetConversation(); t != "" {
		return t, ""
	}
	if em := v.Message.GetExtendedTextMessage(); em != nil {
		if t := em.GetText(); t != "" {
			return t, ""
		}
	}
	msg := unwrapMessage(v.Message)
	if m := msg.GetImageMessage(); m != nil {
		return m.GetCaption(), "image"
	}
	if m := msg.GetVideoMessage(); m != nil {
		return m.GetCaption(), "video"
	}
	if m := msg.GetDocumentMessage(); m != nil {
		return m.GetCaption(), "document"
	}
	if m := msg.GetAudioMessage(); m != nil {
		return "", "audio"
	}
	if m := msg.GetStickerMessage(); m != nil {
		return "", "sticker"
	}
	// Reactions and other non-text system messages stay silent.
	return "", ""
}

// unwrapMessage peels ephemeral/view-once wrappers to reach the inner payload.
func unwrapMessage(m *waE2E.Message) *waE2E.Message {
	if m == nil {
		return nil
	}
	for {
		if em := m.GetEphemeralMessage(); em != nil && em.GetMessage() != nil {
			m = em.GetMessage()
			continue
		}
		if vom := m.GetViewOnceMessage(); vom != nil && vom.GetMessage() != nil {
			m = vom.GetMessage()
			continue
		}
		if v2 := m.GetViewOnceMessageV2(); v2 != nil && v2.GetMessage() != nil {
			m = v2.GetMessage()
			continue
		}
		break
	}
	return m
}

// filterMessage reports whether a message must be dropped, and why.
func filterMessage(v *events.Message, connectedAt time.Time) (skip bool, reason string) {
	if v == nil || v.Info.Chat.IsEmpty() || v.Info.Sender.IsEmpty() || v.Message == nil {
		return true, "nil message data"
	}
	if v.Info.Timestamp.IsZero() || v.Info.Timestamp.Before(connectedAt) {
		return true, ""
	}
	return false, ""
}

func logIncoming(v *events.Message, sender types.JID, who string, isVIP bool, content string) {
	number := sender.User
	if number == "" {
		number = sender.String()
	}

	direction := "incoming"
	if v.Info.IsFromMe {
		direction = "self-text"
	}

	chatType := "private"
	if v.Info.IsGroup {
		chatType = "group"
	}

	vip := "no"
	if isVIP {
		vip = "yes"
	}

	if content == "" {
		content = "<non-text>"
	}

	logging.Log("WHATSAPP", logging.SevInfo, "MESSAGE", "Message received",
		"from", who,
		"number", number,
		"chat", chatType,
		"vip", vip,
		"direction", direction,
		"msg", logging.Brief(content, 60))
}

func logReply(toNumber, content string) {
	logging.Log("WHATSAPP", logging.SevInfo, "SEND", "Message sent",
		"to", toNumber,
		"msg", logging.Brief(content, 60))
}
