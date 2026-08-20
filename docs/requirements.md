# Requirements

Clark is a **homelab service** — a long-running personal assistant built to be self-hosted on private infrastructure. It is not a hosted SaaS or a single-binary desktop app. To operate at full capability, the system is distributed across a private server and, optionally, a Mac.

This document describes what is required, what is optional, and what the author uses in production. Resource figures are measured, not estimated.

## Service model

A homelab service is installed once and runs continuously. Clark monitors WhatsApp, optionally iMessage, and the web console. It persists settings, session, and conversation history in a local SQLite database. There is no external account, telemetry, or cloud dependency beyond the optional web-search provider.

## Minimum requirements

To answer WhatsApp messages, Clark requires:

| Component | Requirement |
| --- | --- |
| Runtime | Go 1.25.5 to build from source, or Docker with `docker compose` |
| Language model | A reachable Ollama instance with a pulled model (`ollama pull llama3.2` or newer) |
| Messaging account | A WhatsApp account that can link a device via QR code |
| Persistence | A filesystem location for the SQLite database (`CLARK_DB`, default `mystore.db`) |
| Network | Outbound HTTPS for model calls (local) and inbound reachability for the optional web console |

No other external service is required at minimum.

## Recommended homelab setup

The author's deployment pairs a Mac and a private Debian server on the same Tailscale network. This is the reference configuration under which all features are verified.

**Mac — `tristans-macbook-pro`**

* Apple M4, 24 GB unified memory, macOS 15+
* Runs: Ollama with the primary model, the iMessage bridge daemon (`cmd/imessage-bridge`), and the remote Kokoro TTS server (`mlx-audio`, 8-bit, asset `am_michael`)
* Tailscale IP `100.94.240.11:8790` (Kokoro) and `:8791` (bridge action endpoint)
* Power: `caffeinate -s -i` launchd agent (`scripts/install-caffeinate.sh`) keeps daemons alive with the lid closed on AC; on battery the Mac sleeps and Clark falls back to server-side TTS

**Server — `3studio-server`**

* Intel i5-9500T, 16 GB RAM (measured 15 GB usable, planned upgrade to 31 GB), 1 TB HDD in provisioning, Debian 13
* Runs: the Clark container (`golang:1.25-alpine` build, `debian:bookworm-slim` runtime), Netdata `:19999`, and Uptime Kuma `:3001`
* Reverse proxy: Nginx Proxy Manager on `npm_default`, terminating TLS for `https://clark.studio.lab` and forwarding to `clark:8090` inside the Docker network
* Auto-deploy: a self-hosted GitHub Actions runner pulls `main` and runs `docker compose up -d --build`

Anyone self-hosting should treat these figures as a **tested reference**, not a hard floor. Clark will run on smaller machines if optional components are disabled.

## Feature dependencies

| Feature | Requires | When disabled |
| --- | --- | --- |
| WhatsApp | `whatsmeow` session authenticated via QR code | No messaging |
| iMessage | macOS 13+ with Full Disk Access for `~/Library/Messages/chat.db`, a shared `IMESSAGE_BRIDGE_TOKEN`, and the Docker `IMESSAGE_ENABLED=1` bridge route | WhatsApp-only deployment |
| Web console | `WEB_ENABLED=1` and a 32-byte hex `WEB_TOKEN` (`openssl rand -hex 32`); reverse proxy for public HTTPS | CLI-only management |
| Voice (STT) | `STT_ENGINE=faster-whisper` (baked-in `Systran/faster-whisper-small`, ~461 MB, CPU int8) or `STT_ENGINE=ollama` with an Ollama whisper model | Text-only responses |
| Voice (TTS) | Primary: Mac Kokoro/MLX server (`TTS_ENGINE=kokoro-remote`); fallback: server-side Piper daemon (`en_US-ryan-high`, ~120 MB) baked into the image | Text-only; console shows `ttsEngine` unavailable |
| Alerts | `ALERT_TOKEN` for `POST /web/api/notify`, optionally `MAC_ACTION_URL` for FaceTime/banner on silent mode | No monitoring alerts |
| Web search | `TAVILY_API_KEY` (free 1,000 searches/month at tavily.com) | `web_search` tool unavailable to VIPs and the Master |

Models are baked at `docker build` time (`Systran/faster-whisper-small`, `en_US-ryan-high`, optional affirmations) so the container never downloads at runtime.

## Resource profile

Measured on the reference server running a single Clark container with the baked models:

* Idle container: ~400 MB RSS
* Faster-whisper small (CPU int8): ~700 MB peak during transcription, ~1–2× real-time on the i5-9500T
* Piper fallback: ~150 MB, ~0.6× real-time
* Kokoro/MLX on the Mac (M4, 8-bit): ~5× faster than onnxruntime/CoreML, `am_michael` default voice
* SQLite database: a few megabytes for settings/VIP/history; history retention is controlled by `clark history <N>` (default 10 turns per chat, see `docs/cli.md`)
* Ollama model storage is external and dominates disk: 4–8 GB per model, held on the Mac in the reference setup

## Networking

* Tailscale (or another private WireGuard mesh) between Mac and server is required for the Kokoro/MLX remote TTS and the iMessage bridge to stay reachable without opening inbound ports on the Mac.
* Public HTTPS for the web console is terminated at Nginx Proxy Manager; the bridge API is never published to a host port. It is bridge-initiated over HTTPS.

## Expectations for new operators

Clark assumes operator familiarity with: cloning a Go project, managing a `.env` file, running `docker compose`, scanning a WhatsApp linked-device QR code, and, if iMessage is desired, granting Full Disk Access on macOS. There is no graphical installer. The documentation in this `docs/` directory is organized so that a reader can provision a minimal WhatsApp-only instance first, then add transports and voice incrementally.
