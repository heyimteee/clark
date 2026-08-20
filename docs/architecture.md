# Architecture

## Pipeline

```
WhatsApp ─┐
iMessage ─┼─► gateway.Handler ─► assistant.Service (butler) ─► Ollama / tools ─► Messenger
Web chat ─┘         │                         │
                   └─────────────────────────► history / VIP / settings (store)
```

All transports implement a small `Messenger` interface; the butler is the shared brain. Adding a transport means implementing `Messenger`, not editing the pipeline.

## Principles

* Transport-neutral — every channel feeds the same handler and tool loop.
* Interface-driven — depend on `STT`/`TTS`/`LLM`/`Messenger` abstractions.
* Nil-safe — missing engines degrade to “unavailable” rather than crash.
* Daemon pattern — long-lived processes for STT/TTS (faster-whisper, Piper, Kokoro/MLX).
* FailoverTTS — primary → fallback gated on two consecutive failures.

## Project layout

```
main.go                       thin entry point
cmd/imessage-bridge           macOS bridge daemon
internal/app                  composition root + CLI (init/run/vip/ctx/toggle/think/history/view/access/help)
internal/alert                alert service (voice/silent, multi-channel delivery)
internal/config               single .env load + validation
internal/logging              structured colored logger (+ whatsmeow adapter)
internal/store                persistence interfaces + SQLite implementation
internal/ollama               Ollama /api/chat client (tools, optional thinking)
internal/tools                tool registry + argument helpers
internal/websearch            Tavily client
internal/assistant            butler service (settings, VIP rules, prompt, tool loop)
internal/gateway              transport-neutral pipeline (handler, echo tracker, prefixes)
internal/imessage             iMessage transport (HTTP API, outbound queue, messenger)
internal/whatsapp             WhatsApp transport (messenger, handler, echo tracker)
internal/voice                STT/TTS engines (faster-whisper, Kokoro remote, Piper, FailoverTTS)
internal/notify               desktop notifications
internal/web                  REST API, WebSocket chat/logs, SPA (voice endpoints)
docker/whisper_run.py         faster-whisper runner (framed protocol)
Dockerfile                    multi-stage build (builder + runtime)
docker-compose.yml            orchestration
.github/workflows/deploy.yml  auto-deploy on push to main
docs/                         this directory
```

Dependencies flow downward (`app → whatsapp, imessage, assistant, store, ollama, notify`). The store is SQLite (`CLARK_DB`); the file reference above matches `AGENTS.md:8`.
