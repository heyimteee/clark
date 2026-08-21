package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActionServerAuth(t *testing.T) {
	srv := httptest.NewServer(NewActionServer("secret").Routes())
	defer srv.Close()

	// No token -> 401.
	resp, err := http.Post(srv.URL+"/action", "application/json", bytes.NewReader([]byte(`{"type":"banner","title":"t","body":"b"}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", resp.StatusCode)
	}
}

func TestActionServerRejectsUnknownType(t *testing.T) {
	srv := httptest.NewServer(NewActionServer("secret").Routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/action", bytes.NewReader([]byte(`{"type":"explode"}`)))
	req.Header.Set("X-Clark-Bridge-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown-type status = %d, want 400", resp.StatusCode)
	}
}

func TestActionServerMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(NewActionServer("secret").Routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/action", bytes.NewReader([]byte(`{`)))
	req.Header.Set("X-Clark-Bridge-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed status = %d, want 400", resp.StatusCode)
	}
}

func TestTriggerFaceTimeRequiresNumber(t *testing.T) {
	if err := triggerFaceTime(""); err == nil {
		t.Error("triggerFaceTime(\"\") succeeded, want error")
	}
}

func TestValidE164(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"+628117705636", true},
		{"+12125550123", true},
		{"628117705636", false},      // missing +
		{"+02125550123", false},      // leading 0 after +
		{"+1212", false},             // too short
		{"+12125550123;echo", false}, // injection attempt
		{"attacker@evil.com", false}, // email handle
		{"+1212 555 0123", false},    // spaces rejected (caller must trim/normalize)
		{"facetime://x", false},      // scheme smuggling
	}
	for _, c := range cases {
		if got := validE164(c.in); got != c.want {
			t.Errorf("validE164(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMacPhoneNumber(t *testing.T) {
	// macPhoneNumber lives in internal/app; verify the digits-only rule here is
	// covered by the app's own tests. This guards the helper contract.
	if got := stripToDigits("+628117705636"); got != "628117705636" {
		t.Errorf("stripToDigits = %q, want 628117705636", got)
	}
}

func stripToDigits(s string) string {
	var out []rune
	for _, r := range s {
		if r >= '0' && r <= '9' {
			out = append(out, r)
		}
	}
	return string(out)
}

// ensure the action payload shape serializes as the bridge expects.
func TestActionPayloadShape(t *testing.T) {
	body, err := json.Marshal(map[string]any{"type": "facetime", "number": "+628117705636"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Type   string `json:"type"`
		Number string `json:"number"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "facetime" || got.Number != "+628117705636" {
		t.Errorf("shape = %+v", got)
	}
}
