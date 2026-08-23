package gateway

import (
	"testing"
	"time"
)

func TestEchoTrackerConsumeWithinTTL(t *testing.T) {
	e := NewEchoTracker()
	now := time.Now()
	e.now = func() time.Time { return now }

	e.Mark("id1")
	if !e.Consume("id1") {
		t.Fatal("consume within TTL failed")
	}
	if e.Consume("id1") {
		t.Fatal("consume twice succeeded")
	}
}

func TestEchoTrackerSweepsExpired(t *testing.T) {
	e := NewEchoTracker()
	now := time.Now()
	e.now = func() time.Time { return now }

	e.Mark("stale")
	now = now.Add(echoTTL + time.Minute)

	// A later Mark sweeps the expired entry.
	e.Mark("fresh")
	if e.Consume("stale") {
		t.Fatal("expired echo still consumed, want swept")
	}
	if !e.Consume("fresh") {
		t.Fatal("fresh echo missing")
	}
}

func TestEchoTrackerCapEvictsOldest(t *testing.T) {
	e := NewEchoTracker()
	now := time.Now()
	e.now = func() time.Time { return now }

	for i := 0; i < echoCap; i++ {
		now = now.Add(time.Millisecond)
		e.Mark(string(rune('a'+i)) + string(rune('0'+i%10)))
	}
	// One more forces eviction of the oldest-seen entry.
	now = now.Add(time.Millisecond)
	first := string(rune('a')) + "0"
	e.Mark("new")

	e.mu.Lock()
	_, hasFirst := e.ids[first]
	size := len(e.ids)
	e.mu.Unlock()

	if hasFirst {
		t.Error("oldest entry survived cap eviction")
	}
	if size > echoCap {
		t.Errorf("size %d exceeds cap %d", size, echoCap)
	}
}
