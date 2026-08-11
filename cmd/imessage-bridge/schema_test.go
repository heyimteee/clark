package main

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// synthetic chat.db DDL mirroring the verified real schema subset the bridge reads.
const synthDDL = `
CREATE TABLE message (
	ROWID INTEGER PRIMARY KEY AUTOINCREMENT,
	guid TEXT UNIQUE NOT NULL,
	text TEXT,
	handle_id INTEGER,
	date INTEGER,
	date_read INTEGER,
	date_delivered INTEGER,
	is_from_me INTEGER,
	is_system_message INTEGER,
	is_delivered INTEGER,
	is_read INTEGER,
	associated_message_type INTEGER,
	associated_message_guid TEXT,
	error INTEGER,
	service TEXT,
	cache_has_attachments INTEGER,
	cache_roomnames TEXT
);
CREATE TABLE handle (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	remote_id TEXT UNIQUE NOT NULL,
	service TEXT NOT NULL,
	uncanonicalized_id TEXT,
	country TEXT,
	UNIQUE (service, remote_id)
);
CREATE TABLE chat (
	ROWID INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_identifier TEXT NOT NULL,
	service_name TEXT
);
CREATE TABLE chat_message_join (
	chat_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	message_date INTEGER,
	PRIMARY KEY (chat_id, message_id)
);`

func openSynthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open synth db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(synthDDL); err != nil {
		t.Fatalf("create synth schema: %v", err)
	}
	return db
}

// addMessage inserts a message with its handle and a private chat, returning
// its ROWID. isGroup routes the join through a group-style chat identifier.
func addMessage(t *testing.T, db *sql.DB, remoteID, text string, isFromMe bool, isGroup bool, extra map[string]any) int64 {
	t.Helper()
	var handleID int64
	if remoteID != "" {
		err := db.QueryRow(`SELECT id FROM handle WHERE remote_id = ?`, remoteID).Scan(&handleID)
		if err == sql.ErrNoRows {
			res, err := db.Exec(`INSERT INTO handle (remote_id, service) VALUES (?, 'iMessage')`, remoteID)
			if err != nil {
				t.Fatalf("insert handle: %v", err)
			}
			handleID, _ = res.LastInsertId()
		} else if err != nil {
			t.Fatalf("lookup handle: %v", err)
		}
	}

	var handleNull any
	if handleID > 0 {
		handleNull = handleID
	}
	assocType := 0
	if v, ok := extra["associated_message_type"].(int); ok {
		assocType = v
	}
	isSystem := 0
	if v, ok := extra["is_system_message"].(int); ok {
		isSystem = v
	}
	isFromMeInt := 0
	if isFromMe {
		isFromMeInt = 1
	}

	res, err := db.Exec(`INSERT INTO message (guid, text, handle_id, date, is_from_me, is_system_message, associated_message_type, error, service)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, 'iMessage')`,
		fmt.Sprintf("guid-%d", time.Now().UnixNano()), text, handleNull, epoch.UnixNano(),
		isFromMeInt, isSystem, assocType)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	msgID, _ := res.LastInsertId()

	chatIdent := remoteID
	if isGroup {
		chatIdent = "chat123456789;iMessage"
	}
	var chatID int64
	if err := db.QueryRow(`SELECT ROWID FROM chat WHERE chat_identifier = ?`, chatIdent).Scan(&chatID); err != nil {
		res, err := db.Exec(`INSERT INTO chat (chat_identifier, service_name) VALUES (?, 'iMessage')`, chatIdent)
		if err != nil {
			t.Fatalf("insert chat: %v", err)
		}
		chatID, _ = res.LastInsertId()
	}
	if _, err := db.Exec(`INSERT INTO chat_message_join (chat_id, message_id) VALUES (?, ?)`, chatID, msgID); err != nil {
		t.Fatalf("insert join: %v", err)
	}
	return msgID
}

func TestQueryNewMessagesFilters(t *testing.T) {
	db := openSynthDB(t)

	vip := addMessage(t, db, "+6281267858909", "hello clark", false, false, nil)
	addMessage(t, db, "+6281267858909", "hello outbound", true, false, nil)                                       // is_from_me
	addMessage(t, db, "+6281267858909", "group chatter", false, true, nil)                                        // group chat
	addMessage(t, db, "+6281267858909", "", false, false, nil)                                                    // empty text
	addMessage(t, db, "+6281267858909", "tapback", false, false, map[string]any{"associated_message_type": 2000}) // reaction
	addMessage(t, db, "+6281267858909", "service msg", false, false, map[string]any{"is_system_message": 1})      // system message

	got, err := queryNewMessages(db, 0)
	if err != nil {
		t.Fatalf("queryNewMessages: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1: %+v", len(got), got)
	}
	if got[0].RowID != vip || got[0].Handle != "+6281267858909" || got[0].Text != "hello clark" {
		t.Errorf("message = %+v, want the single inbound DM", got[0])
	}
	if got[0].IsFromMe {
		t.Error("IsFromMe = true, want false")
	}
}

func TestQueryNewMessagesWatermark(t *testing.T) {
	db := openSynthDB(t)

	id1 := addMessage(t, db, "+6281267858909", "first", false, false, nil)
	id2 := addMessage(t, db, "+6281267858909", "second", false, false, nil)

	got, err := queryNewMessages(db, id1)
	if err != nil {
		t.Fatalf("queryNewMessages: %v", err)
	}
	if len(got) != 1 || got[0].RowID != id2 {
		t.Fatalf("got %+v, want only message %d", got, id2)
	}
}

func TestMaxRowID(t *testing.T) {
	db := openSynthDB(t)
	if max, err := maxRowID(db); err != nil || max != 0 {
		t.Fatalf("empty db max = %d/%v, want 0", max, err)
	}
	addMessage(t, db, "+6281267858909", "x", false, false, nil)
	addMessage(t, db, "+6281267858909", "y", false, false, nil)
	if max, err := maxRowID(db); err != nil || max != 2 {
		t.Fatalf("max = %d/%v, want 2", max, err)
	}
}

func TestDetectOwnHandle(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "incoming", false, false, nil)
	if got, err := detectOwnHandle(db); err != nil || got != "" {
		t.Fatalf("no outbound -> handle %q/%v, want empty", got, err)
	}

	addMessage(t, db, "+6281111111111", "outgoing", true, false, nil)
	got, err := detectOwnHandle(db)
	if err != nil || got != "+6281111111111" {
		t.Fatalf("handle = %q/%v, want +6281111111111", got, err)
	}
}

func TestMessageTime(t *testing.T) {
	// A zero date column is the epoch itself (2001-01-01 UTC).
	if got := messageTime(0); !got.Equal(epoch) {
		t.Errorf("messageTime(0) = %v, want epoch", got)
	}
	// 10^9 ns later is one second after the epoch.
	if got := messageTime(1e9); !got.Equal(epoch.Add(time.Second)) {
		t.Errorf("messageTime(1e9) = %v, want epoch+1s", got)
	}
}
