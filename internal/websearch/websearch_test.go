package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearch(t *testing.T) {
	var gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody = readAll(t, r.Body)
		json.NewEncoder(w).Encode(searchResponse{
			Results: []Result{
				{Title: "Paris", URL: "https://example.com/paris", Content: "Paris is the capital of France."},
			},
		})
	}))
	defer server.Close()

	c := NewWithEndpoint("tvly-test", server.URL)
	results, err := c.Search(context.Background(), "capital of france", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Title != "Paris" {
		t.Errorf("Title = %q", results[0].Title)
	}

	if gotAuth != "Bearer tvly-test" {
		t.Errorf("Authorization = %q, want Bearer tvly-test", gotAuth)
	}
	if !strings.Contains(gotBody, `"query":"capital of france"`) {
		t.Errorf("body missing query: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"max_results":3`) {
		t.Errorf("body missing max_results: %s", gotBody)
	}
}

func TestSearchHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	c := NewWithEndpoint("tvly-test", server.URL)
	if _, err := c.Search(context.Background(), "q", 3); err == nil {
		t.Fatal("Search succeeded on 401, want error")
	}
}

func TestSearchMissingKey(t *testing.T) {
	c := New("")
	if _, err := c.Search(context.Background(), "q", 3); err == nil {
		t.Fatal("Search succeeded without key, want error")
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	c := New("tvly-test")
	if _, err := c.Search(context.Background(), "  ", 3); err == nil {
		t.Fatal("Search succeeded with empty query, want error")
	}
}

func TestFormat(t *testing.T) {
	out := Format([]Result{
		{Title: "A", URL: "https://a.example", Content: "snippet a"},
		{Title: "B", URL: "https://b.example", Content: ""},
	})
	if !strings.Contains(out, "1. A\nhttps://a.example\nsnippet a") {
		t.Errorf("unexpected format:\n%s", out)
	}
	if !strings.Contains(out, "2. B\nhttps://b.example") {
		t.Errorf("unexpected format:\n%s", out)
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
