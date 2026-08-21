package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/heyimteee/clark/internal/logging"
)

const (
	maxAudioBytes   = 25 << 20 // 25 MB body cap for STT uploads (§6.5)
	maxTTSTextChars = 4000
	// ttsSynthTimeout bounds a single synthesis call. Kokoro on the i5 runs
	// ~1.2-1.5x real-time, so a long reply (up to 4000 chars, ~5 min of
	// speech) needs a generous ceiling; 30s was killing long replies with
	// "context deadline exceeded".
	ttsSynthTimeout = 3 * time.Minute
)

// handleVoiceStatus reports the voice seam as the flat §6.5 shape.
func (s *Server) handleVoiceStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"sttModel":  s.sttModel,
		"ttsEngine": s.ttsEngine,
		"ttsVoice":  s.ttsVoice(),
		"available": s.voice != nil && (s.voice.STT != nil || s.voice.TTS != nil),
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTTS synthesizes text and returns base64 WAV audio (§6.5).
func (s *Server) handleTTS(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil || s.voice.TTS == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "text to speech is not available"})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is required"})
		return
	}
	if len(body.Text) > maxTTSTextChars {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is too long"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ttsSynthTimeout)
	defer cancel()
	logging.Log("WEB", logging.SevInfo, "TTS", "Synthesizing", "chars", len(body.Text))
	wav, err := s.voice.TTS.Synthesize(ctx, stripForSpeech(body.Text))
	if err != nil {
		logging.Log("WEB", logging.SevWarn, "TTS", "Synthesis failed", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "synthesis failed"})
		return
	}
	logging.Log("WEB", logging.SevInfo, "TTS", "Synthesis OK", "bytes", len(wav))
	writeJSON(w, http.StatusOK, map[string]any{
		"audio":  base64.StdEncoding.EncodeToString(wav),
		"format": "audio/wav",
	})
}

// handleSpeech returns raw WAV audio for direct <audio> playback in the browser.
func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil || s.voice.TTS == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "text to speech is not available"})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is required"})
		return
	}
	if len(body.Text) > maxTTSTextChars {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is too long"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ttsSynthTimeout)
	defer cancel()
	wav, err := s.voice.TTS.Synthesize(ctx, stripForSpeech(body.Text))
	if err != nil {
		logging.Log("WEB", logging.SevWarn, "TTS", "Synthesis failed", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "synthesis failed"})
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(wav); err != nil {
		logging.Log("WEB", logging.SevWarn, "TTS", "Speech stream interrupted", "error", err.Error())
	}
}

// handleSTT transcribes a base64 WAV clip (§6.5). The body is capped at
// maxAudioBytes.
func (s *Server) handleSTT(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil || s.voice.STT == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "speech to text is not available"})
		return
	}
	logging.Log("WEB", logging.SevInfo, "STT", "STT request received", "content_length", r.ContentLength)
	var body struct {
		Audio string `json:"audio"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAudioBytes)
	// Decode directly here instead of decodeBody: decodeBody wraps the body in
	// its own 1 MB MaxBytesReader, silently overriding the 25 MB STT cap and
	// rejecting any clip whose base64 body exceeds ~1 MB (~18 s of audio).
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "audio is too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if body.Audio == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "audio is required"})
		return
	}
	audio, err := base64.StdEncoding.DecodeString(body.Audio)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "audio is not valid base64"})
		return
	}
	if len(audio) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty audio"})
		return
	}

	// Bound concurrent transcriptions: whisper is CPU-hungry and shares the
	// box with Ollama; unbounded concurrency starves everything else (#60).
	s.sttSlots <- struct{}{}
	defer func() { <-s.sttSlots }()

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	logging.Log("WEB", logging.SevInfo, "STT", "Transcribing", "audio_bytes", len(audio))
	text, err := s.voice.STT.Transcribe(ctx, audio)
	if err != nil {
		logging.Log("WEB", logging.SevWarn, "STT", "Transcription failed", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "transcription failed"})
		return
	}
	logging.Log("WEB", logging.SevInfo, "STT", "Transcription OK", "chars", len(text))
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

// stripForSpeech removes markdown formatting and emoji so TTS only receives
// clean plain text. Mirrors the iMessage messenger's patterns but extended and
// run twice: double-asterisk bold must be stripped before single-asterisk
// emphasis, and a second pass mops up any leftovers (e.g. "*a * b*") so TTS
// never reads aloud "asterisk Available asterisk".
func stripForSpeech(s string) string {
	for pass := 0; pass < 2; pass++ {
		for _, re := range speechStripRe {
			s = re.ReplaceAllString(s, "$1")
		}
	}
	return s
}

// speechStripRe strips block and inline markdown in a deterministic order.
// Block constructs first (fences, headings, quotes, bullets), then code spans
// (double-backtick before single), then emphasis (double before single), then
// links. Only paired delimiters are unwrapped so literals like "2*3" or
// "a * b" pass through untouched.
var speechStripRe = []*regexp.Regexp{
	// "```code```" → "code"
	regexp.MustCompile(`(?s)\x60{3}[^\n]*\n?(.*?)\x60{3}`),
	// "# Title" → "Title"
	regexp.MustCompile(`(?m)^#{1,6}\s+(.*)$`),
	// "> quote" → "quote"
	regexp.MustCompile(`(?m)^>\s?(.*)$`),
	// "- item" / "+ item" / "* item" bullets → "item"
	regexp.MustCompile(`(?m)^\s*[-+*]\s+`),
	// "``code``" → "code"
	regexp.MustCompile("``([^`]+)``"),
	// "`code`" → "code"
	regexp.MustCompile("`([^`]+)`"),
	// "**bold**" → "bold" (before single asterisk)
	regexp.MustCompile(`\*\*([^*]+)\*\*`),
	// "__bold__" → "bold" (before single underscore)
	regexp.MustCompile(`__([^_]+)__`),
	// "*bold*" → "bold"
	regexp.MustCompile(`\*([^*\n]+)\*`),
	// "_italic_" → "italic"
	regexp.MustCompile(`_([^_\n]+)_`),
	// "~strike~" → "strike"
	regexp.MustCompile(`~([^~\n]+)~`),
	// "[text](url)" → "text"
	regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`),
	// Emoji + common symbols (🚨, ™, ©, arrows, etc.) → "" so they are never
	// read aloud by TTS. Keeps the WhatsApp/web rendering untouched; this strip
	// only feeds the speech engine.
	regexp.MustCompile(`[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2190}-\x{21FF}\x{2B00}-\x{2BFF}]`),
	regexp.MustCompile(`[\x{FE0F}\x{200D}]`),
}
