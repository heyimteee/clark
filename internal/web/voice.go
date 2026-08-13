package web

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/heyimteee/clark/internal/logging"
)

const (
	maxAudioBytes   = 25 << 20 // 25 MB body cap for STT uploads (§6.5)
	maxTTSTextChars = 4000
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
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
	if err := decodeBody(w, r, &body); err != nil {
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

// stripForSpeech removes markdown formatting so TTS only receives clean plain
// text. Mirrors the same patterns used by the iMessage messenger so Piper
// never reads aloud "asterisk Available asterisk".
func stripForSpeech(s string) string {
	for _, re := range speechStripRe {
		s = re.ReplaceAllString(s, "$1")
	}
	return s
}

// speechStripRe strips block and inline markdown in a deterministic order.
// Only paired delimiters between word characters are unwrapped so literals
// like "2*3" or "a * b" pass through untouched.
var speechStripRe = []*regexp.Regexp{
	// "# Title" → "Title"
	regexp.MustCompile(`(?m)^#{1,6}\s+(.*)$`),
	// "> quote" → "quote"
	regexp.MustCompile(`(?m)^>\s?(.*)$`),
	// "``code``" → "code"
	regexp.MustCompile("``([^`]+)``"),
	// "`code`" → "code"
	regexp.MustCompile("`([^`]+)`"),
	// "*bold*" → "bold"
	regexp.MustCompile(`\*([^*\n]+)\*`),
	// "_italic_" → "italic"
	regexp.MustCompile(`_([^_\n]+)_`),
	// "~strike~" → "strike"
	regexp.MustCompile(`~([^~\n]+)~`),
}
