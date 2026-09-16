package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/health"
	"github.com/heyimteee/clark/internal/imessage"
	"github.com/heyimteee/clark/internal/store"
)

type stubOutboundStore struct{}

func (stubOutboundStore) EnqueueIMessage(_, _ string) (int64, error) { return 0, nil }
func (stubOutboundStore) NextIMessageOutbound() (store.OutboundMessage, bool, error) {
	return store.OutboundMessage{}, false, nil
}
func (stubOutboundStore) AckIMessage(_ int64) error { return nil }
func (stubOutboundStore) StaleIMessageOutboundIDs(_ time.Duration) ([]int64, error) {
	return nil, nil
}

func TestMacBridgeCheckerDenied(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-Clark-Bridge-Token") != "tok" {
			t.Error("bridge token missing")
		}
		w.Write([]byte(`{"chat_db":"denied","calendar":"authorized","watcher":"disabled","error":"eperm"}`))
	}))
	defer ts.Close()
	a := &App{cfg: &config.Config{MacActionURL: ts.URL, MacActionToken: "tok"}}
	err := macBridgeChecker(a.cfg).Check(context.Background())
	var f *health.Failure
	if !errors.As(err, &f) || !f.Urgent || !strings.Contains(f.Hint, "Full Disk Access") {
		t.Fatalf("err = %v, want urgent FDA failure", err)
	}
}

func TestMacBridgeCheckerCalendarDenied(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"chat_db":"ok","calendar":"denied","watcher":"running"}`))
	}))
	defer ts.Close()
	a := &App{cfg: &config.Config{MacActionURL: ts.URL}}
	err := macBridgeChecker(a.cfg).Check(context.Background())
	var f *health.Failure
	if !errors.As(err, &f) || !f.Urgent || !strings.Contains(f.Hint, "Calendars") {
		t.Fatalf("err = %v, want urgent calendar failure", err)
	}
}

func TestMacBridgeCheckerHealthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"chat_db":"ok","calendar":"authorized","watcher":"running"}`))
	}))
	defer ts.Close()
	a := &App{cfg: &config.Config{MacActionURL: ts.URL}}
	if err := macBridgeChecker(a.cfg).Check(context.Background()); err != nil {
		t.Fatalf("err = %v, want healthy", err)
	}
}

func TestMacBridgeCheckerUnreachable(t *testing.T) {
	a := &App{cfg: &config.Config{MacActionURL: "http://127.0.0.1:1"}}
	err := macBridgeChecker(a.cfg).Check(context.Background())
	var f *health.Failure
	if !errors.As(err, &f) || f.Urgent {
		t.Fatalf("err = %v, want non-urgent reachability failure", err)
	}
}

func TestImessageCheckerFreshness(t *testing.T) {
	a := &App{cfg: &config.Config{}}
	if err := a.imessageChecker().Check(context.Background()); !errors.Is(err, health.ErrUnknown) {
		t.Fatalf("nil server err = %v, want ErrUnknown", err)
	}
	srv := imessage.NewServer("", "", stubOutboundStore{}, nil)
	a.imSrv.Store(srv)
	if err := a.imessageChecker().Check(context.Background()); err == nil {
		t.Fatal("never-polled err = nil, want stale failure")
	}
	req := httptest.NewRequest(http.MethodGet, "/outbound", nil)
	srv.Routes().ServeHTTP(httptest.NewRecorder(), req)
	if err := a.imessageChecker().Check(context.Background()); err != nil {
		t.Fatalf("fresh poll err = %v, want healthy", err)
	}
}

func TestWhatsappCheckerUnknownBeforeHook(t *testing.T) {
	a := &App{cfg: &config.Config{}}
	if err := a.whatsappChecker().Check(context.Background()); !errors.Is(err, health.ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
}

func TestBuildHealthCheckersSkipsUnconfigured(t *testing.T) {
	bare := &App{cfg: &config.Config{}}
	if got := len(bare.buildHealthCheckers()); got != 2 {
		t.Fatalf("bare checkers = %d, want 2 (llm + whatsapp)", got)
	}
	full := &App{cfg: &config.Config{MacActionURL: "http://x", IMessageEnabled: true, TavilyAPIKey: "k"}}
	if got := len(full.buildHealthCheckers()); got != 5 {
		t.Fatalf("full checkers = %d, want 5", got)
	}
}
