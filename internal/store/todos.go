package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *Store) AddTodo(jid, text string, priority int, dueAt *time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `INSERT INTO todos (jid, text, priority, due_at) VALUES (?, ?, ?, ?)`, jid, text, priority, dueAt)
	if err != nil {
		return 0, fmt.Errorf("fail to add todo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("fail to read todo id: %w", err)
	}
	return id, nil
}

func (s *Store) ListTodos(jid, status string, limit int) ([]Todo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	query := `SELECT id, jid, text, status, priority, due_at, created_at, completed_at FROM todos WHERE 1=1`
	var args []any
	if jid != "" {
		query += ` AND jid = ?`
		args = append(args, jid)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY priority DESC, id ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("fail to list todos: %w", err)
	}
	defer rows.Close()
	var out []Todo
	for rows.Next() {
		var t Todo
		var dueAt sql.NullTime
		var completedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.JID, &t.Text, &t.Status, &t.Priority, &dueAt, &t.CreatedAt, &completedAt); err != nil {
			return nil, fmt.Errorf("fail to scan todo: %w", err)
		}
		if dueAt.Valid {
			t.DueAt = &dueAt.Time
		}
		if completedAt.Valid {
			t.CompletedAt = &completedAt.Time
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CompleteTodo(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `UPDATE todos SET status = 'done', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, id); err != nil {
		return fmt.Errorf("fail to complete todo: %w", err)
	}
	return nil
}

func (s *Store) DeleteTodo(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM todos WHERE id = ?`, id); err != nil {
		return fmt.Errorf("fail to delete todo: %w", err)
	}
	return nil
}

func (s *Store) ClearTodos(jid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM todos WHERE jid = ?`, jid); err != nil {
		return fmt.Errorf("fail to clear todos: %w", err)
	}
	return nil
}
