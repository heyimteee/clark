package web

import (
	"testing"

	"github.com/heyimteee/clark/internal/logging"
)

func TestLogsReplayAndLive(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	c := wsDial(t, ts, "/web/api/logs")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})

	frame := wsReadJSON(t, c)
	if frame["type"] != "auth" || frame["ok"] != true {
		t.Fatalf("auth frame = %v, want auth ok", frame)
	}

	// Replay frame arrives after auth with any buffered lines.
	replay := wsReadJSON(t, c)
	if replay["type"] != "replay" {
		t.Fatalf("replay frame = %v, want replay", replay)
	}

	logging.Log("web", logging.SevInfo, "TEST", "live line emitted for ws test", "k", "v")

	live := wsReadJSON(t, c)
	if live["type"] != "log" {
		t.Fatalf("live frame = %v, want log", live)
	}
	line, _ := live["line"].(string)
	if line == "" {
		t.Fatal("log frame has empty line")
	}
	if len(line) > 0 && line[0] == '\033' {
		t.Errorf("log line is ANSI-colored: %q", line)
	}
}

func TestLogsReplayContainsRecentLines(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	logging.Log("web", logging.SevInfo, "TEST", "pre-connect line")

	c := wsDial(t, ts, "/web/api/logs")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": tok})
	wsReadJSON(t, c) // auth ok

	replay := wsReadJSON(t, c)
	lines, _ := replay["lines"].([]any)
	found := false
	for _, l := range lines {
		if containsStr(l.(string), "pre-connect line") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("replay missing pre-connect line (got %d lines)", len(lines))
	}
}

func TestLogsRejectsBadAuth(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	c := wsDial(t, ts, "/web/api/logs")
	wsWriteJSON(t, c, map[string]any{"type": "auth", "token": "nope"})

	frame := wsReadJSON(t, c)
	if frame["type"] != "auth" || frame["ok"] != false {
		t.Fatalf("auth frame = %v, want auth ok=false", frame)
	}
}

func containsStr(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
