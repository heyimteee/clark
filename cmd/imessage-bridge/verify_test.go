package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/store"
)

// fakeVerifier stands in for chat.db so the poller's decision table can be
// exercised without a real Messages database.
type fakeVerifier struct {
	after     int64
	noteErr   error
	ghost     bool
	found     bool
	verifyErr error
	calls     int
}

func (v *fakeVerifier) preSendNote() (int64, error) {
	if v.noteErr != nil {
		return 0, v.noteErr
	}
	return v.after, nil
}

func (v *fakeVerifier) verify(int64, string, string) (bool, bool, error) {
	v.calls++
	if v.verifyErr != nil {
		return false, false, v.verifyErr
	}
	return v.ghost, v.found, nil
}

func oneMessage() []store.OutboundMessage {
	return []store.OutboundMessage{{ID: 1, Recipient: "+6281267858909", Text: "hello", Attempts: 1}}
}

// TestPollerAcksOnlyWhenVerified is the core change: a message is acked only
// after chat.db shows the outgoing row, so a script that succeeds while delivering
// nothing can no longer be reported as sent (#210).
func TestPollerAcksOnlyWhenVerified(t *testing.T) {
	fake := &fakeOutboundClient{queue: oneMessage()}
	sender := &fakeSender{}
	p := NewPoller(fake, sender, 0).WithVerifier(&fakeVerifier{found: true})

	p.pollOnce(context.Background())

	if len(fake.acked) != 1 || fake.acked[0] != 1 {
		t.Errorf("acked = %v, want [1] after a verified send", fake.acked)
	}
	if len(fake.failed) != 0 {
		t.Errorf("failed = %v, want none for a verified send", fake.failed)
	}
}

// TestPollerDoesNotAckUnverifiedSend proves an unverifiable send is reported, not
// acked, and is classified unknown so it is never retried into a duplicate.
func TestPollerDoesNotAckUnverifiedSend(t *testing.T) {
	fake := &fakeOutboundClient{queue: oneMessage()}
	sender := &fakeSender{}
	p := NewPoller(fake, sender, 0).WithVerifier(&fakeVerifier{found: false})

	p.pollOnce(context.Background())

	if len(fake.acked) != 0 {
		t.Errorf("acked = %v, want none; delivery was never confirmed", fake.acked)
	}
	if len(fake.failed) != 1 {
		t.Fatalf("failed = %v, want one report", fake.failed)
	}
	if fake.failed[0].classification != "unknown" {
		t.Errorf("classification = %q, want unknown", fake.failed[0].classification)
	}
}

// TestPollerClassifiesGhostRow pins the macOS 26 signature: Messages reports
// success but writes an empty unjoined row, so nothing was delivered and a retry
// is safe.
func TestPollerClassifiesGhostRow(t *testing.T) {
	fake := &fakeOutboundClient{queue: oneMessage()}
	sender := &fakeSender{}
	p := NewPoller(fake, sender, 0).WithVerifier(&fakeVerifier{found: true, ghost: true})

	p.pollOnce(context.Background())

	if len(fake.acked) != 0 {
		t.Errorf("acked = %v, want none; a ghost row is not a delivery", fake.acked)
	}
	if len(fake.failed) != 1 || fake.failed[0].classification != "ghost" {
		t.Fatalf("failed = %+v, want one ghost classification", fake.failed)
	}
}

// TestPollerClassifiesScriptFailure proves a non-zero osascript exit is treated as
// never-dispatched, which is the only failure a retry can never duplicate.
func TestPollerClassifiesScriptFailure(t *testing.T) {
	fake := &fakeOutboundClient{queue: oneMessage()}
	sender := &fakeSender{err: errors.New("osascript failed: not authorized")}
	p := NewPoller(fake, sender, 0).WithVerifier(&fakeVerifier{found: true})

	p.pollOnce(context.Background())

	if len(fake.acked) != 0 {
		t.Errorf("acked = %v, want none", fake.acked)
	}
	if len(fake.failed) != 1 || fake.failed[0].classification != "not_started" {
		t.Fatalf("failed = %+v, want one not_started classification", fake.failed)
	}
}

// TestPollerTreatsSnapshotFailureAsUnknown proves that when the pre-send watermark
// cannot be read, the poller does not fall back to trusting the sender — an
// unverifiable send must be reported as unknown.
func TestPollerTreatsSnapshotFailureAsUnknown(t *testing.T) {
	fake := &fakeOutboundClient{queue: oneMessage()}
	sender := &fakeSender{}
	v := &fakeVerifier{noteErr: errors.New("database is locked")}
	p := NewPoller(fake, sender, 0).WithVerifier(v)

	p.pollOnce(context.Background())

	if len(fake.acked) != 0 {
		t.Errorf("acked = %v, want none; a send that cannot be verified must not be acked", fake.acked)
	}
	if len(fake.failed) != 1 || fake.failed[0].classification != "unknown" {
		t.Fatalf("failed = %+v, want unknown", fake.failed)
	}
	if v.calls != 0 {
		t.Errorf("verify called %d times, want 0 — the send never happened", v.calls)
	}
}

// TestPollerWithoutVerifierTrustsSender keeps the pre-#210 behaviour available for
// senders that cannot fail silently, which is what the poller's own unit tests
// rely on.
func TestPollerWithoutVerifierTrustsSender(t *testing.T) {
	fake := &fakeOutboundClient{queue: oneMessage()}
	sender := &fakeSender{}
	p := NewPoller(fake, sender, 0)

	p.pollOnce(context.Background())

	if len(fake.acked) != 1 {
		t.Errorf("acked = %v, want [1] with no verifier attached", fake.acked)
	}
}

// TestOutcomeRetrySafety pins the rule that prevents duplicate sends.
func TestOutcomeRetrySafety(t *testing.T) {
	safe := map[deliveryOutcome]bool{
		outcomeNotStarted: true,
		outcomeGhost:      true,
		outcomeDelivered:  false,
		outcomeUnknown:    false,
	}
	for outcome, want := range safe {
		if got := outcome.retrySafe(); got != want {
			t.Errorf("%s.retrySafe() = %v, want %v", outcome, got, want)
		}
	}
}

// TestDescribeNextIsActionable proves each failure logs what actually happens to
// the message, which is the difference between a diagnosable bridge and a silent
// one.
func TestDescribeNextIsActionable(t *testing.T) {
	if got := describeNext(outcomeUnknown, false); !strings.Contains(got, "not retried") {
		t.Errorf("describeNext(unknown) = %q, want it to say the message is not retried", got)
	}
	if got := describeNext(outcomeGhost, true); !strings.Contains(got, "dead-lettered") {
		t.Errorf("describeNext(ghost, exhausted) = %q, want dead-letter mention", got)
	}
	if got := describeNext(outcomeNotStarted, false); !strings.Contains(got, "retrying") {
		t.Errorf("describeNext(not_started) = %q, want retry mention", got)
	}
}

// --- verification against a synthetic chat.db -------------------------------

// addOutgoing inserts an outgoing message row for verification tests.
func addOutgoing(t *testing.T, db *sql.DB, handleID, text string) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO message (guid, text, handle_id, date, is_from_me, is_system_message, associated_message_type, error, service)
		VALUES (?, ?, (SELECT ROWID FROM handle WHERE id = ?), ?, 1, 0, 0, 0, 'iMessage')`,
		"out-"+handleID+"-"+text, text, handleID, epoch.UnixNano())
	if err != nil {
		t.Fatalf("insert outgoing: %v", err)
	}
	_ = res
}

// TestOutgoingSinceFindsVerifiedRow proves a sent message is detected after the
// pre-send watermark.
func TestOutgoingSinceFindsVerifiedRow(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "seed", false, false, nil)
	after, err := maxRowID(db)
	if err != nil {
		t.Fatalf("maxRowID: %v", err)
	}
	addOutgoing(t, db, "+6281267858909", "on my way")

	row, found, err := outgoingSince(db, after, "+6281267858909", "on my way")
	if err != nil {
		t.Fatalf("outgoingSince: %v", err)
	}
	if !found {
		t.Fatal("outgoing row not found; a delivered message would be dead-lettered")
	}
	if row.Text != "on my way" {
		t.Errorf("row text = %q, want the sent text", row.Text)
	}
}

// TestOutgoingSinceDetectsGhostRow pins the macOS 26 signature: a row for the right
// recipient carrying no text means Messages wrote a placeholder, not a message.
func TestOutgoingSinceDetectsGhostRow(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "seed", false, false, nil)
	after, _ := maxRowID(db)
	addOutgoing(t, db, "+6281267858909", "")

	row, found, err := outgoingSince(db, after, "+6281267858909", "hello")
	if err != nil {
		t.Fatalf("outgoingSince: %v", err)
	}
	if !found {
		t.Fatal("ghost row not detected; a failed send would be reported as delivered")
	}
	if row.Text != "" {
		t.Errorf("row text = %q, want empty for a ghost row", row.Text)
	}
}

// TestOutgoingSinceIgnoresOtherRecipients proves a row for a different handle is
// not mistaken for this send's row.
func TestOutgoingSinceIgnoresOtherRecipients(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "seed", false, false, nil)
	after, _ := maxRowID(db)
	addOutgoing(t, db, "+6289999999999", "not for you")

	if _, found, err := outgoingSince(db, after, "+6281267858909", "not for you"); err != nil {
		t.Fatalf("outgoingSince: %v", err)
	} else if found {
		t.Error("matched another recipient's row; verification would ack a failed send")
	}
}

// TestOutgoingSinceIgnoresRowsBelowWatermark proves the pre-send snapshot is what
// scopes the check, so an older message cannot be mistaken for the new send.
func TestOutgoingSinceIgnoresRowsBelowWatermark(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "seed", false, false, nil)
	addOutgoing(t, db, "+6281267858909", "old")
	after, _ := maxRowID(db)

	if _, found, err := outgoingSince(db, after, "+6281267858909", "old"); err != nil {
		t.Fatalf("outgoingSince: %v", err)
	} else if found {
		t.Error("matched a row at or below the watermark; the snapshot is the whole point")
	}
}

// TestNormaliseForCompare proves verification tolerates the punctuation and
// spacing differences Messages introduces, which would otherwise produce false
// negatives and dead-letter a delivered message.
func TestNormaliseForCompare(t *testing.T) {
	cases := [][2]string{
		{"on my way", "On my way!"},
		{"I'll be there", "ill be there"},
		{"see you  at 8", "see you at 8"},
	}
	for _, c := range cases {
		if normaliseForCompare(c[0]) != normaliseForCompare(c[1]) {
			t.Errorf("normalise(%q) != normalise(%q)", c[0], c[1])
		}
	}
	if normaliseForCompare("") != "" {
		t.Error("empty input should normalise to empty")
	}
}

// TestSameRecipientToleratesFormatting proves the + prefix and spacing that the
// bridge, store, and chat.db each format differently do not defeat matching.
func TestSameRecipientToleratesFormatting(t *testing.T) {
	if !sameRecipient("+6281267858909", "6281267858909") {
		t.Error("handle and bare number should match")
	}
	if !sameRecipient("+62 812-6785-8909", "+6281267858909") {
		t.Error("formatted number should match the bare form")
	}
	if sameRecipient("+6281267858909", "+6289999999999") {
		t.Error("different numbers must not match")
	}
}

// TestVerifyWindowExpiry proves an absent row resolves to unknown rather than
// blocking forever. The window is shrunk so the test does not wait eight seconds.
func TestVerifyWindowExpiry(t *testing.T) {
	db := openSynthDB(t)
	addMessage(t, db, "+6281267858909", "seed", false, false, nil)
	after, _ := maxRowID(db)

	w := &Watcher{
		db:        db,
		now:       time.Now,
		vWindow:   40 * time.Millisecond,
		vInterval: 5 * time.Millisecond,
	}
	ghost, found, err := w.verify(after, "+6281267858909", "never sent")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if found {
		t.Error("found = true for a send that produced no row")
	}
	if ghost {
		t.Error("ghost = true for a send that produced no row")
	}
}
