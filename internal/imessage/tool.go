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
		"Send an iMessage to a VIP other than the Master himself, on the Master's behalf. The message is QUEUED and delivered by the Mac bridge shortly; this tool cannot confirm it arrived, so do not tell the Master it was delivered. If he asks whether it got there, call imessage_delivery_status with the returned id. Never for 'me', 'myself', or the Master — use relay_to_master for those. Only the Master may use this.",
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
			id, err := msgr.SendQueued(ctx, handle, message)
			if err != nil {
				return "", err
			}
			// Report exactly what happened. The bridge has not sent this yet, and
			// may not be able to: claiming delivery here is how a lost message
			// becomes a silent one (#215).
			return fmt.Sprintf("Queued for delivery to %s (queue id %d). "+
				"It has NOT been sent yet — the Mac bridge picks it up shortly. "+
				"Tell the Master it is on its way, not that it was delivered.", recipient, id), nil
		},
	)

	reg.RegisterFunc(
		"imessage_delivery_status",
		"Check whether a queued iMessage was actually delivered. Triggered by 'did that message get through', 'was my iMessage delivered'. Only the Master may use this.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "The queue id returned by send_imessage"},
			},
			"required": []string{"id"},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if !tools.IsMaster(ctx) {
				return "", fmt.Errorf("forbidden: only the Master may check delivery")
			}
			id := tools.IntArg(args, "id", 0)
			if id <= 0 {
				return "", fmt.Errorf("a queue id is required")
			}
			return msgr.DeliveryStatus(ctx, int64(id))
		},
	)
}
