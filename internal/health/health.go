// Package health probes externally-dependent tools (Mac bridge, LLM
// backend, WhatsApp, iMessage bridge polling, web search) and notifies the
// Master on actionable failures. Local-only tools (time, todos, schedules)
// cannot fail independently of the process, so they are out of scope.
//
// Failure severity drives the notify policy: permission loss (FDA, Calendar
// consent) pushes immediately because it is rare and actionable, while plain
// unreachability (a sleeping Mac) stays quiet with a daytime escalation so a
// closed lid does not page the Master every night.
package health

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heyimteee/clark/internal/logging"
)

// ErrUnknown reports "cannot tell yet" (e.g. a transport not wired at boot).
// Unknown results never notify and never disturb existing tracking.
var ErrUnknown = errors.New("health unknown")

// Failure is an unhealthy probe result. Urgent failures (permission loss)
// notify at once; the rest wait out the grace period.
type Failure struct {
	Urgent bool
	Hint   string
	Detail string
}

func (e *Failure) Error() string { return e.Detail }

// UrgentFailure builds an actionable permission-style failure.
func UrgentFailure(hint, detail string) *Failure {
	return &Failure{Urgent: true, Hint: hint, Detail: detail}
}

// TransientFailure builds a reachability-style failure with a hint.
func TransientFailure(hint, detail string) *Failure {
	return &Failure{Hint: hint, Detail: detail}
}

// Checker probes one external dependency. Tools names the LLM tools that stop
// working when it fails. OnlyIfHealthy suppresses the checker (as unknown)
// while another checker is unhealthy, so one root cause yields one alert.
type Checker struct {
	Name          string
	Tools         []string
	OnlyIfHealthy string
	Check         func(ctx context.Context) error
}

// State is the last known checker condition.
type State string

const (
	Healthy   State = "healthy"
	Unhealthy State = "unhealthy"
	Unknown   State = "unknown"
)

// Result is one checker's reported condition.
type Result struct {
	Name      string
	State     State
	Hint      string
	Detail    string
	Since     time.Time
	CheckedAt time.Time
}

// Monitor polls checkers, tracks transitions, and notifies on policy.
type Monitor struct {
	checkers    []Checker
	interval    time.Duration
	grace       time.Duration
	remindEvery time.Duration
	dayStart    int
	dayEnd      int
	probeTime   time.Duration

	now    func() time.Time
	notify func(ctx context.Context, text string)

	mu         sync.Mutex
	prev       map[string]State
	downSince  map[string]time.Time
	lastNotify map[string]time.Time
	lastHint   map[string]string
	lastDetail map[string]string
}

// New builds a Monitor. notify fires push alerts (multi-channel fan-out);
// now is injectable for tests (nil means time.Now).
func New(checkers []Checker, notify func(ctx context.Context, text string)) *Monitor {
	return &Monitor{
		checkers:    checkers,
		interval:    5 * time.Minute,
		grace:       30 * time.Minute,
		remindEvery: 6 * time.Hour,
		dayStart:    7,
		dayEnd:      22,
		probeTime:   10 * time.Second,
		notify:      notify,
		prev:        make(map[string]State),
		downSince:   make(map[string]time.Time),
		lastNotify:  make(map[string]time.Time),
		lastHint:    make(map[string]string),
		lastDetail:  make(map[string]string),
	}
}

// SetClock overrides time for tests.
func (m *Monitor) SetClock(now func() time.Time) { m.now = now }

// SetTimings overrides interval/grace/reminder/daytime window (tests).
func (m *Monitor) SetTimings(interval, grace, remindEvery time.Duration, dayStart, dayEnd int) {
	m.interval, m.grace, m.remindEvery = interval, grace, remindEvery
	m.dayStart, m.dayEnd = dayStart, dayEnd
}

func (m *Monitor) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Run polls immediately, then on interval until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	m.Poll(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Poll(ctx)
		}
	}
}

// Poll runs every checker once and notifies on policy.
func (m *Monitor) Poll(ctx context.Context) {
	results := m.checkAll(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	for _, c := range m.checkers {
		r, ok := results[c.Name]
		if !ok || r.State == Unknown {
			continue
		}
		if r.State == Healthy {
			if m.prev[c.Name] == Unhealthy {
				m.push(ctx, now, c.Name, fmt.Sprintf("✅ %s is healthy again.", c.Name))
			}
			delete(m.downSince, c.Name)
			m.prev[c.Name] = Healthy
			continue
		}
		var f *Failure
		urgent, hint := false, ""
		if errors.As(r.err, &f) {
			urgent, hint = f.Urgent, f.Hint
		}
		m.lastHint[c.Name], m.lastDetail[c.Name] = hint, r.Detail
		if _, seen := m.downSince[c.Name]; !seen {
			m.downSince[c.Name] = now
		}
		downFor := now.Sub(m.downSince[c.Name])
		last, reminded := m.lastNotify[c.Name]
		due := false
		switch {
		case urgent:
			due = !reminded || now.Sub(last) >= m.remindEvery
		default:
			daytime := now.Hour() >= m.dayStart && now.Hour() < m.dayEnd
			due = downFor >= m.grace && (daytime || downFor >= m.remindEvery) &&
				(!reminded || now.Sub(last) >= m.remindEvery)
		}
		if due {
			tools := strings.Join(c.Tools, ", ")
			text := fmt.Sprintf("⚠️ %s is down", c.Name)
			if hint != "" {
				text += ": " + hint
			}
			if tools != "" {
				text += fmt.Sprintf(" (affects: %s)", tools)
			}
			text += "."
			m.push(ctx, now, c.Name, text)
		}
		m.prev[c.Name] = Unhealthy
	}
}

// rawResult pairs a checker outcome with its error for policy routing.
type rawResult struct {
	Result
	err error
}

func (m *Monitor) checkAll(ctx context.Context) map[string]rawResult {
	out := make(map[string]rawResult, len(m.checkers))
	now := m.clock()
	healthy := func(name string) bool {
		r, ok := out[name]
		return ok && r.State == Healthy
	}
	for _, c := range m.checkers {
		if c.OnlyIfHealthy != "" && !healthy(c.OnlyIfHealthy) {
			out[c.Name] = rawResult{Result: Result{Name: c.Name, State: Unknown, CheckedAt: now}}
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, m.probeTime)
		err := c.Check(pctx)
		cancel()
		r := rawResult{Result: Result{Name: c.Name, CheckedAt: now}, err: err}
		switch {
		case err == nil:
			r.State = Healthy
		case errors.Is(err, ErrUnknown):
			r.State = Unknown
		default:
			r.State = Unhealthy
			r.Detail = err.Error()
			var f *Failure
			if errors.As(err, &f) {
				r.Hint = f.Hint
			}
			if since, ok := m.downSince[c.Name]; ok {
				r.Since = since
			} else {
				r.Since = now
			}
		}
		out[c.Name] = r
	}
	return out
}

func (m *Monitor) push(ctx context.Context, now time.Time, name, text string) {
	m.lastNotify[name] = now
	logging.Log("HEALTH", logging.SevWarn, "NOTIFY", "Tool health transition", "checker", name, "text", text)
	if m.notify != nil {
		m.notify(ctx, text)
	}
}

// Snapshot renders the current conditions as human lines for get_state,
// tool_health, and logs. Unknown checkers are omitted until they report.
func (m *Monitor) Snapshot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.checkers))
	for _, c := range m.checkers {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	var lines []string
	for _, n := range names {
		switch m.prev[n] {
		case Healthy:
			lines = append(lines, n+": healthy")
		case Unhealthy:
			line := n + ": DOWN"
			if h := m.lastHint[n]; h != "" {
				line += " — " + h
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "no tool health data yet"
	}
	return strings.Join(lines, "\n")
}

// Results returns the last known conditions for structured consumers (the
// web console tile).
func (m *Monitor) Results() []Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Result
	for _, c := range m.checkers {
		st, ok := m.prev[c.Name]
		if !ok {
			continue
		}
		r := Result{Name: c.Name, State: st, CheckedAt: m.clock()}
		if st == Unhealthy {
			r.Since = m.downSince[c.Name]
			r.Hint = m.lastHint[c.Name]
			r.Detail = m.lastDetail[c.Name]
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
