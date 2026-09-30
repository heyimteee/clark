package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Outbound delivery states. A row only advances forward: pending is claimable,
// picked is leased to a bridge, and dead is the terminal state once the retry
// budget is spent. `dead` rows are retained rather than deleted so a failed
// iMessage is visible instead of silently disappearing (#210).
const (
	OutboundPending = "pending"
	OutboundPicked  = "picked"
	OutboundDead    = "dead"
)

// Outbound failure classifications, set by the bridge after a delivery attempt.
// The distinction that matters is retry-safety: `not_started` proves nothing was
// sent, `unknown` proves nothing — and must never be retried automatically,
// because the message may already be on the recipient's device (#210).
const (
	// FailureNotStarted means the transport refused before dispatching.
	FailureNotStarted = "not_started"
	// FailureUnknown means the outcome could not be established either way.
	FailureUnknown = "unknown"
	// FailureGhost means Messages reported success but wrote an empty unjoined
	// row instead of delivering — a known macOS 26 behaviour.
	FailureGhost = "ghost"
)

// OutboundLease is how long a claimed row stays leased before another bridge
// (or the same one after a restart) may re-claim it. Long enough to cover a
// send plus its verification window, short enough that a crashed bridge does not
// strand the message for long.
const OutboundLease = 2 * time.Minute

// MaxOutboundAttempts is the retry budget before a message is dead-lettered.
const MaxOutboundAttempts = 5

// OutboundMessage is one iMessage awaiting bridge delivery.
type OutboundMessage struct {
	ID        int64  `json:"id"`
	Recipient string `json:"recipient"`
	Text      string `json:"text"`
	// Attempts counts delivery attempts made, including the claim in hand.
	Attempts int `json:"attempts"`
	// LastError is the most recent failure reason, empty when none.
	LastError string `json:"last_error,omitempty"`
}

// EnqueueIMessage queues an outbound iMessage and returns its row id.
func (s *Store) EnqueueIMessage(recipient, text string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO imessage_outbound (recipient, text, status) VALUES (?, ?, 'pending')`, recipient, text)
	if err != nil {
		return 0, fmt.Errorf("fail to enqueue iMessage: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("fail to read iMessage id: %w", err)
	}
	return id, nil
}

// NextIMessageOutbound claims the next deliverable message and leases it.
//
// Three things are claimable, which is what stops messages being lost (#210):
//   - pending rows that are due for another attempt
//   - pending rows whose backoff has not yet elapsed (excluded by the WHERE)
//   - picked rows whose lease expired, meaning a bridge died mid-send
//
// Rows parked with an `unknown` failure are never returned: the message may
// already have been delivered, so re-claiming it risks a duplicate send.
func (s *Store) NextIMessageOutbound() (OutboundMessage, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Cheap read-only check first so an empty queue does not take a write lock on
	// every poll.
	var claimable int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM imessage_outbound
		 WHERE (status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
		    OR (status = 'picked' AND picked_at < ?)`,
		nowUTC(), time.Now().Add(-OutboundLease).UTC().Format("2006-01-02 15:04:05"),
	).Scan(&claimable)
	if err != nil {
		return OutboundMessage{}, false, fmt.Errorf("fail to check claimable iMessages: %w", err)
	}
	if claimable == 0 {
		return OutboundMessage{}, false, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OutboundMessage{}, false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var msg OutboundMessage
	var lastErr sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT id, recipient, text, attempts, last_error FROM imessage_outbound
		 WHERE (status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
		    OR (status = 'picked' AND picked_at < ?)
		 ORDER BY id ASC LIMIT 1`,
		nowUTC(), time.Now().Add(-OutboundLease).UTC().Format("2006-01-02 15:04:05"),
	).Scan(&msg.ID, &msg.Recipient, &msg.Text, &msg.Attempts, &lastErr)
	if err != nil {
		if err == sql.ErrNoRows {
			return OutboundMessage{}, false, nil
		}
		return OutboundMessage{}, false, fmt.Errorf("fail to load claimable iMessage: %w", err)
	}
	msg.LastError = lastErr.String

	if _, err := tx.ExecContext(ctx,
		`UPDATE imessage_outbound
		    SET status = 'picked', picked_at = CURRENT_TIMESTAMP,
		        attempts = attempts + 1, next_attempt_at = NULL
		  WHERE id = ?`, msg.ID); err != nil {
		return OutboundMessage{}, false, fmt.Errorf("fail to claim iMessage: %w", err)
	}
	// Attempts is reported including this claim, so the bridge can compare it
	// directly against the budget: Attempts == MaxOutboundAttempts means the
	// last permitted try is the one just made.
	msg.Attempts++

	if err := tx.Commit(); err != nil {
		return OutboundMessage{}, false, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return msg, true, nil
}

// AckIMessage removes a delivered iMessage from the queue.
func (s *Store) AckIMessage(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM imessage_outbound WHERE id = ?`, id); err != nil {
		return fmt.Errorf("fail to ack iMessage: %w", err)
	}
	return nil
}

// FailIMessage records a failed attempt.
//
// retryable outcomes schedule another attempt after the caller's backoff;
// otherwise the row is dead-lettered with its reason. A `not_started` or `ghost`
// failure can be retried because the transport proved nothing was delivered. An
// `unknown` failure never is: the message may already be on the device, and a
// blind retry would duplicate it (#210).
func (s *Store) FailIMessage(id int64, reason, classification string, retryAt time.Time, exhausted bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	retriable := !exhausted && classification != FailureUnknown
	if retriable {
		_, err := s.db.ExecContext(ctx,
			`UPDATE imessage_outbound
			    SET status = 'pending', picked_at = NULL, last_error = ?, next_attempt_at = ?
			  WHERE id = ?`, reason, retryAt.UTC().Format("2006-01-02 15:04:05"), id)
		if err != nil {
			return fmt.Errorf("fail to reschedule iMessage: %w", err)
		}
		return nil
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE imessage_outbound
		    SET status = 'dead', picked_at = NULL, last_error = ?
		  WHERE id = ?`, reason, id)
	if err != nil {
		return fmt.Errorf("fail to dead-letter iMessage: %w", err)
	}
	return nil
}

// DeadOutbound is one message that could not be delivered.
type DeadOutbound struct {
	ID        int64     `json:"id"`
	Recipient string    `json:"recipient"`
	Text      string    `json:"text"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
}

// DeadIMessages returns dead-lettered messages, newest first, so a failure is
// visible rather than silent (#210).
func (s *Store) DeadIMessages(limit int) ([]DeadOutbound, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, recipient, text, attempts, COALESCE(last_error,''), created_at
		   FROM imessage_outbound WHERE status = 'dead' ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("fail to load dead iMessages: %w", err)
	}
	defer rows.Close()

	var out []DeadOutbound
	for rows.Next() {
		var d DeadOutbound
		if err := rows.Scan(&d.ID, &d.Recipient, &d.Text, &d.Attempts, &d.LastError, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("fail to scan dead iMessage: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// OutboundCounts summarises the queue for /status: how many messages are waiting,
// in flight, or dead-lettered.
type OutboundCounts struct {
	Pending int `json:"pending"`
	Picked  int `json:"picked"`
	Dead    int `json:"dead"`
}

// OutboundQueueCounts returns the current queue composition.
func (s *Store) OutboundQueueCounts() (OutboundCounts, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var c OutboundCounts
	err := s.db.QueryRowContext(ctx,
		`SELECT
		   COALESCE(SUM(status = 'pending'), 0),
		   COALESCE(SUM(status = 'picked'), 0),
		   COALESCE(SUM(status = 'dead'), 0)
		 FROM imessage_outbound`).Scan(&c.Pending, &c.Picked, &c.Dead)
	if err != nil {
		return OutboundCounts{}, fmt.Errorf("fail to count iMessages: %w", err)
	}
	return c, nil
}

// ClearDeadIMessage removes a dead-lettered message the Master has acknowledged.
func (s *Store) ClearDeadIMessage(id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `DELETE FROM imessage_outbound WHERE id = ? AND status = 'dead'`, id); err != nil {
		return fmt.Errorf("fail to clear dead iMessage: %w", err)
	}
	return nil
}

// nowUTC is the SQLite timestamp format used for every date column here.
func nowUTC() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}

// RetryDelay is the exponential backoff before attempt n+1, capped so a message
// that keeps failing still surfaces within a reasonable window.
func RetryDelay(attempts int) time.Duration {
	const base = 30 * time.Second
	const cap = 30 * time.Minute
	d := base
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= cap {
			return cap
		}
	}
	return d
}
