package imessage

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
)

// maxMessageAge is the staleness threshold — messages older than this are
// silently dropped to prevent spam after bridge restarts or reconnections.
const maxMessageAge = 5 * time.Minute

// Server exposes the bridge-facing HTTP API: it accepts inbound messages,
// serves outbound ones for the bridge to deliver, and receives delivery acks.
type Server struct {
	token      string
	selfHandle string
	out        OutboundStore
	gw         *gateway.Handler
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
	return s.requireToken(mux)
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
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if in.Handle == "" || in.Text == "" {
		http.Error(w, "handle and text are required", http.StatusBadRequest)
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
	// Staleness guard: reject messages older than maxMessageAge. This prevents
	// spam when the bridge restarts and re-delivers messages that arrived while
	// Clark was off. WhatsApp has this via connectedAt; iMessage needs it here.
	if !in.Timestamp.IsZero() && time.Since(in.Timestamp) > maxMessageAge {
		logging.Log("IMESSAGE", logging.SevInfo, "INBOUND", "Dropped stale message",
			"handle", in.Handle, "age", time.Since(in.Timestamp).Round(time.Second))
		w.WriteHeader(http.StatusOK)
		return
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

// handleOutbound claims the oldest pending outbound message for the bridge to
// deliver. An empty queue returns 204 with no body.
func (s *Server) handleOutbound(w http.ResponseWriter, r *http.Request) {
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
	if err := json.NewDecoder(r.Body).Decode(&ack); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
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

// toGateway maps a bridge message to the neutral gateway representation. The
// sender is the canonical identity (a phone handle maps to its WhatsApp JID so
// a person on both transports shares one VIP entry). iMessage is direct-to-
// device, so the chat a reply must reach is always the sender itself.
func toGateway(in InboundMessage) gateway.Message {
	sender := canonicalSender(in.Handle)
	return gateway.Message{
		ID:        in.ID,
		Sender:    sender,
		Chat:      sender,
		Text:      in.Text,
		Timestamp: in.Timestamp,
		IsSelf:    in.IsSelf,
		IsGroup:   false,
	}
}
