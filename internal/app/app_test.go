package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/heyimteee/clark/internal/config"
)

func TestBuildVoiceEnginePiperReady(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "piper")
	voiceFile := filepath.Join(dir, "en_US-amy-medium.onnx")
	for _, p := range []string{bin, voiceFile} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	cfg := &config.Config{
		OllamaURL:  "http://ollama:11434",
		STTModel:   "whisper-turbo",
		TTSEngine:  "piper",
		PiperBin:   bin,
		PiperVoice: voiceFile,
	}
	engine := buildVoiceEngine(cfg)

	if engine.STT == nil {
		t.Error("STT = nil, want OllamaWhisper always wired")
	}
	if engine.TTS == nil {
		t.Error("TTS = nil, want piper wired when binary and voice exist")
	}
	if engine.TTS.Voice() != "en_US-amy-medium" {
		t.Errorf("TTS voice = %q, want en_US-amy-medium", engine.TTS.Voice())
	}
}

func TestBuildVoiceEngineDegradesWhenPiperMissing(t *testing.T) {
	cfg := &config.Config{
		OllamaURL:  "http://ollama:11434",
		STTModel:   "whisper-turbo",
		TTSEngine:  "piper",
		PiperBin:   "/nonexistent/piper",
		PiperVoice: "/nonexistent/voice.onnx",
	}
	engine := buildVoiceEngine(cfg)

	if engine.STT == nil {
		t.Error("STT = nil, want STT to survive a missing piper")
	}
	if engine.TTS != nil {
		t.Error("TTS = non-nil, want nil when piper binary is missing")
	}
}

func TestBuildVoiceEngineUnknownEngineDisabled(t *testing.T) {
	cfg := &config.Config{
		OllamaURL: "http://ollama:11434",
		STTModel:  "whisper-turbo",
		TTSEngine: "bark",
	}
	engine := buildVoiceEngine(cfg)

	if engine.STT == nil {
		t.Error("STT = nil, want STT wired regardless of TTS engine")
	}
	if engine.TTS != nil {
		t.Error("TTS = non-nil, want nil for an uninstalled engine")
	}
}
