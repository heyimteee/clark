package imessage

import (
	"context"
	"fmt"

	"github.com/heyimteee/clark/internal/tools"
)

// RegisterSendMessageTool wires the send_imessage capability, which lets the
// Master have clark deliver an iMessage to a VIP by name or number.
func RegisterSendMessageTool(reg *tools.Registry, msgr *Messenger, nameToHandle func(string) (string, bool)) {
	reg.RegisterFunc(
		"send_imessage",
		"Send an iMessage to a VIP on the Master's behalf. Only the Master may use this.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"recipient": map[string]any{"type": "string", "description": "A VIP's name or phone number"},
				"message":   map[string]any{"type": "string", "description": "The message text to deliver"},
			},
			"required": []string{"recipient", "message"},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if !tools.IsMaster(ctx) {
				return "", fmt.Errorf("forbidden: only the Master may send messages")
			}
			recipient := tools.StringArg(args, "recipient")
			message := tools.StringArg(args, "message")
			if recipient == "" || message == "" {
				return "", fmt.Errorf("recipient and message are required")
			}

			handle, ok := nameToHandle(recipient)
			if !ok {
				return "", fmt.Errorf("no VIP found matching %q", recipient)
			}
			if err := msgr.Send(ctx, handle, message); err != nil {
				return "", err
			}
			return "Message delivered to " + recipient + ".", nil
		},
	)
}
