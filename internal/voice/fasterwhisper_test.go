package voice

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestFasterWhisperHelperProcess is the spawned subprocess for the exec seam
// test: it reads framed audio from stdin and writes a framed transcript to
// stdout, simulating the daemon protocol.
func TestFasterWhisperHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_WHISPER_HELPER") != "1" {
		return
	}
	// Signal ready on stderr (same as real daemon).
	fmt.Fprint(os.Stderr, "ready")
	// Read framed requests, write framed responses.
	for {
		head := make([]byte, 4)
		if _, err := io.ReadFull(os.Stdin, head); err != nil {
			os.Exit(0)
		}
		n := binary.LittleEndian.Uint32(head)
		data := make([]byte, n)
		if _, err := io.ReadFull(os.Stdin, data); err != nil {
			os.Exit(1)
		}
		// Respond with the input data as transcript (echo).
		resp := []byte("transcript:" + string(data))
		binary.LittleEndian.PutUint32(head, uint32(len(resp)))
		os.Stdout.Write(head)
		os.Stdout.Write(resp)
	}
}

func whisperHelperCommand(_ context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestFasterWhisperHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_WHISPER_HELPER=1")
	return cmd
}

func TestFasterWhisperDaemonTranscribe(t *testing.T) {
	execMu.Lock()
	execCommand = whisperHelperCommand
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	w := NewFasterWhisper("/opt/whisper/run.py", "/opt/whisper/model")
	wav := []byte("fake-audio-data")
	text, err := w.Transcribe(context.Background(), wav)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !strings.HasPrefix(text, "transcript:") {
		t.Errorf("transcript = %q, want prefix 'transcript:'", text)
	}
}

func TestFasterWhisperDaemonReuse(t *testing.T) {
	execMu.Lock()
	startCount := 0
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		startCount++
		return whisperHelperCommand(context.Background(), name, args...)
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	w := NewFasterWhisper("/opt/whisper/run.py", "/opt/whisper/model")
	// First call starts the daemon.
	if _, err := w.Transcribe(context.Background(), []byte("audio1")); err != nil {
		t.Fatalf("first Transcribe: %v", err)
	}
	// Second call reuses the daemon.
	if _, err := w.Transcribe(context.Background(), []byte("audio2")); err != nil {
		t.Fatalf("second Transcribe: %v", err)
	}
	execMu.Lock()
	defer execMu.Unlock()
	if startCount != 1 {
		t.Errorf("daemon started %d times, want 1 (reuse)", startCount)
	}
}

func TestFasterWhisperEmptyAudio(t *testing.T) {
	w := NewFasterWhisper("/opt/whisper/run.py", "/opt/whisper/model")
	if _, err := w.Transcribe(context.Background(), nil); err == nil {
		t.Error("Transcribe accepted nil audio")
	}
	if _, err := w.Transcribe(context.Background(), []byte{}); err == nil {
		t.Error("Transcribe accepted empty audio")
	}
}

func TestFasterWhisperDaemonRestart(t *testing.T) {
	execMu.Lock()
	callCount := 0
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			// First daemon: exit immediately (simulates crash).
			return exec.Command("sh", "-c", "exit 1")
		}
		// Subsequent daemons: echo protocol.
		return whisperHelperCommand(context.Background(), name, args...)
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	w := NewFasterWhisper("/opt/whisper/run.py", "/opt/whisper/model")
	// First call fails (daemon crashes).
	if _, err := w.Transcribe(context.Background(), []byte("audio")); err == nil {
		t.Error("expected error from crashed daemon")
	}
	// Second call should restart the daemon and succeed.
	text, err := w.Transcribe(context.Background(), []byte("retry"))
	if err != nil {
		t.Fatalf("retry Transcribe: %v", err)
	}
	if !strings.HasPrefix(text, "transcript:") {
		t.Errorf("transcript = %q, want prefix 'transcript:'", text)
	}
}
