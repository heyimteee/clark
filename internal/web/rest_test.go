package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
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
