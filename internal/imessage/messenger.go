package imessage

import (
	"context"
	"regexp"
	"strings"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
)

// Messenger sends outbound iMessages by enqueueing them for the macOS bridge.
// It implements gateway.Messenger. Chats are canonical VIP identities (phone
// JIDs like "628...@s.whatsapp.net" or email handles) and are converted to the
// +<digits> handle the bridge's osascript can deliver to.
type Messenger struct {
	out        OutboundStore
	selfHandle string
}

// NewMessenger wraps an outbound queue. selfHandle is the Master's own
// iMessage handle (e.g. "+6281111111111"), used for SendSelf routing.
func NewMessenger(out OutboundStore, selfHandle string) *Messenger {
	return &Messenger{out: out, selfHandle: selfHandle}
}

// Self returns clark's own identity as a canonical gateway sender.
func (m *Messenger) Self() string {
	return canonicalSender(m.selfHandle)
}

// Send queues a delivery to chat.
func (m *Messenger) Send(_ context.Context, chat, text string) error {
	handle := toHandle(chat)
	if handle == "" {
		return errEmptyRecipient
	}
	text = gateway.PrefixIMessage(text)
	text = stripMarkdown(text)
	if _, err := m.out.EnqueueIMessage(handle, text); err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "SEND", "Failed to queue iMessage", "to", handle, "error", err)
		return err
	}
	logging.Log("IMESSAGE", logging.SevInfo, "SEND", "iMessage queued", "to", handle)
	return nil
}

// SendSelf is intentionally a no-op: alerts (rate limits, the "get him to me"
// bypass) reach the Master on WhatsApp only, not on iMessage. It keeps the
// gateway.Messenger contract while never enqueueing an iMessage to the Master.
func (m *Messenger) SendSelf(_ context.Context, text string) error {
	if m.selfHandle == "" {
		logging.Log("IMESSAGE", logging.SevErr, "SEND", "Cannot send self iMessage", "reason", "IMESSAGE_SELF_HANDLE not set")
		return errNoSelfHandle
	}
	logging.Log("IMESSAGE", logging.SevInfo, "SEND", "Self iMessage suppressed; alerts are WhatsApp-only", "text", text)
	return nil
}

// canonicalSender normalizes a chat.db handle into the canonical identity the
// gateway, VIP table, and history key on. A phone handle becomes the same
// address as its WhatsApp JID so a person on both transports shares one VIP
// entry; an email handle passes through untouched.
func canonicalSender(handle string) string {
	h := strings.TrimSpace(handle)
	if h == "" || strings.Contains(h, "@") {
		return h
	}
	digits := nonDigits.ReplaceAllString(h, "")
	if digits == "" {
		return h
	}
	return digits + "@s.whatsapp.net"
}

// toHandle converts a canonical identity (or already-handle address) into the
// "+<digits>" form the bridge delivers to. Email addresses pass through.
func toHandle(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	if !strings.Contains(t, "@") {
		return t
	}
	local := strings.SplitN(t, "@", 2)[0]
	if digitsOnly(local) {
		return "+" + local
	}
	return t
}

// stripMarkdown removes WhatsApp-style rich-text markers so iMessage, which
// does not render them, reads as clean plain text. Formatting is WhatsApp-only:
// the assistant still emits markdown and this messenger is the only place it
// gets removed. Only paired, non-whitespace delimiters are unwrapped, so
// literals like "2*3" or "a * b" pass through untouched.
func stripMarkdown(s string) string {
	for _, re := range markdownStrippers {
		s = re.ReplaceAllString(s, "$1")
	}
	return s
}

// markdownStrippers apply in order: block constructs first, then inline code
// (double-backtick before single so multi-char code spans survive), then the
// inline emphasis delimiters.
var markdownStrippers = []*regexp.Regexp{
	// "# Title" -> "Title"
	regexp.MustCompile(`(?m)^#{1,6}\s+(.*)$`),
	// "> quote" -> "quote"
	regexp.MustCompile(`(?m)^>\s?(.*)$`),
	// "``code``" -> "code"
	regexp.MustCompile("``([^`]+)``"),
	// "`code`" -> "code"
	regexp.MustCompile("`([^`]+)`"),
	// "*bold*" -> "bold"
	regexp.MustCompile(`\*([^*\n]+)\*`),
	// "_italic_" -> "italic"
	regexp.MustCompile(`_([^_\n]+)_`),
	// "~strike~" -> "strike"
	regexp.MustCompile(`~([^~\n]+)~`),
}

var nonDigits = regexp.MustCompile(`[^0-9]`)

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
