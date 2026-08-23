package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/heyimteee/clark/internal/assistant"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/voice"
)

// voiceFixture writes fake whisper runner/model and piper daemon/voice files
// and returns a config pointing at them with a working TTS engine.
func voiceFixture(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()

	script := filepath.Join(dir, "run.py")
	modelDir := filepath.Join(dir, "model")
	piperDaemon := filepath.Join(dir, "daemon.py")
	piperVoice := filepath.Join(dir, "en_US-ryan-high.onnx")

	for _, p := range []string{script, piperDaemon, piperVoice} {
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
		PiperDaemon:     piperDaemon,
		PiperVoice:      piperVoice,
		KokoroVoice:     "am_michael",
	}
}

func TestBuildVoiceEngineFasterWhisperAndPiperReady(t *testing.T) {
	engine := buildVoiceEngine(voiceFixture(t))

	if engine.STT == nil {
		t.Error("STT = nil, want faster-whisper wired when runner and model exist")
	}
	if engine.TTS == nil {
		t.Error("TTS = nil, want piper wired when daemon and voice exist")
	}
	if engine.TTS.Voice() != "en_US-ryan-high" {
		t.Errorf("TTS voice = %q, want en_US-ryan-high", engine.TTS.Voice())
	}
}

func TestBuildVoiceEngineDegradesWhenPiperMissing(t *testing.T) {
	cfg := voiceFixture(t)
	cfg.PiperDaemon = "/nonexistent/daemon.py"
	cfg.PiperVoice = "/nonexistent/voice.onnx"

	engine := buildVoiceEngine(cfg)

	if engine.STT == nil {
		t.Error("STT = nil, want STT to survive a missing piper")
	}
	if engine.TTS != nil {
		t.Error("TTS = non-nil, want nil when piper daemon is missing")
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

func TestBuildTTSEngineKokoroRemoteUsesFailover(t *testing.T) {
	cfg := voiceFixture(t)
	cfg.TTSEngine = "kokoro-remote"
	cfg.TTSRemoteURL = "http://100.64.0.1:8790"
	cfg.TTSRemoteToken = "mac-secret"

	engine := buildTTSEngine(cfg)

	if _, ok := engine.(*voice.FailoverTTS); !ok {
		t.Fatalf("TTS engine = %T, want *voice.FailoverTTS (remote → piper)", engine)
	}
	if engine.Voice() != "am_michael" {
		t.Errorf("TTS voice = %q, want am_michael (remote kokoro)", engine.Voice())
	}
}

func TestBuildTTSEngineKokoroRemoteFallsBackToPiperWhenNoURL(t *testing.T) {
	cfg := voiceFixture(t)
	cfg.TTSEngine = "kokoro-remote"
	cfg.TTSRemoteURL = ""

	engine := buildTTSEngine(cfg)

	if _, ok := engine.(*voice.PiperTTS); !ok {
		t.Fatalf("TTS engine = %T, want *voice.PiperTTS (piper fallback)", engine)
	}
}

// newTestAssistant builds a Service over an in-memory store for wiring tests.
func newTestAssistant(t *testing.T, st *store.Store) *assistant.Service {
	t.Helper()
	llm := &stubLLM{}
	ast, err := assistant.New(&config.Config{DBPath: ":memory:", OllamaModel: "test-model"}, st, llm)
	if err != nil {
		t.Fatalf("assistant.New: %v", err)
	}
	return ast
}

// stubLLM satisfies assistant.LLM without touching a model server.
type stubLLM struct{}

func (s *stubLLM) SetThink(bool) {}
func (s *stubLLM) Chat(_ context.Context, _ []ollama.Message, _ []ollama.Tool) (*ollama.ChatResult, error) {
	return &ollama.ChatResult{Content: "ok"}, nil
}
func (s *stubLLM) ChatStream(ctx context.Context, m []ollama.Message, tools []ollama.Tool, fn func(string)) (*ollama.ChatResult, error) {
	return s.Chat(ctx, m, tools)
}
