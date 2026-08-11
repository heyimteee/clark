package gateway

// MessagePrefix brands every outbound message on WhatsApp, where the backticks
// render as monospace and a single newline flows into the body. IMessagePrefix
// is the plain-text equivalent for iMessage, which cannot render backticks, so
// it ends with a blank line to visually separate the brand from the body.
const (
	MessagePrefix  = "`🤵🏻‍♂️[CLARK]`\n"
	IMessagePrefix = "🤵🏻‍♂️[CLARK]\n\n"
)

// PrefixMessage prepends clark's WhatsApp branding to an outbound message.
func PrefixMessage(text string) string {
	return MessagePrefix + text
}

// PrefixIMessage prepends clark's plain-text branding to an iMessage.
func PrefixIMessage(text string) string {
	return IMessagePrefix + text
}
