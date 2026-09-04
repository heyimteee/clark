package gateway

import "strings"

// clarkPrefixes are the outbound branding prefixes Clark prepends to his own
// messages on every transport. Inbound text starting with one of these is
// Clark's own sent message looping back (same-number self-chat, bridge echo),
// not a human's words — the handler drops it via IsClarkEcho before history.
var clarkPrefixes = []string{
	"`🤵🏻‍♂️[CLARK]`\n", // WhatsApp form (backtick monospace brand)
	"🤵🏻‍♂️[CLARK]\n\n", // iMessage plain-text form
	"🤵🏻‍♂️[CLARK]\n",
	"🤵🏻‍♂️[CLARK]",
}

// IsClarkEcho reports whether text is Clark's own branded output looping back
// inbound. Only a leading prefix counts: quoting Clark mid-text is legitimate
// and must not be dropped. Leading whitespace/newlines are tolerated because
// transports may pad echoed bodies.
func IsClarkEcho(text string) bool {
	t := strings.TrimLeft(text, " \t\n\r")
	for _, p := range clarkPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// SanitizeInbound strips Clark's own branding from the start of an inbound
// message so a sender cannot impersonate Clark in conversation history.
// Occurrences mid-text are left untouched (quoting the bot is legitimate).
func SanitizeInbound(text string) string {
	for {
		trimmed := text
		for _, p := range clarkPrefixes {
			if strings.HasPrefix(trimmed, p) {
				trimmed = strings.TrimLeft(trimmed[len(p):], " \t")
				break
			}
		}
		if trimmed == text {
			return text
		}
		text = trimmed
	}
}
