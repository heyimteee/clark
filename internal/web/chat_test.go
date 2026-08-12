package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/heyimteee/clark/internal/ollama"
)

// wsReadReply reads the ack then collects token+thinking+done frames and
// returns the full text and any thinking string.
func wsReadReply(t *testing.T, c *websocket.Conn) (text, thinking string) {
	t.Helper()
	// Read ack
	ack := wsReadJSON(t, c)
	if ack["type"] != "ack" {
		t.Fatalf("expected ack, got %v", ack)
	}
	// Read streaming frames until done.
	var parts []string
	var thinkParts []string
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for done frame")
		default:
		}
		f := wsReadJSON(t, c)
		switch f["type"] {
		case "thinking":
			thinkParts = append(thinkParts, f["text"].(string))
		case "token":
			parts = append(parts, f["text"].(string))
		case "done":
			return strings.Join(parts, ""), strings.Join(thinkParts, "")
		case "error":
			t.Fatalf("error frame: %v", f)
		default:
			t.Fatalf("unexpected frame type: %v", f["type"])
		}
	}
}

// fastPathPhrase is a deterministic view command the fast path would answer
// hardcoded instead of asking the model.
const fastPathPhrase = "list of tools"

// TestChatDeliversFullAIReply ensures every chat turn reaches the model and
// the reply arrives as streaming token frames + done.
func TestChatDeliversFullAIReply(t *testing.T) {
	ts, _, llm, _ := newTestServer(t)
	tok := login(t, ts)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})
	auth := wsReadJSON(t, c)
	if auth["type"] != "auth" || auth["ok"] != true {
		t.Fatalf("auth frame = %v, want auth ok", auth)
	}

	wsWriteJSON(t, c, map[string]any{"type": "chat", "text": "list of tools"})
	text, _ := wsReadReply(t, c)
	if text == "" {
		t.Error("streaming reply text is empty")
	}
	if len(llm.got) == 0 {
		t.Error("chat did not reach the model (fast path would have answered hardcoded)")
	}
}

// TestChatRejectsBadAuth ensures a wrong token closes the connection.
func TestChatRejectsBadAuth(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": "nope"})

	frame := wsReadJSON(t, c)
	if frame["type"] != "auth" || frame["ok"] != false {
		t.Fatalf("auth frame = %v, want auth ok=false", frame)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil {
		t.Error("connection stayed open after rejected auth")
	}
}

// TestChatRequiresAuthFirst ensures chat frames before auth get an error.
func TestChatRequiresAuthFirst(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "chat", "text": "hi"})

	frame := wsReadJSON(t, c)
	if frame["type"] != "error" {
		t.Fatalf("first frame = %v, want error (auth required)", frame)
	}
	if msg, _ := frame["message"].(string); msg == "" {
		t.Error("error frame has no message")
	}
}

// TestChatPongRespondsToPing ensures ping frames get pong replies.
func TestChatPongRespondsToPing(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})
	wsReadJSON(t, c)

	wsWriteJSON(t, c, map[string]any{"type": "ping"})
	pong := wsReadJSON(t, c)
	if pong["type"] != "pong" {
		t.Fatalf("pong frame = %v, want pong", pong)
	}
}

// TestChatRateLimitPropagates ensures model rate-limit errors surface.
func TestChatRateLimitPropagates(t *testing.T) {
	ts, _, llm, _ := newTestServer(t)
	tok := login(t, ts)
	llm.err = ollama.ErrRateLimited

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})
	wsReadJSON(t, c)

	wsWriteJSON(t, c, map[string]any{"type": "chat", "text": "hello"})
	wsReadJSON(t, c) // ack
	frame := wsReadJSON(t, c)
	if frame["type"] != "error" {
		t.Fatalf("frame = %v, want error", frame)
	}
	if msg, _ := frame["message"].(string); msg == "" {
		t.Error("rate-limit error frame has no message")
	}
}

// TestChatSerializesPerConnection ensures a burst of messages is serial.
func TestChatSerializesPerConnection(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})
	wsReadJSON(t, c)

	// A burst of chat frames must each get exactly one reply (token + done).
	for i := 0; i < 3; i++ {
		wsWriteJSON(t, c, map[string]any{"type": "chat", "text": "hello"})
	}
	for i := 0; i < 3; i++ {
		text, _ := wsReadReply(t, c)
		if text == "" {
			t.Fatalf("burst reply %d is empty", i)
		}
	}
}

// TestChatUnknownMessageType ensures unrecognized frames get an error.
func TestChatUnknownMessageType(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})
	wsReadJSON(t, c)

	wsWriteJSON(t, c, map[string]any{"type": "frobnicate"})
	frame := wsReadJSON(t, c)
	if frame["type"] != "error" {
		t.Fatalf("frame = %v, want error for unknown type", frame)
	}
}

var _ = websocket.StatusNormalClosure
