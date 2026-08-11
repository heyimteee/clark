package store

import (
	"context"
	"fmt"
	"time"
)

// SaveMessage appends a message to jid's history, keeping only the 30 most recent.
func (s *Store) SaveMessage(jid, role, content string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chat_history (jid, role, content) VALUES (?, ?, ?)`, jid, role, content); err != nil {
		return fmt.Errorf("fail to save message: %w", err)
	}

	cleanup := `
        DELETE FROM chat_history 
        WHERE jid = ? AND id NOT IN (
            SELECT id FROM (
                SELECT id FROM chat_history 
                WHERE jid = ? 
                ORDER BY id DESC 
                LIMIT 30
            ) AS temp
        )`
	if _, err := tx.ExecContext(ctx, cleanup, jid, jid); err != nil {
		return fmt.Errorf("fail to clean up: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// Messages returns jid's full history in chronological order, restricted to
// user/assistant roles.
func (s *Store) Messages(jid string) ([]Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx,
		`SELECT role, content FROM chat_history WHERE jid = ? ORDER BY id ASC`, jid)
	if err != nil {
		return nil, fmt.Errorf("failed to query history: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var dbRole, content string
		if err := rows.Scan(&dbRole, &content); err != nil {
			return nil, err
		}
		switch dbRole {
		case "user", "assistant":
			messages = append(messages, Message{Role: dbRole, Content: content})
		}
	}
	return messages, rows.Err()
}

// RecentMessages returns jid's most recent limit messages in chronological
// order. When fewer than limit exist, it returns whatever there is.
func (s *Store) RecentMessages(jid string, limit int) ([]Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if limit < 1 {
		limit = 1
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT role, content FROM chat_history WHERE jid = ? ORDER BY id DESC LIMIT ?`, jid, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent history: %w", err)
	}
	defer rows.Close()

	var newest []Message
	for rows.Next() {
		var dbRole, content string
		if err := rows.Scan(&dbRole, &content); err != nil {
			return nil, err
		}
		switch dbRole {
		case "user", "assistant":
			newest = append(newest, Message{Role: dbRole, Content: content})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	return newest, nil
}

// AllRecentMessages returns the most recent limit messages across every
// conversation, oldest of the window first. A non-positive limit returns the
// complete stored history.
func (s *Store) AllRecentMessages(limit int) ([]HistoryEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `SELECT jid, role, content FROM chat_history ORDER BY id DESC`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query all history: %w", err)
	}
	defer rows.Close()

	var newest []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.JID, &e.Role, &e.Content); err != nil {
			return nil, err
		}
		if e.Role == "user" || e.Role == "assistant" {
			newest = append(newest, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	return newest, nil
}
