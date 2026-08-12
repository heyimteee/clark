package web

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/voice"
)

// freePort grabs an ephemeral port and releases it so Run can bind it.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestRunServesConsoleAndShutsDown(t *testing.T) {
	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, Options{
			ListenAddr: addr,
			WebToken:   testWebToken,
			Butler:     newAssistant(t, testStore(t), &stubLLM{}),
			Store:      testStore(t),
			Voice:      &voice.Engine{},
		})
	}()

	url := "http://" + addr + "/web/"
	var resp *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("console never came up: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET /web/ = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	// The listener must be released after shutdown.
	if _, err := http.Get(url); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("listener still accepting after shutdown: %v", err)
	}
}

func TestRunErrorsWhenPortTaken(t *testing.T) {
	addr := freePort(t)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			ListenAddr: addr,
			WebToken:   testWebToken,
			Butler:     newAssistant(t, testStore(t), &stubLLM{}),
			Store:      testStore(t),
			Voice:      &voice.Engine{},
		})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil error with the port already bound")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not surface the bind error")
	}
}
