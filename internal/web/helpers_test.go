package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/heyimteee/clark/internal/assistant"
	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/voice"
)

const testWebToken = "test-web-token"

// stubLLM implements assistant.LLM so the web tests drive a real Service.
type stubLLM struct {
	err      error
	got      []ollama.Message
	gotTools []ollama.Tool
}

func (s *stubLLM) SetThink(bool) {}

func (s *stubLLM) Chat(_ context.Context, messages []ollama.Message, tools []ollama.Tool) (*ollama.ChatResult, error) {
	s.got = messages
	s.gotTools = tools
	if s.err != nil {
		return nil, s.err
	}
	return &ollama.ChatResult{Content: "Indubitably."}, nil
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.InitDefaults(); err != nil {
		t.Fatalf("InitDefaults: %v", err)
	}
	if err := st.Set("context", "web testing context"); err != nil {
		t.Fatalf("set context: %v", err)
	}
	if err := st.Set("status", "true"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	return st
}

func newAssistant(t *testing.T, st *store.Store, llm assistant.LLM) *assistant.Service {
	t.Helper()
	ast, err := assistant.New(&config.Config{DBPath: ":memory:", OllamaModel: "test-model"}, st, llm)
	if err != nil {
		t.Fatalf("assistant.New: %v", err)
	}
	return ast
}

func voiceEngine() *voice.Engine {
	return &voice.Engine{STT: &fakeSTT{text: "hello world"}, TTS: &fakeTTS{wav: []byte{1, 2, 3}}}
}

func newTestServer(t *testing.T) (*httptest.Server, *assistant.Service, *stubLLM, *store.Store) {
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
		TTSEngine:  "kokoro-remote",
		Voice:      &voice.Engine{},
	})
	ts := newServerFor(t, srv)
	return ts, ast, llm, st
}

func newServerFor(t *testing.T, srv *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// fakeSTT / fakeTTS implement the voice interfaces for endpoint tests.
type fakeSTT struct {
	text string
	err  error
}

func (f *fakeSTT) Transcribe(_ context.Context, _ []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.text, nil
}

type fakeTTS struct {
	wav []byte
	err error
}

func (f *fakeTTS) Synthesize(_ context.Context, _ string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.wav, nil
}

func (f *fakeTTS) Voice() string { return "am_michael" }

func bearer(r *http.Request, token string) *http.Request {
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func postJSON(t *testing.T, ts *httptest.Server, path, token string, body any) (int, map[string]any) {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(buf))
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
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func postRaw(t *testing.T, ts *httptest.Server, path, token string, body []byte, ct string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", ct)
	if token != "" {
		req = bearer(req, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func getJSON(t *testing.T, ts *httptest.Server, path, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req = bearer(req, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func login(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	code, out := postJSON(t, ts, "/web/api/login", "", map[string]any{"key": testWebToken})
	if code != http.StatusOK {
		t.Fatalf("login = %d, want 200 (body %v)", code, out)
	}
	tok, _ := out["token"].(string)
	if tok == "" {
		t.Fatalf("login returned no token: %v", out)
	}
	return tok
}

func wsDial(t *testing.T, ts *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + path
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("ws dial: %v (status %d)", err, resp.StatusCode)
		}
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { c.Close(websocket.StatusNormalClosure, "done") })
	return c
}

func wsWriteJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal ws frame: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

func jsonDecode(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}

func wsReadJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("ws decode %q: %v", data, err)
	}
	return out
}
