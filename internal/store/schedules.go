package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Schedule is one recurring master task: a cron spec plus the prompt executed
// with master privileges when it fires.
type Schedule struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Task      string     `json:"task"`
	Spec      string     `json:"spec"`
	Enabled   bool       `json:"enabled"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// ScheduleStore persists recurring task definitions.
type ScheduleStore interface {
	UpsertSchedule(s Schedule) (Schedule, error)
	ListSchedules() ([]Schedule, error)
	GetSchedule(name string) (Schedule, error)
	DeleteSchedule(id int64) error
	SetScheduleEnabled(id int64, enabled bool) error
	MarkScheduleRun(id int64) error
}

func (s *Store) UpsertSchedule(sc Schedule) (Schedule, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if sc.Name == "" {
		return Schedule{}, fmt.Errorf("schedule name is required")
	}
	enabled := 0
	if sc.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO schedules (name, task, spec, enabled)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			task = excluded.task,
			spec = excluded.spec,
			enabled = excluded.enabled`,
		sc.Name, sc.Task, sc.Spec, enabled)
	if err != nil {
		return Schedule{}, fmt.Errorf("fail to upsert schedule: %w", err)
	}
	return s.GetSchedule(sc.Name)
}

func (s *Store) ListSchedules() ([]Schedule, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, task, spec, enabled, last_run_at, created_at FROM schedules ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("fail to list schedules: %w", err)
	}
	defer rows.Close()
	schedules := []Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows.Scan)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, sc)
	}
	return schedules, rows.Err()
}

func (s *Store) GetSchedule(name string) (Schedule, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `SELECT id, name, task, spec, enabled, last_run_at, created_at FROM schedules WHERE name = ?`, name)
	sc, err := scanSchedule(row.Scan)
	if err == sql.ErrNoRows {
		return Schedule{}, fmt.Errorf("schedule %q not found", name)
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("fail to get schedule: %w", err)
	}
	return sc, nil
}

func (s *Store) DeleteSchedule(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("fail to delete schedule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("schedule id %d not found", id)
	}
	return nil
}

func (s *Store) SetScheduleEnabled(id int64, enabled bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	flag := 0
	if enabled {
		flag = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET enabled = ? WHERE id = ?`, flag, id)
	if err != nil {
		return fmt.Errorf("fail to update schedule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("schedule id %d not found", id)
	}
	return nil
}

func (s *Store) MarkScheduleRun(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE schedules SET last_run_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("fail to mark schedule run: %w", err)
	}
	return nil
}

func scanSchedule(scan func(dest ...any) error) (Schedule, error) {
	var sc Schedule
	var lastRun sql.NullTime
	var enabled int
	if err := scan(&sc.ID, &sc.Name, &sc.Task, &sc.Spec, &enabled, &lastRun, &sc.CreatedAt); err != nil {
		return Schedule{}, err
	}
	sc.Enabled = enabled == 1
	if lastRun.Valid {
		sc.LastRunAt = &lastRun.Time
	}
	return sc, nil
}
