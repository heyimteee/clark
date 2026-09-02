package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := expandHome("~/Library/Messages/chat.db"); got != filepath.Join(home, "Library/Messages/chat.db") {
		t.Fatalf("expandHome(~/...) = %q", got)
	}
	if got := expandHome("~"); got != home {
		t.Fatalf("expandHome(~) = %q", got)
	}
	abs := filepath.Join(home, "x", "y.png")
	if got := expandHome(abs); got != abs {
		t.Fatalf("absolute path changed: %q", got)
	}
}

func TestBuildInboundMediaPng(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "Messages", "Attachments", "ab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	path := filepath.Join(dir, "img.png")
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}

	m := buildInboundMedia(attachmentRow{Filename: "~/Library/Messages/Attachments/ab/img.png", TransferName: "img.png"})
	if m == nil {
		t.Fatal("buildInboundMedia returned nil for a valid png")
	}
	if m.Type != "image" {
		t.Fatalf("type = %q, want image", m.Type)
	}
	if string(m.Data) != string(png) {
		t.Fatal("data mismatch")
	}
}

func TestBuildInboundMediaRejectsOutsideAttachments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m := buildInboundMedia(attachmentRow{Filename: path}); m != nil {
		t.Fatal("file outside Attachments dir should be rejected")
	}
}

func TestBuildInboundMediaMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Path inside Attachments dir but the file does not exist: retries then nil.
	if m := buildInboundMedia(attachmentRow{Filename: "~/Library/Messages/Attachments/ab/nope.png"}); m != nil {
		t.Fatal("missing attachment should return nil")
	}
}

func TestBuildInboundMediaHEICFallsBackToOriginal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "Messages", "Attachments", "cd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Not a real HEIC — sips will fail, and the original bytes must be kept
	// (graceful degradation), with the image type still classified.
	fake := []byte("not a real heic")
	path := filepath.Join(dir, "photo.heic")
	if err := os.WriteFile(path, fake, 0o644); err != nil {
		t.Fatal(err)
	}

	m := buildInboundMedia(attachmentRow{Filename: path, TransferName: "photo.heic"})
	if m == nil {
		t.Fatal("HEIC fallback should still return the original media")
	}
	if string(m.Data) != string(fake) {
		t.Fatal("fallback should carry original bytes")
	}
	if m.Type != "image" {
		t.Fatalf("type = %q, want image", m.Type)
	}
}

func TestUTIToMIME(t *testing.T) {
	tests := []struct{ uti, want string }{
		{"public.jpeg", "image/jpeg"},
		{"public.png", "image/png"},
		{"public.heic", "image/heic"},
		{"com.compuserve.gif", "image/gif"},
		{"com.apple.quicktime-movie", "video/quicktime"},
		{"public.unknown-uti", ""},
	}
	for _, tt := range tests {
		if got := utiToMIME(tt.uti); got != tt.want {
			t.Errorf("utiToMIME(%q) = %q, want %q", tt.uti, got, tt.want)
		}
	}
}

func TestClassifyMIMEMedia(t *testing.T) {
	tests := []struct {
		mime, uti, want string
	}{
		{"image/jpeg", "", "image"},
		{"image/gif", "", "gif"},
		{"", "com.compuserve.gif", "gif"},
		{"video/mp4", "", "video"},
		{"audio/aac", "", "audio"},
		{"", "", "document"},
	}
	for _, tt := range tests {
		if got := classifyMIMEMedia(tt.mime, tt.uti); got != tt.want {
			t.Errorf("classifyMIMEMedia(%q, %q) = %q, want %q", tt.mime, tt.uti, got, tt.want)
		}
	}
}

func TestReadFileWithRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := readFileWithRetry(path)
	if err != nil || string(data) != "hi" {
		t.Fatalf("read: %v %q", err, data)
	}
	if _, err := readFileWithRetry(filepath.Join(t.TempDir(), "gone.txt")); err == nil {
		t.Fatal("missing file should error")
	}
}

func TestIsHEIC(t *testing.T) {
	if !isHEIC("image/heic", "", "") || !isHEIC("", "public.heif", "") || !isHEIC("", "", "/x/y.HEIC") {
		t.Fatal("HEIC detection failed")
	}
	if isHEIC("image/png", "public.png", "/x/y.png") {
		t.Fatal("png misdetected as HEIC")
	}
	if strings.Contains("/x/y.png", "heic") {
		t.Fatal("sanity")
	}
}
