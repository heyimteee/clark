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
}

// Poller drains clark's outbound queue: claim one message, deliver it via the
// Sender, then ack it. A failed delivery leaves the message picked and unacked;
// it is never re-served (the queue guarantees at-most-once), and stale picks
// surface in clark's logs for manual reconciliation.
type Poller struct {
	client   outboundClient
	sender   Sender
	interval time.Duration
}

// NewPoller wires the outbound loop around the client and sender.
func NewPoller(client outboundClient, sender Sender, interval time.Duration) *Poller {
	return &Poller{client: client, sender: sender, interval: interval}
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

	if err := p.sender.Send(msg.Recipient, msg.Text); err != nil {
		logging.Log("BRIDGE", logging.SevErr, "OUTBOUND", "Delivery failed; leaving unacked", "id", msg.ID, "to", msg.Recipient, "error", err)
		return
	}

	if err := p.client.Ack(ctx, msg.ID); err != nil {
		logging.Log("BRIDGE", logging.SevErr, "OUTBOUND", "Delivered but ack failed", "id", msg.ID, "error", err)
		return
	}
	logging.Log("BRIDGE", logging.SevInfo, "OUTBOUND", "Message delivered", "id", msg.ID, "to", msg.Recipient)
}
