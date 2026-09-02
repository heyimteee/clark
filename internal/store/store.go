// Package store defines clark's persistence interfaces and their SQLite implementation.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/heyimteee/clark/internal/logging"
)

// Message is a single chat history entry.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HistoryEntry is one stored message tagged with its conversation.
type HistoryEntry struct {
	JID     string `json:"jid"`
	Role    string `json:"role"`
	Content string `json:"content"`
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

// OutboundMessage is one iMessage awaiting bridge delivery.
type OutboundMessage struct {
	ID        int64  `json:"id"`
	Recipient string `json:"recipient"`
	Text      string `json:"text"`
}

// HistoryStore persists per-contact chat history.
type HistoryStore interface {
	SaveMessage(jid, role, content string) error
	Messages(jid string) ([]Message, error)
	RecentMessages(jid string, limit int) ([]Message, error)
	AllRecentMessages(limit int) ([]HistoryEntry, error)
	ClearHistory(jid string) error
}

// Todo is one item in a per-conversation todo list.
type Todo struct {
	ID          int64      `json:"id"`
	JID         string     `json:"jid"`
	Text        string     `json:"text"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status"`
	Priority    int        `json:"priority"`
	DueAt       *time.Time `json:"due_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TodoStore persists the per-conversation todo list.
type TodoStore interface {
	AddTodo(jid, text, description string, priority int, dueAt *time.Time) (int64, error)
	ListTodos(jid, status string, limit int) ([]Todo, error)
	CompleteTodo(id int64) error
	UpdateTodoStatus(id int64, status string) error
	DeleteTodo(id int64) error
	ClearTodos(jid string) error
}

// Store is the SQLite-backed implementation of Settings, VIPStore and HistoryStore.
type Store struct {
	db *sql.DB
}

// dsnFor wraps a plain file path with the SQLite pragmas clark depends on:
// WAL journaling (readers never block behind writers), a busy timeout for the
// rare cross-pool contention with whatsmeow's own handle on the same file,
// and foreign-key enforcement. Non-file targets (:memory:, URIs) pass through.
func dsnFor(path string) string {
	if path == "" || path == ":memory:" || strings.Contains(path, ":") {
		return path
	}
	return "file:" + path + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on"
}

// Open opens (creating if needed) the SQLite database at dbPath and runs
// migrations. The file holds WhatsApp session keys and private chat history,
// so it is tightened to owner-only permissions (#61).
func Open(dbPath string) (*Store, error) {
	rawDb, err := sql.Open("sqlite3", dsnFor(dbPath))
	if err != nil {
		return nil, fmt.Errorf("fail to open database: %w", err)
	}

	if err := rawDb.Ping(); err != nil {
		rawDb.Close()
		return nil, fmt.Errorf("fail to ping database: %w", err)
	}

	// Best-effort hardening: real files only, ignore :memory: and URIs.
	if !strings.Contains(dbPath, ":") && dbPath != "" {
		if err := os.Chmod(dbPath, 0o600); err != nil && !os.IsNotExist(err) {
			logging.Log("STORE", logging.SevWarn, "OPEN", "Could not tighten database permissions", "error", err.Error())
		}
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
		{"chat_history_jid_idx", `CREATE INDEX IF NOT EXISTS idx_chat_history_jid ON chat_history(jid, id)`},
		{"imessage_outbound", `CREATE TABLE IF NOT EXISTS imessage_outbound (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			recipient TEXT NOT NULL,
			text TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			picked_at DATETIME
		);`},
		{"todos", `CREATE TABLE IF NOT EXISTS todos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			jid TEXT NOT NULL,
			text TEXT NOT NULL,
			description TEXT DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			priority INTEGER DEFAULT 0,
			due_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME
		);`},
		{"todos_desc_migrate", `ALTER TABLE todos ADD COLUMN description TEXT DEFAULT ''`},
		{"todos_jid_idx", `CREATE INDEX IF NOT EXISTS idx_todos_jid ON todos(jid, id)`},
		{"todos_status_idx", `CREATE INDEX IF NOT EXISTS idx_todos_status ON todos(status)`},
		{"meeting_notes", `CREATE TABLE IF NOT EXISTS meeting_notes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			jid TEXT NOT NULL,
			title TEXT,
			transcript TEXT,
			digest TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`},
		{"meeting_notes_jid_idx", `CREATE INDEX IF NOT EXISTS idx_meeting_notes_jid ON meeting_notes(jid, id)`},
		{"protocols", `CREATE TABLE IF NOT EXISTS protocols (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT NOT NULL UNIQUE,
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			origin TEXT NOT NULL DEFAULT 'master',
			version INTEGER NOT NULL DEFAULT 1,
			use_count INTEGER NOT NULL DEFAULT 0,
			last_used_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`},
		{"schedules", `CREATE TABLE IF NOT EXISTS schedules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			task TEXT NOT NULL,
			spec TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_run_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`},
		{"chat_history_ts_idx", `CREATE INDEX IF NOT EXISTS idx_chat_history_timestamp ON chat_history(timestamp)`},
		{"chat_history_jid_ts_idx", `CREATE INDEX IF NOT EXISTS idx_chat_history_jid_timestamp ON chat_history(jid, timestamp)`},
	}

	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt.sql); err != nil {
			// Ignore duplicate column error for idempotent migrations (e.g., description).
			if stmt.name == "todos_desc_migrate" && isDuplicateColumnError(err) {
				continue
			}
			return fmt.Errorf("fail to create table <%s>: %w", stmt.name, err)
		}
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	msg := err.Error()
	return containsFold(msg, "duplicate column name") || containsFold(msg, "already exists")
}

func containsFold(s, substr string) bool {
	// Minimal case-insensitive contains without importing strings.
	ls := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		ls[i] = c
	}
	lsub := make([]byte, len(substr))
	for i := range substr {
		c := substr[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		lsub[i] = c
	}
	return indexOf(string(ls), string(lsub)) >= 0
}

func indexOf(s, substr string) int {
	n := len(substr)
	if n == 0 {
		return 0
	}
	for i := 0; i <= len(s)-n; i++ {
		if s[i:i+n] == substr {
			return i
		}
	}
	return -1
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
