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

func TestIMessageQueueStaleWindow(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Enqueue and claim so the row is picked now; it must not be stale yet.
	id, err := st.EnqueueIMessage("+6281111111111", "hello")
	if err != nil {
		t.Fatalf("EnqueueIMessage: %v", err)
	}
	if _, ok, err := st.NextIMessageOutbound(); err != nil || !ok {
		t.Fatalf("NextIMessageOutbound: ok=%v err=%v", ok, err)
	}

	stale, err := st.StaleIMessageOutboundIDs(time.Hour)
	if err != nil {
		t.Fatalf("StaleIMessageOutboundIDs: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("freshly picked message reported stale: %v", stale)
	}

	// A picked message older than the window is reported.
	if _, err := st.db.Exec(`UPDATE imessage_outbound SET picked_at = datetime('now', '-2 hours') WHERE id = ?`, id); err != nil {
		t.Fatalf("backdating picked_at: %v", err)
	}
	stale, err = st.StaleIMessageOutboundIDs(time.Hour)
	if err != nil {
		t.Fatalf("StaleIMessageOutboundIDs: %v", err)
	}
	if len(stale) != 1 || stale[0] != id {
		t.Fatalf("stale = %v, want [%d]", stale, id)
	}
}
