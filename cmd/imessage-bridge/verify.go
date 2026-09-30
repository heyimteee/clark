package main

import (
	"database/sql"
	"strings"
	"time"
)

// AppleScript exiting 0 does not mean the message was delivered. Messages has
// documented failure modes where the script succeeds and nothing is sent — most
// notably on macOS 26, where it reports success while writing an empty, unjoined
// SMS row instead of delivering (the "ghost row"). Without a post-send check
// those are indistinguishable from success and the message is simply lost (#210).
//
// So every send is verified against chat.db before it is acked.
const (
	// defaultVerifyWindow is how long to wait for Messages to commit the
	// outgoing row. The script returning does not mean the row is written yet.
	defaultVerifyWindow = 8 * time.Second
	// defaultVerifyInterval is the poll cadence within that window.
	defaultVerifyInterval = 400 * time.Millisecond
)

// verifyWindow is this watcher's confirmation window, overridable so tests need
// not wait eight real seconds.
func (w *Watcher) verifyWindow() time.Duration {
	if w.vWindow > 0 {
		return w.vWindow
	}
	return defaultVerifyWindow
}

// verifyInterval is this watcher's confirmation poll cadence.
func (w *Watcher) verifyInterval() time.Duration {
	if w.vInterval > 0 {
		return w.vInterval
	}
	return defaultVerifyInterval
}

// deliveryOutcome is the result of one delivery attempt, and the only thing that
// decides whether another attempt is safe.
type deliveryOutcome string

const (
	// outcomeDelivered: the outgoing row was observed, so the message is on its
	// way. Safe to ack.
	outcomeDelivered deliveryOutcome = "delivered"
	// outcomeNotStarted: the transport refused before dispatching, so nothing was
	// sent. Safe to retry.
	outcomeNotStarted deliveryOutcome = "not_started"
	// outcomeGhost: Messages claimed success but wrote an empty unjoined row, so
	// nothing was delivered. Safe to retry, with a named cause.
	outcomeGhost deliveryOutcome = "ghost"
	// outcomeUnknown: the script succeeded but no row appeared in the window.
	// Delivery can be neither proved nor disproved. NEVER retry automatically —
	// the message may already be on the recipient's device.
	outcomeUnknown deliveryOutcome = "unknown"
)

// retrySafe reports whether another attempt risks a duplicate.
func (o deliveryOutcome) retrySafe() bool {
	return o == outcomeNotStarted || o == outcomeGhost
}

// classify converts an outcome to the wire classification the server understands.
func (o deliveryOutcome) classify() string {
	switch o {
	case outcomeNotStarted:
		return "not_started"
	case outcomeGhost:
		return "ghost"
	default:
		return "unknown"
	}
}

// preSendNote returns the current highest message ROWID, which scopes the
// verification query to rows this attempt produced.
func (w *Watcher) preSendNote() (int64, error) {
	return maxRowID(w.db)
}

// outgoingSince finds a row this send produced: outbound, newer than the
// pre-send watermark, addressed to the recipient.
//
// The text is matched loosely. Messages rewrites attributed bodies (emoji,
// formatting) and strips markup, so an exact comparison produces false
// negatives — and a false negative here means a delivered message is dead-lettered
// as "unknown", which is the expensive direction to be wrong in.
func outgoingSince(db *sql.DB, after int64, recipient, text string) (outgoingRow, bool, error) {
	rows, err := db.Query(`
		SELECT message.ROWID, COALESCE(message.text,''), COALESCE(handle.id,'')
		FROM message
		LEFT JOIN handle ON handle.ROWID = message.handle_id
		WHERE message.ROWID > ? AND message.is_from_me = 1
		  AND COALESCE(message.error, 0) = 0
		  AND (message.date_read IS NULL OR 1)
		ORDER BY message.ROWID ASC
		LIMIT 20`, after)
	if err != nil {
		return outgoingRow{}, false, err
	}
	defer rows.Close()

	needle := normaliseForCompare(text)
	for rows.Next() {
		var r outgoingRow
		if err := rows.Scan(&r.RowID, &r.Text, &r.Handle); err != nil {
			return outgoingRow{}, false, err
		}
		if !sameRecipient(r.Handle, recipient) {
			continue
		}
		// A row with no text, from this send, to this recipient, is the macOS 26
		// ghost-row signature: Messages wrote a placeholder instead of sending.
		if strings.TrimSpace(r.Text) == "" {
			return r, true, nil
		}
		if needle == "" || strings.Contains(normaliseForCompare(r.Text), needle) {
			return r, true, nil
		}
	}
	return outgoingRow{}, false, rows.Err()
}

// outgoingRow is a candidate message row observed after a send.
type outgoingRow struct {
	RowID  int64
	Text   string
	Handle string
}

// verify blocks until the send's row appears, the window expires, or ctx ends.
//
// ghost reports the macOS 26 signature: a row exists for this recipient but
// carries no text, meaning Messages wrote a placeholder instead of sending.
// found is false when the window expired with nothing observed, which is the
// genuinely unknown case.
func (w *Watcher) verify(after int64, recipient, text string) (ghost, found bool, err error) {
	deadline := time.Now().Add(w.verifyWindow())
	for {
		row, ok, qerr := outgoingSince(w.db, after, recipient, text)
		if qerr != nil {
			return false, false, qerr
		}
		if ok {
			return strings.TrimSpace(row.Text) == "", true, nil
		}
		if time.Now().After(deadline) {
			return false, false, nil
		}
		time.Sleep(w.verifyInterval())
	}
}

// normaliseForCompare folds a message body to something comparable after
// Messages' own rewriting.
//
// Punctuation is dropped rather than turned into a space boundary, so contractions
// and hyphenation match ("I'll be there" ≡ "ill be there"). Genuine whitespace
// collapses to a single space, so word boundaries survive ("not for you" does not
// match "for you").
//
// A false negative here is the expensive direction: a delivered message would be
// dead-lettered as unverified, so the comparison is deliberately loose.
func normaliseForCompare(s string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSpace = false
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			// Punctuation: dropped without introducing a boundary.
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// digitsOnly reduces a handle to its digits, so "+62 812-6785-8909" and
// "6281267858909" compare equal. Numbers carry no word boundaries, so every
// non-digit — including spacing and the + prefix — is discarded.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sameRecipient compares handles loosely, tolerating the + prefix and spacing
// that the bridge, the store, and chat.db each format differently. A handle with
// no digits (an email address) falls back to a plain trimmed comparison.
func sameRecipient(a, b string) bool {
	da, db := digitsOnly(a), digitsOnly(b)
	if da != "" && db != "" {
		return da == db
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
