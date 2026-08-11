package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestOsascriptSenderPassesArgsVerbatim(t *testing.T) {
	var gotName string
	var gotArgs []string
	s := &osascriptSender{runner: func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = args
		return exec.Command("echo")
	}}

	if err := s.Send("+6281267858909", "hello there; rm -rf /"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotName != "osascript" {
		t.Errorf("name = %q, want osascript", gotName)
	}

	// The critical property: the recipient and message travel as argv entries,
	// never interpolated into a shell string, so shell metacharacters are inert.
	want := []string{"-", "+6281267858909", "hello there; rm -rf /"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %q, want %q", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, gotArgs[i], want[i])
		}
	}

	if !strings.Contains(sendScript, "service type is iMessage") {
		t.Error("script should target the iMessage service, got:", sendScript)
	}
}

func TestOsascriptSenderPropagatesFailure(t *testing.T) {
	s := &osascriptSender{runner: func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}}
	if err := s.Send("+6281267858909", "hi"); err == nil {
		t.Fatal("non-zero exit should surface as an error")
	}
}
