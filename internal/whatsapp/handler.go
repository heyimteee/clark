package whatsapp

import (
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
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

	var userMsg string
	if conversation := v.Message.GetConversation(); conversation != "" {
		userMsg = conversation
	} else if extendedMessage := v.Message.GetExtendedTextMessage(); extendedMessage != nil {
		userMsg = extendedMessage.GetText()
	}

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
		ID:      string(v.Info.ID),
		Sender:  senderStr,
		Chat:    v.Info.Chat.String(),
		Text:    userMsg,
		IsSelf:  isSelf,
		IsGroup: v.Info.IsGroup,
	}, true
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
		"msg", content)
}

func logReply(toNumber, content string) {
	logging.Log("WHATSAPP", logging.SevInfo, "SEND", "Message sent",
		"to", toNumber,
		"msg", content)
}
