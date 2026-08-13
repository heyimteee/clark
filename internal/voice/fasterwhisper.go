package voice

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// FasterWhisper transcribes audio with the faster-whisper python package, run
// through a small runner script. The model is baked into the image at build
// time, so each call loads from disk and never phones home.
type FasterWhisper struct {
	scriptPath string
	modelDir   string
}

// NewFasterWhisper returns an STT engine that runs the python runner at
// scriptPath with the faster-whisper model at modelDir.
func NewFasterWhisper(scriptPath, modelDir string) *FasterWhisper {
	return &FasterWhisper{scriptPath: scriptPath, modelDir: modelDir}
}

// Transcribe pipes the WAV audio into the runner on stdin and returns the
// transcript written to stdout.
func (w *FasterWhisper) Transcribe(ctx context.Context, audioWAV []byte) (string, error) {
	if len(audioWAV) == 0 {
		return "", fmt.Errorf("empty audio")
	}

	cmd := execCommand(ctx, "python3", w.scriptPath, w.modelDir)
	cmd.Stdin = bytes.NewReader(audioWAV)

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("faster-whisper failed: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("empty transcription")
	}
	return text, nil
}
