package web

import (
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestFanOutNotBlockedBySlowWriter proves per-client concurrency in the hub:
// one writer parking past its deadline must not extend the wall-clock of the
// other deliveries beyond its own timeout slice.
func TestFanOutNotBlockedBySlowWriter(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	slow := &websocket.Conn{}
	fast := &websocket.Conn{}
	clients := []*websocket.Conn{slow, fast}

	var (
		start       time.Time
		fastDone    = make(chan struct{})
		fastLatency time.Duration
	)
	writes := map[*websocket.Conn]func() error{
		slow: func() error {
			<-release // park well past the per-write deadline
			return nil
		},
		fast: func() error {
			fastLatency = time.Since(start)
			close(fastDone)
			return nil
		},
	}
	write := func(c *websocket.Conn, _ []byte) error { return writes[c]() }

	h := newChatHub()
	start = time.Now()
	fanDone := make(chan struct{})
	go func() {
		h.fanOut(clients, []byte("payload"), write, 300*time.Millisecond)
		close(fanDone)
	}()

	select {
	case <-fastDone:
		if fastLatency >= 150*time.Millisecond {
			t.Fatalf("fast writer delayed %v by the parked writer", fastLatency)
		}
	case <-time.After(time.Second):
		t.Fatal("fast writer never invoked")
	}

	// fanOut itself outlives the parked writer up to its deadline.
	select {
	case <-fanDone:
	case <-time.After(time.Second):
		t.Fatal("fanOut never returned")
	}
}
