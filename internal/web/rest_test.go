package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/scheduler"
	"github.com/heyimteee/clark/internal/voice"
)

func TestStateSnapshot(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := getJSON(t, ts, "/web/api/state", tok)
	if code != 200 {
		t.Fatalf("state = %d, want 200", code)
	}
	st, _ := out["state"].(map[string]any)
	if st == nil {
		t.Fatalf("state missing from %v", out)
	}
	for _, k := range []string{"name", "model", "enabled", "thinking", "historyLimit", "context", "sttModel", "ttsEngine", "ttsVoice", "vips", "tools"} {
		if _, ok := st[k]; !ok {
			t.Errorf("state missing key %q", k)
		}
	}
	if _, ok := st["tools"].([]any); !ok {
		t.Errorf("tools = %T, want array", st["tools"])
	}
	if _, ok := st["vips"].([]any); !ok {
		t.Errorf("vips = %T, want array", st["vips"])
	}
}

func TestHistoryWebScope(t *testing.T) {
	ts, _, _, st := newTestServer(t)
	tok := login(t, ts)

	if err := st.SaveMessage("web", "user", "hi"); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	code, out := getJSON(t, ts, "/web/api/history?scope=web", tok)
	if code != 200 {
		t.Fatalf("history = %d, want 200", code)
	}
	entries, _ := out["entries"].([]any)
	if len(entries) == 0 {
		t.Fatal("history returned no entries")
	}
	// The API contract (§6.4) is lowercase role/content, which the SPA reads.
	first, _ := entries[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("entry role = %v, want user", first["role"])
	}
	if first["content"] != "hi" {
		t.Errorf("entry content = %v, want hi", first["content"])
	}
}

func TestHistoryRejectsUnknownScope(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := getJSON(t, ts, "/web/api/history?scope=weird", tok)
	if code != 400 {
		t.Fatalf("history bad scope = %d, want 400", code)
	}
	if out["error"] == "" {
		t.Error("bad scope returned no error")
	}
}

func TestMutationsReturnSnapshot(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := postJSON(t, ts, "/web/api/status", tok, map[string]any{"enabled": false})
	if code != 200 {
		t.Fatalf("status = %d, want 200: %v", code, out)
	}
	if st, _ := out["state"].(map[string]any); st == nil {
		t.Fatalf("status mutation did not return fresh state: %v", out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d, want 200", code)
	} else if st, _ := out["state"].(map[string]any); st["enabled"] != false {
		t.Errorf("enabled = %v, want false", st["enabled"])
	}
}

func TestContextMutation(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := postJSON(t, ts, "/web/api/context", tok, map[string]any{"context": "new context"})
	if code != 200 {
		t.Fatalf("context = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else if st, _ := out["state"].(map[string]any); st["context"] != "new context" {
		t.Errorf("context = %v, want 'new context'", st["context"])
	}
}

func TestThinkingMutation(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	if code, out := postJSON(t, ts, "/web/api/thinking", tok, map[string]any{"enabled": true}); code != 200 {
		t.Fatalf("thinking = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else if st, _ := out["state"].(map[string]any); st["thinking"] != true {
		t.Errorf("thinking = %v, want true", st["thinking"])
	}
}

func TestHistoryLimitMutation(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	if code, out := postJSON(t, ts, "/web/api/history-limit", tok, map[string]any{"limit": 20}); code != 200 {
		t.Fatalf("history-limit = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else if st, _ := out["state"].(map[string]any); st["historyLimit"] != float64(20) {
		t.Errorf("historyLimit = %v, want 20", st["historyLimit"])
	}
}

func TestAlertModeMutation(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	// Default is voice.
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else if st, _ := out["state"].(map[string]any); st["alertMode"] != "voice" {
		t.Errorf("default alertMode = %v, want voice", st["alertMode"])
	}

	if code, out := postJSON(t, ts, "/web/api/alert-mode", tok, map[string]any{"mode": "silent"}); code != 200 {
		t.Fatalf("alert-mode = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else if st, _ := out["state"].(map[string]any); st["alertMode"] != "silent" {
		t.Errorf("alertMode = %v, want silent", st["alertMode"])
	}

	if code, _ := postJSON(t, ts, "/web/api/alert-mode", tok, map[string]any{"mode": "bogus"}); code != http.StatusBadRequest {
		t.Errorf("invalid mode = %d, want 400", code)
	}
}

func TestVIPAddDelete(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := postJSON(t, ts, "/web/api/vip/add", tok, map[string]any{"input": "6281267858909,Tiara,Girlfriend"})
	if code != 200 {
		t.Fatalf("vip/add = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else {
		found := false
		for _, v := range out["state"].(map[string]any)["vips"].([]any) {
			entry := v.(map[string]any)
			if entry["name"] == "Tiara" && entry["relation"] == "Girlfriend" {
				found = true
			}
		}
		if !found {
			t.Errorf("vips = %v, want a Tiara entry", out["state"].(map[string]any)["vips"])
		}
	}

	code, out = postJSON(t, ts, "/web/api/vip/delete", tok, map[string]any{"jid": "6281267858909"})
	if code != 200 {
		t.Fatalf("vip/delete = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else if vips := out["state"].(map[string]any)["vips"].([]any); len(vips) != 0 {
		t.Errorf("vips after delete = %v, want empty", vips)
	}
}

func TestVIPBulkAdd(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := postJSON(t, ts, "/web/api/vip/add-bulk", tok, map[string]any{
		"entries": []string{"6281111111111,Alice,Sister", "6282222222222,Bob,Friend"},
	})
	if code != 200 {
		t.Fatalf("vip/add-bulk = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else if vips := out["state"].(map[string]any)["vips"].([]any); len(vips) != 2 {
		t.Errorf("vips after bulk = %v, want 2 entries", vips)
	}
}

func TestVIPStatusToggle(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	if code, _ := postJSON(t, ts, "/web/api/vip/add", tok, map[string]any{"input": "6281267858909,Tiara,Girlfriend"}); code != 200 {
		t.Fatalf("vip/add = %d", code)
	}
	code, out := postJSON(t, ts, "/web/api/vip/status", tok, map[string]any{"jid": "6281267858909", "enabled": false})
	if code != 200 {
		t.Fatalf("vip/status = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else {
		for _, v := range out["state"].(map[string]any)["vips"].([]any) {
			entry := v.(map[string]any)
			if entry["name"] == "Tiara" && entry["enabled"] != false {
				t.Errorf("Tiara enabled = %v, want false override", entry["enabled"])
			}
		}
	}
}

func TestAccessMutation(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	if code, _ := postJSON(t, ts, "/web/api/vip/add", tok, map[string]any{"input": "6281267858909,Tiara,Girlfriend"}); code != 200 {
		t.Fatalf("vip/add = %d", code)
	}

	code, out := postJSON(t, ts, "/web/api/access", tok, map[string]any{"jid": "6281267858909@s.whatsapp.net", "tool": "web_search", "enabled": true})
	if code != 200 {
		t.Fatalf("access = %d, want 200: %v", code, out)
	}
	if code, out := getJSON(t, ts, "/web/api/state", tok); code != 200 {
		t.Fatalf("state = %d", code)
	} else {
		for _, v := range out["state"].(map[string]any)["vips"].([]any) {
			entry := v.(map[string]any)
			if entry["name"] == "Tiara" {
				access, _ := entry["access"].([]any)
				hasWeb := false
				for _, a := range access {
					if a == "web_search" {
						hasWeb = true
					}
				}
				if !hasWeb {
					t.Errorf("Tiara access = %v, want web_search granted", entry["access"])
				}
			}
		}
	}

	code, out = postJSON(t, ts, "/web/api/access", tok, map[string]any{"jid": "6281267858909@s.whatsapp.net", "tool": "web_search", "enabled": false})
	if code != 200 {
		t.Fatalf("access revoke = %d, want 200: %v", code, out)
	}
}

func TestSendMessage(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := postJSON(t, ts, "/web/api/send", tok, map[string]any{
		"jid":  "web",
		"text": fmt.Sprintf("ignore this test message %s", strings.Repeat("x", 8)),
	})
	if code != 200 {
		t.Fatalf("send = %d, want 200: %v", code, out)
	}
	if out["state"] == nil {
		t.Fatal("send did not return a fresh state snapshot")
	}
	if out["reply"] != "Indubitably." {
		t.Errorf("send reply = %v, want Indubitably.", out["reply"])
	}
}

// TestSendMessageRejectsForeignJID guards #58: the send endpoint must only
// ever serve the web session's own conversation. A caller-supplied VIP JID
// would poison that conversation's stored history with master-context turns.
func TestSendMessageRejectsForeignJID(t *testing.T) {
	ts, _, _, st := newTestServer(t)
	tok := login(t, ts)

	victim := "6281267858909@s.whatsapp.net"
	code, _ := postJSON(t, ts, "/web/api/send", tok, map[string]any{
		"jid":  victim,
		"text": "inject into another person's history",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("send with foreign jid = %d, want 400", code)
	}
	entries, err := st.RecentMessages(victim, 10)
	if err != nil {
		t.Fatalf("read victim history: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("victim jid gained %d history rows; want 0", len(entries))
	}
}

func TestClearHistory(t *testing.T) {
	ts, _, _, st := newTestServer(t)
	tok := login(t, ts)

	if err := st.SaveMessage("web", "user", "hi"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	code, out := postJSON(t, ts, "/web/api/history/clear", tok, map[string]any{"jid": "web"})
	if code != 200 {
		t.Fatalf("clear = %d, want 200: %v", code, out)
	}
	hist, err := st.RecentMessages("web", 20)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(hist) != 0 {
		t.Errorf("history after clear = %d, want 0", len(hist))
	}
}

func TestProtocolsWebCRUD(t *testing.T) {
	ts, _, _, st := newTestServer(t)
	tok := login(t, ts)

	code, out := getJSON(t, ts, "/web/api/protocols", tok)
	if code != 200 {
		t.Fatalf("list protocols = %d, want 200", code)
	}
	if rows, ok := out["protocols"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("fresh protocols = %v", out)
	}

	code, out = postJSON(t, ts, "/web/api/protocols", tok, map[string]any{"title": "Morning News Digest", "body": "1. gather\n2. report", "origin": "master"})
	if code != 201 {
		t.Fatalf("create protocol = %d, want 201 (%v)", code, out)
	}
	proto, _ := out["protocol"].(map[string]any)
	if proto["slug"] != "morning-news-digest" {
		t.Fatalf("slug = %v", proto["slug"])
	}
	id := int(proto["id"].(float64))

	code, out = getJSON(t, ts, "/web/api/protocols", tok)
	rows, _ := out["protocols"].([]any)
	if code != 200 || len(rows) != 1 {
		t.Fatalf("list after create = %d len=%d", code, len(rows))
	}

	code, out = postJSON(t, ts, "/web/api/protocols", tok, map[string]any{"title": "Morning News Digest", "body": "1. better", "origin": "master"})
	if code != 201 {
		t.Fatalf("upsert = %d", code)
	}
	if up, _ := out["protocol"].(map[string]any); up["version"].(float64) != 2 {
		t.Fatalf("upsert version = %v, want 2", up["version"])
	}

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/web/api/protocols/"+itoa(id), strings.NewReader(`{"title":"Morning News Digest","body":"1. edited"}`))
	req = bearer(req, tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT protocol = %d, want 200", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/web/api/protocols/"+itoa(id), nil)
	req = bearer(req, tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE protocol = %d, want 200", resp.StatusCode)
	}
	if _, err := st.GetProtocol("morning-news-digest"); err == nil {
		t.Fatal("protocol should be deleted")
	}
}

func TestSchedulesWebUnavailableWithoutScheduler(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := getJSON(t, ts, "/web/api/schedules", tok)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("schedules without scheduler = %d (%v), want 503", code, out)
	}
}

func TestSchedulesWebCRUD(t *testing.T) {
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
	tok := login(t, ts)

	code, out := postJSON(t, ts, "/web/api/schedules", tok, map[string]any{"name": "morning-news", "task": "run protocol", "spec": "not-a-spec"})
	if code != 400 {
		t.Fatalf("invalid spec = %d (%v), want 400", code, out)
	}

	code, out = postJSON(t, ts, "/web/api/schedules", tok, map[string]any{"name": "morning-news", "task": "run protocol", "spec": "0 6 * * *"})
	if code != 201 {
		t.Fatalf("create schedule = %d (%v), want 201", code, out)
	}

	code, out = getJSON(t, ts, "/web/api/schedules", tok)
	if code != 200 {
		t.Fatalf("list schedules = %d", code)
	}
	rows, _ := out["schedules"].([]any)
	if len(rows) != 1 {
		t.Fatalf("schedules len = %d, want 1", len(rows))
	}
	row := rows[0].(map[string]any)
	if row["next_run"] == nil {
		t.Fatal("next_run should be set for enabled schedule")
	}

	code, _ = postJSON(t, ts, "/web/api/schedules", tok, map[string]any{"name": "morning-news", "enabled": false})
	if code != 201 {
		t.Fatalf("pause schedule = %d, want 201", code)
	}
	code, out = getJSON(t, ts, "/web/api/schedules", tok)
	rows, _ = out["schedules"].([]any)
	row = rows[0].(map[string]any)
	if row["enabled"].(bool) || row["next_run"] != nil {
		t.Fatalf("paused schedule wrong: %v", row)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/web/api/schedules/morning-news", nil)
	req = bearer(req, tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE schedule = %d, want 200", resp.StatusCode)
	}
}

func TestProtocolsRequireAuth(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	code, _ := getJSON(t, ts, "/web/api/protocols", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("protocols without token = %d, want 401", code)
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
