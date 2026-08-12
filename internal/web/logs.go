package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/heyimteee/clark/internal/logging"
)

// handleLogsWS streams the clark log line fan-out. After auth the client gets
// a replay of the ring buffer, then live lines as they are emitted.
func (s *Server) handleLogsWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, acceptOptions)
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if !s.chatAuth(ctx, c) {
		return
	}

	// Replay the recent ring, then live-feed. Lines logged in between replay
	// and subscribe are intentionally skipped (fresh reconnects see recent state).
	s.writeFrame(ctx, c, map[string]any{"type": "replay", "lines": logging.Recent(200)})

	sub, unsub := logging.Subscribe()
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The reader owns the socket's read side; a closed client surfaces as
		// a read error here, which lets the writer below exit.
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case line, ok := <-sub:
			if !ok {
				return
			}
			data, err := json.Marshal(map[string]any{"type": "log", "line": line})
			if err != nil {
				continue
			}
			writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
			if err := c.Write(writeCtx, websocket.MessageText, data); err != nil {
				writeCancel()
				return
			}
			writeCancel()
		}
	}
}
