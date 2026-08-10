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
                ORDER BY timestamp DESC 
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

// Messages returns jid's history in chronological order, restricted to user/assistant roles.
func (s *Store) Messages(jid string) ([]Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx,
		`SELECT role, content FROM chat_history WHERE jid = ? ORDER BY timestamp ASC`, jid)
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
