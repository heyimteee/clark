package imessage

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
//
// The shape of this interface is the retry contract (#210). A delivery attempt
// ends in exactly one of three ways, and the distinction that matters is whether
// another attempt is safe:
//
//	AckIMessage      — the message is on the device; done
//	FailIMessage     — the attempt failed; retry only if classification permits
//	DeadIMessages    — exhausted, retained so the failure stays visible
type OutboundStore interface {
	// EnqueueIMessage queues an outbound message and returns its row id.
	EnqueueIMessage(recipient, text string) (int64, error)
	// NextIMessageOutbound claims the next deliverable message and leases it.
	// Includes rows whose lease expired because a bridge died mid-send.
	NextIMessageOutbound() (store.OutboundMessage, bool, error)
	// AckIMessage removes a delivered iMessage from the queue.
	AckIMessage(id int64) error
	// FailIMessage records a failed attempt. retryAt carries the backoff when
	// exhausted is false; a non-retryable classification is dead-lettered
	// regardless, because the message may already have been delivered.
	FailIMessage(id int64, reason, classification string, retryAt time.Time, exhausted bool) error
	// DeadIMessages returns undeliverable messages so they can be surfaced.
	DeadIMessages(limit int) ([]store.DeadOutbound, error)
	// OutboundQueueCounts summarises the queue for health reporting.
	OutboundQueueCounts() (store.OutboundCounts, error)
}

// ReportDead surfaces dead-lettered iMessages to the Master through the alert
// fan-out. Failures are returned rather than swallowed so the caller can log a
// useful reason.
func ReportDead(ctx context.Context, out OutboundStore, notify func(context.Context, string) error) ([]store.DeadOutbound, error) {
	dead, err := out.DeadIMessages(20)
	if err != nil {
		return nil, err
	}
	if len(dead) == 0 || notify == nil {
		return dead, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sir, %d iMessage%s could not be delivered:", len(dead), plural(len(dead)))
	for _, d := range dead {
		fmt.Fprintf(&b, "\n- to %s (%d attempts): %s", d.Recipient, d.Attempts, d.LastError)
	}
	_ = notify(ctx, b.String())
	return dead, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
