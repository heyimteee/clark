package store

import (
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestWebSessionCRUD(t *testing.T) {
	st := openTestStore(t)

	created, err := st.CreateWebSession("First")
	if err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}
	if created.ID == 0 || created.Title != "First" {
		t.Fatalf("created wrong: %+v", created)
	}

	second, err := st.CreateWebSession("")
	if err != nil {
		t.Fatalf("CreateWebSession default title: %v", err)
	}
	if second.Title != "New chat" {
		t.Fatalf("default title = %q, want New chat", second.Title)
	}

	list, err := st.ListWebSessions()
	if err != nil || len(list) != 2 {
		t.Fatalf("ListWebSessions: %v len=%d", err, len(list))
	}
	if list[0].ID != second.ID {
		t.Fatalf("newest should sort first: %+v", list)
	}

	renamed, err := st.RenameWebSession(created.ID, "Renamed")
	if err != nil || renamed.Title != "Renamed" {
		t.Fatalf("RenameWebSession: %v %+v", err, renamed)
	}
	if _, err := st.RenameWebSession(created.ID, ""); err == nil {
		t.Fatal("empty title should fail")
	}
	if _, err := st.RenameWebSession(99999, "x"); err == nil {
		t.Fatal("rename of missing session should fail")
	}

	if err := st.SaveMessage(WebSessionJID(created.ID), "user", "hello"); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := st.TouchWebSession(created.ID); err != nil {
		t.Fatalf("TouchWebSession: %v", err)
	}
	if err := st.DeleteWebSession(created.ID); err != nil {
		t.Fatalf("DeleteWebSession: %v", err)
	}
	msgs, err := st.Messages(WebSessionJID(created.ID))
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("session history should be deleted, got %d messages", len(msgs))
	}
	if err := st.DeleteWebSession(created.ID); err == nil {
		t.Fatal("double delete should fail")
	}
}

func TestEnsureDefaultWebSessionAdoptsLegacy(t *testing.T) {
	st := openTestStore(t)

	if err := st.SaveMessage("web", "user", "legacy hello"); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	def, err := st.EnsureDefaultWebSession()
	if err != nil {
		t.Fatalf("EnsureDefaultWebSession: %v", err)
	}
	if def.Title != "Main chat" {
		t.Fatalf("title = %q, want Main chat", def.Title)
	}
	msgs, err := st.Messages(WebSessionJID(def.ID))
	if err != nil || len(msgs) != 1 || msgs[0].Content != "legacy hello" {
		t.Fatalf("legacy history not adopted: %+v %v", msgs, err)
	}
	legacy, err := st.Messages("web")
	if err != nil || len(legacy) != 0 {
		t.Fatalf("legacy jid should be empty: %+v %v", legacy, err)
	}
	// Second call is a no-op returning the same session.
	again, err := st.EnsureDefaultWebSession()
	if err != nil || again.ID != def.ID {
		t.Fatalf("ensure not idempotent: %+v %v", again, err)
	}
}

func TestEnsureDefaultWebSessionEmpty(t *testing.T) {
	st := openTestStore(t)

	def, err := st.EnsureDefaultWebSession()
	if err != nil {
		t.Fatalf("EnsureDefaultWebSession: %v", err)
	}
	if def.Title != "New chat" {
		t.Fatalf("title = %q, want New chat", def.Title)
	}
}
