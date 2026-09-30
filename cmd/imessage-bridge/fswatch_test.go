package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIsInteresting pins which paths are worth waking a scan for. The parent
// directory also reports unrelated churn under ~/Library/Messages, so sidecar
// names must be matched explicitly rather than accepting every event.
func TestIsInteresting(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/Users/me/Library/Messages/chat.db", true},
		{"/Users/me/Library/Messages/chat.db-wal", true},
		{"/Users/me/Library/Messages/chat.db-shm", true},
		{"/Users/me/Library/Messages/attachments/2/photo.jpg", false},
		{"/Users/me/Library/Messages", false},
		{"/Users/me/Library/Messages/chat.db.bak", false},
	}
	for _, tc := range cases {
		if got := isInteresting(tc.path); got != tc.want {
			t.Errorf("isInteresting(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestWatchTargetsCoversSidecarsAndDirectory proves the watch set includes the
// parent directory. Without it, sidecar rotation after a SQLite checkpoint would
// go unnoticed and the watcher would permanently deafen (#208).
func TestWatchTargetsCoversSidecarsAndDirectory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "Messages", "chat.db")
	w := &Watcher{dbPath: dbPath}

	got := w.watchTargets()
	want := []string{dbPath, dbPath + "-wal", dbPath + "-shm", filepath.Dir(dbPath)}
	if len(got) != len(want) {
		t.Fatalf("watchTargets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("watchTargets[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestArmWatchesToleratesMissingSidecars proves a database with no WAL — a
// completely normal SQLite state — is not treated as a failure.
func TestArmWatchesToleratesMissingSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chat.db")
	if err := os.WriteFile(dbPath, []byte("db"), 0o600); err != nil {
		t.Fatalf("write chat.db: %v", err)
	}

	w := &Watcher{dbPath: dbPath}
	w.startFileWatch(context.Background())
	defer w.stopFileWatch()

	if w.fs == nil {
		t.Fatal("filesystem watcher unavailable on this platform")
	}
	// No sidecars exist; arming must not panic or error out.
	w.rearm()
}

// TestTriggerScanNeverBlocks proves a burst of events cannot wedge the watcher.
// A dropped signal is safe because the ROWID watermark — not the signal — is what
// guarantees nothing is lost.
func TestTriggerScanNeverBlocks(t *testing.T) {
	w := &Watcher{wake: make(chan struct{}, 1)}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			w.triggerScan()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("triggerScan blocked; a burst of events would wedge the watcher")
	}
	if n := len(w.wake); n != 1 {
		t.Errorf("wake depth = %d, want 1 (coalesced)", n)
	}
}

// TestFileWriteTriggersScan is the end-to-end proof that a real write to chat.db
// produces a scan trigger, which is the whole point of the event watcher (#208).
func TestFileWriteTriggersScan(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chat.db")
	if err := os.WriteFile(dbPath, []byte("db"), 0o600); err != nil {
		t.Fatalf("write chat.db: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Watcher{dbPath: dbPath, wake: make(chan struct{}, 1)}
	w.startFileWatch(ctx)
	defer w.stopFileWatch()
	if w.fs == nil {
		t.Skip("filesystem watcher unavailable on this platform")
	}

	// Messages.app writing a new row is a write to chat.db and/or its WAL.
	f, err := os.OpenFile(dbPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open chat.db: %v", err)
	}
	if _, err := f.WriteString("row"); err != nil {
		f.Close()
		t.Fatalf("append: %v", err)
	}
	f.Close()

	select {
	case <-w.wake:
	case <-time.After(3 * time.Second):
		t.Fatal("writing chat.db did not trigger a scan; the event watcher is not working")
	}
}

// TestSidecarCreationTriggersRearm proves a WAL appearing after the watches were
// armed is both noticed and picked up — the case that goes deaf without it.
func TestSidecarCreationTriggersRearm(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chat.db")
	if err := os.WriteFile(dbPath, []byte("db"), 0o600); err != nil {
		t.Fatalf("write chat.db: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Watcher{dbPath: dbPath, wake: make(chan struct{}, 1)}
	w.startFileWatch(ctx)
	defer w.stopFileWatch()
	if w.fs == nil {
		t.Skip("filesystem watcher unavailable on this platform")
	}

	// A SQLite checkpoint creating the WAL is exactly the rotation case.
	if err := os.WriteFile(dbPath+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatalf("create wal: %v", err)
	}

	select {
	case <-w.wake:
	case <-time.After(3 * time.Second):
		t.Fatal("sidecar creation did not trigger a scan")
	}
}
