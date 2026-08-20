# Web console

A single-page dashboard on `:8090` — vanilla JS embedded via `go:embed`, no build step. Two modes, one listener, real-time state push.

## Modes

* **Bento** — live tiles: Config (status, thinking, history limit, context), Voice (engines, test, mic/wake), VIPs (add/bulk/status/delete), Access (per-VIP tool toggles), History (Global / Per-VIP / Web).
* **Chat** — full-screen conversation with Clark over WebSocket. Each message gets a genuine AI reply with the full tool loop; the `web` conversation is kept separate from per-VIP history.
* **Logs** — collapsible real-time stream from the structured logger.

Voice seam: wake word `Clark` arms the browser listener; click-to-talk falls back to manual recording.

## Setup

```sh
WEB_ENABLED=1
WEB_TOKEN=<openssl rand -hex 32>   # required when WEB_ENABLED=1, else the server refuses to start
ALERT_TOKEN=<openssl rand -hex 32>
ALERT_MODE=voice                    # voice | silent (also toggled from the console)

STT_ENGINE=faster-whisper           # or ollama
STT_MODEL=whisper-turbo             # when STT_ENGINE=ollama
TTS_ENGINE=kokoro-remote            # or piper
KOKORO_VOICE=am_michael
TTS_VOICE=en_US-ryan-high
```

Affirmations are served from `/opt/affirmations` (host volume) seeded from the baked Piper fallback. TTS/STT degrade gracefully when an engine or model is missing.

## API surface

All endpoints except `POST /web/api/login` and `POST /web/api/notify` require `Authorization: Bearer <session>`. Login returns a 12-hour sliding session.

* `POST /web/api/login` with `{"key":"<WEB_TOKEN>"}`
* `GET /web/api/state` — bento snapshot; every mutation (`status`, `thinking`, `history-limit`, `context`, `vip/*`, `access`, `history/clear`, `send`) returns a fresh snapshot. Mutations also broadcast `{type:"state", state:…}` over the chat WebSocket.
* `GET /web/api/history?scope=global|vip|web&jid=&limit=` — chronological turns.
* `POST /web/api/stt` — base64 WAV to text; `POST /web/api/tts` and `POST /web/api/speech` — base64 / raw WAV; `GET /web/api/voice` — engine status.
* `POST /web/api/alert-mode` with `{"mode":"voice"|"silent"}`
* `POST /web/api/notify` — monitoring webhooks (`X-Clark-Alert-Token: <ALERT_TOKEN>`, body `{"kind","title","body"}`); see `alerts-and-monitoring.md`
* `GET /web/api/chat` and `GET /web/api/logs` — WebSockets (auth as first frame)

The iMessage bridge API (`/inbound`, `/outbound`, `/ack`) is mounted inside the same listener. The bridge also listens on `IMESSAGE_ACTION_LISTEN` (`:8791`) for `POST /action` — `{"type":"facetime","number":…}` and `{"type":"banner","title","body"}` used by silent-mode alerts.
