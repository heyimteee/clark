package whatsapp

import "sync"

// EchoTracker tracks outbound message IDs so their delivery echoes can be ignored.
type EchoTracker struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

// NewEchoTracker returns an empty tracker.
func NewEchoTracker() *EchoTracker {
	return &EchoTracker{ids: make(map[string]struct{})}
}

// Mark records an outbound message ID.
func (e *EchoTracker) Mark(id string) {
	if id == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ids[id] = struct{}{}
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
	}
	return ok
}
