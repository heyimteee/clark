package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/logging"
)

// ActionServer handles alert actions (FaceTime call, macOS banner) that clark
// triggers on the Mac. FaceTime has no scriptable "call" verb, so we open the
// facetime:// URL scheme; the banner uses osascript's display notification.
type ActionServer struct {
	token string
}

// NewActionServer wires the action HTTP handler. token is the shared bridge
// secret (X-Clark-Bridge-Token); empty disables auth (never in production).
func NewActionServer(token string) *ActionServer {
	return &ActionServer{token: token}
}

// Routes returns the action endpoints with auth enforced.
func (s *ActionServer) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /action", s.handleAction)
	mux.HandleFunc("GET /calendars/events", handleCalendarList)
	mux.HandleFunc("POST /calendars/events", handleCalendarCreate)
	mux.HandleFunc("DELETE /calendars/events/", handleCalendarDelete)
	return s.requireToken(mux)
}

func (s *ActionServer) requireToken(next http.Handler) http.Handler {
	if s.token == "" {
		logging.Log("ACTIONS", logging.SevWarn, "SERVER", "Action token empty; action API is unauthenticated")
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Clark-Bridge-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *ActionServer) handleAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type   string `json:"type"`
		Number string `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	switch body.Type {
	case "facetime":
		if err := triggerFaceTime(body.Number); err != nil {
			logging.Log("ACTIONS", logging.SevErr, "FACETIME", "Failed to start call", "error", err)
			http.Error(w, "facetime failed", http.StatusInternalServerError)
			return
		}
		logging.Log("ACTIONS", logging.SevNotice, "FACETIME", "FaceTime call triggered", "number", body.Number)
	case "banner":
		if err := showBanner(body.Title, body.Body); err != nil {
			logging.Log("ACTIONS", logging.SevErr, "BANNER", "Failed to show banner", "error", err)
			http.Error(w, "banner failed", http.StatusInternalServerError)
			return
		}
		logging.Log("ACTIONS", logging.SevNotice, "BANNER", "macOS banner shown", "title", body.Title)
	default:
		http.Error(w, "unknown action type", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// e164Re matches a strict +E.164 phone number: leading +, country code not
// starting with 0, 8-14 more digits, nothing else. Every FaceTime number must
// pass this before it is ever concatenated into a URL scheme.
var e164Re = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)

// validE164 reports whether number is a strict +E.164 string.
func validE164(number string) bool {
	return e164Re.MatchString(number)
}

// triggerFaceTime opens the facetime:// URL scheme, which starts a FaceTime
// audio call to the number (rings the Master's iPhone so he notices, without
// needing to answer). Requires the number to be a +E.164 string so arbitrary
// input can never reach the URL scheme (e.g. an email handle or injected
// shell/scheme characters).
func triggerFaceTime(number string) error {
	number = strings.TrimSpace(number)
	if number == "" {
		return fmt.Errorf("facetime requires a number")
	}
	if !validE164(number) {
		return fmt.Errorf("facetime number %q is not a valid +E.164 string", number)
	}
	cmd := exec.Command("open", "facetime://"+number)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open facetime failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// showBanner displays a native macOS notification via osascript.
func showBanner(title, body string) error {
	script := `on run argv
	set t to item 1 of argv
	set b to item 2 of argv
	display notification b with title t
end run`
	cmd := exec.Command("osascript", "-", title, body)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript banner failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Run serves the action API until ctx is done, mirroring the bridge's other
// subsystems.
func RunActionServer(ctx context.Context, addr, token string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           NewActionServer(token).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logging.Log("ACTIONS", logging.SevNotice, "SERVER", "Action server listening", "addr", addr)
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
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
		return server.Shutdown(shutdownCtx)
	}
}
