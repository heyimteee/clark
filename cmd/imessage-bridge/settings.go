package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/heyimteee/clark/internal/logging"
)

// System Settings deep-link anchors for the two TCC panes the bridge depends
// on. Apple offers no API to request Full Disk Access, so opening the exact
// pane is the closest the bridge can get to self-service.
const (
	settingsPaneFDA      = "Privacy_AllFiles"
	settingsPaneCalendar = "Privacy_Calendars"
)

// openSettingsURL opens a System Settings pane. It is a variable so tests can
// stub the GUI side effect.
var openSettingsURL = func(pane string) error {
	return exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?"+pane).Run()
}

// stateDirForMarkers resolves the directory holding the bridge state file,
// reused for once-per-episode marker files.
func stateDirForMarkers() string {
	if p := os.Getenv("IMESSAGE_STATE_PATH"); p != "" {
		return filepath.Dir(p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, "Library", "Application Support", "clark-bridge")
}

// maybeOpenSettingsPane opens the given Settings pane unless a marker shows it
// was already opened for the current denial episode. Best effort: failures are
// logged, never fatal.
func maybeOpenSettingsPane(stateDir, pane string) {
	marker := filepath.Join(stateDir, "settings-opened-"+pane)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if err := openSettingsURL(pane); err != nil {
		logging.Log("BRIDGE", logging.SevWarn, "SETTINGS", "Could not open System Settings pane", "pane", pane, "error", err)
		return
	}
	logging.Log("BRIDGE", logging.SevNotice, "SETTINGS", "Opened System Settings pane", "pane", pane)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(marker, []byte("opened"), 0o600)
}

// clearSettingsPaneMarker drops the opened marker so a future denial episode
// opens the pane again.
func clearSettingsPaneMarker(stateDir, pane string) {
	_ = os.Remove(filepath.Join(stateDir, "settings-opened-"+pane))
}
