package scheduler

import (
	"testing"
	"time"
)

func TestSchedulerOnceValidation(t *testing.T) {
	s, _, _ := newTestScheduler(t)

	past := time.Now().Add(-time.Hour)
	if _, err := s.UpsertFull("p", "task", "", "once", &past, nil); err == nil {
		t.Fatal("past run_at should fail")
	}
	if _, err := s.UpsertFull("n", "", "", "once", nil, nil); err == nil {
		t.Fatal("missing run_at should fail")
	}
	future := time.Now().Add(time.Hour)
	if _, err := s.UpsertFull("n", "", "", "once", &future, nil); err == nil {
		t.Fatal("new one-time schedule without task should fail")
	}
	if _, err := s.UpsertFull("n", "t", "", "bogus", nil, nil); err == nil {
		t.Fatal("bogus kind should fail")
	}
}

func TestSchedulerOnceFireAutoDisables(t *testing.T) {
	s, st, fired := newTestScheduler(t)

	future := time.Now().Add(50 * time.Millisecond)
	created, err := s.UpsertFull("once-job", "do once", "", "once", &future, nil)
	if err != nil {
		t.Fatalf("UpsertFull: %v", err)
	}
	if !created.IsOnce() || created.RunAt == nil {
		t.Fatalf("not stored as once: %+v", created)
	}
	s.mu.Lock()
	timers := len(s.timers)
	s.mu.Unlock()
	if timers != 1 {
		t.Fatalf("timers = %d, want 1", timers)
	}

	// Fire directly (deterministic) instead of waiting on the timer.
	s.fireOnce(created.ID, created.Name, created.Task)()

	if len(*fired) != 1 || (*fired)[0].name != "once-job" {
		t.Fatalf("fired wrong: %+v", *fired)
	}
	persisted, err := st.GetSchedule("once-job")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if persisted.Enabled {
		t.Fatal("one-time schedule should auto-disable after firing")
	}
	if persisted.LastRunAt == nil {
		t.Fatal("LastRunAt should be set after fire")
	}
	s.mu.Lock()
	timers = len(s.timers)
	s.mu.Unlock()
	if timers != 0 {
		t.Fatalf("timers = %d after fire, want 0", timers)
	}
}

func TestSchedulerOnceListNextRun(t *testing.T) {
	s, _, _ := newTestScheduler(t)

	future := time.Now().Add(2 * time.Hour).Round(time.Second)
	if _, err := s.UpsertFull("soon", "task", "", "once", &future, nil); err != nil {
		t.Fatalf("UpsertFull: %v", err)
	}
	rows, next, err := s.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("List: %v len=%d", err, len(rows))
	}
	if next[0].IsZero() {
		t.Fatal("next run should be set for pending one-time schedule")
	}
	if !next[0].Equal(future) {
		t.Fatalf("next run = %v, want %v", next[0], future)
	}
}

func TestSchedulerOnceMergeKeepsRunAt(t *testing.T) {
	s, _, _ := newTestScheduler(t)

	future := time.Now().Add(time.Hour)
	if _, err := s.UpsertFull("m", "original", "", "once", &future, nil); err != nil {
		t.Fatalf("UpsertFull: %v", err)
	}
	// Task-only edit with empty kind keeps once-ness and run time.
	updated, err := s.UpsertFull("m", "edited", "", "", nil, nil)
	if err != nil {
		t.Fatalf("merge edit: %v", err)
	}
	if !updated.IsOnce() || updated.Task != "edited" {
		t.Fatalf("merge lost once-ness: %+v", updated)
	}
	if updated.RunAt == nil || !updated.RunAt.Equal(future) {
		t.Fatalf("merge lost run_at: %+v", updated.RunAt)
	}
	// Pause keeps the run time for later resume.
	off := false
	paused, err := s.UpsertFull("m", "", "", "", nil, &off)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.Enabled || paused.RunAt == nil {
		t.Fatalf("pause wrong: %+v", paused)
	}
}

func TestSchedulerRename(t *testing.T) {
	s, st, _ := newTestScheduler(t)

	if _, err := s.Upsert("old", "task", "0 6 * * *", nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	renamed, err := s.Rename("old", "new")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Name != "new" || renamed.Task != "task" {
		t.Fatalf("rename wrong: %+v", renamed)
	}
	if _, err := st.GetSchedule("old"); err == nil {
		t.Fatal("old name should be gone")
	}
	if _, err := s.Rename("new", "new"); err != nil {
		t.Fatalf("no-op rename should succeed: %v", err)
	}
	if _, err := s.Upsert("other", "t", "0 7 * * *", nil); err != nil {
		t.Fatalf("Upsert other: %v", err)
	}
	if _, err := s.Rename("new", "other"); err == nil {
		t.Fatal("rename onto existing name should fail")
	}
}
