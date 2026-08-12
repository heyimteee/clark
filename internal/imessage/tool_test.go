package imessage

import (
	"context"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/tools"
)

func TestRegisterSendMessageTool(t *testing.T) {
	reg := tools.NewRegistry()
	out := &fakeOutbound{}
	msgr := NewMessenger(out, "+6281111111111")
	nameToHandle := func(input string) (string, bool) {
		if input == "Tiara" {
			return "6281267858909@s.whatsapp.net", true
		}
		return "", false
	}
	RegisterSendMessageTool(reg, msgr, nameToHandle)

	tool, ok := regHas(reg, "send_imessage")
	if !ok {
		t.Fatalf("send_imessage tool not registered")
	}

	// Master is allowed to send.
	ctx := tools.WithMaster(context.Background())
	res, err := tool.Execute(ctx, map[string]any{"recipient": "Tiara", "message": "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res, "Tiara") {
		t.Errorf("result = %q, want mention of Tiara", res)
	}
	if len(out.enqueued) != 1 || out.enqueued[0].Recipient != "+6281267858909" {
		t.Fatalf("enqueued = %+v, want delivery to Tiara's handle", out.enqueued)
	}

	// A stranger is refused.
	res, err = tool.Execute(ctx, map[string]any{"recipient": "Someone", "message": "hello"})
	if err == nil || !strings.Contains(err.Error(), "no VIP found") {
		t.Fatalf("unknown recipient err = %v, want no VIP found", err)
	}

	// Non-master is forbidden.
	if _, err := tool.Execute(context.Background(), map[string]any{"recipient": "Tiara", "message": "hello"}); err == nil || !strings.Contains(err.Error(), "only the Master") {
		t.Fatalf("non-master err = %v, want forbidden", err)
	}

	// Missing args are rejected.
	if _, err := tool.Execute(ctx, map[string]any{"recipient": "Tiara"}); err == nil {
		t.Fatal("missing message arg accepted, want error")
	}
}

func regHas(reg *tools.Registry, name string) (tools.Tool, bool) {
	for _, t := range reg.List() {
		if t.Definition.Name == name {
			return t, true
		}
	}
	return tools.Tool{}, false
}
