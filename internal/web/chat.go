package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/ollama"
)

// chatFrame is one message from the browser to the console.
type chatFrame struct {
	Type  string `json:"type"`
	Token string `json:"token"`
	Text  string `json:"text"`
}

// handleChatWS runs the chat socket: auth first, then a serial
// auth/chat/ping -> ack/reply/error/pong loop backed by ReplyLLM.
func (s *Server) handleChatWS(w http.ResponseWriter, r *http.Request) {
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

	s.hub.add(c)
	defer s.hub.remove(c)

	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}

		var frame chatFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			s.writeFrame(ctx, c, map[string]any{"type": "error", "message": "malformed frame"})
			continue
		}

		switch frame.Type {
		case "ping":
			s.writeFrame(ctx, c, map[string]any{"type": "pong"})
		case "chat":
			s.handleChatTurn(ctx, c, frame)
		case "auth":
			s.writeFrame(ctx, c, map[string]any{"type": "auth", "ok": true})
		default:
			s.writeFrame(ctx, c, map[string]any{"type": "error", "message": "unknown message type"})
		}
	}
}

// chatAuth validates the first frame; the connection is closed on failure.
func (s *Server) chatAuth(ctx context.Context, c *websocket.Conn) bool {
	_, data, err := c.Read(ctx)
	if err != nil {
		return false
	}
	var frame chatFrame
	if err := json.Unmarshal(data, &frame); err != nil || frame.Type != "auth" {
		s.writeFrame(ctx, c, map[string]any{"type": "error", "message": "authentication required"})
		_ = c.Close(websocket.StatusPolicyViolation, "authentication required")
		return false
	}
	if !s.authToken(frame.Token) {
		s.writeFrame(ctx, c, map[string]any{"type": "auth", "ok": false, "message": "unauthorized"})
		_ = c.Close(websocket.StatusPolicyViolation, "unauthorized")
		return false
	}
	s.writeFrame(ctx, c, map[string]any{"type": "auth", "ok": true})
	return true
}

// handleChatTurn runs one full-AI turn for the web session and streams the
// outcome back. Tokens are delivered in real-time from Ollama via streaming.
// A final "done" frame signals completion.
func (s *Server) handleChatTurn(ctx context.Context, c *websocket.Conn, frame chatFrame) {
	if frame.Text == "" {
		s.writeFrame(ctx, c, map[string]any{"type": "error", "message": "empty message"})
		return
	}
	s.writeFrame(ctx, c, map[string]any{"type": "ack"})
	turnCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	reply, thinking, err := s.butler.ReplyLLMStream(turnCtx, webJID, frame.Text, true, func(token string) {
		s.writeFrame(ctx, c, map[string]any{"type": "token", "text": token})
	})
	if err != nil {
		if errors.Is(err, ollama.ErrRateLimited) {
			s.writeFrame(ctx, c, map[string]any{"type": "error", "message": "I'm a bit swamped. Try again in a minute or two."})
			return
		}
		logging.Log("WEB", logging.SevWarn, "CHAT", "Chat turn failed", "error", err.Error())
		s.writeFrame(ctx, c, map[string]any{"type": "error", "message": "something went wrong"})
		return
	}

	if thinking != "" {
		s.writeFrame(ctx, c, map[string]any{"type": "thinking", "text": thinking})
	}

	s.writeFrame(ctx, c, map[string]any{"type": "reply", "text": reply})
	s.writeFrame(ctx, c, map[string]any{"type": "done"})
}

func (s *Server) writeFrame(ctx context.Context, c *websocket.Conn, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		logging.Log("WEB", logging.SevDebug, "WS", "Frame write failed", "error", err.Error())
	}
}
