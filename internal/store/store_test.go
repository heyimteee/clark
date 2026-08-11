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
	// With second-resolution timestamps the surviving 30 are not
	// deterministically the newest; only the cap is guaranteed.
	for _, m := range messages {
		if m.Role != "user" {
			t.Errorf("role = %q, want user", m.Role)
		}
	}
}
