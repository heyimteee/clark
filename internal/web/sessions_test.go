package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/heyimteee/clark/internal/scheduler"
	"github.com/heyimteee/clark/internal/voice"
)

func newSessionTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	st := testStore(t)
	sched := scheduler.New(st, func(_ context.Context, _, _ string) {})
	srv := New(Options{
		ListenAddr: ":0",
		WebToken:   testWebToken,
		Store:      st,
		STTModel:   "whisper-turbo",
		TTSEngine:  "kokoro-remote",
		Voice:      &voice.Engine{},
		Scheduler:  sched,
	})
	ts := newServerFor(t, srv)
	return ts, login(t, ts)
}

func sessionRequest(t *testing.T, ts *httptest.Server, tok, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = bytes.NewReader(raw)
	} else {
		buf = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req = bearer(req, tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestChatSessionsCRUD(t *testing.T) {
	ts, tok := newSessionTestServer(t)

	// First list creates the default session.
	code, out := getJSON(t, ts, "/web/api/chat/sessions", tok)
	if code != 200 {
		t.Fatalf("list = %d (%v), want 200", code, out)
	}
	rows, _ := out["sessions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(rows))
	}

	// Create a second session.
	code, out = postJSON(t, ts, "/web/api/chat/sessions", tok, map[string]any{"title": "Research"})
	if code != 201 {
		t.Fatalf("create = %d (%v), want 201", code, out)
	}
	created := out["session"].(map[string]any)
	id := fmt.Sprintf("%v", created["id"])

	// List now has two, newest first.
	code, out = getJSON(t, ts, "/web/api/chat/sessions", tok)
	rows, _ = out["sessions"].([]any)
	if code != 200 || len(rows) != 2 {
		t.Fatalf("list = %d len=%d (%v), want 200/2", code, len(rows), out)
	}

	// Messages start empty.
	code, out = sessionRequest(t, ts, tok, http.MethodGet, "/web/api/chat/sessions/"+id+"/messages", nil)
	if code != 200 {
		t.Fatalf("messages = %d (%v), want 200", code, out)
	}

	// Unknown session is 404.
	if code, _ := sessionRequest(t, ts, tok, http.MethodGet, "/web/api/chat/sessions/99999/messages", nil); code != 404 {
		t.Fatalf("unknown messages = %d, want 404", code)
	}

	// Rename requires a title.
	if code, _ := sessionRequest(t, ts, tok, http.MethodPut, "/web/api/chat/sessions/"+id, map[string]any{"title": ""}); code != 400 {
		t.Fatalf("empty rename = %d, want 400", code)
	}
	code, out = sessionRequest(t, ts, tok, http.MethodPut, "/web/api/chat/sessions/"+id, map[string]any{"title": "Deep dive"})
	if code != 200 {
		t.Fatalf("rename = %d (%v), want 200", code, out)
	}
	if out["session"].(map[string]any)["title"] != "Deep dive" {
		t.Fatalf("renamed wrong: %v", out)
	}

	// Delete returns a session to switch to.
	code, out = sessionRequest(t, ts, tok, http.MethodDelete, "/web/api/chat/sessions/"+id, nil)
	if code != 200 {
		t.Fatalf("delete = %d (%v), want 200", code, out)
	}
	if out["next"] == nil {
		t.Fatalf("delete should return next session: %v", out)
	}
}
