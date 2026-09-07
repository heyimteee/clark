package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// WebSession is one web-chat conversation. Its messages live in chat_history
// under jid "web:<id>"; the legacy "web" conversation is adopted into a
// session on first use (see EnsureDefaultWebSession).
type WebSession struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WebSessionJID maps a session id to its chat_history conversation key.
func WebSessionJID(id int64) string {
	return fmt.Sprintf("web:%d", id)
}

// CreateWebSession starts a new conversation with the given title.
func (s *Store) CreateWebSession(title string) (WebSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if title == "" {
		title = "New chat"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions (title) VALUES (?)`, title)
	if err != nil {
		return WebSession{}, fmt.Errorf("fail to create web session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return WebSession{}, fmt.Errorf("fail to read web session id: %w", err)
	}
	return s.GetWebSession(id)
}

// ListWebSessions returns every conversation, most recently active first.
func (s *Store) ListWebSessions() ([]WebSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, created_at, updated_at FROM web_sessions ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("fail to list web sessions: %w", err)
	}
	defer rows.Close()
	sessions := []WebSession{}
	for rows.Next() {
		var ws WebSession
		if err := rows.Scan(&ws.ID, &ws.Title, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, ws)
	}
	return sessions, rows.Err()
}

// GetWebSession fetches one conversation by id.
func (s *Store) GetWebSession(id int64) (WebSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at FROM web_sessions WHERE id = ?`, id)
	var ws WebSession
	if err := row.Scan(&ws.ID, &ws.Title, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return WebSession{}, fmt.Errorf("web session %d not found", id)
		}
		return WebSession{}, fmt.Errorf("fail to get web session: %w", err)
	}
	return ws, nil
}

// RenameWebSession changes a conversation's title.
func (s *Store) RenameWebSession(id int64, title string) (WebSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if title == "" {
		return WebSession{}, fmt.Errorf("title is required")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE web_sessions SET title = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, title, id)
	if err != nil {
		return WebSession{}, fmt.Errorf("fail to rename web session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return WebSession{}, fmt.Errorf("web session %d not found", id)
	}
	return s.GetWebSession(id)
}

// TouchWebSession bumps a conversation to the top of the list.
func (s *Store) TouchWebSession(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE web_sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("fail to touch web session: %w", err)
	}
	return nil
}

// DeleteWebSession removes a conversation and its messages.
func (s *Store) DeleteWebSession(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("fail to delete web session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("web session %d not found", id)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_history WHERE jid = ?`, WebSessionJID(id)); err != nil {
		return fmt.Errorf("fail to delete web session history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// EnsureDefaultWebSession guarantees at least one conversation exists. The
// legacy single "web" conversation is adopted into it so no history is lost.
func (s *Store) EnsureDefaultWebSession() (WebSession, error) {
	existing, err := s.ListWebSessions()
	if err != nil {
		return WebSession{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var legacy int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_history WHERE jid = 'web'`).Scan(&legacy); err != nil {
		return WebSession{}, fmt.Errorf("fail to count legacy web history: %w", err)
	}
	title := "New chat"
	if legacy > 0 {
		title = "Main chat"
	}
	created, err := s.CreateWebSession(title)
	if err != nil {
		return WebSession{}, err
	}
	if legacy > 0 {
		if _, err := s.db.ExecContext(ctx, `UPDATE chat_history SET jid = ? WHERE jid = 'web'`, WebSessionJID(created.ID)); err != nil {
			return WebSession{}, fmt.Errorf("fail to adopt legacy web history: %w", err)
		}
	}
	return created, nil
}
