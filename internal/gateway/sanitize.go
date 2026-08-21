package gateway

import "strings"

// clarkPrefixes are the outbound branding prefixes Clark prepends to his own
// messages on every transport. A VIP can send text starting with one of these
// to forge Clark attribution in stored history — the system prompt tells the
// model that unprefixed lines are human and prefixed ones are Clark's own.
var clarkPrefixes = []string{
	"`🤵🏻‍♂️[CLARK]`\n", // WhatsApp form (backtick monospace brand)
	"🤵🏻‍♂️[CLARK]\n\n", // iMessage plain-text form
	"🤵🏻‍♂️[CLARK]\n",
	"🤵🏻‍♂️[CLARK]",
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
