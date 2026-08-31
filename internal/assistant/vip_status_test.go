package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/ollama"
)

const (
	testMasterJID = "628111@s.whatsapp.net"
	testVIPJID    = "6281234567890@s.whatsapp.net"
)

func addTestVIP(t *testing.T, s *Service) {
	t.Helper()
	if err := s.AddVIP("6281234567890, Tiara, Girlfriend"); err != nil {
		t.Fatalf("AddVIP: %v", err)
	}
}

func TestServiceEnabledForOverrideWins(t *testing.T) {
	s, _, _ := newService(t)
	addTestVIP(t, s)

	if !s.Enabled() {
		t.Fatal("precondition: global status must start on")
	}
	if !s.EnabledFor(testVIPJID) {
		t.Error("EnabledFor(VIP) = false with no override and global on")
	}
	if !s.EnabledFor(testMasterJID) {
		t.Error("EnabledFor(Master) = false with no override and global on")
	}

	if err := s.SetVIPStatus("Tiara", false); err != nil {
		t.Fatalf("SetVIPStatus(off): %v", err)
	}
	if s.EnabledFor(testVIPJID) {
		t.Error("EnabledFor(VIP) = true after personal off override")
	}
	if !s.EnabledFor(testMasterJID) {
		t.Error("EnabledFor(Master) = false; per-VIP override leaked to the Master")
	}
	if !s.Enabled() {
		t.Error("global status changed by a per-VIP override")
	}

	if err := s.SetVIPStatus("Tiara", true); err != nil {
		t.Fatalf("SetVIPStatus(on): %v", err)
	}
	if !s.EnabledFor(testVIPJID) {
		t.Error("EnabledFor(VIP) = false after personal on override")
	}
}

func TestServiceSetStatusClearsOverrides(t *testing.T) {
	s, _, _ := newService(t)
	addTestVIP(t, s)

	if err := s.SetStatus(false); err != nil {
		t.Fatalf("SetStatus(false): %v", err)
	}
	if err := s.SetVIPStatus("Tiara", true); err != nil {
		t.Fatalf("SetVIPStatus(on): %v", err)
	}
	if !s.EnabledFor(testVIPJID) {
		t.Fatal("precondition: per-VIP override must be active")
	}

	if err := s.SetStatus(true); err != nil {
		t.Fatalf("SetStatus(true): %v", err)
	}
	if !s.Enabled() {
		t.Error("global status still off after SetStatus(true)")
	}
	if len(s.vip.EnabledMap()) != 0 {
		t.Errorf("overrides survived a global SetStatus: %v", s.vip.EnabledMap())
	}
	if !s.EnabledFor(testVIPJID) {
		t.Error("EnabledFor(VIP) = false; override cleared but global on should apply")
	}
}

func TestServiceFastPathPerVIPStatus(t *testing.T) {
	t.Skip("fastPath removed except viewAll for v6.1.0")
	s, _, fake := newService(t)
	addTestVIP(t, s)
	if err := s.SetStatus(false); err != nil {
		t.Fatalf("SetStatus(false): %v", err)
	}

	got, err := s.Reply(context.Background(), testMasterJID, "wake clark for Tiara", true)
	if err != nil {
		t.Fatalf("Reply wake: %v", err)
	}
	if s.EnabledFor(testVIPJID) == false {
		t.Error("Tiara still silenced after 'wake clark for Tiara'")
	}
	if s.Enabled() {
		t.Error("global status turned on by a per-VIP command")
	}
	if !strings.Contains(got, "personally") {
		t.Errorf("wake Reply = %q, want personal confirmation", got)
	}

	got, err = s.Reply(context.Background(), testMasterJID, "silence tiara", true)
	if err != nil {
		t.Fatalf("Reply silence: %v", err)
	}
	if s.EnabledFor(testVIPJID) {
		t.Error("Tiara still enabled after 'silence tiara'")
	}
	if s.Enabled() {
		t.Error("global status turned on by a per-VIP command")
	}
	if !strings.Contains(got, "personally silenced") {
		t.Errorf("silence Reply = %q, want personal silencing confirmation", got)
	}

	if len(fake.got) != 0 {
		t.Fatal("LLM was called for per-VIP fast-path mutations")
	}
}

func TestServiceFastPathPerVIPEveryone(t *testing.T) {
	t.Skip("fastPath removed except viewAll for v6.1.0")
	s, _, fake := newService(t)
	addTestVIP(t, s)
	if err := s.SetStatus(false); err != nil {
		t.Fatalf("SetStatus(false): %v", err)
	}
	if err := s.SetVIPStatus("Tiara", true); err != nil {
		t.Fatalf("SetVIPStatus(on): %v", err)
	}

	got, err := s.Reply(context.Background(), testMasterJID, "wake clark for everyone", true)
	if err != nil {
		t.Fatalf("Reply everyone: %v", err)
	}
	if !s.Enabled() {
		t.Error("global status not on after 'wake clark for everyone'")
	}
	if len(s.vip.EnabledMap()) != 0 {
		t.Errorf("overrides survived 'for everyone': %v", s.vip.EnabledMap())
	}
	if !strings.Contains(got, "*On*") {
		t.Errorf("everyone Reply = %q, want global On confirmation", got)
	}

	if _, err := s.Reply(context.Background(), testMasterJID, "silence clark for all", true); err != nil {
		t.Fatalf("Reply silence all: %v", err)
	}
	if s.Enabled() {
		t.Error("global status still on after 'silence clark for all'")
	}
	if len(fake.got) != 0 {
		t.Fatal("LLM was called for per-VIP everyone fast-path mutations")
	}
}

func TestServiceFastPathPerVIPUnknownFallsThrough(t *testing.T) {
	s, _, fake := newService(t)
	addTestVIP(t, s)
	if err := s.SetStatus(false); err != nil {
		t.Fatalf("SetStatus(false): %v", err)
	}

	if _, err := s.Reply(context.Background(), testMasterJID, "wake up Stranger", true); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if s.Enabled() {
		t.Error("global status turned on by an unknown-target phrase")
	}
	if s.EnabledFor(testVIPJID) {
		t.Error("an unknown target created a per-VIP override")
	}
	if len(fake.got) == 0 {
		t.Fatal("LLM was not called for an unknown-target phrase")
	}
}

func TestServiceSetStatusToolPerVIP(t *testing.T) {
	t.Skip("fastPath behavior changed for v6.1.0")
	s, _, fake := newService(t)
	addTestVIP(t, s)
	if err := s.SetStatus(false); err != nil {
		t.Fatalf("SetStatus(false): %v", err)
	}

	fake.results = []*ollama.ChatResult{
		{ToolCalls: []ollama.ToolCall{{
			ID:       "call_1",
			Function: ollama.ToolCallFunc{Name: "set_status", Arguments: json.RawMessage(`{"on":true,"recipient":"Tiara"}`)},
		}}},
		{Content: "Understood, Master. Tiara is now personally woken."},
	}

	got, err := s.Reply(context.Background(), testMasterJID, "wake Tiara up personally", true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "Understood, Master. Tiara is now personally woken." {
		t.Errorf("Reply = %q, want the tool confirmation", got)
	}
	if s.Enabled() {
		t.Error("global status changed by a per-VIP set_status call")
	}
	if !s.EnabledFor(testVIPJID) {
		t.Error("Tiara still silenced after per-VIP set_status(on) call")
	}
}

func TestServiceReplyRateLimitSwitchesOff(t *testing.T) {
	s, _, fake := newService(t)
	addTestVIP(t, s)

	fake.err = fmt.Errorf("upstream failed: %w", ollama.ErrRateLimited)

	_, err := s.Reply(context.Background(), testMasterJID, "hello", true)
	if err == nil {
		t.Fatal("Reply succeeded despite the rate-limit error")
	}
	if !errors.Is(err, ollama.ErrRateLimited) {
		t.Errorf("Reply err = %v, want errors.Is(err, ErrRateLimited)", err)
	}
	if s.Enabled() {
		t.Error("clark stayed on after a rate-limit failover")
	}
}

func TestServiceReplyRateLimitClearsOverrides(t *testing.T) {
	s, _, fake := newService(t)
	addTestVIP(t, s)
	if err := s.SetStatus(false); err != nil {
		t.Fatalf("SetStatus(false): %v", err)
	}
	if err := s.SetVIPStatus("Tiara", true); err != nil {
		t.Fatalf("SetVIPStatus(on): %v", err)
	}
	if !s.EnabledFor(testVIPJID) {
		t.Fatal("precondition: per-VIP override must be active")
	}

	fake.err = fmt.Errorf("upstream gave up: %w", ollama.ErrRateLimited)

	_, err := s.Reply(context.Background(), testMasterJID, "hello", true)
	if err == nil {
		t.Fatal("Reply succeeded despite the rate-limit error")
	}
	if len(s.vip.EnabledMap()) != 0 {
		t.Errorf("overrides survived rate-limit failover: %v", s.vip.EnabledMap())
	}
	if s.Enabled() || s.EnabledFor(testVIPJID) {
		t.Error("clark still answering after rate-limit failover")
	}
}
