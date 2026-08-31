package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
		if a.Filename == "" {
			continue
		}
		// Path traversal guard: only allow files under the user's Attachments dir.
		clean := filepath.Clean(a.Filename)
		// Attachments are under ~/Library/Messages/Attachments or /var/.../Attachments
		// Allow any path that contains "/Attachments/" segment.
		if !strings.Contains(clean, "/Attachments/") {
			logging.Log("BRIDGE", logging.SevWarn, "MEDIA", "Skipping attachment outside Attachments dir", "file", clean)
			continue
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			logging.Log("BRIDGE", logging.SevWarn, "MEDIA", "Failed to read attachment", "file", clean, "error", err)
			continue
		}
		if len(data) > maxIMessageMediaBytes {
			data = data[:maxIMessageMediaBytes]
		}
		mimeType := firstNonEmpty(a.MimeType, utiToMIME(a.UTI), mime.TypeByExtension(filepath.Ext(a.TransferName)), http.DetectContentType(firstBytes(data, 512)))
		mtype := classifyMIMEMedia(mimeType, a.UTI)
		name := firstNonEmpty(a.TransferName, filepath.Base(clean))
		out = append(out, imessage.InboundMedia{
			Type: mtype,
			Name: name,
			MIME: mimeType,
			Data: data,
		})
	}
	return out
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

// encodeForTransport is a helper for testing: base64 is handled by json.Marshal
// automatically for []byte, but we keep it explicit for logging.
func encodeForTransport(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

var _ = fmt.Sprintf // keep import used if needed
var _ = encodeForTransport
