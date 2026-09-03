package store

import (
	"context"
	"fmt"
	"time"
)

// Citation is one web-research source kept ephemerally: Clark records the
// URLs each web_search returns so he can cite them when asked, then rows
// expire (48h TTL + per-conversation cap) on the next record.
type Citation struct {
	ID        int64     `json:"id"`
	JID       string    `json:"jid"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Query     string    `json:"query"`
	CreatedAt time.Time `json:"created_at"`
}

// CitationTTL bounds how long a research source stays recallable. Research
// rots fast; anything older is re-searched instead of quoted.
const CitationTTL = 48 * time.Hour

// maxCitationsPerJID caps one conversation's archive so a research-heavy
// week cannot grow it without bound.
const maxCitationsPerJID = 200

// CitationStore persists ephemeral web-research sources.
type CitationStore interface {
	RecordCitations(jid, query string, titleURL [][2]string) error
	ListCitations(jid, query string, limit int) ([]Citation, error)
}

// RecordCitations upserts (jid, url) rows — re-saving an URL refreshes its
// expiry and query — then purges expired rows and trims over-cap
// conversations, all in one transaction.
func (s *Store) RecordCitations(jid, query string, titleURL [][2]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if jid == "" {
		return fmt.Errorf("citation jid is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fail to record citations: %w", err)
	}
	defer tx.Rollback()
	for _, tu := range titleURL {
		if tu[1] == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO citations (jid, url, title, query)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(jid, url) DO UPDATE SET
				title = excluded.title,
				query = excluded.query,
				created_at = CURRENT_TIMESTAMP`, jid, tu[1], tu[0], query); err != nil {
			return fmt.Errorf("fail to upsert citation: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM citations WHERE created_at < datetime('now', '-48 hours')`); err != nil {
		return fmt.Errorf("fail to purge expired citations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM citations WHERE jid = ? AND id NOT IN
		(SELECT id FROM citations WHERE jid = ? ORDER BY created_at DESC, id DESC LIMIT ?)`, jid, jid, maxCitationsPerJID); err != nil {
		return fmt.Errorf("fail to trim citation archive: %w", err)
	}
	return tx.Commit()
}

// ListCitations returns a conversation's unexpired sources, newest first,
// optionally filtered by a substring of title, URL, or query.
func (s *Store) ListCitations(jid, query string, limit int) ([]Citation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	q := `SELECT id, jid, url, title, query, created_at FROM citations
		WHERE jid = ? AND created_at >= datetime('now', '-48 hours')`
	var args []any
	args = append(args, jid)
	if query != "" {
		q += ` AND (title LIKE '%' || ? || '%' OR url LIKE '%' || ? || '%' OR query LIKE '%' || ? || '%')`
		args = append(args, query, query, query)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("fail to list citations: %w", err)
	}
	defer rows.Close()
	var out []Citation
	for rows.Next() {
		var c Citation
		if err := rows.Scan(&c.ID, &c.JID, &c.URL, &c.Title, &c.Query, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("fail to scan citation: %w", err)
		}
		out = append(out, c)
	}
	if out == nil {
		out = []Citation{}
	}
	return out, rows.Err()
}
