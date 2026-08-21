package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/heyimteee/clark/internal/alert"
	"github.com/heyimteee/clark/internal/voice"
)

func TestTokenBucketAllowsBurst(t *testing.T) {
	b := newTokenBucket(5, 5)
	for i := 0; i < 5; i++ {
		if !b.allow() {
			t.Fatalf("token %d denied within burst, want allowed", i+1)
		}
	}
	if b.allow() {
		t.Fatal("7th token allowed, want drained bucket")
	}
}

func TestTokenBucketRefills(t *testing.T) {
	b := newTokenBucket(60, 1) // 1 token/sec
	now := time.Now()
	b.now = func() time.Time { return now }

	if !b.allow() {
		t.Fatal("first allow failed")
	}
	if b.allow() {
		t.Fatal("second immediate allow succeeded, want empty")
	}
	now = now.Add(time.Second)
	if !b.allow() {
		t.Fatal("allow after 1s refill failed")
	}
}

func TestNotifyRateLimited(t *testing.T) {
	st := testStore(t)
	ast := newAssistant(t, st, &stubLLM{})
	srv := New(Options{
		ListenAddr: ":0",
		WebToken:   testWebToken,
		AlertToken: "alert-secret",
		Butler:     ast,
		Store:      st,
		Voice:      &voice.Engine{},
		Alerts:     alert.New(nil),
	})
	ts := newServerFor(t, srv)

	post := func() int {
		body, _ := json.Marshal(map[string]any{"kind": "reboot", "title": "t", "body": "b"})
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/web/api/notify", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Clark-Alert-Token", "alert-secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for i := 0; i < notifyBurst; i++ {
		if code := post(); code == http.StatusTooManyRequests {
			t.Fatalf("notify %d = %d, want accepted", i+1, code)
		}
	}
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("burst-exceeded notify = %d, want 429", code)
	}
}

func TestSTTConcurrencyCapped(t *testing.T) {
	st := testStore(t)
	llm := &stubLLM{}
	ast := newAssistant(t, st, llm)

	slow := &gateSTT{}
	srv := New(Options{
		ListenAddr: ":0",
		WebToken:   testWebToken,
		Butler:     ast,
		Store:      st,
		Voice:      &voice.Engine{STT: slow},
	})
	ts := newServerFor(t, srv)
	tok := login(t, ts)

	body := []byte(`{"audio":"` + base64.StdEncoding.EncodeToString([]byte("wav")) + `"}`)
	const total = 3
	release := make(chan struct{})
	slow.gate = release

	var wg sync.WaitGroup
	var mu sync.Mutex
	maxSeen := 0
	current := 0
	slow.onEnter = func() {
		mu.Lock()
		current++
		if current > maxSeen {
			maxSeen = current
		}
		mu.Unlock()
		<-release // block until test releases all
		mu.Lock()
		current--
		mu.Unlock()
	}

	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := postRaw(t, ts, "/web/api/stt", tok, body, "application/json")
			resp.Body.Close()
		}()
	}
	// Wait until the semaphore is saturated (two inside transcription, one
	// still queued), then let them finish.
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return current == sttMaxConcurrency && slow.entered >= sttMaxConcurrency
	})
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxSeen != sttMaxConcurrency {
		t.Fatalf("max concurrent transcriptions = %d, want %d", maxSeen, sttMaxConcurrency)
	}
}

// gateSTT is a controllable fake STT for concurrency tests.
type gateSTT struct {
	gate    chan struct{}
	onEnter func()
	mu      sync.Mutex
	entered int
}

func (g *gateSTT) Transcribe(_ context.Context, _ []byte) (string, error) {
	g.mu.Lock()
	g.entered++
	g.mu.Unlock()
	if g.onEnter != nil {
		g.onEnter()
	}
	return "ok", nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
