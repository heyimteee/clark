package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
)

// summarizeLLM records what SummarizeAlert sends to the model.
type summarizeLLM struct {
	gotMessages []ollama.Message
	gotTools    []ollama.Tool
	reply       string
}

func (s *summarizeLLM) SetThink(bool) {}

func (s *summarizeLLM) Chat(_ context.Context, messages []ollama.Message, tools []ollama.Tool) (*ollama.ChatResult, error) {
	s.gotMessages = messages
	s.gotTools = tools
	return &ollama.ChatResult{Content: s.reply}, nil
}

func (s *summarizeLLM) ChatStream(ctx context.Context, messages []ollama.Message, tools []ollama.Tool, fn func(string)) (*ollama.ChatResult, error) {
	return s.Chat(ctx, messages, tools)
}

func newSummarizeService(t *testing.T, llm *summarizeLLM) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := New(&config.Config{OllamaModel: "test-model"}, st, llm)
	if err != nil {
		t.Fatalf("assistant.New: %v", err)
	}
	return svc, st
}

// TestSummarizeAlertRunsToolLess is the core regression guard for #58: alert
// text must never reach a master-privileged tool loop. The model request must
// carry zero tools and the call must persist nothing to chat history.
func TestSummarizeAlertRunsToolLess(t *testing.T) {
	llm := &summarizeLLM{reply: "Disk usage at 91 percent."}
	svc, st := newSummarizeService(t, llm)

	out, err := svc.SummarizeAlert(context.Background(), "disk_high", "Disk", "91% full")
	if err != nil {
		t.Fatalf("SummarizeAlert: %v", err)
	}
	if out != "Disk usage at 91 percent." {
		t.Errorf("summary = %q", out)
	}

	if len(llm.gotTools) != 0 {
		t.Fatalf("summarizer sent %d tools to the model; want none (tool-less path)", len(llm.gotTools))
	}
	if len(llm.gotMessages) == 0 {
		t.Fatal("no messages sent")
	}
	var userMsg string
	for _, m := range llm.gotMessages {
		if m.Role == "user" {
			userMsg = m.Content
		}
	}
	for _, fact := range []string{"disk_high", "Disk", "91% full"} {
		if !strings.Contains(userMsg, fact) {
			t.Errorf("user prompt missing alert fact %q: %q", fact, userMsg)
		}
	}

	entries, err := st.AllRecentMessages(0)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("SummarizeAlert wrote %d chat_history rows; want 0 (no persistence)", len(entries))
	}
}

// TestSummarizeAlertEmptyReplyIsError verifies an empty model reply surfaces
// as an error so callers fall back to the generic template.
func TestSummarizeAlertEmptyReplyIsError(t *testing.T) {
	llm := &summarizeLLM{reply: ""}
	svc, _ := newSummarizeService(t, llm)
	if _, err := svc.SummarizeAlert(context.Background(), "x", "t", "b"); err == nil {
		t.Fatal("empty summary returned nil error; want error")
	}
}
