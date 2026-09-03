package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/assistant"
	"github.com/heyimteee/clark/internal/calendar"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/tools"
	"github.com/heyimteee/clark/internal/voice"
	"github.com/heyimteee/clark/internal/websearch"
	"os"
	"strings"
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

func TestFormatCalendarEvent(t *testing.T) {
	zone := time.FixedZone("WIB", 7*3600)
	tests := []struct {
		name string
		ev   calendar.Event
		want string
	}{
		{
			name: "timed event same day",
			ev: calendar.Event{
				ID: "abc-1", Title: "Standup",
				Start: time.Date(2026, 9, 2, 9, 0, 0, 0, zone),
				End:   time.Date(2026, 9, 2, 9, 30, 0, 0, zone),
			},
			want: "- Standup [id:abc-1] | 2026-09-02 09:00 → 09:30",
		},
		{
			name: "timed event crosses midnight",
			ev: calendar.Event{
				ID: "abc-2", Title: "New year",
				Start: time.Date(2026, 12, 31, 21, 0, 0, 0, zone),
				End:   time.Date(2027, 1, 1, 2, 0, 0, 0, zone),
			},
			want: "- New year [id:abc-2] | 2026-12-31 21:00 → 2027-01-01 02:00",
		},
		{
			name: "all day event",
			ev: calendar.Event{
				ID: "abc-3", Title: "pay ram", AllDay: true,
				Start: time.Date(2026, 9, 1, 0, 0, 0, 0, zone),
				End:   time.Date(2026, 9, 1, 23, 59, 59, 0, zone),
			},
			want: "- pay ram [id:abc-3] (all day) | 2026-09-01",
		},
		{
			name: "location appended",
			ev: calendar.Event{
				ID: "abc-4", Title: "Class",
				Start:    time.Date(2026, 9, 2, 7, 0, 0, 0, zone),
				End:      time.Date(2026, 9, 2, 9, 30, 0, 0, zone),
				Location: "F3.9A",
			},
			want: "- Class [id:abc-4] | 2026-09-02 07:00 → 09:30 @ F3.9A",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatCalendarEvent(tt.ev); got != tt.want {
				t.Errorf("formatCalendarEvent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWebSearchRecordsAndRecentLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"title": "Paris", "url": "https://example.com/paris", "content": "Paris is the capital."},
			{"title": "Lyon", "url": "https://example.com/lyon", "content": "Lyon is a city."},
		}})
	}))
	defer server.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	reg := tools.NewRegistry()
	registerWebSearchTool(reg, st, websearch.NewWithEndpoint("tvly-test", server.URL))
	ctx := tools.WithSender(context.Background(), "master@master")

	out, err := reg.Execute(ctx, "web_search", []byte(`{"query":"france cities","max_results":2}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !strings.Contains(out, "recent_links") {
		t.Fatalf("result missing teaching line: %q", out)
	}
	rows, err := st.ListCitations("master@master", "", 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("recorded: %v len=%d", err, len(rows))
	}

	registerRecentLinksTool(reg, st)
	masterCtx := tools.WithMaster(tools.WithSender(context.Background(), "master@master"))
	listed, err := reg.Execute(masterCtx, "recent_links", []byte(`{}`))
	if err != nil {
		t.Fatalf("recent_links: %v", err)
	}
	if !strings.Contains(listed, "https://example.com/paris") {
		t.Fatalf("links missing: %q", listed)
	}

	// VIPs are refused.
	vipCtx := tools.WithSender(context.Background(), "someone@vip")
	if _, err := reg.Execute(vipCtx, "recent_links", []byte(`{}`)); err == nil {
		t.Fatal("recent_links should refuse non-master")
	}
}

func TestCitationAge(t *testing.T) {
	if got := citationAge(time.Now()); got != "just now" {
		t.Fatalf("now = %q", got)
	}
	if got := citationAge(time.Now().Add(-90 * time.Minute)); got != "1h ago" {
		t.Fatalf("90m = %q", got)
	}
	if got := citationAge(time.Now().Add(-30 * time.Hour)); got != "1d ago" {
		t.Fatalf("30h = %q", got)
	}
}
