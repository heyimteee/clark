package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Protocol is one reusable, self-evolving skill document: a step-by-step
// procedure Clark (or the Master) saved after solving a task, loaded again
// the next time a similar problem appears.
type Protocol struct {
	ID         int64      `json:"id"`
	Slug       string     `json:"slug"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Origin     string     `json:"origin"`
	Version    int        `json:"version"`
	UseCount   int        `json:"use_count"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// ProtocolStore persists skill protocols. Origin records who last saved the
// protocol ("master" or "clark") so clark-initiated saves can be reported.
type ProtocolStore interface {
	UpsertProtocol(p Protocol) (Protocol, error)
	ListProtocols() ([]Protocol, error)
	GetProtocol(slug string) (Protocol, error)
	DeleteProtocol(id int64) error
	TouchProtocol(id int64) error
}

func (s *Store) UpsertProtocol(p Protocol) (Protocol, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if p.Slug == "" || p.Title == "" {
		return Protocol{}, fmt.Errorf("protocol slug and title are required")
	}
	if len(p.Body) > maxProtocolBody {
		return Protocol{}, fmt.Errorf("protocol body is %d bytes, max is %d", len(p.Body), maxProtocolBody)
	}
	origin := p.Origin
	if origin != "clark" {
		origin = "master"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO protocols (slug, title, body, origin, version)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(slug) DO UPDATE SET
			title = excluded.title,
			body = excluded.body,
			origin = excluded.origin,
			version = protocols.version + 1,
			updated_at = CURRENT_TIMESTAMP`,
		p.Slug, p.Title, p.Body, origin)
	if err != nil {
		return Protocol{}, fmt.Errorf("fail to upsert protocol: %w", err)
	}
	return s.GetProtocol(p.Slug)
}

func (s *Store) ListProtocols() ([]Protocol, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, slug, title, body, origin, version, use_count, last_used_at, created_at, updated_at
		FROM protocols ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("fail to list protocols: %w", err)
	}
	defer rows.Close()
	protocols := []Protocol{}
	for rows.Next() {
		p, err := scanProtocol(rows.Scan)
		if err != nil {
			return nil, err
		}
		protocols = append(protocols, p)
	}
	return protocols, rows.Err()
}

func (s *Store) GetProtocol(slug string) (Protocol, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `SELECT id, slug, title, body, origin, version, use_count, last_used_at, created_at, updated_at
		FROM protocols WHERE slug = ?`, slug)
	p, err := scanProtocol(row.Scan)
	if err == sql.ErrNoRows {
		return Protocol{}, fmt.Errorf("protocol %q not found", slug)
	}
	if err != nil {
		return Protocol{}, fmt.Errorf("fail to get protocol: %w", err)
	}
	return p, nil
}

func (s *Store) DeleteProtocol(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM protocols WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("fail to delete protocol: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("protocol id %d not found", id)
	}
	return nil
}

func (s *Store) TouchProtocol(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE protocols SET use_count = use_count + 1, last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("fail to touch protocol: %w", err)
	}
	return nil
}

func scanProtocol(scan func(dest ...any) error) (Protocol, error) {
	var p Protocol
	var lastUsed sql.NullTime
	if err := scan(&p.ID, &p.Slug, &p.Title, &p.Body, &p.Origin, &p.Version, &p.UseCount, &lastUsed, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Protocol{}, err
	}
	if lastUsed.Valid {
		p.LastUsedAt = &lastUsed.Time
	}
	return p, nil
}

// maxProtocolBody bounds a protocol document so a runaway save can never
// blow up the prompt context when loaded.
const maxProtocolBody = 8 << 10
