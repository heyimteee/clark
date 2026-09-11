// Package llmcompat speaks the OpenAI Responses API (as served by OpenCode
// Go) behind the assistant LLM interface, so Clark can run on a hosted
// frontier model while local Ollama remains the default backend.
//
// Wire notes (verified against the live gateway):
//   - Every request carries Authorization (the Go key), a product
//     User-Agent (Go-http-client is rejected by policy), and a stable
//     x-opencode-session header used for routing and prompt caching. The
//     session id is random per process: Clark multiplexes many
//     conversations through one client, so per-conversation ids are not
//     expressible here — stability still satisfies the gateway.
//   - Clark owns history: store:false is sent explicitly, full input each
//     turn. Tool outputs (role:"tool") carry no call id upstream, so they
//     bind to pending function calls positionally, in order.
//   - Reasoning summaries are opaque/encrypted unless requested; SetThink
//     maps to reasoning {effort, summary:auto} and anything returned lands
//     in Thinking. Unknown stream event types are ignored.
package llmcompat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/ollama"
)

// BackendName is the LLM_BACKEND value selecting this client.
const BackendName = "opencode-go"

const (
	requestTimeout = 5 * time.Minute
	userAgent      = "clark/1.0"
	maxErrBody     = 2048
)

// Client talks to one Responses endpoint with a fixed model.
type Client struct {
	endpoint string
	apiKey   string
	model    string
	session  string
	http     *http.Client
	think    bool
}

// New returns a Client for the given Responses endpoint URL, API key, and
// model. The session id is generated once per process (see package notes).
func New(endpoint, apiKey, model string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   apiKey,
		model:    model,
		session:  newSessionID(),
		http:     &http.Client{Timeout: requestTimeout},
	}
}

// SetThink enables or disables reasoning summaries for subsequent chats.
func (c *Client) SetThink(on bool) { c.think = on }

func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("clark-%d", time.Now().UnixNano())
	}
	return "clark-" + hex.EncodeToString(b)
}

type reasoningOpt struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responseRequest struct {
	Model     string        `json:"model"`
	Input     []any         `json:"input"`
	Stream    bool          `json:"stream,omitempty"`
	Tools     []any         `json:"tools,omitempty"`
	Reasoning *reasoningOpt `json:"reasoning,omitempty"`
	Store     bool          `json:"store"`
}

func (c *Client) reasoning() *reasoningOpt {
	if !c.think {
		return nil
	}
	return &reasoningOpt{Effort: "medium", Summary: "auto"}
}

// inputItems translates Clark messages to Responses input items. Tool outputs
// bind to pending function calls positionally (see package notes).
func inputItems(messages []ollama.Message) []any {
	items := make([]any, 0, len(messages))
	pending := []string{}
	callSeq := 0
	newCallID := func() string {
		callSeq++
		return fmt.Sprintf("call_%d", callSeq)
	}
	for _, m := range messages {
		switch m.Role {
		case "tool":
			callID := ""
			if len(pending) > 0 {
				callID, pending = pending[0], pending[1:]
			} else {
				callID = newCallID()
			}
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  m.Content,
			})
			continue
		}
		if len(m.ToolCalls) > 0 {
			if m.Content != "" {
				items = append(items, textItem(m.Role, m.Content))
			}
			for _, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					id = newCallID()
				}
				pending = append(pending, id)
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   id,
					"name":      tc.Function.Name,
					"arguments": string(tc.Function.Arguments),
				})
			}
			continue
		}
		if m.Content == "" && len(m.Images) == 0 {
			continue
		}
		items = append(items, textItem(m.Role, m.Content))
		for _, img := range m.Images {
			items = append(items, map[string]any{
				"type":      "input_image",
				"image_url": "data:image/jpeg;base64," + img,
			})
		}
	}
	return items
}

// textItem renders one text message. Assistant turns use the output_text part
// type the Responses API expects on assistant input items.
func textItem(role, text string) any {
	part := "input_text"
	if role == "assistant" {
		part = "output_text"
	}
	return map[string]any{
		"role":    role,
		"content": []any{map[string]any{"type": part, "text": text}},
	}
}

// wireTools translates Clark tools to Responses function tools.
func wireTools(tools []ollama.Tool) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		params := t.Function.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Function.Name,
			"description": t.Function.Description,
			"parameters":  params,
		})
	}
	return out
}

type responseEnvelope struct {
	Status string         `json:"status"`
	Error  *responseError `json:"error"`
	Output []responseItem `json:"output"`
}

type responseError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type responseItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

// assemble folds a completed response envelope into a ChatResult.
func assemble(env *responseEnvelope) (*ollama.ChatResult, error) {
	if env == nil {
		return nil, fmt.Errorf("empty response envelope")
	}
	if env.Status != "" && env.Status != "completed" {
		msg := "response " + env.Status
		if env.Error != nil && env.Error.Message != "" {
			msg += ": " + env.Error.Message
		}
		return nil, fmt.Errorf("%s", msg)
	}
	res := &ollama.ChatResult{}
	for _, item := range env.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					res.Content += part.Text
				}
			}
		case "function_call":
			args := item.Arguments
			if args == "" {
				args = "{}"
			}
			res.ToolCalls = append(res.ToolCalls, ollama.ToolCall{
				ID: item.CallID,
				Function: ollama.ToolCallFunc{
					Name:      item.Name,
					Arguments: json.RawMessage(args),
				},
			})
		case "reasoning":
			for _, part := range item.Summary {
				if part.Type == "summary_text" {
					res.Thinking += part.Text
				}
			}
		}
	}
	if res.Content == "" && len(res.ToolCalls) == 0 {
		return nil, fmt.Errorf("empty response from model")
	}
	return res, nil
}

// do posts one request and maps transport-level failures, preserving the
// rate-limit sentinel the assistant fails over on.
func (c *Client) do(ctx context.Context, body responseRequest) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("x-opencode-session", c.session)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to reach model at %s: %w", c.endpoint, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ollama.ErrRateLimited, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode == http.StatusUnauthorized {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		resp.Body.Close()
		return nil, fmt.Errorf("model unauthorized: check LLM_API_KEY (%s)", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		resp.Body.Close()
		return nil, fmt.Errorf("model returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// Chat sends the messages and returns the model's reply, including any tool calls.
func (c *Client) Chat(ctx context.Context, messages []ollama.Message, tools []ollama.Tool) (*ollama.ChatResult, error) {
	resp, err := c.do(ctx, responseRequest{
		Model:     c.model,
		Input:     inputItems(messages),
		Tools:     wireTools(tools),
		Reasoning: c.reasoning(),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var env responseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return assemble(&env)
}

// streamEvent is one SSE data payload.
type streamEvent struct {
	Type     string            `json:"type"`
	Delta    string            `json:"delta"`
	ItemID   string            `json:"item_id"`
	Item     *responseItem     `json:"item"`
	Response *responseEnvelope `json:"response"`
}

// ChatStream sends the messages and streams text back via fn as it arrives.
// Function-call arguments buffer until response.completed, whose embedded
// envelope is authoritative; an EOF without it falls back to the buffers.
func (c *Client) ChatStream(ctx context.Context, messages []ollama.Message, tools []ollama.Tool, fn func(token string)) (*ollama.ChatResult, error) {
	resp, err := c.do(ctx, responseRequest{
		Model:     c.model,
		Input:     inputItems(messages),
		Stream:    true,
		Tools:     wireTools(tools),
		Reasoning: c.reasoning(),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var (
		content   strings.Builder
		thinking  strings.Builder
		argBufs   = map[string]*strings.Builder{}
		argOrder  []string
		callMeta  = map[string]*responseItem{}
		final     *responseEnvelope
		streamErr error
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if line == "data: [DONE]" {
			break
		}
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			content.WriteString(ev.Delta)
			if fn != nil && ev.Delta != "" {
				fn(ev.Delta)
			}
		case "response.reasoning_summary_text.delta":
			thinking.WriteString(ev.Delta)
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				id := ev.Item.CallID
				if id == "" {
					id = ev.ItemID
				}
				cp := *ev.Item
				callMeta[id] = &cp
				if _, ok := argBufs[id]; !ok {
					argBufs[id] = &strings.Builder{}
					argOrder = append(argOrder, id)
				}
			}
		case "response.function_call_arguments.delta":
			id := ev.ItemID
			b, ok := argBufs[id]
			if !ok {
				b = &strings.Builder{}
				argBufs[id] = b
				argOrder = append(argOrder, id)
			}
			b.WriteString(ev.Delta)
		case "response.completed":
			final = ev.Response
		case "response.failed":
			msg := "response failed"
			if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
				msg += ": " + ev.Response.Error.Message
			}
			streamErr = fmt.Errorf("%s", msg)
		case "error":
			streamErr = fmt.Errorf("model stream error: %s", ev.Delta)
		}
		if streamErr != nil {
			return nil, streamErr
		}
		if final != nil {
			return assemble(final)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read stream: %w", err)
	}
	if final != nil {
		return assemble(final)
	}
	// EOF without a completed envelope: assemble from buffers.
	res := &ollama.ChatResult{Content: content.String(), Thinking: thinking.String()}
	for _, id := range argOrder {
		meta := callMeta[id]
		name := ""
		callID := id
		if meta != nil {
			name = meta.Name
			if meta.CallID != "" {
				callID = meta.CallID
			}
		}
		args := argBufs[id].String()
		if args == "" && meta != nil {
			args = meta.Arguments
		}
		if args == "" {
			args = "{}"
		}
		res.ToolCalls = append(res.ToolCalls, ollama.ToolCall{
			ID:       callID,
			Function: ollama.ToolCallFunc{Name: name, Arguments: json.RawMessage(args)},
		})
	}
	if res.Content == "" && len(res.ToolCalls) == 0 {
		return nil, fmt.Errorf("empty response from model")
	}
	return res, nil
}
