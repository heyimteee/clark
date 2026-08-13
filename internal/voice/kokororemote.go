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

// KokoroRemote synthesizes speech by POSTing to a remote Kokoro server (the
// kokoro_mac_server.py daemon on the Master's Mac) over the network. It
// implements the same TTS interface as the local engines, so the web/chat seam
// is unchanged. Near-instant on Apple Silicon; a no-op on failure so callers
// can fall back.
type KokoroRemote struct {
	url   string
	token string
	voice string
	http  *http.Client
}

// NewKokoroRemote returns a TTS engine that POSTs to the remote server at url
// (e.g. http://<mac-tailnet-ip>:8790). token authenticates the request; voice
// is the Kokoro voice id (e.g. am_michael).
func NewKokoroRemote(url, token, voice string) *KokoroRemote {
	return &KokoroRemote{
		url:   strings.TrimRight(url, "/"),
		token: token,
		voice: voice,
		http:  &http.Client{Timeout: 3 * time.Minute},
	}
}

// Voice returns the active voice id for the UI.
func (r *KokoroRemote) Voice() string { return r.voice }

// Synthesize renders text to WAV bytes via the remote server.
func (r *KokoroRemote) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text")
	}
	body, err := json.Marshal(map[string]any{"text": text, "voice": r.voice})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+"/tts", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("X-Clark-Kokoro-Token", r.token)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kokoro remote returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var out struct {
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("kokoro remote bad response: %w", err)
	}
	if out.Audio == "" {
		return nil, fmt.Errorf("kokoro remote empty audio")
	}
	wav, err := base64.StdEncoding.DecodeString(out.Audio)
	if err != nil {
		return nil, fmt.Errorf("kokoro remote bad base64: %w", err)
	}
	if len(wav) == 0 {
		return nil, fmt.Errorf("kokoro remote empty wav")
	}
	return wav, nil
}

// FailoverTTS tries the primary engine first and falls back to the backup when
// the primary is unreachable (e.g. the remote Mac is asleep). Voice() reports
// the primary's voice.
type FailoverTTS struct {
	primary TTS
	backup  TTS
}

// NewFailoverTTS wraps two engines with primary-first routing.
func NewFailoverTTS(primary, backup TTS) *FailoverTTS {
	return &FailoverTTS{primary: primary, backup: backup}
}

// Voice returns the primary engine's voice id.
func (f *FailoverTTS) Voice() string { return f.primary.Voice() }

// Synthesize tries primary, then backup on failure (reporting the primary's
// error if the backup also fails, as it is the more actionable one).
func (f *FailoverTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	wav, err := f.primary.Synthesize(ctx, text)
	if err == nil {
		return wav, nil
	}
	if f.backup == nil {
		return nil, err
	}
	if wav, berr := f.backup.Synthesize(ctx, text); berr == nil {
		return wav, nil
	}
	return nil, err
}

// Start pre-warms the local backup daemon (the remote server has no daemon to
// warm). Used by app boot so a fallback is resident if the Mac is unreachable.
func (f *FailoverTTS) Start(ctx context.Context) error {
	if w, ok := f.backup.(interface{ Start(context.Context) error }); ok {
		return w.Start(ctx)
	}
	return nil
}
