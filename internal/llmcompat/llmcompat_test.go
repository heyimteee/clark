package llmcompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/ollama"
)

type seenRequest struct {
	path    string
	auth    string
	ua      string
	session string
	body    map[string]any
}

func newStub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, seen *seenRequest)) (*Client, *seenRequest) {
	t.Helper()
	seen := &seenRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.path = r.URL.Path
		seen.auth = r.Header.Get("Authorization")
		seen.ua = r.Header.Get("User-Agent")
		seen.session = r.Header.Get("x-opencode-session")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen.body = body
		handler(w, r, seen)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/v1/responses", "test-key", "test-model"), seen
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func TestChatToolCallRoundTrip(t *testing.T) {
	c, seen := newStub(t, func(w http.ResponseWriter, r *http.Request, seen *seenRequest) {
		writeJSON(t, w, map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "checking "}}},
				map[string]any{"type": "function_call", "call_id": "call_1", "name": "get_time", "arguments": "{}"},
			},
		})
	})
	res, err := c.Chat(t.Context(), []ollama.Message{{Role: "user", Content: "hi"}}, []ollama.Tool{
		{Type: "function", Function: ollama.ToolFunction{Name: "get_time", Description: "time", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Content != "checking " {
		t.Fatalf("content = %q", res.Content)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "call_1" || res.ToolCalls[0].Function.Name != "get_time" {
		t.Fatalf("calls = %+v", res.ToolCalls)
	}
	if seen.auth != "Bearer test-key" {
		t.Fatalf("auth = %q", seen.auth)
	}
	if seen.ua == "" || strings.Contains(seen.ua, "Go-http-client") {
		t.Fatalf("user-agent = %q, want product token", seen.ua)
	}
	if seen.session == "" {
		t.Fatal("x-opencode-session header missing")
	}
	if bodyModel, _ := seen.body["model"].(string); bodyModel != "test-model" {
		t.Fatalf("model = %v", seen.body["model"])
	}
	if _, ok := seen.body["store"]; !ok {
		t.Fatal("store flag should be explicit")
	}
}

func TestChatHistoryAndReasoning(t *testing.T) {
	c, seen := newStub(t, func(w http.ResponseWriter, r *http.Request, seen *seenRequest) {
		writeJSON(t, w, map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "thoughts"}}},
				map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "done"}}},
			},
		})
	})
	c.SetThink(true)
	msgs := []ollama.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do it"},
		{Role: "assistant", Content: "", ToolCalls: []ollama.ToolCall{
			{ID: "call_9", Function: ollama.ToolCallFunc{Name: "run", Arguments: json.RawMessage(`{"x":1}`)}},
		}},
		{Role: "tool", Content: "ok"},
		{Role: "user", Content: "again"},
	}
	res, err := c.Chat(t.Context(), msgs, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Thinking != "thoughts" || res.Content != "done" {
		t.Fatalf("res = %+v", res)
	}
	input, _ := seen.body["input"].([]any)
	if len(input) != len(msgs) {
		t.Fatalf("input items = %d, want %d: %v", len(input), len(msgs), seen.body["input"])
	}
	// Positional binding: the tool output follows its function call.
	callItem, _ := input[2].(map[string]any)
	outItem, _ := input[3].(map[string]any)
	if callItem["type"] != "function_call" || callItem["call_id"] != "call_9" {
		t.Fatalf("call item = %v", callItem)
	}
	if outItem["type"] != "function_call_output" || outItem["call_id"] != "call_9" || outItem["output"] != "ok" {
		t.Fatalf("output item = %v", outItem)
	}
	if _, ok := seen.body["reasoning"]; !ok {
		t.Fatal("reasoning should be set when think is on")
	}
}

func TestChatStreamAssembly(t *testing.T) {
	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request, seen *seenRequest) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `: ping`)
		fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"hel"}`)
		fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"lo"}`)
		fmt.Fprintln(w, `data: {"type":"response.output_item.added","item":{"id":"it_1","type":"function_call","name":"ping","call_id":"call_7"}}`)
		fmt.Fprintln(w, `data: {"type":"response.function_call_arguments.delta","item_id":"call_7","delta":"{\\"a\\":"}`)
		fmt.Fprintln(w, `data: {"type":"response.function_call_arguments.delta","item_id":"call_7","delta":"1}"}`)
		fmt.Fprintln(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","call_id":"call_7","name":"ping","arguments":"{\"a\":1}"}]}}`)
		fmt.Fprintln(w, `data: [DONE]`)
	})
	var tokens []string
	res, err := c.ChatStream(t.Context(), []ollama.Message{{Role: "user", Content: "hi"}}, nil, func(tok string) {
		tokens = append(tokens, tok)
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if strings.Join(tokens, "") != "hello" {
		t.Fatalf("tokens = %q", tokens)
	}
	if res.Content != "hello" {
		t.Fatalf("content = %q", res.Content)
	}
	if len(res.ToolCalls) != 1 || string(res.ToolCalls[0].Function.Arguments) != `{"a":1}` {
		t.Fatalf("calls = %+v", res.ToolCalls)
	}
}

func TestChatStreamFallbackWithoutCompleted(t *testing.T) {
	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request, seen *seenRequest) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"type":"response.output_item.added","item":{"id":"it_1","type":"function_call","name":"ping","call_id":"call_7"}}`)
		fmt.Fprintln(w, `data: {"type":"response.function_call_arguments.delta","item_id":"call_7","delta":"{}"}`)
		// EOF without completed/DONE: buffers still assemble.
	})
	res, err := c.ChatStream(t.Context(), []ollama.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "call_7" {
		t.Fatalf("calls = %+v", res.ToolCalls)
	}
}

func TestChatErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"rate limited", 429, "slow down", func(err error) bool {
			return errors.Is(err, ollama.ErrRateLimited)
		}},
		{"unauthorized", 401, "bad key", func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "LLM_API_KEY")
		}},
		{"server error", 500, "boom", func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "500")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request, seen *seenRequest) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := c.Chat(t.Context(), []ollama.Message{{Role: "user", Content: "hi"}}, nil)
			if !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestChatRejectsFailedStatus(t *testing.T) {
	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request, seen *seenRequest) {
		writeJSON(t, w, map[string]any{"status": "failed", "error": map[string]any{"message": "nope"}})
	})
	_, err := c.Chat(t.Context(), []ollama.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestChatEmptyResponse(t *testing.T) {
	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request, seen *seenRequest) {
		writeJSON(t, w, map[string]any{"status": "completed", "output": []any{}})
	})
	_, err := c.Chat(t.Context(), []ollama.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("want error for empty response")
	}
}
