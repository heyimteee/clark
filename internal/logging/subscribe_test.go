package logging

import (
	"strings"
	"testing"
	"time"
)

// drain drains ch for up to the timeout, returning whatever arrived.
func drain(ch <-chan string, timeout time.Duration) []string {
	var out []string
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, line)
		case <-deadline:
			return out
		}
	}
}

// TestSubscribeReceivesPlainLines verifies the sink gets the uncolored line.
func TestSubscribeReceivesPlainLines(t *testing.T) {
	ch, unsub := Subscribe()
	t.Cleanup(unsub)

	Log("CLARK", SevInfo, "TEST", "hello sink", "key", "value")

	lines := drain(ch, time.Second)
	if len(lines) == 0 {
		t.Fatal("sink received no lines after Log")
	}
	line := lines[0]
	if strings.Contains(line, "\033[") {
		t.Errorf("sink line contains ANSI codes: %q", line)
	}
	if !strings.Contains(line, "TEST") || !strings.Contains(line, "hello sink") || !strings.Contains(line, "key=") {
		t.Errorf("sink line missing expected parts: %q", line)
	}
}

// TestSubscribeUnsub verifies unsubscribing stops delivery.
func TestSubscribeUnsub(t *testing.T) {
	ch, unsub := Subscribe()
	unsub()

	Log("CLARK", SevInfo, "TEST", "after unsub")

	if lines := drain(ch, 150*time.Millisecond); len(lines) != 0 {
		t.Errorf("received %d lines after unsub: %v", len(lines), lines)
	}
}

// TestLoggerNeverBlocksOnFullSink verifies a full subscriber channel never
// blocks the logger (drop-oldest behaviour).
func TestLoggerNeverBlocksOnFullSink(t *testing.T) {
	ch, unsub := Subscribe()
	t.Cleanup(func() {
		unsub()
	})

	// Saturate the buffer by logging more lines than it can hold without
	// draining. If the logger blocked on the full sink this would time out.
	done := make(chan struct{})
	go func() {
		for i := 0; i < sinkBuf*4; i++ {
			Log("CLARK", SevInfo, "TEST", "burst")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("logger blocked on a full sink; drop-oldest is broken")
	}

	// The buffer must still be usable for the next line. Drain it (FIFO) and
	// expect the newest line, "after burst", at the tail.
	Log("CLARK", SevInfo, "TEST", "after burst")
	lines := drain(ch, time.Second)
	if len(lines) == 0 {
		t.Fatal("sink stopped delivering after overflow")
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "after burst") {
		t.Errorf("last buffered line = %q, want the newest 'after burst'", last)
	}
}

// TestSubscribeManyFansOut verifies multiple subscribers each get lines.
func TestSubscribeManyFansOut(t *testing.T) {
	chA, unsubA := Subscribe()
	defer unsubA()
	chB, unsubB := Subscribe()
	defer unsubB()

	Log("CLARK", SevInfo, "TEST", "fan out")

	if got := drain(chA, time.Second); len(got) != 1 {
		t.Errorf("subscriber A got %d lines, want 1", len(got))
	}
	if got := drain(chB, time.Second); len(got) != 1 {
		t.Errorf("subscriber B got %d lines, want 1", len(got))
	}
}

// TestRecentReplaysLastLines verifies Recent returns the most recent N plain
// lines in order, capped by the ring size.
func TestRecentReplaysLastLines(t *testing.T) {
	// Recent should also see the lines emitted by earlier tests in this
	// package; instead of assuming a clean ring, log a fresh, tagged burst and
	// verify Recent surfaces the newest ones first-to-last.
	for i := 0; i < 5; i++ {
		Log("CLARK", SevInfo, "RECENT", "recent line", "i", i)
	}

	got := Recent(3)
	if len(got) != 3 {
		t.Fatalf("Recent(3) = %d lines, want 3", len(got))
	}
	if !strings.Contains(got[len(got)-1], "i=4") {
		t.Errorf("last recent line = %q, want the newest (i=4)", got[len(got)-1])
	}
	if !strings.Contains(got[0], "i=2") {
		t.Errorf("first recent line = %q, want i=2", got[0])
	}

	all := Recent(0)
	if len(all) != ringCap {
		t.Errorf("Recent(0) = %d lines, want the full ring (%d)", len(all), ringCap)
	}
	if !strings.Contains(all[len(all)-1], "i=4") {
		t.Errorf("ring tail = %q, want the newest (i=4)", all[len(all)-1])
	}
}
