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
			logging.Log(h.component, logging.SevInfo, "DEDUP", "Duplicate message dropped; already processed within dedup window",
				"id", msg.ID, "chat", msg.Chat, "from", msg.Sender,
				"next", "no reply sent; original delivery stands")
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
		logging.Log(h.component, logging.SevInfo, "GATE", "Message ignored by gating rule; not eligible for a reply",
			"chat", msg.Chat, "from", msg.Sender, "self", msg.IsSelf, "group", msg.IsGroup,
			"enabled_for_sender", h.butler.EnabledFor(msg.Sender), "is_vip", isVIP,
			"next", "enable Clark for this sender or check VIP list / group rule")
		return
	}

	logging.Log(h.component, logging.SevInfo, "RECEIVED", "Message received",
		"id", msg.ID, "chat", msg.Chat,
		"from", msg.Sender, "self", msg.IsSelf, "group", msg.IsGroup,
		"preview", logging.Brief(msg.Text, 80))

	if msg.Text == "" {
		if len(msg.Media) > 0 {
			// Has downloadable media for vision — let dispatcher describe it;
			// fall through to normal flow (process will prepend description).
		} else if msg.MediaType != "" {
			// Has media kind but no bytes (or not downloadable) — acknowledge instead of ghosting.
			ctx := context.Background()
			reply := mediaAckMessage(msg.MediaType)
			if err := h.msgr.Send(ctx, msg.Chat, reply); err != nil {
				logging.Log(h.component, logging.SevWarn, "SEND", "Failed to send media ack; sender got no acknowledgement for unprocessable media",
					"to", msg.Chat, "from", msg.Sender, "media", msg.MediaType, "error", err,
					"next", "check transport connection, then ask sender to resend with a caption")
			} else {
				logging.Log(h.component, logging.SevInfo, "MESSAGE", "Media message acked; asked sender for a caption",
					"media", msg.MediaType, "from", msg.Sender, "to", msg.Chat)
			}
			return
		} else {
			logging.Log(h.component, logging.SevWarn, "MESSAGE", "Message discarded; no text and no media to answer",
				"id", msg.ID, "chat", msg.Chat, "from", msg.Sender,
				"next", "sender must resend with text or a captioned attachment")
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
				logging.Log(h.component, logging.SevWarn, "SEND", "Failed to send alert-cooldown reply; sender got no cooldown notice",
					"to", msg.Chat, "from", msg.Sender, "error", err)
			} else {
				logging.Log(h.component, logging.SevInfo, "ALERT", "Alert cooldown held; repeat bypass suppressed",
					"chat", msg.Chat, "from", msg.Sender)
			}
			return
		}
		h.alert(ctx, msg.Chat, relation)
		return
	}

	// Fast path: deterministic commands answered with hardcoded messages.
	if reply, handled, err := h.butler.Prehandle(msg.Sender, msg.Text, msg.IsSelf); err != nil {
		logging.Log("OLLAMA", logging.SevErr, "RESPONSE", "Prehandle failed while reading the command; sending apology",
			"chat", msg.Chat, "from", msg.Sender, "preview", logging.Brief(msg.Text, 80), "error", err,
			"next", "sender should repeat the command; check assistant store health")
		if serr := h.msgr.Send(ctx, msg.Chat, apologyFor("reading your command")); serr != nil {
			logging.Log(h.component, logging.SevErr, "SEND", "Failed to send prehandle apology; sender got no failure notice",
				"to", msg.Chat, "error", serr, "cause", err)
		}
		return
	} else if handled {
		if err := h.msgr.Send(ctx, msg.Chat, reply); err != nil {
			logging.Log(h.component, logging.SevErr, "SEND", "Failed to send fast-path reply; command ran but answer was lost",
				"to", msg.Chat, "from", msg.Sender, "preview", logging.Brief(reply, 80), "error", err,
				"next", "check transport connection; sender may repeat the command")
		}
		return
	}

	// Slow path: acknowledge immediately, then reply in the background so the
	// transport event loop is never blocked on a model generation. Only the
	// Master gets the "one moment" ack; VIPs get nothing until the reply.
	if msg.IsSelf {
		if err := h.msgr.Send(ctx, msg.Chat, ackMaster); err != nil {
			logging.Log(h.component, logging.SevWarn, "SEND", "Failed to send master ack; reply still queued in background",
				"to", msg.Chat, "error", err, "next", "check transport connection")
		}
	}
	h.disp.enqueue(inbound{
		id:        msg.ID,
		chat:      msg.Chat,
		senderJID: msg.Sender,
		userMsg:   msg.Text,
		isSelf:    msg.IsSelf,
		media:     msg.Media,
	})
}

// --- background dispatcher -------------------------------------------------

const (
	ackMaster = "_One moment, Sir..._"
	// rateLimitMasterMessage is delivered to the Master's own chat when the
	// model throttles the request; clark switches himself off at the same time.
	rateLimitMasterMessage = "🚨 Attention Sir!\n\nI have been silenced: the model is rate-limiting my requests. I have turned myself *Off* to stay reliable. Say _wake up buddy_ when you need me again."
)

// apologyFor names the failed stage so the human knows what broke instead of
// reading a bare "technical difficulties". Keeps butler voice; the full error
// goes to logs, never hidden.
func apologyFor(stage string) string {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "answering you"
	}
	return "_My apologies, Sir — I failed while " + stage + "._ The fault is mine, not yours. Please try again in a moment, and tell me exactly what you sent if it fails twice."
}

func mediaAckMessage(mediaType string) string {
	kind := strings.TrimSpace(mediaType)
	switch kind {
	case "image":
		return "_My apologies — I can only read text at the moment, so your image went unanswered._ If you add a caption I can reply to that, Sir."
	case "video":
		return "_My apologies — your video needs a caption for me to reply at the moment, Sir._ Please resend it with a line of text."
	case "gif":
		return "_My apologies — your GIF needs a caption for me to reply at the moment, Sir._ Please resend it with a line of text."
	case "document":
		return "_My apologies — your file needs a caption for me to reply at the moment, Sir._ Please resend it with a line of text."
	case "audio":
		return "_My apologies — I cannot transcribe audio yet, so your voice note went unanswered, Sir._ Please send it as text for now."
	case "sticker":
		return "_Noted, Sir._ — I cannot read stickers, but I am here for your message."
	case "":
		return "_My apologies — I received something I cannot read (empty message), Sir._ Please send it as text."
	default:
		return "_My apologies — I cannot read your " + kind + " at the moment, Sir._ Please resend it with a text caption."
	}
}

// inbound is one message awaiting a slow, model-backed reply.
type inbound struct {
	id        string
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
		logging.Log(d.component, logging.SevWarn, "DISPATCH", "Dispatcher closed; message refused instead of queued",
			"id", in.id, "chat", in.chat, "sender", in.senderJID,
			"next", "restart Clark; sender should resend")
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
		logging.Log(d.component, logging.SevWarn, "DISPATCH", "Sender queue full; message dropped instead of stalling transport",
			"id", in.id, "chat", in.chat, "sender", in.senderJID, "queue_depth", len(ch), "queue_cap", cap(ch),
			"preview", logging.Brief(in.userMsg, 80),
			"next", "sender should resend; dedup window still guards double-delivery")
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
// lines, the first attachment that could not be processed (nil when everything
// succeeded), and a human-readable failure detail for the sender notice.
func (d *dispatcher) processMedia(in inbound) (lines []string, failed *MediaAttachment, detail string) {
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
	fail := func(stage string, m MediaAttachment, err error, capability, human string) (*MediaAttachment, string) {
		logging.Log(d.component, logging.SevErr, "MEDIA",
			"Local media processing failed; degrading to caption-only fallback instead of ghosting",
			"id", in.id, "chat", in.chat, "sender", in.senderJID,
			"stage", stage, "type", m.Type, "mime", m.MIME, "name", m.Name,
			"capability", capability, "has_caption", strings.TrimSpace(in.userMsg) != "", "error", err,
			"next", "sender can resend with a text caption; check the local capability health")
		cpy := m
		return &cpy, human
	}

	if len(audio) > 0 {
		tr, ok := d.butler.(AudioTranscriber)
		for _, m := range audio {
			if !ok {
				f, h := fail("capability-missing", m, errLocalProcessorUnavailable, "AudioTranscriber", "voice-note transcription is not configured")
				return lines, f, h
			}
			text, err := tr.TranscribeVoice(d.ctx, m.MIME, m.Data)
			if err != nil {
				f, h := fail("transcribe", m, err, "AudioTranscriber", "voice-note transcription failed ("+err.Error()+")")
				return lines, f, h
			}
			addLine("voice note", text)
		}
	}

	if len(docs) > 0 {
		dg, ok := d.butler.(DocDigester)
		for _, m := range docs {
			if !ok {
				f, h := fail("capability-missing", m, errLocalProcessorUnavailable, "DocDigester", "document reading is not configured")
				return lines, f, h
			}
			digest, err := dg.DigestDocument(d.ctx, m.Name, string(m.Data))
			if err != nil {
				f, h := fail("digest", m, err, "DocDigester", "document reading failed ("+err.Error()+")")
				return lines, f, h
			}
			addLine("document "+m.Name+" | compacted", digest)
		}
	}

	if len(visual) > 0 {
		de, ok := d.butler.(MediaDescriber)
		if !ok {
			f, h := fail("capability-missing", visual[0], errLocalProcessorUnavailable, "MediaDescriber", "image description is not configured")
			return lines, f, h
		}
		desc, err := de.Describe(d.ctx, visual)
		if err != nil {
			f, h := fail("describe", visual[0], err, "MediaDescriber", "image description failed ("+err.Error()+")")
			return lines, f, h
		}
		prefix := visual[0].Type
		if prefix == "" || prefix == "image" && len(visual) > 1 {
			prefix = "image"
		}
		addLine(prefix, desc)
	}
	return lines, nil, ""
}

// errLocalProcessorUnavailable marks a missing local capability distinctly
// from a runtime failure in logs.
var errLocalProcessorUnavailable = errors.New("local processor not configured")

func (d *dispatcher) process(in inbound) {
	userMsg := in.userMsg
	if len(in.media) > 0 {
		lines, failed, detail := d.processMedia(in)
		if failed != nil {
			caption := strings.TrimSpace(userMsg)
			if caption == "" {
				// No caption to fall back on — acknowledge with kind text.
				if serr := d.msgr.Send(d.ctx, in.chat, mediaAckMessage(failed.Type)); serr != nil {
					logging.Log(d.component, logging.SevErr, "SEND", "Failed to send no-caption media ack; sender got no notice",
						"id", in.id, "to", in.chat, "sender", in.senderJID, "media", failed.Type, "cause", detail, "error", serr,
						"next", "check transport connection")
				} else {
					logging.Log(d.component, logging.SevInfo, "MESSAGE", "Uncaptioned media acked after processing failure",
						"id", in.id, "chat", in.chat, "sender", in.senderJID, "media", failed.Type, "cause", detail)
				}
				return
			}
			// Caption fallback: answer from the caption but explicitly tell
			// the sender which asset step failed and why.
			note := "_My apologies — I could not process your " + failed.Type + " just now (" + detail + ")._ I will answer based on your caption alone; resend the file if you need it read properly."
			if err := d.msgr.Send(d.ctx, in.chat, note); err != nil {
				logging.Log(d.component, logging.SevWarn, "SEND", "Failed to send asset-failure notice; answering from caption anyway",
					"id", in.id, "to", in.chat, "sender", in.senderJID, "media", failed.Type, "cause", detail, "error", err)
			}
		}
		for _, l := range lines {
			userMsg += "\n" + l
		}
		userMsg = strings.TrimSpace(userMsg)
	}
	if userMsg == "" {
		logging.Log(d.component, logging.SevWarn, "MESSAGE", "Message discarded after media; no caption text left to answer",
			"id", in.id, "chat", in.chat, "sender", in.senderJID,
			"next", "sender must resend with a text caption")
		return
	}
	reply, err := d.butler.Reply(d.ctx, in.senderJID, userMsg, in.isSelf)
	if err != nil {
		if errors.Is(err, ollama.ErrRateLimited) {
			logging.Log("OLLAMA", logging.SevErr, "RATELIMIT", "Model rate limited; master alerted and clark switched off",
				"id", in.id, "chat", in.chat, "sender", in.senderJID,
				"preview", logging.Brief(userMsg, 80), "error", err,
				"next", "master says wake up buddy when ready; check model quota")
			if serr := d.msgr.SendSelf(d.ctx, rateLimitMasterMessage); serr != nil {
				logging.Log(d.component, logging.SevErr, "SEND", "Failed to alert master about rate limit",
					"sender", in.senderJID, "error", serr, "cause", err)
			}
		} else {
			logging.Log("OLLAMA", logging.SevErr, "RESPONSE", "Model reply failed; sending staged apology",
				"id", in.id, "chat", in.chat, "sender", in.senderJID,
				"preview", logging.Brief(userMsg, 80), "error", err,
				"next", "sender should repeat the message; check model health")
		}
		if err := d.msgr.Send(d.ctx, in.chat, apologyFor("crafting my reply")); err != nil {
			logging.Log(d.component, logging.SevErr, "SEND", "Failed to send reply-failure apology; sender got no failure notice",
				"id", in.id, "to", in.chat, "sender", in.senderJID, "error", err, "cause", err,
				"next", "check transport connection")
		}
		return
	}
	if err := d.msgr.Send(d.ctx, in.chat, reply); err != nil {
		logging.Log(d.component, logging.SevErr, "SEND", "Model reply generated but delivery failed",
			"id", in.id, "to", in.chat, "sender", in.senderJID,
			"preview", logging.Brief(reply, 80), "error", err,
			"next", "check transport connection; reply was saved to history")
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
		logging.Log(h.component, logging.SevInfo, "ALERT", "Urgent bypass fired via kind-aware alerter",
			"chat", chat, "relation", relation)
	} else {
		if h.notifier != nil {
			if err := h.notifier.Notify(title, body); err != nil {
				logging.Log("CLARK", logging.SevWarn, "NOTIFY", "Desktop notification failed; falling back to self-chat message",
					"chat", chat, "relation", relation, "error", err,
					"next", "check desktop notifier config")
			}
		}
		if err := h.msgr.SendSelf(ctx, "🚨 "+title+"\n"+body); err != nil {
			logging.Log(h.component, logging.SevErr, "SEND", "Failed to send urgent alert to master self-chat",
				"chat", chat, "relation", relation, "error", err,
				"next", "check transport connection immediately")
		}
	}
	if err := h.msgr.Send(ctx, chat, "_One moment._ I've alerted the Master."); err != nil {
		logging.Log(h.component, logging.SevErr, "SEND", "Urgent alert fired but sender acknowledgement failed",
			"to", chat, "relation", relation, "error", err,
			"next", "master was still alerted; check transport connection")
	}
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
