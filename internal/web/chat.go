package web

import (
	"context"
	"encoding/base64"
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
		switch typ {
		case websocket.MessageBinary:
			s.handleBinaryFrame(ctx, c, data)
		case websocket.MessageText:
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
}

// handleBinaryFrame processes binary WebSocket frames for STT/TTS.
// Protocol: first byte is type, rest is payload.
//
//	0x01 = STT request (WAV bytes) -> responds with 0x02 (UTF-8 transcript)
//	0x03 = TTS request (UTF-8 text) -> responds with 0x04 (WAV bytes)
func (s *Server) handleBinaryFrame(ctx context.Context, c *websocket.Conn, data []byte) {
	if len(data) < 1 {
		return
	}
	frameType := data[0]
	payload := data[1:]

	switch frameType {
	case 0x01: // STT request
		if s.voice == nil || s.voice.STT == nil {
			s.writeBinaryFrame(ctx, c, 0x02, []byte("STT unavailable"))
			return
		}
		audio, err := base64Decode(payload)
		if err != nil {
			s.writeBinaryFrame(ctx, c, 0x02, []byte("invalid audio"))
			return
		}
		sttCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		text, err := s.voice.STT.Transcribe(sttCtx, audio)
		if err != nil {
			s.writeBinaryFrame(ctx, c, 0x02, []byte("transcription failed"))
			return
		}
		s.writeBinaryFrame(ctx, c, 0x02, []byte(text))

	case 0x03: // TTS request
		if s.voice == nil || s.voice.TTS == nil {
			s.writeBinaryFrame(ctx, c, 0x04, nil)
			return
		}
		text := string(payload)
		if len(text) > maxTTSTextChars {
			text = text[:maxTTSTextChars]
		}
		ttsCtx, cancel := context.WithTimeout(ctx, ttsSynthTimeout)
		defer cancel()
		wav, err := s.voice.TTS.Synthesize(ttsCtx, stripForSpeech(text))
		if err != nil {
			s.writeBinaryFrame(ctx, c, 0x04, nil)
			return
		}
		s.writeBinaryFrame(ctx, c, 0x04, wav)
	}
}

func (s *Server) writeBinaryFrame(ctx context.Context, c *websocket.Conn, frameType byte, payload []byte) {
	data := make([]byte, 1+len(payload))
	data[0] = frameType
	copy(data[1:], payload)
	if err := c.Write(ctx, websocket.MessageBinary, data); err != nil {
		logging.Log("WEB", logging.SevDebug, "WS", "Binary frame write failed", "error", err.Error())
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

func base64Decode(data []byte) ([]byte, error) {
	return base64.StdEncoding.DecodeString(string(data))
}
