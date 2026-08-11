package imessage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/store"
)

func TestCanonicalSender(t *testing.T) {
	cases := []struct {
		handle string
		want   string
	}{
		{"+6281267858909", "6281267858909@s.whatsapp.net"},
		{"6281267858909", "6281267858909@s.whatsapp.net"},
		{"  +6281267858909  ", "6281267858909@s.whatsapp.net"},
		{"me@icloud.com", "me@icloud.com"},
		{"", ""},
		{"abc", "abc"},
	}
	for _, c := range cases {
		if got := canonicalSender(c.handle); got != c.want {
			t.Errorf("canonicalSender(%q) = %q, want %q", c.handle, got, c.want)
		}
	}
}

func TestToHandle(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"6281267858909@s.whatsapp.net", "+6281267858909"},
		{"+6281267858909", "+6281267858909"},
		{"me@icloud.com", "me@icloud.com"},
		{"", ""},
	}
	for _, c := range cases {
		if got := toHandle(c.target); got != c.want {
			t.Errorf("toHandle(%q) = %q, want %q", c.target, got, c.want)
		}
	}
}

type fakeOutbound struct {
	enqueued []store.OutboundMessage
	err      error
}

func (f *fakeOutbound) EnqueueIMessage(recipient, text string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.enqueued = append(f.enqueued, store.OutboundMessage{Recipient: recipient, Text: text})
	return int64(len(f.enqueued)), nil
}
func (f *fakeOutbound) NextIMessageOutbound() (store.OutboundMessage, bool, error) {
	if len(f.enqueued) == 0 {
		return store.OutboundMessage{}, false, nil
	}
	msg := f.enqueued[0]
	f.enqueued = f.enqueued[1:]
	return msg, true, nil
}
func (f *fakeOutbound) AckIMessage(int64) error { return nil }
func (f *fakeOutbound) StaleIMessageOutboundIDs(_ time.Duration) ([]int64, error) {
	return nil, nil
}

func TestMessengerSendPrefixesAndConverts(t *testing.T) {
	out := &fakeOutbound{}
	m := NewMessenger(out, "+6281111111111")

	if err := m.Send(context.Background(), "6281267858909@s.whatsapp.net", "greetings"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(out.enqueued) != 1 {
		t.Fatalf("enqueued %d, want 1", len(out.enqueued))
	}
	msg := out.enqueued[0]
	if msg.Recipient != "+6281267858909" {
		t.Errorf("recipient = %q, want +6281267858909", msg.Recipient)
	}
	if !strings.HasPrefix(msg.Text, gateway.MessagePrefix) || !strings.Contains(msg.Text, "greetings") {
		t.Errorf("text = %q, want branded message", msg.Text)
	}
}

func TestMessengerSendStripsMarkdown(t *testing.T) {
	out := &fakeOutbound{}
	m := NewMessenger(out, "+6281111111111")

	err := m.Send(context.Background(), "6281267858909@s.whatsapp.net", "*Status Updated*\n\n_One moment, Master..._")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(out.enqueued) != 1 {
		t.Fatalf("enqueued %d, want 1", len(out.enqueued))
	}
	got := out.enqueued[0].Text
	if strings.Contains(got, "*") || strings.Contains(got, "_") || strings.Contains(got, "`") {
		t.Errorf("text = %q, want markdown stripped", got)
	}
	for _, want := range []string{"Status Updated", "One moment, Master...", gateway.MessagePrefix} {
		if !strings.Contains(got, want) {
			t.Errorf("text = %q, missing %q", got, want)
		}
	}
}

func TestStripMarkdown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"*bold*", "bold"},
		{"_italic_", "italic"},
		{"~strike~", "strike"},
		{"`code`", "code"},
		{"``code``", "code"},
		{"# Header", "Header"},
		{"## Sub", "Sub"},
		{"> quoted", "quoted"},
		{"*Status Updated*\n\nClark is now *On*.", "Status Updated\n\nClark is now On."},
		{"- `set_context`: do the thing", "- set_context: do the thing"},
		{"1. first\n2. second", "1. first\n2. second"},
		{"a * b", "a * b"},
		{"2*3", "2*3"},
		{"plain text", "plain text"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripMarkdown(c.in); got != c.want {
			t.Errorf("stripMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMessengerSendEmptyRecipient(t *testing.T) {
	out := &fakeOutbound{}
	m := NewMessenger(out, "+6281111111111")

	if err := m.Send(context.Background(), "   ", "hi"); !errors.Is(err, errEmptyRecipient) {
		t.Fatalf("Send(empty) err = %v, want errEmptyRecipient", err)
	}
	if len(out.enqueued) != 0 {
		t.Fatalf("enqueued %d, want 0", len(out.enqueued))
	}
}

func TestMessengerSendSelfSuppressed(t *testing.T) {
	out := &fakeOutbound{}
	m := NewMessenger(out, "+6281111111111")

	if err := m.SendSelf(context.Background(), "hello self"); err != nil {
		t.Fatalf("SendSelf: %v", err)
	}
	if len(out.enqueued) != 0 {
		t.Fatalf("enqueued %+v, want none (self alerts are WhatsApp-only)", out.enqueued)
	}
}

func TestMessengerSendSelfNoHandle(t *testing.T) {
	out := &fakeOutbound{}
	m := NewMessenger(out, "")

	if err := m.SendSelf(context.Background(), "hello"); !errors.Is(err, errNoSelfHandle) {
		t.Fatalf("SendSelf without handle err = %v, want errNoSelfHandle", err)
	}
	if len(out.enqueued) != 0 {
		t.Fatalf("enqueued %d, want 0", len(out.enqueued))
	}
}

func TestMessengerSelf(t *testing.T) {
	m := NewMessenger(&fakeOutbound{}, "+6281111111111")
	if got := m.Self(); got != "6281111111111@s.whatsapp.net" {
		t.Errorf("Self() = %q, want canonical self jid", got)
	}
}

func TestMessengerImplementsGatewayMessenger(t *testing.T) {
	var _ gateway.Messenger = NewMessenger(&fakeOutbound{}, "+6281111111111")
}
