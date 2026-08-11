package gateway

// MessagePrefix brands every outbound message from clark.
const MessagePrefix = "`🤵🏻‍♂️[CLARK]`\n"

// PrefixMessage prepends clark's branding to an outbound message.
func PrefixMessage(text string) string {
	return MessagePrefix + text
}
