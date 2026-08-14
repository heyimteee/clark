package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKokoroRemoteSynthesize(t *testing.T) {
	wantWAV := []byte("RIFF....WAVEfmt data")
	var gotPath string
	var gotToken string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Clark-Kokoro-Token")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"audio": base64.StdEncoding.EncodeToString(wantWAV), "format": "audio/wav"})
	}))
	defer srv.Close()

	r := NewKokoroRemote(srv.URL, "mac-secret", "am_michael")
	wav, err := r.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(wav) != string(wantWAV) {
		t.Errorf("wav = %q, want %q", wav, wantWAV)
	}
	if gotPath != "/tts" {
		t.Errorf("path = %q, want /tts", gotPath)
	}
	if gotToken != "mac-secret" {
		t.Errorf("token header = %q, want mac-secret", gotToken)
	}
	if gotBody["text"] != "hello" || gotBody["voice"] != "am_michael" {
		t.Errorf("body = %v, want text+voice", gotBody)
	}
}

func TestKokoroRemoteErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	r := NewKokoroRemote(srv.URL, "", "am_michael")
	if _, err := r.Synthesize(context.Background(), "hi"); err == nil {
		t.Error("Synthesize succeeded against a 502 server")
	}
	if _, err := r.Synthesize(context.Background(), "   "); err == nil {
		t.Error("Synthesize accepted blank text")
	}
}

func TestKokoroRemoteUnreachable(t *testing.T) {
	r := NewKokoroRemote("http://127.0.0.1:1", "", "am_michael")
	if _, err := r.Synthesize(context.Background(), "hi"); err == nil {
		t.Error("Synthesize succeeded against an unreachable host")
	}
}

type stubFailTTS struct {
	text  string
	err   error
	calls int
}

func (s *stubFailTTS) Synthesize(_ context.Context, _ string) ([]byte, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return []byte("WAV" + s.text), nil
}
func (s *stubFailTTS) Voice() string { return s.text }

func TestFailoverTTSPrimaryFirst(t *testing.T) {
	primary := &stubFailTTS{text: "p"}
	backup := &stubFailTTS{text: "b"}
	f := NewFailoverTTS(primary, backup)

	wav, err := f.Synthesize(context.Background(), "x")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(wav) != "WAVp" {
		t.Errorf("wav = %q, want primary result", wav)
	}
	if primary.calls != 1 || backup.calls != 0 {
		t.Errorf("calls p=%d b=%d, want p=1 b=0", primary.calls, backup.calls)
	}
	if f.Voice() != "p" {
		t.Errorf("Voice() = %q, want primary voice", f.Voice())
	}
}

func TestFailoverTTSBackupOnPrimaryFailure(t *testing.T) {
	primary := &stubFailTTS{err: context.DeadlineExceeded}
	backup := &stubFailTTS{text: "b"}
	f := NewFailoverTTS(primary, backup)

	wav, err := f.Synthesize(context.Background(), "x")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(wav) != "WAVb" {
		t.Errorf("wav = %q, want backup result", wav)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Errorf("calls p=%d b=%d, want 1/1", primary.calls, backup.calls)
	}
}

func TestFailoverTTSNoBackup(t *testing.T) {
	primary := &stubFailTTS{err: context.Canceled}
	f := NewFailoverTTS(primary, nil)

	if _, err := f.Synthesize(context.Background(), "x"); err == nil {
		t.Error("Synthesize succeeded without a backup")
	}
}

// genericErr is a synthesis-level failure (not connection-level): it should not
// trigger a fallback until the consecutive-failure threshold is reached.
var genericErr = fmt.Errorf("synthesis failed: boom")

func TestFailoverTTSTransientFailureNoFallback(t *testing.T) {
	primary := &stubFailTTS{err: genericErr}
	backup := &stubFailTTS{text: "b"}
	f := NewFailoverTTS(primary, backup)

	if _, err := f.Synthesize(context.Background(), "x"); err == nil {
		t.Fatal("Synthesize succeeded on first failure")
	}
	if primary.calls != 1 || backup.calls != 0 {
		t.Errorf("calls p=%d b=%d, want p=1 b=0 (no fallback on first transient failure)", primary.calls, backup.calls)
	}
}

func TestFailoverTTSPersistentFailureFallsBack(t *testing.T) {
	primary := &stubFailTTS{err: genericErr}
	backup := &stubFailTTS{text: "b"}
	f := NewFailoverTTS(primary, backup)

	for i := 0; i < failThreshold; i++ {
		if _, err := f.Synthesize(context.Background(), "x"); err == nil && i < failThreshold-1 {
			t.Fatalf("Synthesize %d succeeded, want failure", i+1)
		}
	}
	if backup.calls != 1 {
		t.Errorf("backup calls = %d, want 1 after %d consecutive failures", backup.calls, failThreshold)
	}
}

func TestFailoverTTSSuccessResetsCounter(t *testing.T) {
	primary := &stubFailTTS{err: genericErr}
	backup := &stubFailTTS{text: "b"}
	f := NewFailoverTTS(primary, backup)

	// First failure: no fallback, counter = 1.
	if _, err := f.Synthesize(context.Background(), "x"); err == nil {
		t.Fatal("first synthesize should fail")
	}
	// Recovery: primary succeeds, resets the counter.
	primary.err = nil
	if _, err := f.Synthesize(context.Background(), "x"); err != nil {
		t.Fatalf("recovered synthesize: %v", err)
	}
	// Next failure should again be a transient (counter back to 1).
	primary.err = genericErr
	if _, err := f.Synthesize(context.Background(), "x"); err == nil {
		t.Fatal("post-reset failure should not succeed")
	}
	if backup.calls != 0 {
		t.Errorf("backup calls = %d, want 0 (counter reset after recovery)", backup.calls)
	}
}

func TestFailoverTTSTimeoutFallsBackImmediately(t *testing.T) {
	primary := &stubFailTTS{err: context.DeadlineExceeded}
	backup := &stubFailTTS{text: "b"}
	f := NewFailoverTTS(primary, backup)

	wav, err := f.Synthesize(context.Background(), "x")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(wav) != "WAVb" {
		t.Errorf("wav = %q, want backup on timeout", wav)
	}
}
