package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GetTools returns the granted tool names for a VIP. ok reports whether an
// access row exists at all.
func (s *Store) GetTools(jid string) ([]string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT tools FROM vip_access WHERE jid = ?`, jid).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("fail to load access for %s: %w", jid, err)
	}

	var tools []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tools = append(tools, t)
		}
	}
	return tools, true, nil
}

// SetTools stores the granted tool names for a VIP.
func (s *Store) SetTools(jid string, tools []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	raw := strings.Join(tools, ",")
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO vip_access (jid, tools) VALUES (?, ?)`,
		jid, raw); err != nil {
		return fmt.Errorf("fail to set access for %s: %w", jid, err)
	}
	return nil
}

// DeleteAccess removes the access row for a VIP.
func (s *Store) DeleteAccess(jid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM vip_access WHERE jid = ?`, jid); err != nil {
		return fmt.Errorf("fail to delete access for %s: %w", jid, err)
	}
	return nil
}
