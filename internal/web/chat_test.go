package web

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/heyimteee/clark/internal/ollama"
)

func TestChatAuthThenReply(t *testing.T) {
	ts, _, llm, _ := newTestServer(t)
	tok := login(t, ts)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})

	frame := wsReadJSON(t, c)
	if frame["type"] != "auth" || frame["ok"] != true {
		t.Fatalf("auth frame = %v, want auth ok", frame)
	}

	wsWriteJSON(t, c, map[string]any{"type": "chat", "text": "list of tools"})
	ack := wsReadJSON(t, c)
	if ack["type"] != "ack" {
		t.Fatalf("ack frame = %v, want ack", ack)
	}
	reply := wsReadJSON(t, c)
	if reply["type"] != "reply" {
		t.Fatalf("reply frame = %v, want reply", reply)
	}
	text, _ := reply["text"].(string)
	if text == "" {
		t.Errorf("reply text empty: %v", reply)
	}
	if len(llm.got) == 0 {
		t.Error("chat did not reach the model (fast path would have answered hardcoded)")
	}
}

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

func TestChatRequiresAuthFirst(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "chat", "text": "hi"})

	frame := wsReadJSON(t, c)
	if frame["type"] != "error" {
		t.Fatalf("first frame = %v, want error (auth required)", frame)
	}
	if msg, _ := frame["message"].(string); msg == "" {
		t.Error("auth-required error frame has no message")
	}
}

func TestChatPingPong(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})
	wsReadJSON(t, c) // auth ok

	wsWriteJSON(t, c, map[string]any{"type": "ping"})
	frame := wsReadJSON(t, c)
	if frame["type"] != "pong" {
		t.Fatalf("frame = %v, want pong", frame)
	}
}

func TestChatSurfacesRateLimit(t *testing.T) {
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

func TestChatSerializesPerConnection(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	c := wsDial(t, ts, "/web/api/chat")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})
	wsReadJSON(t, c)

	// A burst of chat frames must each get exactly one ack + one reply.
	for i := 0; i < 3; i++ {
		wsWriteJSON(t, c, map[string]any{"type": "chat", "text": "hello"})
	}
	for i := 0; i < 3; i++ {
		if ack := wsReadJSON(t, c); ack["type"] != "ack" {
			t.Fatalf("burst ack %d = %v, want ack", i, ack)
		}
		if reply := wsReadJSON(t, c); reply["type"] != "reply" {
			t.Fatalf("burst reply %d = %v, want reply", i, reply)
		}
	}
}

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
