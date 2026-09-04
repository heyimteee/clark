package gateway

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/ollama"
)

// Handler routes inbound messages from any transport through the shared
// pipeline: gating, urgent commands, fast path, and the serial dispatcher.
type Handler struct {
	component string
	msgr      Messenger
	butler    Butler
	notifier  Notifier
	disp      *dispatcher

	// bypassRe matches the urgent phrase on word boundaries; lastAlert holds
	// per-chat fire times for the cooldown window.
	bypassRe    *regexp.Regexp
	clock       func() time.Time
	lastAlertMu sync.Mutex
	lastAlert   map[string]time.Time

	// dedup tracks recently processed message IDs to prevent re-delivery
	// on bridge restarts. Entries are evicted after dedupTTL.
	dedupMu sync.Mutex
	dedup   map[string]time.Time
}

// NewHandler wires the pipeline around its dependencies. component names the
// transport in logs (e.g. "WHATSAPP"). bypassPhrase is the command word that
// triggers an urgent alert; empty falls back to "get him to me".
func NewHandler(component string, msgr Messenger, butler Butler, notifier Notifier, bypassPhrase string) *Handler {
	bypass := strings.TrimSpace(bypassPhrase)
	if bypass == "" {
		bypass = defaultBypassPhrase
	}
	h := &Handler{
		component: component,
		msgr:      msgr,
		butler:    butler,
		notifier:  notifier,
		disp:      newDispatcher(component, butler, msgr),
		dedup:     make(map[string]time.Time),
		bypassRe:  compileBypass(bypass),
		clock:     time.Now,
		lastAlert: make(map[string]time.Time),
	}
	return h
}

const defaultBypassPhrase = "get him to me"

// bypassCooldown is the minimum spacing between alert cascades from one chat.
// A VIP repeating the phrase (or a message that happens to contain it) must
// not machine-gun FaceTime calls and voice alerts at the Master (#60).
const bypassCooldown = 90 * time.Second

// compileBypass builds a case-insensitive, word-boundary matcher for the
// phrase so punctuation-suffixed phrases trigger while embedded lookalikes
// ("get him to meow") do not.
func compileBypass(phrase string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(phrase) + `\b`)
}

// bypassMatches reports whether text contains the phrase on word boundaries.
func bypassMatches(text, phrase string) bool {
	return compileBypass(phrase).MatchString(text)
}

// Close stops the background dispatcher and waits for in-flight replies.
func (h *Handler) Close() {
	h.disp.close()
}

// Handle runs one inbound message through the pipeline.
func (h *Handler) Handle(msg Message) {
	// Deduplication: reject messages with an ID we've already processed
	// within the last dedupTTL window.
	if msg.ID != "" {
		h.dedupMu.Lock()
		if _, seen := h.dedup[msg.ID]; seen {
			h.dedupMu.Unlock()
			logging.Log(h.component, logging.SevInfo, "DEDUP", "Duplicate message dropped", "id", msg.ID)
			return
		}
		h.dedup[msg.ID] = time.Now()
		h.dedupMu.Unlock()
		h.evictDedup()
	}

	// Clark-echo filter (#136): Clark sends through the Master's own number /
	// handle, so his branded output can loop back as inbound (missed echo ID
	// after a restart, redelivery, bridge mirror). A leading 🤵🏻‍♂️[CLARK]
	// prefix proves it is HIS text, not the Master's — drop it before gating,
	// history, or the model so it is never saved as a human message nor
	// answered. Mid-text quotes do not match (see IsClarkEcho).
	if IsClarkEcho(msg.Text) {
		logging.Log(h.component, logging.SevInfo, "MESSAGE", "Clark echo dropped; branded output looped back, not a human message",
			"id", msg.ID, "chat", msg.Chat, "from", msg.Sender, "self", msg.IsSelf,
			"preview", logging.Brief(msg.Text, 80),
			"next", "no reply sent; original reply already stored as assistant history")
		return
	}

	relation, isVIP := h.butler.Relation(msg.Sender)

	// The Master's own chat is always trusted (whether clark is enabled or the
	// Master is registered as a VIP), so a fresh install can be bootstrapped
	// with wake/context/VIP commands. Everyone else must be an enabled VIP in a
	// private chat.
	if !msg.IsSelf && (!h.butler.EnabledFor(msg.Sender) || !isVIP || msg.IsGroup) {
		return
	}

	logging.Log(h.component, logging.SevInfo, "RECEIVED", "Message received",
		"from", msg.Sender, "self", msg.IsSelf, "group", msg.IsGroup)

	if msg.Text == "" {
		if len(msg.Media) > 0 {
			// Has downloadable media for vision — let dispatcher describe it;
			// fall through to normal flow (process will prepend description).
		} else if msg.MediaType != "" {
			// Has media kind but no bytes (or not downloadable) — acknowledge instead of ghosting.
			ctx := context.Background()
			reply := mediaAckMessage(msg.MediaType)
			if err := h.msgr.Send(ctx, msg.Chat, reply); err != nil {
				logging.Log(h.component, logging.SevWarn, "SEND", "Failed to send media ack", "to", msg.Chat, "error", err)
			}
			logging.Log(h.component, logging.SevInfo, "MESSAGE", "Media message acked", "media", msg.MediaType, "from", msg.Sender)
			return
		} else {
			logging.Log(h.component, logging.SevWarn, "MESSAGE", "Message discarded", "reason", "no text content")
			return
		}
	}

	// Strip Clark's own branding so a sender cannot impersonate him in
	// stored history (#58).
	msg.Text = SanitizeInbound(msg.Text)

	ctx := context.Background()
	// Urgent command: match on word boundaries and honor the per-chat
	// cooldown so repeats cannot spam the alert cascade (#60).
	if h.bypassRe.MatchString(msg.Text) {
		if !h.alertAllowed(msg.Chat) {
			if err := h.msgr.Send(ctx, msg.Chat, "_One moment._ I have only just alerted the Master — he has heard you."); err != nil {
				logging.Log(h.component, logging.SevWarn, "SEND", "Failed to send cooldown reply", "to", msg.Chat, "error", err)
			}
			return
		}
		h.alert(ctx, msg.Chat, relation)
		return
	}

	// Fast path: deterministic commands answered with hardcoded messages.
	if reply, handled, err := h.butler.Prehandle(msg.Sender, msg.Text, msg.IsSelf); err != nil {
		logging.Log("OLLAMA", logging.SevErr, "RESPONSE", "Prehandle error", "error", err)
		h.msgr.Send(ctx, msg.Chat, apologyMessage)
		return
	} else if handled {
		if err := h.msgr.Send(ctx, msg.Chat, reply); err != nil {
			logging.Log(h.component, logging.SevErr, "SEND", "Failed to send fast reply", "to", msg.Chat, "error", err)
		}
		return
	}

	// Slow path: acknowledge immediately, then reply in the background so the
	// transport event loop is never blocked on a model generation. Only the
	// Master gets the "one moment" ack; VIPs get nothing until the reply.
	if msg.IsSelf {
		if err := h.msgr.Send(ctx, msg.Chat, ackMaster); err != nil {
			logging.Log(h.component, logging.SevWarn, "SEND", "Failed to send ack", "to", msg.Chat, "error", err)
		}
	}
	h.disp.enqueue(inbound{
		chat:      msg.Chat,
		senderJID: msg.Sender,
		userMsg:   msg.Text,
		isSelf:    msg.IsSelf,
		media:     msg.Media,
	})
}

// --- background dispatcher -------------------------------------------------

const (
	ackMaster      = "_One moment, Sir..._"
	apologyMessage = "_My apologies, but I am experiencing technical difficulties._ Please try again later."
	// rateLimitMasterMessage is delivered to the Master's own chat when the
	// model throttles the request; clark switches himself off at the same time.
	rateLimitMasterMessage = "🚨 Attention Sir!\n\nI have been silenced: the model is rate-limiting my requests. I have turned myself *Off* to stay reliable. Say _wake up buddy_ when you need me again."
)

func mediaAckMessage(mediaType string) string {
	switch mediaType {
	case "image":
		return "_My apologies — I can only read text at the moment._ If you add a caption I can reply to that, Sir."
	case "video":
		return "_My apologies — video needs a caption for me to reply at the moment, Sir._"
	case "gif":
		return "_My apologies — GIFs need a caption for me to reply at the moment, Sir._"
	case "document":
		return "_My apologies — files need a caption for me to reply at the moment, Sir._"
	case "audio":
		return "_My apologies — I cannot transcribe audio yet, Sir._"
	case "sticker":
		return "_Noted, Sir._ — I cannot read stickers, but I am here for your message."
	default:
		return "_My apologies — I can only read text at the moment, Sir._"
	}
}

// inbound is one message awaiting a slow, model-backed reply.
type inbound struct {
	chat      string
	senderJID string
	userMsg   string
	isSelf    bool
	media     []MediaAttachment
}

// dispatcher runs one serial worker goroutine per sender so replies arrive in
// order, while never blocking the transport event loop on a slow generation.
// Idle workers retire after dispatcherIdleIdleTimeout with an empty queue and
// are recreated on the sender's next message, so goroutines track active
// conversations rather than every sender ever seen.
type dispatcher struct {
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	queues    map[string]chan inbound
	wg        sync.WaitGroup
	component string
	butler    Butler
	msgr      Messenger
	idle      time.Duration // worker idle retirement; 0 = defaultDispatcherIdle
	now       func() time.Time
}

const defaultDispatcherIdle = 10 * time.Minute

func newDispatcher(component string, butler Butler, msgr Messenger) *dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &dispatcher{
		ctx:       ctx,
		cancel:    cancel,
		queues:    make(map[string]chan inbound),
		component: component,
		butler:    butler,
		msgr:      msgr,
		idle:      defaultDispatcherIdle,
		now:       time.Now,
	}
}

// enqueue hands a message to its sender's worker, creating the worker on
// first use or after idle retirement. The lookup and send happen atomically
// under the mutex so a retiring worker can never orphan an in-flight send;
// when a sender's queue is full the message is dropped with a warning rather
// than stalling the transport event loop (dedup + user retry cover it).
func (d *dispatcher) enqueue(in inbound) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	ch, ok := d.queues[in.senderJID]
	if !ok {
		ch = make(chan inbound, 16)
		d.queues[in.senderJID] = ch
		d.wg.Add(1)
		go d.worker(in.senderJID, ch)
	}
	select {
	case ch <- in:
	default:
		logging.Log(d.component, logging.SevWarn, "DISPATCH", "Sender queue full; message dropped",
			"sender", in.senderJID)
	}
}

// worker drains one sender's queue serially and retires itself after the idle
// timeout with nothing pending.
func (d *dispatcher) worker(sender string, ch chan inbound) {
	defer d.wg.Done()
	idle := d.idle
	if idle <= 0 {
		idle = defaultDispatcherIdle
	}
	for {
		timer := time.NewTimer(idle)
		select {
		case in, ok := <-ch:
			timer.Stop()
			if !ok {
				return
			}
			d.process(in)
		case <-timer.C:
			d.mu.Lock()
			if len(ch) == 0 && d.queues[sender] == ch && !d.closed {
				delete(d.queues, sender)
				d.mu.Unlock()
				return
			}
			d.mu.Unlock()
			// Work arrived during retirement or queue was replaced; loop.
		}
	}
}

// processMedia converts each attachment into a bracketed context line using
// the Butler's optional local-processing capabilities. Returns the context
// lines, and the first attachment that could not be processed — nil when
// everything succeeded or nothing needed processing.
func (d *dispatcher) processMedia(in inbound) (lines []string, failed *MediaAttachment) {
	var audio, docs, visual []MediaAttachment
	for i := range in.media {
		switch in.media[i].Type {
		case "audio":
			audio = append(audio, in.media[i])
		case "document":
			docs = append(docs, in.media[i])
		default:
			visual = append(visual, in.media[i])
		}
	}

	addLine := func(prefix, body string) {
		lines = append(lines, "["+prefix+": "+strings.TrimSpace(body)+"]")
	}
	fail := func(stage string, m MediaAttachment, err error, capability string) *MediaAttachment {
		logging.Log(d.component, logging.SevErr, "MEDIA",
			"LOCAL MEDIA PROCESSING FAILED — degrading message to caption-only fallback",
			"stage", stage, "type", m.Type, "mime", m.MIME,
			"capability", capability, "has_caption", strings.TrimSpace(in.userMsg) != "", "error", err)
		return &m
	}

	if len(audio) > 0 {
		tr, ok := d.butler.(AudioTranscriber)
		for _, m := range audio {
			if !ok {
				return lines, fail("capability-missing", m, errLocalProcessorUnavailable, "AudioTranscriber")
			}
			text, err := tr.TranscribeVoice(d.ctx, m.MIME, m.Data)
			if err != nil {
				return lines, fail("transcribe", m, err, "AudioTranscriber")
			}
			addLine("voice note", text)
		}
	}

	if len(docs) > 0 {
		dg, ok := d.butler.(DocDigester)
		for _, m := range docs {
			if !ok {
				return lines, fail("capability-missing", m, errLocalProcessorUnavailable, "DocDigester")
			}
			digest, err := dg.DigestDocument(d.ctx, m.Name, string(m.Data))
			if err != nil {
				return lines, fail("digest", m, err, "DocDigester")
			}
			addLine("document "+m.Name+" | compacted", digest)
		}
	}

	if len(visual) > 0 {
		de, ok := d.butler.(MediaDescriber)
		if !ok {
			return lines, fail("capability-missing", visual[0], errLocalProcessorUnavailable, "MediaDescriber")
		}
		desc, err := de.Describe(d.ctx, visual)
		if err != nil {
			return lines, fail("describe", visual[0], err, "MediaDescriber")
		}
		prefix := visual[0].Type
		if prefix == "" || prefix == "image" && len(visual) > 1 {
			prefix = "image"
		}
		addLine(prefix, desc)
	}
	return lines, nil
}

// errLocalProcessorUnavailable marks a missing local capability distinctly
// from a runtime failure in logs.
var errLocalProcessorUnavailable = errors.New("local processor not configured")

func (d *dispatcher) process(in inbound) {
	userMsg := in.userMsg
	if len(in.media) > 0 {
		lines, failed := d.processMedia(in)
		if failed != nil {
			caption := strings.TrimSpace(userMsg)
			if caption == "" {
				// No caption to fall back on — acknowledge with kind text.
				_ = d.msgr.Send(d.ctx, in.chat, mediaAckMessage(failed.Type))
				return
			}
			// Caption fallback: answer from the caption but explicitly tell
			// the sender their asset could not be processed right now.
			note := "_My apologies — I could not process your " + failed.Type + " due to internal issues just now._ I will answer based on your caption alone."
			if err := d.msgr.Send(d.ctx, in.chat, note); err != nil {
				logging.Log(d.component, logging.SevWarn, "SEND", "Failed to send asset-failure notice", "to", in.chat, "error", err)
			}
		}
		for _, l := range lines {
			userMsg += "\n" + l
		}
		userMsg = strings.TrimSpace(userMsg)
	}
	if userMsg == "" {
		logging.Log(d.component, logging.SevWarn, "MESSAGE", "Message discarded after media", "reason", "no text content")
		return
	}
	reply, err := d.butler.Reply(d.ctx, in.senderJID, userMsg, in.isSelf)
	if err != nil {
		if errors.Is(err, ollama.ErrRateLimited) {
			logging.Log("OLLAMA", logging.SevErr, "RATELIMIT", "Model rate limited; master alerted and clark switched off", "error", err)
			if serr := d.msgr.SendSelf(d.ctx, rateLimitMasterMessage); serr != nil {
				logging.Log(d.component, logging.SevErr, "SEND", "Failed to alert master about rate limit", "error", serr)
			}
		} else {
			logging.Log("OLLAMA", logging.SevErr, "RESPONSE", "AI response error", "error", err)
		}
		if err := d.msgr.Send(d.ctx, in.chat, apologyMessage); err != nil {
			logging.Log(d.component, logging.SevErr, "SEND", "Failed to send apology", "to", in.chat, "error", err)
		}
		return
	}
	if err := d.msgr.Send(d.ctx, in.chat, reply); err != nil {
		logging.Log(d.component, logging.SevErr, "SEND", "Failed to deliver reply", "to", in.chat, "error", err)
	}
}

// close stops accepting new work, closes every queue, and waits for queued and
// in-flight replies to finish before cancelling the shared context.
func (d *dispatcher) close() {
	d.mu.Lock()
	d.closed = true
	for _, ch := range d.queues {
		close(ch)
	}
	d.mu.Unlock()
	d.wg.Wait()
	d.cancel()
}

func (h *Handler) alert(ctx context.Context, chat, relation string) {
	title := "Attention Sir!"
	body := relation + " needs you!"
	// Prefer the kind-aware alert notifier (delivers to WhatsApp, web chat,
	// and voice in one shot); fall back to the legacy two-arg Notify +
	// self-chat. A nil notifier (tests, headless wiring) skips desktop
	// notification but still answers the sender.
	if an, ok := h.notifier.(AlertNotifier); ok && an != nil {
		an.Alert(ctx, "bypass", title, body)
	} else {
		if h.notifier != nil {
			if err := h.notifier.Notify(title, body); err != nil {
				logging.Log("CLARK", logging.SevWarn, "NOTIFY", "Notification failed", "error", err)
			}
		}
		h.msgr.SendSelf(ctx, "🚨 "+title+"\n"+body)
	}
	h.msgr.Send(ctx, chat, "_One moment._ I've alerted the Master.")
}

// alertAllowed reports whether chat may fire the alert cascade now, marking
// the fire when allowed. Chats inside the cooldown window are refused.
func (h *Handler) alertAllowed(chat string) bool {
	now := h.clock()
	h.lastAlertMu.Lock()
	defer h.lastAlertMu.Unlock()
	if last, ok := h.lastAlert[chat]; ok && now.Sub(last) < bypassCooldown {
		return false
	}
	h.lastAlert[chat] = now
	return true
}

const dedupTTL = 10 * time.Minute

// evictDedup removes entries older than dedupTTL. Called after each insert.
func (h *Handler) evictDedup() {
	h.dedupMu.Lock()
	defer h.dedupMu.Unlock()
	cutoff := time.Now().Add(-dedupTTL)
	for id, t := range h.dedup {
		if t.Before(cutoff) {
			delete(h.dedup, id)
		}
	}
}
