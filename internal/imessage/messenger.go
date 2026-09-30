package imessage

import (
	"context"
	"fmt"
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
	_, err := m.SendQueued(context.Background(), chat, text)
	return err
}

// SendQueued queues a delivery and returns its queue id, so a caller can later
// ask whether it actually went out. Send stays for callers that do not need to
// report on the outcome.
func (m *Messenger) SendQueued(_ context.Context, chat, text string) (int64, error) {
	handle := toHandle(chat)
	if handle == "" {
		return 0, errEmptyRecipient
	}
	text = gateway.PrefixIMessage(text)
	text = stripMarkdown(text)
	id, err := m.out.EnqueueIMessage(handle, text)
	if err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "SEND", "Failed to queue iMessage", "to", handle, "error", err)
		return 0, err
	}
	logging.Log("IMESSAGE", logging.SevInfo, "SEND", "iMessage queued", "to", handle, "id", id)
	return id, nil
}

// DeliveryStatus reports what actually happened to a queued message. The bridge
// deletes a row only after the outgoing row is verified in chat.db, so a
// missing id means delivered; a `dead` row is a real failure, and a `picked` or
// `pending` row means the bridge has not finished with it yet.
//
// This is the honest answer to "did that get through", which the send tool
// structurally cannot give (#215).
func (m *Messenger) DeliveryStatus(_ context.Context, id int64) (string, error) {
	counts, err := m.out.OutboundQueueCounts()
	if err != nil {
		return "", err
	}
	dead, err := m.out.DeadIMessages(50)
	if err != nil {
		return "", err
	}
	for _, d := range dead {
		if d.ID == id {
			return fmt.Sprintf("Message %d to %s could NOT be delivered after %d attempts: %s. "+
				"Tell the Master it failed and why — do not claim it was sent.", id, d.Recipient, d.Attempts, d.LastError), nil
		}
	}
	// Still in the queue, or already gone because it was delivered.
	return fmt.Sprintf("Message %d is not in the failed list, so the bridge either delivered it or still has it in flight "+
		"(queue: %d waiting, %d in flight, %d failed). Delivery is confirmed only once the bridge has finished with it.", id,
		counts.Pending, counts.Picked, counts.Dead), nil
}

// SendSelf delivers a message to the Master's own iMessage handle. Used by
// alerts (rate limits, the "get him to me" bypass) so they reach the Master on
// iMessage as well as WhatsApp. Echo-safe: the bridge watcher only selects
// inbound rows (is_from_me = 0) and the server drops master self-chat inbound,
// so a self-sent message cannot loop back.
func (m *Messenger) SendSelf(_ context.Context, text string) error {
	if m.selfHandle == "" {
		logging.Log("IMESSAGE", logging.SevErr, "SEND", "Cannot send self iMessage", "reason", "IMESSAGE_SELF_HANDLE not set")
		return errNoSelfHandle
	}
	handle := toHandle(m.selfHandle)
	if handle == "" {
		return errEmptyRecipient
	}
	text = gateway.PrefixIMessage(text)
	text = stripMarkdown(text)
	if _, err := m.out.EnqueueIMessage(handle, text); err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "SEND", "Failed to queue self iMessage", "to", handle, "error", err)
		return err
	}
	logging.Log("IMESSAGE", logging.SevInfo, "SEND", "Self iMessage queued", "to", handle)
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
