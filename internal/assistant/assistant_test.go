package assistant

import (
	"context"
	"testing"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
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
	reply string
	err   error
	got   []ollama.Message
}

func (f *fakeLLM) Chat(_ context.Context, messages []ollama.Message) (string, error) {
	f.got = messages
	return f.reply, f.err
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

	fake := &fakeLLM{reply: "Indubitably."}
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

	got, err := s.Reply(context.Background(), jid, "Hello there")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != fake.reply {
		t.Errorf("Reply = %q, want %q", got, fake.reply)
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

func TestServiceReplyRejectsNonVIP(t *testing.T) {
	s, _, _ := newService(t)

	if _, err := s.Reply(context.Background(), "999@s.whatsapp.net", "Hello"); err == nil {
		t.Fatal("Reply succeeded for non-VIP, want error")
	}
}
