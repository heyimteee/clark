package voice

import (
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// piperSampleRate is the fixed output rate of piper medium voices.
const piperSampleRate = 22050

// execCommand is overridable so tests can stub the piper binary.
var execCommand = exec.CommandContext

// PiperTTS synthesizes speech with the piper CLI, wrapping its raw PCM output
// in a WAV header. Process-per-call: piper loads in ~0.1-0.3 s on the i5, and
// v5 will move to a long-lived daemon for streaming.
type PiperTTS struct {
	binPath   string
	voicePath string
}

// NewPiper returns a TTS engine that runs the piper binary at binPath with
// the voice model at voicePath.
func NewPiper(binPath, voicePath string) *PiperTTS {
	return &PiperTTS{binPath: binPath, voicePath: voicePath}
}

// Synthesize renders text to 16-bit PCM mono WAV bytes at 22.05 kHz.
func (p *PiperTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text")
	}

	cmd := execCommand(ctx, p.binPath, "--model", p.voicePath, "--output-raw")
	cmd.Stdin = strings.NewReader(text)

	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("piper failed: %w", err)
	}
	return buildWAV(raw, piperSampleRate), nil
}

// Voice returns the voice id without the .onnx extension for display.
func (p *PiperTTS) Voice() string {
	name := filepath.Base(p.voicePath)
	return strings.TrimSuffix(name, ".onnx")
}

// buildWAV wraps raw PCM samples in a standard 44-byte RIFF/WAVE header.
func buildWAV(pcm []byte, sampleRate int) []byte {
	wav := make([]byte, 44+len(pcm))

	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+len(pcm)))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(wav[20:22], 1)  // audio format: PCM
	binary.LittleEndian.PutUint16(wav[22:24], 1)  // channels: mono
	binary.LittleEndian.PutUint32(wav[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(wav[28:32], uint32(sampleRate*2)) // byte rate
	binary.LittleEndian.PutUint16(wav[32:34], 2)                    // block align
	binary.LittleEndian.PutUint16(wav[34:36], 16)                   // bits per sample
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(len(pcm)))

	copy(wav[44:], pcm)
	return wav
}
