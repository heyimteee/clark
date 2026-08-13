package voice

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// --- OllamaWhisper ---

// TestWhisperTranscribeRequestShape verifies the request body and URL against
// a live httptest server that echoes what it received.
func TestWhisperTranscribeRequestShape(t *testing.T) {
	const audioBase64 = "UklGRi4AAABXQVZFZm10IBAAAAABAAEAgD4AAIA+AAABAAgAZGF0YQAAAAAAAAAA"
	audioBytes, err := base64.StdEncoding.DecodeString(audioBase64)
	if err != nil {
		t.Fatalf("decode test audio: %v", err)
	}

	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":"hello world"}`))
	}))
	defer srv.Close()

	w := NewOllamaWhisper(srv.URL+"/", "whisper-turbo")
	text, err := w.Transcribe(context.Background(), audioBytes)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if gotPath != "/api/generate" {
		t.Errorf("path = %q, want /api/generate", gotPath)
	}
	if text != "hello world" {
		t.Errorf("transcript = %q, want hello world", text)
	}

	if gotBody["model"] != "whisper-turbo" {
		t.Errorf("model = %v, want whisper-turbo", gotBody["model"])
	}
	if gotBody["prompt"] != "<|startoftranscript|>" {
		t.Errorf("prompt = %v, want start-of-transcript token", gotBody["prompt"])
	}
	if gotBody["audio"] != audioBase64 {
		t.Errorf("audio not sent as base64")
	}
	if gotBody["stream"] != false {
		t.Errorf("stream = %v, want false", gotBody["stream"])
	}
	if gotBody["keep_alive"] != "10m" {
		t.Errorf("keep_alive = %v, want 10m", gotBody["keep_alive"])
	}
}

// TestWhisperTranscribeErrors covers guards and failure propagation.
func TestWhisperTranscribeErrors(t *testing.T) {
	w := NewOllamaWhisper("http://localhost:9", "whisper-turbo")
	if _, err := w.Transcribe(context.Background(), nil); err == nil {
		t.Error("Transcribe accepted nil audio")
	}
	if _, err := w.Transcribe(context.Background(), make([]byte, maxAudioSize+1)); err == nil {
		t.Error("Transcribe accepted oversized audio")
	}

	small := make([]byte, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	w = NewOllamaWhisper(srv.URL, "nope")
	if _, err := w.Transcribe(context.Background(), small); err == nil {
		t.Error("Transcribe succeeded against a 404 server")
	}
}

// --- shared daemon helpers ---

// execCommand is stubbed by the kokoro tests below.
var execMu sync.Mutex

// testWAV builds a valid 22.05 kHz mono WAV around the given PCM, matching the
// WAV the kokoro python daemon writes.
func testWAV(pcm []byte) []byte {
	wav := make([]byte, 44+len(pcm))
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+len(pcm)))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 22050)
	binary.LittleEndian.PutUint32(wav[28:32], 22050*2)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(len(pcm)))
	copy(wav[44:], pcm)
	return wav
}

// --- helpers ---

func u16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

func u32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
