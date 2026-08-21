package web

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/heyimteee/clark/internal/logging"
)

// Notify webhook rate limiting: a flood of accepted alerts cascades into
// WhatsApp/iMessage/voice delivery, so bursts beyond this budget are refused
// with 429 (#60).
const (
	notifyPerMinute = 5
	notifyBurst     = 5
)

// handleNotify accepts monitoring/alarm webhooks (Netdata, Uptime Kuma, and the
// server's bootwatch service) and pushes them to the Master through the shared
// alert service: WhatsApp, the web console chat, and spoken voice.
//
// The endpoint is authenticated with the dedicated ALERT_TOKEN (a static shared
// secret, distinct from the console WEB_TOKEN) sent in the X-Clark-Alert-Token
// header, so monitoring tools cannot touch the rest of the console.
//
// Body: {"kind":"overheat","title":"...","body":"..."}
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if s.alerts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "alerts are not configured"})
		return
	}
	if s.alertToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Clark-Alert-Token")), []byte(s.alertToken)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !s.notifyLimiter.allow() {
		logging.Log("WEB", logging.SevWarn, "ALERT", "Alert webhook rate limited")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many alerts; slow down"})
		return
	}
	var body struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
		Text  string `json:"body"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body is required"})
		return
	}
	logging.Log("WEB", logging.SevNotice, "ALERT", "Alert webhook received", "kind", body.Kind, "title", body.Title)
	s.alerts.Deliver(r.Context(), body.Kind, body.Title, body.Text)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
