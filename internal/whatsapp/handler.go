package whatsapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/ollama"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Butler is the conversational brain the handler replies through.
type Butler interface {
	// Prehandle consumes fast deterministic commands (views, mutations) with a
	// hardcoded reply. It returns a message and true when it handled the input.
	Prehandle(senderJID, text string, isSelf bool) (string, bool, error)
	Reply(ctx context.Context, senderJID, text string, isSelf bool) (string, error)
	Relation(jid string) (string, bool)
	Enabled() bool
	// EnabledFor reports whether a specific sender may reach clark. A per-sender
	// status override wins; otherwise the global status applies.
	EnabledFor(jid string) bool
}

// Notifier raises attention for urgent commands.
type Notifier interface {
	Notify(title, body string) error
}

// CommandFunc runs an urgent command, e.g. "get him to me".
type CommandFunc func(ctx context.Context, sender types.JID, relation string)

type command struct {
	phrase string
	run    CommandFunc
}

// Handler routes WhatsApp events through the message pipeline.
type Handler struct {
	msgr        Messenger
	butler      Butler
	notifier    Notifier
	echo        *EchoTracker
	connectedAt time.Time
	commands    []command
	disp        *dispatcher
}

// NewHandler wires the pipeline around its dependencies. bypassPhrase is the
// command word that triggers an urgent alert; empty falls back to "get him to me".
func NewHandler(msgr Messenger, butler Butler, notifier Notifier, echo *EchoTracker, connectedAt time.Time, bypassPhrase string) *Handler {
	h := &Handler{
		msgr:        msgr,
		butler:      butler,
		notifier:    notifier,
		echo:        echo,
		connectedAt: connectedAt,
		disp:        newDispatcher(butler, msgr),
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

	isSelf := false
	if v.Info.IsFromMe {
		if !h.msgr.IsSelfChat(v.Info.Chat) {
			return
		}
		isSelf = true
	}

	sender := h.msgr.ResolveSender(v)
	senderStr := sender.String()
	relation, isVIP := h.butler.Relation(senderStr)

	var userMsg string
	if conversation := v.Message.GetConversation(); conversation != "" {
		userMsg = conversation
	} else if extendedMessage := v.Message.GetExtendedTextMessage(); extendedMessage != nil {
		userMsg = extendedMessage.GetText()
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

	// The Master's own chat is always trusted (whether clark is enabled or the
	// Master is registered as a VIP), so a fresh install can be bootstrapped
	// with wake/context/VIP commands. Everyone else must be an enabled VIP in a
	// private chat.
	if !isSelf && (!h.butler.EnabledFor(senderStr) || !isVIP || v.Info.IsGroup) {
		return
	}

	if userMsg == "" {
		logging.Log("WHATSAPP", logging.SevWarn, "MESSAGE", "Message discarded", "reason", "no text content")
		return
	}

	ctx := context.Background()
	lower := strings.ToLower(userMsg)
	for _, c := range h.commands {
		if strings.Contains(lower, c.phrase) {
			c.run(ctx, sender, relation)
			return
		}
	}

	// Fast path: deterministic commands answered with hardcoded messages.
	if reply, handled, err := h.butler.Prehandle(senderStr, userMsg, isSelf); err != nil {
		logging.Log("OLLAMA", logging.SevErr, "RESPONSE", "Prehandle error", "error", err)
		h.msgr.Send(ctx, sender, apologyMessage)
		return
	} else if handled {
		if err := h.msgr.Send(ctx, sender, reply); err != nil {
			logging.Log("WHATSAPP", logging.SevErr, "SEND", "Failed to send fast reply", "to", sender.User, "error", err)
		}
		return
	}

	// Slow path: acknowledge immediately, then reply in the background so the
	// WhatsApp event loop is never blocked on a model generation. Only the
	// Master gets the "one moment" ack; VIPs get nothing until the reply.
	if isSelf {
		if err := h.msgr.Send(ctx, sender, ackMaster); err != nil {
			logging.Log("WHATSAPP", logging.SevWarn, "SEND", "Failed to send ack", "to", sender.User, "error", err)
		}
	}
	h.disp.enqueue(inbound{
		sender:    sender,
		senderJID: senderStr,
		userMsg:   userMsg,
		isSelf:    isSelf,
	})
}

// --- background dispatcher -------------------------------------------------

const (
	ackMaster      = "_One moment, Master..._"
	apologyMessage = "_My apologies, but I am experiencing technical difficulties._ Please try again later."
	// rateLimitMasterMessage is delivered to the Master's own chat when the
	// model throttles the request; clark switches himself off at the same time.
	rateLimitMasterMessage = "🚨 Attention Master!\n\nI have been silenced: the model is rate-limiting my requests. I have turned myself *Off* to stay reliable. Say _wake up buddy_ when you need me again."
)

// inbound is one message awaiting a slow, model-backed reply.
type inbound struct {
	sender    types.JID
	senderJID string
	userMsg   string
	isSelf    bool
}

// dispatcher runs one serial worker goroutine per sender so replies arrive in
// order, while never blocking the event loop on a slow model generation.
type dispatcher struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	queues map[string]chan inbound
	wg     sync.WaitGroup
	butler Butler
	msgr   Messenger
}

func newDispatcher(butler Butler, msgr Messenger) *dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &dispatcher{
		ctx:    ctx,
		cancel: cancel,
		queues: make(map[string]chan inbound),
		butler: butler,
		msgr:   msgr,
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
				logging.Log("WHATSAPP", logging.SevErr, "SEND", "Failed to alert master about rate limit", "error", serr)
			}
		} else {
			logging.Log("OLLAMA", logging.SevErr, "RESPONSE", "AI response error", "error", err)
		}
		if err := d.msgr.Send(d.ctx, in.sender, apologyMessage); err != nil {
			logging.Log("WHATSAPP", logging.SevErr, "SEND", "Failed to send apology", "to", in.sender.User, "error", err)
		}
		return
	}
	if err := d.msgr.Send(d.ctx, in.sender, reply); err != nil {
		logging.Log("WHATSAPP", logging.SevErr, "SEND", "Failed to deliver reply", "to", in.sender.User, "error", err)
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

func (h *Handler) alert(ctx context.Context, sender types.JID, relation string) {
	if err := h.notifier.Notify("Attention Sir!", relation+" needs you!"); err != nil {
		logging.Log("CLARK", logging.SevWarn, "NOTIFY", "Notification failed", "error", err)
	}
	h.msgr.SendSelf(ctx, "🚨 Attention Master!\n"+relation+" needs you!")
	h.msgr.Send(ctx, sender, "_One moment._ I've alerted the Master.")
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
		"msg", content)
}

func logReply(toNumber, content string) {
	logging.Log("WHATSAPP", logging.SevInfo, "SEND", "Message sent",
		"to", toNumber,
		"msg", content)
}
