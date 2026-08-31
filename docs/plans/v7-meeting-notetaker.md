# v7.0.0 — Meeting Notetaker

> Feasibility: **FREE for MVP** (local STT + local LLM). Only real-time per-speaker diarization is paid/heavy.

## Objective

Records **the full meeting audio**, transcribes with the resident `faster-whisper` daemon, then AI-processes into structured notes.

## Behavior (approved)

Browser Start → continuous capture until Stop → `media.ToWav16k` → batched `STT Transcribe` (respecting `sttSlots` cap `server.go:76`) → concat transcript → new `assistant.SummarizeMeeting` (tool-less `llm.Chat` with a notetaker prompt) → SQLite `meeting_notes` table.

## Capture — browser

Reuses existing seams: `getUserMedia {audio:true}` `app.js:1170`, `MediaRecorder` `1482`, `AudioContext` `631`, `blobToWav` `1562` → `bufferToBase64` `1609`. For meetings the current VAD (`analyser` `1491-1508` `rms>0.02`, silence `1500ms`) is **disabled** — `Start meeting` records continuously, `Stop` finalizes, chunking at `mediaRecorder.start(30000)` → `ondataavailable` every 30 s. Remote attendee audio (Google Meet in the same tab) optionally captured via `getDisplayMedia {audio:true, video:true}` mixin through `AudioContext.createMediaStreamDestination()` — **Chromium-only**. No `getDisplayMedia` currently in repo (grep = 0), adding it is ~15 lines beside `setupAnalyser` `1208`.

## Upload / STT

Same base64 JSON shape `{"audio": b64}` as `handleSTT` (`voice.go:103-155`) with `MaxBytesReader 25MB` `112` (the 1 MB `decodeBody` override at `113-115`). Meeting chunks POST sequentially; server concatenates transcripts. Each chunk respects `sttReadTimeout 90s` (`fasterwhisper.go:14`), `maxSTTBytes 20MB` (`18`) → ~5 min @16 kHz mono, so chunking keeps us well under cap.

## Summarization

Clone of `assistant/summarize.go:29-48` `SummarizeAlert` + `assistant/assistant.go:280-334` `DigestDocument` chunk/merge: new `meetingPrompt`

```text
You are a meeting notetaker. Create:
## Summary
## Decisions
## Action Items (owner if mentioned)
## Open Questions
## Transcript Highlights
Bullets only. Keep names verbatim.
```

Tool-less `llm.Chat` so no hallucinated tool calls. Map-reduce (`chunkSize 8000, overlap 400, total 8m, per-chunk 120s`) handles hour-long transcripts the same way `DigestDocument` already does.

## Storage

New table via `store.go:89-171` WAL migration:

```sql
CREATE TABLE IF NOT EXISTS meeting_notes(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT,
  transcript TEXT,
  digest TEXT,
  jid TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
-- index on created_at
```

Pattern mirrors `chat_history` `155-160` and `migrate()` `173-177`. Per `docs/requirements.md:9` WAL+busy_timeout already enforced (`store.go:89-94`).

## Endpoints & UI

| Route | Verb | Handler | Notes |
|---|---|---|---|
| `/web/api/meeting/transcribe` | `POST` | `handleMeetingTranscribe` | reuses `handleSTT` body; guarded by `sttSlots` |
| `/web/api/meeting/summarize` | `POST` | `{transcript, title} → assistant.SummarizeMeeting → store.CreateMeetingNote → hub.broadcast` | like `broadcastChatAlert` `hub.go:91` |
| `/web/api/meetings` | `GET` | list with `?limit=` | like `handleHistory` `rest.go:98-138` |

SPA `app.js:187-272` bento shell: new `tile-meeting` card beside `tile-voice`, binding like `bindBento` `348-435`, `refreshHistory` `576-612`. Registers under `server.go:146-148` `HandleFunc` triad (`requireAuth`, `decodeBody` audit `server.go:198-211`).

## Free vs paid

| Layer | Free MVP | Paid/heavy only |
|---|---|---|
| Mic capture | `getUserMedia` — all browsers | — |
| System/tab audio | `getDisplayMedia` mixin — Chromium | recall.ai bot $0.15–0.30/hr |
| Wav, upload | `blobToWav` + ffmpeg `ToWav16k` | — |
| Transcribe | `faster-whisper small int8` (`Dockerfile:47` baked) | Deepgram/AssemblyAI |
| Summarize | `gemma4:e4b` local (`ollama.go:66`) | OpenAI API |
| Store/push | SQLite WAL + `hub.broadcast` | — |
| **Diarization** (“who said what”) | LLM guess + manual names (80% useful) | `pyannote.audio` (heavy, HF token, EULA) / Deepgram diarization ($) |

## Testing & rollout

Fixture audio → fake STT asserts wav≥44 bytes (`assistant/media_test.go` helpers `tinyWav`); fake LLM scripted `Chat` returns chunk-indexed digests verifying map-reduce coverage; `gofmt/vet/test-race/node --check` + `docker compose build`. Manual: Start meeting → 2 min talk → Stop → `meeting_notes` row visible via new tile + web chat `GET /web/api/meetings`.

## Files

New: `internal/store/meeting.go`, `internal/assistant/meeting.go`, `internal/web/meeting.go`, SPA tile + CSS
Modified: `internal/config/config.go` (+`MEETING_LANG=en` optional), `server.go:146-148`, `app.js`, `whisper_run.py:61` make `language` configurable if needed.
