package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/heyimteee/clark/internal/logging"
)

const (
	// loginMaxFails is how many failed logins from one source are tolerated
	// before the source is locked out of further attempts.
	loginMaxFails = 6
	// loginLockoutWindow is how long the lockout (and the failure memory)
	// lasts. A fixed window keeps the accounting simple and predictable.
	loginLockoutWindow = 15 * time.Minute
)

// failWindow records consecutive login failures for one source address.
type failWindow struct {
	count       int
	windowStart time.Time
}

// loginThrottle rate-limits /web/api/login per source address. The console
// sits behind public ingress, so unlimited guessing is not acceptable even
// against a high-entropy key: this bounds an attacker to loginMaxFails tries
// per window while leaving legitimate users unaffected.
type loginThrottle struct {
	mu    sync.Mutex
	fails map[string]*failWindow
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{fails: make(map[string]*failWindow)}
}

// loginPruneThreshold is the map size at which expired windows are swept
// eagerly: sources that failed once and never returned would otherwise
// accumulate for the life of the process.
const loginPruneThreshold = 1024

// allow reports whether src may attempt a login right now.
func (t *loginThrottle) allow(src string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.fails) > loginPruneThreshold {
		for k, w := range t.fails {
			if time.Since(w.windowStart) >= loginLockoutWindow {
				delete(t.fails, k)
			}
		}
	}
	w, ok := t.fails[src]
	if !ok {
		return true
	}
	if time.Since(w.windowStart) >= loginLockoutWindow {
		delete(t.fails, src)
		return true
	}
	return w.count < loginMaxFails
}

// fail records a failed attempt for src, starting a fresh window if needed.
func (t *loginThrottle) fail(src string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.fails) > loginPruneThreshold {
		for k, w2 := range t.fails {
			if time.Since(w2.windowStart) >= loginLockoutWindow {
				delete(t.fails, k)
			}
		}
	}
	w, ok := t.fails[src]
	if !ok || time.Since(w.windowStart) >= loginLockoutWindow {
		t.fails[src] = &failWindow{count: 1, windowStart: time.Now()}
		return
	}
	w.count++
}

// reset clears src's failure memory after a successful login.
func (t *loginThrottle) reset(src string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, src)
}

// clientIP resolves the best-effort source address for logging and
// throttling: the first X-Forwarded-For entry when present (the console runs
// behind a trusted reverse proxy), otherwise the RemoteAddr host.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0]); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// logAuthFailure emits a WARN line with the source so repeated guessing is
// visible in the log stream and on disk.
func logAuthFailure(src, detail string) {
	logging.Log("WEB", logging.SevWarn, "LOGIN", "Failed web login attempt", "source", src, "reason", detail)
}

// tokenBucket is a minimal rate limiter: burst tokens refilling at rate
// tokens per minute. Used for webhook endpoints where a flood would cascade
// into message bombing of the Master (#60).
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64 // tokens per second
	burst  float64
	now    func() time.Time
}

func newTokenBucket(perMinute, burst int) *tokenBucket {
	return &tokenBucket{
		tokens: float64(burst),
		last:   time.Now(),
		rate:   float64(perMinute) / 60,
		burst:  float64(burst),
		now:    time.Now,
	}
}

// allow consumes one token if available, refilling for elapsed time first.
func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = min64(b.burst, b.tokens+elapsed*b.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
