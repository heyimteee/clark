package voice

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// sttReadTimeout bounds waiting for a transcript frame from the resident
// FasterWhisper daemon. Generous enough for long audio clips on CPU.
const sttReadTimeout = 90 * time.Second

// maxSTTBytes caps a single transcription input (~5 min of audio at 16 kHz).
const maxSTTBytes = 20 << 20

// FasterWhisper transcribes audio with the faster-whisper python package,
// running as a long-lived daemon process. The model is loaded once at startup
// and stays resident, so every call after the first is a stdin/stdout
// round-trip instead of a fresh process + model load.
type FasterWhisper struct {
	scriptPath string
	modelDir   string

	mu sync.Mutex
	d  *ttsDaemon
}

// NewFasterWhisper returns an STT engine that runs the daemon script at
// scriptPath with the faster-whisper model at modelDir.
func NewFasterWhisper(scriptPath, modelDir string) *FasterWhisper {
	return &FasterWhisper{scriptPath: scriptPath, modelDir: modelDir}
}

// Start launches the daemon now (pre-warm) instead of lazily on first call.
// Safe to call multiple times; a running daemon is left alone.
func (w *FasterWhisper) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startLocked(ctx)
}

func (w *FasterWhisper) startLocked(ctx context.Context) error {
	if w.d != nil {
		return nil
	}
	cmd := execCommand(ctx, "python3", w.scriptPath, w.modelDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("faster-whisper daemon start: %w", err)
	}
	w.d = &ttsDaemon{cmd: cmd, stdin: stdin, stdout: stdout}

	// Wait for "ready" on stderr (model loaded).
	readyCh := make(chan struct{})
	go func() {
		buf := make([]byte, 64)
		n, _ := stderr.Read(buf)
		if n > 0 && strings.Contains(string(buf[:n]), "ready") {
			close(readyCh)
		}
	}()
	// Also watch for process exit (crash before ready).
	exitCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exitCh)
	}()

	select {
	case <-readyCh:
	case <-exitCh:
		w.d = nil
		return fmt.Errorf("faster-whisper daemon exited before ready")
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		w.d = nil
		return fmt.Errorf("faster-whisper daemon did not become ready in time")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		w.d = nil
		return ctx.Err()
	}
	return nil
}

// Transcribe pipes the WAV audio into the daemon on stdin and returns the
// transcript read from stdout. If the write fails (daemon crashed), the daemon
// is restarted and the write is retried once.
func (w *FasterWhisper) Transcribe(ctx context.Context, audioWAV []byte) (string, error) {
	if len(audioWAV) == 0 {
		return "", fmt.Errorf("empty audio")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.d == nil {
		if err := w.startLocked(ctx); err != nil {
			return "", err
		}
	}

	// Write framed audio to daemon stdin.
	if _, err := w.d.stdin.Write(sttFrameBytes(audioWAV)); err != nil {
		// Daemon died — restart and retry once.
		w.d = nil
		if err := w.startLocked(ctx); err != nil {
			return "", fmt.Errorf("faster-whisper daemon restart: %w", err)
		}
		if _, err := w.d.stdin.Write(sttFrameBytes(audioWAV)); err != nil {
			w.d = nil
			return "", fmt.Errorf("faster-whisper daemon write: %w", err)
		}
	}

	// Read framed transcript from daemon stdout.
	text, err := w.readFrame(ctx)
	if err != nil {
		w.d = nil
		return "", err
	}
	if text == "" {
		return "", fmt.Errorf("empty transcription")
	}
	return text, nil
}

// sttFrameBytes length-prefixes raw WAV bytes for the daemon.
func sttFrameBytes(data []byte) []byte {
	frame := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	return frame
}

// readFrame reads one [u32 length][UTF-8 transcript] frame from the daemon
// with a bounded wait. A wedged daemon is killed so the read goroutine unblocks.
func (w *FasterWhisper) readFrame(ctx context.Context) (string, error) {
	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		head := make([]byte, 4)
		if _, err := io.ReadFull(w.d.stdout, head); err != nil {
			ch <- result{err: fmt.Errorf("faster-whisper daemon read: %w", err)}
			return
		}
		n := binary.LittleEndian.Uint32(head)
		if n > maxSTTBytes {
			ch <- result{err: fmt.Errorf("faster-whisper returned invalid size %d", n)}
			return
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(w.d.stdout, data); err != nil {
			ch <- result{err: fmt.Errorf("faster-whisper daemon read: %w", err)}
			return
		}
		ch <- result{text: strings.TrimSpace(string(data))}
	}()

	select {
	case r := <-ch:
		return r.text, r.err
	case <-time.After(sttReadTimeout):
		if w.d != nil {
			_ = w.d.cmd.Process.Kill()
		}
		w.d = nil
		return "", fmt.Errorf("faster-whisper daemon timed out")
	case <-ctx.Done():
		if w.d != nil {
			_ = w.d.cmd.Process.Kill()
		}
		w.d = nil
		return "", ctx.Err()
	}
}
