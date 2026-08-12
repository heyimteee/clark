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
}

// New returns a Client for the given base URL and model.
func New(baseURL, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// SetThink enables or disables reasoning tokens for subsequent chats.
func (c *Client) SetThink(on bool) { c.think = on }

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
	Think    bool      `json:"think"`
}

type chatResponse struct {
	Message struct {
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
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
