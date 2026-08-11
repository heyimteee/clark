package ollama

import (
	"context"
	"encoding/json"
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
	result, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result.Content != "hello back" {
		t.Errorf("content = %q, want hello back", result.Content)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("unexpected tool calls: %+v", result.ToolCalls)
	}

	if !strings.Contains(gotBody, `"model":"test-model"`) {
		t.Errorf("request body missing model: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"stream":false`) || !strings.Contains(gotBody, `"think":false`) {
		t.Errorf("request body should disable stream and think: %s", gotBody)
	}
}

func TestChatSendsTools(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readAll(t, r.Body)
		w.Write([]byte(`{"message":{"content":"ok"}}`))
	}))
	defer server.Close()

	c := New(server.URL, "test-model")
	tools := []Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:        "web_search",
			Description: "search the web",
			Parameters:  map[string]any{"type": "object"},
		},
	}}
	if _, err := c.Chat(context.Background(), nil, tools); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(gotBody, `"tools"`) || !strings.Contains(gotBody, `"web_search"`) {
		t.Errorf("request body missing tools: %s", gotBody)
	}
}

func TestChatReturnsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"content":"","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":{"city":"London"}}}]}}`))
	}))
	defer server.Close()

	c := New(server.URL, "test-model")
	result, err := c.Chat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "get_weather" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	var args map[string]string
	if err := json.Unmarshal(tc.Function.Arguments, &args); err != nil {
		t.Fatalf("bad arguments json: %v", err)
	}
	if args["city"] != "London" {
		t.Errorf("city = %q, want London", args["city"])
	}
}

func TestChatHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := New(server.URL, "test-model")
	if _, err := c.Chat(context.Background(), nil, nil); err == nil {
		t.Fatal("Chat succeeded on HTTP 500, want error")
	}
}

func TestChatEmptyReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"content":""}}`))
	}))
	defer server.Close()

	c := New(server.URL, "test-model")
	if _, err := c.Chat(context.Background(), nil, nil); err == nil {
		t.Fatal("Chat succeeded with empty content and no tool calls, want error")
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
