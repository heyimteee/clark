package store

import (
	"context"
	"fmt"
	"time"
)

// All returns every VIP entry, most-recently-added last.
func (s *Store) All() ([]VIPEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `SELECT jid, name, relation FROM vip`)
	if err != nil {
		return nil, fmt.Errorf("fail to load table <vip>: %w", err)
	}
	defer rows.Close()

	var entries []VIPEntry
	for rows.Next() {
		var e VIPEntry
		if err := rows.Scan(&e.JID, &e.Name, &e.Relation); err != nil {
			return nil, fmt.Errorf("fail to scan jid and relation: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Add upserts a VIP entry.
func (s *Store) Add(entry VIPEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO vip (jid, name, relation) VALUES (?, ?, ?)`,
		entry.JID, entry.Name, entry.Relation); err != nil {
		return fmt.Errorf("fail to add new vip: %w", err)
	}
	return nil
}

// Delete removes the VIP entry and its access grants for the given jid.
func (s *Store) Delete(jid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM vip WHERE jid = ?`, jid); err != nil {
		return fmt.Errorf("fail to delete vip: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM vip_access WHERE jid = ?`, jid); err != nil {
		return fmt.Errorf("fail to delete access: %w", err)
	}
	return nil
}

// ClearAll removes every VIP entry and their access grants.
func (s *Store) ClearAll() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM vip`); err != nil {
		return fmt.Errorf("fail to clear vip list: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM vip_access`); err != nil {
		return fmt.Errorf("fail to clear vip access: %w", err)
	}
	return nil
}
