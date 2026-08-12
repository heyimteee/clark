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
	"github.com/heyimteee/clark/internal/assistant"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/voice"
)

const (
	defaultSessionTTL = 12 * time.Hour
	webJID            = "web"
)

// Options wires the web console to the services it drives.
type Options struct {
	ListenAddr string
	WebToken   string
	Butler     *assistant.Service
	Store      *store.Store
	Bridge     http.Handler
	Voice      *voice.Engine
	STTModel   string
	TTSEngine  string
	SessionTTL time.Duration
}

// Server owns the HTTP handlers, sessions, and the voice engine.
type Server struct {
	mux       *http.ServeMux
	webToken  string
	butler    *assistant.Service
	store     *store.Store
	voice     *voice.Engine
	sttModel  string
	ttsEngine string
	listen    string

	sessions *sessionManager
}

// New builds the console server (nothing listens yet; app wires the listener).
func New(opts Options) *Server {
	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	s := &Server{
		mux:       http.NewServeMux(),
		webToken:  opts.WebToken,
		butler:    opts.Butler,
		store:     opts.Store,
		voice:     opts.Voice,
		sttModel:  opts.STTModel,
		ttsEngine: opts.TTSEngine,
		listen:    opts.ListenAddr,
		sessions:  newSessionManager(ttl),
	}

	s.mux.HandleFunc("POST /web/api/login", s.handleLogin)

	s.mux.HandleFunc("GET /web/api/state", s.requireAuth(s.handleState))
	s.mux.HandleFunc("GET /web/api/history", s.requireAuth(s.handleHistory))
	s.mux.HandleFunc("GET /web/api/voice", s.requireAuth(s.handleVoiceStatus))

	s.mux.HandleFunc("POST /web/api/status", s.requireAuth(s.handleSetStatus))
	s.mux.HandleFunc("POST /web/api/thinking", s.requireAuth(s.handleSetThinking))
	s.mux.HandleFunc("POST /web/api/history-limit", s.requireAuth(s.handleSetHistoryLimit))
	s.mux.HandleFunc("POST /web/api/context", s.requireAuth(s.handleSetContext))
	s.mux.HandleFunc("POST /web/api/vip/add", s.requireAuth(s.handleAddVIP))
	s.mux.HandleFunc("POST /web/api/vip/add-bulk", s.requireAuth(s.handleAddVIPBulk))
	s.mux.HandleFunc("POST /web/api/vip/delete", s.requireAuth(s.handleDeleteVIP))
	s.mux.HandleFunc("POST /web/api/vip/status", s.requireAuth(s.handleSetVIPStatus))
	s.mux.HandleFunc("POST /web/api/access", s.requireAuth(s.handleSetAccess))
	s.mux.HandleFunc("POST /web/api/history/clear", s.requireAuth(s.handleClearHistory))
	s.mux.HandleFunc("POST /web/api/send", s.requireAuth(s.handleSend))

	s.mux.HandleFunc("POST /web/api/tts", s.requireAuth(s.handleTTS))
	s.mux.HandleFunc("POST /web/api/speech", s.requireAuth(s.handleSpeech))
	s.mux.HandleFunc("POST /web/api/stt", s.requireAuth(s.handleSTT))

	s.mux.HandleFunc("GET /web/api/chat", s.handleChatWS)
	s.mux.HandleFunc("GET /web/api/logs", s.handleLogsWS)

	s.mux.HandleFunc("/web/api/", s.handleAPI404)
	s.mux.Handle("/web/static/", http.StripPrefix("/web/static/", http.FileServer(http.FS(staticSubFS))))
	s.mux.HandleFunc("/web/", s.handleSPA)

	if opts.Bridge != nil {
		s.mux.Handle("/", opts.Bridge)
	}
	return s
}

// Handler returns the router the app mounts behind its root mux.
func (s *Server) Handler() http.Handler { return s.mux }

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
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
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

// sessionManager issues opaque bearer tokens with a sliding TTL.
type sessionManager struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]time.Time
}

func newSessionManager(ttl time.Duration) *sessionManager {
	return &sessionManager{ttl: ttl, sessions: make(map[string]time.Time)}
}

func (sm *sessionManager) issue() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("web: crypto/rand failed: %v", err))
	}
	tok := hex.EncodeToString(buf)
	sm.mu.Lock()
	sm.sessions[tok] = time.Now().Add(sm.ttl)
	sm.mu.Unlock()
	return tok
}

func (sm *sessionManager) valid(tok string) bool {
	if tok == "" {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	exp, ok := sm.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(sm.sessions, tok)
		return false
	}
	// Sliding TTL: every authenticated call buys another full window.
	sm.sessions[tok] = time.Now().Add(sm.ttl)
	return true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if body.Key == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing key"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Key), []byte(s.webToken)) != 1 {
		logging.Log("WEB", logging.SevWarn, "LOGIN", "Failed web login attempt")
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid key"})
		return
	}
	tok := s.sessions.issue()
	logging.Log("WEB", logging.SevInfo, "LOGIN", "Web session opened")
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      tok,
		"expires_in": int(s.sessions.ttl / time.Second),
	})
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
