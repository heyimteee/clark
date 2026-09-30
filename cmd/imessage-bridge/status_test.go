package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func statusTestServer(t *testing.T, srv *ActionServer) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

func getStatus(t *testing.T, url, token string) (int, StatusReport) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url+"/status", nil)
	if token != "" {
		req.Header.Set("X-Clark-Bridge-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var rep StatusReport
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, rep
}

func TestStatusEndpointShape(t *testing.T) {
	srv := NewActionServer("secret")
	srv.SetStatusFunc(func() StatusReport {
		return StatusReport{ChatDB: "denied", Calendar: "not_determined", Watcher: "disabled", Error: "nope"}
	})
	ts := statusTestServer(t, srv)

	code, rep := getStatus(t, ts.URL, "secret")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if rep.ChatDB != "denied" || rep.Calendar != "not_determined" || rep.Watcher != "disabled" || rep.Error != "nope" {
		t.Fatalf("report = %+v", rep)
	}
}

func TestStatusEndpointRequiresAuth(t *testing.T) {
	srv := NewActionServer("secret")
	ts := statusTestServer(t, srv)
	if code, _ := getStatus(t, ts.URL, ""); code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", code)
	}
}

func TestStatusEndpointDefaultUnknown(t *testing.T) {
	srv := NewActionServer("secret")
	ts := statusTestServer(t, srv)
	code, rep := getStatus(t, ts.URL, "secret")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if rep.ChatDB != "unknown" || rep.Calendar != "unknown" || rep.Watcher != "unknown" {
		t.Fatalf("default report = %+v, want unknowns", rep)
	}
}

func TestProbeChatDBMissing(t *testing.T) {
	if err := probeChatDB(filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Fatal("probeChatDB on missing file succeeded, want error")
	}
}

func TestSettingsPaneOncePerEpisode(t *testing.T) {
	old := openSettingsURL
	calls := 0
	openSettingsURL = func(pane string) error {
		calls++
		if pane != "Privacy_Test" {
			t.Errorf("pane = %q", pane)
		}
		return nil
	}
	t.Cleanup(func() { openSettingsURL = old })

	dir := t.TempDir()
	maybeOpenSettingsPane(dir, "Privacy_Test")
	maybeOpenSettingsPane(dir, "Privacy_Test")
	if calls != 1 {
		t.Fatalf("opener calls = %d, want 1 (second suppressed by marker)", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings-opened-Privacy_Test")); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	clearSettingsPaneMarker(dir, "Privacy_Test")
	maybeOpenSettingsPane(dir, "Privacy_Test")
	if calls != 2 {
		t.Fatalf("opener calls = %d, want 2 after clear", calls)
	}
}
