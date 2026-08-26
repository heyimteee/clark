// Package ollama provides a thin chat client for an Ollama server.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is a single chat turn.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Images    []string   `json:"images,omitempty"`
}

// ToolCall is a request to invoke a tool, as emitted by the model.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Function ToolCallFunc `json:"function"`
}

// ToolCallFunc names the tool and its JSON arguments.
type ToolCallFunc struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Tool is the wire schema that advertises a tool to the model.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a tool's name, purpose, and JSON-Schema parameters.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ChatResult is a model reply: content and any tool calls to run.
type ChatResult struct {
	Content   string
	Thinking  string
	ToolCalls []ToolCall
}

// Client talks to one Ollama endpoint with a fixed model.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
	think   bool
	temp    float64 // sampling temperature; negative means "server default"
}

// New returns a Client for the given base URL and model.
func New(baseURL, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: 5 * time.Minute},
		temp:    -1,
	}
}

// SetThink enables or disables reasoning tokens for subsequent chats.
func (c *Client) SetThink(on bool) { c.think = on }

// SetTemperature pins the sampling temperature for subsequent chats (e.g.
// 0.2 for fidelity-critical digest passes). Negative restores the default.
func (c *Client) SetTemperature(t float64) { c.temp = t }

type chatOptions struct {
	Temperature float64 `json:"temperature"`
}

type chatRequest struct {
	Model    string       `json:"model"`
	Messages []Message    `json:"messages"`
	Tools    []Tool       `json:"tools,omitempty"`
	Stream   bool         `json:"stream"`
	Think    bool         `json:"think"`
	Options  *chatOptions `json:"options,omitempty"`
}

// options renders the per-request sampler options, nil when defaulted.
func (c *Client) options() *chatOptions {
	if c.temp < 0 {
		return nil
	}
	return &chatOptions{Temperature: c.temp}
}

type chatResponse struct {
	Message struct {
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	Done bool `json:"done"`
}

// ErrRateLimited marks a model reply refused because the server is throttling
// requests (HTTP 429). Callers may use errors.Is to trigger failover behaviour.
var ErrRateLimited = errors.New("ollama rate limited")

// Chat sends the messages and returns the model's reply, including any tool calls.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResult, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
		Stream:   false,
		Think:    c.think,
		Options:  c.options(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	url := c.baseURL + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to reach Ollama at %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("%w: %s", ErrRateLimited, strings.TrimSpace(string(respBody)))
		}
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if chatResp.Message.Content == "" && len(chatResp.Message.ToolCalls) == 0 {
		return nil, fmt.Errorf("empty response from model")
	}

	return &ChatResult{
		Content:   chatResp.Message.Content,
		Thinking:  chatResp.Message.Thinking,
		ToolCalls: chatResp.Message.ToolCalls,
	}, nil
}

// ChatStream sends the messages and streams tokens back via fn as they arrive.
// Tool calls are buffered until the stream completes (Ollama sends them in the
// final chunk). Returns the full assembled reply and tool calls.
func (c *Client) ChatStream(ctx context.Context, messages []Message, tools []Tool, fn func(token string)) (*ChatResult, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
		Stream:   true,
		Think:    c.think,
		Options:  c.options(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	url := c.baseURL + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to reach Ollama at %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// Read NDJSON stream line-by-line.
	decoder := json.NewDecoder(resp.Body)
	var (
		content   string
		thinking  string
		toolCalls []ToolCall
	)
	for {
		var chunk chatResponse
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to decode stream chunk: %w", err)
		}
		if chunk.Message.Content != "" {
			content += chunk.Message.Content
			if fn != nil {
				fn(chunk.Message.Content)
			}
		}
		if chunk.Message.Thinking != "" {
			thinking += chunk.Message.Thinking
		}
		if len(chunk.Message.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.Message.ToolCalls...)
		}
		if chunk.Done {
			break
		}
	}

	if content == "" && len(toolCalls) == 0 {
		return nil, fmt.Errorf("empty response from model")
	}

	return &ChatResult{
		Content:   content,
		Thinking:  thinking,
		ToolCalls: toolCalls,
	}, nil
}
