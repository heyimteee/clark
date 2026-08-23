package gateway

import (
	"testing"
	"time"

	"runtime"
)

// TestDispatcherRetiresIdleWorkers guards the per-sender goroutine leak:
// after the idle timeout with an empty queue, the worker must exit and its
// queue entry must be removed, returning the process to baseline goroutines.
func TestDispatcherRetiresIdleWorkers(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true}
	h := newTestHandler(msgr, butler)

	h.disp.idle = 100 * time.Millisecond

	runtime.GC()
	base := runtime.NumGoroutine()

	h.Handle(testMsg(testVIP, "hello there"))
	waitForReplyCount(t, butler, 1)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > base {
		t.Fatalf("goroutines %d > baseline %d: idle worker never retired", after, base)
	}

	h.disp.mu.Lock()
	_, exists := h.disp.queues[testVIP]
	h.disp.mu.Unlock()
	if exists {
		t.Fatal("queue entry not deleted after worker retirement")
	}
}

// TestDispatcherRetiredQueueRecreates verifies a sender whose worker retired
// can immediately message again (fresh queue + worker).
func TestDispatcherRetiredQueueRecreates(t *testing.T) {
	msgr := &fakeMessenger{}
	butler := &fakeButler{enabled: true}
	h := newTestHandler(msgr, butler)

	now := time.Now()
	h.disp.idle = 50 * time.Millisecond
	clock := func() time.Time { return now }
	h.clock = clock
	h.disp.now = clock

	h.Handle(testMsg(testVIP, "first"))
	waitForReplyCount(t, butler, 1)
	time.Sleep(120 * time.Millisecond) // let worker retire

	h.Handle(testMsg(testVIP, "second"))
	waitForReplyCount(t, butler, 2)
	h.Close()
}

func waitForReplyCount(t *testing.T, b *fakeButler, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(b.replied)
		b.mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("reply count never reached %d", want)
}

// silence unused import when ollama referenced only via other files
