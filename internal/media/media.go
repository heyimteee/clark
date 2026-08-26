// Package media wraps the container's ffmpeg and pdftotext binaries for
// local-first media processing: audio conversion for transcription, sticker
// normalization, video keyframe extraction, and PDF text extraction. Every
// helper is size-capped and returns wrapped errors; nothing here talks to
// the network.
package media

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
)

// Input caps — generous but bounded, so a hostile attachment cannot exhaust
// CPU or memory. WhatsApp itself caps most of these far lower in practice.
const (
	MaxAudioBytes   = 25 << 20 // voice notes
	MaxStickerBytes = 5 << 20  // stickers (static or animated)
	MaxVideoBytes   = 50 << 20 // clips/GIFs
	MaxDocBytes     = 20 << 20 // documents
	MaxPDFChars     = 100_000  // extracted text chars before TRUNCATED flagging
)

// ffCtx builds an exec.Cmd bound to ctx with stdin/stdout/stderr pipes wired.
func runFF(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-hide_banner", "-loglevel", "error"}, args...)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg %s: %w: %s", args[0], err, errBuf.String())
	}
	return out.Bytes(), nil
}

// ToWav16k converts any audio container (ogg/opus from WhatsApp voice notes)
// to mono 16 kHz WAV bytes for the whisper daemon.
func ToWav16k(ctx context.Context, audio []byte) ([]byte, error) {
	if len(audio) == 0 || len(audio) > MaxAudioBytes {
		return nil, fmt.Errorf("audio size %d out of bounds", len(audio))
	}
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-ac", "1", "-ar", "16000",
		"-f", "wav", "pipe:1")
	cmd.Stdin = bytes.NewReader(audio)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg wav convert: %w: %s", err, errBuf.String())
	}
	return out.Bytes(), nil
}

// ToPNG normalizes a static image blob (e.g. webp stickers) to PNG so any
// vision backend can consume it regardless of source format support.
func ToPNG(ctx context.Context, img []byte) ([]byte, error) {
	if len(img) == 0 || len(img) > MaxStickerBytes {
		return nil, fmt.Errorf("image size %d out of bounds", len(img))
	}
	return runFF(ctx, "-i", "pipe:0", "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "pipe:1")
}

// ExtractFrames pulls n evenly spaced frames from a video clip as JPEGs,
// scaled to maxWidth keeping aspect. Returns one blob per frame, in order.
func ExtractFrames(ctx context.Context, video []byte, n, maxWidth int) ([][]byte, error) {
	if n < 1 || len(video) == 0 || len(video) > MaxVideoBytes {
		return nil, fmt.Errorf("video size %d or frame count %d out of bounds", len(video), n)
	}
	frames := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		// Seek to (i + 0.5)/n of the stream so frames land inside segments.
		at := (float64(i) + 0.5) / float64(n)
		blob, err := runFF(ctx,
			"-i", "pipe:0",
			"-ss", strconv.FormatFloat(at, 'f', -1, 64),
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", maxWidth),
			"-q:v", "5",
			"-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1")
		if err != nil || len(blob) == 0 {
			continue // sparse streams may yield fewer than n frames; keep going
		}
		frames = append(frames, blob)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames extracted")
	}
	return frames, nil
}

// ExtractText pulls plain text out of a PDF via pdftotext. Returns the text
// and whether truncation occurred at MaxPDFChars.
func ExtractText(ctx context.Context, pdf []byte) (string, bool, error) {
	if len(pdf) == 0 || len(pdf) > MaxDocBytes {
		return "", false, fmt.Errorf("pdf size %d out of bounds", len(pdf))
	}
	cmd := exec.CommandContext(ctx, "pdftotext", "-", "-")
	cmd.Stdin = bytes.NewReader(pdf)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("pdftotext: %w: %s", err, errBuf.String())
	}
	text := out.String()
	truncated := false
	if len(text) > MaxPDFChars {
		text = text[:MaxPDFChars]
		truncated = true
	}
	return text, truncated, nil
}
