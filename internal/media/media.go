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
	"math"
	"os/exec"
	"strconv"
	"strings"
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

// runFF builds an exec.Cmd bound to ctx with stdin/stdout/stderr pipes wired.
func runFF(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-hide_banner", "-loglevel", "error"}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
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

func probeDuration(ctx context.Context, data []byte) float64 {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", "pipe:0")
	cmd.Stdin = bytes.NewReader(data)
	if b, err := cmd.Output(); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" && s != "N/A" {
			if f, e := strconv.ParseFloat(s, 64); e == nil && f > 0 && !math.IsNaN(f) && !math.IsInf(f, 0) && f < 3600 {
				return f
			}
		}
	}
	cmd = exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames,avg_frame_rate,r_frame_rate",
		"-of", "default=noprint_wrappers=1", "pipe:0")
	cmd.Stdin = bytes.NewReader(data)
	if b, err := cmd.Output(); err == nil {
		var nb int
		var fps float64
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "nb_read_frames=") {
				fmt.Sscanf(l, "nb_read_frames=%d", &nb)
			} else if strings.HasPrefix(l, "avg_frame_rate=") {
				var a, b int
				fmt.Sscanf(l, "avg_frame_rate=%d/%d", &a, &b)
				if b != 0 {
					fps = float64(a) / float64(b)
				}
			}
		}
		if nb > 0 && fps > 0 {
			return float64(nb) / fps
		}
	}
	return 0
}

func splitJPEGs(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i < len(b)-1; i++ {
		if b[i] == 0xFF && b[i+1] == 0xD8 {
			if start != -1 {
				out = append(out, bytes.Clone(b[start:i]))
			}
			start = i
		}
	}
	if start != -1 {
		out = append(out, bytes.Clone(b[start:]))
	}
	return out
}

func isAnimatedWebP(b []byte) bool {
	if len(b) < 16 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return false
	}
	if bytes.Contains(b, []byte("ANIM")) {
		return true
	}
	if len(b) > 21 && b[20]&0x02 != 0 {
		return true
	}
	return false
}

// ToPNG normalizes a static image blob (e.g. webp stickers) to PNG so any
// vision backend can consume it regardless of source format support.
func ToPNG(ctx context.Context, img []byte) ([]byte, error) {
	if len(img) == 0 || len(img) > MaxStickerBytes {
		return nil, fmt.Errorf("image size %d out of bounds", len(img))
	}
	if isAnimatedWebP(img) {
		if b, err := runFF(ctx, img,
			"-probesize", "10M", "-analyzeduration", "10M",
			"-i", "pipe:0",
			"-vf", "select=eq(n\\,0),scale='min(768,iw)':-2", "-vsync", "vfr",
			"-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "pipe:1"); err == nil && len(b) > 0 {
			return b, nil
		}
		if b, err := runFF(ctx, img, "-i", "pipe:0", "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "pipe:1"); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	out, err := runFF(ctx, img, "-i", "pipe:0", "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "pipe:1")
	if err == nil && len(out) > 0 {
		return out, nil
	}
	if out2, err2 := runFF(ctx, img,
		"-probesize", "10M", "-analyzeduration", "10M",
		"-i", "pipe:0", "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "pipe:1"); err2 == nil && len(out2) > 0 {
		return out2, nil
	}
	return nil, err
}

// ExtractFrames pulls n evenly spaced frames from a video clip as JPEGs,
// scaled to maxWidth keeping aspect. Returns one blob per frame, in order.
func ExtractFrames(ctx context.Context, video []byte, n, maxWidth int) ([][]byte, error) {
	if n < 1 || len(video) == 0 || len(video) > MaxVideoBytes {
		return nil, fmt.Errorf("video size %d or frame count %d out of bounds", len(video), n)
	}
	dur := probeDuration(ctx, video)
	if dur > 0 {
		fps := float64(n) / dur
		if fps < 0.2 {
			fps = 0.2
		}
		if fps > 30 {
			fps = 30
		}
		vf := fmt.Sprintf("fps=%f,scale='min(%d,iw)':-2", fps, maxWidth)
		if raw, err := runFF(ctx, video, "-i", "pipe:0", "-vf", vf, "-q:v", "5", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1"); err == nil && len(raw) > 0 {
			frames := splitJPEGs(raw)
			if len(frames) > 0 {
				if len(frames) > n {
					step := float64(len(frames)) / float64(n)
					sampled := make([][]byte, 0, n)
					for i := 0; i < n; i++ {
						sampled = append(sampled, frames[int(math.Floor(float64(i)*step))])
					}
					frames = sampled
				}
				return frames, nil
			}
		}
	}
	if dur > 0 {
		frames := make([][]byte, 0, n)
		for i := 0; i < n; i++ {
			at := (float64(i) + 0.5) / float64(n) * dur
			blob, err := runFF(ctx, video,
				"-i", "pipe:0",
				"-ss", strconv.FormatFloat(at, 'f', 6, 64),
				"-frames:v", "1",
				"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", maxWidth),
				"-q:v", "5",
				"-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1")
			if err != nil || len(blob) == 0 {
				continue
			}
			frames = append(frames, blob)
		}
		if len(frames) > 0 {
			return frames, nil
		}
	}
	raw, err := runFF(ctx, video, "-i", "pipe:0",
		"-vf", fmt.Sprintf("thumbnail,scale='min(%d,iw)':-2", maxWidth),
		"-frames:v", strconv.Itoa(n),
		"-vsync", "vfr", "-q:v", "5", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1")
	if err == nil && len(raw) > 0 {
		if frames := splitJPEGs(raw); len(frames) > 0 {
			return frames, nil
		}
	}
	return nil, fmt.Errorf("no frames extracted")
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
