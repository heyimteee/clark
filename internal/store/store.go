// Package store defines clark's persistence interfaces and their SQLite implementation.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Message is a single chat history entry.
type Message struct {
	Role    string
	Content string
}

// HistoryEntry is one stored message tagged with its conversation.
type HistoryEntry struct {
	JID     string
	Role    string
	Content string
}

// VIPEntry is a person admitted to the inner circle.
type VIPEntry struct {
	JID      string
	Name     string
	Relation string
}

// Settings persists the assistant's key/value settings.
type Settings interface {
	IsInitialized() (bool, error)
	InitDefaults() error
	Get(key string) (string, error)
	Set(key, value string) error
}

// VIPStore persists the inner-circle list and each VIP's status override.
type VIPStore interface {
	All() ([]VIPEntry, error)
	Add(entry VIPEntry) error
	Delete(jid string) error
	ClearAll() error
	SetEnabled(jid string, on bool) error
	Enabled(jid string) (on bool, ok bool, err error)
	ClearEnabled(jid string) error
	ClearAllEnabled() error
}

// AccessStore persists each VIP's granted tool set.
type AccessStore interface {
	GetTools(jid string) (tools []string, ok bool, err error)
	SetTools(jid string, tools []string) error
	DeleteAccess(jid string) error
}

// HistoryStore persists per-contact chat history.
type HistoryStore interface {
	SaveMessage(jid, role, content string) error
	Messages(jid string) ([]Message, error)
	RecentMessages(jid string, limit int) ([]Message, error)
	AllRecentMessages(limit int) ([]HistoryEntry, error)
}

// Store is the SQLite-backed implementation of Settings, VIPStore and HistoryStore.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at dbPath and runs migrations.
func Open(dbPath string) (*Store, error) {
	rawDb, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("fail to open database: %w", err)
	}

	if err := rawDb.Ping(); err != nil {
		rawDb.Close()
		return nil, fmt.Errorf("fail to ping database: %w", err)
	}

	s := &Store{db: rawDb}
	if err := s.migrate(); err != nil {
		rawDb.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stmts := []struct {
		name string
		sql  string
	}{
		{"vip", `CREATE TABLE IF NOT EXISTS vip (
			jid TEXT PRIMARY KEY,
			name TEXT,
			relation TEXT
		);`},
		{"vip_status", `CREATE TABLE IF NOT EXISTS vip_status (
			jid TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL
		);`},
		{"vip_access", `CREATE TABLE IF NOT EXISTS vip_access (
			jid TEXT PRIMARY KEY,
			tools TEXT NOT NULL
		);`},
		{"assistant_setting", `CREATE TABLE IF NOT EXISTS assistant_setting (
			key TEXT PRIMARY KEY,
			value TEXT
		);`},
		{"chat_history", `CREATE TABLE IF NOT EXISTS chat_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			jid TEXT,
			role TEXT,
			content TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		);`},
	}

	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt.sql); err != nil {
			return fmt.Errorf("fail to create table <%s>: %w", stmt.name, err)
		}
	}
	return nil
}

// IsInitialized reports whether the default settings have been seeded.
func (s *Store) IsInitialized() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assistant_setting WHERE key IN ('name', 'status', 'context')`).Scan(&count); err != nil {
		return false, fmt.Errorf("fail to load table <assistant_setting>: %w", err)
	}
	return count == 3, nil
}

// InitDefaults seeds the assistant's default settings without overwriting existing values.
func (s *Store) InitDefaults() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	defaults := map[string]string{
		"name":    "Clark",
		"status":  "false",
		"context": "",
	}

	for key, value := range defaults {
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO assistant_setting (key, value) VALUES (?, ?)`, key, value); err != nil {
			return fmt.Errorf("fail to initialize default for %s: %w", key, err)
		}
	}
	return nil
}

// Get returns the value for key, or "" when the key is absent.
func (s *Store) Get(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM assistant_setting WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// Set upserts the value for key.
func (s *Store) Set(key, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO assistant_setting (key, value) VALUES (?, ?)`, key, value); err != nil {
		return fmt.Errorf("failed to update %s in DB: %w", key, err)
	}
	return nil
}
