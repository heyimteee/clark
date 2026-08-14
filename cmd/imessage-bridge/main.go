// Command imessage-bridge is the macOS daemon that connects clark (on the
// Debian host) to iMessage. It watches ~/Library/Messages/chat.db read-only
// for inbound messages, forwards them to clark over HTTPS, and drains clark's
// outbound queue by sending via the Messages.app AppleScript verb.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/heyimteee/clark/internal/logging"
)

// bridgeConfig is the bridge's environment-driven configuration. It does not
// reuse internal/config because the bridge runs on macOS without ollama.
type bridgeConfig struct {
	dbPath       string
	statePath    string
	ownHandle    string
	baseURL      string
	token        string
	rootCA       string
	pollInterval time.Duration
	actionAddr   string
}

func loadBridgeConfig() (bridgeConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return bridgeConfig{}, fmt.Errorf("fail to resolve home directory: %w", err)
	}

	cfg := bridgeConfig{
		dbPath:     firstNonEmpty(os.Getenv("IMESSAGE_DB_PATH"), home+"/Library/Messages/chat.db"),
		statePath:  firstNonEmpty(os.Getenv("IMESSAGE_STATE_PATH"), home+"/Library/Application Support/clark-bridge/state.json"),
		ownHandle:  os.Getenv("IMESSAGE_OWN_HANDLE"),
		baseURL:    os.Getenv("IMESSAGE_BRIDGE_URL"),
		token:      os.Getenv("IMESSAGE_BRIDGE_TOKEN"),
		rootCA:     os.Getenv("IMESSAGE_TLS_ROOTCA"),
		actionAddr: firstNonEmpty(os.Getenv("IMESSAGE_ACTION_LISTEN"), ":8791"),
	}

	interval := time.Second
	if raw := os.Getenv("IMESSAGE_POLL_INTERVAL"); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			interval = time.Duration(secs) * time.Second
		}
	}
	cfg.pollInterval = interval

	if cfg.baseURL == "" {
		return bridgeConfig{}, fmt.Errorf("IMESSAGE_BRIDGE_URL is required, e.g. https://clark.example.com")
	}
	return cfg, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func main() {
	cfg, err := loadBridgeConfig()
	if err != nil {
		logging.Fatalf("CONFIG", "Bridge configuration error: %v", err)
	}

	logging.Log("BRIDGE", logging.SevNotice, "START", "iMessage bridge starting",
		"chat_db", cfg.dbPath, "url", cfg.baseURL)

	db, err := openChatDB(cfg.dbPath)
	if err != nil {
		logging.Fatalf("DB", "Cannot open chat.db (needs Full Disk Access): %v", err)
	}
	defer db.Close()

	ownHandle := cfg.ownHandle
	if ownHandle == "" {
		ownHandle, err = detectOwnHandle(db)
		if err != nil {
			logging.Fatalf("DB", "Cannot detect own handle: %v", err)
		}
	}
	if ownHandle == "" {
		logging.Log("BRIDGE", logging.SevWarn, "CONFIG", "No own handle known; self-chat bootstrap disabled", "hint", "set IMESSAGE_OWN_HANDLE")
	} else {
		logging.Log("BRIDGE", logging.SevInfo, "CONFIG", "Own handle resolved", "handle", ownHandle)
	}

	client, err := NewClient(cfg.baseURL, cfg.token, cfg.rootCA)
	if err != nil {
		logging.Fatalf("CLIENT", "Cannot build bridge client: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	watcher := NewWatcher(db, cfg.statePath, ownHandle, client, cfg.pollInterval)
	poller := NewPoller(client, NewSender(), cfg.pollInterval)

	errCh := make(chan error, 3)
	go func() {
		if err := watcher.Run(ctx); err != nil {
			errCh <- err
		}
	}()
	go func() {
		if err := poller.Run(ctx); err != nil {
			errCh <- err
		}
	}()
	go func() {
		if err := RunActionServer(ctx, cfg.actionAddr, cfg.token); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		if err != nil {
			stop()
			logging.Fatalf("BRIDGE", "Bridge stopped: %v", err)
		}
	case <-ctx.Done():
	}

	logging.Log("BRIDGE", logging.SevNotice, "STOP", "iMessage bridge stopped")
}
