package store

import (
	"strings"
	"testing"
)

func TestSettingsLifecycle(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.InitDefaults(); err != nil {
		t.Fatalf("InitDefaults: %v", err)
	}

	ok, err := st.IsInitialized()
	if err != nil {
		t.Fatalf("IsInitialized: %v", err)
	}
	if !ok {
		t.Fatal("IsInitialized = false after InitDefaults")
	}

	got, err := st.Get("name")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "Clark" {
		t.Errorf("name = %q, want Clark", got)
	}

	if err := st.Set("context", "new ctx"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err = st.Get("context")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got != "new ctx" {
		t.Errorf("context = %q, want new ctx", got)
	}
}

func TestSettingsNotInitialized(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ok, err := st.IsInitialized()
	if err != nil {
		t.Fatalf("IsInitialized: %v", err)
	}
	if ok {
		t.Fatal("IsInitialized = true on empty database")
	}
}

func TestVIPStoreCRUD(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	entry := VIPEntry{JID: "6281234567890@s.whatsapp.net", Name: "Test", Relation: "Friend"}
	if err := st.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := st.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(entries) != 1 || entries[0] != entry {
		t.Fatalf("All = %+v, want [%+v]", entries, entry)
	}

	if err := st.Delete(entry.JID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, err = st.All()
	if err != nil {
		t.Fatalf("All after delete: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("All after delete = %+v, want empty", entries)
	}
}

func TestVIPStoreClearAll(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	for i, e := range []VIPEntry{
		{JID: "6281234567890@s.whatsapp.net", Name: "Test", Relation: "Friend"},
		{JID: "6289999999999@s.whatsapp.net", Name: "Second", Relation: "Colleague"},
	} {
		if err := st.Add(e); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
	}
	if err := st.SetTools("6281234567890@s.whatsapp.net", []string{"web_search"}); err != nil {
		t.Fatalf("SetTools: %v", err)
	}

	if err := st.ClearAll(); err != nil {
		t.Fatalf("ClearAll: %v", err)
	}

	entries, err := st.All()
	if err != nil {
		t.Fatalf("All after ClearAll: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("All after ClearAll = %+v, want empty", entries)
	}
	if _, ok, err := st.GetTools("6281234567890@s.whatsapp.net"); err != nil || ok {
		t.Fatalf("access grant survived ClearAll: ok=%v err=%v, want gone", ok, err)
	}
}

func TestHistoryKeepsLast30(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	for i := 0; i < 35; i++ {
		if err := st.SaveMessage("jid", "user", strings.Repeat("x", i+1)); err != nil {
			t.Fatalf("SaveMessage(%d): %v", i, err)
		}
	}

	messages, err := st.Messages("jid")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 30 {
		t.Fatalf("kept %d messages, want 30", len(messages))
	}
	// The cap is trimmed by insertion id, so the surviving 30 are exactly
	// the newest 30, in chronological order.
	if messages[0].Content != strings.Repeat("x", 6) {
		t.Errorf("first surviving message content length %d, want 6", len(messages[0].Content))
	}
	if messages[len(messages)-1].Content != strings.Repeat("x", 35) {
		t.Errorf("last surviving message content length %d, want 35", len(messages[len(messages)-1].Content))
	}
	for _, m := range messages {
		if m.Role != "user" {
			t.Errorf("role = %q, want user", m.Role)
		}
	}
}

func TestRecentMessagesLimitAndOrder(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	for i := 0; i < 5; i++ {
		if err := st.SaveMessage("jid", "user", strings.Repeat("m", i+1)); err != nil {
			t.Fatalf("SaveMessage(%d): %v", i, err)
		}
	}

	recent, err := st.RecentMessages("jid", 3)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("RecentMessages = %d messages, want 3", len(recent))
	}
	if recent[0].Content != "mmm" {
		t.Errorf("first recent = %q, want 'mmm'", recent[0].Content)
	}
	if recent[2].Content != "mmmmm" {
		t.Errorf("last recent = %q, want 'mmmmm'", recent[2].Content)
	}

	all, err := st.RecentMessages("jid", 100)
	if err != nil {
		t.Fatalf("RecentMessages(100): %v", err)
	}
	if len(all) != 5 {
		t.Errorf("RecentMessages(100) = %d messages, want all 5", len(all))
	}
}

func TestAllRecentMessagesAcrossChats(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	for i := 0; i < 3; i++ {
		if err := st.SaveMessage("a@s.whatsapp.net", "user", "a"+strings.Repeat("x", i)); err != nil {
			t.Fatalf("SaveMessage(a %d): %v", i, err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := st.SaveMessage("b@s.whatsapp.net", "assistant", "b"+strings.Repeat("x", i)); err != nil {
			t.Fatalf("SaveMessage(b %d): %v", i, err)
		}
	}

	full, err := st.AllRecentMessages(0)
	if err != nil {
		t.Fatalf("AllRecentMessages(0): %v", err)
	}
	if len(full) != 7 {
		t.Fatalf("AllRecentMessages(0) = %d entries, want all 7", len(full))
	}
	if full[0].JID != "a@s.whatsapp.net" || full[0].Content != "a" {
		t.Errorf("first full entry = %+v, want the earliest a message", full[0])
	}
	if full[len(full)-1].JID != "b@s.whatsapp.net" || full[len(full)-1].Content != "bxxx" {
		t.Errorf("last full entry = %+v, want the latest b message", full[len(full)-1])
	}

	limited, err := st.AllRecentMessages(3)
	if err != nil {
		t.Fatalf("AllRecentMessages(3): %v", err)
	}
	if len(limited) != 3 {
		t.Fatalf("AllRecentMessages(3) = %d entries, want 3", len(limited))
	}
	if limited[0].Content != "bx" || limited[2].Content != "bxxx" {
		t.Errorf("limited window = %q..%q, want the newest 3 across chats", limited[0].Content, limited[2].Content)
	}
}
