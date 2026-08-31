package imessage

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
	clarkmedia "github.com/heyimteee/clark/internal/media"
)

// maxMessageAge is the staleness threshold — messages older than this are
// silently dropped to prevent spam after bridge restarts or reconnections.
const maxMessageAge = 5 * time.Minute

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
	mux.HandleFunc("GET /identity", s.handleIdentity)
	return s.requireToken(mux)
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
	// Staleness guard: reject messages older than maxMessageAge. This prevents
	// spam when the bridge restarts and re-delivers messages that arrived while
	// Clark was off. WhatsApp has this via connectedAt; iMessage needs it here.
	if !in.Timestamp.IsZero() && time.Since(in.Timestamp) > maxMessageAge {
		logging.Log("IMESSAGE", logging.SevInfo, "INBOUND", "Dropped stale message",
			"handle", in.Handle, "age", time.Since(in.Timestamp).Round(time.Second))
		w.WriteHeader(http.StatusOK)
		return
	}
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
