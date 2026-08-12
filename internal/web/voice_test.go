package web

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/voice"
)

func postJSONBody(t *testing.T, ts *httptest.Server, path, token string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req = bearer(req, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func testVoiceServer(t *testing.T, eng *voice.Engine) (*httptest.Server, string) {
	t.Helper()
	st := testStore(t)
	llm := &stubLLM{}
	ast := newAssistant(t, st, llm)

	srv := New(Options{
		ListenAddr: ":0",
		WebToken:   testWebToken,
		Butler:     ast,
		Store:      st,
		STTModel:   "whisper-turbo",
		TTSEngine:  "piper",
		Voice:      eng,
	})
	ts := newServerFor(t, srv)
	return ts, login(t, ts)
}

func TestVoiceStatusUnavailableWhenNil(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := getJSON(t, ts, "/web/api/voice", tok)
	if code != 200 {
		t.Fatalf("voice status = %d, want 200", code)
	}
	if out["available"] != false {
		t.Errorf("voice available = %v, want false", out["available"])
	}
	if out["sttModel"] != "whisper-turbo" || out["ttsEngine"] != "piper" {
		t.Errorf("voice engines = %v", out)
	}
}

func TestVoiceStatusAvailable(t *testing.T) {
	ts, tok := testVoiceServer(t, &voice.Engine{STT: &fakeSTT{text: "x"}, TTS: &fakeTTS{wav: []byte{1}}})

	code, out := getJSON(t, ts, "/web/api/voice", tok)
	if code != 200 {
		t.Fatalf("voice status = %d", code)
	}
	if out["available"] != true {
		t.Errorf("available = %v, want true", out["available"])
	}
	if out["sttModel"] != "whisper-turbo" || out["ttsEngine"] != "piper" || out["ttsVoice"] != "en_US-lessac-medium" {
		t.Errorf("voice status = %v", out)
	}
}

func TestTTSEndpoint(t *testing.T) {
	ts, tok := testVoiceServer(t, &voice.Engine{STT: &fakeSTT{}, TTS: &fakeTTS{wav: []byte{1, 2, 3, 4}}})

	code, out := postJSON(t, ts, "/web/api/tts", tok, map[string]any{"text": "hello there"})
	if code != 200 {
		t.Fatalf("tts = %d, want 200: %v", code, out)
	}
	if out["format"] != "audio/wav" {
		t.Errorf("tts format = %v, want audio/wav", out["format"])
	}
	audio, _ := out["audio"].(string)
	decoded, err := base64.StdEncoding.DecodeString(audio)
	if err != nil {
		t.Fatalf("tts audio not valid base64: %v", err)
	}
	if len(decoded) != 4 {
		t.Errorf("tts audio = %d bytes, want 4", len(decoded))
	}
}

func TestTTSUnavailable(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	code, out := postJSON(t, ts, "/web/api/tts", tok, map[string]any{"text": "hello"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("tts without engine = %d, want 503: %v", code, out)
	}
}

func TestTTSTooLong(t *testing.T) {
	ts, tok := testVoiceServer(t, &voice.Engine{TTS: &fakeTTS{wav: []byte{1}}})
	long := strings.Repeat("x", maxTTSTextChars+1)

	code, out := postJSON(t, ts, "/web/api/tts", tok, map[string]any{"text": long})
	if code != http.StatusBadRequest {
		t.Fatalf("tts too long = %d, want 400: %v", code, out)
	}
}

func TestTTSSpeechRequestReturnsWAV(t *testing.T) {
	ts, tok := testVoiceServer(t, &voice.Engine{STT: &fakeSTT{}, TTS: &fakeTTS{wav: []byte{1, 2, 3, 4}}})

	resp := postRaw(t, ts, "/web/api/speech", tok, []byte(`{"text":"hello"}`), "application/json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("speech = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("speech content-type = %q, want audio/wav", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read speech body: %v", err)
	}
	if len(body) != 4 {
		t.Errorf("speech body = %d bytes, want 4", len(body))
	}
}

func TestSTTEndpoint(t *testing.T) {
	ts, tok := testVoiceServer(t, &voice.Engine{STT: &fakeSTT{text: "hello world"}, TTS: &fakeTTS{wav: []byte{1}}})

	body := `{"audio":"` + base64.StdEncoding.EncodeToString([]byte("RIFFnotreally")) + `"}`
	resp := postJSONBody(t, ts, "/web/api/stt", tok, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stt = %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode stt response: %v", err)
	}
	if out["text"] != "hello world" {
		t.Errorf("stt text = %v, want 'hello world'", out["text"])
	}
}

func TestSTTBadBase64(t *testing.T) {
	ts, tok := testVoiceServer(t, &voice.Engine{STT: &fakeSTT{text: "x"}, TTS: &fakeTTS{wav: []byte{1}}})

	resp := postJSONBody(t, ts, "/web/api/stt", tok, `{"audio":"not!!base64!!"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("stt bad base64 = %d, want 400", resp.StatusCode)
	}
}

func TestSTTUnavailable(t *testing.T) {
	ts, _, _, _ := newTestServer(t)
	tok := login(t, ts)

	resp := postJSONBody(t, ts, "/web/api/stt", tok, `{"audio":"RIFF"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stt without engine = %d, want 503", resp.StatusCode)
	}
}

func TestSTTTooLarge(t *testing.T) {
	ts, tok := testVoiceServer(t, &voice.Engine{STT: &fakeSTT{text: "x"}, TTS: &fakeTTS{wav: []byte{1}}})

	// A 25 MB base64 payload (plus JSON wrapping) trips the MaxBytesReader.
	payload := strings.Repeat("A", maxAudioBytes+1024)
	resp := postJSONBody(t, ts, "/web/api/stt", tok, `{"audio":"`+payload+`"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("stt oversized = %d, want 413", resp.StatusCode)
	}
}

func TestVoiceEndpointsRequireAuth(t *testing.T) {
	ts, _, _, _ := newTestServer(t)

	if code, _ := getJSON(t, ts, "/web/api/voice", ""); code != http.StatusUnauthorized {
		t.Errorf("voice status no auth = %d, want 401", code)
	}
	resp := postJSONBody(t, ts, "/web/api/tts", "", `{"text":"x"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("tts no auth = %d, want 401", resp.StatusCode)
	}
	resp = postJSONBody(t, ts, "/web/api/stt", "", `{"audio":"RIFF"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("stt no auth = %d, want 401", resp.StatusCode)
	}
}
