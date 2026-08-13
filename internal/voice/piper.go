package voice

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxTTSBytes caps a single synthesized clip (generous; ~5 min at 22.05 kHz).
const maxTTSBytes = 20 << 20

// daemonReadTimeout bounds waiting for a synthesis frame from a resident TTS
// daemon. It must comfortably exceed the web handler's synthesis timeout so a
// long clip is never killed mid-flight (Kokoro on the i5 is ~1.2-1.5x
// real-time). A genuinely wedged daemon still dies when the caller's ctx
// expires.
const daemonReadTimeout = 3 * time.Minute

// execCommand is overridable so tests can stub the piper process.
var execCommand = exec.CommandContext

// PiperTTS synthesizes speech with a long-lived piper process (the v5 daemon
// idea pulled into v4). The model is loaded once when the daemon starts and
// stays resident, so every call after the first is a stdin/stdout round-trip
// instead of a fresh process + model load. The daemon speaks a tiny framed
// protocol: [u32 length][bytes] text in, [u32 length][WAV bytes] out.
type PiperTTS struct {
	binPath   string
	voicePath string

	mu sync.Mutex
	d  *piperDaemon
}

// piperDaemon is the live python process and its pipes.
type piperDaemon struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// NewPiper returns a TTS engine that runs the daemon script at binPath with
// the voice model at voicePath.
func NewPiper(binPath, voicePath string) *PiperTTS {
	return &PiperTTS{binPath: binPath, voicePath: voicePath}
}

// Start launches the daemon now (pre-warm) instead of lazily on first call.
// Safe to call multiple times; a running daemon is left alone.
func (p *PiperTTS) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startLocked(ctx)
}

func (p *PiperTTS) startLocked(ctx context.Context) error {
	if p.d != nil {
		return nil
	}
	cmd := execCommand(ctx, "python3", p.binPath, p.voicePath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("piper daemon start: %w", err)
	}
	p.d = &piperDaemon{cmd: cmd, stdin: stdin, stdout: stdout}
	return nil
}

// Synthesize renders text to WAV bytes via the resident daemon.
func (p *PiperTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.d == nil {
		if err := p.startLocked(ctx); err != nil {
			return nil, err
		}
	}

	if _, err := p.d.stdin.Write(frameBytes(text)); err != nil {
		p.d = nil
		return nil, fmt.Errorf("piper daemon write: %w", err)
	}

	out, err := p.readFrame(ctx)
	if err != nil {
		p.d = nil
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("piper returned empty audio")
	}
	return out, nil
}

// readFrame reads one [u32 length][WAV bytes] frame from the daemon with a
// bounded wait. A wedged daemon is killed so the read goroutine unblocks.
func (p *PiperTTS) readFrame(ctx context.Context) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		head := make([]byte, 4)
		if _, err := io.ReadFull(p.d.stdout, head); err != nil {
			ch <- result{nil, fmt.Errorf("piper daemon read: %w", err)}
			return
		}
		n := binary.LittleEndian.Uint32(head)
		if n == 0 || n > maxTTSBytes {
			ch <- result{nil, fmt.Errorf("piper returned invalid size %d", n)}
			return
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(p.d.stdout, data); err != nil {
			ch <- result{nil, fmt.Errorf("piper daemon read: %w", err)}
			return
		}
		ch <- result{data: data}
	}()

	select {
	case r := <-ch:
		return r.data, r.err
	case <-time.After(daemonReadTimeout):
		if p.d != nil {
			_ = p.d.cmd.Process.Kill()
		}
		return nil, fmt.Errorf("piper daemon timed out")
	case <-ctx.Done():
		if p.d != nil {
			_ = p.d.cmd.Process.Kill()
		}
		return nil, ctx.Err()
	}
}

// frameBytes length-prefixes the UTF-8 text for the daemon.
func frameBytes(text string) []byte {
	data := []byte(text)
	frame := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	return frame
}

// Voice returns the voice id without the .onnx extension for display.
func (p *PiperTTS) Voice() string {
	name := filepath.Base(p.voicePath)
	return strings.TrimSuffix(name, ".onnx")
}
