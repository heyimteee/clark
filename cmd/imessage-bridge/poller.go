package main

import (
	"context"
	"time"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/store"
)

// outboundClient is the slice of the clark API the poller needs.
type outboundClient interface {
	NextOutbound(ctx context.Context) (store.OutboundMessage, bool, error)
	Ack(ctx context.Context, id int64) error
	Fail(ctx context.Context, id int64, reason, classification string, attempts int) error
}

// Poller drains clark's outbound queue: claim one message, deliver it via the
// Sender, verify it actually landed, then ack or report the failure.
//
// Every attempt is classified before anything is acked (#210). A message is only
// acked once chat.db shows the outgoing row; a failure the server can safely
// retry is rescheduled with backoff, and anything whose outcome is genuinely
// unknown is reported without a retry, because the message may already be on the
// recipient's device.
type Poller struct {
	client   outboundClient
	sender   Sender
	verifier deliveryVerifier
	interval time.Duration
	now      func() time.Time
}

// deliveryVerifier confirms a send produced a real outgoing row.
type deliveryVerifier interface {
	// preSendNote captures the state a later verification is compared against.
	preSendNote() (int64, error)
	// verify waits for the send's row to appear. ghost is the macOS 26
	// placeholder-row signature; found is false when the window expired with
	// nothing observed, which is the genuinely unknown case.
	verify(after int64, recipient, text string) (ghost, found bool, err error)
}

// NewPoller wires the outbound loop around the client and sender.
func NewPoller(client outboundClient, sender Sender, interval time.Duration) *Poller {
	return &Poller{client: client, sender: sender, interval: interval, now: time.Now}
}

// WithVerifier attaches the chat.db verifier. Without one the poller falls back
// to trusting the sender, which is the pre-#210 behaviour and is only correct
// for tests and senders that cannot fail silently.
func (p *Poller) WithVerifier(v deliveryVerifier) *Poller {
	p.verifier = v
	return p
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *Poller) pollOnce(ctx context.Context) {
	msg, ok, err := p.client.NextOutbound(ctx)
	if err != nil {
		logging.Log("BRIDGE", logging.SevErr, "OUTBOUND", "Failed to claim outbound message", "error", err)
		return
	}
	if !ok {
		return
	}

	outcome, detail := p.deliver(ctx, msg)

	switch outcome {
	case outcomeDelivered:
		if err := p.client.Ack(ctx, msg.ID); err != nil {
			logging.Log("BRIDGE", logging.SevErr, "OUTBOUND", "Delivered but ack failed; will be re-claimed after the lease expires",
				"id", msg.ID, "error", err,
				"next", "the lease makes this safe; a duplicate is prevented by verification")
			return
		}
		logging.Log("BRIDGE", logging.SevInfo, "OUTBOUND", "Message delivered", "id", msg.ID, "to", msg.Recipient)

	default:
		attempts := msg.Attempts
		reason := detail
		if reason == "" {
			reason = string(outcome)
		}
		exhausted := attempts >= store.MaxOutboundAttempts

		if err := p.client.Fail(ctx, msg.ID, reason, outcome.classify(), attempts); err != nil {
			logging.Log("BRIDGE", logging.SevErr, "OUTBOUND", "Delivery failed and the failure could not be reported",
				"id", msg.ID, "to", msg.Recipient, "outcome", outcome, "error", err,
				"next", "the lease will re-claim this message; check server reachability")
			return
		}
		logging.Log("BRIDGE", logging.SevWarn, "OUTBOUND", "Delivery did not succeed",
			"id", msg.ID, "to", msg.Recipient, "outcome", outcome, "attempts", attempts,
			"exhausted", exhausted, "retry_safe", outcome.retrySafe(), "detail", detail,
			"next", describeNext(outcome, exhausted))
	}
}

// deliver attempts one send and classifies the result.
func (p *Poller) deliver(ctx context.Context, msg store.OutboundMessage) (deliveryOutcome, string) {
	var after int64
	if p.verifier != nil {
		note, err := p.verifier.preSendNote()
		if err != nil {
			// Without a reliable watermark we cannot verify, so we must not
			// claim success. Treat it as unknown: reported, never auto-retried.
			return outcomeUnknown, "could not snapshot chat.db before sending: " + err.Error()
		}
		after = note
	}

	if err := p.sender.Send(msg.Recipient, msg.Text); err != nil {
		// A non-zero osascript exit means the transport refused before
		// dispatching, so nothing was sent and a retry cannot duplicate.
		return outcomeNotStarted, err.Error()
	}

	if p.verifier == nil {
		return outcomeDelivered, ""
	}

	ghost, found, err := p.verifier.verify(after, msg.Recipient, msg.Text)
	if err != nil {
		return outcomeUnknown, "could not read chat.db after sending: " + err.Error()
	}
	if !found {
		return outcomeUnknown, "the send script reported success but no outgoing row appeared in chat.db within " +
			defaultVerifyWindow.String() + "; delivery cannot be confirmed"
	}
	if ghost {
		return outcomeGhost, "Messages wrote an empty unjoined row instead of delivering; a known macOS 26 behaviour"
	}
	return outcomeDelivered, ""
}

// describeNext states what happens to a failed message, so the log is actionable
// without cross-referencing the code.
func describeNext(outcome deliveryOutcome, exhausted bool) string {
	switch {
	case outcome == outcomeUnknown:
		return "not retried: the message may already be delivered, so a retry could duplicate it"
	case exhausted:
		return "dead-lettered after the retry budget; visible via GET /outbound/dead"
	case outcome == outcomeGhost:
		return "retrying with backoff: Messages did not actually deliver"
	default:
		return "retrying with backoff: the send never dispatched"
	}
}
