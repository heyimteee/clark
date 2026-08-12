package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// --- PiperTTS ---

// execCommand is stubbed by the tests below.
var execMu sync.Mutex

// TestPiperHelperProcess writes deterministic raw PCM to stdout. It is not a
// real test; it is the spawned subprocess for the exec seam tests.
func TestPiperHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PIPER_HELPER") != "1" {
		return
	}
	if _, err := os.Stdout.Write(make([]byte, 100)); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func piperHelperCommand(_ context.Context, _ string, _ ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestPiperHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_PIPER_HELPER=1")
	return cmd
}

// TestPiperSynthesizeWAVHeader verifies the RIFF/WAVE header wrapping the raw
// PCM produced by the piper binary.
func TestPiperSynthesizeWAVHeader(t *testing.T) {
	execMu.Lock()
	execCommand = piperHelperCommand
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	p := NewPiper("/opt/piper/piper", "/opt/piper/voices/en_US-amy-medium.onnx")
	wav, err := p.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	head := string(wav[:4])
	if head != "RIFF" {
		t.Errorf("magic = %q, want RIFF", head)
	}
	if string(wav[8:12]) != "WAVE" {
		t.Errorf("format chunk missing WAVE")
	}
	if string(wav[36:40]) != "data" {
		t.Errorf("missing data chunk")
	}
	if string(wav[12:16]) != "fmt " {
		t.Errorf("missing fmt chunk")
	}

	// fmt sub-chunk details (little-endian).
	if u16(wav[20:22]) != 1 {
		t.Errorf("audioFormat = %d, want 1 (PCM)", u16(wav[20:22]))
	}
	if u16(wav[22:24]) != 1 {
		t.Errorf("channels = %d, want 1 (mono)", u16(wav[22:24]))
	}
	if u32(wav[24:28]) != 22050 {
		t.Errorf("sampleRate = %d, want 22050", u32(wav[24:28]))
	}
	if u16(wav[34:36]) != 16 {
		t.Errorf("bitsPerSample = %d, want 16", u16(wav[34:36]))
	}
	if u16(wav[32:34]) != 2 {
		t.Errorf("blockAlign = %d, want 2", u16(wav[32:34]))
	}
	if u32(wav[40:44]) != 100 {
		t.Errorf("dataSize = %d, want 100 (raw PCM length)", u32(wav[40:44]))
	}
	if len(wav) != 44+100 {
		t.Errorf("len(wav) = %d, want 144", len(wav))
	}
}

// TestPiperCommandArgs verifies the exact arguments passed to the binary.
func TestPiperCommandArgs(t *testing.T) {
	execMu.Lock()
	var gotName string
	var gotArgs []string
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = args
		cmd := exec.Command(os.Args[0], "-test.run=TestPiperHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_PIPER_HELPER=1")
		return cmd
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	p := NewPiper("/opt/piper/piper", "/opt/piper/voices/en_US-amy-medium.onnx")
	if _, err := p.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	if gotName != "/opt/piper/piper" {
		t.Errorf("binary = %q, want /opt/piper/piper", gotName)
	}
	want := []string{"--model", "/opt/piper/voices/en_US-amy-medium.onnx", "--output-raw"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

// TestPiperSynthesizeErrors propagates binary failures.
func TestPiperSynthesizeErrors(t *testing.T) {
	execMu.Lock()
	execCommand = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo boom >&2; exit 1")
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	p := NewPiper("/opt/piper/piper", "/opt/piper/voices/en_US-amy-medium.onnx")
	if _, err := p.Synthesize(context.Background(), "hi"); err == nil {
		t.Error("Synthesize succeeded despite piper exiting 1")
	}

	if _, err := p.Synthesize(context.Background(), "   "); err == nil {
		t.Error("Synthesize accepted blank text")
	}
}

// TestPiperVoiceName strips the .onnx extension for the UI.
func TestPiperVoiceName(t *testing.T) {
	p := NewPiper("/bin/piper", filepath.Join("voices", "en_US-amy-medium.onnx"))
	if got := p.Voice(); got != "en_US-amy-medium" {
		t.Errorf("Voice() = %q, want en_US-amy-medium", got)
	}
}

// --- helpers ---

func u16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

func u32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
