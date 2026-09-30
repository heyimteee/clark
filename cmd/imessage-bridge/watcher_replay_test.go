package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// fixedClock returns a time source frozen at t, so a test can place a row either
// side of the watcher's last-scan marker deterministically.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// newWatcherAt builds a watcher with a controlled clock and an empty cursor.
func newWatcherAt(t *testing.T, db *sql.DB, clock func() time.Time) *Watcher {
	t.Helper()
	return &Watcher{
		db:        db,
		statePath: filepath.Join(t.TempDir(), "state.json"),
		ownHandle: "",
		client:    &fakeInboundClient{},
		interval:  time.Second,
		now:       clock,
	}
}

// TestIsReplayJudgementLiveness pins the rule the bridge owns (#206): a row
// surfacing now that predates the last completed scan is backlog the watcher was
// blind to; one sent afterwards is live and must be answered.
func TestIsReplayJudgementLiveness(t *testing.T) {
	scanAt := time.Now()
	before := newMessage{RowID: 1, Date: scanAt.Add(-4 * time.Hour).Sub(epoch).Nanoseconds()}
	after := newMessage{RowID: 2, Date: scanAt.Add(time.Minute).Sub(epoch).Nanoseconds()}

	cases := []struct {
		name       string
		lastScanAt time.Time
		msg        newMessage
		want       bool
	}{
		{"predates last scan", scanAt, before, true},
		{"sent after last scan", scanAt, after, false},
		{"no scan yet", time.Time{}, before, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &Watcher{lastScanAt: tc.lastScanAt}
			if got := w.isReplay(tc.msg); got != tc.want {
				t.Errorf("isReplay = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWatcherFlagsCatchUpAsReplay proves the end-to-end path: after a restart the
// bridge re-delivers a backlog and marks it Replay, so clark keeps it as history
// without answering it.
func TestWatcherFlagsCatchUpAsReplay(t *testing.T) {
	db := openSynthDB(t)
	now := time.Now()
	// Arrived while the bridge was down: four hours before the restart.
	addMessage(t, db, "+6281267858909", "sent while you were down", false, false,
		map[string]any{"sent_at": now.Add(-4 * time.Hour)})

	w := newWatcherAt(t, db, fixedClock(now))
	if err := w.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// Run() is what stamps lastScanAt; set the post-bootstrap state directly so
	// the test does not have to wait a tick.
	w.lastScanAt = w.clock()
	w.lastRowID = 0

	w.scanOnce(context.Background())

	posts := w.client.(*fakeInboundClient).posts()
	if len(posts) != 1 {
		t.Fatalf("posted %d messages, want 1", len(posts))
	}
	if !posts[0].Replay {
		t.Error("Replay = false, want true for a message predating the last scan")
	}
}

// TestWatcherFlagsLiveMessageNotReplay is the other half: a message that arrives
// after the watcher is running is live, so ordinary conversation is answered.
func TestWatcherFlagsLiveMessageNotReplay(t *testing.T) {
	db := openSynthDB(t)
	scanAt := time.Now().Add(-time.Minute)
	// Sent one minute after the last scan — i.e. during live operation.
	addMessage(t, db, "+6281267858909", "are you there", false, false,
		map[string]any{"sent_at": time.Now()})

	w := newWatcherAt(t, db, fixedClock(scanAt))
	if err := w.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	w.lastScanAt = scanAt
	w.lastRowID = 0

	w.scanOnce(context.Background())

	posts := w.client.(*fakeInboundClient).posts()
	if len(posts) != 1 {
		t.Fatalf("posted %d messages, want 1", len(posts))
	}
	if posts[0].Replay {
		t.Error("Replay = true, want false for a message sent during live operation")
	}
}

// TestWatcherAdvancesLastScanAfterCleanCycle proves the blind-spot marker only
// moves after a clean pass, so a cycle that bailed out early does not mark unseen
// rows as live.
func TestWatcherAdvancesLastScanAfterCleanCycle(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "first", false, false, nil)

	now := time.Now()
	w := newWatcherAt(t, db, fixedClock(now))
	if err := w.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	w.lastRowID = 0
	w.lastScanAt = now

	w.scanOnce(context.Background())

	if !w.lastScanAt.Equal(now) {
		t.Errorf("lastScanAt = %v, want the clock value %v after a clean cycle", w.lastScanAt, now)
	}
}

// TestWatcherHoldsLastScanOnFailedForward proves a failed POST does not advance
// the marker: those rows were not delivered, so they must stay backlog on retry
// rather than being presented as live conversation.
func TestWatcherHoldsLastScanOnFailedForward(t *testing.T) {
	db := openSynthDB(t)
	now := time.Now()
	addMessage(t, db, "+6281267858909", "unreachable", false, false,
		map[string]any{"sent_at": now.Add(-time.Hour)})

	fake := &fakeInboundClient{failNext: true}
	w := &Watcher{
		db:        db,
		statePath: filepath.Join(t.TempDir(), "state.json"),
		client:    fake,
		interval:  time.Second,
		now:       fixedClock(now),
	}
	if err := w.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	w.lastRowID = 0
	w.lastScanAt = now.Add(-2 * time.Hour)

	w.scanOnce(context.Background())

	if !w.lastScanAt.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("lastScanAt = %v, want the pre-cycle value preserved after a failed forward", w.lastScanAt)
	}
}
