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

// KokoroTTS synthesizes speech with a long-lived Kokoro process (ONNX via
// onnxruntime, no torch). The model and voices load once when the daemon
// starts, so every call after the first is a fast stdin/stdout round-trip.
// The daemon speaks the same framed protocol as the TTS daemon:
//
//	[u32 length][bytes] text in, [u32 length][WAV bytes] out.
type KokoroTTS struct {
	daemonPath string
	modelPath  string
	voicesPath string
	voice      string

	mu sync.Mutex
	d  *ttsDaemon
}

// NewKokoro returns a TTS engine that runs the daemon script at daemonPath
// with the ONNX model, voices file, and voice name.
func NewKokoro(daemonPath, modelPath, voicesPath, voice string) *KokoroTTS {
	return &KokoroTTS{
		daemonPath: daemonPath,
		modelPath:  modelPath,
		voicesPath: voicesPath,
		voice:      voice,
	}
}

// Start launches the daemon now (pre-warm) instead of lazily on first call.
func (p *KokoroTTS) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startLocked(ctx)
}

func (p *KokoroTTS) startLocked(ctx context.Context) error {
	if p.d != nil {
		return nil
	}
	cmd := execCommand(ctx, "python3", p.daemonPath, p.modelPath, p.voicesPath, p.voice)
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
		return fmt.Errorf("kokoro daemon start: %w", err)
	}
	p.d = &ttsDaemon{cmd: cmd, stdin: stdin, stdout: stdout}
	return nil
}

// Synthesize renders text to WAV bytes via the resident daemon.
func (p *KokoroTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
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
		return nil, fmt.Errorf("kokoro daemon write: %w", err)
	}

	out, err := p.readFrame(ctx)
	if err != nil {
		p.d = nil
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("kokoro returned empty audio")
	}
	return out, nil
}

// readFrame reads one [u32 length][WAV bytes] frame from the daemon with a
// bounded wait. A wedged daemon is killed so the read goroutine unblocks.
func (p *KokoroTTS) readFrame(ctx context.Context) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		head := make([]byte, 4)
		if _, err := io.ReadFull(p.d.stdout, head); err != nil {
			ch <- result{nil, fmt.Errorf("kokoro daemon read: %w", err)}
			return
		}
		n := binary.LittleEndian.Uint32(head)
		if n == 0 || n > maxTTSBytes {
			ch <- result{nil, fmt.Errorf("kokoro returned invalid size %d", n)}
			return
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(p.d.stdout, data); err != nil {
			ch <- result{nil, fmt.Errorf("kokoro daemon read: %w", err)}
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
		return nil, fmt.Errorf("kokoro daemon timed out")
	case <-ctx.Done():
		if p.d != nil {
			_ = p.d.cmd.Process.Kill()
		}
		return nil, ctx.Err()
	}
}

// Voice returns the active voice id for the UI.
func (p *KokoroTTS) Voice() string { return p.voice }
