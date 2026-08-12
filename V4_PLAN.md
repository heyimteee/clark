# Clark v4 — Bento Web Console + Real-Time Chat + Logs + Voice Seam

> **Status:** PLANNED — not yet implemented. Execution scheduled for a later session.
> **Scope:** v4.0.0 (single-page web console) **plus** the explicit architectural seams v5.0.0 (full voice conversation) will plug into.
> **Owner:** @tristan

---

## 1. Goal

Ship a single-page **Japanese-bento management dashboard** at `https://clark.studio.lab` that lets the Master (authenticated by a `.env` key) manage every facet of clark from the browser instead of WhatsApp:

1. **One page, two full-screen modes** toggled by a button:
   - **Bento mode** — every setting is a visible, interactive container (Config, Voice, VIPs, Access, History). No hidden settings.
   - **Chat mode** — full-screen conversation with clark. **Every message gets a full AI reply with all toolcalls** (no hardcoded fast-path replies; the fast-path mutations live on the bento page as controls).
2. **Real-time logs** streamed to the page over WebSocket.
3. **Voice seam** (prepared, working): STT (Whisper via the existing Ollama) + TTS (Piper baked into the image) behind swappable interfaces, plus the UI chrome (wake-word "Clark" hook, mic/speaker bubble) so v5 is mostly wiring.
4. **Login with the `.env` key** (`WEB_TOKEN`). No OAuth.

The bento page is a third transport that reuses the existing transport-neutral pipeline (`gateway`/`assistant.Service`) — identical in spirit to the iMessage transport (`internal/imessage/adapter.go`).

---

## 2. Confirmed decisions (from the Master)

| Decision | Choice |
|---|---|
| Chat vs bento | Full-screen toggle between two modes of the **same** page |
| UI language | English |
| Web-chat history | **Separate** namespace (not merged into per-VIP WhatsApp history) |
| Voice engines | STT = Whisper via existing Ollama; TTS = Piper **baked into the image**; Bark later as drop-in (v5) |
| Auth | `WEB_TOKEN` from `.env`; server **refuses to start** without it when `WEB_ENABLED=1` |
| Voice output language | English only |
| Frontend | Vanilla embedded SPA via `go:embed` — no build step, no framework |
| Design | Frontend Agent Skills: `minimalist-ui` + `impeccable`, then `layout`/`typeset`/`polish` |

---

## 3. Environment facts (verified, hard prerequisites)

- `clark.studio.lab` already proxies in NPM → `clark:8090`, HTTPS (mkcert wildcard `*.studio.lab`), `allow_websocket_upgrade=1` — **no NPM change needed**.
- Domain only resolves on internal DNS (AdGuard) → Tailscale IP `100.93.103.83`; clark container has **zero host ports**.
- **Server port 443 is publicly open** (router → NPM). Anyone who guesses the hostname can reach `clark:8090`. ⇒ `WEB_TOKEN` is **mandatory**; do not rely on DNS obscurity alone.
- `github.com/coder/websocket v1.8.15` already in `go.mod` (promote to direct require).
- Server: Intel i5-9500T, 15 GB RAM, **no GPU**. Ollama 0.32.6 serving `gemma4:cloud`, `gemma4:e2b`; whisper family available. Piper runs on CPU fine.
- `assistant.New(cfg, st, llm LLM)` takes an **interface** (`LLM { Chat(...) }`) → easy to inject fakes in tests.

---

## 4. Architecture

```
Browser (clark.studio.lab)
   │  HTTPS (NPM, mkcert) → clark:8090
   ▼
internal/web.Server  (new)
   ├── /web/api/login        → WEB_TOKEN check → session token
   ├── /web/api/state        → full bento snapshot
   ├── /web/api/*            → REST mutations (status/thinking/context/vip/access/history)
   ├── /web/api/stt, /tts    → internal/voice
   ├── /web/api/chat  (WS)   → assistant.Service.ReplyLLM(jid="web", isSelf=true)
   ├── /web/api/logs  (WS)   ← internal/logging sink (plain-line fan-out)
   └── /web/…                → go:embed SPA
   └── (if iMessage enabled) root.Handle("/", bridge) → /inbound | /outbound | /ack

internal/voice (new)
   ├── STT  interface → OllamaWhisper  (POST {OllamaURL}/api/generate, audio=base64)
   └── TTS  interface → PiperTTS       (exec piper, WAV out)
                     (future: BarkTTS in v5 — same interface)

internal/assistant.Service  (1 surgical change)
   └── Reply()/ReplyLLM() → shared private reply(..., allowFastPath bool)
                            Web chat saves to jid "web" → separate history automatically.
```

`internal/app.Run` builds **one** `http.Server` on `:8090`:
- When `WEB_ENABLED=1`: `root.Handle("/web/", webMux)` + (if iMessage enabled) `root.Handle("/", imessage.Routes())`. Go 1.22+ `ServeMux` longest-pattern-wins keeps `/web/*` and the bridge's method-patterns (`POST /inbound`, `GET /outbound`, `POST /ack`) disjoint — **zero iMessage changes required**.
- When web disabled: current behaviour (iMessage `Run` owns the listener) unchanged.

---

## 5. Backend changes, file by file

### 5.1 `internal/assistant/assistant.go` — add `ReplyLLM`

The only change to the core brain. Today `Reply` calls `s.fastPath(...)` at line ~469.

1. Rename current `Reply` body to a private `reply(ctx, senderJID, userMsg string, isSelf, allowFastPath bool)`.
2. `Reply(...)` → `s.reply(..., allowFastPath: true)` (unchanged behaviour; all existing tests stay green).
3. Add:
   ```go
   // ReplyLLM runs the full model pipeline and skips the deterministic
   // fast path, so the web console's chat always gets a genuine AI reply
   // with every tool available. Used by the Master's web session only.
   func (s *Service) ReplyLLM(ctx context.Context, senderJID, userMsg string, isSelf bool) (string, error) {
       return s.reply(ctx, senderJID, userMsg, isSelf, false)
   }
   ```
4. In `reply`, gate the fast-path block with `if allowFastPath { ... }`.

Why it works for the web:
- `isSelf=true` passes the `!isSelf && !isVIP` gate (assistant.go:424).
- `toolsForSender(_, isSelf=true)` returns **all tools** (assistant.go:1012) → every toolcall available.
- History saved under `senderJID` — the web session uses the reserved `webJID = "web"`, so `SaveMessage("web", …)` and `RecentMessages("web", limit)` give a **separate** conversation with zero schema changes (30-message cap per jid already exists).

Tests (mirror `internal/assistant/assistant_test.go`, which uses `fakeLLM` implementing `LLM`):
- A fast-path-able phrase (e.g. "show context" / "turn off") must **reach the fakeLLM** via `ReplyLLM`, but be answered hardcoded via `Reply`.
- Web session grants all tools: assert `toolsForSender("web", true)` length equals `s.tools.List()` length.
- Separate history: after `ReplyLLM(ctx, "web", …)`, `Messages("web")` returns the turns and `Messages("<vip jid>")` is untouched.

### 5.2 `internal/config/config.go` — new env vars

Add to `Config` and `Load()`:

| Field | Env | Default | Notes |
|---|---|---|---|
| `WebEnabled bool` | `WEB_ENABLED` | `false` | serve the console on `:8090` |
| `WebToken string` | `WEB_TOKEN` | — | **required** if `WEB_ENABLED=1` else `Load` returns an error |
| `STTModel string` | `STT_MODEL` | `whisper-turbo` | Ollama model name for transcription |
| `TTSEngine string` | `TTS_ENGINE` | `piper` | `piper` now, `bark` later |
| `TTSVoice string` | `TTS_VOICE` | `en_US-amy-medium` | Piper voice id |

### 5.3 `internal/logging/log.go` — subscriber sink (real-time logs)

Add a plain-line fan-out so the web WS can stream logs without ANSI codes.

```go
var sinkMu sync.RWMutex
var sinks []chan string
const sinkBuf = 512 // drop-oldest on overflow, never block the logger

// Subscribe registers a consumer of plain (uncolored) log lines and returns
// the channel and an unsubscribe func. Buffered, non-blocking.
func Subscribe() (<-chan string, func())
```

In `colorHandler.Handle`, after the plain `line` is built (line ~140, before coloring), call `notifySinks(line)`. The existing JSON-format path (`CLARK_LOG_FORMAT=json`) gets the same hook at the `log/slog` level (simplest: call `notifySinks` from `stdLogger`'s handler wrapper — implement in `web/logs.go` by re-emitting, or add the call in both handlers). Keep it minimal: emit plain lines from the color handler only; acceptable for v4.

### 5.4 `internal/voice/voice.go` — interfaces (the v5 seam)

```go
package voice

// STT transcribes audio into text.
type STT interface {
    Transcribe(ctx context.Context, audioWAV []byte) (string, error)
}

// TTS synthesizes speech from text.
type TTS interface {
    // Synthesize returns 16-bit PCM mono WAV bytes (Piper medium = 22.05 kHz).
    Synthesize(ctx context.Context, text string) ([]byte, error)
    // Voice returns the active voice id (for the UI).
    Voice() string
}

// Engine selects a named STT/TTS implementation.
type Engine struct {
    STT  STT
    TTS  TTS
}
```

### 5.5 `internal/voice/whisper.go` — `OllamaWhisper` (STT)

- `NewOllamaWhisper(baseURL, model string) *OllamaWhisper` — reuse `strings.TrimRight(baseURL,"/")` like `ollama.New`.
- `Transcribe` POSTs to `{baseURL}/api/generate`:
  ```json
  { "model": "<STT_MODEL>", "prompt": "<|startoftranscript|>", "audio": "<base64 wav>", "stream": false, "keep_alive": "10m" }
  ```
  Response field `"response"` is the transcript. (Field name for the audio blob is the **one item to verify during implementation** against the installed Ollama version; the `/api/generate` shape is confirmed, only the audio key name needs a live check. Fallback: pass audio via the documented `audio` key or `ollama run whisper-turbo` prompt convention.)
- Guard: enforce max audio size (e.g. 25 MB), 60s HTTP timeout, `context` cancellation.
- Note: `assistant.New` takes an `LLM` interface; the voice package talks to Ollama directly (its own thin client) so STT does not touch the chat pipeline.

### 5.6 `internal/voice/piper.go` — `PiperTTS` (TTS)

- `NewPiper(binPath, voicePath string) *PiperTTS`. Fields wired from config + build args.
- `Synthesize`: `exec.CommandContext(ctx, binPath, "--model", voicePath, "--output-raw")`, stdin = text, stdout = raw PCM; wrap raw PCM in a WAV header (44-byte RIFF, S16_LE mono, 22050 Hz — piper medium voices). Return WAV bytes.
- Keep piper's process-per-call for v4 (voice model is ~60 MB, loads in ~0.1–0.3 s on the i5); **v5 plan** will add a long-lived daemon for streaming. Note this latency trade-off explicitly.
- Preflight at startup: if the binary/voice is missing, log WARN and degrade to `TTS = nil` (web UI shows "TTS unavailable" instead of a hard crash).

### 5.7 `internal/web/` — new package

Files:
- `server.go` — `Server` struct, `Run(ctx, Options)`, mux composition, session store, auth middleware.
- `rest.go` — REST handlers (state, mutations, history).
- `chat.go` — `/web/api/chat` WebSocket.
- `logs.go` — `/web/api/logs` WebSocket + `logging.Subscribe`.
- `voice.go` — `/web/api/stt`, `/web/api/tts`, `/web/api/voice` (engines + voice list).
- `static.go` — `//go:embed static/*` + mux for the SPA (serve `index.html` for `/web/`).
- `server_test.go`, `rest_test.go`, `chat_test.go`.

`Options`:
```go
type Options struct {
    ListenAddr  string            // ":8090"
    WebToken    string            // required
    Butler      *assistant.Service
    Store       *store.Store
    Voice       *voice.Engine     // may be nil (STT/TTS degrade gracefully)
    Bridge      http.Handler      // imessage.Routes(); optional
    STTModel    string            // for /web/api/voice display
}
```

`Run`:
```go
webMux := http.NewServeMux()
webMux.HandleFunc("/api/login", s.handleLogin)
// … each /api/… route; auth middleware wraps all except login
root := http.NewServeMux()
root.Handle("/web/", s.auth(webMux))
if opts.Bridge != nil { root.Handle("/", opts.Bridge) }
srv := &http.Server{Addr: opts.ListenAddr, Handler: root, ReadHeaderTimeout: 10*time.Second}
// graceful shutdown on ctx.Done(); mirror imessage.Run's pattern
```

---

## 6. REST & WS API specification

All routes under `/web/`. Auth: `Authorization: Bearer <session>` except `/api/login`. JSON in/out.

### 6.1 Session auth

- `POST /web/api/login` — body `{"key":"<WEB_TOKEN>"}`.
  - OK → `200 {"token":"<session>","expires_in":43200}`.
  - Bad key → `401 {"error":"invalid key"}`.
- Session: 32 random bytes → hex, stored in an in-memory `map[string]session{created time.Time}` with a **12 h** sliding TTL (refresh on use). Single process, so no persistence needed. Sessions are cleared on process restart.
- WS auth: first frame `{"type":"auth","token":"<session>"}`; server replies `{"type":"auth","ok":true}` before accepting chat/log frames. (Avoids tokens in URLs/logs.)

### 6.2 State

- `GET /web/api/state` →
  ```json
  {
    "name": "Clark",
    "model": "gemma4:cloud",
    "enabled": true,
    "thinking": true,
    "historyLimit": 10,
    "context": "Available",
    "sttModel": "whisper-turbo",
    "ttsEngine": "piper",
    "ttsVoice": "en_US-amy-medium",
    "vips": [
      { "jid": "+6281267858909@s.whatsapp.net", "name": "Tiara", "relation": "Girlfriend",
        "enabled": true, "access": ["web_search", "view_history"] }
    ],
    "tools": [ { "name": "web_search", "description": "…" } ]
  }
  ```
  Built from: `a.ast.Name()`, `Model()`, `Enabled()`, `Thinking()`, `HistoryLimit()`, `Context()`, `VIPList()` + `EnabledFor(jid)` + `AccessFor(jid)`, `Tools().List()`.

### 6.3 Mutations (each `POST`, returns `200 {"ok":true}` or `400/500 {"error":…}`)

| Endpoint | Body | Service call |
|---|---|---|
| `/api/status` | `{"enabled":bool}` | `SetStatus` |
| `/api/thinking` | `{"enabled":bool}` | `SetThinking` |
| `/api/history-limit` | `{"limit":int}` | `SetHistoryLimit` |
| `/api/context` | `{"context":string}` | `SetContext` |
| `/api/vip/add` | `{"input":"628…,Tiara,Girlfriend"}` | `AddVIP` |
| `/api/vip/add-bulk` | `{"entries":["…","…"]}` | `parseBulkVIP` (loop `AddVIP`, all-or-nothing — reuse the v3.1 bulk semantics) |
| `/api/vip/delete` | `{"jid":"…"}` | `DeleteVIP` |
| `/api/vip/status` | `{"jid":"…","enabled":bool}` | `SetVIPStatus(jid, …)` |
| `/api/access` | `{"jid":"…","tool":"web_search","enabled":bool}` | `AccessFor` + add/remove + `SetAccess` (mirror `app.Access` CLI logic) |

Every mutation returns a fresh `{"state":<GET /api/state snapshot>}` so the SPA re-renders from one response (simple, no optimistic UI complexity in v4).

### 6.4 History

- `GET /api/history?scope=global|vip|web&jid=<jid>&limit=10`
  - `global` → `Store.AllRecentMessages(limit)`
  - `vip` → `Store.RecentMessages(jid, limit)`
  - `web` → `Store.RecentMessages("web", limit)` (the separate web conversation)
  - Response: `{"entries":[{"jid":"…","role":"user","content":"…"}, …]}` newest-first (or chronological — pick one, display matters).
- Limit default 10; the SPA requests a larger limit for "Show all".

### 6.5 Voice

- `GET /api/voice` → `{"sttModel":"whisper-turbo","ttsEngine":"piper","ttsVoice":"en_US-amy-medium","available":true}`.
- `POST /api/stt` body `{"audio":"<base64 wav>"}` → `{"text":"…"}` (max 25 MB body).
- `POST /api/tts` body `{"text":"…"}` → `{"audio":"<base64 wav>","format":"audio/wav"}` (guard text length, e.g. ≤ 4000 chars).

### 6.6 WebSocket `/api/chat` (full AI, no fast-path)

JSON frames, one logical turn per client message (per-connection serial ⇒ ordering is natural; no dispatcher needed).

Client → Server:
```json
{"type":"auth","token":"…"}
{"type":"chat","text":"…"}
{"type":"ping"}
```
Server → Client:
```json
{"type":"auth","ok":true}
{"type":"ack"}                                   // sent immediately, like the Master "one moment"
{"type":"reply","text":"…"}                      // final full AI reply (all toolcalls run server-side)
{"type":"error","message":"…"}                   // model error / apology path
{"type":"pong"}
```

Handler logic per `chat` frame:
1. `ack` to the client.
2. `text, err := svc.ReplyLLM(ctx, "web", text, true)`.
3. On `ollama.ErrRateLimited` (or wrapped): send `{"type":"error","message":rateLimitMasterMessage}` (clark already auto-disables itself inside `handleModelError`).
4. On other errors: send the apology message.
5. Else `reply` frame.

### 6.7 WebSocket `/api/logs`

- Auth: same first-frame pattern.
- Server pushes `{"type":"log","line":"15:04:05.000 INFO  OLLAMA  REQUEST: …"}` for every line from `logging.Subscribe()`.
- Client may send `{"type":"pause"}` / `{"type":"resume"}` (not required in v4; buffer last 200 lines server-side and replay on connect so the page isn't empty at load).

---

## 7. Frontend — embedded SPA (`internal/web/static/`)

- Files: `index.html`, `app.css`, `app.js`. Embedded via `//go:embed static`.
- Load skills in order: **`minimalist-ui`** (warm monochrome palette, flat bento grid, typographic contrast — matches "white, simple, minimalist"), **`impeccable`** (component/polish quality), then `typeset`/`layout`/`polish` as needed. English UI text throughout.
- No framework, no build step. Vanilla JS modules + `fetch` + `WebSocket`.

### 7.1 App shell
- Login card first (one input + button → `POST /api/login`, store session in `sessionStorage`).
- Header bar: page title, **mode toggle** (Bento ⇄ Chat), session/logout, live indicator (WS connected?).
- Real-time **logs strip**: a slim, collapsible console at the bottom (or top) fed by `/api/logs`, auto-scrolling, pause-on-hover, with the ability to pin it open.

### 7.2 Bento mode — grid layout
White background, generous whitespace, a **CSS grid** of boxes (bento = mixed spans, flat, thin dividers, muted borders). Boxes:

1. **Config** (1 tile): Global Status toggle (on/off), Thinking toggle, History limit (number input), Context (textarea + Save). All live, all visible — nothing hidden.
2. **Voice (STT & TTS)** (1 tile): engine/voice display, "Test voice" button (`POST /tts` + play), mic + speaker toggles, wake-word status line ("Say 'Clark'" — v5 active hook; in v4 the mic toggle arms the browser listener and logs readiness).
3. **VIPs** (large tile): table — name, relation, per-VIP **status toggle**, access chips, delete. "Add VIP" inline form + "Bulk add" textarea (one entry per line) + Add-All button.
4. **Access** (1–2 tiles): VIP picker + per-tool checkboxes (from `tools` list) → `POST /api/access`.
5. **History** (full-width, bottom, tall, spans the remaining vertical space): segmented control **Global / Per-VIP / Web**; VIP picker shown when Per-VIP; newest **10** by default; **"Show all"** expands to the full conversation; refresh button.

### 7.3 Chat mode — full-screen
- Back to bento toggle in the header.
- Scrollable message list (user bubbles + clark replies rendered as markdown-light text).
- Input bar + Send.
- **Layout toggle button** ⇄ **voice bubble layout** (per requirement): the bubble is a floating audio card showing wake-word state, live transcript, reply, and a speaker/play button. In v4 the mic button is present but **wake word "Clark" is the intended trigger** (v5 turns it on fully; v4 leaves a click-to-talk fallback).

---

## 8. Voice flow (v4 wiring, v5 machinery)

v4 end-to-end manual path (proves the seam):
1. Bubble mode → record 3–10 s via `MediaRecorder` → WebAudio `encodeWAV` (16-bit PCM mono 22.05/44.1 kHz) → base64.
2. `POST /api/stt` → transcript.
3. Transcript is injected into the chat input → `WS /api/chat` → full AI reply.
4. `POST /api/tts` with the reply → base64 WAV → `<audio>` playback.

Wake word in v4: browser `SpeechRecognition` (continuous, interim) is **armed** when the mic toggle is on; when it hears "Clark" the SPA logs "wake detected" and, if `navigator.mediaDevices` is available, records → runs the flow above. v5 hardens this loop (see `V5_PLAN.md`).

---

## 9. Docker / build changes

### 9.1 `Dockerfile`
Runtime stage (`alpine:3.21`) additions:
- `RUN apk add --no-cache espeak-ng` (piper phonemizer data).
- Copy a **static piper binary** into `/opt/piper/piper` (download `piper_linux_x86_64.tar.gz` from the piper releases at build; it is static, glibc-independent — works on alpine).
- Download a voice model into the image at `/opt/piper/voices/` (`en_US-amy-medium.onnx` + `.onnx.json` from the piper voices HF repo). Build-time download is acceptable (CI has network).
- `ENV PIPER_BIN=/opt/piper/piper PIPER_VOICE=/opt/piper/voices/en_US-amy-medium.onnx`
- Fallback (documented, not default): if build-time download proves flaky, add a runtime first-run download in `docker-entrypoint.sh` when the voice file is absent.

> **Decision point for the implementer:** if the static-binary route fails (missing `libgomp`/espeak data at runtime), **fallback option** — switch the runtime stage to `debian:bookworm-slim` and `pip install piper-tts`, calling `piper` from PATH. Both produce a WAV; `PiperTTS` only cares about the binary path. Verify which works in CI before writing the tag.

### 9.2 `docker-compose.yml`
Add to `clark.environment`:
```yaml
- WEB_ENABLED=${WEB_ENABLED:-1}
- WEB_TOKEN=${WEB_TOKEN:?set WEB_TOKEN in your .env}   # required when web enabled
- STT_MODEL=${STT_MODEL:-whisper-turbo}
- TTS_ENGINE=${TTS_ENGINE:-piper}
- TTS_VOICE=${TTS_VOICE:-en_US-amy-medium}
```
Keep `:8090` unpublished to the host (NPM-only ingress).

### 9.3 `.env.example`
Document `WEB_ENABLED`, `WEB_TOKEN` (generate via `openssl rand -hex 32`), `STT_MODEL`, `TTS_ENGINE`, `TTS_VOICE`.

### 9.4 `go.mod`
Move `github.com/coder/websocket` from indirect to direct (`go mod tidy` after first import).

---

## 10. `internal/app/app.go` wiring

In `Run` (after the iMessage block), replace the transport goroutine logic:

```go
if a.cfg.WebEnabled {
    if a.cfg.WebToken == "" {
        return fmt.Errorf("WEB_ENABLED requires WEB_TOKEN in .env")
    }
    var bridge http.Handler
    if a.cfg.IMessageEnabled {
        msgr := imessage.NewMessenger(a.st, a.cfg.IMessageSelfHandle)
        h := gateway.NewHandler("IMESSAGE", msgr, a.ast, a.notifier(), a.cfg.BypassPhrase)
        imessage.RegisterSendMessageTool(a.ast.Tools(), msgr, a.ast.LookupIMessage) // (export from tool.go)
        bridge = imessage.NewServer(a.cfg.IMessageBridgeToken, a.cfg.IMessageSelfHandle, a.st, h).Routes()
    }
    engine := buildVoiceEngine(a.cfg) // voice.NewOllamaWhisper + voice.NewPiper, both nil-safe
    errCh <- web.Run(ctx, web.Options{
        ListenAddr: a.cfg.IMessageListenAddr, WebToken: a.cfg.WebToken,
        Butler: a.ast, Store: a.st, Voice: engine, Bridge: bridge,
    })
} else if a.cfg.IMessageEnabled {
    // current iMessage Run path, unchanged
}
```

Notes:
- `imessage/tool.go` `registerSendMessageTool` needs export (→ `RegisterSendMessageTool`) since app now wires it directly; the iMessage `Run` path keeps using it internally.
- When both web + iMessage are on, iMessage's own `Run` is **not** started; the bridge routes are mounted inside the web server's root mux.
- `a.st` doubles as the web `HistoryStore` and outbound queue.

---

## 11. v5 preparations baked into v4 (the seams)

Every item below is deliberately in v4 so v5 is additive, not a rewrite:

1. **`internal/voice` interfaces** (`STT`, `TTS`) + engine config — Bark becomes `voice.NewBarkTTS(...)` selected by `TTS_ENGINE=bark`. No web or assistant changes.
2. **`assistant.ReplyLLM`** — the web chat entry point v5's voice loop reuses verbatim.
3. **Reserved `web` JID history** — v5's voice turns land in the same separate conversation.
4. **Voice UI chrome** — mic/speaker toggles, wake-word status line, bubble layout, click-to-talk fallback. v5 only changes behaviour behind them.
5. **`logging.Subscribe()`** — v5 can surface STT/TTS progress lines in the logs strip for free.
6. **`/api/stt` + `/api/tts` endpoints** — stable contract; v5 adds `/api/tts/stream` but the base endpoints stay.
7. **WAV contract** — S16_LE mono (Piper 22.05 kHz) end-to-end; Browser-side `encodeWAV` is reusable for v5.
8. **Graceful degradation** — voice `nil`-safe everywhere; a missing piper/whisper never takes down the console.

---

## 12. Testing & verification plan

### 12.1 Gates (must all pass before release)
```bash
gofmt -l .            # expect empty
go vet ./...
go test -race ./...
```

### 12.2 New unit tests
- `assistant`: `ReplyLLM` bypasses fast-path; `Reply` still hits it; web session gets all tools; `web` history isolation.
- `logging`: `Subscribe` receives plain lines; unsub works; logger never blocks on a slow/full sink.
- `voice`: `OllamaWhisper` request shape (URL, body fields, base64); `PiperTTS` WAV-header correctness (RIFF size, sample rate, mono) against a stubbed `exec` (or a canned WAV).
- `web`: login (good/bad key, session expiry), auth middleware (401 without bearer), every REST mutation maps to the right `Service` call (assert via injected fake `*assistant.Service`? — `Service` is concrete; instead use a real Service on `:memory:` DB with `fakeLLM`, the pattern `assistant_test.go` already uses), chat WS (auth frame → chat frame → reply frame, and that fast-path phrases still produce an LLM call), logs WS (replays last N lines, streams new ones).

### 12.3 Manual / integration (server, after deploy)
1. `curl -k https://clark.studio.lab/web/api/login -d '{"key":"<WEB_TOKEN>"}'` → session.
2. Load the page: bento renders all boxes; every toggle mutates state and re-renders; History Global/Per-VIP/Web scopes show the right rows (Web shows only console turns).
3. Chat mode: send "give me a summary of your context" → full AI reply (a fast-path phrase that would have been hardcoded must instead invoke the model + tools).
4. Logs strip streams live lines while chatting.
5. Voice: test TTS plays audio; mic → STT transcribes; the 4-step flow in §8 works; wake word "Clark" arms the recorder.
6. iMessage bridge still works through the merged mux (send a bridge POST, ack returns).
7. Confirm `clark` still has zero host ports; `WEB_TOKEN` missing → container fails fast with a clear log.

---

## 13. Release & deploy steps

1. Implement per §5–§10. Commit on a branch; pass §12.1 gates.
2. Merge to `main` → self-hosted runner `clark-server` runs `.github/workflows/deploy.yml` → `git pull --ff-only` + `docker compose up -d --build` on the server.
3. Verify container: `docker logs clark` shows the web server started + bridge mounted; §12.3 manual pass.
4. Tag `v4.0.0` and push tags (matches the v3.1.0 flow).
5. Add `WEB_TOKEN` to the server `.env` (via SSH) **before** enabling `WEB_ENABLED` in compose, so the container never restarts without it.
6. Update README (web console section, env table, screenshot).

---

## 14. Risks & fallbacks

| Risk | Likelihood | Mitigation / fallback |
|---|---|---|
| Piper static binary fails on alpine runtime | Medium | Debian-slim + `pip install piper-tts` fallback; both behind the same `PiperTTS` interface. Feature-flag by failing soft (`TTS=nil` → UI shows "unavailable"). |
| Ollama whisper audio field differs | Medium | Verify live during implementation; adjust request key; the endpoint contract (`POST /api/stt` → text) is unaffected. |
| Browser wake word flaky / Chrome-only | Medium | v4 keeps click-to-talk fallback; v5 moves wake logic server-side if needed (always-listening via `SpeechRecognition` is Chrome-only; Safari needs a click first). |
| `SpeechRecognition` needs HTTPS | Low | Already HTTPS (NPM/mkcert). |
| Logs WS volume overloads logger | Low | Buffered non-blocking sink, drop-oldest; cap WS broadcast rate. |
| Same-port mux collision | Low | Go 1.22 longest-pattern-wins; `/web/` vs method-pattern bridge paths are disjoint. Unit-tested. |
| Memory: web chat with huge context | Low | Existing 30-message cap + historyLimit already bound the pipeline. |

## 15. Definition of done (v4.0.0)

- [ ] `gofmt -l .` empty, `go vet`, `go test -race` all green.
- [ ] One page at `https://clark.studio.lab`: bento + chat modes toggle; all §7.2 boxes live.
- [ ] Chat always full-AI (verified with a fast-path phrase). Web history separate under `web`.
- [ ] Real-time logs on the page.
- [ ] STT + TTS working through `/api/stt` + `/api/tts`; wake-word hook armed.
- [ ] Bridge endpoints intact; zero host ports; `WEB_TOKEN` enforced.
- [ ] Deployed to server, tagged `v4.0.0`.
- [ ] README + `.env.example` updated.
- [ ] All §11 v5 seams present and documented.
