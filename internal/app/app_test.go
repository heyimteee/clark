package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/heyimteee/clark/internal/config"
)

// voiceFixture writes fake whisper runner/model and piper bin/voice files and
// returns a config pointing at them.
func voiceFixture(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()

	script := filepath.Join(dir, "run.py")
	modelDir := filepath.Join(dir, "model")
	piperBin := filepath.Join(dir, "piper")
	piperVoice := filepath.Join(dir, "en_US-lessac-medium.onnx")

	for _, p := range []string{script, piperBin, piperVoice} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", modelDir, err)
	}

	return &config.Config{
		OllamaURL:       "http://ollama:11434",
		STTModel:        "whisper-turbo",
		STTEngine:       "faster-whisper",
		WhisperScript:   script,
		WhisperModelDir: modelDir,
		TTSEngine:       "piper",
		PiperBin:        piperBin,
		PiperVoice:      piperVoice,
	}
}

func TestBuildVoiceEngineFasterWhisperAndPiperReady(t *testing.T) {
	engine := buildVoiceEngine(voiceFixture(t))

	if engine.STT == nil {
		t.Error("STT = nil, want faster-whisper wired when runner and model exist")
	}
	if engine.TTS == nil {
		t.Error("TTS = nil, want piper wired when binary and voice exist")
	}
	if engine.TTS.Voice() != "en_US-lessac-medium" {
		t.Errorf("TTS voice = %q, want en_US-lessac-medium", engine.TTS.Voice())
	}
}

func TestBuildVoiceEngineDegradesWhenPiperMissing(t *testing.T) {
	cfg := voiceFixture(t)
	cfg.PiperBin = "/nonexistent/piper"
	cfg.PiperVoice = "/nonexistent/voice.onnx"

	engine := buildVoiceEngine(cfg)

	if engine.STT == nil {
		t.Error("STT = nil, want STT to survive a missing piper")
	}
	if engine.TTS != nil {
		t.Error("TTS = non-nil, want nil when piper binary is missing")
	}
}

func TestBuildVoiceEngineDegradesWhenWhisperMissing(t *testing.T) {
	cfg := voiceFixture(t)
	cfg.WhisperScript = "/nonexistent/run.py"
	cfg.WhisperModelDir = "/nonexistent/model"

	engine := buildVoiceEngine(cfg)

	if engine.STT != nil {
		t.Error("STT = non-nil, want nil when whisper runner is missing")
	}
	if engine.TTS == nil {
		t.Error("TTS = nil, want TTS to survive a missing whisper model")
	}
}

func TestBuildSTTEngineOllama(t *testing.T) {
	cfg := voiceFixture(t)
	cfg.STTEngine = "ollama"

	engine := buildVoiceEngine(cfg)

	if engine.STT == nil {
		t.Error("STT = nil, want OllamaWhisper wired for STT_ENGINE=ollama")
	}
}

func TestBuildVoiceEngineUnknownEnginesDisabled(t *testing.T) {
	cfg := voiceFixture(t)
	cfg.STTEngine = "bogus"
	cfg.TTSEngine = "bark"

	engine := buildVoiceEngine(cfg)

	if engine.STT != nil {
		t.Error("STT = non-nil, want nil for an unknown engine")
	}
	if engine.TTS != nil {
		t.Error("TTS = non-nil, want nil for an unknown engine")
	}
}
