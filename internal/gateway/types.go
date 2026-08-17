// Package gateway is the transport-neutral message pipeline every channel
// clark speaks on routes through (WhatsApp today, iMessage next). It owns the
// gating rules, urgent commands, fast path, and the serial reply dispatcher so
// no transport needs to reimplement the brain contract.
package gateway

import (
	"context"
	"time"
)

// Butler is the conversational brain the handler replies through.
type Butler interface {
	// Prehandle consumes fast deterministic commands (views, mutations) with a
	// hardcoded reply. It returns a message and true when it handled the input.
	Prehandle(sender, text string, isSelf bool) (string, bool, error)
	Reply(ctx context.Context, sender, text string, isSelf bool) (string, error)
	Relation(sender string) (string, bool)
	Enabled() bool
	// EnabledFor reports whether a specific sender may reach clark. A per-sender
	// status override wins; otherwise the global status applies.
	EnabledFor(sender string) bool
}

// Notifier raises attention for urgent commands.
type Notifier interface {
	Notify(title, body string) error
}

// AlertNotifier is an optional richer notifier that delivers alerts across
// every channel (WhatsApp, web chat, voice) with a kind tag. Transports whose
// notifier implements it get kind-aware alert delivery for urgent commands;
// others fall back to plain Notify.
type AlertNotifier interface {
	Alert(ctx context.Context, kind, title, body string)
}

// CommandFunc runs an urgent command, e.g. "get him to me".
type CommandFunc func(ctx context.Context, chat, relation string)

type command struct {
	phrase string
	run    CommandFunc
}

// Message is one inbound message, neutral to the transport it arrived on.
// Sender is who wrote it and Chat is where the reply must be delivered; they
// coincide in private chats but differ in groups.
type Message struct {
	// ID is the transport message id (used for deduplication where needed).
	ID string
	// Sender identifies who wrote the message (JID, iMessage handle, ...).
	Sender string
	// Chat is the conversation replies must be delivered to.
	Chat string
	// Text is the message body.
	Text string
	// Timestamp is when the message was originally sent (from the transport).
	// Used for staleness filtering — messages older than a threshold are dropped.
	Timestamp time.Time
	// IsSelf reports whether this is the Master's own chat.
	IsSelf bool
	// IsGroup reports whether this arrived in a group conversation.
	IsGroup bool
}

// Messenger delivers replies through a transport.
type Messenger interface {
	// Self returns clark's own identity on the transport.
	Self() string
	// Send delivers text to a chat.
	Send(ctx context.Context, chat, text string) error
	// SendSelf delivers text to clark's own chat.
	SendSelf(ctx context.Context, text string) error
}
