// Package imessage is clark's iMessage transport: a bridge-facing HTTP server
// that feeds inbound messages into the shared gateway pipeline and queues
// outbound messages for a macOS bridge to deliver.
package imessage

import "time"

// InboundMessage is one message the bridge POSTs to /inbound. The bridge
// already filtered outbound self-messages and non-text rows, so this is the
// raw handle as it appears in chat.db.
type InboundMessage struct {
	ID        string         `json:"id"`
	Handle    string         `json:"handle"`
	Text      string         `json:"text"`
	IsSelf    bool           `json:"is_self"`
	Timestamp time.Time      `json:"timestamp"`
	MediaType string         `json:"media_type,omitempty"`
	Media     []InboundMedia `json:"media,omitempty"`
}

// InboundMedia is one attachment for an inbound message.
type InboundMedia struct {
	Type string `json:"type"`
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

// AckRequest is the bridge's POST /ack body confirming delivery of one
// outbound message.
type AckRequest struct {
	ID int64 `json:"id"`
}
