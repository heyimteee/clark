package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/heyimteee/clark/internal/logging"
)

// Event-driven scanning of chat.db.
//
// Polling every second wakes a MacBook's CPU forever to service a bridge that is
// idle almost all of the time, and a 1s tick can still land in the middle of a
// row write, reading a partially-committed message. kqueue events fix both: the
// bridge sleeps until the filesystem says something changed.
//
// Two details decide whether this actually works:
//
//   - Debounce. Messages.app writes the row, then follows up with WAL flushes,
//     attachment metadata, and an is_from_me correction, all within a few
//     milliseconds. Undebounced, one message triggers four scans.
//   - Re-arming. fsnotify's kqueue backend watches an open file descriptor, so a
//     sidecar deleted by a SQLite checkpoint leaves the watch pointing at a dead
//     inode. The parent-directory watch is what tells us to re-add it; without
//     that, the watcher goes permanently deaf after the first checkpoint.
const (
	// scanDebounce is the quiet period after the last filesystem event before a
	// scan runs. Long enough to swallow a message's follow-up writes, short
	// enough to feel immediate.
	scanDebounce = 250 * time.Millisecond

	// fallbackPollInterval is the low-frequency safety net for dropped or
	// coalesced events, which macOS does deliver under load and around
	// sleep/wake. It is a backstop, not the primary mechanism.
	fallbackPollInterval = 5 * time.Second
)

// startFileWatch begins watching the Messages database and its SQLite sidecars.
// Failure is never fatal: the caller keeps the fallback poll, so the bridge
// degrades to the old behaviour rather than going deaf.
func (w *Watcher) startFileWatch(ctx context.Context) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		logging.Log("BRIDGE", logging.SevWarn, "WATCH", "Filesystem watch unavailable; falling back to polling alone",
			"error", err, "poll_interval", fallbackPollInterval)
		return
	}
	w.fs = fsw
	w.armWatches()
	go w.watchLoop(ctx)
}

// watchTargets is every path worth an event: the database, both sidecars, and
// the containing directory. The directory is what surfaces sidecar creation,
// deletion, and replacement.
func (w *Watcher) watchTargets() []string {
	dir := filepath.Dir(w.dbPath)
	return []string{
		w.dbPath,
		w.dbPath + "-wal",
		w.dbPath + "-shm",
		dir,
	}
}

// armWatches (re)adds every watch that currently exists on disk. Missing sidecars
// are expected and ignored — a database with no WAL is a perfectly normal state.
func (w *Watcher) armWatches() {
	if w.fs == nil {
		return
	}
	for _, p := range w.watchTargets() {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := w.fs.Add(p); err != nil {
			// A path we cannot watch is covered by the fallback poll.
			logging.Log("BRIDGE", logging.SevInfo, "WATCH", "Path not watched; fallback poll covers it",
				"path", p, "error", err)
		}
	}
}

// isInteresting reports whether an event means chat.db may have new rows. The
// parent directory also reports unrelated churn in ~/Library/Messages, which is
// why sidecar names are matched explicitly.
func isInteresting(path string) bool {
	base := filepath.Base(path)
	return base == "chat.db" ||
		strings.HasSuffix(base, "chat.db-wal") ||
		strings.HasSuffix(base, "chat.db-shm")
}

// watchLoop collapses filesystem events into debounced scan triggers.
func (w *Watcher) watchLoop(ctx context.Context) {
	// Go 1.23+ timer channels are unbuffered, so Stop/Reset need no drain: a
	// stale value can never be received after the call returns.
	timer := time.NewTimer(scanDebounce)
	timer.Stop()
	defer timer.Stop()

	var pending <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			if !isInteresting(ev.Name) {
				continue
			}
			// A rotated or replaced sidecar leaves a dead watch behind; re-add
			// the whole set so the next checkpoint cannot deafen us.
			if ev.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				w.rearm()
			}
			timer.Reset(scanDebounce)
			pending = timer.C
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			logging.Log("BRIDGE", logging.SevWarn, "WATCH", "Filesystem watch error; relying on fallback poll", "error", err)
		case <-pending:
			pending = nil
			w.triggerScan()
		}
	}
}

// rearm drops and re-adds every watch. fsnotify keeps watches keyed by path, so
// re-adding an existing path replaces the stale descriptor.
func (w *Watcher) rearm() {
	if w.fs == nil {
		return
	}
	for _, p := range w.watchTargets() {
		_ = w.fs.Remove(p)
	}
	w.armWatches()
}

// triggerScan signals the scan loop without blocking. A dropped signal is
// harmless: the fallback poll and the next event both re-check the watermark, so
// the cursor is the real safety net, not the signal.
func (w *Watcher) triggerScan() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// stopFileWatch releases the kqueue descriptor.
func (w *Watcher) stopFileWatch() {
	if w.fs != nil {
		_ = w.fs.Close()
	}
}

// runScanLoop is the single place scans are triggered from: a debounced
// filesystem event, or the low-frequency fallback poll.
func (w *Watcher) runScanLoop(ctx context.Context) {
	ticker := time.NewTicker(fallbackPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			w.scanOnce(ctx)
		case <-ticker.C:
			w.scanOnce(ctx)
		}
	}
}
