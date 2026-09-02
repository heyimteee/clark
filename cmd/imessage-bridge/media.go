package main

import (
	"database/sql"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/imessage"
	"github.com/heyimteee/clark/internal/logging"
)

// maxIMessageMediaBytes caps an attachment read from disk.
const maxIMessageMediaBytes = 50 << 20

// collectIMessageMedia reads the attachments for a message and converts them
// to imessage.InboundMedia slices ready for transport. It mirrors
// whatsapp/collectMedia's caps and classification but reads from the
// filesystem instead of via whatsmeow download.
func collectIMessageMedia(db *sql.DB, m newMessage) []imessage.InboundMedia {
	if !m.HasAttachments {
		return nil
	}
	rows, err := queryAttachments(db, m.RowID)
	if err != nil {
		logging.Log("BRIDGE", logging.SevWarn, "MEDIA", "Failed to query attachments", "row", m.RowID, "error", err)
		return nil
	}
	var out []imessage.InboundMedia
	for _, a := range rows {
		if media := buildInboundMedia(a); media != nil {
			out = append(out, *media)
		}
	}
	return out
}

// buildInboundMedia reads and classifies one attachment row. It expands a
// leading ~, guards against paths outside the Attachments tree, retries the
// read to survive the flush race between the message row and its file, and
// converts iPhone HEIC photos to JPEG via sips (the Debian container's
// ffmpeg cannot decode HEIC, so conversion must happen here on the Mac).
func buildInboundMedia(a attachmentRow) *imessage.InboundMedia {
	if a.Filename == "" {
		return nil
	}
	clean := expandHome(filepath.Clean(a.Filename))
	// Path traversal guard: only allow files under an Attachments dir.
	if !strings.Contains(clean, "/Attachments/") {
		logging.Log("BRIDGE", logging.SevWarn, "MEDIA", "Skipping attachment outside Attachments dir", "file", clean)
		return nil
	}
	data, err := readFileWithRetry(clean)
	if err != nil {
		logging.Log("BRIDGE", logging.SevWarn, "MEDIA", "Failed to read attachment", "file", clean, "error", err)
		return nil
	}
	if len(data) > maxIMessageMediaBytes {
		logging.Log("BRIDGE", logging.SevWarn, "MEDIA", "Skipping oversized attachment", "file", clean, "bytes", len(data))
		return nil
	}
	mimeType := firstNonEmpty(a.MimeType, utiToMIME(a.UTI), mime.TypeByExtension(filepath.Ext(a.TransferName)), http.DetectContentType(firstBytes(data, 512)))
	mtype := classifyMIMEMedia(mimeType, a.UTI)
	name := firstNonEmpty(a.TransferName, filepath.Base(clean))

	if isHEIC(mimeType, a.UTI, clean) {
		if jpeg, ok := heicToJPEG(clean); ok {
			data, mimeType, mtype = jpeg, "image/jpeg", "image"
		} else {
			logging.Log("BRIDGE", logging.SevWarn, "MEDIA", "HEIC conversion failed; passing original", "file", clean)
		}
	}
	return &imessage.InboundMedia{
		Type: mtype,
		Name: name,
		MIME: mimeType,
		Data: data,
	}
}

// expandHome resolves a leading ~ so chat.db paths like "~/Library/Messages/…"
// become readable without a shell.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// readFileWithRetry retries short reads: Messages.app may commit the message
// row a moment before the attachment file lands on disk.
func readFileWithRetry(path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func isHEIC(mimeType, uti, path string) bool {
	if strings.HasPrefix(mimeType, "image/heic") || strings.HasPrefix(mimeType, "image/heif") {
		return true
	}
	if uti == "public.heic" || uti == "public.heif" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".heic" || ext == ".heif"
}

// heicToJPEG converts an HEIC image to JPEG via macOS sips. Returns false if
// sips is unavailable or the conversion fails; the caller falls back to the
// original bytes.
func heicToJPEG(path string) ([]byte, bool) {
	if _, err := exec.LookPath("sips"); err != nil {
		return nil, false
	}
	tmp, err := os.CreateTemp("", "clark-heic-*.jpg")
	if err != nil {
		return nil, false
	}
	out := tmp.Name()
	tmp.Close()
	defer os.Remove(out)
	if outBytes, err := exec.Command("sips", "-s", "format", "jpeg", path, "--out", out).CombinedOutput(); err != nil {
		logging.Log("BRIDGE", logging.SevWarn, "MEDIA", "sips conversion error", "error", err.Error(), "output", string(outBytes))
		return nil, false
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, false
	}
	return data, true
}

func firstBytes(data []byte, n int) []byte {
	if len(data) < n {
		return data
	}
	return data[:n]
}

// utiToMIME maps common UTIs to MIME.
func utiToMIME(uti string) string {
	switch uti {
	case "public.jpeg":
		return "image/jpeg"
	case "public.png":
		return "image/png"
	case "public.heic", "public.heif":
		return "image/heic"
	case "com.compuserve.gif":
		return "image/gif"
	case "public.mpeg-4", "public.movie", "public.mpeg-4-audio":
		return "video/mp4"
	case "public.aac-audio", "public.mp3":
		return "audio/aac"
	case "com.apple.quicktime-movie":
		return "video/quicktime"
	default:
		return ""
	}
}

func classifyMIMEMedia(mime, uti string) string {
	if strings.HasPrefix(mime, "image/") {
		if mime == "image/gif" || uti == "com.compuserve.gif" {
			return "gif"
		}
		return "image"
	}
	if strings.HasPrefix(mime, "video/") {
		return "video"
	}
	if strings.HasPrefix(mime, "audio/") {
		return "audio"
	}
	if uti == "com.compuserve.gif" {
		return "gif"
	}
	// Fallback: treat unknown as document so it can be digested if text-like.
	return "document"
}
