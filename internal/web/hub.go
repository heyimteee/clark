package web

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/heyimteee/clark/internal/logging"
)

// chatHub tracks the connected web-console chat sockets so alerts and other
// server-initiated events can be pushed to every open console. Each connection
// registers itself after auth and unregisters on close.
type chatHub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
}

func newChatHub() *chatHub {
	return &chatHub{clients: make(map[*websocket.Conn]struct{})}
}

// add registers a chat socket for broadcast delivery.
func (h *chatHub) add(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

// remove drops a chat socket (idempotent).
func (h *chatHub) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// broadcast sends a frame to every connected chat socket. Each client is
// written concurrently with its own deadline, so a stalled console can never
// delay alert delivery to the others.
func (h *chatHub) broadcast(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	clients := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()

	// Fire-and-forget: callers (alert delivery, state pushes) must never wait
	// on the slowest console's write deadline.
	go h.fanOut(clients, data, func(c *websocket.Conn, data []byte) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return c.Write(ctx, websocket.MessageText, data)
	}, 5*time.Second)
}

// fanOut writes data to every client in its own goroutine; a writer exceeding
// timeout is simply abandoned (its goroutine returns when the ctx fires) and
// dropped from the hub.
func (h *chatHub) fanOut(clients []*websocket.Conn, data []byte, write func(*websocket.Conn, []byte) error, timeout time.Duration) {
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *websocket.Conn) {
			defer wg.Done()
			done := make(chan error, 1)
			go func() { done <- write(c, data) }()
			select {
			case err := <-done:
				if err != nil {
					h.remove(c)
				}
			case <-time.After(timeout):
				h.remove(c)
			}
		}(c)
	}
	wg.Wait()
}

// broadcastChatAlert pushes an alert message to every open web console, where
// the SPA renders it as a chat bubble and, when speak is true, reads it aloud.
// Silent-mode alerts pass speak=false so the console shows the alert without
// any audio (clark stays quiet during meetings/class).
func (s *Server) broadcastChatAlert(text string, speak bool) {
	if text == "" {
		return
	}
	logging.Log("WEB", logging.SevNotice, "ALERT", "Pushing alert to web consoles", "chars", len(text), "speak", speak)
	s.hub.broadcast(map[string]any{"type": "alert", "text": text, "speak": speak})
}
