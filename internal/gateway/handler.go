package gateway

import (
	"context"
	"errors"
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
	commands  []command
	disp      *dispatcher

	// dedup tracks recently processed message IDs to prevent re-delivery
	// on bridge restarts. Entries are evicted after dedupTTL.
	dedupMu sync.Mutex
	dedup   map[string]time.Time
}

// NewHandler wires the pipeline around its dependencies. component names the
// transport in logs (e.g. "WHATSAPP"). bypassPhrase is the command word that
// triggers an urgent alert; empty falls back to "get him to me".
func NewHandler(component string, msgr Messenger, butler Butler, notifier Notifier, bypassPhrase string) *Handler {
	h := &Handler{
		component: component,
		msgr:      msgr,
		butler:    butler,
		notifier:  notifier,
		disp:      newDispatcher(component, butler, msgr),
		dedup:     make(map[string]time.Time),
	}
	bypass := strings.TrimSpace(bypassPhrase)
	if bypass == "" {
		bypass = "get him to me"
	}
	h.commands = []command{
		{phrase: bypass, run: h.alert},
	}
	return h
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
		logging.Log(h.component, logging.SevWarn, "MESSAGE", "Message discarded", "reason", "no text content")
		return
	}

	// Strip Clark's own branding so a sender cannot impersonate him in
	// stored history (#58).
	msg.Text = SanitizeInbound(msg.Text)

	ctx := context.Background()
	lower := strings.ToLower(msg.Text)
	for _, c := range h.commands {
		if strings.Contains(lower, c.phrase) {
			c.run(ctx, msg.Chat, relation)
			return
		}
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

// inbound is one message awaiting a slow, model-backed reply.
type inbound struct {
	chat      string
	senderJID string
	userMsg   string
	isSelf    bool
}

// dispatcher runs one serial worker goroutine per sender so replies arrive in
// order, while never blocking the transport event loop on a slow generation.
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
}

func newDispatcher(component string, butler Butler, msgr Messenger) *dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &dispatcher{
		ctx:       ctx,
		cancel:    cancel,
		queues:    make(map[string]chan inbound),
		component: component,
		butler:    butler,
		msgr:      msgr,
	}
}

// enqueue hands a message to its sender's worker, creating the worker on first
// use. It blocks only while that sender's queue is full (bounded at 16).
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
		go d.worker(ch)
	}

	select {
	case ch <- in:
	case <-d.ctx.Done():
	}
}

func (d *dispatcher) worker(ch <-chan inbound) {
	defer d.wg.Done()
	for in := range ch {
		d.process(in)
	}
}

func (d *dispatcher) process(in inbound) {
	reply, err := d.butler.Reply(d.ctx, in.senderJID, in.userMsg, in.isSelf)
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
	// Prefer the kind-aware alert notifier (delivers to WhatsApp, web chat, and
	// voice in one shot); fall back to the legacy two-arg Notify + self-chat.
	if an, ok := h.notifier.(AlertNotifier); ok && an != nil {
		an.Alert(ctx, "bypass", title, body)
	} else {
		if err := h.notifier.Notify(title, body); err != nil {
			logging.Log("CLARK", logging.SevWarn, "NOTIFY", "Notification failed", "error", err)
		}
		h.msgr.SendSelf(ctx, "🚨 "+title+"\n"+body)
	}
	h.msgr.Send(ctx, chat, "_One moment._ I've alerted the Master.")
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
