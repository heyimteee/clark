package imessage

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
	clarkmedia "github.com/heyimteee/clark/internal/media"
	"github.com/heyimteee/clark/internal/store"
)

// maxBodyBytes caps request bodies for acks; inbound messages with media
// may be larger (base64 images).
const maxBodyBytes = 256 << 10
const maxInboundBytes = 55 << 20

// Server exposes the bridge-facing HTTP API: it accepts inbound messages,
// serves outbound ones for the bridge to deliver, and receives delivery acks.
type Server struct {
	token      string
	selfHandle string
	out        OutboundStore
	gw         *gateway.Handler
	// lastPollUnix records the last /outbound poll (Unix seconds) so the
	// tool-health monitor can tell a wedged bridge poller from a quiet queue.
	lastPollUnix atomic.Int64
}

// NewServer wires the API around its dependencies. token is the bridge's
// shared secret sent in X-Clark-Bridge-Token; empty disables auth (never use in
// production). selfHandle is the Master's own iMessage handle ("+6281111111111");
// messages from it are the Master's self-chat and are dropped (management is
// WhatsApp-only).
func NewServer(token, selfHandle string, out OutboundStore, gw *gateway.Handler) *Server {
	return &Server{token: token, selfHandle: selfHandle, out: out, gw: gw}
}

// Routes returns the HTTP handler with auth enforced.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /inbound", s.handleInbound)
	mux.HandleFunc("GET /outbound", s.handleOutbound)
	mux.HandleFunc("POST /ack", s.handleAck)
	mux.HandleFunc("POST /fail", s.handleFail)
	mux.HandleFunc("GET /identity", s.handleIdentity)
	mux.HandleFunc("GET /outbound/dead", s.handleDeadOutbound)
	return s.requireToken(mux)
}

// handleDeadOutbound lists iMessages that could not be delivered, so an
// undeliverable message is visible rather than silently lost (#210).
func (s *Server) handleDeadOutbound(w http.ResponseWriter, r *http.Request) {
	dead, err := s.out.DeadIMessages(50)
	if err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "OUTBOUND", "Failed to load dead-lettered messages", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if dead == nil {
		dead = []store.DeadOutbound{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"dead": dead, "count": len(dead)})
}

// failRequest is the bridge's POST /fail body: one delivery attempt that did not
// succeed, with the classification that decides whether another attempt is safe.
type failRequest struct {
	ID             int64  `json:"id"`
	Reason         string `json:"reason"`
	Classification string `json:"classification"`
	Attempts       int    `json:"attempts"`
	// RetryAfterSeconds is the bridge's suggested backoff; the server clamps it.
	RetryAfterSeconds int `json:"retry_after_seconds"`
}

// maxRetryAfterSeconds caps a bridge-supplied backoff so a buggy or hostile
// client cannot park a message for days.
const maxRetryAfterSeconds = 3600

// handleFail records a failed delivery attempt and decides retry vs dead-letter.
// Retrying a send whose outcome is genuinely unknown risks a duplicate message on
// the recipient's device, so that classification is never retried (#210).
func (s *Server) handleFail(w http.ResponseWriter, r *http.Request) {
	var req failRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeBodyError(w, err)
		return
	}
	if req.ID < 1 {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	classification := req.Classification
	switch classification {
	case store.FailureNotStarted, store.FailureUnknown, store.FailureGhost:
	case "":
		classification = store.FailureNotStarted
	default:
		http.Error(w, "unknown classification "+classification, http.StatusBadRequest)
		return
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "delivery failed"
	}

	retryAfter := time.Duration(req.RetryAfterSeconds) * time.Second
	if retryAfter <= 0 {
		retryAfter = store.RetryDelay(req.Attempts)
	}
	if retryAfter > maxRetryAfterSeconds*time.Second {
		retryAfter = maxRetryAfterSeconds * time.Second
	}
	exhausted := req.Attempts >= store.MaxOutboundAttempts

	if err := s.out.FailIMessage(req.ID, reason, classification, time.Now().Add(retryAfter), exhausted); err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "FAIL", "Failed to record delivery failure", "id", req.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	willRetry := !exhausted && classification != store.FailureUnknown
	next := "dead-lettered; message will not be retried"
	if willRetry {
		next = "retry after backoff"
	}
	logging.Log("IMESSAGE", logging.SevWarn, "FAIL", "Outbound delivery failed",
		"id", req.ID, "classification", classification, "attempts", req.Attempts,
		"exhausted", exhausted, "retry_after", retryAfter, "reason", reason, "next", next)
	w.WriteHeader(http.StatusOK)
}

// handleIdentity returns the configured master self-handle so the bridge can
// fetch its single source of truth at boot instead of heuristically guessing.
func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"self_handle": s.selfHandle}); err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "IDENTITY", "Failed to encode identity", "error", err)
	}
}

func (s *Server) requireToken(next http.Handler) http.Handler {
	if s.token == "" {
		logging.Log("IMESSAGE", logging.SevWarn, "SERVER", "Bridge token empty; inbound API is unauthenticated")
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

// handleInbound feeds one bridge-delivered message into the gateway pipeline.
func (s *Server) handleInbound(w http.ResponseWriter, r *http.Request) {
	var in InboundMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxInboundBytes)).Decode(&in); err != nil {
		writeBodyError(w, err)
		return
	}
	if in.Handle == "" || (in.Text == "" && len(in.Media) == 0 && in.MediaType == "") {
		http.Error(w, "handle and text or media required", http.StatusBadRequest)
		return
	}
	// The Master's own iMessage self-chat is not a control surface: management
	// happens on WhatsApp only. Dropping it here (acknowledged so the bridge
	// advances its watermark) also kills the echo loop caused by chat.db
	// storing a mirrored is_from_me=0 copy of every outbound self message.
	if s.isSelf(in) {
		logging.Log("IMESSAGE", logging.SevInfo, "INBOUND", "Dropped master self-chat message; management is WhatsApp-only", "handle", in.Handle)
		w.WriteHeader(http.StatusOK)
		return
	}
	// No staleness drop. A message that arrives hours late is still kept as
	// history so it can inform a later live reply; whether it is *answered* is
	// the gateway's call, using the bridge's Replay flag and the status
	// watermark (#206). Dropping it here destroyed the backlog outright.
	// Normalize media for local vision (video/gif -> frames, etc.) so the
	// gateway can treat iMessage exactly like WhatsApp.
	if len(in.Media) > 0 {
		mctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		in.Media = normalizeIMessageMedia(mctx, in.Media)
		cancel()
		if len(in.Media) > 0 {
			in.MediaType = in.Media[0].Type
		}
	}
	s.gw.Handle(toGateway(in))
	w.WriteHeader(http.StatusOK)
}

// isSelf reports whether in came from the Master's own chat, either because the
// bridge marked it (IsSelf) or because the handle resolves to the configured
// self handle (defense in depth against a misbehaving bridge).
func (s *Server) isSelf(in InboundMessage) bool {
	if in.IsSelf {
		return true
	}
	return s.selfHandle != "" && canonicalSender(in.Handle) == canonicalSender(s.selfHandle)
}

// LastPoll returns when the macOS bridge last polled /outbound (zero when it
// never has). The bridge polls every second, so anything older than a couple
// of minutes means it stopped asking.
func (s *Server) LastPoll() time.Time {
	if unix := s.lastPollUnix.Load(); unix > 0 {
		return time.Unix(unix, 0)
	}
	return time.Time{}
}

// handleOutbound claims the oldest pending outbound message for the bridge to
// deliver. An empty queue returns 204 with no body.
func (s *Server) handleOutbound(w http.ResponseWriter, r *http.Request) {
	s.lastPollUnix.Store(time.Now().Unix())
	msg, ok, err := s.out.NextIMessageOutbound()
	if err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "OUTBOUND", "Failed to claim outbound message", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(msg); err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "OUTBOUND", "Failed to encode outbound message", "error", err)
	}
}

// handleAck removes a delivered outbound message from the queue.
func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	var ack AckRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&ack); err != nil {
		writeBodyError(w, err)
		return
	}
	if ack.ID < 1 {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if err := s.out.AckIMessage(ack.ID); err != nil {
		logging.Log("IMESSAGE", logging.SevErr, "ACK", "Failed to ack outbound message", "id", ack.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// writeBodyError maps a body-decode failure to a status: an over-limit body is
// a 413 (the client must not retry it as-is), anything else a 400.
func writeBodyError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "invalid JSON body", http.StatusBadRequest)
}

func normalizeIMessageMedia(ctx context.Context, media []InboundMedia) []InboundMedia {
	var out []InboundMedia
	for _, m := range media {
		switch m.Type {
		case "video", "gif":
			frames, err := clarkmedia.ExtractFrames(ctx, m.Data, 4, 768)
			if err != nil {
				logging.Log("IMESSAGE", logging.SevWarn, "MEDIA", "Failed to extract frames", "type", m.Type, "error", err)
				continue
			}
			for _, f := range frames {
				out = append(out, InboundMedia{Type: m.Type, Name: m.Name, MIME: "image/jpeg", Data: f})
			}
		case "sticker":
			// Try to determine if animated by MIME; if video/* treat as frames.
			if m.MIME == "video/mp4" || m.MIME == "image/webp" && len(m.Data) > 100*1024 {
				frames, err := clarkmedia.ExtractFrames(ctx, m.Data, 3, 768)
				if err == nil && len(frames) > 0 {
					for _, f := range frames {
						out = append(out, InboundMedia{Type: "sticker", Name: m.Name, MIME: "image/jpeg", Data: f})
					}
					continue
				}
			}
			png, err := clarkmedia.ToPNG(ctx, m.Data)
			if err == nil && len(png) > 0 {
				out = append(out, InboundMedia{Type: "sticker", Name: m.Name, MIME: "image/png", Data: png})
			} else {
				out = append(out, m)
			}
		default:
			out = append(out, m)
		}
	}
	if len(out) == 0 && len(media) > 0 {
		return media
	}
	return out
}

// toGateway maps a bridge message to the neutral gateway representation. The
// sender is the canonical identity (a phone handle maps to its WhatsApp JID so
// a person on both transports shares one VIP entry). iMessage is direct-to-
// device, so the chat a reply must reach is always the sender itself.
func toGateway(in InboundMessage) gateway.Message {
	sender := canonicalSender(in.Handle)
	msg := gateway.Message{
		ID:        in.ID,
		Sender:    sender,
		Chat:      sender,
		Text:      in.Text,
		Timestamp: in.Timestamp,
		IsSelf:    in.IsSelf,
		IsGroup:   false,
		Replay:    in.Replay,
		MediaType: in.MediaType,
	}
	for _, m := range in.Media {
		msg.Media = append(msg.Media, gateway.MediaAttachment{
			Type: m.Type,
			Name: m.Name,
			MIME: m.MIME,
			Data: m.Data,
		})
		if msg.MediaType == "" {
			msg.MediaType = m.Type
		}
	}
	return msg
}
