package voice

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestKokoroHelperProcess implements the daemon frame protocol: it loops
// reading length-prefixed requests and answering each with a length-prefixed
// WAV, so it behaves like a resident process. Not a real test; it is the
// spawned subprocess for the exec seam tests.
func TestKokoroHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_KOKORO_HELPER") != "1" {
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

func kokoroHelperCommand(_ context.Context, _ string, _ ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=TestKokoroHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_KOKORO_HELPER=1")
	return cmd
}

// TestKokoroCommandArgs verifies the exact command the daemon runs.
func TestKokoroCommandArgs(t *testing.T) {
	execMu.Lock()
	var gotName string
	var gotArgs []string
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = args
		cmd := exec.Command(os.Args[0], "-test.run=TestKokoroHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_KOKORO_HELPER=1")
		return cmd
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	k := NewKokoro("/opt/kokoro/daemon.py", "/opt/kokoro/model/kokoro-v1.0.int8.onnx", "/opt/kokoro/model/voices-v1.0.bin", "am_michael")
	if _, err := k.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	if gotName != "python3" {
		t.Errorf("interpreter = %q, want python3", gotName)
	}
	want := "/opt/kokoro/daemon.py /opt/kokoro/model/kokoro-v1.0.int8.onnx /opt/kokoro/model/voices-v1.0.bin am_michael"
	if strings.Join(gotArgs, " ") != want {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

// TestKokoroStartPreWarms ensures the daemon is spawned exactly once and stays
// resident across calls.
func TestKokoroStartPreWarms(t *testing.T) {
	execMu.Lock()
	var calls int
	execCommand = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		calls++
		return kokoroHelperCommand(nil, "", "")
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	k := NewKokoro("/opt/kokoro/daemon.py", "/opt/kokoro/model/kokoro-v1.0.int8.onnx", "/opt/kokoro/model/voices-v1.0.bin", "am_michael")
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := k.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if _, err := k.Synthesize(context.Background(), "again"); err != nil {
		t.Fatalf("second Synthesize: %v", err)
	}
	if calls != 1 {
		t.Errorf("daemon spawned %d times, want 1 (resident)", calls)
	}
}

// TestKokoroSynthesizeWAVHeader verifies the WAV returned by the daemon
// survives the framed round-trip intact.
func TestKokoroSynthesizeWAVHeader(t *testing.T) {
	execMu.Lock()
	execCommand = kokoroHelperCommand
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	k := NewKokoro("/opt/kokoro/daemon.py", "/opt/kokoro/model/kokoro-v1.0.int8.onnx", "/opt/kokoro/model/voices-v1.0.bin", "am_michael")
	wav, err := k.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(wav[:4]) != "RIFF" || string(wav[36:40]) != "data" {
		t.Errorf("wav header missing RIFF/data chunks: %q", wav[:44])
	}
	if u32(wav[40:44]) != 100 {
		t.Errorf("dataSize = %d, want 100", u32(wav[40:44]))
	}
}

// TestKokoroErrors covers guards and failure propagation.
func TestKokoroErrors(t *testing.T) {
	execMu.Lock()
	execCommand = func(_ context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo boom >&2; exit 1")
	}
	execMu.Unlock()
	defer func() { execCommand = exec.CommandContext }()

	k := NewKokoro("/opt/kokoro/daemon.py", "/opt/kokoro/model/kokoro-v1.0.int8.onnx", "/opt/kokoro/model/voices-v1.0.bin", "am_michael")
	if _, err := k.Synthesize(context.Background(), "hi"); err == nil {
		t.Error("Synthesize succeeded despite daemon exiting 1")
	}
	if _, err := k.Synthesize(context.Background(), "   "); err == nil {
		t.Error("Synthesize accepted blank text")
	}
}

// TestKokoroVoiceName returns the configured voice id.
func TestKokoroVoiceName(t *testing.T) {
	k := NewKokoro("/opt/kokoro/daemon.py", "m", "v", "am_michael")
	if got := k.Voice(); got != "am_michael" {
		t.Errorf("Voice() = %q, want am_michael", got)
	}
}
