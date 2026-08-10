package whatsapp

import (
	"context"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/logging"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Butler is the conversational brain the handler replies through.
type Butler interface {
	Reply(ctx context.Context, senderJID, text string) (string, error)
	Relation(jid string) (string, bool)
	Enabled() bool
}

// Notifier raises attention for urgent commands.
type Notifier interface {
	Notify(title, body string) error
}

// CommandFunc runs an urgent command, e.g. "get him to me".
type CommandFunc func(ctx context.Context, sender types.JID, relation string)

type command struct {
	phrase string
	run    CommandFunc
}

// Handler routes WhatsApp events through the message pipeline.
type Handler struct {
	msgr        Messenger
	butler      Butler
	notifier    Notifier
	echo        *EchoTracker
	connectedAt time.Time
	commands    []command
}

// NewHandler wires the pipeline around its dependencies.
func NewHandler(msgr Messenger, butler Butler, notifier Notifier, echo *EchoTracker, connectedAt time.Time) *Handler {
	h := &Handler{
		msgr:        msgr,
		butler:      butler,
		notifier:    notifier,
		echo:        echo,
		connectedAt: connectedAt,
	}
	h.commands = []command{
		{phrase: "get him to me", run: h.alert},
	}
	return h
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

	if v.Info.IsFromMe && !h.msgr.IsSelfChat(v.Info.Chat) {
		return
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
	logIncoming(v, sender, who, isVIP, userMsg)

	if !h.butler.Enabled() || !isVIP || v.Info.IsGroup {
		return
	}

	if userMsg == "" {
		logging.Log("WHATSAPP", logging.SevWarn, "MESSAGE", "Message discarded", "reason", "no text content")
		return
	}

	ctx := context.Background()
	lower := strings.ToLower(userMsg)
	for _, c := range h.commands {
		if strings.Contains(lower, c.phrase) {
			c.run(ctx, sender, relation)
			return
		}
	}

	aiResp, err := h.butler.Reply(ctx, senderStr, userMsg)
	if err != nil {
		logging.Log("OLLAMA", logging.SevErr, "RESPONSE", "AI response error", "error", err)
		h.msgr.Send(ctx, sender, "I apologize, but I'm experiencing technical difficulties. Please try again later.")
		return
	}
	h.msgr.Send(ctx, sender, aiResp)
}

func (h *Handler) alert(ctx context.Context, sender types.JID, relation string) {
	if err := h.notifier.Notify("Attention Sir!", relation+" needs you!"); err != nil {
		logging.Log("CLARK", logging.SevWarn, "NOTIFY", "Notification failed", "error", err)
	}
	h.msgr.SendSelf(ctx, "🚨 Attention Master!\n"+relation+" needs you!")
	h.msgr.Send(ctx, sender, "I've alerted him. One Moment.")
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
