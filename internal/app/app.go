// Package app wires clark together and hosts the CLI commands.
package app

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/heyimteee/clark/internal/assistant"
	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/imessage"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/notify"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/tools"
	"github.com/heyimteee/clark/internal/voice"
	"github.com/heyimteee/clark/internal/web"
	"github.com/heyimteee/clark/internal/websearch"
	"github.com/heyimteee/clark/internal/whatsapp"
)

// App is the composition root for a clark process.
type App struct {
	cfg *config.Config
	st  *store.Store
	ast *assistant.Service
}

// New loads config, opens the store, and builds the assistant.
func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	llm := ollama.New(cfg.OllamaURL, cfg.OllamaModel)
	ast, err := assistant.New(cfg, st, llm)
	if err != nil {
		st.Close()
		return nil, err
	}

	if cfg.TavilyAPIKey != "" {
		registerWebSearchTool(ast.Tools(), websearch.New(cfg.TavilyAPIKey))
		logging.Log("CLARK", logging.SevInfo, "TOOLS", "Web search enabled", "provider", "tavily")
	} else {
		logging.Log("CLARK", logging.SevWarn, "TOOLS", "Web search disabled", "reason", "no TAVILY_API_KEY in .env")
	}

	return &App{cfg: cfg, st: st, ast: ast}, nil
}

func registerWebSearchTool(reg *tools.Registry, client *websearch.Client) {
	reg.RegisterFunc(
		"web_search",
		"Search the web for current information and return concise, sourced results. Use whenever the answer needs up-to-date facts — news, weather, prices, sports scores, or anything the Master asks to 'google', 'look up', 'find out', or 'check'.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "The search query"},
				"max_results": map[string]any{"type": "integer", "description": "How many results to fetch (default 5)"},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			query := tools.StringArg(args, "query")
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			maxResults := tools.IntArg(args, "max_results", 5)
			if maxResults < 1 || maxResults > 10 {
				maxResults = 5
			}
			results, err := client.Search(ctx, query, maxResults)
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "No results found.", nil
			}
			return websearch.Format(results), nil
		},
	)
}

// Close releases the underlying store.
func (a *App) Close() error {
	return a.st.Close()
}

// Init seeds the assistant's default settings.
func (a *App) Init() error {
	return a.ast.Init()
}

// Run starts the WhatsApp listener and, when enabled, the web console (with
// the iMessage bridge mounted inside it) or the standalone iMessage bridge
// server. All block until ctx is done; a transport error stops the others.
func (a *App) Run() error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}
	if a.ast.Context() == "" {
		return fmt.Errorf("No context yet Sir. Do 'clark ctx -c [context]' first.")
	}

	logging.Log("CLARK", logging.SevInfo, "START", "Assistant started", "name", a.ast.Name())
	logging.Log("CLARK", logging.SevInfo, "CONTEXT", "Master context loaded", "context", a.ast.Context())
	logging.Log("CLARK", logging.SevInfo, "STATUS", "Assistant status", "enabled", a.ast.Enabled())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() {
		errCh <- whatsapp.Run(ctx, whatsapp.Options{
			DBPath:       a.cfg.DBPath,
			Butler:       a.ast,
			Notifier:     a.notifier(),
			Tools:        a.ast.Tools(),
			BypassPhrase: a.cfg.BypassPhrase,
			NameToJID:    a.ast.LookupJID,
		})
	}()

	if a.cfg.WebEnabled {
		var bridge http.Handler
		if a.cfg.IMessageEnabled {
			msgr := imessage.NewMessenger(a.st, a.cfg.IMessageSelfHandle)
			handler := gateway.NewHandler("IMESSAGE", msgr, a.ast, a.notifier(), a.cfg.BypassPhrase)
			imessage.RegisterSendMessageTool(a.ast.Tools(), msgr, a.ast.LookupIMessage)
			bridge = imessage.NewServer(a.cfg.IMessageBridgeToken, a.cfg.IMessageSelfHandle, a.st, handler).Routes()
			logging.Log("IMESSAGE", logging.SevNotice, "SERVER", "Bridge routes mounted inside web console", "addr", a.cfg.IMessageListenAddr)
		}
		engine := buildVoiceEngine(a.cfg)
		// Pre-warm any resident TTS daemon (local kokoro, piper, or the
		// failover's local backup) so the model is loaded before first use.
		if w, ok := engine.TTS.(interface{ Start(context.Context) error }); ok {
			if err := w.Start(ctx); err != nil {
				logging.Log("VOICE", logging.SevWarn, "TTS", "TTS daemon pre-warm failed; will retry on demand", "error", err.Error())
			} else {
				logging.Log("VOICE", logging.SevInfo, "TTS", "TTS daemon pre-warmed")
			}
		}
		errCh <- web.Run(ctx, web.Options{
			ListenAddr:     a.cfg.IMessageListenAddr,
			WebToken:       a.cfg.WebToken,
			Butler:         a.ast,
			Store:          a.st,
			Voice:          engine,
			Bridge:         bridge,
			STTModel:       a.cfg.STTModel,
			TTSEngine:      a.cfg.TTSEngine,
			AffirmationDir: a.cfg.AffirmationDir,
		})
	} else if a.cfg.IMessageEnabled {
		logging.Log("IMESSAGE", logging.SevNotice, "SERVER", "iMessage bridge transport enabled",
			"listen", a.cfg.IMessageListenAddr)
		go func() {
			errCh <- imessage.Run(ctx, imessage.Options{
				Out:          a.st,
				Butler:       a.ast,
				Notifier:     a.notifier(),
				Tools:        a.ast.Tools(),
				SelfHandle:   a.cfg.IMessageSelfHandle,
				BypassPhrase: a.cfg.BypassPhrase,
				ListenAddr:   a.cfg.IMessageListenAddr,
				Token:        a.cfg.IMessageBridgeToken,
				NameToHandle: a.ast.LookupIMessage,
			})
		}()
	}

	err = <-errCh
	stop()
	return err
}

// buildVoiceEngine assembles the STT/TTS seam. Engines are only wired when
// their prerequisites exist, so a missing whisper model or piper degrades to
// "voice unavailable" instead of a hard crash.
func buildVoiceEngine(cfg *config.Config) *voice.Engine {
	return &voice.Engine{STT: buildSTTEngine(cfg), TTS: buildTTSEngine(cfg)}
}

func buildSTTEngine(cfg *config.Config) voice.STT {
	switch cfg.STTEngine {
	case "faster-whisper", "":
		if _, err := os.Stat(cfg.WhisperScript); err != nil {
			logging.Log("VOICE", logging.SevWarn, "STT", "Whisper runner missing; STT disabled", "script", cfg.WhisperScript)
			return nil
		}
		if _, err := os.Stat(cfg.WhisperModelDir); err != nil {
			logging.Log("VOICE", logging.SevWarn, "STT", "Whisper model missing; STT disabled", "model", cfg.WhisperModelDir)
			return nil
		}
		logging.Log("VOICE", logging.SevInfo, "STT", "faster-whisper ready", "model", cfg.WhisperModelDir)
		return voice.NewFasterWhisper(cfg.WhisperScript, cfg.WhisperModelDir)
	case "ollama":
		logging.Log("VOICE", logging.SevInfo, "STT", "Ollama whisper wired", "model", cfg.STTModel)
		return voice.NewOllamaWhisper(cfg.OllamaURL, cfg.STTModel)
	default:
		logging.Log("VOICE", logging.SevWarn, "STT", "Unknown STT engine; disabled", "engine", cfg.STTEngine)
		return nil
	}
}

func buildTTSEngine(cfg *config.Config) voice.TTS {
	switch cfg.TTSEngine {
	case "kokoro-remote":
		// Remote Kokoro on the Mac, with the local Kokoro daemon as fallback so
		// TTS never dies when the Mac is asleep or unreachable.
		local := buildLocalKokoro(cfg)
		if cfg.TTSRemoteURL == "" {
			return local
		}
		remote := voice.NewKokoroRemote(cfg.TTSRemoteURL, cfg.TTSRemoteToken, cfg.KokoroVoice)
		if local == nil {
			return remote
		}
		return voice.NewFailoverTTS(remote, local)
	case "kokoro":
		return buildLocalKokoro(cfg)
	case "piper":
		if _, err := os.Stat(cfg.PiperDaemon); err != nil {
			logging.Log("VOICE", logging.SevWarn, "TTS", "Piper daemon missing; TTS disabled", "script", cfg.PiperDaemon)
			return nil
		}
		if _, err := os.Stat(cfg.PiperVoice); err != nil {
			logging.Log("VOICE", logging.SevWarn, "TTS", "Piper voice missing; TTS disabled", "voice", cfg.PiperVoice)
			return nil
		}
		logging.Log("VOICE", logging.SevInfo, "TTS", "Piper ready", "daemon", cfg.PiperDaemon, "voice", cfg.PiperVoice)
		return voice.NewPiper(cfg.PiperDaemon, cfg.PiperVoice)
	default:
		logging.Log("VOICE", logging.SevWarn, "TTS", "Unknown TTS engine; disabled", "engine", cfg.TTSEngine)
		return nil
	}
}

func buildLocalKokoro(cfg *config.Config) voice.TTS {
	if _, err := os.Stat(cfg.KokoroDaemon); err != nil {
		logging.Log("VOICE", logging.SevWarn, "TTS", "Kokoro daemon missing; TTS disabled", "script", cfg.KokoroDaemon)
		return nil
	}
	if _, err := os.Stat(cfg.KokoroModel); err != nil {
		logging.Log("VOICE", logging.SevWarn, "TTS", "Kokoro model missing; TTS disabled", "model", cfg.KokoroModel)
		return nil
	}
	if _, err := os.Stat(cfg.KokoroVoices); err != nil {
		logging.Log("VOICE", logging.SevWarn, "TTS", "Kokoro voices missing; TTS disabled", "voices", cfg.KokoroVoices)
		return nil
	}
	logging.Log("VOICE", logging.SevInfo, "TTS", "Kokoro ready", "daemon", cfg.KokoroDaemon, "voice", cfg.KokoroVoice)
	return voice.NewKokoro(cfg.KokoroDaemon, cfg.KokoroModel, cfg.KokoroVoices, cfg.KokoroVoice)
}

// notifier picks the desktop notifier, or a silent no-op in headless
// environments (CLARK_NO_NOTIFY=1).
func (a *App) notifier() gateway.Notifier {
	if a.cfg.NoNotify {
		return notify.Silent{}
	}
	return notify.New()
}

// VIP manages the inner circle.
func (a *App) VIP(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	fs := flag.NewFlagSet("vip", flag.ContinueOnError)
	var addTarget string
	var delTarget string
	var clear bool

	fs.StringVar(&addTarget, "add", "", "Add New VIP")
	fs.StringVar(&addTarget, "a", "", "Add New VIP (shorthand)")
	fs.StringVar(&delTarget, "delete", "", "Delete VIP")
	fs.StringVar(&delTarget, "d", "", "Delete VIP (shorthand)")
	fs.BoolVar(&clear, "clear", false, "Empty the entire VIP list")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing args: %w", err)
	}

	if addTarget == "" && delTarget == "" && !clear {
		fs.Usage()
		return fmt.Errorf("empty input")
	}

	if clear {
		if addTarget != "" || delTarget != "" {
			return fmt.Errorf("cannot mix -clear with -add or -delete")
		}
		if err := a.ast.ClearVIPs(); err != nil {
			return fmt.Errorf("clearing VIP list: %w", err)
		}
		logging.Log("MEMORY", logging.SevInfo, "VIPCLEAR", "Inner circle emptied")
	}

	if addTarget != "" {
		if err := a.ast.AddVIP(addTarget); err != nil {
			return fmt.Errorf("adding new VIP: %w", err)
		}
		logging.Log("MEMORY", logging.SevInfo, "VIPADD", "Added to VIP list", "contact", addTarget)
	}

	if delTarget != "" {
		if err := a.ast.DeleteVIP(delTarget); err != nil {
			return fmt.Errorf("deleting VIP: %w", err)
		}
		logging.Log("MEMORY", logging.SevInfo, "VIPDEL", "Deleted from VIP list", "contact", delTarget)
	}

	logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "Current VIP list")
	for jid, relation := range a.ast.VIPList() {
		logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "VIP entry", "jid", jid, "relation", relation)
	}

	return nil
}

// Context updates the master context.
func (a *App) Context(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	fs := flag.NewFlagSet("ctx", flag.ContinueOnError)
	var context string
	var clear bool

	fs.StringVar(&context, "change", "", "Change Context")
	fs.StringVar(&context, "c", "", "Change Context (Shorthand)")
	fs.BoolVar(&clear, "clear", false, "Empty the master context")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if clear {
		if context != "" {
			return fmt.Errorf("cannot mix -clear with -change")
		}
		return a.ast.SetContext("")
	}

	return a.ast.SetContext(context)
}

// Toggle flips the assistant's enabled status. Bare usage flips everyone;
// -r <name|number> -set on|off sets one VIP's personal status; -all on|off
// sets everyone explicitly and clears personal statuses.
func (a *App) Toggle(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	fs := flag.NewFlagSet("toggle", flag.ContinueOnError)
	var recipient string
	var set string
	var all string

	fs.StringVar(&recipient, "recipient", "", "VIP name or number to toggle individually")
	fs.StringVar(&recipient, "r", "", "VIP name or number (shorthand)")
	fs.StringVar(&set, "set", "", "on or off")
	fs.StringVar(&all, "all", "", "Set everyone explicitly to on or off")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing args: %w", err)
	}

	if recipient != "" && all != "" {
		return fmt.Errorf("cannot mix -r with -all")
	}
	if all != "" {
		if all != "on" && all != "off" {
			return fmt.Errorf("invalid -all %q. Use 'on' or 'off'", all)
		}
		return a.ast.SetStatus(all == "on")
	}
	if recipient != "" {
		if set != "on" && set != "off" {
			return fmt.Errorf("usage: clark toggle -r <name|number> -set on|off")
		}
		return a.ast.SetVIPStatus(recipient, set == "on")
	}
	if set != "" {
		return fmt.Errorf("usage: clark toggle -set requires -r <name|number>")
	}

	return a.ast.Toggle()
}

// Think enables or disables the model's reasoning mode.
func (a *App) Think(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return fmt.Errorf("usage: clark think on|off")
	}

	return a.ast.SetThinking(args[0] == "on")
}

// History sets how many recent messages clark reviews on every turn.
func (a *App) History(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	if len(args) != 1 {
		return fmt.Errorf("usage: clark history <N>")
	}
	limit, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid history limit %q", args[0])
	}

	return a.ast.SetHistoryLimit(limit)
}

// View prints the current settings, VIP list, and per-VIP tool access.
func (a *App) View() error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	logging.Log("CLARK", logging.SevInfo, "VIEW", "Settings",
		"name", a.ast.Name(),
		"model", a.ast.Model(),
		"status", a.ast.Enabled(),
		"thinking", a.ast.Thinking(),
		"history", a.ast.HistoryLimit(),
		"context", a.ast.Context())
	logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "Current VIP list")
	for jid, name := range a.ast.VIPList() {
		grants, _, _ := a.ast.AccessFor(jid)
		logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "VIP entry", "jid", jid, "relation", name, "status", a.ast.EnabledFor(jid), "tools", grants)
	}

	return nil
}

// Access manages a VIP's granted tools.
func (a *App) Access(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	fs := flag.NewFlagSet("access", flag.ContinueOnError)
	var setAccess string
	var tool string
	var onOff string

	fs.StringVar(&setAccess, "recipient", "", "VIP name or number")
	fs.StringVar(&setAccess, "r", "", "VIP name or number (shorthand)")
	fs.StringVar(&tool, "tool", "", "Tool to grant or revoke, e.g. web_search")
	fs.StringVar(&onOff, "set", "", "on or off")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing args: %w", err)
	}

	if setAccess == "" && tool == "" && onOff == "" {
		fs.Usage()
		return fmt.Errorf("empty input. Use: clark access -r <name|number> -tool <tool> -set on|off")
	}

	enabled := onOff == "on"
	if onOff != "" && onOff != "on" && onOff != "off" {
		return fmt.Errorf("invalid -set %q. Use 'on' or 'off'", onOff)
	}

	jid, ok := a.ast.LookupJID(setAccess)
	if !ok {
		return fmt.Errorf("no VIP found matching %q", setAccess)
	}

	grants, _, err := a.ast.AccessFor(jid)
	if err != nil {
		return err
	}

	if enabled {
		found := false
		for _, g := range grants {
			if g == tool {
				found = true
				break
			}
		}
		if !found {
			grants = append(grants, tool)
		}
	} else {
		next := grants[:0]
		for _, g := range grants {
			if g != tool {
				next = append(next, g)
			}
		}
		grants = next
	}

	if err := a.ast.SetAccess(jid, grants); err != nil {
		return err
	}

	logging.Log("MEMORY", logging.SevInfo, "ACCESS", "Access updated",
		"jid", jid, "tool", tool, "enabled", enabled, "grants", grants)
	return nil
}

// Help prints the CLI usage.
func (a *App) Help() error {
	logging.Log("CLARK", logging.SevInfo, "HELP", "clark commands",
		"init", "clark init",
		"run", "clark run",
		"vip", "clark vip -a <number,name,relation> | -d <number> | -clear",
		"ctx", "clark ctx -c <context> | -clear",
		"toggle", "clark toggle | -r <name|number> -set on|off | -all on|off",
		"think", "clark think on|off",
		"history", "clark history <N>",
		"view", "clark view",
		"access", "clark access -r <name|number> -tool <tool> -set on|off")
	return nil
}
