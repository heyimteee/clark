// Package scheduler runs recurring master tasks on cron schedules. It owns
// the single cron.Cron instance, keeps in-memory entries in sync with the
// schedules table, and delegates each fire to a caller-supplied run function
// so the scheduler stays decoupled from the assistant.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/store"
)

// RunFunc executes one fired schedule. Implementations reply as master and
// relay the result; the scheduler only orchestrates timing.
type RunFunc func(ctx context.Context, name, task string)

const maxRunDuration = 10 * time.Minute

// Scheduler keeps the live cron instance in sync with the schedules table.
// All mutations (Upsert/SetEnabled/Delete) persist AND resync immediately,
// so chat tools and web REST share one mutation path.
type Scheduler struct {
	st  store.ScheduleStore
	run RunFunc

	cron    *cron.Cron
	mu      sync.Mutex
	entries map[int64]cron.EntryID
	started bool
}

// New builds a scheduler over the given store. The run function is invoked
// on every fire with a cancellable context (10-minute budget).
func New(st store.ScheduleStore, run RunFunc) *Scheduler {
	logger := cron.PrintfLogger(log.New(os.Stderr, "cron: ", log.LstdFlags))
	return &Scheduler{
		st:  st,
		run: run,
		cron: cron.New(
			cron.WithLocation(time.Local),
			cron.WithChain(cron.Recover(logger), cron.SkipIfStillRunning(logger)),
		),
		entries: map[int64]cron.EntryID{},
	}
}

// Start loads every enabled schedule and runs the cron loop until ctx is
// done. Starting twice is a no-op.
func (s *Scheduler) Start(ctx context.Context) error {
	rows, err := s.st.ListSchedules()
	if err != nil {
		return fmt.Errorf("fail to load schedules: %w", err)
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	for _, sc := range rows {
		if !sc.Enabled {
			continue
		}
		if err := s.addEntryLocked(sc); err != nil {
			logging.Log("SCHED", logging.SevWarn, "START", "Skipping schedule with invalid spec", "name", sc.Name, "error", err)
		}
	}
	s.cron.Start()
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.cron.Stop()
	}()
	logging.Log("SCHED", logging.SevInfo, "START", "Scheduler running", "entries", len(s.entries))
	return nil
}

// Upsert creates or updates a schedule by name. Blank task/spec keep the
// existing values (merge semantics), and a nil enabled keeps the current
// flag — so pausing is Upsert(name, "", "", &false). The spec is validated
// before anything is persisted.
func (s *Scheduler) Upsert(name, task, spec string, enabled *bool) (store.Schedule, error) {
	if name == "" {
		return store.Schedule{}, fmt.Errorf("schedule name is required")
	}
	if _, err := cron.ParseStandard(spec); spec != "" && err != nil {
		return store.Schedule{}, fmt.Errorf("invalid cron spec %q: %w", spec, err)
	}
	sc, err := s.st.GetSchedule(name)
	if err != nil {
		if task == "" || spec == "" {
			return store.Schedule{}, fmt.Errorf("task and spec are required for a new schedule")
		}
		sc = store.Schedule{Name: name, Enabled: true}
	}
	if task != "" {
		sc.Task = task
	}
	if spec != "" {
		sc.Spec = spec
	}
	if enabled != nil {
		sc.Enabled = *enabled
	}
	saved, err := s.st.UpsertSchedule(sc)
	if err != nil {
		return store.Schedule{}, err
	}
	s.resync(saved)
	return saved, nil
}

// SetEnabled toggles a schedule live (persisted + resynced).
func (s *Scheduler) SetEnabled(name string, enabled bool) error {
	sc, err := s.st.GetSchedule(name)
	if err != nil {
		return err
	}
	if err := s.st.SetScheduleEnabled(sc.ID, enabled); err != nil {
		return err
	}
	sc.Enabled = enabled
	s.resync(sc)
	return nil
}

// Delete removes a schedule by name (persisted + unscheduled).
func (s *Scheduler) Delete(name string) error {
	sc, err := s.st.GetSchedule(name)
	if err != nil {
		return err
	}
	if err := s.st.DeleteSchedule(sc.ID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[sc.ID]; ok {
		s.cron.Remove(entry)
		delete(s.entries, sc.ID)
	}
	return nil
}

// List returns every persisted schedule with its computed next run (zero
// time when disabled or invalid).
func (s *Scheduler) List() ([]store.Schedule, []time.Time, error) {
	rows, err := s.st.ListSchedules()
	if err != nil {
		return nil, nil, err
	}
	next := make([]time.Time, len(rows))
	for i, sc := range rows {
		next[i], _ = s.NextRun(sc.Spec)
		if !sc.Enabled {
			next[i] = time.Time{}
		}
	}
	return rows, next, nil
}

// NextRun computes the next fire time for a spec in the scheduler's zone.
func (s *Scheduler) NextRun(spec string) (time.Time, error) {
	sched, err := cron.ParseStandard(spec)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(time.Now()), nil
}

func (s *Scheduler) resync(sc store.Schedule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[sc.ID]; ok {
		s.cron.Remove(entry)
		delete(s.entries, sc.ID)
	}
	if !sc.Enabled {
		return
	}
	if err := s.addEntryLocked(sc); err != nil {
		logging.Log("SCHED", logging.SevWarn, "SYNC", "Could not schedule entry", "name", sc.Name, "error", err)
	}
}

func (s *Scheduler) addEntryLocked(sc store.Schedule) error {
	if _, err := cron.ParseStandard(sc.Spec); err != nil {
		return err
	}
	entry, err := s.cron.AddFunc(sc.Spec, s.fire(sc.ID, sc.Name, sc.Task))
	if err != nil {
		return err
	}
	s.entries[sc.ID] = entry
	return nil
}

// fire wraps one execution: bounded context, run callback, run timestamp.
func (s *Scheduler) fire(id int64, name, task string) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), maxRunDuration)
		defer cancel()
		logging.Log("SCHED", logging.SevInfo, "FIRE", "Schedule firing", "name", name)
		s.run(ctx, name, task)
		if err := s.st.MarkScheduleRun(id); err != nil {
			logging.Log("SCHED", logging.SevWarn, "FIRE", "Could not mark run", "name", name, "error", err)
		}
	}
}
