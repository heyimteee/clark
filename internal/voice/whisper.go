package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxAudioSize caps uploaded WAV blobs (25 MB).
const maxAudioSize = 25 << 20

// OllamaWhisper transcribes audio via an Ollama server running a whisper
// model. It talks to the same /api/generate endpoint Ollama exposes for any
// model, so it is independent of the chat pipeline.
type OllamaWhisper struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewOllamaWhisper returns a whisper client for the given Ollama base URL.
func NewOllamaWhisper(baseURL, model string) *OllamaWhisper {
	return &OllamaWhisper{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Transcribe posts the WAV audio to Ollama and returns the transcript.
func (w *OllamaWhisper) Transcribe(ctx context.Context, audioWAV []byte) (string, error) {
	if len(audioWAV) == 0 {
		return "", fmt.Errorf("empty audio")
	}
	if len(audioWAV) > maxAudioSize {
		return "", fmt.Errorf("audio too large: %d bytes (max %d)", len(audioWAV), maxAudioSize)
	}

	body, err := json.Marshal(map[string]any{
		"model":      w.model,
		"prompt":     "<|startoftranscript|>",
		"audio":      base64.StdEncoding.EncodeToString(audioWAV),
		"stream":     false,
		"keep_alive": "10m",
	})
	if err != nil {
		return "", fmt.Errorf("failed to encode request: %w", err)
	}

	url := w.baseURL + "/api/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to reach Ollama at %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var out struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	out.Response = strings.TrimSpace(out.Response)
	if out.Response == "" {
		return "", fmt.Errorf("empty transcription")
	}
	return out.Response, nil
}
