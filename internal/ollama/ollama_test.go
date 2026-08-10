package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChat(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}
		gotBody = readAll(t, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"content":"hello back"}}`))
	}))
	defer server.Close()

	c := New(server.URL, "test-model")
	reply, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply != "hello back" {
		t.Errorf("reply = %q, want hello back", reply)
	}

	if !strings.Contains(gotBody, `"model":"test-model"`) {
		t.Errorf("request body missing model: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"stream":false`) || !strings.Contains(gotBody, `"think":false`) {
		t.Errorf("request body should disable stream and think: %s", gotBody)
	}
}

func TestChatHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := New(server.URL, "test-model")
	if _, err := c.Chat(context.Background(), nil); err == nil {
		t.Fatal("Chat succeeded on HTTP 500, want error")
	}
}

func TestChatEmptyReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"content":""}}`))
	}))
	defer server.Close()

	c := New(server.URL, "test-model")
	if _, err := c.Chat(context.Background(), nil); err == nil {
		t.Fatal("Chat succeeded with empty content, want error")
	}
}

func readAll(t *testing.T, r interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if n == 0 {
			break
		}
		if err != nil {
			break
		}
	}
	return b.String()
}
