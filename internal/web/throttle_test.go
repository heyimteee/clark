package web

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginThrottleAllowsWithinBudget(t *testing.T) {
	th := newLoginThrottle()
	for i := 0; i < loginMaxFails; i++ {
		if !th.allow("1.2.3.4") {
			t.Fatalf("attempt %d blocked, want allowed", i+1)
		}
		th.fail("1.2.3.4")
	}
}

func TestLoginThrottleBlocksAfterMaxFails(t *testing.T) {
	th := newLoginThrottle()
	for i := 0; i < loginMaxFails; i++ {
		th.fail("1.2.3.4")
	}
	if th.allow("1.2.3.4") {
		t.Fatal("allow after max failures, want locked")
	}
	// A different source is unaffected.
	if !th.allow("5.6.7.8") {
		t.Fatal("other source blocked, want independent buckets")
	}
}

func TestLoginThrottleSuccessResets(t *testing.T) {
	th := newLoginThrottle()
	for i := 0; i < loginMaxFails-1; i++ {
		th.fail("1.2.3.4")
	}
	th.reset("1.2.3.4")
	for i := 0; i < loginMaxFails; i++ {
		if !th.allow("1.2.3.4") {
			t.Fatalf("attempt %d blocked after reset, want fresh budget", i+1)
		}
		th.fail("1.2.3.4")
	}
}

func TestLoginThrottleUnlocksAfterWindow(t *testing.T) {
	th := newLoginThrottle()
	for i := 0; i < loginMaxFails; i++ {
		th.fail("1.2.3.4")
	}
	// Simulate the window elapsing by rewinding the recorded window.
	th.mu.Lock()
	th.fails["1.2.3.4"].windowStart = time.Now().Add(-loginLockoutWindow - time.Second)
	th.mu.Unlock()
	if !th.allow("1.2.3.4") {
		t.Fatal("still locked after window elapsed, want unlocked")
	}
}

func TestClientIPPrefersForwardedFor(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.2")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("clientIP = %q, want first XFF entry", got)
	}
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "198.51.100.7:5555"
	if got := clientIP(r2); got != "198.51.100.7" {
		t.Errorf("clientIP fallback = %q, want RemoteAddr host", got)
	}
}
