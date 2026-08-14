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

// broadcast sends a frame to every connected chat socket. Slow or closed
// clients are dropped without blocking the others.
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

	for _, c := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.Write(ctx, websocket.MessageText, data)
		cancel()
		if err != nil {
			h.remove(c)
		}
	}
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
