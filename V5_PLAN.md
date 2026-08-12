# Clark v5 — Full Voice Conversation (STT → LLM → TTS loop, Wake Word, Bark)

> **Status:** PLANNED — depends on v4 (this doc references v4 seams directly). Do NOT start until `v4.0.0` is shipped.
> **Owner:** @tristan

---

## 1. Goal

Turn the v4 voice chrome into a real **hands-free conversation**: say **"Clark"** and talk — clark hears, transcribes (Whisper via Ollama), answers with a full AI reply (all toolcalls, no fast-path), and speaks back (Piper now, Bark as a drop-in). The v4 bubble layout becomes the primary chat experience; the text chat stays as a fallback.

V5 is **additive**: it consumes the v4 seams (§11 of `V4_PLAN.md`) and only introduces (a) the conversation state machine, (b) streaming/daemonized TTS, (c) the Bark engine, and (d) voice-loop UX polish. No changes to the core brain beyond what v4 already added.

---

## 2. What v4 already gives v5 (the seams)

| v4 seam | v5 consumes it as |
|---|---|
| `internal/voice.STT` / `TTS` interfaces | Bark is a new `TTS` impl selected by config; `OllamaWhisper` is already the STT engine |
| `assistant.Service.ReplyLLM(jid="web", isSelf=true)` | the voice loop's brain call — full AI, all tools, separate `web` history |
| `POST /api/stt`, `POST /api/tts` | baseline contract; v5 adds `/api/tts/stream` but keeps these |
| Reserved `web` JID history | voice turns persist into the same separate conversation, visible in the History box |
| Voice UI chrome (mic/speaker toggles, bubble, wake-word status line, click-to-talk) | v5 only changes behaviour behind these controls |
| `logging.Subscribe()` | STT/TTS progress events stream into the logs strip for free |
| WAV contract (S16_LE mono, 22.05 kHz) | unchanged end-to-end |

---

## 3. Conversation state machine (the core of v5)

A single `VoiceLoop` lives in the **browser** (v5 keeps the loop client-side; the server stays stateless and just services STT/LLM/TTS). States:

```
 IDLE ──(say "Clark" | click mic)──▶ LISTENING ──(VAD silence ~1.2s)──▶ TRANSCRIBING
  ▲                                                                        │
  │                                                                    POST /api/stt
  │                                                                        ▼
  │                                                                LLM_REPLYING  (WS /api/chat, full AI)
  │                                                                        │
  │                                                                        ▼
 SPEAKING ◀────────(play returned WAV)────────────────────────────── TTS_SYNTHESIZING (POST /api/tts/stream)
  │                                                                        │
  │   (silence / timeout 30s / explicit "stop")                            │
  └─────────────────────────────────────────────────────────────────────────┘
```

Transitions & rules:

1. **IDLE → LISTENING**: wake word "Clark" detected (browser `SpeechRecognition`, continuous) or mic button pressed. UI shows pulsing mic.
2. **LISTENING → TRANSCRIBING**: browser VAD (analyser `getByteTimeDomainData` RMS threshold) or 1.2 s of silence stops the recorder; `MediaRecorder` → `encodeWAV` (16-bit PCM mono) → base64 → `POST /api/stt`. If transcript is empty/very short, return to IDLE (no reply).
3. **TRANSCRIBING → LLM_REPLYING**: send `{"type":"chat","text":transcript}` on the **persistent** `/api/chat` WS. Show the transcript as a user bubble immediately.
4. **LLM_REPLYING → TTS_SYNTHESIZING**: on `reply` frame, request speech synthesis (streaming where possible). Show a "…" indicator until audio is ready.
5. **TTS_SYNTHESIZING → SPEAKING**: play audio via WebAudio/`<audio>`; highlight the reply bubble.
6. **SPEAKING → IDLE**: after playback ends (or interrupted), re-arm wake-word listening. If the user speaks during playback (VAD above threshold while TTS is silent-padded), treat as **barge-in**: stop audio and restart at IDLE→LISTENING.

**Barge-in** (v5 nicety, low-cost): keep the mic analyser running during SPEAKING; if a loud segment is heard, `audio.pause()`, cancel any in-flight TTS fetch, and jump to LISTENING. Piper per-call latency (~100–300 ms) makes this feel near-instant.

**Errors**: any failed step logs into the logs strip and returns to IDLE with a short toast (e.g. "Didn't catch that"). `ollama.ErrRateLimited` surfaces the existing master alert message.

---

## 4. Wake word ("Clark") — v5 behaviour

- Armed continuously (except during SPEAKING, where it's replaced by barge-in logic).
- Implementation: `webkitSpeechRecognition`/`SpeechRecognition` with `continuous: true`, `interimResults: true`, `lang: "en-US"`. Match final + interim results against `/\bclark\b/i` (allow "hey clark", "clark?").
- **Fake-recognition anti-trigger**: require the match to occur after a minimum silence gap so echo of clark's own voice doesn't re-wake the loop; suppress detection entirely while the TTS audio element is `paused=false`.
- **Fallback when `SpeechRecognition` is unavailable** (Firefox, Safari-without-click): mic button remains the trigger; a banner notes "Wake word needs Chrome".
- Server-side option (stretch, not required): v5 keeps wake in the browser. A later v6 could stream audio to a local wake-word model on the server — the `/api/stt` endpoint already accepts arbitrary audio.

---

## 5. STT (Whisper via Ollama) — v5 hardening

- Keep `OllamaWhisper` from v4. v5 adds:
  - **Language pin**: transcribe with language hint `en` (English-only output requirement) to cut latency/ambiguity. Expose via `STT_LANG` (default `en`) in the generate request.
  - **Audio preprocessing**: downmix to mono + 16 kHz in the browser before base64 (halves upload size; Whisper accepts 16 kHz mono natively) — keep 22.05 kHz only for TTS side.
  - **Endpointing**: VAD (RMS/silence) for stop; hard cap at 30 s recordings.
  - **keep_alive**: request `keep_alive: "10m"` on `/api/generate` so the whisper model stays warm between turns (avoids cold-start on a 6-core CPU).
  - **Progress**: `logging.Log("VOICE", SevInfo, "STT", "Transcribed", "secs", …)` so turns show in the logs strip.

---

## 6. TTS — Piper polish + Bark drop-in

### 6.1 `internal/voice` engine registry

Refactor the v4 `voice.Engine` into a small registry driven by config:

```go
func Build(cfg config) (*Engine, error) // switch on cfg.TTSEngine:
//   "piper" -> NewPiper(...)   (v4, unchanged behaviour)
//   "bark"  -> NewBarkTTS(...)
// "whisper-turbo" STT via NewOllamaWhisper(...) as in v4
```

`TTS_ENGINE=bark` selects Bark; everything downstream (`/api/tts`, chat loop) is engine-agnostic.

### 6.2 Piper: daemonized, streaming

- **Long-lived piper process** (replaces v4 process-per-call): spawn `piper --model <voice> --output-raw --json-input` once at startup; feed lines `{"text":"…"}` and read raw PCM → chunk into a WAV stream. Loads the model once (~0.2 s vs per-call) and enables streaming.
- **`POST /api/tts/stream`**: `{"text":"…"}` → `200` with `Content-Type: audio/wav` and the bytes flushed sentence-by-sentence (split on sentence boundaries server-side so the browser starts playing before synthesis finishes). Guard concurrency with a mutex (single piper instance; queue turns).
- v4's blocking `POST /api/tts` stays for the "Test voice" button and non-streaming callers.

### 6.3 Bark (drop-in, optional engine)

- **`internal/voice/bark.go` — `NewBarkTTS(...)` implementing `TTS`** (same interface as Piper ⇒ zero web/assistant changes). Pinned for later; v5 ships it behind `TTS_ENGINE=bark` if the pieces land, otherwise a documented stub that returns "not available".
- Bark reality check (from prior evaluation): on the i5-9500T CPU a medium Bark pass is **30–120 s** — far from conversational. **Piper stays the default.** Bark is for *offline narration / longer passages* where naturalness > latency, or if a GPU is later added.
- If shipped: model cache under `/data/voice/bark`, warm-up at container start, WAV output via its ONNX graph; same `Synthesize(ctx, text) ([]byte, error)` contract.

### 6.4 Voices

- `GET /api/voice` extended: `{"sttModel":…,"sttLang":"en","ttsEngine":"piper","ttsVoice":"en_US-amy-medium","availableVoices":["en_US-amy-medium","en_US-lessac-medium","en_GB-alan-medium"]}` — the list comes from `/opt/piper/voices/*.onnx` on disk (no hardcoding).
- `POST /api/voice` `{"ttsVoice":"…"}` switches at runtime (validation against the on-disk list; persists via a settings row so it survives restart). TTS engine switch stays env-only.

---

## 7. API changes (delta vs v4)

| Endpoint | Change |
|---|---|
| `POST /api/tts/stream` | **New** — streaming WAV, sentence-flushed |
| `GET /api/voice` | Extended: `sttLang`, `availableVoices` |
| `POST /api/voice` | **New** — set `ttsVoice` at runtime (persisted) |
| `POST /api/stt` | Unchanged (now honours `STT_LANG`) |
| `WS /api/chat` | Unchanged — the voice loop is a normal chat client |

Config additions: `STT_LANG` (default `en`), `PIPER_STREAM` (default `true`). `TTS_ENGINE=bark` + `BARK_VOICE` (default a pinned Bark voice) for the optional path.

---

## 8. Frontend changes (delta vs v4)

- **Voice loop module** (`voice.js`): implements §3 state machine; owns `SpeechRecognition`, VAD analyser, recorder, `encodeWAV`, audio playback, barge-in.
- **Bubble layout becomes the default chat layout** in Chat mode; text input still available (toggle).
- Bubble card UI: state dot + label (Idle / Listening… / Transcribing… / Thinking… / Speaking), pulsing mic, transcript preview, reply text, stop button.
- Wake-word toggle in the Voice bento tile now actually arms the loop; "Test voice" still hits the blocking `/api/tts`.
- Logs strip unchanged (voice events stream through it automatically).
- Streaming playback: feed `/api/tts/stream` bytes into `MediaSource`/`AudioContext` so speech starts before the full WAV arrives.

---

## 9. Testing & verification

- **Unit**: `voice` engine registry switch (`piper`/`bark` → right impl); `BarkTTS` contract compliance; streaming WAV chunking (bytes form a valid WAV); runtime voice switch persists; `STT_LANG` lands in the request.
- **Integration (server)**: `curl /api/tts/stream` returns playable audio for a long paragraph (streams before completion); `POST /api/voice` switches voice and `GET` reflects it; piper daemon survives many turns without leaking.
- **Manual voice loop** (the DoD): say "Clark" → answer back → clark replies and speaks. Test: barge-in stops playback and re-listens; silence/no-speech returns to Idle; long sentence streams while speaking; "give me a web search" triggers a **toolcall** (proving full-AI, not fast-path); English-only output.
- **Gates**: `gofmt -l .` empty, `go vet ./...`, `go test -race ./...`.

---

## 10. Release steps

1. Branch → implement §4–§8 → gates green.
2. Merge to `main` → auto-deploy via self-hosted runner (unchanged workflow).
3. Manual pass §9 on `clark.studio.lab` (mic + wake word need the mkcert CA trusted in the browser — already the case on the Master's Mac).
4. Tag `v5.0.0`, update README + `.env.example` (STT_LANG, TTS_ENGINE=bark docs).

---

## 11. Risks & fallbacks (v5)

| Risk | Likelihood | Mitigation / fallback |
|---|---|---|
| Wake word unreliable in browser (Chrome-only, echo re-trigger) | Medium | Anti-echo gating + silence-gap check (§4); click-to-talk always available; server-side wake is a clean later path |
| Streaming piper daemon instability over long sessions | Medium | Mutex + turn queue; restart daemon on failed read; fall back to blocking `/api/tts` |
| Bark too slow on CPU (30–120 s) | High (known) | Piper stays default; Bark opt-in + clearly labelled as "narration mode"; re-evaluate if GPU is added |
| Browser mic permission friction | Low | First-use prompts; clear in-UI guidance; still fully usable via text chat |

## 12. Definition of done (v5.0.0)

- [ ] Hands-free loop: wake "Clark" → STT → full-AI reply → streaming TTS → speak; repeats.
- [ ] Barge-in works; silence returns to Idle; errors toast + log.
- [ ] Toolcalls fire from voice turns (a web-search voice request returns a sourced answer).
- [ ] `TTS_ENGINE=piper` default; `=bark` at least compiles/ships (or documented stub); runtime voice switch via UI.
- [ ] All gates green; deployed; tagged `v5.0.0`.
