package gateway

// MessagePrefix brands every outbound message from clark. It ends with a blank
// line so the brand reads as a header on WhatsApp without relying on backtick
// monospace formatting.

const MessagePrefix = "🤵🏻‍♂️[CLARK]\n\n"

// PrefixMessage prepends clark's branding to an outbound message.
func PrefixMessage(text string) string {
	return MessagePrefix + text
}
