package store

import (
	"testing"
	"time"
)

func TestIMessageQueueLifecycle(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	id1, err := st.EnqueueIMessage("+6281111111111", "hello one")
	if err != nil {
		t.Fatalf("EnqueueIMessage: %v", err)
	}
	id2, err := st.EnqueueIMessage("+6282222222222", "hello two")
	if err != nil {
		t.Fatalf("EnqueueIMessage: %v", err)
	}
	if id1 >= id2 {
		t.Fatalf("ids not monotonic: %d then %d", id1, id2)
	}

	// Claims arrive oldest-first and are marked picked (not deleted).
	first, ok, err := st.NextIMessageOutbound()
	if err != nil || !ok {
		t.Fatalf("NextIMessageOutbound = %v/%v/%v, want first message", first, ok, err)
	}
	if first.ID != id1 || first.Recipient != "+6281111111111" || first.Text != "hello one" {
		t.Fatalf("first = %+v, want id %d", first, id1)
	}

	// Claiming again must not re-serve a picked message.
	second, ok, err := st.NextIMessageOutbound()
	if err != nil || !ok {
		t.Fatalf("NextIMessageOutbound = %v/%v/%v, want second message", second, ok, err)
	}
	if second.ID != id2 {
		t.Fatalf("second = %+v, want id %d", second, id2)
	}

	if _, ok, err := st.NextIMessageOutbound(); err != nil || ok {
		t.Fatalf("NextIMessageOutbound after both claimed = %v/%v, want empty", ok, err)
	}

	// Ack removes exactly the delivered message; the other stays picked.
	if err := st.AckIMessage(id1); err != nil {
		t.Fatalf("AckIMessage: %v", err)
	}
	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM imessage_outbound WHERE id = ?`, id2).Scan(&count); err != nil {
		t.Fatalf("counting id %d: %v", id2, err)
	}
	if count != 1 {
		t.Fatalf("acking %d removed %d, want it to stay picked", id1, id2)
	}
}

func TestIMessageQueueEmpty(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if _, ok, err := st.NextIMessageOutbound(); err != nil || ok {
		t.Fatalf("NextIMessageOutbound on empty queue = %v/%v, want empty", ok, err)
	}
}

// TestIMessageQueueLeaseReclaim replaces the old stale-window diagnostic. A row
// stranded in `picked` by a bridge that died mid-send used to be unrecoverable
// and never re-served; the lease now makes it claimable again (#210).
func TestIMessageQueueLeaseReclaim(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	id, err := st.EnqueueIMessage("+6281111111111", "hello")
	if err != nil {
		t.Fatalf("EnqueueIMessage: %v", err)
	}
	if _, ok, err := st.NextIMessageOutbound(); err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}

	// A live lease is respected: no second claim while the bridge still holds it.
	if _, ok, err := st.NextIMessageOutbound(); err != nil || ok {
		t.Fatalf("re-claim under a live lease: ok=%v err=%v, want not claimable", ok, err)
	}

	// Simulate a bridge that died mid-send by backdating the lease.
	if _, err := st.db.Exec(`UPDATE imessage_outbound SET picked_at = datetime('now', '-1 hour') WHERE id = ?`, id); err != nil {
		t.Fatalf("backdate picked_at: %v", err)
	}
	msg, ok, err := st.NextIMessageOutbound()
	if err != nil || !ok {
		t.Fatalf("re-claim after lease expiry: ok=%v err=%v, want the message back", ok, err)
	}
	if msg.ID != id {
		t.Errorf("re-claimed id = %d, want %d", msg.ID, id)
	}
	if msg.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 (the stranded attempt counts)", msg.Attempts)
	}
}

// TestIMessageQueueRetryBackoff proves a retryable failure returns to pending with
// a future next_attempt_at, so the queue does not spin on it.
func TestIMessageQueueRetryBackoff(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	id, err := st.EnqueueIMessage("+6281111111111", "hello")
	if err != nil {
		t.Fatalf("EnqueueIMessage: %v", err)
	}
	if _, ok, _ := st.NextIMessageOutbound(); !ok {
		t.Fatal("first claim failed")
	}

	if err := st.FailIMessage(id, "osascript refused", FailureNotStarted, time.Now().Add(time.Minute), false); err != nil {
		t.Fatalf("FailIMessage: %v", err)
	}
	if _, ok, err := st.NextIMessageOutbound(); err != nil || ok {
		t.Fatalf("claim during backoff: ok=%v err=%v, want not claimable", ok, err)
	}
}

// TestIMessageQueueUnknownIsNeverRetried is the safety-critical one: an unknown
// outcome may already be on the recipient's device, so it must be dead-lettered
// rather than re-sent.
func TestIMessageQueueUnknownIsNeverRetried(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	id, err := st.EnqueueIMessage("+6281111111111", "hello")
	if err != nil {
		t.Fatalf("EnqueueIMessage: %v", err)
	}
	if _, ok, _ := st.NextIMessageOutbound(); !ok {
		t.Fatal("first claim failed")
	}

	if err := st.FailIMessage(id, "no row appeared", FailureUnknown, time.Now(), false); err != nil {
		t.Fatalf("FailIMessage: %v", err)
	}
	if _, ok, err := st.NextIMessageOutbound(); err != nil || ok {
		t.Fatalf("unknown outcome was re-offered: ok=%v err=%v; this can duplicate a delivered message", ok, err)
	}

	dead, err := st.DeadIMessages(10)
	if err != nil {
		t.Fatalf("DeadIMessages: %v", err)
	}
	if len(dead) != 1 || dead[0].ID != id {
		t.Fatalf("dead = %+v, want the message dead-lettered so the failure is visible", dead)
	}
	if dead[0].LastError == "" {
		t.Error("dead-lettered message has no recorded reason")
	}
}

// TestIMessageQueueExhaustionDeadLetters proves a persistently failing message
// stops consuming attempts and becomes visible.
func TestIMessageQueueExhaustionDeadLetters(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	id, err := st.EnqueueIMessage("+6281111111111", "hello")
	if err != nil {
		t.Fatalf("EnqueueIMessage: %v", err)
	}
	if _, ok, _ := st.NextIMessageOutbound(); !ok {
		t.Fatal("first claim failed")
	}
	if err := st.FailIMessage(id, "gave up", FailureNotStarted, time.Now(), true); err != nil {
		t.Fatalf("FailIMessage: %v", err)
	}

	if _, ok, err := st.NextIMessageOutbound(); err != nil || ok {
		t.Fatalf("dead-lettered message was re-offered: ok=%v err=%v", ok, err)
	}
	dead, err := st.DeadIMessages(10)
	if err != nil {
		t.Fatalf("DeadIMessages: %v", err)
	}
	if len(dead) != 1 {
		t.Fatalf("dead = %+v, want one dead-lettered message", dead)
	}
}

// TestIMessageQueueCounts proves /status can report queue composition.
func TestIMessageQueueCounts(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	for i := 0; i < 3; i++ {
		if _, err := st.EnqueueIMessage("+6281111111111", "m"); err != nil {
			t.Fatalf("EnqueueIMessage: %v", err)
		}
	}
	if _, ok, _ := st.NextIMessageOutbound(); !ok {
		t.Fatal("claim failed")
	}
	deadID, _ := st.EnqueueIMessage("+6281111111111", "doomed")
	if err := st.FailIMessage(deadID, "nope", FailureNotStarted, time.Now(), true); err != nil {
		t.Fatalf("FailIMessage: %v", err)
	}

	c, err := st.OutboundQueueCounts()
	if err != nil {
		t.Fatalf("OutboundQueueCounts: %v", err)
	}
	if c.Picked != 1 || c.Dead != 1 || c.Pending != 2 {
		t.Errorf("counts = %+v, want picked=1 dead=1 pending=2", c)
	}
}

// TestRetryDelayGrowsAndCaps proves the backoff backs off without becoming so
// long that a transient failure is never retried.
func TestRetryDelayGrowsAndCaps(t *testing.T) {
	if got := RetryDelay(1); got != 30*time.Second {
		t.Errorf("RetryDelay(1) = %v, want 30s", got)
	}
	if got := RetryDelay(2); got != time.Minute {
		t.Errorf("RetryDelay(2) = %v, want 1m", got)
	}
	if got := RetryDelay(20); got != 30*time.Minute {
		t.Errorf("RetryDelay(20) = %v, want the 30m cap", got)
	}
}
