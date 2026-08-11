package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/tools"
)

func TestSanitizeJID(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"6281234567890", "6281234567890@s.whatsapp.net", false},
		{"+6281234567890", "6281234567890@s.whatsapp.net", false},
		{" 6281234567890 ", "6281234567890@s.whatsapp.net", false},
		{"6281234567890@lid", "6281234567890@s.whatsapp.net", false},
		{"abc", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := sanitizeJID(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("sanitizeJID(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("sanitizeJID(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("sanitizeJID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func newVIP(t *testing.T) *VIP {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewVIP(st)
}

func TestVIPAddAndCheck(t *testing.T) {
	vip := newVIP(t)

	if _, ok := vip.Check("6281234567890@s.whatsapp.net"); ok {
		t.Fatal("Check matched before adding")
	}

	if err := vip.Add("6281234567890, Test, Friend"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	relation, ok := vip.Check("6281234567890@s.whatsapp.net")
	if !ok {
		t.Fatal("Check missed after adding")
	}
	if relation != "Test (Friend)" {
		t.Errorf("relation = %q, want %q", relation, "Test (Friend)")
	}
}

func TestVIPAddInvalid(t *testing.T) {
	vip := newVIP(t)

	tests := []string{
		"",
		"6281234567890",
		"abc, Test, Friend",
		"6281234567890, Test123, Friend",
		"6281234567890, Test, Friend, extra",
	}
	for _, in := range tests {
		if err := vip.Add(in); err == nil {
			t.Errorf("Add(%q) succeeded, want error", in)
		}
	}
}

func TestVIPAddTooLong(t *testing.T) {
	vip := newVIP(t)
	long := make([]byte, 101)
	for i := range long {
		long[i] = 'a'
	}
	if err := vip.Add(string(long)); err == nil {
		t.Fatal("Add(101 chars) succeeded, want error")
	}
}

func TestVIPDelete(t *testing.T) {
	vip := newVIP(t)

	if err := vip.Add("6281234567890, Test, Friend"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := vip.Delete("6281234567890"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := vip.Check("6281234567890@s.whatsapp.net"); ok {
		t.Fatal("Check matched after delete")
	}
}

type fakeLLM struct {
	results  []*ollama.ChatResult
	always   *ollama.ChatResult
	err      error
	got      []ollama.Message
	gotTools []ollama.Tool
	think    bool
}

func (f *fakeLLM) SetThink(on bool) { f.think = on }

func (f *fakeLLM) Chat(_ context.Context, messages []ollama.Message, tools []ollama.Tool) (*ollama.ChatResult, error) {
	f.got = messages
	f.gotTools = tools
	if f.err != nil {
		return nil, f.err
	}
	if len(f.results) == 0 {
		if f.always != nil {
			return f.always, nil
		}
		return &ollama.ChatResult{Content: "Indubitably."}, nil
	}
	r := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	return r, nil
}

func newService(t *testing.T) (*Service, *store.Store, *fakeLLM) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.InitDefaults(); err != nil {
		t.Fatalf("InitDefaults: %v", err)
	}
	if err := st.Set("context", "testing context"); err != nil {
		t.Fatalf("Set context: %v", err)
	}
	if err := st.Set("status", "true"); err != nil {
		t.Fatalf("Set status: %v", err)
	}

	fake := &fakeLLM{}
	s, err := New(&config.Config{DBPath: ":memory:", OllamaModel: "test-model"}, st, fake)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, st, fake
}

func TestServiceLoadsState(t *testing.T) {
	s, _, _ := newService(t)

	if s.Name() != "Clark" {
		t.Errorf("Name = %q, want Clark", s.Name())
	}
	if s.Model() != "test-model" {
		t.Errorf("Model = %q, want test-model", s.Model())
	}
	if s.Context() != "testing context" {
		t.Errorf("Context = %q, want testing context", s.Context())
	}
	if !s.Enabled() {
		t.Error("Enabled = false, want true")
	}
}

func TestServiceToggle(t *testing.T) {
	s, _, _ := newService(t)

	if err := s.Toggle(); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if s.Enabled() {
		t.Error("Enabled = true after toggle, want false")
	}
	if err := s.Toggle(); err != nil {
		t.Fatalf("Toggle back: %v", err)
	}
	if !s.Enabled() {
		t.Error("Enabled = false after second toggle, want true")
	}
}

func TestServiceSetContext(t *testing.T) {
	s, _, _ := newService(t)

	if err := s.SetContext("new context"); err != nil {
		t.Fatalf("SetContext: %v", err)
	}
	if s.Context() != "new context" {
		t.Errorf("Context = %q, want new context", s.Context())
	}
}

func TestServiceReply(t *testing.T) {
	s, st, fake := newService(t)
	jid := "6281234567890@s.whatsapp.net"

	if err := s.AddVIP("6281234567890, Test, Friend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	got, err := s.Reply(context.Background(), jid, "Hello there", false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "Indubitably." {
		t.Errorf("Reply = %q, want %q", got, "Indubitably.")
	}

	if len(fake.got) == 0 || fake.got[0].Role != "system" {
		t.Fatalf("fakeLLM got %d messages, first not system", len(fake.got))
	}

	messages, err := st.Messages(jid)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("persisted %d messages, want 2", len(messages))
	}
	if messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Errorf("roles = %q,%q, want user,assistant", messages[0].Role, messages[1].Role)
	}
}

func TestServiceReplyRunsToolCall(t *testing.T) {
	s, _, fake := newService(t)
	jid := "6281234567890@s.whatsapp.net"

	if err := s.AddVIP("6281234567890, Test, Friend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	s.Tools().RegisterFunc("echo_tool", "echoes back its text arg", map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
	}, func(_ context.Context, args map[string]any) (string, error) {
		return "echo:" + args["text"].(string), nil
	})

	fake.results = []*ollama.ChatResult{
		{ToolCalls: []ollama.ToolCall{{
			ID:       "call_1",
			Function: ollama.ToolCallFunc{Name: "echo_tool", Arguments: json.RawMessage(`{"text":"hello"}`)},
		}}},
		{Content: "The echo says echo:hello."},
	}

	got, err := s.Reply(context.Background(), jid, "echo please", false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "The echo says echo:hello." {
		t.Errorf("Reply = %q", got)
	}

	// Second model call must include the tool result message.
	msgs := fake.got
	if len(msgs) == 0 {
		t.Fatal("no messages captured")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.Content != "echo:hello" {
		t.Errorf("last message = %q (%q), want tool echo:hello", last.Role, last.Content)
	}
}

func TestServiceVIPToolGrant(t *testing.T) {
	s, _, fake := newService(t)
	jid := "6281234567890@s.whatsapp.net"

	if err := s.AddVIP("6281234567890, Test, Friend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	s.Tools().RegisterFunc("web_search", "search", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (string, error) {
		return "results", nil
	})
	s.Tools().RegisterFunc("secret_tool", "master only", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (string, error) {
		return "secret", nil
	})

	if _, err := s.Reply(context.Background(), jid, "hi", false); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	for _, tt := range fake.gotTools {
		if tt.Function.Name == "secret_tool" {
			t.Fatal("VIP saw master-only tool in the request")
		}
		if tt.Function.Name != "web_search" && tt.Function.Name != "view_history" {
			t.Errorf("unexpected tool advertised to VIP: %s", tt.Function.Name)
		}
	}
	if len(fake.gotTools) != len(defaultVIPGrants) {
		t.Errorf("got %d tools for VIP, want %d (%v)", len(fake.gotTools), len(defaultVIPGrants), defaultVIPGrants)
	}

	// Master sees everything.
	if _, err := s.Reply(context.Background(), jid, "hi", true); err != nil {
		t.Fatalf("Reply as master: %v", err)
	}
	if len(fake.gotTools) == 0 {
		t.Fatal("master got no tools")
	}
}

func TestServiceManagementToolRejectsVIP(t *testing.T) {
	s, _, _ := newService(t)

	if _, err := s.tools.Execute(context.Background(), "set_status", []byte(`{"on":false}`)); err == nil {
		t.Fatal("set_status allowed without master context, want error")
	}

	out, err := s.tools.Execute(tools.WithMaster(context.Background()), "set_status", []byte(`{"on":false}`))
	if err != nil {
		t.Fatalf("set_status with master context: %v", err)
	}
	if out == "" {
		t.Fatal("set_status returned empty result")
	}
}

func TestServiceReplyRejectsNonVIP(t *testing.T) {
	s, _, _ := newService(t)

	if _, err := s.Reply(context.Background(), "999@s.whatsapp.net", "Hello", false); err == nil {
		t.Fatal("Reply succeeded for non-VIP, want error")
	}
}

func TestServiceReplyAllowsMasterAnywhere(t *testing.T) {
	s, _, _ := newService(t)

	if _, err := s.Reply(context.Background(), "999@s.whatsapp.net", "Hello", true); err != nil {
		t.Fatalf("Reply as master for unknown jid: %v", err)
	}
}

func TestServiceLookupJID(t *testing.T) {
	s, _, _ := newService(t)
	if err := s.AddVIP("6281234567890, Test, Friend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	if jid, ok := s.LookupJID("Test"); !ok || jid != "6281234567890@s.whatsapp.net" {
		t.Errorf("LookupJID(Test) = %q, %v", jid, ok)
	}
	if jid, ok := s.LookupJID("6281234567890"); !ok || jid != "6281234567890@s.whatsapp.net" {
		t.Errorf("LookupJID(number) = %q, %v", jid, ok)
	}
	if _, ok := s.LookupJID("Nobody"); ok {
		t.Error("LookupJID(Nobody) matched")
	}
}

func TestServiceSetAccessRoundTrip(t *testing.T) {
	s, _, _ := newService(t)
	jid := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Test, Friend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	grants, ok, err := s.AccessFor(jid)
	if err != nil {
		t.Fatalf("AccessFor: %v", err)
	}
	if ok {
		t.Fatal("no access row should exist by default")
	}
	if len(grants) != len(defaultVIPGrants) {
		t.Errorf("default grants = %v, want %v", grants, defaultVIPGrants)
	}
	for _, g := range defaultVIPGrants {
		found := false
		for _, got := range grants {
			if got == g {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default grants %v missing %q", grants, g)
		}
	}

	if err := s.SetAccess(jid, []string{}); err != nil {
		t.Fatalf("SetAccess empty: %v", err)
	}
	grants, ok, err = s.AccessFor(jid)
	if err != nil {
		t.Fatalf("AccessFor: %v", err)
	}
	if !ok || len(grants) != 0 {
		t.Errorf("grants = %v (ok=%v), want empty revoke", grants, ok)
	}
}

func registerSendTool(t *testing.T, s *Service, executed *bool) {
	t.Helper()
	s.Tools().RegisterFunc("send_message", "Send a WhatsApp message to a VIP.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"recipient": map[string]any{"type": "string"},
			"message":   map[string]any{"type": "string"},
		},
		"required": []string{"recipient", "message"},
	}, func(_ context.Context, args map[string]any) (string, error) {
		if executed != nil {
			*executed = true
		}
		return "Message delivered to " + tools.StringArg(args, "recipient") + ".", nil
	})
}

func TestServiceNudgeOnNarration(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
	sent := false
	registerSendTool(t, s, &sent)

	fake.results = []*ollama.ChatResult{
		{Content: "I shall send the message to Tiara now, Master."},
		{ToolCalls: []ollama.ToolCall{{
			ID:       "call_1",
			Function: ollama.ToolCallFunc{Name: "send_message", Arguments: json.RawMessage(`{"recipient":"Tiara","message":"I love you"}`)},
		}}},
		{Content: "The message has been delivered to Tiara, Master."},
	}

	got, err := s.Reply(context.Background(), jid, "send a message to Tiara", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "The message has been delivered to Tiara, Master." {
		t.Errorf("Reply = %q, want delivered confirmation", got)
	}
	if !sent {
		t.Fatal("send_message was never executed")
	}
	foundNudge := false
	for _, m := range fake.got {
		if m.Role == "system" && strings.Contains(m.Content, "Words alone do nothing") {
			foundNudge = true
		}
	}
	if !foundNudge {
		t.Error("nudge system message was not fed back to the model")
	}
}

func TestServiceChatReplyNotNudged(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	got, err := s.Reply(context.Background(), jid, "how are you", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "Indubitably." {
		t.Errorf("Reply = %q, want Indubitably.", got)
	}
	for _, m := range fake.got {
		if m.Role == "system" && strings.Contains(m.Content, "Words alone do nothing") {
			t.Fatal("chit-chat was nudged to call a tool")
		}
	}
}

func TestServicePostToolConfirmationNotNudged(t *testing.T) {
	s, _, fake := newService(t)
	jid := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
	registerSendTool(t, s, nil)

	fake.results = []*ollama.ChatResult{
		{ToolCalls: []ollama.ToolCall{{
			ID:       "call_1",
			Function: ollama.ToolCallFunc{Name: "send_message", Arguments: json.RawMessage(`{"recipient":"Tiara","message":"I love you"}`)},
		}}},
		{Content: "The message has been sent to Tiara."},
	}

	got, err := s.Reply(context.Background(), jid, "send a message to Tiara", false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "The message has been sent to Tiara." {
		t.Errorf("Reply = %q", got)
	}
	for _, m := range fake.got {
		if m.Role == "system" && strings.Contains(m.Content, "Words alone do nothing") {
			t.Fatal("post-tool confirmation was wrongly nudged")
		}
	}
}

func TestServiceManageNarrationNudgedAndExecutes(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	fake.results = []*ollama.ChatResult{
		{Content: "Understood. Clark is now silenced, Master."},
		{ToolCalls: []ollama.ToolCall{{
			ID:       "call_1",
			Function: ollama.ToolCallFunc{Name: "set_status", Arguments: json.RawMessage(`{"on":false}`)},
		}}},
		{Content: "I have set clark's status to off, Master."},
	}

	got, err := s.Reply(context.Background(), jid, "silence", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "I have set clark's status to off, Master." {
		t.Errorf("Reply = %q, want confirmation", got)
	}
	if s.Enabled() {
		t.Fatal("status was never turned off: set_status did not execute")
	}
	foundNudge := false
	for _, m := range fake.got {
		if m.Role == "system" && strings.Contains(m.Content, "Words alone do nothing") {
			foundNudge = true
		}
	}
	if !foundNudge {
		t.Error("manage narration was not nudged")
	}
}

func TestServiceStatusQuestionNotNudged(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"
	s.Tools().RegisterFunc("web_search", "Search the web.", map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
		"required":   []string{"query"},
	}, func(_ context.Context, _ map[string]any) (string, error) {
		return "results", nil
	})

	fake.results = []*ollama.ChatResult{{Content: "I am currently operational, awaiting your command, Sir."}}

	got, err := s.Reply(context.Background(), jid, "whats the current status clark", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "I am currently operational, awaiting your command, Sir." {
		t.Errorf("Reply = %q, want the content answer", got)
	}
	for _, m := range fake.got {
		if m.Role == "system" && strings.Contains(m.Content, "Words alone do nothing") {
			t.Fatal("a genuine content answer was nudged toward a tool")
		}
	}
}

func TestServiceResearchRefusalNudged(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"
	searched := false
	s.Tools().RegisterFunc("web_search", "Search the web.", map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
		"required":   []string{"query"},
	}, func(_ context.Context, _ map[string]any) (string, error) {
		searched = true
		return "Go 1.25.5 is the latest stable release.", nil
	})

	fake.results = []*ollama.ChatResult{
		{Content: "I am unable to answer that without context, Sir."},
		{ToolCalls: []ollama.ToolCall{{
			ID:       "call_1",
			Function: ollama.ToolCallFunc{Name: "web_search", Arguments: json.RawMessage(`{"query":"latest Go version"}`)},
		}}},
		{Content: "Go 1.25.5 is the latest stable release, Sir."},
	}

	got, err := s.Reply(context.Background(), jid, "what is the current latest Go version?", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "Go 1.25.5 is the latest stable release, Sir." {
		t.Errorf("Reply = %q, want the researched answer", got)
	}
	if !searched {
		t.Fatal("web_search was never executed for a refused research question")
	}
	foundNudge := false
	for _, m := range fake.got {
		if m.Role == "system" && strings.Contains(m.Content, "Words alone do nothing") {
			foundNudge = true
		}
	}
	if !foundNudge {
		t.Error("research refusal was not nudged")
	}
}

func TestServiceNudgeExhaustionHonestMessage(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	fake.always = &ollama.ChatResult{Content: "Understood. Clark is now silenced, Master."}

	got, err := s.Reply(context.Background(), jid, "silence", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != couldNotActMessage {
		t.Errorf("Reply = %q, want honest could-not-act message %q", got, couldNotActMessage)
	}
	if s.pendingIteration(jid) != nil {
		t.Fatal("nudge exhaustion must not leave a resumable iteration")
	}
}

func TestServiceVIPClaimNotNudged(t *testing.T) {
	s, _, fake := newService(t)
	jid := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	fake.results = []*ollama.ChatResult{{Content: "I have sent the message to the Master."}}

	got, err := s.Reply(context.Background(), jid, "hello", false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "I have sent the message to the Master." {
		t.Errorf("Reply = %q, want the claim returned unchanged", got)
	}
	for _, m := range fake.got {
		if m.Role == "system" && strings.Contains(m.Content, "Words alone do nothing") {
			t.Fatal("VIP claim nudged toward a tool they cannot use")
		}
	}
}

func TestServiceWrongToolNarrationNudged(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	fake.results = []*ollama.ChatResult{
		{ToolCalls: []ollama.ToolCall{{
			ID:       "call_1",
			Function: ollama.ToolCallFunc{Name: "get_state", Arguments: json.RawMessage(`{}`)},
		}}},
		{Content: "I have set clark's status to off, Master."},
		{ToolCalls: []ollama.ToolCall{{
			ID:       "call_2",
			Function: ollama.ToolCallFunc{Name: "set_status", Arguments: json.RawMessage(`{"on":false}`)},
		}}},
		{Content: "Done, Master. Clark is now off."},
	}

	got, err := s.Reply(context.Background(), jid, "silence", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "Done, Master. Clark is now off." {
		t.Errorf("Reply = %q, want confirmation", got)
	}
	if s.Enabled() {
		t.Fatal("set_status did not execute after the wrong-tool narration")
	}
}

func TestServiceIterationLimitAndContinue(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"
	s.Tools().RegisterFunc("echo_tool", "echoes back its text arg", map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
	}, func(_ context.Context, args map[string]any) (string, error) {
		return "echo:" + args["text"].(string), nil
	})

	fake.always = &ollama.ChatResult{ToolCalls: []ollama.ToolCall{{
		ID:       "call_loop",
		Function: ollama.ToolCallFunc{Name: "echo_tool", Arguments: json.RawMessage(`{"text":"x"}`)},
	}}}

	got, err := s.Reply(context.Background(), jid, "echo please", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != iterationLimitMessage {
		t.Errorf("Reply = %q, want iteration limit message", got)
	}
	if s.pendingIteration(jid) == nil {
		t.Fatal("expected a paused iteration after hitting the limit")
	}

	fake.always = nil
	fake.results = []*ollama.ChatResult{{Content: "done"}}
	got, err = s.Reply(context.Background(), jid, "continue", true)
	if err != nil {
		t.Fatalf("Reply after continue: %v", err)
	}
	if got != "done" {
		t.Errorf("Reply after continue = %q, want done", got)
	}
	if s.pendingIteration(jid) != nil {
		t.Fatal("paused iteration not cleared after continue")
	}
}

func TestServiceHardcodedViews(t *testing.T) {
	s, _, fake := newService(t)
	masterJID := "628111@s.whatsapp.net"
	vipJID := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
	s.Tools().RegisterFunc("web_search", "Search the web.", map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
		"required":   []string{"query"},
	}, func(_ context.Context, _ map[string]any) (string, error) {
		return "results", nil
	})
	s.Tools().RegisterFunc("echo_tool", "echoes back its text arg", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (string, error) {
		return "echo", nil
	})

	tests := []struct {
		name    string
		jid     string
		msg     string
		want    []string
		notWant []string
	}{
		{"master tools", masterJID, "list of tools", []string{"web_search", "echo_tool"}, nil},
		{"master vips", masterJID, "list of vip", []string{"Tiara (Girlfriend)"}, nil},
		{"master all", masterJID, "show me everything", []string{"Status:", "Context:", "Inner Circle", "Tools available"}, nil},
		{"vip tools", vipJID, "list of tools", []string{"web_search"}, []string{"echo_tool"}},
		{"vip all denied", vipJID, "show me everything", []string{"reserved for the"}, nil},
		{"vip vips denied", vipJID, "list of vip", []string{"reserved for the"}, nil},
	}

	for _, tt := range tests {
		got, err := s.Reply(context.Background(), tt.jid, tt.msg, tt.jid == masterJID)
		if err != nil {
			t.Fatalf("%s: Reply: %v", tt.name, err)
		}
		for _, w := range tt.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: Reply missing %q in %q", tt.name, w, got)
			}
		}
		for _, nw := range tt.notWant {
			if strings.Contains(got, nw) {
				t.Errorf("%s: Reply should not contain %q in %q", tt.name, nw, got)
			}
		}
	}

	if len(fake.got) != 0 {
		t.Error("LLM was called for hardcoded views")
	}
}

func TestServiceViewClearsPending(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"
	s.Tools().RegisterFunc("echo_tool", "echoes back its text arg", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (string, error) {
		return "echo", nil
	})

	fake.always = &ollama.ChatResult{ToolCalls: []ollama.ToolCall{{
		ID:       "call_loop",
		Function: ollama.ToolCallFunc{Name: "echo_tool", Arguments: json.RawMessage(`{"text":"x"}`)},
	}}}
	if _, err := s.Reply(context.Background(), jid, "echo please", true); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if s.pendingIteration(jid) == nil {
		t.Fatal("expected a paused iteration")
	}

	if _, err := s.Reply(context.Background(), jid, "list of tools", true); err != nil {
		t.Fatalf("Reply view: %v", err)
	}
	if s.pendingIteration(jid) != nil {
		t.Fatal("paused iteration not cleared by a view request")
	}
}

func TestServiceContinueWithoutPending(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	got, err := s.Reply(context.Background(), jid, "continue", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "Indubitably." {
		t.Errorf("Reply = %q, want Indubitably.", got)
	}
	if len(fake.got) == 0 {
		t.Fatal("LLM not called for continue without pending")
	}
}

func TestServiceFastPathWakeUpBuddy(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	for _, phrase := range []string{"wake up buddy", "wake clark"} {
		if _, err := s.Reply(context.Background(), jid, phrase, true); err != nil {
			t.Fatalf("Reply %q: %v", phrase, err)
		}
		if !s.Enabled() {
			t.Fatalf("status still off after %q", phrase)
		}
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for a fast-path mutation")
	}
}

func TestServiceFastPathClearContext(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	if _, err := s.Reply(context.Background(), jid, "set my context to in the garden", true); err != nil {
		t.Fatalf("Reply set context: %v", err)
	}
	if s.Context() != "in the garden" {
		t.Fatalf("Context = %q, want set before clearing", s.Context())
	}

	got, err := s.Reply(context.Background(), jid, "clear context", true)
	if err != nil {
		t.Fatalf("Reply clear context: %v", err)
	}
	if s.Context() != "" {
		t.Fatalf("Context = %q, want empty after 'clear context'", s.Context())
	}
	if !strings.Contains(got, "*Context Cleared*") {
		t.Errorf("Reply = %q, want *Context Cleared* confirmation", got)
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for a fast-path mutation")
	}
}

func TestServiceClearVIPs(t *testing.T) {
	s, _, _ := newService(t)
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
	if err := s.SetAccess("6281234567890@s.whatsapp.net", []string{"web_search"}); err != nil {
		t.Fatalf("SetAccess: %v", err)
	}

	if err := s.ClearVIPs(); err != nil {
		t.Fatalf("ClearVIPs: %v", err)
	}
	if len(s.VIPList()) != 0 {
		t.Fatalf("VIPList = %v, want empty", s.VIPList())
	}
	if _, ok, err := s.AccessFor("6281234567890@s.whatsapp.net"); err != nil || ok {
		t.Fatalf("access grant survived ClearVIPs: ok=%v err=%v, want gone", ok, err)
	}
}

func TestServiceFastPathClearVIPs(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	got, err := s.Reply(context.Background(), jid, "clear vips", true)
	if err != nil {
		t.Fatalf("Reply clear vips: %v", err)
	}
	if len(s.VIPList()) != 0 {
		t.Fatalf("VIPList = %v, want empty after 'clear vips'", s.VIPList())
	}
	if !strings.Contains(got, "*Inner Circle Cleared*") {
		t.Errorf("Reply = %q, want *Inner Circle Cleared* confirmation", got)
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for a fast-path mutation")
	}
}

func TestServiceFastPathGuidanceMasterOnly(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	for _, phrase := range []string{"show commands", "tool guidance", "help"} {
		got, err := s.Reply(context.Background(), jid, phrase, true)
		if err != nil {
			t.Fatalf("Reply %q: %v", phrase, err)
		}
		if !strings.Contains(got, "Manual") || !strings.Contains(got, "wake up buddy") {
			t.Errorf("Reply %q = %q, want the Master's manual with hardcoded commands", phrase, got)
		}
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for guidance")
	}
}

func TestServiceFastPathGuidanceVIPFallsThroughToModel(t *testing.T) {
	s, _, fake := newService(t)
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	got, err := s.Reply(context.Background(), "6281234567890@s.whatsapp.net", "show commands", false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if strings.Contains(got, "Manual") || strings.Contains(got, "wake up buddy") {
		t.Errorf("VIP received the Master's manual: %q", got)
	}
	if len(fake.got) == 0 {
		t.Fatal("VIP guidance should fall through to the model, not be served hardcoded")
	}
}

func TestServiceSetThinking(t *testing.T) {
	s, st, fake := newService(t)

	if s.Thinking() {
		t.Fatal("thinking on by default, want off")
	}
	if err := s.SetThinking(true); err != nil {
		t.Fatalf("SetThinking(true): %v", err)
	}
	if !s.Thinking() || !fake.think {
		t.Fatalf("Thinking = %v, llm.think = %v, want both true", s.Thinking(), fake.think)
	}

	if err := s.ToggleThinking(); err != nil {
		t.Fatalf("ToggleThinking: %v", err)
	}
	if s.Thinking() || fake.think {
		t.Fatalf("Thinking = %v, llm.think = %v, want both false after toggle", s.Thinking(), fake.think)
	}

	got, err := st.Get("think")
	if err != nil {
		t.Fatalf("Get(think): %v", err)
	}
	if got != "false" {
		t.Fatalf("persisted think = %q, want false", got)
	}
}

func TestServiceFastPathThinking(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	for _, phrase := range []string{"thinking mode on", "reasoning mode on", "turn thinking on"} {
		got, err := s.Reply(context.Background(), jid, phrase, true)
		if err != nil {
			t.Fatalf("Reply %q: %v", phrase, err)
		}
		if !s.Thinking() {
			t.Fatalf("thinking still off after %q", phrase)
		}
		if !strings.Contains(got, "*On*") {
			t.Errorf("Reply %q = %q, want on confirmation", phrase, got)
		}
	}

	got, err := s.Reply(context.Background(), jid, "thinking mode off", true)
	if err != nil {
		t.Fatalf("Reply off: %v", err)
	}
	if s.Thinking() {
		t.Fatal("thinking still on after 'thinking mode off'")
	}
	if !strings.Contains(got, "*Off*") {
		t.Errorf("off Reply = %q, want off confirmation", got)
	}

	before := s.Thinking()
	if _, err := s.Reply(context.Background(), jid, "toggle thinking", true); err != nil {
		t.Fatalf("Reply toggle: %v", err)
	}
	if s.Thinking() == before {
		t.Fatal("thinking unchanged after 'toggle thinking'")
	}

	if len(fake.got) != 0 {
		t.Fatal("LLM was called for fast-path mutations")
	}
}

func TestServiceSetHistoryLimit(t *testing.T) {
	s, st, _ := newService(t)

	if s.HistoryLimit() != defaultHistoryLimit {
		t.Fatalf("HistoryLimit = %d, want default %d", s.HistoryLimit(), defaultHistoryLimit)
	}
	if err := s.SetHistoryLimit(5); err != nil {
		t.Fatalf("SetHistoryLimit(5): %v", err)
	}
	if s.HistoryLimit() != 5 {
		t.Fatalf("HistoryLimit = %d, want 5", s.HistoryLimit())
	}
	if err := s.SetHistoryLimit(0); err == nil {
		t.Fatal("SetHistoryLimit(0) succeeded, want error")
	}

	persisted, err := st.Get("history_limit")
	if err != nil {
		t.Fatalf("Get(history_limit): %v", err)
	}
	if persisted != "5" {
		t.Fatalf("persisted history_limit = %q, want 5", persisted)
	}

	reloaded, err := New(&config.Config{DBPath: ":memory:", OllamaModel: "test-model"}, st, &fakeLLM{})
	if err != nil {
		t.Fatalf("New reload: %v", err)
	}
	if reloaded.HistoryLimit() != 5 {
		t.Errorf("reloaded HistoryLimit = %d, want 5", reloaded.HistoryLimit())
	}
}

func TestServiceFastPathHistoryLimit(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	got, err := s.Reply(context.Background(), jid, "set history limit to 5", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if s.HistoryLimit() != 5 {
		t.Fatalf("HistoryLimit = %d, want 5", s.HistoryLimit())
	}
	if !strings.Contains(got, "*History Limit Updated*") {
		t.Errorf("Reply = %q, want history limit confirmation", got)
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for a fast-path mutation")
	}
}

func TestServiceReplyInjectsLimitedHistory(t *testing.T) {
	s, st, fake := newService(t)
	jid := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
	if err := s.SetHistoryLimit(2); err != nil {
		t.Fatalf("SetHistoryLimit: %v", err)
	}
	for _, m := range []string{"m1", "m2", "m3"} {
		if err := st.SaveMessage(jid, "user", m); err != nil {
			t.Fatalf("SaveMessage: %v", err)
		}
	}
	fake.always = &ollama.ChatResult{Content: "Noted."}
	if _, err := s.Reply(context.Background(), jid, "hello", false); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	userCount := 0
	for _, m := range fake.got {
		if m.Role == "user" {
			userCount++
		}
	}
	// Only the 2 most recent (m3 + the just-saved hello) should be injected.
	if userCount != 2 {
		t.Fatalf("user messages in context = %d, want 2 (m3 + hello)", userCount)
	}
	joined := ""
	for _, m := range fake.got {
		if m.Role == "user" {
			joined += m.Content + "|"
		}
	}
	if !strings.Contains(joined, "m3|hello|") || strings.Contains(joined, "m1|") || strings.Contains(joined, "m2|") {
		t.Errorf("injected window = %q, want only m3 then hello", joined)
	}
}

func TestServiceHistoryTools(t *testing.T) {
	s, st, _ := newService(t)
	jid := "6281234567890@s.whatsapp.net"
	master := "628111@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
	for i, m := range []string{"one", "two", "three"} {
		if err := st.SaveMessage(jid, "user", m); err != nil {
			t.Fatalf("SaveMessage(%d): %v", i, err)
		}
	}
	if err := st.SaveMessage(jid, "assistant", "Welcome."); err != nil {
		t.Fatalf("SaveMessage assistant: %v", err)
	}

	// A VIP sees their own full history with speaker labels.
	ctx := tools.WithSender(context.Background(), jid)
	got, err := s.Tools().Execute(ctx, "view_history", nil)
	if err != nil {
		t.Fatalf("view_history: %v", err)
	}
	for _, want := range []string{
		"Tiara (Girlfriend): one",
		"Tiara (Girlfriend): three",
		s.name + ": Welcome.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("view_history missing %q:\n%s", want, got)
		}
	}

	// A limited window drops the oldest.
	got, err = s.Tools().Execute(ctx, "view_history", []byte(`{"limit":2}`))
	if err != nil {
		t.Fatalf("view_history(limit=2): %v", err)
	}
	if strings.Contains(got, ": one") {
		t.Errorf("limit=2 should drop the oldest message:\n%s", got)
	}

	// A VIP may not view another chat.
	if _, err := s.Tools().Execute(ctx, "view_history", []byte(`{"recipient":"6281234567890"}`)); err == nil {
		t.Fatal("VIP viewing another chat should fail")
	}

	// The Master may view any chat by name.
	mctx := tools.WithMaster(tools.WithSender(context.Background(), master))
	if _, err := s.Tools().Execute(mctx, "view_history", []byte(`{"recipient":"Tiara"}`)); err != nil {
		t.Fatalf("master view_history(recipient): %v", err)
	}

	// view_all_history is Master-only.
	if _, err := s.Tools().Execute(ctx, "view_all_history", nil); err == nil {
		t.Fatal("VIP view_all_history should fail")
	}
	got, err = s.Tools().Execute(mctx, "view_all_history", nil)
	if err != nil {
		t.Fatalf("master view_all_history: %v", err)
	}
	if !strings.Contains(got, "three") {
		t.Errorf("view_all_history missing stored message:\n%s", got)
	}

	// set_history_limit is Master-only and persists.
	if _, err := s.Tools().Execute(ctx, "set_history_limit", []byte(`{"limit":3}`)); err == nil {
		t.Fatal("VIP set_history_limit should fail")
	}
	got, err = s.Tools().Execute(mctx, "set_history_limit", []byte(`{"limit":3}`))
	if err != nil {
		t.Fatalf("set_history_limit: %v", err)
	}
	if !strings.Contains(got, "3 most recent") {
		t.Errorf("set_history_limit reply = %q, want confirmation", got)
	}
	if s.HistoryLimit() != 3 {
		t.Fatalf("HistoryLimit = %d, want 3", s.HistoryLimit())
	}
	persisted, err := st.Get("history_limit")
	if err != nil {
		t.Fatalf("Get(history_limit): %v", err)
	}
	if persisted != "3" {
		t.Errorf("persisted history_limit = %q, want 3", persisted)
	}
}

func TestPromptNoRoleplay(t *testing.T) {
	prompt := strings.ToLower(promptTemplate)
	for _, want := range []string{
		"no bows",
		"no roleplay",
		"conversational language",
		"not performing",
		"🤵🏻‍♂️[clark]",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing no-roleplay/identity directive %q", want)
		}
	}
	if strings.Contains(prompt, "elegant bow") {
		t.Error("prompt still instructs a ritual bow")
	}
}

func TestPromptHistoryRules(t *testing.T) {
	for _, want := range []string{
		"History-First",
		"view_history",
		"view_all_history",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("prompt missing history directive %q", want)
		}
	}
}

func TestServiceFastPathStatusOff(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	got, err := s.Reply(context.Background(), jid, "clark status off", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if s.Enabled() {
		t.Fatal("status still enabled after 'clark status off'")
	}
	if !strings.Contains(got, "*Off*") {
		t.Errorf("Reply = %q, want rich-text Off confirmation", got)
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for a fast-path mutation")
	}
}

func TestServiceFastPathStatusOnAndToggle(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	got, err := s.Reply(context.Background(), jid, "set status to off", true)
	if err != nil {
		t.Fatalf("Reply off: %v", err)
	}
	if s.Enabled() {
		t.Fatal("status still enabled after 'set status to off'")
	}
	if !strings.Contains(got, "*Off*") {
		t.Errorf("off Reply = %q, want rich-text Off confirmation", got)
	}

	got, err = s.Reply(context.Background(), jid, "turn clark on", true)
	if err != nil {
		t.Fatalf("Reply on: %v", err)
	}
	if !s.Enabled() {
		t.Fatal("status still off after 'turn clark on'")
	}
	if !strings.Contains(got, "*On*") {
		t.Errorf("on Reply = %q, want rich-text On confirmation", got)
	}

	before := s.Enabled()
	if _, err := s.Reply(context.Background(), jid, "toggle clark", true); err != nil {
		t.Fatalf("Reply toggle: %v", err)
	}
	if s.Enabled() == before {
		t.Fatal("status unchanged after toggle")
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for fast-path mutations")
	}
}

func TestServiceFastPathContext(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	got, err := s.Reply(context.Background(), jid, "set my context to busy in a meeting until noon", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if s.Context() != "busy in a meeting until noon" {
		t.Errorf("Context = %q, want updated context", s.Context())
	}
	if !strings.Contains(got, "busy in a meeting until noon") {
		t.Errorf("Reply = %q, want new context echoed", got)
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for a fast-path mutation")
	}
}

func TestServiceFastPathAddDeleteVIP(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	got, err := s.Reply(context.Background(), jid, "add vip 6281234567890, Tiara, Girlfriend", true)
	if err != nil {
		t.Fatalf("Reply add: %v", err)
	}
	if _, ok := s.vip.Check("6281234567890@s.whatsapp.net"); !ok {
		t.Fatal("Tiara not added")
	}
	if !strings.Contains(got, "Tiara") {
		t.Errorf("add Reply = %q, want Tiara welcome", got)
	}

	got, err = s.Reply(context.Background(), jid, "delete vip tiara", true)
	if err != nil {
		t.Fatalf("Reply delete: %v", err)
	}
	if _, ok := s.vip.Check("6281234567890@s.whatsapp.net"); ok {
		t.Fatal("Tiara still present after delete")
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for fast-path mutations")
	}
}

func TestServiceFastPathAccess(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	got, err := s.Reply(context.Background(), jid, "give tiara access to web_search", true)
	if err != nil {
		t.Fatalf("Reply grant: %v", err)
	}
	grants, _, err := s.AccessFor("6281234567890@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(grants, "web_search") {
		t.Fatalf("grants = %v, want web_search", grants)
	}
	if !strings.Contains(got, "*granted*") {
		t.Errorf("grant Reply = %q, want rich-text grant confirmation", got)
	}

	got, err = s.Reply(context.Background(), jid, "revoke tiara's access to web_search", true)
	if err != nil {
		t.Fatalf("Reply revoke: %v", err)
	}
	grants, _, _ = s.AccessFor("6281234567890@s.whatsapp.net")
	if contains(grants, "web_search") {
		t.Fatalf("grants = %v, want revoked", grants)
	}
	if !strings.Contains(got, "*revoked*") {
		t.Errorf("revoke Reply = %q, want rich-text revoke confirmation", got)
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for fast-path mutations")
	}
}

func TestServiceFastPathMasterOnly(t *testing.T) {
	s, _, fake := newService(t)
	vipJID := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	got, err := s.Reply(context.Background(), vipJID, "set status to off", false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if !s.Enabled() {
		t.Fatal("a VIP changed clark's status")
	}
	if got != "Indubitably." {
		t.Errorf("Reply = %q, want model reply for a VIP, not a fast-path command", got)
	}
	if len(fake.got) == 0 {
		t.Fatal("LLM not called for a VIP command")
	}
}

func TestServicePrehandleMutationPersists(t *testing.T) {
	s, _, fake := newService(t)
	jid := "628111@s.whatsapp.net"

	got, handled, err := s.Prehandle(jid, "set status to off", true)
	if err != nil {
		t.Fatalf("Prehandle: %v", err)
	}
	if !handled {
		t.Fatal("Prehandle did not handle a mutation")
	}
	if !strings.Contains(got, "*Off*") {
		t.Errorf("Prehandle = %q, want rich-text Off confirmation", got)
	}
	if s.Enabled() {
		t.Fatal("status still enabled after Prehandle")
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for a Prehandle command")
	}
	msgs, err := s.history.Messages(jid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("Prehandle did not persist the exchange to history")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestFormatExceptionVisitors(t *testing.T) {
	people := []config.Person{
		{Name: "Tiara", Relation: "Girlfriend"},
		{Name: "Anang", Relation: "Father"},
		{Name: "Renni", Relation: "Mother"},
		{Name: "Aziz", Relation: "Bestfriend"},
	}
	got := formatExceptionVisitors(people)
	want := "Tiara (Girlfriend), Anang (Father), Renni (Mother), Aziz (Bestfriend)"
	if got != want {
		t.Errorf("formatExceptionVisitors = %q, want %q", got, want)
	}

	if got := formatExceptionVisitors(nil); got != "" {
		t.Errorf("formatExceptionVisitors(nil) = %q, want empty", got)
	}
}

func TestPromptCarriesPersonaConfig(t *testing.T) {
	s, _, fake := newService(t)
	jid := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
	fake.always = &ollama.ChatResult{Content: "Welcome."}

	if _, err := s.Reply(context.Background(), jid, "hello", false); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	sys := systemPromptOf(fake)
	if !strings.Contains(sys, "Head Butler to the Master.") {
		t.Errorf("default persona not applied: %q", sys)
	}
	if strings.Contains(sys, "Sir Tristan") || strings.Contains(sys, "Basori") {
		t.Error("prompt leaked default personal data")
	}
	if strings.Contains(sys, "The Basori Protocol") {
		t.Error("default protocol name leaked into prompt")
	}
	if strings.Contains(sys, "EXCEPTION VISITOR") {
		t.Error("exception-visitor section rendered when none configured")
	}
}

func TestPromptPersonaConfigured(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.InitDefaults(); err != nil {
		t.Fatalf("InitDefaults: %v", err)
	}
	if err := st.Set("context", "testing context"); err != nil {
		t.Fatalf("Set context: %v", err)
	}
	if err := st.Set("status", "true"); err != nil {
		t.Fatalf("Set status: %v", err)
	}

	cfg := &config.Config{
		DBPath:       ":memory:",
		OllamaModel:  "test-model",
		MasterName:   "Sir Tristan Al Harrish Basori",
		ProtocolName: "Basori",
		PalaceName:   "Basori Digital Palace",
		BypassPhrase: "get him to me",
		InnerCircle: []config.Person{
			{Name: "Tiara", Relation: "Girlfriend"},
			{Name: "Anang", Relation: "Father"},
		},
	}
	fake := &fakeLLM{}
	s, err := New(cfg, st, fake)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake.always = &ollama.ChatResult{Content: "Welcome."}

	jid := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
	if _, err := s.Reply(context.Background(), jid, "hello", false); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	sys := systemPromptOf(fake)
	for _, want := range []string{
		"Head Butler to Sir Tristan Al Harrish Basori.",
		"The Basori Protocol",
		"the Basori Digital Palace (Whatsapp)",
		"Tiara (Girlfriend), Anang (Father)",
		"get him to me",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("configured persona missing %q", want)
		}
	}
}

func TestPromptConversationalResponsiveness(t *testing.T) {
	for _, want := range []string{
		"Conversational Responsiveness",
		"laugh along",
		"foul or rude language",
		"never repeat the word",
		"Small talk deserves a real reply",
		"actually listen",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("prompt missing conversational directive %q", want)
		}
	}
}

func TestPromptWhatsAppRichText(t *testing.T) {
	for _, want := range []string{
		"WhatsApp rich text",
		"*bold*",
		"_italics_",
		"`code`",
		"> for quotes",
		"- for bulleted lines",
	} {
		if !strings.Contains(promptTemplate, want) {
			t.Errorf("prompt missing WhatsApp rich-text directive %q", want)
		}
	}
}

func systemPromptOf(fake *fakeLLM) string {
	if len(fake.got) == 0 || fake.got[0].Role != "system" {
		return ""
	}
	return fake.got[0].Content
}

func TestServiceFirstTurnGreetsVisitor(t *testing.T) {
	s, _, fake := newService(t)
	fake.always = &ollama.ChatResult{Content: "Welcome."}
	jid := "6281234567890@s.whatsapp.net"
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}

	if _, err := s.Reply(context.Background(), jid, "hello", false); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	sys := systemPromptOf(fake)
	if !strings.Contains(sys, "A Visitor has arrived") {
		t.Error("first visitor turn lacks the greeting directive")
	}
	if strings.Contains(sys, "Continue the ongoing conversation") {
		t.Error("first visitor turn wrongly told to continue an ongoing conversation")
	}
}

func TestServiceFirstTurnMasterNoStatusRecital(t *testing.T) {
	s, _, fake := newService(t)
	fake.always = &ollama.ChatResult{Content: "At your service, Sir."}
	jid := "628111@s.whatsapp.net"

	if _, err := s.Reply(context.Background(), jid, "hello", true); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	sys := systemPromptOf(fake)
	if !strings.Contains(sys, "Master has arrived") {
		t.Error("first master turn lacks the master greeting directive")
	}
	if strings.Contains(sys, "The Visitor has just arrived") || strings.Contains(sys, "A Visitor has arrived") {
		t.Error("master was greeted as a visitor")
	}
	if strings.Contains(sys, "never announce your own status") == false {
		t.Error("master greeting does not forbid status recitals to the Master")
	}
}

func TestServiceFollowUpNoBoilerplate(t *testing.T) {
	s, _, fake := newService(t)
	fake.always = &ollama.ChatResult{Content: "Right away."}
	jid := "628111@s.whatsapp.net"

	if _, err := s.Reply(context.Background(), jid, "whats the news at malang today", true); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if _, err := s.Reply(context.Background(), jid, "give me the details", true); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	sys := systemPromptOf(fake)
	if !strings.Contains(sys, "Continue the ongoing conversation") {
		t.Error("follow-up lacks the continue-directive")
	}
	if strings.Contains(sys, "Visitor has just arrived") {
		t.Error("follow-up turn still greeted as a new visitor")
	}
}
