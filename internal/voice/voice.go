// Package voice provides swappable speech-to-text and text-to-speech engines.
//
// The STT and TTS interfaces are the v5 seam: the web console drives them via
// HTTP, and future engines (e.g. BarkTTS) plug in without touching the web or
// assistant packages.
package voice

import "context"

// STT transcribes audio into text.
type STT interface {
	// Transcribe converts WAV audio bytes into a text transcript.
	Transcribe(ctx context.Context, audioWAV []byte) (string, error)
}

// TTS synthesizes speech from text.
type TTS interface {
	// Synthesize returns 16-bit PCM mono WAV bytes (Piper medium = 22.05 kHz).
	Synthesize(ctx context.Context, text string) ([]byte, error)
	// Voice returns the active voice id (for the UI).
	Voice() string
}

// Engine selects a named STT/TTS implementation. Either field may be nil;
// callers must degrade gracefully.
type Engine struct {
	STT STT
	TTS TTS
}
