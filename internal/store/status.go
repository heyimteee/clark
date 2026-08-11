package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SetEnabled persists a per-VIP status override for the given jid.
func (s *Store) SetEnabled(jid string, on bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	enabled := 0
	if on {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO vip_status (jid, enabled) VALUES (?, ?)`, jid, enabled); err != nil {
		return fmt.Errorf("fail to set vip status: %w", err)
	}
	return nil
}

// Enabled reports a VIP's persisted status override. ok is false when no
// override is stored for the jid.
func (s *Store) Enabled(jid string) (on bool, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var enabled int
	err = s.db.QueryRowContext(ctx,
		`SELECT enabled FROM vip_status WHERE jid = ?`, jid).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("fail to load vip status: %w", err)
	}
	return enabled != 0, true, nil
}

// ClearEnabled removes a VIP's status override so the global status applies.
func (s *Store) ClearEnabled(jid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM vip_status WHERE jid = ?`, jid); err != nil {
		return fmt.Errorf("fail to clear vip status: %w", err)
	}
	return nil
}

// ClearAllEnabled removes every VIP's status override.
func (s *Store) ClearAllEnabled() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM vip_status`); err != nil {
		return fmt.Errorf("fail to clear vip statuses: %w", err)
	}
	return nil
}
