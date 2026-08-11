package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// Sender delivers an iMessage to a handle. Kept as an interface so tests can
// substitute a fake and the osascript path can evolve without touching callers.
type Sender interface {
	Send(recipient, text string) error
}

// cmdRunner abstracts exec.Command for testability.
type cmdRunner func(name string, args ...string) *exec.Cmd

// osascriptSender sends via the Messages.app AppleScript "send" verb, which
// remains available on modern macOS (the receive handlers were removed).
type osascriptSender struct {
	runner cmdRunner
}

// NewSender returns the default AppleScript-backed sender.
func NewSender() Sender {
	return &osascriptSender{runner: exec.Command}
}

// The script picks the iMessage account explicitly (never SMS) and delivers
// the text verbatim via argv, sidestepping shell quoting entirely.
const sendScript = `on run argv
	set targetBuddy to item 1 of argv
	set targetMessage to item 2 of argv
	tell application "Messages"
		set theService to 1st service whose service type is iMessage
		send targetMessage to buddy targetBuddy of theService
	end tell
end run`

// Send delivers text to recipient via AppleScript. It requires the process
// hosting the bridge to hold Automation permission for Messages.app.
func (s *osascriptSender) Send(recipient, text string) error {
	cmd := s.runner("osascript", "-", recipient, text)
	cmd.Stdin = strings.NewReader(sendScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
