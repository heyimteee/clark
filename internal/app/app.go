// Package app wires clark together and hosts the CLI commands.
package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/heyimteee/clark/internal/assistant"
	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/notify"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
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

	return &App{cfg: cfg, st: st, ast: ast}, nil
}

// Close releases the underlying store.
func (a *App) Close() error {
	return a.st.Close()
}

// Init seeds the assistant's default settings.
func (a *App) Init() error {
	return a.ast.Init()
}

// Run starts the WhatsApp listener.
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
	if !a.ast.Enabled() {
		return fmt.Errorf("Clark is not active yet Sir. Do 'clark toggle' to toggle it on.")
	}

	logging.Log("CLARK", logging.SevInfo, "START", "Assistant started", "name", a.ast.Name())
	logging.Log("CLARK", logging.SevInfo, "CONTEXT", "Master context loaded", "context", a.ast.Context())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return whatsapp.Run(ctx, whatsapp.Options{
		DBPath:   a.cfg.DBPath,
		Butler:   a.ast,
		Notifier: notify.New(),
	})
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

	fs.StringVar(&addTarget, "add", "", "Add New VIP")
	fs.StringVar(&addTarget, "a", "", "Add New VIP (shorthand)")
	fs.StringVar(&delTarget, "delete", "", "Delete VIP")
	fs.StringVar(&delTarget, "d", "", "Delete VIP (shorthand)")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing args: %w", err)
	}

	if addTarget == "" && delTarget == "" {
		fs.Usage()
		return fmt.Errorf("empty input")
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

	fs.StringVar(&context, "change", "", "Change Context")
	fs.StringVar(&context, "c", "", "Change Context (Shorthand)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	return a.ast.SetContext(context)
}

// Toggle flips the assistant's enabled status.
func (a *App) Toggle() error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	return a.ast.Toggle()
}

// View prints the current settings and VIP list.
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
		"context", a.ast.Context())
	logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "Current VIP list")
	for jid, name := range a.ast.VIPList() {
		logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "VIP entry", "jid", jid, "relation", name)
	}

	return nil
}
