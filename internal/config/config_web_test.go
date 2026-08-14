package config

import (
	"os"
	"testing"
)

// loadFromEnv runs Load() with a pristine environment populated from the map.
func loadFromEnv(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	// Isolate from any real .env in the working directory.
	t.Setenv("ENV_FILE_GUARD", "1")
	_ = os.Unsetenv("OLLAMA_MODEL")
	_ = os.Unsetenv("OLLAMA_URL")
	_ = os.Unsetenv("WEB_ENABLED")
	_ = os.Unsetenv("WEB_TOKEN")
	_ = os.Unsetenv("ALERT_TOKEN")
	_ = os.Unsetenv("STT_ENGINE")
	_ = os.Unsetenv("STT_MODEL")
	_ = os.Unsetenv("WHISPER_SCRIPT")
	_ = os.Unsetenv("WHISPER_MODEL_DIR")
	_ = os.Unsetenv("TTS_ENGINE")
	_ = os.Unsetenv("TTS_VOICE")
	_ = os.Unsetenv("TTS_REMOTE_URL")
	_ = os.Unsetenv("TTS_REMOTE_TOKEN")
	_ = os.Unsetenv("PIPER_DAEMON")
	_ = os.Unsetenv("PIPER_VOICE")
	_ = os.Unsetenv("KOKORO_VOICE")
	_ = os.Unsetenv("AFFIRMATIONS_DIR")
	_ = os.Unsetenv("CLARK_DB")
	_ = os.Unsetenv("IMESSAGE_ENABLED")
	_ = os.Unsetenv("IMESSAGE_LISTEN_ADDR")
	_ = os.Unsetenv("IMESSAGE_BRIDGE_TOKEN")
	_ = os.Unsetenv("IMESSAGE_SELF_HANDLE")
	for k, v := range env {
		t.Setenv(k, v)
	}
	// godotenv.Load looks for ".env" in the cwd; point it at a nonexistent dir.
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return Load()
}

func TestLoadWebDisabledDefaults(t *testing.T) {
	cfg, err := loadFromEnv(t, map[string]string{"OLLAMA_MODEL": "gemma4:cloud"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WebEnabled {
		t.Error("WebEnabled = true, want false by default")
	}
	if cfg.STTModel != "whisper-turbo" {
		t.Errorf("STTModel = %q, want whisper-turbo", cfg.STTModel)
	}
	if cfg.STTEngine != "faster-whisper" {
		t.Errorf("STTEngine = %q, want faster-whisper", cfg.STTEngine)
	}
	if cfg.WhisperScript != "/opt/whisper/run.py" {
		t.Errorf("WhisperScript = %q, want /opt/whisper/run.py", cfg.WhisperScript)
	}
	if cfg.WhisperModelDir != "/opt/whisper/model" {
		t.Errorf("WhisperModelDir = %q, want /opt/whisper/model", cfg.WhisperModelDir)
	}
	if cfg.TTSEngine != "kokoro-remote" {
		t.Errorf("TTSEngine = %q, want kokoro-remote", cfg.TTSEngine)
	}
	if cfg.TTSRemoteURL != "" || cfg.TTSRemoteToken != "" {
		t.Errorf("TTS remote defaults = %q/%q, want empty", cfg.TTSRemoteURL, cfg.TTSRemoteToken)
	}
	if cfg.TTSVoice != "en_US-ryan-high" {
		t.Errorf("TTSVoice = %q, want en_US-ryan-high", cfg.TTSVoice)
	}
	if cfg.PiperDaemon != "/opt/piper/daemon.py" {
		t.Errorf("PiperDaemon = %q, want /opt/piper/daemon.py", cfg.PiperDaemon)
	}
	if cfg.PiperVoice != "/opt/piper/voices/en_US-ryan-high.onnx" {
		t.Errorf("PiperVoice = %q, want /opt/piper/voices/en_US-ryan-high.onnx", cfg.PiperVoice)
	}
	if cfg.KokoroVoice != "am_michael" {
		t.Errorf("KokoroVoice = %q, want am_michael", cfg.KokoroVoice)
	}
	if cfg.AffirmationDir != "/opt/affirmations" {
		t.Errorf("AffirmationDir = %q, want /opt/affirmations", cfg.AffirmationDir)
	}
}

func TestLoadWebEnabledWithoutTokenFails(t *testing.T) {
	_, err := loadFromEnv(t, map[string]string{
		"OLLAMA_MODEL": "gemma4:cloud",
		"WEB_ENABLED":  "1",
	})
	if err == nil {
		t.Fatal("Load succeeded with WEB_ENABLED=1 but no WEB_TOKEN; want an error")
	}
}

func TestLoadWebEnabledWithToken(t *testing.T) {
	cfg, err := loadFromEnv(t, map[string]string{
		"OLLAMA_MODEL":      "gemma4:cloud",
		"WEB_ENABLED":       "1",
		"WEB_TOKEN":         "s3cret",
		"ALERT_TOKEN":       "alert-s3cret",
		"STT_ENGINE":        "ollama",
		"STT_MODEL":         "whisper-large-v3",
		"WHISPER_SCRIPT":    "/custom/run.py",
		"WHISPER_MODEL_DIR": "/custom/model",
		"TTS_ENGINE":        "bark",
		"TTS_VOICE":         "v2/en_US-speaker_1",
		"TTS_REMOTE_URL":    "http://100.64.0.1:8790",
		"TTS_REMOTE_TOKEN":  "mac-secret",
		"PIPER_DAEMON":      "/custom/daemon.py",
		"PIPER_VOICE":       "/custom/voices/ryan.onnx",
		"KOKORO_VOICE":      "am_eric",
		"AFFIRMATIONS_DIR":  "/custom/affirmations",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.WebEnabled {
		t.Error("WebEnabled = false, want true")
	}
	if cfg.WebToken != "s3cret" {
		t.Errorf("WebToken = %q, want s3cret", cfg.WebToken)
	}
	if cfg.AlertToken != "alert-s3cret" {
		t.Errorf("AlertToken = %q, want alert-s3cret", cfg.AlertToken)
	}
	if cfg.STTModel != "whisper-large-v3" {
		t.Errorf("STTModel = %q, want whisper-large-v3", cfg.STTModel)
	}
	if cfg.STTEngine != "ollama" {
		t.Errorf("STTEngine = %q, want ollama", cfg.STTEngine)
	}
	if cfg.WhisperScript != "/custom/run.py" {
		t.Errorf("WhisperScript = %q, want /custom/run.py", cfg.WhisperScript)
	}
	if cfg.WhisperModelDir != "/custom/model" {
		t.Errorf("WhisperModelDir = %q, want /custom/model", cfg.WhisperModelDir)
	}
	if cfg.TTSEngine != "bark" {
		t.Errorf("TTSEngine = %q, want bark", cfg.TTSEngine)
	}
	if cfg.TTSRemoteURL != "http://100.64.0.1:8790" {
		t.Errorf("TTSRemoteURL = %q, want http://100.64.0.1:8790", cfg.TTSRemoteURL)
	}
	if cfg.TTSRemoteToken != "mac-secret" {
		t.Errorf("TTSRemoteToken = %q, want mac-secret", cfg.TTSRemoteToken)
	}
	if cfg.TTSVoice != "v2/en_US-speaker_1" {
		t.Errorf("TTSVoice = %q, want v2/en_US-speaker_1", cfg.TTSVoice)
	}
	if cfg.PiperDaemon != "/custom/daemon.py" {
		t.Errorf("PiperDaemon = %q, want /custom/daemon.py", cfg.PiperDaemon)
	}
	if cfg.PiperVoice != "/custom/voices/ryan.onnx" {
		t.Errorf("PiperVoice = %q, want /custom/voices/ryan.onnx", cfg.PiperVoice)
	}
	if cfg.KokoroVoice != "am_eric" {
		t.Errorf("KokoroVoice = %q, want am_eric", cfg.KokoroVoice)
	}
	if cfg.AffirmationDir != "/custom/affirmations" {
		t.Errorf("AffirmationDir = %q, want /custom/affirmations", cfg.AffirmationDir)
	}
}

func TestLoadWebEnabledWithoutModelStillFails(t *testing.T) {
	// WEB_TOKEN present but the base OLLAMA_MODEL check still applies.
	_, err := loadFromEnv(t, map[string]string{
		"WEB_ENABLED": "1",
		"WEB_TOKEN":   "s3cret",
	})
	if err == nil {
		t.Fatal("Load succeeded without OLLAMA_MODEL; want an error")
	}
}
