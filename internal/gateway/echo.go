package gateway

import (
	"sync"
	"time"
)

const (
	// echoTTL bounds how long an outbound ID is remembered awaiting its
	// delivery echo. Failed or never-acknowledged sends must not accumulate
	// for the life of the process.
	echoTTL = time.Hour
	// echoCap hard-caps tracked IDs regardless of TTL sweep cadence.
	echoCap = 10_000
)

// EchoTracker tracks outbound message IDs so their delivery echoes can be
// ignored. Entries carry a last-seen timestamp and are swept lazily: a TTL
// bounds memory even when echoes never arrive.
type EchoTracker struct {
	mu       sync.Mutex
	ids      map[string]struct{}
	lastSeen map[string]time.Time
	now      func() time.Time
}

// NewEchoTracker returns an empty tracker.
func NewEchoTracker() *EchoTracker {
	return &EchoTracker{
		ids:      make(map[string]struct{}),
		lastSeen: make(map[string]time.Time),
		now:      time.Now,
	}
}

// Mark records an outbound message ID, sweeping expired entries first and
// evicting the oldest when the hard cap is exceeded.
func (e *EchoTracker) Mark(id string) {
	if id == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()

	for k, seen := range e.lastSeen {
		if now.Sub(seen) >= echoTTL {
			delete(e.ids, k)
			delete(e.lastSeen, k)
		}
	}
	for len(e.ids) >= echoCap {
		var oldest string
		var oldestAt time.Time
		first := true
		for k, seen := range e.lastSeen {
			if first || seen.Before(oldestAt) {
				oldest, oldestAt, first = k, seen, false
			}
		}
		if oldest == "" {
			break
		}
		delete(e.ids, oldest)
		delete(e.lastSeen, oldest)
	}

	e.ids[id] = struct{}{}
	e.lastSeen[id] = now
}

// Consume reports whether id was a tracked outbound message, removing it.
func (e *EchoTracker) Consume(id string) bool {
	if id == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.ids[id]
	if ok {
		delete(e.ids, id)
		delete(e.lastSeen, id)
	}
	return ok
}
