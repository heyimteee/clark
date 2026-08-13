package voice

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
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

// execCommand is stubbed by the piper tests below.
var execMu sync.Mutex

// testWAV builds a valid 22.05 kHz mono WAV around the given PCM, matching the
// WAV the piper python daemon writes.
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

// TestPiperHelperProcess implements the daemon frame protocol: it loops reading
// length-prefixed requests and answering each with a length-prefixed WAV, so it
// behaves like a resident process. Not a real test; it is the spawned
// subprocess for the exec seam tests.
func TestPiperHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PIPER_HELPER") != "1" {
		return
	}
	for {
		head := make([]byte, 4)
		if _, err := io.ReadFull(os.Stdin, head); err != nil {
			os.Exit(0)
		}
		n := binary.LittleEndian.Uint32(head)
		buf := make([]byte, n)
		if _, err := io.ReadFull(os.Stdin, buf); err != nil {
			os.Exit(0)
		}
		payload := testWAV(make([]byte, 100))
		out := make([]byte, 4+len(payload))
		binary.LittleEndian.PutUint32(out, uint32(len(payload)))
		copy(out[4:], payload)
		if _, err := os.Stdout.Write(out); err != nil {
			os.Exit(1)
		}
	}
}

func piperHelperCommand(_ context.Context, _ string, _ ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestPiperHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_PIPER_HELPER=1")
	return cmd
}

// TestPiperSynthesizeWAVHeader verifies the WAV returned by the daemon survives
// the framed round-trip intact.
func TestPiperSynthesizeWAVHeader(t *testing.T) {
	execMu.Lock()
	execCommand = piperHelperCommand
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	p := NewPiper("/opt/piper/daemon.py", "/opt/piper/voices/en_US-ryan-high.onnx")
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
	if u32(wav[40:44]) != 100 {
		t.Errorf("dataSize = %d, want 100 (raw PCM length)", u32(wav[40:44]))
	}
	if len(wav) != 44+100 {
		t.Errorf("len(wav) = %d, want 144", len(wav))
	}
}

// TestPiperCommandArgs verifies the exact command the daemon runs.
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

	p := NewPiper("/opt/piper/daemon.py", "/opt/piper/voices/en_US-ryan-high.onnx")
	if _, err := p.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	if gotName != "python3" {
		t.Errorf("interpreter = %q, want python3", gotName)
	}
	want := []string{"/opt/piper/daemon.py", "/opt/piper/voices/en_US-ryan-high.onnx"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

// TestPiperStartPreWarms ensures the daemon is spawned exactly once and stays
// resident across calls.
func TestPiperStartPreWarms(t *testing.T) {
	execMu.Lock()
	var calls int
	execCommand = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		calls++
		return piperHelperCommand(nil, "", "")
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	p := NewPiper("/opt/piper/daemon.py", "/opt/piper/voices/en_US-ryan-high.onnx")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := p.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if _, err := p.Synthesize(context.Background(), "again"); err != nil {
		t.Fatalf("second Synthesize: %v", err)
	}
	if calls != 1 {
		t.Errorf("daemon spawned %d times, want 1 (resident)", calls)
	}
}

// TestPiperSynthesizeErrors propagates daemon failures.
func TestPiperSynthesizeErrors(t *testing.T) {
	execMu.Lock()
	execCommand = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo boom >&2; exit 1")
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	p := NewPiper("/opt/piper/daemon.py", "/opt/piper/voices/en_US-ryan-high.onnx")
	if _, err := p.Synthesize(context.Background(), "hi"); err == nil {
		t.Error("Synthesize succeeded despite piper exiting 1")
	}

	if _, err := p.Synthesize(context.Background(), "   "); err == nil {
		t.Error("Synthesize accepted blank text")
	}
}

// TestPiperVoiceName strips the .onnx extension for the UI.
func TestPiperVoiceName(t *testing.T) {
	p := NewPiper("/bin/piper", filepath.Join("voices", "en_US-ryan-high.onnx"))
	if got := p.Voice(); got != "en_US-ryan-high" {
		t.Errorf("Voice() = %q, want en_US-ryan-high", got)
	}
}

// --- helpers ---

func u16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

func u32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
