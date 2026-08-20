package install

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/joho/godotenv"
)

// Config is the entry point for `clark config`.
func Config(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	var edit string
	fs.StringVar(&edit, "edit", "", "feature to reconfigure (core, persona, imessage, voice, live)")
	fs.StringVar(&edit, "e", "", "feature to reconfigure (shorthand)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if edit != "" {
		return reconfigureFeature(strings.ToLower(edit))
	}
	return interactiveConfig()
}

func interactiveConfig() error {
	envPath := ".env"
	existing, _ := godotenv.Read(envPath)
	if existing == nil {
		existing = map[string]string{}
	}
	// Build checklist view
	for {
		choices := buildChecklist(existing)
		var selected string
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Clark config — select a feature to reconfigure (current state shown)").
					Options(huh.NewOptions(choices...)...).
					Value(&selected),
			),
		)
		if err := form.Run(); err != nil {
			return err
		}
		if selected == "exit" || selected == "" {
			fmt.Println("Done.")
			return nil
		}
		if err := reconfigureFeature(selected); err != nil {
			fmt.Printf("Failed to reconfigure %s: %v\n", selected, err)
		}
		// Reload .env for next loop
		existing, _ = godotenv.Read(envPath)
		if existing == nil {
			existing = map[string]string{}
		}
	}
}

func buildChecklist(env map[string]string) []string {
	// Returns display strings; value is the feature key
	has := func(k string) bool { v, ok := env[k]; return ok && strings.TrimSpace(v) != "" }
	check := func(ok bool) string {
		if ok {
			return "✓"
		}
		return "✗"
	}
	opts := []string{
		fmt.Sprintf("%s Core (OLLAMA_MODEL, OLLAMA_URL) — %s model, %s url", check(has("OLLAMA_MODEL")), firstNonEmpty(env["OLLAMA_MODEL"], "not set"), firstNonEmpty(env["OLLAMA_URL"], "http://localhost:11434")),
		fmt.Sprintf("%s Persona (MASTER_NAME, PROTOCOL_NAME, PALACE_NAME, BYPASS_PHRASE, INNER_CIRCLE)", check(has("MASTER_NAME")||has("PROTOCOL_NAME")||has("PALACE_NAME")||has("INNER_CIRCLE"))),
		fmt.Sprintf("%s iMessage (IMESSAGE_ENABLED, bridge token, handle) — %s", check(env["IMESSAGE_ENABLED"] == "1"), map[bool]string{true: "enabled", false: "disabled"}[env["IMESSAGE_ENABLED"] == "1"]),
		fmt.Sprintf("%s Web console (WEB_TOKEN, ALERT_TOKEN) — %s", check(has("WEB_TOKEN")), map[bool]string{true: "enabled", false: "disabled"}[has("WEB_TOKEN")]),
		fmt.Sprintf("%s Voice (STT/TTS, Kokoro remote) — %s / %s", check(true), firstNonEmpty(env["STT_ENGINE"], "faster-whisper"), firstNonEmpty(env["TTS_ENGINE"], "kokoro-remote")),
		fmt.Sprintf("%s Live (status, context, think — in store, SIGHUP live)", check(true)),
		"exit — done",
	}
	// Map display to keys for huh value
	// huh needs distinct values; we use feature keys
	return opts
}

// reconfigureFeature re-enters the wizard for a single feature group.
func reconfigureFeature(feature string) error {
	// Normalize feature key from checklist display or --edit flag
	key := strings.ToLower(feature)
	// Extract feature key from display string (first word)
	if strings.Contains(key, "core") {
		key = "core"
	} else if strings.Contains(key, "persona") {
		key = "persona"
	} else if strings.Contains(key, "imessage") {
		key = "imessage"
	} else if strings.Contains(key, "web") {
		key = "web"
	} else if strings.Contains(key, "voice") {
		key = "voice"
	} else if strings.Contains(key, "live") {
		key = "live"
	}

	envPath := ".env"
	existing, _ := godotenv.Read(envPath)
	if existing == nil {
		existing = map[string]string{}
	}

	p := huhPrompter{}
	ex := osExecutor{}

	switch key {
	case "core":
		return reconfigureCore(p, envPath, existing, ex)
	case "persona":
		return reconfigurePersona(p, envPath, existing, ex)
	case "imessage":
		return reconfigureIMessage(p, envPath, existing, ex)
	case "web":
		return reconfigureWeb(p, envPath, existing, ex)
	case "voice":
		return reconfigureVoice(p, envPath, existing, ex)
	case "live":
		return reconfigureLive(p, envPath, existing, ex)
	default:
		return fmt.Errorf("unknown feature %q (try: core, persona, imessage, web, voice, live)", feature)
	}
}

func reconfigureCore(p Prompter, envPath string, existing map[string]string, ex Executor) error {
	ollamaURL, err := p.Input("Ollama URL", firstNonEmpty(existing["OLLAMA_URL"], "http://localhost:11434"), validateURL)
	if err != nil {
		return err
	}
	ollamaModel, err := p.Input("Ollama model", existing["OLLAMA_MODEL"], required("OLLAMA_MODEL"))
	if err != nil {
		return err
	}
	env := copyEnv(existing)
	env["OLLAMA_URL"] = ollamaURL
	env["OLLAMA_MODEL"] = ollamaModel
	return writeAndApplyWithRestart(envPath, env, Answers{OllamaURL: ollamaURL, OllamaModel: ollamaModel}, ex, true)
}

func reconfigurePersona(p Prompter, envPath string, existing map[string]string, ex Executor) error {
	masterName, _ := p.Input("Master name", existing["MASTER_NAME"], nil)
	protocolName, _ := p.Input("Protocol name", existing["PROTOCOL_NAME"], nil)
	palaceName, _ := p.Input("Palace name", existing["PALACE_NAME"], nil)
	bypassPhrase, _ := p.Input("Bypass phrase", firstNonEmpty(existing["BYPASS_PHRASE"], "get him to me"), nil)
	innerCircle, _ := p.Input("Inner circle", existing["INNER_CIRCLE"], nil)
	env := copyEnv(existing)
	setOrDelete(env, "MASTER_NAME", masterName)
	setOrDelete(env, "PROTOCOL_NAME", protocolName)
	setOrDelete(env, "PALACE_NAME", palaceName)
	if bypassPhrase != "" {
		env["BYPASS_PHRASE"] = bypassPhrase
	}
	setOrDelete(env, "INNER_CIRCLE", innerCircle)
	return writeAndApplyWithRestart(envPath, env, Answers{}, ex, true)
}

func reconfigureIMessage(p Prompter, envPath string, existing map[string]string, ex Executor) error {
	enabled, err := p.Confirm("Enable iMessage bridge?", existing["IMESSAGE_ENABLED"] == "1")
	if err != nil {
		return err
	}
	env := copyEnv(existing)
	if !enabled {
		env["IMESSAGE_ENABLED"] = "0"
		delete(env, "IMESSAGE_BRIDGE_TOKEN")
		delete(env, "IMESSAGE_SELF_HANDLE")
		return writeAndApplyWithRestart(envPath, env, Answers{IMessageEnabled: false}, ex, true)
	}
	selfHandle, err := p.Input("Your iMessage handle (e.g. +6281234567890)", existing["IMESSAGE_SELF_HANDLE"], nil)
	if err != nil {
		return err
	}
	bridgeToken := existing["IMESSAGE_BRIDGE_TOKEN"]
	if bridgeToken == "" {
		bridgeToken = generateToken()
	}
	bridgeToken, err = p.Input("iMessage bridge token", bridgeToken, nil)
	if err != nil {
		return err
	}
	env["IMESSAGE_ENABLED"] = "1"
	env["IMESSAGE_BRIDGE_TOKEN"] = bridgeToken
	env["IMESSAGE_SELF_HANDLE"] = selfHandle
	return writeAndApplyWithRestart(envPath, env, Answers{IMessageEnabled: true, IMessageSelfHandle: selfHandle, IMessageBridgeToken: bridgeToken}, ex, true)
}

func reconfigureWeb(p Prompter, envPath string, existing map[string]string, ex Executor) error {
	webToken := firstNonEmpty(existing["WEB_TOKEN"], generateToken())
	webTokenIn, err := p.Input("Web console token (WEB_TOKEN)", webToken, nil)
	if err != nil {
		return err
	}
	alertToken := firstNonEmpty(existing["ALERT_TOKEN"], generateToken())
	alertTokenIn, err := p.Input("Alert token (ALERT_TOKEN)", alertToken, nil)
	if err != nil {
		return err
	}
	env := copyEnv(existing)
	env["WEB_ENABLED"] = "1"
	env["WEB_TOKEN"] = webTokenIn
	env["ALERT_TOKEN"] = alertTokenIn
	return writeAndApplyWithRestart(envPath, env, Answers{WebToken: webTokenIn, AlertToken: alertTokenIn}, ex, true)
}

func reconfigureVoice(p Prompter, envPath string, existing map[string]string, ex Executor) error {
	sttEngine, err := p.Select("STT engine", firstNonEmpty(existing["STT_ENGINE"], "faster-whisper"), []string{"faster-whisper", "ollama"})
	if err != nil {
		return err
	}
	ttsEngine, err := p.Select("TTS engine", firstNonEmpty(existing["TTS_ENGINE"], "kokoro-remote"), []string{"kokoro-remote", "piper"})
	if err != nil {
		return err
	}
	env := copyEnv(existing)
	env["STT_ENGINE"] = sttEngine
	env["TTS_ENGINE"] = ttsEngine
	if ttsEngine == "kokoro-remote" {
		remoteURL, _ := p.Input("Kokoro remote URL (e.g. http://100.x:8790)", existing["TTS_REMOTE_URL"], nil)
		remoteToken, _ := p.Input("Kokoro remote token", existing["TTS_REMOTE_TOKEN"], nil)
		setOrDelete(env, "TTS_REMOTE_URL", remoteURL)
		setOrDelete(env, "TTS_REMOTE_TOKEN", remoteToken)
	}
	return writeAndApplyWithRestart(envPath, env, Answers{STTEngine: sttEngine, TTSEngine: ttsEngine}, ex, true)
}

func reconfigureLive(p Prompter, envPath string, existing map[string]string, ex Executor) error {
	// Live settings are in store, not .env — offer quick toggles via app
	fmt.Println("Live settings (store) — these apply instantly via SIGHUP. Use:")
	fmt.Println("  clark toggle          — flip global status")
	fmt.Println("  clark ctx -c \"text\"   — set context")
	fmt.Println("  clark think on|off")
	fmt.Println("  clark history <N>")
	fmt.Println("Or run the web console: https://clark.studio.lab")
	_ = p
	_ = ex
	_ = envPath
	_ = existing
	return nil
}

func copyEnv(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
func setOrDelete(m map[string]string, k, v string) {
	if strings.TrimSpace(v) == "" {
		delete(m, k)
	} else {
		m[k] = v
	}
}

func writeAndApplyWithRestart(envPath string, env map[string]string, ans Answers, ex Executor, needRestart bool) error {
	// Write .env
	content, err := godotenv.Marshal(env)
	if err != nil {
		return err
	}
	if _, err := os.Stat(envPath); err == nil {
		data, _ := os.ReadFile(envPath)
		_ = os.WriteFile(envPath+".bak", data, 0600)
	}
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (%d keys)\n", envPath, len(env))
	_ = ans
	_ = needRestart
	// Auto-restart if interactive and docker available
	if needRestart {
		fmt.Println("Restarting Clark to apply .env changes...")
		// Prefer docker compose restart if compose file exists
		if _, err := os.Stat("docker-compose.yml"); err == nil {
			_ = ex.Run("docker", "compose", "restart")
			// Also try up --build in case new env needs rebuild
			_ = ex.Run("docker", "compose", "up", "-d")
		} else {
			fmt.Println("No docker-compose.yml found — restart manually: docker compose restart")
		}
	}
	return nil
}
