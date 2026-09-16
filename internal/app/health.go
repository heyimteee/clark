package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/health"
	"github.com/heyimteee/clark/internal/llmcompat"
)

// bridgeStatus mirrors the macOS bridge GET /status shape (see
// cmd/imessage-bridge/actions.go).
type bridgeStatus struct {
	ChatDB   string `json:"chat_db"`
	Calendar string `json:"calendar"`
	Watcher  string `json:"watcher"`
	Error    string `json:"error"`
}

// macBridgeChecker probes the macOS action server: reachability plus the
// self-reported FDA and Calendar consent state. Only registered when a Mac
// action URL is configured.
func macBridgeChecker(cfg *config.Config) health.Checker {
	return health.Checker{
		Name:  "mac_bridge",
		Tools: []string{"list_calendar_events", "add_calendar_event", "delete_calendar_event", "send_imessage", "relay_to_master"},
		Check: func(ctx context.Context) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				strings.TrimRight(cfg.MacActionURL, "/")+"/status", nil)
			if err != nil {
				return health.TransientFailure("Mac bridge request failed to build", err.Error())
			}
			if cfg.MacActionToken != "" {
				req.Header.Set("X-Clark-Bridge-Token", cfg.MacActionToken)
			}
			resp, err := (&http.Client{}).Do(req)
			if err != nil {
				return health.TransientFailure("Mac bridge unreachable — Mac asleep, offline, or bridge down", err.Error())
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				return health.UrgentFailure("Mac bridge token rejected — Mac .env and server .env out of sync", "401 from bridge /status")
			}
			if resp.StatusCode != http.StatusOK {
				return health.TransientFailure("Mac bridge answered "+resp.Status, "GET /status: "+resp.Status)
			}
			var st bridgeStatus
			if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
				return health.TransientFailure("Mac bridge status unreadable", err.Error())
			}
			if st.ChatDB == "denied" {
				return health.UrgentFailure("iMessage bridge lost Full Disk Access — re-add imessage-bridge in System Settings → Privacy & Security → Full Disk Access, then restart the bridge", st.Error)
			}
			switch st.Calendar {
			case "denied", "restricted":
				return health.UrgentFailure("Calendar consent missing — choose Allow for imessage-bridge in System Settings → Privacy & Security → Calendars", "calendar: "+st.Calendar)
			case "write_only":
				return health.UrgentFailure("Calendar access is Write-Only — listing needs Full Access in System Settings → Privacy & Security → Calendars", "calendar: write_only")
			case "unknown":
				return health.TransientFailure("Calendar probe failed on the Mac", "calendar: unknown")
			}
			if st.Watcher == "disabled" && st.ChatDB == "ok" {
				return health.TransientFailure("Bridge watcher disabled though chat.db reads fine — restart the bridge", "watcher: disabled")
			}
			return nil
		},
	}
}

// llmBackendChecker pings the active chat brain without spending tokens: the
// Ollama tags endpoint for local, a TCP+TLS dial for the hosted Responses
// gateway.
func llmBackendChecker(cfg *config.Config) health.Checker {
	return health.Checker{
		Name:  "llm_backend",
		Tools: []string{"all replies"},
		Check: func(ctx context.Context) error {
			if cfg.LLMBackend == llmcompat.BackendName {
				return dialEndpoint(ctx, cfg.LLMURL, "LLM backend")
			}
			base := strings.TrimRight(cfg.OllamaURL, "/")
			if base == "" {
				base = "http://127.0.0.1:11434"
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
			if err != nil {
				return health.TransientFailure("Ollama request failed to build — check Ollama", err.Error())
			}
			resp, err := (&http.Client{}).Do(req)
			if err != nil {
				return health.TransientFailure("Ollama unreachable — restart Ollama", err.Error())
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return health.TransientFailure(fmt.Sprintf("Ollama answered %s — check Ollama", resp.Status), "GET /api/tags: "+resp.Status)
			}
			return nil
		},
	}
}

// dialEndpoint verifies host reachability (TCP, plus TLS handshake for
// https) without sending application bytes.
func dialEndpoint(ctx context.Context, rawURL, what string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return health.TransientFailure(what+" URL is invalid — check config", rawURL)
	}
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return health.TransientFailure(what+" unreachable — check network/backend", err.Error())
	}
	defer conn.Close()
	if u.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		defer tlsConn.Close()
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return health.TransientFailure(what+" TLS failed — check backend", err.Error())
		}
	}
	return nil
}

// whatsappChecker reflects the live whatsmeow connection. Unknown until the
// messenger hook fires at transport boot.
func (a *App) whatsappChecker() health.Checker {
	return health.Checker{
		Name:  "whatsapp",
		Tools: []string{"send_message", "relay_to_master"},
		Check: func(ctx context.Context) error {
			msgr := a.waMsgr.Load()
			if msgr == nil {
				return health.ErrUnknown
			}
			if !msgr.Connected() {
				return health.TransientFailure("WhatsApp client disconnected — restart Clark or re-login", "whatsmeow not connected")
			}
			return nil
		},
	}
}

// imessageChecker watches bridge /outbound poll freshness. Suppressed while
// mac_bridge is unhealthy so one root cause yields one alert.
func (a *App) imessageChecker() health.Checker {
	return health.Checker{
		Name:          "imessage_bridge",
		Tools:         []string{"send_imessage", "relay_to_master"},
		OnlyIfHealthy: "mac_bridge",
		Check: func(ctx context.Context) error {
			srv := a.imSrv.Load()
			if srv == nil {
				return health.ErrUnknown
			}
			last := srv.LastPoll()
			if last.IsZero() {
				return health.TransientFailure("Bridge never polled — starting up or bridge down", "no /outbound poll yet")
			}
			if age := time.Since(last); age > 2*time.Minute {
				return health.TransientFailure(fmt.Sprintf("No bridge poll for %s — bridge poller wedged", age.Round(time.Second)), "last poll "+last.Format(time.RFC3339))
			}
			return nil
		},
	}
}

// webSearchChecker verifies egress to the Tavily endpoint (TCP only — a real
// query would spend credits). Only registered when a key is configured,
// mirroring the web_search tool registration.
func webSearchChecker() health.Checker {
	return health.Checker{
		Name:  "web_search",
		Tools: []string{"web_search"},
		Check: func(ctx context.Context) error {
			d := &net.Dialer{}
			conn, err := d.DialContext(ctx, "tcp", "api.tavily.com:443")
			if err != nil {
				return health.TransientFailure("Web search endpoint unreachable — check network", err.Error())
			}
			conn.Close()
			return nil
		},
	}
}

// buildHealthCheckers assembles one checker per externally-dependent tool
// family, skipping families that are not configured.
func (a *App) buildHealthCheckers() []health.Checker {
	cfg := a.cfg
	checkers := []health.Checker{llmBackendChecker(cfg), a.whatsappChecker()}
	if cfg.MacActionURL != "" {
		checkers = append(checkers, macBridgeChecker(cfg))
	}
	if cfg.IMessageEnabled {
		checkers = append(checkers, a.imessageChecker())
	}
	if cfg.TavilyAPIKey != "" {
		checkers = append(checkers, webSearchChecker())
	}
	return checkers
}
