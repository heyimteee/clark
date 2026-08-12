package voice

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestFasterWhisperHelperProcess is the spawned subprocess for the exec seam
// test: it echoes stdin back and appends a transcript.
func TestFasterWhisperHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_WHISPER_HELPER") != "1" {
		return
	}
	_, err := io.Copy(os.Stdout, os.Stdin)
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func whisperHelperCommand(_ context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestFasterWhisperHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_WHISPER_HELPER=1")
	return cmd
}

// TestFasterWhisperTranscribe passes the WAV bytes on stdin and uses stdout
// as the transcript.
func TestFasterWhisperTranscribe(t *testing.T) {
	execMu.Lock()
	execCommand = whisperHelperCommand
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	w := NewFasterWhisper("/opt/whisper/run.py", "/opt/whisper/model")
	wav := []byte{0x52, 0x49, 0x46, 0x46, 0x01, 0x02, 0x03}
	text, err := w.Transcribe(context.Background(), wav)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if text != string(wav) {
		t.Errorf("transcript = %q, want stdin bytes %q", text, wav)
	}
}

// TestFasterWhisperCommandArgs verifies the exact command the engine runs.
func TestFasterWhisperCommandArgs(t *testing.T) {
	execMu.Lock()
	var gotName string
	var gotArgs []string
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = args
		cmd := exec.Command(os.Args[0], "-test.run=TestFasterWhisperHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_WHISPER_HELPER=1")
		return cmd
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	w := NewFasterWhisper("/opt/whisper/run.py", "/opt/whisper/model")
	if _, err := w.Transcribe(context.Background(), []byte{1, 2, 3}); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if gotName != "python3" {
		t.Errorf("interpreter = %q, want python3", gotName)
	}
	if strings.Join(gotArgs, " ") != "/opt/whisper/run.py /opt/whisper/model" {
		t.Errorf("args = %v, want script + model dir", gotArgs)
	}
}

// TestFasterWhisperErrors covers guards and failure propagation.
func TestFasterWhisperErrors(t *testing.T) {
	execMu.Lock()
	execCommand = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo boom >&2; exit 1")
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	w := NewFasterWhisper("/opt/whisper/run.py", "/opt/whisper/model")
	if _, err := w.Transcribe(context.Background(), nil); err == nil {
		t.Error("Transcribe accepted nil audio")
	}
	if _, err := w.Transcribe(context.Background(), []byte{1}); err == nil {
		t.Error("Transcribe succeeded despite runner exiting 1")
	}
}

// TestFasterWhisperEmptyTranscript treats an empty stdout as a failure.
func TestFasterWhisperEmptyTranscript(t *testing.T) {
	execMu.Lock()
	execCommand = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "exit 0")
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	w := NewFasterWhisper("/opt/whisper/run.py", "/opt/whisper/model")
	if _, err := w.Transcribe(context.Background(), []byte{1}); err == nil {
		t.Error("Transcribe accepted an empty transcript")
	}
}
