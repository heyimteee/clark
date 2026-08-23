package app

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/alert"
	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/store"
)

// TestRunConsolesStartsWebAndBridgeTogether is the regression test for the
// #57 follow-up: web.Run blocks its caller, so when it ran on the main path
// the bridge goroutine below it was never reached in combined deployments.
// Both listeners must come up concurrently.
func TestRunConsolesStartsWebAndBridgeTogether(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.InitDefaults(); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	if err := st.Set("context", "test context"); err != nil {
		t.Fatalf("set context: %v", err)
	}

	ast := newTestAssistant(t, st)

	cfg := &config.Config{
		OllamaModel:         "test-model",
		DBPath:              filepath.Join(t.TempDir(), "t.db"),
		WebEnabled:          true,
		WebToken:            "tok",
		WebListenAddr:       freePort(t),
		IMessageEnabled:     true,
		IMessageListenAddr:  freePort(t),
		IMessageBridgeToken: "bridge-tok",
		STTEngine:           "none",
		TTSEngine:           "none",
	}

	a := &App{cfg: cfg, st: st, ast: ast}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- a.runConsoles(ctx, alert.New(nil)) }()

	waitDialable(t, cfg.WebListenAddr)
	waitDialable(t, cfg.IMessageListenAddr)

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runConsoles did not stop after context cancel")
	}
}

func waitDialable(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("listener on %s never became dialable", addr)
}

// freePort returns a 127.0.0.1 address on an OS-assigned free port by binding
// and immediately closing a listener.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}
