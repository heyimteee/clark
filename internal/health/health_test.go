package health

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testMonitor(ck []Checker, clk *clock) (*Monitor, *[]string) {
	var got []string
	m := New(ck, func(_ context.Context, text string) { got = append(got, text) })
	m.SetClock(clk.now)
	m.SetTimings(time.Minute, 30*time.Minute, 6*time.Hour, 7, 22)
	return m, &got
}

func okChecker(name string) Checker {
	return Checker{Name: name, Check: func(context.Context) error { return nil }}
}

func TestUrgentNotifiesAtOnce(t *testing.T) {
	clk := &clock{t: time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC)} // 03:00: night
	m, got := testMonitor([]Checker{{
		Name:  "mac_bridge",
		Tools: []string{"list_calendar_events"},
		Check: func(context.Context) error { return UrgentFailure("grant FDA", "denied") },
	}}, clk)
	m.Poll(context.Background())
	if len(*got) != 1 || !strings.Contains((*got)[0], "grant FDA") {
		t.Fatalf("got %v, want immediate urgent push", *got)
	}
	// Second poll inside the reminder window stays quiet.
	clk.t = clk.t.Add(time.Hour)
	m.Poll(context.Background())
	if len(*got) != 1 {
		t.Fatalf("got %v, want no repeat inside 6h", *got)
	}
	// Past the reminder window it re-notifies.
	clk.t = clk.t.Add(6 * time.Hour)
	m.Poll(context.Background())
	if len(*got) != 2 {
		t.Fatalf("got %v, want 6h reminder", *got)
	}
}

func TestTransientStaysQuietThenEscalatesDaytime(t *testing.T) {
	clk := &clock{t: time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)} // night
	fail := func(context.Context) error { return TransientFailure("mac asleep", "timeout") }
	m, got := testMonitor([]Checker{{Name: "mac_bridge", Check: fail}}, clk)
	m.Poll(context.Background())
	m.Poll(context.Background())
	if len(*got) != 0 {
		t.Fatalf("got %v, want silence before grace", *got)
	}
	// Past grace but still night: quiet.
	clk.t = clk.t.Add(31 * time.Minute)
	m.Poll(context.Background())
	if len(*got) != 0 {
		t.Fatalf("got %v, want night silence", *got)
	}
	// Daytime past grace: escalate once.
	clk.t = time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC)
	m.Poll(context.Background())
	if len(*got) != 1 || !strings.Contains((*got)[0], "mac asleep") {
		t.Fatalf("got %v, want daytime escalation", *got)
	}
}

func TestRecoveryNotifiesOnce(t *testing.T) {
	clk := &clock{t: time.Now()}
	down := true
	m, got := testMonitor([]Checker{{
		Name: "x",
		Check: func(context.Context) error {
			if down {
				return UrgentFailure("fix it", "bad")
			}
			return nil
		},
	}}, clk)
	m.Poll(context.Background())
	down = false
	m.Poll(context.Background())
	m.Poll(context.Background())
	want := 2 // down push + recovery
	if len(*got) != want {
		t.Fatalf("got %v, want down+recovery only", *got)
	}
	if !strings.Contains((*got)[1], "healthy again") {
		t.Fatalf("recovery text = %q", (*got)[1])
	}
}

func TestUnknownNeverNotifies(t *testing.T) {
	clk := &clock{t: time.Now()}
	m, got := testMonitor([]Checker{{
		Name:  "x",
		Check: func(context.Context) error { return ErrUnknown },
	}}, clk)
	m.Poll(context.Background())
	if len(*got) != 0 {
		t.Fatalf("got %v, want silence", *got)
	}
	if s := m.Snapshot(); s != "no tool health data yet" {
		t.Fatalf("snapshot = %q", s)
	}
}

func TestOnlyIfHealthySuppresses(t *testing.T) {
	clk := &clock{t: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	imessagePolled := false
	m, got := testMonitor([]Checker{
		{
			Name:  "mac_bridge",
			Check: func(context.Context) error { return TransientFailure("asleep", "timeout") },
		},
		{
			Name:          "imessage_bridge",
			OnlyIfHealthy: "mac_bridge",
			Check: func(context.Context) error {
				imessagePolled = true
				return TransientFailure("stale", "stale")
			},
		},
	}, clk)
	// Pre-age the mac_bridge outage past grace so it would notify.
	m.downSince["mac_bridge"] = clk.t.Add(-time.Hour)
	m.prev["mac_bridge"] = Unhealthy
	m.Poll(context.Background())
	if imessagePolled {
		t.Fatal("imessage checker ran while mac_bridge unhealthy")
	}
	for _, n := range *got {
		if strings.Contains(n, "imessage_bridge") {
			t.Fatalf("imessage notified despite suppression: %q", n)
		}
	}
}

func TestFailureErrorUnwrap(t *testing.T) {
	f := UrgentFailure("h", "d")
	var out *Failure
	if !errors.As(f, &out) || !out.Urgent {
		t.Fatal("Failure must survive errors.As")
	}
}
