package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/store"
)

type firedSchedule struct {
	name string
	task string
}

func newTestScheduler(t *testing.T) (*Scheduler, *store.Store, *[]firedSchedule) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fired := &[]firedSchedule{}
	s := New(st, func(_ context.Context, name, task string) {
		*fired = append(*fired, firedSchedule{name, task})
	})
	return s, st, fired
}

func TestSchedulerUpsertValidation(t *testing.T) {
	s, _, _ := newTestScheduler(t)

	if _, err := s.Upsert("", "task", "* * * * *", nil); err == nil {
		t.Fatal("empty name should fail")
	}
	if _, err := s.Upsert("bad", "task", "not a spec", nil); err == nil {
		t.Fatal("invalid spec should fail")
	}
	if _, err := s.Upsert("new", "", "0 6 * * *", nil); err == nil {
		t.Fatal("new schedule without task should fail")
	}
}

func TestSchedulerUpsertMergeSemantics(t *testing.T) {
	s, st, _ := newTestScheduler(t)

	created, err := s.Upsert("news", "gather news", "0 6 * * *", nil)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !created.Enabled {
		t.Fatal("new schedule should default to enabled")
	}

	// Pause: blank task/spec keep existing values, enabled=false applies.
	paused, err := s.Upsert("news", "", "", &[]bool{false}[0])
	if err != nil {
		t.Fatalf("Upsert pause: %v", err)
	}
	if paused.Task != "gather news" || paused.Spec != "0 6 * * *" {
		t.Fatalf("merge lost values: task=%q spec=%q", paused.Task, paused.Spec)
	}
	if paused.Enabled {
		t.Fatal("paused schedule should be disabled")
	}
	persisted, err := st.GetSchedule("news")
	if err != nil || persisted.Enabled {
		t.Fatalf("pause not persisted: %v enabled=%v", err, persisted.Enabled)
	}

	// Re-enable without touching task/spec.
	resumed, err := s.Upsert("news", "", "30 7 * * *", &[]bool{true}[0])
	if err != nil {
		t.Fatalf("Upsert resume: %v", err)
	}
	if resumed.Task != "gather news" || resumed.Spec != "30 7 * * *" || !resumed.Enabled {
		t.Fatalf("resume merge wrong: %+v", resumed)
	}
}

func TestSchedulerListAndNextRun(t *testing.T) {
	s, _, _ := newTestScheduler(t)

	if _, _, err := s.List(); err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if _, err := s.Upsert("six", "do it", "0 6 * * *", nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	rows, next, err := s.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("List: %v len=%d", err, len(rows))
	}
	if next[0].IsZero() {
		t.Fatal("next run should be set for enabled schedule")
	}
	if !next[0].After(time.Now().Add(-time.Second)) {
		t.Fatalf("next run in the past: %v", next[0])
	}
	// Next run at 06:00 local.
	wantHour := 6
	if got := next[0].Hour(); got != wantHour {
		t.Fatalf("next run hour = %d, want %d", got, wantHour)
	}
}

func TestSchedulerFireRunsAndMarks(t *testing.T) {
	s, st, fired := newTestScheduler(t)

	created, err := s.Upsert("ping", "say hi", "0 6 * * *", nil)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s.fire(created.ID, created.Name, created.Task)()

	if len(*fired) != 1 {
		t.Fatalf("run called %d times, want 1", len(*fired))
	}
	if (*fired)[0].name != "ping" || (*fired)[0].task != "say hi" {
		t.Fatalf("fire args wrong: %+v", (*fired)[0])
	}
	persisted, err := st.GetSchedule("ping")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if persisted.LastRunAt == nil {
		t.Fatal("LastRunAt should be set after fire")
	}
}

func TestSchedulerSetEnabledAndDelete(t *testing.T) {
	s, st, _ := newTestScheduler(t)

	if _, err := s.Upsert("job", "work", "*/5 * * * *", nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.SetEnabled("job", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	persisted, err := st.GetSchedule("job")
	if err != nil || persisted.Enabled {
		t.Fatalf("disable not persisted: %v %v", err, persisted.Enabled)
	}
	s.mu.Lock()
	entries := len(s.entries)
	s.mu.Unlock()
	if entries != 0 {
		t.Fatalf("disabled schedule still has %d cron entries", entries)
	}

	if _, err := s.Upsert("job", "", "", &[]bool{true}[0]); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	s.mu.Lock()
	entries = len(s.entries)
	s.mu.Unlock()
	if entries != 1 {
		t.Fatalf("enabled schedule has %d cron entries, want 1", entries)
	}

	if err := s.Delete("job"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.GetSchedule("job"); err == nil {
		t.Fatal("schedule should be gone")
	}
	s.mu.Lock()
	entries = len(s.entries)
	s.mu.Unlock()
	if entries != 0 {
		t.Fatalf("deleted schedule still has %d cron entries", entries)
	}
}

func TestSchedulerStartLoadsEnabledOnly(t *testing.T) {
	s, st, _ := newTestScheduler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := s.Upsert("on", "a", "0 5 * * *", &[]bool{true}[0]); err != nil {
		t.Fatalf("Upsert on: %v", err)
	}
	if _, err := s.Upsert("off", "b", "0 7 * * *", &[]bool{false}[0]); err != nil {
		t.Fatalf("Upsert off: %v", err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.mu.Lock()
	entries := len(s.entries)
	started := s.started
	s.mu.Unlock()
	if entries != 1 || !started {
		t.Fatalf("after Start: entries=%d started=%v, want 1 true", entries, started)
	}
	_ = st
}

func TestSchedulerUpsertRejectsGarbageSpecMessage(t *testing.T) {
	s, _, _ := newTestScheduler(t)
	_, err := s.Upsert("x", "t", "banana", nil)
	if err == nil {
		t.Fatal("garbage spec should fail")
	}
	if !strings.Contains(err.Error(), "banana") {
		t.Fatalf("error should mention the spec: %v", err)
	}
}
