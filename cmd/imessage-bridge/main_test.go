package main

import (
	"strings"
	"testing"
)

// TestLoadBridgeConfigRequiresToken enforces fail-closed auth (#57): the
// bridge forwards private iMessages and exposes the FaceTime/banner action
// API, so it must refuse to run without IMESSAGE_BRIDGE_TOKEN.
func TestLoadBridgeConfigRequiresToken(t *testing.T) {
	t.Setenv("IMESSAGE_BRIDGE_URL", "https://clark.example.com")
	t.Setenv("IMESSAGE_BRIDGE_TOKEN", "")

	_, err := loadBridgeConfig()
	if err == nil {
		t.Fatal("loadBridgeConfig succeeded with an empty IMESSAGE_BRIDGE_TOKEN; want an error")
	}
	if !strings.Contains(err.Error(), "IMESSAGE_BRIDGE_TOKEN") {
		t.Errorf("error = %v, want it to name IMESSAGE_BRIDGE_TOKEN", err)
	}
}

func TestLoadBridgeConfigWithToken(t *testing.T) {
	t.Setenv("IMESSAGE_BRIDGE_URL", "https://clark.example.com")
	t.Setenv("IMESSAGE_BRIDGE_TOKEN", "secret")

	cfg, err := loadBridgeConfig()
	if err != nil {
		t.Fatalf("loadBridgeConfig: %v", err)
	}
	if cfg.token != "secret" {
		t.Errorf("token = %q, want secret", cfg.token)
	}
}
