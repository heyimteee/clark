package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/config"
)

// wakeFromOff drives a genuine OFF -> ON edge. newService seeds status=true
// straight into the store, bypassing SetStatus, so without this the first call
// would be a redundant wake and the watermark would (correctly) not be written.
func wakeFromOff(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.SetStatus(false); err != nil {
		t.Fatalf("SetStatus(false): %v", err)
	}
	if err := svc.SetStatus(true); err != nil {
		t.Fatalf("SetStatus(true): %v", err)
	}
}

// TestStatusSinceAbsentByDefault proves a fresh install has no watermark, so
// nothing is treated as backlog until a status transition is actually recorded.
func TestStatusSinceAbsentByDefault(t *testing.T) {
	svc, _, _ := newService(t)
	if got := svc.StatusSince(); !got.IsZero() {
		t.Errorf("StatusSince = %v, want zero before any status transition", got)
	}
}

// TestStatusSinceRecordedOnWake proves switching ON stamps the boundary, which is
// what makes messages sent while clark was off history-only.
func TestStatusSinceRecordedOnWake(t *testing.T) {
	svc, _, _ := newService(t)
	before := time.Now().Add(-time.Second)

	wakeFromOff(t, svc)

	got := svc.StatusSince()
	if got.IsZero() {
		t.Fatal("StatusSince is zero after an OFF -> ON edge; want a boundary")
	}
	if got.Before(before) {
		t.Errorf("StatusSince = %v, want a timestamp at or after %v", got, before)
	}
}

// TestStatusSinceNotMovedByRedundantWake is the important one: re-issuing "wake up
// buddy" must NOT move the boundary, or it would silently swallow every message
// sent while clark was already awake.
func TestStatusSinceNotMovedByRedundantWake(t *testing.T) {
	svc, _, _ := newService(t)
	wakeFromOff(t, svc)
	first := svc.StatusSince()
	if first.IsZero() {
		t.Fatal("StatusSince is zero after the first wake")
	}

	// RFC3339 has second precision, so a later stamp is only observable across a
	// second boundary. Sleep past one to make the assertion meaningful.
	time.Sleep(1100 * time.Millisecond)
	if err := svc.SetStatus(true); err != nil {
		t.Fatalf("redundant SetStatus(true): %v", err)
	}

	if second := svc.StatusSince(); !second.Equal(first) {
		t.Errorf("StatusSince moved on a redundant wake: %v -> %v; live messages sent in between would be discarded", first, second)
	}
}

// TestStatusSinceHeldWhileOff proves switching OFF preserves the boundary and the
// next OFF -> ON edge re-stamps it.
func TestStatusSinceHeldWhileOff(t *testing.T) {
	svc, _, _ := newService(t)
	wakeFromOff(t, svc)
	first := svc.StatusSince()

	if err := svc.SetStatus(false); err != nil {
		t.Fatalf("SetStatus(false): %v", err)
	}
	if got := svc.StatusSince(); !got.Equal(first) {
		t.Errorf("StatusSince = %v after going OFF, want the value preserved (%v)", got, first)
	}

	time.Sleep(1100 * time.Millisecond)
	if err := svc.SetStatus(true); err != nil {
		t.Fatalf("second SetStatus(true): %v", err)
	}
	if second := svc.StatusSince(); !second.After(first) {
		t.Errorf("StatusSince = %v, want re-stamped later than %v on a new OFF -> ON edge", second, first)
	}
}

// TestStatusSinceIgnoresGarbage proves a corrupt setting degrades to "no
// boundary" (answer everything) rather than silently swallowing all traffic.
func TestStatusSinceIgnoresGarbage(t *testing.T) {
	svc, _, _ := newService(t)
	if err := svc.settings.Set(statusSinceKey, "not-a-timestamp"); err != nil {
		t.Fatalf("set garbage: %v", err)
	}
	if got := svc.StatusSince(); !got.IsZero() {
		t.Errorf("StatusSince = %v for an unparseable value, want zero", got)
	}
}

// TestStatusSinceSurvivesRestart proves the watermark is persisted, not cached —
// otherwise a restart would silently reset it and let backlog be answered.
func TestStatusSinceSurvivesRestart(t *testing.T) {
	svc, st, _ := newService(t)
	wakeFromOff(t, svc)
	want := svc.StatusSince()
	if want.IsZero() {
		t.Fatal("StatusSince is zero after a wake")
	}

	reloaded, err := New(&config.Config{DBPath: ":memory:", OllamaModel: "test-model"}, st, &fakeLLM{})
	if err != nil {
		t.Fatalf("reload service: %v", err)
	}
	if got := reloaded.StatusSince(); !got.Equal(want) {
		t.Errorf("StatusSince after restart = %v, want %v", got, want)
	}
}

// TestRecordWritesUserTurnOnly proves the history-only path stores the inbound
// message and no assistant reply, since nothing was answered.
func TestRecordWritesUserTurnOnly(t *testing.T) {
	svc, st, _ := newService(t)
	jid := "6281267858909@s.whatsapp.net"
	const text = "are we still on for 8pm?"

	if err := svc.Record(context.Background(), jid, text); err != nil {
		t.Fatalf("Record: %v", err)
	}

	msgs, err := st.RecentMessages(jid, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("stored %d messages, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("role = %q, want user", msgs[0].Role)
	}
	if msgs[0].Content != text {
		t.Errorf("content = %q, want the recorded text verbatim", msgs[0].Content)
	}
}
