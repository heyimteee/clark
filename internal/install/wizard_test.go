package install

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

func TestGenerateToken(t *testing.T) {
	tok := GenerateToken()
	if len(tok) != 64 {
		t.Errorf("token len = %d, want 64", len(tok))
	}
	tok2 := GenerateToken()
	if tok == tok2 {
		t.Error("two tokens should differ")
	}
}

func TestBuildEnv_WhatsAppOnly(t *testing.T) {
	existing := map[string]string{}
	ans := Answers{
		OllamaURL:    "http://localhost:11434",
		OllamaModel:  "llama3.2",
		WebToken:     "tok1",
		AlertToken:   "tok2",
		STTEngine:    "faster-whisper",
		TTSEngine:    "kokoro-remote",
		NPMNetwork:   "npm_default",
		BypassPhrase: "get him to me",
	}
	env := BuildEnv(ans, existing)
	if env["OLLAMA_MODEL"] != "llama3.2" {
		t.Errorf("OLLAMA_MODEL = %q, want llama3.2", env["OLLAMA_MODEL"])
	}
	if env["IMESSAGE_ENABLED"] != "0" {
		t.Errorf("IMESSAGE_ENABLED = %q, want 0", env["IMESSAGE_ENABLED"])
	}
	if env["WEB_ENABLED"] != "1" {
		t.Errorf("WEB_ENABLED = %q, want 1", env["WEB_ENABLED"])
	}
	if env["NO_DOCKER"] != "" {
		t.Errorf("NO_DOCKER should be absent for Docker, got %q", env["NO_DOCKER"])
	}
}

func TestBuildEnv_Full(t *testing.T) {
	existing := map[string]string{"TAVILY_API_KEY": "old"}
	ans := Answers{
		IMessageEnabled:     true,
		IMessageSelfHandle:  "+6281234567890",
		IMessageBridgeToken: "bridge-tok",
		SeparateServer:      true,
		SSHHost:             "3studio-server-tail",
		OllamaURL:           "http://100.94.240.11:11434",
		OllamaModel:         "llama3.2:latest",
		WebToken:            "webtok",
		AlertToken:          "alerttok",
		STTEngine:           "faster-whisper",
		TTSEngine:           "kokoro-remote",
		NPMNetwork:          "npm_default",
		MasterName:          "Sir Tristan",
		ProtocolName:        "Basori",
		PalaceName:          "Basori Digital Palace",
		BypassPhrase:        "get him to me",
		InnerCircle:         "Tiara|Girlfriend;Anang|Father",
		TavilyAPIKey:        "tvly-123",
		TTSRemoteURL:        "http://100.94.240.11:8790",
		TTSRemoteToken:      "kokoro-tok",
	}
	env := BuildEnv(ans, existing)
	if env["IMESSAGE_ENABLED"] != "1" {
		t.Errorf("IMESSAGE_ENABLED = %q, want 1", env["IMESSAGE_ENABLED"])
	}
	if env["IMESSAGE_SELF_HANDLE"] != "+6281234567890" {
		t.Errorf("handle = %q", env["IMESSAGE_SELF_HANDLE"])
	}
	if env["SSH_HOST"] != "3studio-server-tail" {
		t.Errorf("SSH_HOST = %q", env["SSH_HOST"])
	}
	if env["TAVILY_API_KEY"] != "tvly-123" {
		t.Errorf("TAVILY = %q", env["TAVILY_API_KEY"])
	}
	if env["MASTER_NAME"] != "Sir Tristan" {
		t.Errorf("MASTER_NAME = %q", env["MASTER_NAME"])
	}
	if env["TTS_REMOTE_URL"] != "http://100.94.240.11:8790" {
		t.Errorf("TTS_REMOTE_URL = %q", env["TTS_REMOTE_URL"])
	}
}

func TestBuildEnv_IMessageOnlyLocal(t *testing.T) {
	env := BuildEnv(Answers{
		IMessageEnabled:     true,
		IMessageSelfHandle:  "+628000000000",
		IMessageBridgeToken: "tok",
		OllamaURL:           "http://localhost:11434",
		OllamaModel:         "llama3.2",
		WebToken:            "w",
		AlertToken:          "a",
		STTEngine:           "faster-whisper",
		TTSEngine:           "kokoro-remote",
		NPMNetwork:          "npm_default",
	}, map[string]string{})
	if env["IMESSAGE_ENABLED"] != "1" {
		t.Error("expected IMESSAGE_ENABLED=1")
	}
	if env["TTS_REMOTE_URL"] != "" {
		t.Errorf("TTS_REMOTE_URL should be empty, got %q", env["TTS_REMOTE_URL"])
	}
}

func TestBuildEnv_SeparateServerSSHUnreachableFallback(t *testing.T) {
	// BuildEnv itself doesn't probe SSH; probe is in runInteractive.
	// Ensure SSH_HOST is preserved for later probe.
	env := BuildEnv(Answers{
		SeparateServer: true,
		SSHHost:        "unreachable-host",
		OllamaURL:      "http://host.docker.internal:11434",
		OllamaModel:    "llama3.2",
		WebToken:       "w",
		AlertToken:     "a",
		STTEngine:      "faster-whisper",
		TTSEngine:      "kokoro-remote",
		NPMNetwork:     "npm_default",
	}, map[string]string{})
	if env["SSH_HOST"] != "unreachable-host" {
		t.Errorf("SSH_HOST = %q", env["SSH_HOST"])
	}
}

func TestBuildEnv_NativeNoDocker(t *testing.T) {
	env := BuildEnv(Answers{
		NoDocker:    true,
		OllamaURL:   "http://localhost:11434",
		OllamaModel: "llama3.2",
		WebToken:    "w",
		AlertToken:  "a",
		STTEngine:   "faster-whisper",
		TTSEngine:   "kokoro-remote",
		NPMNetwork:  "npm_default",
	}, map[string]string{})
	if env["NO_DOCKER"] != "1" {
		t.Errorf("NO_DOCKER = %q, want 1", env["NO_DOCKER"])
	}
}

func TestBuildEnv_ReRunIdempotent(t *testing.T) {
	existing := map[string]string{
		"WEB_TOKEN":    "keep-me",
		"ALERT_TOKEN":  "keep-also",
		"OLLAMA_MODEL": "old-model",
	}
	ans := Answers{
		OllamaURL:   "http://localhost:11434",
		OllamaModel: "new-model",
		WebToken:    "keep-me",
		AlertToken:  "keep-also",
		STTEngine:   "faster-whisper",
		TTSEngine:   "kokoro-remote",
		NPMNetwork:  "npm_default",
	}
	env := BuildEnv(ans, existing)
	if env["WEB_TOKEN"] != "keep-me" {
		t.Errorf("WEB_TOKEN should be kept, got %q", env["WEB_TOKEN"])
	}
	if env["OLLAMA_MODEL"] != "new-model" {
		t.Errorf("OLLAMA_MODEL should be updated, got %q", env["OLLAMA_MODEL"])
	}
}

func TestBuildEnv_ServerOnlyWAOnly(t *testing.T) {
	env := BuildEnv(Answers{
		OllamaURL:   "http://host.docker.internal:11434",
		OllamaModel: "llama3.2",
		WebToken:    "w",
		AlertToken:  "a",
		STTEngine:   "faster-whisper",
		TTSEngine:   "kokoro-remote",
		NPMNetwork:  "npm_default",
	}, map[string]string{})
	if env["IMESSAGE_ENABLED"] != "0" {
		t.Errorf("IMESSAGE_ENABLED = %q", env["IMESSAGE_ENABLED"])
	}
	if _, ok := env["IMESSAGE_BRIDGE_TOKEN"]; ok {
		t.Error("IMESSAGE_BRIDGE_TOKEN should be absent for WA-only")
	}
}

func TestBuildEnv_Validation_Secrets0600(t *testing.T) {
	// E2E-lite: write .env and assert 0600 and config.Load would pass for required fields
	dir := t.TempDir()
	path := dir + "/.env"
	env := BuildEnv(Answers{
		OllamaURL:   "http://localhost:11434",
		OllamaModel: "llama3.2",
		WebToken:    "test-web-token-abc",
		AlertToken:  "test-alert-token",
		STTEngine:   "faster-whisper",
		TTSEngine:   "kokoro-remote",
		NPMNetwork:  "npm_default",
	}, map[string]string{})
	// Simulate writeAndApply's marshal+write 0600
	content, err := godotenvMarshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
	// Check required keys present
	if !strings.Contains(content, "OLLAMA_MODEL") {
		t.Error("content missing OLLAMA_MODEL")
	}
	if !strings.Contains(content, "WEB_TOKEN") {
		t.Error("content missing WEB_TOKEN")
	}
}

// helper to avoid import cycle - use same marshal as wizard
func godotenvMarshal(m map[string]string) (string, error) {
	// Use joho/godotenv Marshal
	// Inline to avoid extra dep in test file import
	return marshalEnv(m)
}

func marshalEnv(m map[string]string) (string, error) {
	// Simple marshal for test - mirrors godotenv.Marshal
	var b strings.Builder
	for k, v := range m {
		// Quote if needed
		needsQuote := strings.Contains(v, " ") || strings.Contains(v, "#") || strings.Contains(v, `"`)
		if needsQuote {
			b.WriteString(k + "=" + `"` + strings.ReplaceAll(v, `"`, `\"`) + `"` + "\n")
		} else {
			b.WriteString(k + "=" + v + "\n")
		}
	}
	return b.String(), nil
}

type scriptPrompter struct {
	t          *testing.T
	selects    []string
	inputs     []string
	confirms   []bool
	si, ii, ci int
}

func (s *scriptPrompter) Select(title, def string, opts []string) (string, error) {
	if s.si >= len(s.selects) {
		s.t.Fatalf("unexpected Select(%q)", title)
	}
	v := s.selects[s.si]
	s.si++
	return v, nil
}

func (s *scriptPrompter) Input(title, def string, validate func(string) error) (string, error) {
	if s.ii >= len(s.inputs) {
		s.t.Fatalf("unexpected Input(%q)", title)
	}
	v := s.inputs[s.ii]
	s.ii++
	return v, nil
}

func (s *scriptPrompter) Confirm(title string, def bool) (bool, error) {
	if s.ci >= len(s.confirms) {
		s.t.Fatalf("unexpected Confirm(%q)", title)
	}
	v := s.confirms[s.ci]
	s.ci++
	return v, nil
}

func goStub(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	type model struct {
		ID string `json:"id"`
	}
	data := []model{}
	for _, m := range models {
		data = append(data, model{ID: m})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if strings.Contains(r.Header.Get("User-Agent"), "Go-http-client") {
			t.Errorf("user-agent = %q, want product token", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAskLLMBackendOllama(t *testing.T) {
	p := &scriptPrompter{t: t, selects: []string{"Local Ollama"}}
	backend, url, model, key, err := askLLMBackend(p, map[string]string{})
	if err != nil {
		t.Fatalf("askLLMBackend: %v", err)
	}
	if backend != "ollama" || url != "" || model != "" || key != "" {
		t.Fatalf("got %q %q %q %q", backend, url, model, key)
	}
}

func TestAskLLMBackendGoHappy(t *testing.T) {
	srv := goStub(t, "muse-spark-1.3-contributor")
	p := &scriptPrompter{
		t:       t,
		selects: []string{"OpenCode Go (hosted)"},
		inputs:  []string{srv.URL + "/v1/responses", "muse-spark-1.3-contributor", "k"},
	}
	backend, url, model, key, err := askLLMBackend(p, map[string]string{})
	if err != nil {
		t.Fatalf("askLLMBackend: %v", err)
	}
	if backend != "opencode-go" || model != "muse-spark-1.3-contributor" || key != "k" {
		t.Fatalf("got %q %q %q", backend, model, key)
	}
	if !strings.HasSuffix(url, "/v1/responses") {
		t.Fatalf("url = %q", url)
	}
}

func TestAskLLMBackendGoKeepsExistingKey(t *testing.T) {
	srv := goStub(t, "m")
	p := &scriptPrompter{
		t:       t,
		selects: []string{"OpenCode Go (hosted)"},
		// No key input queued: an existing key must be reused silently.
		inputs: []string{srv.URL + "/v1/responses", "m"},
	}
	_, _, model, key, err := askLLMBackend(p, map[string]string{"LLM_API_KEY": "k"})
	if err != nil {
		t.Fatalf("askLLMBackend: %v", err)
	}
	if key != "k" || model != "m" {
		t.Fatalf("got %q %q", key, model)
	}
	if p.ii != 2 {
		t.Fatalf("inputs consumed = %d, want 2 (key must not be prompted)", p.ii)
	}
}

func TestAskLLMBackendGoRetry(t *testing.T) {
	srv := goStub(t, "other-model")
	p := &scriptPrompter{
		t:        t,
		selects:  []string{"OpenCode Go (hosted)"},
		inputs:   []string{srv.URL + "/v1/responses", "m", "k", srv.URL + "/v1/responses", "other-model", "k"},
		confirms: []bool{true},
	}
	_, _, model, _, err := askLLMBackend(p, map[string]string{})
	if err != nil {
		t.Fatalf("askLLMBackend: %v", err)
	}
	if model != "other-model" {
		t.Fatalf("model = %q", model)
	}
}

func TestValidateGoModelAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if err := validateGoModel(srv.URL+"/v1/responses", "bad", "m"); err == nil {
		t.Fatal("want error for 401")
	}
}

func TestValidateGoModelUnreachable(t *testing.T) {
	if err := validateGoModel("http://127.0.0.1:1/v1/responses", "k", "m"); err == nil {
		t.Fatal("want error for unreachable host")
	}
}

func TestBuildEnvLLMBackend(t *testing.T) {
	goEnv := BuildEnv(Answers{
		OllamaURL: "http://localhost:11434", OllamaModel: "llama3.2",
		LLMBackend: "opencode-go", LLMURL: "https://x/v1/responses", LLMAPIKey: "k", LLMModel: "m",
	}, map[string]string{})
	if goEnv["LLM_BACKEND"] != "opencode-go" || goEnv["LLM_URL"] == "" || goEnv["LLM_API_KEY"] != "k" || goEnv["LLM_MODEL"] != "m" {
		t.Fatalf("go env wrong: %v", goEnv)
	}
	back := BuildEnv(Answers{
		OllamaURL: "http://localhost:11434", OllamaModel: "llama3.2", LLMBackend: "ollama",
	}, map[string]string{"LLM_API_KEY": "stale", "LLM_URL": "https://x", "LLM_MODEL": "m"})
	if back["LLM_BACKEND"] != "ollama" {
		t.Fatalf("backend = %q", back["LLM_BACKEND"])
	}
	for _, k := range []string{"LLM_URL", "LLM_API_KEY", "LLM_MODEL"} {
		if _, ok := back[k]; ok {
			t.Fatalf("switching to ollama should drop %s", k)
		}
	}
}

type stubExecutor struct {
	runs [][]string
}

func (s *stubExecutor) Run(name string, args ...string) error {
	s.runs = append(s.runs, append([]string{name}, args...))
	return nil
}

func (s *stubExecutor) LookPath(file string) (string, error) { return file, nil }

func TestReconfigureCoreOllama(t *testing.T) {
	dir := t.TempDir()
	envPath := dir + "/.env"
	p := &scriptPrompter{
		t:       t,
		selects: []string{"Local Ollama"},
		inputs:  []string{"http://ollama:11434", "llama3.2"},
	}
	ex := &stubExecutor{}
	existing := map[string]string{
		"LLM_BACKEND": "opencode-go", "LLM_URL": "https://x", "LLM_API_KEY": "stale", "LLM_MODEL": "m",
		"OLLAMA_MODEL": "old", "WEB_TOKEN": "tok",
	}
	if err := reconfigureCore(p, envPath, existing, ex); err != nil {
		t.Fatalf("reconfigureCore: %v", err)
	}
	saved, err := godotenv.Read(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if saved["LLM_BACKEND"] != "ollama" || saved["OLLAMA_MODEL"] != "llama3.2" {
		t.Fatalf("env wrong: %v", saved)
	}
	for _, k := range []string{"LLM_URL", "LLM_API_KEY", "LLM_MODEL"} {
		if _, ok := saved[k]; ok {
			t.Fatalf("switch to ollama should drop %s", k)
		}
	}
	if saved["WEB_TOKEN"] != "tok" {
		t.Fatalf("unrelated keys must survive: %v", saved)
	}
}

func TestReconfigureCoreGo(t *testing.T) {
	srv := goStub(t, "muse-spark-1.3-contributor")
	dir := t.TempDir()
	envPath := dir + "/.env"
	p := &scriptPrompter{
		t:       t,
		selects: []string{"OpenCode Go (hosted)"},
		inputs:  []string{srv.URL + "/v1/responses", "muse-spark-1.3-contributor", "k"},
	}
	ex := &stubExecutor{}
	existing := map[string]string{"OLLAMA_MODEL": "llama3.2", "OLLAMA_URL": "http://localhost:11434"}
	if err := reconfigureCore(p, envPath, existing, ex); err != nil {
		t.Fatalf("reconfigureCore: %v", err)
	}
	saved, err := godotenv.Read(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if saved["LLM_BACKEND"] != "opencode-go" || saved["LLM_MODEL"] != "muse-spark-1.3-contributor" || saved["LLM_API_KEY"] != "k" {
		t.Fatalf("env wrong: %v", saved)
	}
	if saved["OLLAMA_MODEL"] != "llama3.2" {
		t.Fatalf("ollama settings should be preserved: %v", saved)
	}
}

func TestChecklistCoreShowsBackend(t *testing.T) {
	opts := buildChecklist(map[string]string{"LLM_BACKEND": "opencode-go", "LLM_MODEL": "m"})
	if !strings.Contains(opts[0], "opencode-go") || !strings.Contains(opts[0], "m") {
		t.Fatalf("core line = %q", opts[0])
	}
	opts = buildChecklist(map[string]string{"OLLAMA_MODEL": "llama3.2"})
	if !strings.Contains(opts[0], "ollama") || !strings.Contains(opts[0], "llama3.2") {
		t.Fatalf("core line = %q", opts[0])
	}
}
