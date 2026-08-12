package assistant

import (
	"context"
	"testing"
)

// fastPathPhrase is a deterministic view command the fast path would answer
// hardcoded instead of asking the model.
const fastPathPhrase = "list of tools"

// TestReplyLLMBypassesFastPath ensures the web entry point always reaches the
// model: a phrase the fast path would consume must still hit fakeLLM via
// ReplyLLM, while Reply keeps answering it hardcoded.
func TestReplyLLMBypassesFastPath(t *testing.T) {
	ctx := context.Background()

	viaLLM, _, fake := newService(t)
	text, _, err := viaLLM.ReplyLLM(ctx, "web", fastPathPhrase, true)
	if err != nil {
		t.Fatalf("ReplyLLM: %v", err)
	}
	if len(fake.got) == 0 {
		t.Fatal("ReplyLLM answered the fast-path phrase without calling the model")
	}
	if text == "" {
		t.Error("ReplyLLM returned an empty reply")
	}

	viaReply, _, fakeReply := newService(t)
	text, err = viaReply.Reply(ctx, "web", fastPathPhrase, true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if len(fakeReply.got) != 0 {
		t.Errorf("Reply called the model for a fast-path phrase (%d messages)", len(fakeReply.got))
	}
	if text == "" {
		t.Error("Reply returned an empty hardcoded reply")
	}
}

// TestWebSessionGetsAllTools verifies the reserved web session is treated as
// the Master: every registered tool is available.
func TestWebSessionGetsAllTools(t *testing.T) {
	s, _, _ := newService(t)

	web := s.toolsForSender("web", true)
	all := s.tools.List()
	if len(web) != len(all) {
		t.Fatalf("toolsForSender(web, true) returned %d tools, want %d", len(web), len(all))
	}
	seen := make(map[string]bool, len(web))
	for _, tool := range web {
		seen[tool.Definition.Name] = true
	}
	for _, tool := range all {
		if !seen[tool.Definition.Name] {
			t.Errorf("web session missing tool %q", tool.Definition.Name)
		}
	}
}

// TestWebHistoryIsolated verifies the web conversation lives under its own
// reserved jid and never bleeds into a VIP's history.
func TestWebHistoryIsolated(t *testing.T) {
	s, st, fake := newService(t)
	ctx := context.Background()

	if err := s.AddVIP("6281234567890,Tiara,Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	if _, _, err := s.ReplyLLM(ctx, "web", "hello there", true); err != nil {
		t.Fatalf("ReplyLLM: %v", err)
	}
	if len(fake.got) == 0 {
		t.Fatal("model was not called for the web turn")
	}

	webMsgs, err := st.Messages("web")
	if err != nil {
		t.Fatalf("Messages(web): %v", err)
	}
	if len(webMsgs) != 2 {
		t.Fatalf("web history has %d messages, want user+assistant = 2", len(webMsgs))
	}
	if webMsgs[0].Role != "user" || webMsgs[0].Content != "hello there" {
		t.Errorf("web history[0] = %+v, want the user turn", webMsgs[0])
	}

	vipMsgs, err := st.Messages("6281234567890@s.whatsapp.net")
	if err != nil {
		t.Fatalf("Messages(vip): %v", err)
	}
	if len(vipMsgs) != 0 {
		t.Errorf("VIP history was polluted by web chat: %d messages", len(vipMsgs))
	}
}

// TestReplyLLMRejectsEmptySenderAndMessage mirrors Reply's guards through the
// new entry point.
func TestReplyLLMRejectsBadInput(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()

	if _, _, err := s.ReplyLLM(ctx, "", "hi", true); err == nil {
		t.Error("ReplyLLM accepted an empty sender jid")
	}
	if _, _, err := s.ReplyLLM(ctx, "web", "", true); err == nil {
		t.Error("ReplyLLM accepted an empty message")
	}
}
