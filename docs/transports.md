# Transports

Clark is transport-neutral. WhatsApp, iMessage, and the web chat all feed the same `gateway` → `assistant` pipeline (see `architecture.md`). Add a transport by implementing a small interface, not by editing the pipeline.

## WhatsApp

Primary transport via `whatsmeow`. Authenticated by scanning the QR code printed by `clark run` or `docker compose logs -f`. Session and history persist in `CLARK_DB`.

## iMessage bridge

A macOS daemon watches `~/Library/Messages/chat.db` and routes iMessage through the same butler pipeline over HTTPS.

```
iMessage -> [macOS bridge] --HTTPS--> NPM --> clark:8090
```

Bridge-initiated only: the Mac never opens an inbound port, and the bridge API is not published to a host port. Access control is identical to WhatsApp.

### Server side (once)

```sh
# .env on the server
IMESSAGE_ENABLED=1
IMESSAGE_BRIDGE_TOKEN=<long-random-string>
IMESSAGE_SELF_HANDLE=+6281234567890
NPM_NETWORK=npm_default
```

Redeploy:

```sh
git pull && docker compose up -d --build
```

Proxy `https://clark.<domain>` → `http://clark:8090` in Nginx Proxy Manager. Register VIPs with their iMessage handles (`vip -a "<handle>,<name>,<relation>"`); a single VIP entry covers both WhatsApp (`628...`) and iMessage (`+628...`) after canonicalization.

### Mac side (once)

1. Grant Full Disk Access to the terminal used for install (System Settings → Privacy & Security → Full Disk Access) so the bridge can read `chat.db`.
2. Install:

   ```sh
   ./scripts/install-bridge.sh https://clark.<domain> <IMESSAGE_BRIDGE_TOKEN>
   ```

   Builds `cmd/imessage-bridge`, installs launchd agent `com.clark.imessage-bridge`, starts on login.
3. The first outbound send triggers an Automation prompt for Messages.app — allow it.
4. Test: send yourself an iMessage and text `wake up buddy` from your own chat. Logs: `/usr/local/var/log/clark-bridge.log`.

Optional bridge env: `IMESSAGE_OWN_HANDLE`, `IMESSAGE_TLS_ROOTCA` (self-signed root CA), `IMESSAGE_POLL_INTERVAL` (default 1s).

### How it works

* Inbound: polls `chat.db` every second, filters self-sent/system/reaction/group messages, tracks a ROWID watermark in `~/Library/Application Support/clark-bridge/state.json`. A message is marked delivered only after the host accepts it.
* Outbound: polls the host queue, sends via AppleScript `send` on the iMessage service, then acks. Failed deliveries are not re-served.

## Voice

The console supports hands-free talk. STT and TTS are swappable interfaces; missing engines degrade to “unavailable” rather than crashing.

### Engines

* STT: `faster-whisper` (`Systran/faster-whisper-small`, ~461 MB, CPU int8) baked into the image; or `ollama` with a whisper model. The faster-whisper runner is a daemon (`docker/whisper_run.py`) framed over a pipe.
* TTS: primary Kokoro/MLX on the Mac (`mlx-audio`, 8-bit, voice `am_michael`), fallback Piper daemon (`en_US-ryan-high`, ~120 MB) on the server. `FailoverTTS` gates on two consecutive failures.

The faster-whisper and Kokoro remotes share a framed protocol `[u32 length][payload]` and auto-restart on failure.

### Kokoro remote (Mac, primary)

```sh
./scripts/install-kokoro-tts.sh <shared-token>
```

Installs a venv, `mlx-audio`, the 8-bit `Kokoro-82M` model, and launchd agent `com.clark.kokoro-tts` (`KeepAlive`). Logs: `/usr/local/var/log/kokoro-tts.log`.

Server `.env`:

```sh
TTS_ENGINE=kokoro-remote
TTS_REMOTE_URL=http://100.94.240.11:8790
TTS_REMOTE_TOKEN=<same shared token>
KOKORO_VOICE=am_michael
```

If the Mac is unreachable (lid closed on battery, Tailscale down), Clark falls back to Piper automatically. Set `TTS_ENGINE=piper` to force server-side only.

### Keeping the Mac awake

```sh
./scripts/install-caffeinate.sh
```

Runs `caffeinate -s -i` as a launchd agent. On AC it holds through lid close; on battery the Mac still sleeps and Piper takes over. For a battery lid-close override: `sudo pmset -a disablesleep 1`.

### Affirmations

Wakes play a pre-rendered clip (`00.wav` … `09.wav`, plus `processing.wav` and `idle.wav`) from `/opt/affirmations`. The build bakes Piper fallback clips; the Mac can sync Michael clips to `./affirmations` via `scripts/sync-affirmations.sh`.
