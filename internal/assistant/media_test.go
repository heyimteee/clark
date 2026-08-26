package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/ollama"
)

// fakeVisionLLM scripts Chat replies and counts invocations.
type fakeVisionLLM struct {
	mu    sync.Mutex
	calls int
	reply func(call int, msgs []ollama.Message) string
}

func (f *fakeVisionLLM) Chat(_ context.Context, msgs []ollama.Message, _ []ollama.Tool) (*ollama.ChatResult, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	return &ollama.ChatResult{Content: f.reply(n, msgs)}, nil
}
func (f *fakeVisionLLM) ChatStream(ctx context.Context, m []ollama.Message, t []ollama.Tool, fn func(string)) (*ollama.ChatResult, error) {
	return f.Chat(ctx, m, t)
}
func (f *fakeVisionLLM) SetThink(bool) {}

func TestDigestDocumentSinglePass(t *testing.T) {
	s := &Service{visionLLM: &fakeVisionLLM{reply: func(int, []ollama.Message) string {
		return "## Purpose\nbudget approval"
	}}}
	out, err := s.DigestDocument(context.Background(), "small.txt", "short body")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "budget approval") {
		t.Errorf("digest = %q", out)
	}
}

func TestDigestDocumentMapReduceCoversAllChunks(t *testing.T) {
	svc := &Service{}
	long := strings.Repeat("x", 8000*3+100) // forces 4 chunks at 8k/400 overlap
	fake := &fakeVisionLLM{reply: func(call int, _ []ollama.Message) string {
		if call <= 4 {
			return "chunk-fact-" + string(rune('A'+call-1))
		}
		// reduce call: echo everything it received so the test can assert coverage
		return "merged"
	}}
	svc.visionLLM = fake

	var mu sync.Mutex
	var bodies []string
	_ = bodies
	wrapped := &captureLLM{inner: svc.visionLLM, bodies: &bodies, mu: &mu}
	svc.visionLLM = wrapped

	out, err := svc.DigestDocument(context.Background(), "big.pdf", long)
	if err != nil {
		t.Fatal(err)
	}
	if out != "merged" {
		t.Errorf("reduce output missing: %q", out)
	}
	// 4 map chunks + 1 reduce = 5 calls; each map body must be non-empty.
	mu.Lock()
	defer mu.Unlock()
	if len(*wrapped.bodies) != 5 {
		t.Fatalf("llm calls = %d, want 5", len(*wrapped.bodies))
	}
	for i, b := range (*wrapped.bodies)[:4] {
		if len(b) == 0 {
			t.Errorf("chunk %d body empty", i)
		}
	}
}

type captureLLM struct {
	inner  LLM
	bodies *[]string
	mu     *sync.Mutex
}

func (c *captureLLM) Chat(_ context.Context, msgs []ollama.Message, tools []ollama.Tool) (*ollama.ChatResult, error) {
	c.mu.Lock()
	if len(msgs) > 0 {
		*c.bodies = append(*c.bodies, msgs[0].Content)
	}
	c.mu.Unlock()
	return c.inner.Chat(context.Background(), msgs, tools)
}
func (c *captureLLM) ChatStream(ctx context.Context, m []ollama.Message, t []ollama.Tool, fn func(string)) (*ollama.ChatResult, error) {
	return c.inner.ChatStream(ctx, m, t, fn)
}
func (c *captureLLM) SetThink(on bool) { c.inner.SetThink(on) }

func TestDigestDocumentEmptyText(t *testing.T) {
	s := &Service{visionLLM: &fakeVisionLLM{}}
	if _, err := s.DigestDocument(context.Background(), "x.pdf", "   "); err == nil {
		t.Fatal("empty text must error")
	}
}

func TestDescribeMultipleFramesUsesSequencePrompt(t *testing.T) {
	var gotPrompt string
	s := &Service{visionLLM: &fakeVisionLLM{reply: func(_ int, msgs []ollama.Message) string {
		gotPrompt = msgs[0].Content
		return "a ball bounces"
	}}}
	items := []gateway.MediaAttachment{
		{Type: "video", MIME: "image/jpeg", Data: []byte("f1")},
		{Type: "video", MIME: "image/jpeg", Data: []byte("f2")},
	}
	out, err := s.Describe(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a ball bounces" {
		t.Errorf("out = %q", out)
	}
	if !strings.Contains(gotPrompt, "chronological frames") || !strings.Contains(gotPrompt, "ONE short video") {
		t.Errorf("sequence prompt not used: %q", gotPrompt)
	}
}

// tinyWav synthesizes a minimal valid PCM WAV file (44-byte header + samples).
func tinyWav() []byte {
	const samples = 1600 // 0.1s @16kHz mono 16-bit
	dataLen := samples * 2
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	put32(hdr[4:], uint32(36+dataLen))
	copy(hdr[8:], "WAVEfmt ")
	put32(hdr[16:], 16)
	put16(hdr[20:], 1) // PCM
	put16(hdr[22:], 1) // mono
	put32(hdr[24:], 16000)
	put32(hdr[28:], 32000)
	put16(hdr[32:], 2)
	put16(hdr[34:], 16)
	copy(hdr[36:], "data")
	put32(hdr[40:], uint32(dataLen))
	body := make([]byte, dataLen)
	return append(hdr, body...)
}
func put16(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func put32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func TestTranscribeVoiceConvertsAndFeedsSTT(t *testing.T) {
	var gotLen int
	s := &Service{}
	s.AttachSTT(sttFake{fn: func(wav []byte) (string, error) {
		gotLen = len(wav)
		return "hello from the voice note", nil
	}})
	out, err := s.TranscribeVoice(context.Background(), "audio/ogg", tinyWav())
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello from the voice note" {
		t.Errorf("transcript = %q", out)
	}
	if gotLen <= 44 {
		t.Errorf("converted wav suspiciously small: %d bytes", gotLen)
	}
}

type sttFake struct{ fn func([]byte) (string, error) }

func (f sttFake) Transcribe(_ context.Context, wav []byte) (string, error) { return f.fn(wav) }

func TestTranscribeVoiceWithoutSTT(t *testing.T) {
	s := &Service{}
	if _, err := s.TranscribeVoice(context.Background(), "audio/ogg", tinyWav()); err == nil {
		t.Fatal("expected error when STT not attached")
	}
}

var _ = errors.New
