// Package websearch searches the web through the Tavily API.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultEndpoint = "https://api.tavily.com/search"

// Result is a single search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// Client searches the web via Tavily.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// New returns a Tavily-backed client. The endpoint defaults to Tavily's API.
func New(apiKey string) *Client {
	return NewWithEndpoint(apiKey, defaultEndpoint)
}

// NewWithEndpoint returns a client targeting a custom endpoint (tests, proxies).
func NewWithEndpoint(apiKey, endpoint string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

type searchRequest struct {
	APIKey      string `json:"api_key"`
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
}

type searchResponse struct {
	Results []Result `json:"results"`
}

// Search performs a basic-depth web search and returns up to maxResults hits.
func (c *Client) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	return c.SearchDepth(ctx, query, maxResults, "basic")
}

// SearchDepth performs a web search at the given depth. "advanced" returns
// more relevant, usually article-level results at a higher credit cost;
// anything else falls back to "basic".
func (c *Client) SearchDepth(ctx context.Context, query string, maxResults int, depth string) ([]Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty search query")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("Tavily API key is not set")
	}
	if depth != "advanced" {
		depth = "basic"
	}

	body, err := json.Marshal(searchRequest{
		APIKey:      c.apiKey,
		Query:       query,
		MaxResults:  maxResults,
		SearchDepth: depth,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to build search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach Tavily: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read search response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Tavily returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var sr searchResponse
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}
	return sr.Results, nil
}

// Format renders results as compact text for the model.
func Format(results []Result) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. %s\n%s", i+1, r.Title, r.URL)
		if strings.TrimSpace(r.Content) != "" {
			b.WriteString("\n" + strings.TrimSpace(r.Content))
		}
	}
	return b.String()
}
