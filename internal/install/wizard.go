package install

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
		}, envPath, existing, ex)
	}
	return runInteractive(p, sshHost, noDocker, envPath, existing, ex)
}

func runNonInteractive(a Answers, envPath string, existing map[string]string, ex Executor) error {
	if a.OllamaModel == "" {
		a.OllamaModel = firstNonEmpty(existing["OLLAMA_MODEL"], os.Getenv("OLLAMA_MODEL"))
	}
	if a.OllamaModel == "" {
		return fmt.Errorf("OLLAMA_MODEL is required (use --ollama-model or set in env)")
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
	ollamaURL, err := p.Input("Ollama URL", ollamaURLDef, validateURL)
	if err != nil {
		return err
	}
	ollamaModelDef := existing["OLLAMA_MODEL"]
	ollamaModel, err := p.Input("Ollama model (as shown by `ollama list`)", ollamaModelDef, required("OLLAMA_MODEL"))
	if err != nil {
		return err
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

func buildEnv(ans Answers, existing map[string]string) map[string]string {
	env := map[string]string{}
	// copy existing to preserve unknown keys, then overlay wizard answers
	for k, v := range existing {
		env[k] = v
	}
	env["OLLAMA_URL"] = ans.OllamaURL
	env["OLLAMA_MODEL"] = ans.OllamaModel
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
func BuildEnv(ans Answers, existing map[string]string) map[string]string { return buildEnv(ans, existing) }
func GenerateToken() string                                              { return generateToken() }
