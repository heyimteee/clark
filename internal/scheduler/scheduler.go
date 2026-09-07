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
// so chat tools and web REST share one mutation path. Recurring schedules
// run on cron; one-time schedules run on a single timer and auto-disable
// after firing.
type Scheduler struct {
	st  store.ScheduleStore
	run RunFunc

	cron    *cron.Cron
	mu      sync.Mutex
	entries map[int64]cron.EntryID
	timers  map[int64]*time.Timer
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
		timers:  map[int64]*time.Timer{},
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
		if sc.IsOnce() {
			if err := s.addTimerLocked(sc); err != nil {
				logging.Log("SCHED", logging.SevWarn, "START", "Skipping one-time schedule", "name", sc.Name, "error", err)
			}
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
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, t := range s.timers {
			t.Stop()
			delete(s.timers, id)
		}
	}()
	logging.Log("SCHED", logging.SevInfo, "START", "Scheduler running", "entries", len(s.entries))
	return nil
}

// Upsert creates or updates a recurring schedule by name. Blank task/spec keep
// the existing values (merge semantics), and a nil enabled keeps the current
// flag — so pausing is Upsert(name, "", "", &false). The spec is validated
// before anything is persisted.
func (s *Scheduler) Upsert(name, task, spec string, enabled *bool) (store.Schedule, error) {
	return s.UpsertFull(name, task, spec, "recurring", nil, enabled)
}

// UpsertFull creates or updates a schedule of either kind. An empty kind
// keeps the existing kind ("recurring" for new schedules). For "recurring"
// the cron rules of Upsert apply; for "once" runAt sets the fire time and
// the spec is ignored (stored empty). A nil runAt on an existing one-time
// schedule keeps its run time. Enabling a schedule whose one-time run time
// already passed is rejected.
func (s *Scheduler) UpsertFull(name, task, spec, kind string, runAt *time.Time, enabled *bool) (store.Schedule, error) {
	if name == "" {
		return store.Schedule{}, fmt.Errorf("schedule name is required")
	}
	if kind != "" && kind != "recurring" && kind != "once" {
		return store.Schedule{}, fmt.Errorf("schedule kind must be recurring or once")
	}
	sc, err := s.st.GetSchedule(name)
	isNew := err != nil
	if isNew {
		if kind == "" {
			kind = "recurring"
		}
		if kind == "recurring" {
			if task == "" || spec == "" {
				return store.Schedule{}, fmt.Errorf("task and spec are required for a new schedule")
			}
		} else if task == "" || runAt == nil {
			return store.Schedule{}, fmt.Errorf("task and run_at are required for a new one-time schedule")
		}
		sc = store.Schedule{Name: name, Enabled: true}
	} else if kind == "" {
		kind = sc.Kind
		if kind == "" {
			kind = "recurring"
		}
	}
	if kind == "recurring" {
		if _, err := cron.ParseStandard(spec); spec != "" && err != nil {
			return store.Schedule{}, fmt.Errorf("invalid cron spec %q: %w", spec, err)
		}
	} else if runAt == nil {
		runAt = sc.RunAt
		if runAt == nil {
			return store.Schedule{}, fmt.Errorf("run_at is required for a one-time schedule")
		}
	}
	willEnable := sc.Enabled
	if enabled != nil {
		willEnable = *enabled
	}
	if kind == "once" && willEnable && !runAt.After(time.Now()) {
		return store.Schedule{}, fmt.Errorf("run_at must be in the future")
	}
	if task != "" {
		sc.Task = task
	}
	if kind == "recurring" {
		if spec != "" {
			sc.Spec = spec
		}
		sc.Kind = "recurring"
		sc.RunAt = nil
	} else {
		sc.Kind = "once"
		sc.Spec = ""
		sc.RunAt = runAt
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

// Rename changes a schedule's name, keeping its id and timing. Live cron and
// timer entries are resynced so the next fire uses the new name.
func (s *Scheduler) Rename(oldName, newName string) (store.Schedule, error) {
	if oldName == "" || newName == "" {
		return store.Schedule{}, fmt.Errorf("old and new schedule names are required")
	}
	if oldName == newName {
		return s.st.GetSchedule(oldName)
	}
	sc, err := s.st.GetSchedule(oldName)
	if err != nil {
		return store.Schedule{}, err
	}
	if _, err := s.st.GetSchedule(newName); err == nil {
		return store.Schedule{}, fmt.Errorf("schedule %q already exists", newName)
	}
	if err := s.st.RenameSchedule(sc.ID, newName); err != nil {
		return store.Schedule{}, err
	}
	saved, err := s.st.GetSchedule(newName)
	if err != nil {
		return store.Schedule{}, err
	}
	s.resync(saved)
	return saved, nil
}

// SetEnabled toggles a schedule live (persisted + resynced). Enabling a
// one-time schedule whose run time already passed is rejected.
func (s *Scheduler) SetEnabled(name string, enabled bool) error {
	sc, err := s.st.GetSchedule(name)
	if err != nil {
		return err
	}
	if enabled && sc.IsOnce() && (sc.RunAt == nil || !sc.RunAt.After(time.Now())) {
		return fmt.Errorf("one-time schedule %q already passed", name)
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
	if t, ok := s.timers[sc.ID]; ok {
		t.Stop()
		delete(s.timers, sc.ID)
	}
	return nil
}

// Get fetches one schedule by name.
func (s *Scheduler) Get(name string) (store.Schedule, error) {
	return s.st.GetSchedule(name)
}

// List returns every persisted schedule with its computed next run (zero
// time when disabled, invalid, or already passed).
func (s *Scheduler) List() ([]store.Schedule, []time.Time, error) {
	rows, err := s.st.ListSchedules()
	if err != nil {
		return nil, nil, err
	}
	next := make([]time.Time, len(rows))
	for i, sc := range rows {
		next[i] = s.NextRunFor(sc)
		if !sc.Enabled {
			next[i] = time.Time{}
		}
	}
	return rows, next, nil
}

// NextRunFor computes the next fire time for a schedule in the scheduler's
// zone: the cron next time for recurring, run_at for a pending one-time job.
func (s *Scheduler) NextRunFor(sc store.Schedule) time.Time {
	if sc.IsOnce() {
		if sc.RunAt != nil && sc.RunAt.After(time.Now()) {
			return *sc.RunAt
		}
		return time.Time{}
	}
	next, _ := s.NextRun(sc.Spec)
	return next
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
	if t, ok := s.timers[sc.ID]; ok {
		t.Stop()
		delete(s.timers, sc.ID)
	}
	if !sc.Enabled {
		return
	}
	if sc.IsOnce() {
		if err := s.addTimerLocked(sc); err != nil {
			logging.Log("SCHED", logging.SevWarn, "SYNC", "Could not schedule one-time entry", "name", sc.Name, "error", err)
		}
		return
	}
	if err := s.addEntryLocked(sc); err != nil {
		logging.Log("SCHED", logging.SevWarn, "SYNC", "Could not schedule entry", "name", sc.Name, "error", err)
	}
}

// addTimerLocked arms the single-shot timer for a one-time schedule. A past
// run time disables the schedule instead of firing immediately.
func (s *Scheduler) addTimerLocked(sc store.Schedule) error {
	if sc.RunAt == nil {
		return fmt.Errorf("one-time schedule %q has no run time", sc.Name)
	}
	delay := time.Until(*sc.RunAt)
	if delay <= 0 {
		if err := s.st.SetScheduleEnabled(sc.ID, false); err != nil {
			return err
		}
		return fmt.Errorf("one-time schedule %q already passed; disabled", sc.Name)
	}
	s.timers[sc.ID] = time.AfterFunc(delay, s.fireOnce(sc.ID, sc.Name, sc.Task))
	return nil
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

// fireOnce wraps one one-time execution: bounded context, run callback, run
// timestamp, then auto-disable so it never fires twice.
func (s *Scheduler) fireOnce(id int64, name, task string) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), maxRunDuration)
		defer cancel()
		logging.Log("SCHED", logging.SevInfo, "FIRE", "One-time schedule firing", "name", name)
		s.run(ctx, name, task)
		if err := s.st.MarkScheduleRun(id); err != nil {
			logging.Log("SCHED", logging.SevWarn, "FIRE", "Could not mark run", "name", name, "error", err)
		}
		if err := s.st.SetScheduleEnabled(id, false); err != nil {
			logging.Log("SCHED", logging.SevWarn, "FIRE", "Could not auto-disable one-time schedule", "name", name, "error", err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.timers, id)
	}
}
