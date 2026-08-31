package whatsapp

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/media"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Handler adapts whatsmeow events into the shared gateway pipeline.
type Handler struct {
	msgr        *WAMessenger
	butler      gateway.Butler
	echo        *gateway.EchoTracker
	connectedAt time.Time
	gw          *gateway.Handler
}

// NewHandler wires the adapter around its dependencies. bypassPhrase is the
// urgent-alert command word (default "get him to me").
func NewHandler(msgr *WAMessenger, butler gateway.Butler, notifier gateway.Notifier, echo *gateway.EchoTracker, connectedAt time.Time, bypassPhrase string) *Handler {
	return &Handler{
		msgr:        msgr,
		butler:      butler,
		echo:        echo,
		connectedAt: connectedAt,
		gw:          gateway.NewHandler("WHATSAPP", msgr, butler, notifier, bypassPhrase),
	}
}

// Close stops the background dispatcher and waits for in-flight replies.
func (h *Handler) Close() {
	h.gw.Close()
}

// OnEvent is the whatsmeow event sink.
func (h *Handler) OnEvent(evt any) {
	v, ok := evt.(*events.Message)
	if !ok {
		return
	}

	if skip, reason := filterMessage(v, h.connectedAt); skip {
		if reason != "" {
			logging.Log("WHATSAPP", logging.SevWarn, "MESSAGE", "Message discarded", "reason", reason)
		}
		return
	}

	if h.echo.Consume(string(v.Info.ID)) {
		return
	}

	msg, ok := h.toGateway(v)
	if !ok {
		return
	}
	h.gw.Handle(msg)
}

// toGateway maps a whatsmeow message to a neutral gateway.Message. It reports
// false when the transport itself must drop the message (outbound echoes to a
// chat that is not clark's own).
func (h *Handler) toGateway(v *events.Message) (gateway.Message, bool) {
	isSelf := false
	if v.Info.IsFromMe {
		if !h.msgr.IsSelfChat(v.Info.Chat) {
			return gateway.Message{}, false
		}
		isSelf = true
	}

	sender := h.msgr.ResolveSender(v)
	senderStr := sender.String()
	relation, isVIP := h.butler.Relation(senderStr)

	userMsg, mediaType := extractTextAndMedia(v)

	// Local media brain (OLLAMA_VISION_MODEL set): captions and assets are
	// processed TOGETHER whenever the type is locally processable — the
	// caption alone is only a degraded fallback used when local processing
	// fails (the dispatcher acks that failure explicitly). Voice notes,
	// stickers, video/GIF frames, images, and extractable documents all
	// attach bytes; the cloud model only ever sees text derived locally.
	var atts []gateway.MediaAttachment
	if os.Getenv("OLLAMA_VISION_MODEL") != "" && mediaType != "" {
		atts = h.collectMedia(v, mediaType)
	}

	who := "Unknown"
	if isVIP {
		who = relation
	}
	// Log only private messages; group chatter is filtered out so the logs
	// show just the people Clark actually talks to.
	if !v.Info.IsGroup {
		logIncoming(v, sender, who, isVIP, userMsg)
	}

	return gateway.Message{
		ID:        string(v.Info.ID),
		Sender:    senderStr,
		Chat:      v.Info.Chat.String(),
		Text:      userMsg,
		MediaType: mediaType,
		Media:     atts,
		IsSelf:    isSelf,
		IsGroup:   v.Info.IsGroup,
	}, true
}

// extractTextAndMedia returns the user-visible text and a media classification.
// Captions from image/video/document messages are treated as text so Clark
// can answer; uncaptioned media still reports its kind for a polite ack.
func extractTextAndMedia(v *events.Message) (string, string) {
	if v == nil || v.Message == nil {
		return "", ""
	}
	msg := unwrapMessage(v.Message)
	if t := v.Message.GetConversation(); t != "" {
		return t, ""
	}
	if em := v.Message.GetExtendedTextMessage(); em != nil {
		if t := em.GetText(); t != "" {
			return t, ""
		}
	}
	if m := msg.GetImageMessage(); m != nil {
		return m.GetCaption(), "image"
	}
	if m := msg.GetVideoMessage(); m != nil {
		if m.GetGifPlayback() {
			return m.GetCaption(), "gif"
		}
		return m.GetCaption(), "video"
	}
	if m := msg.GetDocumentMessage(); m != nil {
		return m.GetCaption(), "document"
	}
	if m := msg.GetAudioMessage(); m != nil {
		return "", "audio"
	}
	if m := msg.GetStickerMessage(); m != nil {
		return "", "sticker"
	}
	// Reactions and other non-text system messages stay silent.
	return "", ""
}

// mediaKindFor maps a classified type to its DownloadMedia payload kind.
func mediaKindFor(mediaType string) string {
	switch mediaType {
	case "audio":
		return "audio"
	case "video", "gif":
		return "video"
	case "sticker":
		return "sticker"
	case "image", "document":
		return mediaType
	default:
		return ""
	}
}

// collectMedia downloads and locally normalizes the assets of one message so
// the local model can process them (vision frames, transcription audio,
// extractable document text). Returns whatever succeeded; failures are logged
// loudly — the dispatcher decides between co-processing and caption fallback.
func (h *Handler) collectMedia(v *events.Message, mediaType string) []gateway.MediaAttachment {
	dctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	kind := mediaKindFor(mediaType)
	data, mime, name, err := h.msgr.DownloadMedia(dctx, v, kind)
	if err != nil {
		logging.Log("WHATSAPP", logging.SevWarn, "MEDIA",
			"Local media processing: download FAILED; message will degrade to caption-only if a caption exists",
			"type", mediaType, "error", err)
		return nil
	}
	if len(data) == 0 {
		logging.Log("WHATSAPP", logging.SevWarn, "MEDIA",
			"Local media processing: no downloadable payload for this type; degrading to caption-only path",
			"type", mediaType)
		return nil
	}

	mk := func(t, m string, d []byte) gateway.MediaAttachment {
		return gateway.MediaAttachment{Type: t, Name: name, MIME: m, Data: d}
	}

	switch mediaType {
	case "audio":
		return []gateway.MediaAttachment{mk("audio", mime, data)}

	case "image":
		return []gateway.MediaAttachment{mk("image", mime, data)}

	case "sticker":
		animated := stickerAnimated(v)
		if !animated {
			png, perr := media.ToPNG(dctx, data)
			if perr == nil && len(png) > 0 {
				return []gateway.MediaAttachment{mk("sticker", "image/png", png)}
			}
			logging.Log("WHATSAPP", logging.SevWarn, "MEDIA", "Sticker PNG conversion failed; acking without vision", "mime", mime, "size", len(data), "error", perr)
			return nil
		}
		frames, ferr := media.ExtractFrames(dctx, data, 3, 768)
		if ferr != nil {
			logging.Log("WHATSAPP", logging.SevWarn, "MEDIA", "Animated sticker frame extraction failed", "error", ferr)
			return nil
		}
		out := make([]gateway.MediaAttachment, 0, len(frames))
		for _, f := range frames {
			out = append(out, mk("sticker", "image/jpeg", f))
		}
		return out

	case "video", "gif":
		frames, ferr := media.ExtractFrames(dctx, data, 4, 768)
		if ferr != nil {
			logging.Log("WHATSAPP", logging.SevWarn, "MEDIA", "Video keyframe extraction failed; degrading to caption fallback", "type", mediaType, "error", ferr)
			return nil
		}
		out := make([]gateway.MediaAttachment, 0, len(frames))
		for _, f := range frames {
			out = append(out, mk(mediaType, "image/jpeg", f))
		}
		return out

	case "document":
		if len(mime) >= 6 && mime[:6] == "image/" {
			return []gateway.MediaAttachment{mk("document", mime, data)}
		}
		var text string
		truncated := false
		if strings.HasPrefix(mime, "application/pdf") || strings.HasSuffix(strings.ToLower(name), ".pdf") {
			text, truncated, err = media.ExtractText(dctx, data)
			if err != nil {
				logging.Log("WHATSAPP", logging.SevWarn, "MEDIA", "PDF text extraction failed; degrading to caption fallback", "name", name, "error", err)
				return nil
			}
		} else if isTextLike(mime, name) {
			text = string(data)
			if len(text) > media.MaxPDFChars {
				text = text[:media.MaxPDFChars]
				truncated = true
			}
		} else {
			logging.Log("WHATSAPP", logging.SevInfo, "MEDIA", "Unsupported document type for local extraction", "mime", mime, "name", name)
			return nil
		}
		if truncated {
			text += "\n[TRUNCATED:extraction cap reached]"
			logging.Log("WHATSAPP", logging.SevWarn, "MEDIA", "Document text truncated at extraction cap", "name", name)
		}
		return []gateway.MediaAttachment{mk("document", mime, []byte(text))}
	}
	return nil
}

// stickerAnimated reports whether the message carries an animated/video sticker.
func stickerAnimated(v *events.Message) bool {
	sm := unwrapMessage(v.Message).GetStickerMessage()
	if sm == nil {
		return false
	}
	if sm.GetIsAnimated() {
		return true
	}
	return strings.HasPrefix(sm.GetMimetype(), "video/")
}

// isTextLike reports whether a document's mime or filename suggests plain
// extractable text worth digesting.
func isTextLike(mime, name string) bool {
	textMimes := []string{"text/", "application/json", "application/xml", "application/yaml"}
	for _, p := range textMimes {
		if strings.HasPrefix(mime, p) {
			return true
		}
	}
	ext := strings.ToLower(name)
	for _, e := range []string{".txt", ".md", ".csv", ".json", ".xml", ".yaml", ".yml", ".log", ".go", ".py", ".js", ".ts"} {
		if strings.HasSuffix(ext, e) {
			return true
		}
	}
	return false
}

// unwrapMessage peels ephemeral/view-once wrappers to reach the inner payload.
func unwrapMessage(m *waE2E.Message) *waE2E.Message {
	if m == nil {
		return nil
	}
	for {
		if em := m.GetEphemeralMessage(); em != nil && em.GetMessage() != nil {
			m = em.GetMessage()
			continue
		}
		if vom := m.GetViewOnceMessage(); vom != nil && vom.GetMessage() != nil {
			m = vom.GetMessage()
			continue
		}
		if v2 := m.GetViewOnceMessageV2(); v2 != nil && v2.GetMessage() != nil {
			m = v2.GetMessage()
			continue
		}
		break
	}
	return m
}

// filterMessage reports whether a message must be dropped, and why.
func filterMessage(v *events.Message, connectedAt time.Time) (skip bool, reason string) {
	if v == nil || v.Info.Chat.IsEmpty() || v.Info.Sender.IsEmpty() || v.Message == nil {
		return true, "nil message data"
	}
	if v.Info.Timestamp.IsZero() || v.Info.Timestamp.Before(connectedAt) {
		return true, ""
	}
	return false, ""
}

func logIncoming(v *events.Message, sender types.JID, who string, isVIP bool, content string) {
	number := sender.User
	if number == "" {
		number = sender.String()
	}

	direction := "incoming"
	if v.Info.IsFromMe {
		direction = "self-text"
	}

	chatType := "private"
	if v.Info.IsGroup {
		chatType = "group"
	}

	vip := "no"
	if isVIP {
		vip = "yes"
	}

	if content == "" {
		content = "<non-text>"
	}

	logging.Log("WHATSAPP", logging.SevInfo, "MESSAGE", "Message received",
		"from", who,
		"number", number,
		"chat", chatType,
		"vip", vip,
		"direction", direction,
		"msg", logging.Brief(content, 60))
}

func logReply(toNumber, content string) {
	logging.Log("WHATSAPP", logging.SevInfo, "SEND", "Message sent",
		"to", toNumber,
		"msg", logging.Brief(content, 60))
}
