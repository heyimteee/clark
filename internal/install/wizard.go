package install

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/joho/godotenv"
)

// Answers holds the wizard's collected values.
type Answers struct {
	IMessageEnabled     bool
	IMessageSelfHandle  string
	IMessageBridgeToken string
	SeparateServer      bool
	SSHHost             string
	NoDocker            bool
	OllamaURL           string
	OllamaModel         string
	LLMBackend          string
	LLMURL              string
	LLMAPIKey           string
	LLMModel            string
	WebToken            string
	AlertToken          string
	TavilyAPIKey        string
	MasterName          string
	ProtocolName        string
	PalaceName          string
	BypassPhrase        string
	InnerCircle         string
	STTEngine           string
	TTSEngine           string
	TTSRemoteURL        string
	TTSRemoteToken      string
	NPMNetwork          string
}

// Prompter abstracts huh for testing.
type Prompter interface {
	Confirm(title string, def bool) (bool, error)
	Input(title, def string, validate func(string) error) (string, error)
	Select(title string, def string, opts []string) (string, error)
}

type huhPrompter struct{}

func (h huhPrompter) Confirm(title string, def bool) (bool, error) {
	var v bool = def
	err := huh.NewConfirm().Title(title).Value(&v).Run()
	return v, err
}
func (h huhPrompter) Input(title, def string, validate func(string) error) (string, error) {
	var v string = def
	f := huh.NewInput().Title(title).Value(&v)
	if validate != nil {
		f = f.Validate(validate)
	}
	err := f.Run()
	return strings.TrimSpace(v), err
}
func (h huhPrompter) Select(title string, def string, opts []string) (string, error) {
	var v string = def
	err := huh.NewSelect[string]().Title(title).Options(huh.NewOptions(opts...)...).Value(&v).Run()
	return v, err
}

// Executor abstracts os/exec for testing.
type Executor interface {
	Run(name string, args ...string) error
	LookPath(file string) (string, error)
}

type osExecutor struct{}

func (o osExecutor) Run(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}
func (o osExecutor) LookPath(file string) (string, error) { return exec.LookPath(file) }

func generateToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func required(msg string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s is required", msg)
		}
		return nil
	}
}

func validateURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("must be a valid URL (e.g. http://host:11434)")
	}
	return nil
}

// llmBackendOllama and llmBackendGo are the LLM_BACKEND values the wizard writes.
const (
	llmBackendOllama = "ollama"
	llmBackendGo     = "opencode-go"

	defaultGoURL   = "https://opencode.ai/zen/go/v1/responses"
	defaultGoModel = "muse-spark-1.3-contributor"
)

var llmBackendLabels = map[string]string{
	llmBackendOllama: "Local Ollama",
	llmBackendGo:     "OpenCode Go (hosted)",
}

// goModelsURL derives the sibling models endpoint from a Responses URL.
func goModelsURL(responsesURL string) string {
	u := strings.TrimRight(strings.TrimSpace(responsesURL), "/")
	if strings.HasSuffix(u, "/responses") {
		return strings.TrimSuffix(u, "/responses") + "/models"
	}
	return u + "/models"
}

// validateGoModel checks key + model against the live gateway: it must
// answer with a model list containing the requested id. Pure HTTP so tests
// can point it at httptest servers.
func validateGoModel(responsesURL, apiKey, model string) error {
	if strings.TrimSpace(responsesURL) == "" || strings.TrimSpace(apiKey) == "" || strings.TrimSpace(model) == "" {
		return fmt.Errorf("URL, API key, and model are all required")
	}
	req, err := http.NewRequest(http.MethodGet, goModelsURL(responsesURL), nil)
	if err != nil {
		return fmt.Errorf("bad URL: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "clark-install/1.0")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach gateway: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("gateway rejected the key (401) — check LLM_API_KEY")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned %s", resp.Status)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return fmt.Errorf("cannot read model list: %w", err)
	}
	for _, m := range list.Data {
		if m.ID == model {
			return nil
		}
	}
	return fmt.Errorf("model %q is not served by this gateway", model)
}

// askLLMBackend collects the brain configuration: backend select, branch
// prompts, and a live validation loop for Go. Existing keys are prefilled;
// an existing Go key is kept silently (never displayed) unless it fails
// validation, in which case it is re-prompted.
func askLLMBackend(p Prompter, existing map[string]string) (backend, url, model, key string, err error) {
	backendDef := firstNonEmpty(existing["LLM_BACKEND"], llmBackendOllama)
	backendLabel := llmBackendLabels[backendDef]
	if backendLabel == "" {
		backendLabel = llmBackendLabels[llmBackendOllama]
	}
	choice, err := p.Select("LLM backend (Clark's brain)", backendLabel,
		[]string{llmBackendLabels[llmBackendOllama], llmBackendLabels[llmBackendGo]})
	if err != nil {
		return "", "", "", "", err
	}
	backend = llmBackendOllama
	if choice == llmBackendLabels[llmBackendGo] {
		backend = llmBackendGo
	}
	if backend == llmBackendOllama {
		return backend, "", "", "", nil
	}
	needURL := func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("URL is required")
		}
		return validateURL(s)
	}
	key = existing["LLM_API_KEY"]
	urlDef := firstNonEmpty(existing["LLM_URL"], defaultGoURL)
	modelDef := firstNonEmpty(existing["LLM_MODEL"], defaultGoModel)
	for {
		url, err = p.Input("Go Responses endpoint", urlDef, needURL)
		if err != nil {
			return "", "", "", "", err
		}
		model, err = p.Input("Go model id", modelDef, required("model"))
		if err != nil {
			return "", "", "", "", err
		}
		if strings.TrimSpace(key) == "" {
			key, err = p.Input("Go API key", "", required("API key"))
			if err != nil {
				return "", "", "", "", err
			}
			key = strings.TrimSpace(key)
		}
		url = strings.TrimSpace(url)
		model = strings.TrimSpace(model)
		if err := validateGoModel(url, key, model); err != nil {
			fmt.Printf("Connection check failed: %v\n", err)
			retry, rerr := p.Confirm("Edit connection details?", true)
			if rerr != nil {
				return "", "", "", "", rerr
			}
			if !retry {
				return backend, url, model, key, nil
			}
			// Reprompt everything next round (a kept key may be the problem).
			urlDef, modelDef, key = url, model, ""
			continue
		}
		fmt.Printf("Connected: gateway serves %s\n", model)
		return backend, url, model, key, nil
	}
}

// Run is the entry point for `clark install`.
func Run(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	var sshHost, envPath string
	var yes, noDocker bool
	fs.StringVar(&sshHost, "ssh", "", "remote host (user@host or SSH alias) for separate server")
	fs.StringVar(&envPath, "env", ".env", "path to .env file")
	fs.BoolVar(&yes, "yes", false, "non-interactive (require --ollama-model, generate secrets)")
	fs.BoolVar(&noDocker, "no-docker", false, "run without Docker (go build)")
	// non-interactive helpers
	var ollamaModel, ollamaURL string
	fs.StringVar(&ollamaModel, "ollama-model", "", "Ollama model tag (non-interactive)")
	fs.StringVar(&ollamaURL, "ollama-url", "", "Ollama URL (non-interactive)")
	var llmBackend, llmURL, llmKey, llmModel string
	fs.StringVar(&llmBackend, "llm-backend", "", "LLM backend: ollama or opencode-go (non-interactive)")
	fs.StringVar(&llmURL, "llm-url", "", "Responses endpoint URL (non-interactive, go only)")
	fs.StringVar(&llmKey, "llm-key", "", "Go API key (non-interactive, go only)")
	fs.StringVar(&llmModel, "llm-model", "", "Go model id (non-interactive, go only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Load existing .env for prefill (best-effort)
	existing, _ := godotenv.Read(envPath)
	if existing == nil {
		existing = map[string]string{}
	}

	p := huhPrompter{}
	ex := osExecutor{}

	if yes {
		return runNonInteractive(Answers{
			SSHHost:     sshHost,
			NoDocker:    noDocker,
			OllamaModel: ollamaModel,
			OllamaURL:   ollamaURL,
			LLMBackend:  llmBackend,
			LLMURL:      llmURL,
			LLMAPIKey:   llmKey,
			LLMModel:    llmModel,
		}, envPath, existing, ex)
	}
	return runInteractive(p, sshHost, noDocker, envPath, existing, ex)
}

func runNonInteractive(a Answers, envPath string, existing map[string]string, ex Executor) error {
	if a.OllamaModel == "" {
		a.OllamaModel = firstNonEmpty(existing["OLLAMA_MODEL"], os.Getenv("OLLAMA_MODEL"))
	}
	backend := strings.ToLower(strings.TrimSpace(firstNonEmpty(a.LLMBackend, existing["LLM_BACKEND"], os.Getenv("LLM_BACKEND"), llmBackendOllama)))
	if backend != llmBackendOllama && backend != llmBackendGo {
		return fmt.Errorf("unknown --llm-backend %q: want ollama or %s", a.LLMBackend, llmBackendGo)
	}
	llmURL := firstNonEmpty(a.LLMURL, existing["LLM_URL"], os.Getenv("LLM_URL"))
	llmKey := firstNonEmpty(a.LLMAPIKey, existing["LLM_API_KEY"], os.Getenv("LLM_API_KEY"))
	llmModel := firstNonEmpty(a.LLMModel, existing["LLM_MODEL"], os.Getenv("LLM_MODEL"))
	if backend == llmBackendOllama {
		if a.OllamaModel == "" {
			return fmt.Errorf("OLLAMA_MODEL is required (use --ollama-model or set in env)")
		}
	} else if llmURL == "" || llmKey == "" || llmModel == "" {
		return fmt.Errorf("LLM_URL, LLM_API_KEY, and LLM_MODEL are required for the %s backend (flags or env)", llmBackendGo)
	}
	if a.OllamaURL == "" {
		a.OllamaURL = firstNonEmpty(existing["OLLAMA_URL"], os.Getenv("OLLAMA_URL"), "http://localhost:11434")
	}
	// Fill the rest from existing + defaults
	ans := Answers{
		NoDocker:            a.NoDocker,
		SSHHost:             a.SSHHost,
		SeparateServer:      a.SSHHost != "",
		OllamaURL:           a.OllamaURL,
		OllamaModel:         a.OllamaModel,
		LLMBackend:          backend,
		LLMURL:              llmURL,
		LLMAPIKey:           llmKey,
		LLMModel:            llmModel,
		WebToken:            firstNonEmpty(existing["WEB_TOKEN"], generateToken()),
		AlertToken:          firstNonEmpty(existing["ALERT_TOKEN"], generateToken()),
		IMessageEnabled:     existing["IMESSAGE_ENABLED"] == "1",
		STTEngine:           firstNonEmpty(existing["STT_ENGINE"], "faster-whisper"),
		TTSEngine:           firstNonEmpty(existing["TTS_ENGINE"], "kokoro-remote"),
		NPMNetwork:          firstNonEmpty(existing["NPM_NETWORK"], "npm_default"),
		MasterName:          existing["MASTER_NAME"],
		ProtocolName:        existing["PROTOCOL_NAME"],
		PalaceName:          existing["PALACE_NAME"],
		BypassPhrase:        firstNonEmpty(existing["BYPASS_PHRASE"], "get him to me"),
		InnerCircle:         existing["INNER_CIRCLE"],
		TavilyAPIKey:        existing["TAVILY_API_KEY"],
		TTSRemoteURL:        existing["TTS_REMOTE_URL"],
		TTSRemoteToken:      existing["TTS_REMOTE_TOKEN"],
		IMessageSelfHandle:  existing["IMESSAGE_SELF_HANDLE"],
		IMessageBridgeToken: existing["IMESSAGE_BRIDGE_TOKEN"],
	}
	env := buildEnv(ans, existing)
	return writeAndApply(envPath, env, ans, ex)
}

func runInteractive(p Prompter, sshFlag string, noDockerFlag bool, envPath string, existing map[string]string, ex Executor) error {
	// iMessage?
	iMessage, err := p.Confirm("Enable iMessage bridge (requires a Mac)?", existing["IMESSAGE_ENABLED"] == "1")
	if err != nil {
		return err
	}
	var selfHandle, bridgeToken string
	if iMessage {
		selfHandle, err = p.Input("Your iMessage handle (e.g. +6281234567890)", existing["IMESSAGE_SELF_HANDLE"], nil)
		if err != nil {
			return err
		}
		defBridge := existing["IMESSAGE_BRIDGE_TOKEN"]
		if defBridge == "" {
			defBridge = generateToken()
		}
		bridgeToken, err = p.Input("iMessage bridge shared token (generated if empty)", defBridge, nil)
		if err != nil {
			return err
		}
		if strings.TrimSpace(bridgeToken) == "" {
			bridgeToken = generateToken()
		}
	}

	// Separate server?
	sepDef := sshFlag != "" || existing["SSH_HOST"] != ""
	separate, err := p.Confirm("Deploy to a separate server (over SSH)?", sepDef)
	if err != nil {
		return err
	}
	sshHost := sshFlag
	if separate && sshHost == "" {
		defSSH := existing["SSH_HOST"]
		sshHost, err = p.Input("SSH host (user@host or alias, e.g. 3studio-server-tail)", defSSH, nil)
		if err != nil {
			return err
		}
		if sshHost != "" {
			// Probe quickly (best-effort); don't fail the wizard if unreachable
			_ = probeSSH(sshHost, ex)
		}
	}

	// Docker?
	noDocker := noDockerFlag
	if !noDockerFlag {
		dockerDef := existing["NO_DOCKER"] != "1"
		useDocker, err := p.Confirm("Run with Docker (recommended)?", dockerDef)
		if err != nil {
			return err
		}
		noDocker = !useDocker
	}

	ollamaURLDef := firstNonEmpty(existing["OLLAMA_URL"], "http://localhost:11434")
	if separate && sshHost != "" {
		// For remote, default to host.docker.internal won't work; hint at Tailscale
		if ollamaURLDef == "http://localhost:11434" {
			ollamaURLDef = "http://host.docker.internal:11434"
		}
	}
	backend, goURL, goModel, goKey, err := askLLMBackend(p, existing)
	if err != nil {
		return err
	}
	var ollamaURL, ollamaModel string
	if backend == llmBackendOllama {
		ollamaURL, err = p.Input("Ollama URL", ollamaURLDef, validateURL)
		if err != nil {
			return err
		}
		ollamaModelDef := existing["OLLAMA_MODEL"]
		ollamaModel, err = p.Input("Ollama model (as shown by `ollama list`)", ollamaModelDef, required("OLLAMA_MODEL"))
		if err != nil {
			return err
		}
		ollamaURL = strings.TrimSpace(ollamaURL)
		ollamaModel = strings.TrimSpace(ollamaModel)
	} else {
		// Keep existing Ollama settings so switching back is frictionless.
		ollamaURL = existing["OLLAMA_URL"]
		ollamaModel = existing["OLLAMA_MODEL"]
	}

	// Persona (optional, collapsed)
	masterName, _ := p.Input("Master name (who Clark serves) — leave empty for generic", existing["MASTER_NAME"], nil)
	protocolName, _ := p.Input("Protocol name (e.g. Basori) — empty for generic", existing["PROTOCOL_NAME"], nil)
	palaceName, _ := p.Input("Palace name — empty for generic", existing["PALACE_NAME"], nil)
	bypassPhrase, _ := p.Input("Bypass phrase (urgent alert word)", firstNonEmpty(existing["BYPASS_PHRASE"], "get him to me"), nil)
	innerCircle, _ := p.Input("Inner circle (Name|Relation;Name|Relation) — empty to skip", existing["INNER_CIRCLE"], nil)
	tavilyKey, _ := p.Input("Tavily API key for web_search — empty to skip (https://tavily.com)", existing["TAVILY_API_KEY"], nil)

	ans := Answers{
		IMessageEnabled:     iMessage,
		IMessageSelfHandle:  strings.TrimSpace(selfHandle),
		IMessageBridgeToken: strings.TrimSpace(bridgeToken),
		SeparateServer:      separate,
		SSHHost:             strings.TrimSpace(sshHost),
		NoDocker:            noDocker,
		OllamaURL:           strings.TrimSpace(ollamaURL),
		OllamaModel:         strings.TrimSpace(ollamaModel),
		LLMBackend:          backend,
		LLMURL:              goURL,
		LLMModel:            goModel,
		LLMAPIKey:           goKey,
		MasterName:          strings.TrimSpace(masterName),
		ProtocolName:        strings.TrimSpace(protocolName),
		PalaceName:          strings.TrimSpace(palaceName),
		BypassPhrase:        strings.TrimSpace(bypassPhrase),
		InnerCircle:         strings.TrimSpace(innerCircle),
		TavilyAPIKey:        strings.TrimSpace(tavilyKey),
		WebToken:            firstNonEmpty(existing["WEB_TOKEN"], generateToken()),
		AlertToken:          firstNonEmpty(existing["ALERT_TOKEN"], generateToken()),
		STTEngine:           firstNonEmpty(existing["STT_ENGINE"], "faster-whisper"),
		TTSEngine:           firstNonEmpty(existing["TTS_ENGINE"], "kokoro-remote"),
		NPMNetwork:          firstNonEmpty(existing["NPM_NETWORK"], "npm_default"),
		TTSRemoteURL:        existing["TTS_REMOTE_URL"],
		TTSRemoteToken:      existing["TTS_REMOTE_TOKEN"],
	}
	// Preserve existing TTS remote if user didn't touch it
	env := buildEnv(ans, existing)
	return writeAndApply(envPath, env, ans, ex)
}

func probeSSH(host string, ex Executor) error {
	return ex.Run("ssh", "-o", "ConnectTimeout=5", "-o", "BatchMode=yes", host, "true")
}

// applyLLMEnv writes the backend selection into env, shared by the install
// wizard and `clark config core` so credential rules cannot diverge: Go
// stores its endpoint/model/key, Ollama drops stale Go credentials.
func applyLLMEnv(env map[string]string, backend, llmURL, llmModel, llmKey, ollamaURL, ollamaModel string) {
	if backend == "" {
		backend = llmBackendOllama
	}
	env["LLM_BACKEND"] = backend
	if backend == llmBackendGo {
		env["LLM_URL"] = llmURL
		env["LLM_MODEL"] = llmModel
		if llmKey != "" {
			env["LLM_API_KEY"] = llmKey
		} else {
			delete(env, "LLM_API_KEY")
		}
		return
	}
	env["OLLAMA_URL"] = ollamaURL
	env["OLLAMA_MODEL"] = ollamaModel
	delete(env, "LLM_URL")
	delete(env, "LLM_API_KEY")
	delete(env, "LLM_MODEL")
}

func buildEnv(ans Answers, existing map[string]string) map[string]string {
	env := map[string]string{}
	// copy existing to preserve unknown keys, then overlay wizard answers
	for k, v := range existing {
		env[k] = v
	}
	env["OLLAMA_URL"] = ans.OllamaURL
	env["OLLAMA_MODEL"] = ans.OllamaModel
	applyLLMEnv(env, ans.LLMBackend, ans.LLMURL, ans.LLMModel, ans.LLMAPIKey, ans.OllamaURL, ans.OllamaModel)
	env["WEB_ENABLED"] = "1"
	env["WEB_TOKEN"] = ans.WebToken
	env["ALERT_TOKEN"] = ans.AlertToken
	env["STT_ENGINE"] = ans.STTEngine
	env["TTS_ENGINE"] = ans.TTSEngine
	env["NPM_NETWORK"] = ans.NPMNetwork
	if ans.MasterName != "" {
		env["MASTER_NAME"] = ans.MasterName
	} else {
		delete(env, "MASTER_NAME")
	}
	if ans.ProtocolName != "" {
		env["PROTOCOL_NAME"] = ans.ProtocolName
	} else {
		delete(env, "PROTOCOL_NAME")
	}
	if ans.PalaceName != "" {
		env["PALACE_NAME"] = ans.PalaceName
	} else {
		delete(env, "PALACE_NAME")
	}
	if ans.BypassPhrase != "" {
		env["BYPASS_PHRASE"] = ans.BypassPhrase
	}
	if ans.InnerCircle != "" {
		env["INNER_CIRCLE"] = ans.InnerCircle
	} else {
		delete(env, "INNER_CIRCLE")
	}
	if ans.TavilyAPIKey != "" {
		env["TAVILY_API_KEY"] = ans.TavilyAPIKey
	} else {
		delete(env, "TAVILY_API_KEY")
	}
	if ans.IMessageEnabled {
		env["IMESSAGE_ENABLED"] = "1"
		env["IMESSAGE_BRIDGE_TOKEN"] = ans.IMessageBridgeToken
		env["IMESSAGE_SELF_HANDLE"] = ans.IMessageSelfHandle
	} else {
		env["IMESSAGE_ENABLED"] = "0"
		delete(env, "IMESSAGE_BRIDGE_TOKEN")
		delete(env, "IMESSAGE_SELF_HANDLE")
	}
	if ans.TTSRemoteURL != "" {
		env["TTS_REMOTE_URL"] = ans.TTSRemoteURL
	}
	if ans.TTSRemoteToken != "" {
		env["TTS_REMOTE_TOKEN"] = ans.TTSRemoteToken
	}
	// Keep SSH host for re-entry (not consumed by config, but useful)
	if ans.SSHHost != "" {
		env["SSH_HOST"] = ans.SSHHost
	}
	if ans.NoDocker {
		env["NO_DOCKER"] = "1"
	} else {
		delete(env, "NO_DOCKER")
	}
	return env
}

func writeAndApply(envPath string, env map[string]string, ans Answers, ex Executor) error {
	// Backup existing .env
	if _, err := os.Stat(envPath); err == nil {
		data, _ := os.ReadFile(envPath)
		_ = os.WriteFile(envPath+".bak", data, 0600)
	}

	// Validate via godotenv marshal + config.Load dry-run would go here;
	// for now, write directly with 0600.
	content, err := godotenv.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal .env: %w", err)
	}
	// Ensure directory exists for SSH case? Write locally first
	dir := filepath.Dir(envPath)
	if dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (%d keys, 0600)\n", envPath, len(env))

	// Ensure npm network exists for local Docker
	if !ans.NoDocker {
		_ = ex.Run("docker", "network", "create", ans.NPMNetwork)
	}

	if ans.NoDocker {
		// Native path: go build + clark init
		fmt.Println("Building clark binary (native, no Docker)...")
		if err := ex.Run("go", "build", "-o", "clark", "."); err != nil {
			return fmt.Errorf("go build: %w", err)
		}
		if err := ex.Run("./clark", "init"); err != nil {
			return fmt.Errorf("clark init: %w", err)
		}
		fmt.Println("Done. Run ./clark run and scan the QR code in WhatsApp > Linked Devices.")
		if ans.SSHHost != "" {
			fmt.Printf("\nTo deploy to %s without Docker, scp the binary and .env:\n  scp ./clark %s:.env %s:~/clark/\n  ssh %s 'cd ~/clark && ./clark run'\n", ans.SSHHost, ans.SSHHost, ans.SSHHost, ans.SSHHost)
		}
		return nil
	}

	// Docker path
	if ans.SSHHost != "" {
		// Remote: scp .env and compose, then ssh docker compose up
		fmt.Printf("Copying .env to %s...\n", ans.SSHHost)
		if err := ex.Run("scp", envPath, ans.SSHHost+":~/clark/.env"); err != nil {
			fmt.Printf("scp failed, please manually copy .env to %s:~/clark/.env: %v\n", ans.SSHHost, err)
			fmt.Printf("Then run: ssh %s 'cd ~/clark && docker compose up -d --build'\n", ans.SSHHost)
			return nil
		}
		// Also ensure compose files exist remotely - best-effort scp
		_ = ex.Run("scp", "docker-compose.yml", ans.SSHHost+":~/clark/")
		_ = ex.Run("scp", "Dockerfile", ans.SSHHost+":~/clark/")
		fmt.Printf("Starting Clark on %s...\n", ans.SSHHost)
		if err := ex.Run("ssh", ans.SSHHost, "cd ~/clark && docker compose up -d --build"); err != nil {
			return fmt.Errorf("remote docker compose up: %w", err)
		}
		fmt.Printf("Done. Check: ssh %s 'docker ps --filter name=clark; curl -k https://clark.studio.lab/web/api/state'\n", ans.SSHHost)
		return nil
	}

	fmt.Println("Starting Clark with Docker...")
	// Ensure data dirs
	_ = os.MkdirAll("data", 0755)
	_ = os.MkdirAll("affirmations", 0755)
	if err := ex.Run("docker", "compose", "up", "-d", "--build"); err != nil {
		return err
	}
	fmt.Println("Done. Check: docker ps --filter name=clark; docker compose logs -f  (QR code on first run)")
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// For testability: export helpers
func BuildEnv(ans Answers, existing map[string]string) map[string]string {
	return buildEnv(ans, existing)
}
func GenerateToken() string { return generateToken() }
