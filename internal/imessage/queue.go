package imessage

import (
	"errors"
	"time"

	"github.com/heyimteee/clark/internal/store"
)

var (
	// errEmptyRecipient guards against queueing a delivery with no target.
	errEmptyRecipient = errors.New("empty iMessage recipient")
	// errNoSelfHandle guards against SendSelf without a configured own handle.
	errNoSelfHandle = errors.New("IMESSAGE_SELF_HANDLE is not set")
)

// OutboundStore persists outbound iMessages until the bridge delivers them.
// store.Store implements it; the interface keeps the transport testable.
type OutboundStore interface {
	// EnqueueIMessage queues an outbound message and returns its row id.
	EnqueueIMessage(recipient, text string) (int64, error)
	// NextIMessageOutbound claims the oldest pending message (marks it picked).
	NextIMessageOutbound() (store.OutboundMessage, bool, error)
	// AckIMessage marks a picked message delivered.
	AckIMessage(id int64) error
	// StaleIMessageOutboundIDs returns picked-but-unacked ids older than maxAge.
	StaleIMessageOutboundIDs(maxAge time.Duration) ([]int64, error)
}
