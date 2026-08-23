package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSettingsExtraKeysStillInitialized(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.InitDefaults(); err != nil {
		t.Fatalf("InitDefaults: %v", err)
	}

	// Optional settings written by later features (thinking mode, history
	// limit) grow the table beyond the seeded defaults; that must not make an
	// initialized database look uninitialized.
	for _, key := range []string{"think", "history_limit"} {
		if err := st.Set(key, "false"); err != nil {
			t.Fatalf("Set(%s): %v", key, err)
		}
	}
	ok, err := st.IsInitialized()
	if err != nil {
		t.Fatalf("IsInitialized: %v", err)
	}
	if !ok {
		t.Fatal("IsInitialized = false after extra settings were added")
	}
}

func TestSettingsPartialInitNotInitialized(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Only one of the required defaults present.
	if err := st.Set("name", "Clark"); err != nil {
		t.Fatalf("Set(name): %v", err)
	}
	if err := st.Set("think", "false"); err != nil {
		t.Fatalf("Set(think): %v", err)
	}
	ok, err := st.IsInitialized()
	if err != nil {
		t.Fatalf("IsInitialized: %v", err)
	}
	if ok {
		t.Fatal("IsInitialized = true when required defaults are missing")
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

func TestVIPStoreStatusLifecycle(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	jid := "6281234567890@s.whatsapp.net"

	if on, ok, err := st.Enabled(jid); err != nil || ok {
		t.Fatalf("Enabled before any override = %v/%v, want (false,false,nil)", on, ok)
	}

	if err := st.SetEnabled(jid, true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if on, ok, err := st.Enabled(jid); err != nil || !ok || !on {
		t.Fatalf("Enabled after on = %v/%v/%v, want (true,true,nil)", on, ok, err)
	}

	if err := st.SetEnabled(jid, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if on, ok, err := st.Enabled(jid); err != nil || !ok || on {
		t.Fatalf("Enabled after off = %v/%v/%v, want (false,true,nil)", on, ok, err)
	}

	if err := st.ClearEnabled(jid); err != nil {
		t.Fatalf("ClearEnabled: %v", err)
	}
	if on, ok, err := st.Enabled(jid); err != nil || ok {
		t.Fatalf("Enabled after ClearEnabled = %v/%v/%v, want (false,false,nil)", on, ok, err)
	}

	for _, on := range []bool{true, false} {
		if err := st.SetEnabled(jid, on); err != nil {
			t.Fatalf("SetEnabled(%v): %v", on, err)
		}
	}
	if err := st.ClearAllEnabled(); err != nil {
		t.Fatalf("ClearAllEnabled: %v", err)
	}
	if on, ok, err := st.Enabled(jid); err != nil || ok {
		t.Fatalf("Enabled after ClearAllEnabled = %v/%v/%v, want (false,false,nil)", on, ok, err)
	}
}

func TestVIPStoreStatusCascadeOnDelete(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	jid := "6281234567890@s.whatsapp.net"
	if err := st.Add(VIPEntry{JID: jid, Name: "Test", Relation: "Friend"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := st.SetEnabled(jid, true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	if err := st.Delete(jid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if on, ok, err := st.Enabled(jid); err != nil || ok {
		t.Fatalf("status survived Delete: %v/%v/%v, want (false,false,nil)", on, ok, err)
	}
}

func TestVIPStoreStatusCascadeOnClearAll(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	for _, e := range []VIPEntry{
		{JID: "6281234567890@s.whatsapp.net", Name: "Test", Relation: "Friend"},
		{JID: "6289999999999@s.whatsapp.net", Name: "Second", Relation: "Colleague"},
	} {
		if err := st.Add(e); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if err := st.SetEnabled(e.JID, true); err != nil {
			t.Fatalf("SetEnabled: %v", err)
		}
	}

	if err := st.ClearAll(); err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if on, ok, err := st.Enabled("6281234567890@s.whatsapp.net"); err != nil || ok {
		t.Fatalf("status survived ClearAll: %v/%v/%v, want (false,false,nil)", on, ok, err)
	}
}

func TestClearHistoryScopedToJID(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.SaveMessage("web", "user", "one"); err != nil {
		t.Fatalf("SaveMessage web: %v", err)
	}
	if err := st.SaveMessage("web", "assistant", "two"); err != nil {
		t.Fatalf("SaveMessage web: %v", err)
	}
	if err := st.SaveMessage("other", "user", "kept"); err != nil {
		t.Fatalf("SaveMessage other: %v", err)
	}

	if err := st.ClearHistory("web"); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}

	web, err := st.Messages("web")
	if err != nil {
		t.Fatalf("Messages(web): %v", err)
	}
	if len(web) != 0 {
		t.Errorf("web history after clear = %d messages, want 0", len(web))
	}

	other, err := st.Messages("other")
	if err != nil {
		t.Fatalf("Messages(other): %v", err)
	}
	if len(other) != 1 || other[0].Content != "kept" {
		t.Errorf("other history = %+v, want the untouched 'kept' message", other)
	}
}

func TestOpenSetsDBFileMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perm.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, want -rw------- (contains WhatsApp session keys)", fi.Mode().Perm())
	}
}

func TestOpenEnablesWALAndBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	mode, err := st.db.Query("PRAGMA journal_mode")
	if err != nil {
		t.Fatalf("pragma journal_mode: %v", err)
	}
	defer mode.Close()
	for mode.Next() {
		var m string
		if err := mode.Scan(&m); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if m != "wal" {
			t.Errorf("journal_mode = %q, want wal", m)
		}
	}
}

func TestMigrateCreatesHistoryIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	rows, err := st.db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='chat_history' AND name='idx_chat_history_jid'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("idx_chat_history_jid missing after migration")
	}
}

// TestConcurrentReadDuringWrite proves the busy-timeout/WAL setup lets a
// reader proceed while a write transaction holds the database — the exact
// contention web history hits during LLM tool-loop persistence.
func TestConcurrentReadDuringWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conc.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	t.Cleanup(func() { writer.Close() })
	reader, err := sql.Open("sqlite3", dsnFor(path))
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { reader.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin write tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chat_history (jid, role, content) VALUES ('w', 'user', 'x')`); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		var n int
		done <- reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM assistant_setting`).Scan(&n)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read during open write failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("read blocked longer than busy timeout")
	}
}
