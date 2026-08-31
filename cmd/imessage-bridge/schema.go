package main

import (
	"database/sql"
	"fmt"
	"time"
)

// chatDB is the read-only view over the real ~/Library/Messages/chat.db schema.
// The bridge never writes to it: it polls for new messages by ROWID and tracks
// its own watermark in a separate state file.

// newMessagesQuery selects inbound, non-system DMs newer than the watermark.
// Reactions and group chatter are excluded. Media-only messages (empty text
// but with an attachment) are now included so images/gif/video can be
// processed like WhatsApp.
const newMessagesQuery = `
SELECT message.ROWID, message.guid, COALESCE(message.text,''), message.is_from_me,
       handle.id, message.date, message.service,
       COALESCE(message.cache_has_attachments,0)
FROM message
LEFT JOIN handle ON handle.ROWID = message.handle_id
WHERE message.ROWID > ?
  AND message.is_from_me = 0
  AND ( (message.text IS NOT NULL AND message.text != '') OR COALESCE(message.cache_has_attachments,0) = 1 )
  AND COALESCE(message.associated_message_type, 0) = 0
  AND COALESCE(message.is_system_message, 0) = 0
  AND COALESCE(message.error, 0) = 0
  AND EXISTS (
      SELECT 1 FROM chat_message_join j
      JOIN chat c ON c.ROWID = j.chat_id
      WHERE j.message_id = message.ROWID
        AND c.chat_identifier NOT LIKE '%;%'
  )
ORDER BY message.ROWID ASC`

const maxRowIDQuery = `SELECT COALESCE(MAX(ROWID), 0) FROM message`

// ownHandleQuery finds the most recent outbound message's recipient handle, a
// good guess for the Master's own iMessage address.
const ownHandleQuery = `
SELECT handle.id
FROM message
JOIN handle ON handle.ROWID = message.handle_id
WHERE message.is_from_me = 1 AND message.text IS NOT NULL
ORDER BY message.ROWID DESC LIMIT 1`

// newMessage is one row from newMessagesQuery.
type newMessage struct {
	RowID          int64
	GUID           string
	Text           string
	IsFromMe       bool
	Handle         string
	Date           int64
	Service        string
	HasAttachments bool
}

// attachmentRow is one row from the attachment table for a message.
type attachmentRow struct {
	RowID        int64
	Filename     string
	MimeType     string
	UTI          string
	TransferName string
	TotalBytes   int64
}

const attachmentQuery = `
SELECT a.ROWID, a.filename, COALESCE(a.mime_type,''), COALESCE(a.uti,''), COALESCE(a.transfer_name,''), COALESCE(a.total_bytes,0)
FROM attachment a
JOIN message_attachment_join maj ON maj.attachment_id = a.ROWID
WHERE maj.message_id = ?
ORDER BY a.ROWID ASC`

// openChatDB opens chat.db strictly read-only with a busy timeout so a
// concurrent Messages.app write never wedges the poller.
func openChatDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=1000")
	if err != nil {
		return nil, fmt.Errorf("fail to open chat.db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("fail to ping chat.db: %w", err)
	}
	return db, nil
}

// maxRowID returns the highest message ROWID, used to bootstrap the watermark
// so an existing chat history is never replayed.
func maxRowID(db *sql.DB) (int64, error) {
	var id int64
	if err := db.QueryRow(maxRowIDQuery).Scan(&id); err != nil {
		return 0, fmt.Errorf("fail to read max ROWID: %w", err)
	}
	return id, nil
}

// detectOwnHandle guesses the Master's own iMessage handle from the most recent
// outbound message. An empty result means no outbound history exists yet.
func detectOwnHandle(db *sql.DB) (string, error) {
	var handle string
	err := db.QueryRow(ownHandleQuery).Scan(&handle)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("fail to detect own handle: %w", err)
	}
	return handle, nil
}

// queryNewMessages returns every qualifying message with ROWID > after.
func queryNewMessages(db *sql.DB, after int64) ([]newMessage, error) {
	rows, err := db.Query(newMessagesQuery, after)
	if err != nil {
		return nil, fmt.Errorf("fail to query new messages: %w", err)
	}
	defer rows.Close()

	var out []newMessage
	for rows.Next() {
		var m newMessage
		var hasAtt int
		if err := rows.Scan(&m.RowID, &m.GUID, &m.Text, &m.IsFromMe, &m.Handle, &m.Date, &m.Service, &hasAtt); err != nil {
			return nil, fmt.Errorf("fail to scan message: %w", err)
		}
		m.HasAttachments = hasAtt != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// queryAttachments returns the attachments for a message, if any.
func queryAttachments(db *sql.DB, messageID int64) ([]attachmentRow, error) {
	rows, err := db.Query(attachmentQuery, messageID)
	if err != nil {
		return nil, fmt.Errorf("fail to query attachments: %w", err)
	}
	defer rows.Close()
	var out []attachmentRow
	for rows.Next() {
		var a attachmentRow
		if err := rows.Scan(&a.RowID, &a.Filename, &a.MimeType, &a.UTI, &a.TransferName, &a.TotalBytes); err != nil {
			return nil, fmt.Errorf("fail to scan attachment: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// messageTime converts chat.db's date column (nanoseconds since 2001-01-01)
// into a UTC time.
func messageTime(ns int64) time.Time {
	return epoch.Add(time.Duration(ns))
}

var epoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
