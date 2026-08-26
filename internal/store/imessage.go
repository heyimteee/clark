package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// EnqueueIMessage queues an outbound iMessage and returns its row id.
func (s *Store) EnqueueIMessage(recipient, text string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO imessage_outbound (recipient, text) VALUES (?, ?)`, recipient, text)
	if err != nil {
		return 0, fmt.Errorf("fail to enqueue iMessage: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("fail to read iMessage id: %w", err)
	}
	return id, nil
}

// NextIMessageOutbound claims the oldest pending iMessage (marks it picked) and
// returns it. ok is false when the queue is empty. It first does a cheap
// read-only check so empty-queue polls do not take a write lock every 10s.
func (s *Store) NextIMessageOutbound() (OutboundMessage, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var pending int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM imessage_outbound WHERE status = 'pending'`).Scan(&pending); err != nil {
		return OutboundMessage{}, false, fmt.Errorf("fail to check pending iMessages: %w", err)
	}
	if pending == 0 {
		return OutboundMessage{}, false, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OutboundMessage{}, false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var msg OutboundMessage
	err = tx.QueryRowContext(ctx,
		`SELECT id, recipient, text FROM imessage_outbound WHERE status = 'pending' ORDER BY id ASC LIMIT 1`).
		Scan(&msg.ID, &msg.Recipient, &msg.Text)
	if err != nil {
		if err == sql.ErrNoRows {
			return OutboundMessage{}, false, nil
		}
		return OutboundMessage{}, false, fmt.Errorf("fail to load pending iMessage: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE imessage_outbound SET status = 'picked', picked_at = CURRENT_TIMESTAMP WHERE id = ?`, msg.ID); err != nil {
		return OutboundMessage{}, false, fmt.Errorf("fail to claim iMessage: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return OutboundMessage{}, false, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return msg, true, nil
}

// AckIMessage removes a delivered iMessage from the queue.
func (s *Store) AckIMessage(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM imessage_outbound WHERE id = ?`, id); err != nil {
		return fmt.Errorf("fail to ack iMessage: %w", err)
	}
	return nil
}

// StaleIMessageOutboundIDs returns picked-but-unacked ids older than maxAge.
// The bridge may have crashed mid-delivery; these are reported so operators
// can reconcile (they are never re-served, to avoid double-sends).
func (s *Store) StaleIMessageOutboundIDs(maxAge time.Duration) ([]int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cutoff := time.Now().Add(-maxAge).UTC()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM imessage_outbound WHERE status = 'picked' AND picked_at < ? ORDER BY id ASC`,
		cutoff.Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, fmt.Errorf("fail to load stale iMessages: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("fail to scan stale iMessage id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
