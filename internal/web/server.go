// Package web serves clark's single-page console: state, chat, logs, and voice.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/heyimteee/clark/internal/alert"
	"github.com/heyimteee/clark/internal/assistant"
	"github.com/heyimteee/clark/internal/calendar"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/scheduler"
	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/voice"
)

const (
	defaultSessionTTL = 12 * time.Hour
	// defaultSessionMaxLife caps a session's total lifetime regardless of
	// activity. The sliding TTL alone let an actively used token live forever;
	// the absolute cap bounds the blast radius of a leaked token (#59).
	defaultSessionMaxLife = 24 * time.Hour
	wsAuthDeadline        = 10 * time.Second
	webJID                = "web"
)

// Options wires the web console to the services it drives.
type Options struct {
	ListenAddr     string
	WebToken       string
	AlertToken     string
	Butler         *assistant.Service
	Store          *store.Store
	Voice          *voice.Engine
	STTModel       string
	TTSEngine      string
	AffirmationDir string
	SessionTTL     time.Duration
	SessionMaxLife time.Duration
	Alerts         *alert.Service
	Scheduler      *scheduler.Scheduler
	Calendar       calendar.Client
}

// Server owns the HTTP handlers, sessions, and the voice engine.
type Server struct {
	mux          *http.ServeMux
	webToken     string
	alertToken   string
	butler       *assistant.Service
	store        *store.Store
	voice        *voice.Engine
	sttModel     string
	ttsEngine    string
	affirmations string
	listen       string
	alerts       *alert.Service
	sched        *scheduler.Scheduler
	cal          calendar.Client

	sessions *sessionManager
	logins   *loginThrottle
	hub      *chatHub

	// notifyLimiter bounds alert-webhook bursts; sttSlots caps concurrent
	// whisper transcriptions so CPU-bound STT cannot starve the box (#60).
	notifyLimiter *tokenBucket
	sttSlots      chan struct{}
}

// sttMaxConcurrency is how many transcriptions may run at once.
const sttMaxConcurrency = 2

// New builds the console server (nothing listens yet; app wires the listener).
func New(opts Options) *Server {
	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	maxLife := opts.SessionMaxLife
	if maxLife <= 0 {
		maxLife = defaultSessionMaxLife
	}
	s := &Server{
		mux:           http.NewServeMux(),
		webToken:      opts.WebToken,
		alertToken:    opts.AlertToken,
		butler:        opts.Butler,
		store:         opts.Store,
		voice:         opts.Voice,
		sttModel:      opts.STTModel,
		ttsEngine:     opts.TTSEngine,
		affirmations:  opts.AffirmationDir,
		listen:        opts.ListenAddr,
		alerts:        opts.Alerts,
		sched:         opts.Scheduler,
		cal:           opts.Calendar,
		sessions:      newSessionManager(ttl, maxLife),
		logins:        newLoginThrottle(),
		hub:           newChatHub(),
		notifyLimiter: newTokenBucket(notifyPerMinute, notifyBurst),
		sttSlots:      make(chan struct{}, sttMaxConcurrency),
	}
	if s.alerts != nil {
		// Wire the console chat broadcast into the shared alert service so any
		// alert (bypass command, monitoring webhook) reaches the web chat.
		s.alerts.SetBroadcast(s.broadcastChatAlert)
	}

	if s.butler != nil {
		// Push live state to every open console the instant any setting
		// changes (status, context, thinking, alert mode, VIPs, access) — via
		// the existing chat hub — so the dashboard reflects changes in
		// real-time instead of waiting for the 5s poll. Broadcast async so a
		// slow WS write can never block the mutating goroutine (e.g. WhatsApp).
		s.butler.Subscribe(func() {
			go s.hub.broadcast(map[string]any{"type": "state", "state": s.state()})
		})
	}

	s.mux.HandleFunc("POST /web/api/login", s.handleLogin)
	s.mux.HandleFunc("POST /web/api/logout", s.requireAuth(s.handleLogout))

	s.mux.HandleFunc("GET /web/api/state", s.requireAuth(s.handleState))
	s.mux.HandleFunc("GET /web/api/history", s.requireAuth(s.handleHistory))
	s.mux.HandleFunc("GET /web/api/todos", s.requireAuth(s.handleTodos))
	s.mux.HandleFunc("POST /web/api/todos", s.requireAuth(s.handleTodos))
	s.mux.HandleFunc("/web/api/todos/", s.requireAuth(s.handleTodoAction))
	s.mux.HandleFunc("GET /web/api/protocols", s.requireAuth(s.handleProtocols))
	s.mux.HandleFunc("POST /web/api/protocols", s.requireAuth(s.handleProtocols))
	s.mux.HandleFunc("/web/api/protocols/", s.requireAuth(s.handleProtocolAction))
	s.mux.HandleFunc("GET /web/api/schedules", s.requireAuth(s.handleSchedules))
	s.mux.HandleFunc("POST /web/api/schedules", s.requireAuth(s.handleSchedules))
	s.mux.HandleFunc("/web/api/schedules/", s.requireAuth(s.handleScheduleAction))
	s.mux.HandleFunc("GET /web/api/calendar", s.requireAuth(s.handleCalendarEvents))
	s.mux.HandleFunc("POST /web/api/calendar/events", s.requireAuth(s.handleCalendarAdd))
	s.mux.HandleFunc("GET /web/api/voice", s.requireAuth(s.handleVoiceStatus))

	s.mux.HandleFunc("POST /web/api/status", s.requireAuth(s.handleSetStatus))
	s.mux.HandleFunc("POST /web/api/kill", s.requireAuth(s.handleKill))
	s.mux.HandleFunc("POST /web/api/thinking", s.requireAuth(s.handleSetThinking))
	s.mux.HandleFunc("POST /web/api/alert-mode", s.requireAuth(s.handleSetAlertMode))
	s.mux.HandleFunc("POST /web/api/history-limit", s.requireAuth(s.handleSetHistoryLimit))
	s.mux.HandleFunc("POST /web/api/context", s.requireAuth(s.handleSetContext))
	s.mux.HandleFunc("POST /web/api/vip/add", s.requireAuth(s.handleAddVIP))
	s.mux.HandleFunc("POST /web/api/vip/add-bulk", s.requireAuth(s.handleAddVIPBulk))
	s.mux.HandleFunc("POST /web/api/vip/delete", s.requireAuth(s.handleDeleteVIP))
	s.mux.HandleFunc("POST /web/api/vip/status", s.requireAuth(s.handleSetVIPStatus))
	s.mux.HandleFunc("POST /web/api/access", s.requireAuth(s.handleSetAccess))
	s.mux.HandleFunc("POST /web/api/history/clear", s.requireAuth(s.handleClearHistory))
	s.mux.HandleFunc("POST /web/api/send", s.requireAuth(s.handleSend))

	s.mux.HandleFunc("POST /web/api/notify", s.handleNotify)

	s.mux.HandleFunc("POST /web/api/tts", s.requireAuth(s.handleTTS))
	s.mux.HandleFunc("POST /web/api/speech", s.requireAuth(s.handleSpeech))
	s.mux.HandleFunc("POST /web/api/stt", s.requireAuth(s.handleSTT))

	s.mux.HandleFunc("GET /web/api/chat", s.handleChatWS)
	s.mux.HandleFunc("GET /web/api/logs", s.handleLogsWS)

	s.mux.HandleFunc("/web/api/", s.handleAPI404)
	s.mux.Handle("/web/static/", http.StripPrefix("/web/static/", http.FileServer(http.FS(staticSubFS))))
	if s.affirmations != "" {
		// Pre-rendered voice clips (wake-word affirmations, "Processing, Sir.").
		// Served like static assets: tiny, non-sensitive, cacheable. Files only
		// — directory listings are disabled (#59).
		s.mux.Handle("GET /web/affirmations/", http.StripPrefix("/web/affirmations/", noListingFileServer(http.Dir(s.affirmations))))
	}
	s.mux.HandleFunc("/web/", s.handleSPA)

	// Security boundary (#57): the iMessage bridge API is NOT mounted here.
	// It runs on its own listener (IMESSAGE_LISTEN_ADDR) so the public console
	// ingress can never reach /inbound, /outbound, or /ack.
	return s
}

// Handler returns the router the app mounts behind its root mux, wrapped with
// the security-header and audit middleware (#59).
func (s *Server) Handler() http.Handler {
	return withSecurityHeaders(withMutationAudit(s.mux))
}

// securityHeaders are set on every response. The console is a privileged
// single-admin surface: a strict CSP plus frame/mime/referrer hardening give
// defense in depth on top of output escaping.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"media-src 'self'; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'none'; "+
				"frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// HSTS only makes sense when TLS actually terminated in front of us;
		// setting it over plain http would poison the browser for LAN hosts.
		if r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// withMutationAudit logs every state-changing API call with method, path,
// status, and source address — the audit trail for who did what, from where.
func withMutationAudit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead ||
			!strings.HasPrefix(r.URL.Path, "/web/api/") {
			next.ServeHTTP(w, r)
			return
		}
		rec := &auditRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logging.Log("WEB", logging.SevNotice, "MUTATION", "API mutation served",
			"method", r.Method, "path", r.URL.Path, "status", rec.status, "source", clientIP(r))
	})
}

// auditRecorder captures the response status for the audit log line.
type auditRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (a *auditRecorder) WriteHeader(code int) {
	if !a.wroteHeader {
		a.status = code
		a.wroteHeader = true
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *auditRecorder) Write(b []byte) (int, error) {
	if !a.wroteHeader {
		a.wroteHeader = true
	}
	return a.ResponseWriter.Write(b)
}

// ListenerAddr is the address app should listen on for this console.
func (s *Server) ListenerAddr() string { return s.listen }

// Run serves the console (and the optional bridge, mounted inside the same
// mux) until ctx is done, mirroring imessage.Run's graceful-shutdown pattern.
func Run(ctx context.Context, opts Options) error {
	s := New(opts)
	listen := opts.ListenAddr
	if listen == "" {
		listen = ":8090"
	}
	srv := &http.Server{
		Addr:              listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	if pprofEnabled() {
		// Diagnostics only: bound to loopback so it is unreachable from any
		// network interface (#66). Never expose this through a proxy.
		go func() {
			logging.Log("WEB", logging.SevNotice, "PPROF", "pprof listening", "addr", "127.0.0.1:6060")
			_ = http.ListenAndServe("127.0.0.1:6060", nil)
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		logging.Log("WEB", logging.SevNotice, "SERVER", "Console listening", "addr", listen)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		logging.Log("WEB", logging.SevInfo, "SERVER", "Console stopped")
		return nil
	}
}

// sessionPruneThreshold is the map size that triggers an eager purge of
// expired sessions on the next issue().
const sessionPruneThreshold = 4096

// sessionManager issues opaque bearer tokens with a sliding TTL and an
// absolute lifetime cap.
type sessionManager struct {
	mu      sync.Mutex
	ttl     time.Duration
	maxLife time.Duration
	// expiry is the sliding-TTL deadline, extended on every valid use.
	expiry map[string]time.Time
	// created is the issue time; a token is dead once maxLife has passed
	// regardless of activity.
	created map[string]time.Time
}

func newSessionManager(ttl, maxLife time.Duration) *sessionManager {
	return &sessionManager{
		ttl:     ttl,
		maxLife: maxLife,
		expiry:  make(map[string]time.Time),
		created: make(map[string]time.Time),
	}
}

func (sm *sessionManager) issue() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("web: crypto/rand failed: %v", err))
	}
	tok := hex.EncodeToString(buf)
	now := time.Now()
	sm.mu.Lock()
	if len(sm.expiry) > sessionPruneThreshold {
		for k, created := range sm.created {
			if now.Sub(created) >= sm.maxLife || now.After(sm.expiry[k]) {
				delete(sm.expiry, k)
				delete(sm.created, k)
			}
		}
	}
	sm.expiry[tok] = now.Add(sm.ttl)
	sm.created[tok] = now
	sm.mu.Unlock()
	return tok
}

func (sm *sessionManager) valid(tok string) bool {
	if tok == "" {
		return false
	}
	now := time.Now()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	exp, ok := sm.expiry[tok]
	if !ok {
		return false
	}
	// Absolute lifetime wins over any amount of sliding activity (#59).
	if now.Sub(sm.created[tok]) >= sm.maxLife {
		delete(sm.expiry, tok)
		delete(sm.created, tok)
		return false
	}
	if now.After(exp) {
		delete(sm.expiry, tok)
		delete(sm.created, tok)
		return false
	}
	// Sliding TTL: every authenticated call buys another full window.
	sm.expiry[tok] = now.Add(sm.ttl)
	return true
}

// revoke invalidates the token immediately (logout).
func (sm *sessionManager) revoke(tok string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.expiry, tok)
	delete(sm.created, tok)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	src := clientIP(r)
	if !s.logins.allow(src) {
		logAuthFailure(src, "locked out after repeated failures")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many attempts; try again later"})
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if body.Key == "" {
		logAuthFailure(src, "missing key")
		s.logins.fail(src)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing key"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Key), []byte(s.webToken)) != 1 {
		logAuthFailure(src, "invalid key")
		s.logins.fail(src)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid key"})
		return
	}
	s.logins.reset(src)
	tok := s.sessions.issue()
	logging.Log("WEB", logging.SevInfo, "LOGIN", "Web session opened", "source", src)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      tok,
		"expires_in": int(s.sessions.ttl / time.Second),
	})
}

// handleLogout revokes the caller's session immediately so a token left in a
// browser (or copied elsewhere) stops working (#59).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.sessions.revoke(tok)
	logging.Log("WEB", logging.SevInfo, "LOGOUT", "Web session closed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !s.sessions.valid(tok) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// authToken validates a bearer token from a JSON frame (used by WS endpoints).
func (s *Server) authToken(tok string) bool {
	return s.sessions.valid(tok)
}

func (s *Server) handleAPI404(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logging.Log("WEB", logging.SevWarn, "WRITE", "Failed to encode JSON response")
	}
}

var acceptOptions = &websocket.AcceptOptions{
	OriginPatterns: []string{"*.studio.lab"},
}

// noListingFileServer serves files without directory indexes: any request
// resolving to a directory gets a 404 instead of a listing.
func noListingFileServer(root http.FileSystem) http.Handler {
	fs := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}
