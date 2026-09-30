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

That shared identity carries the on/off toggle and the tool grants, but **history is kept per channel**: WhatsApp is stored under `628…@s.whatsapp.net` and iMessage under `imessage:628…@s.whatsapp.net`. So when one person messages Clark on both apps, their two conversations stay separate and Clark can tell which app a remembered message came from. `view_history` defaults to the current channel and accepts a `transport` argument; `view_all_history` labels every line with its channel. WhatsApp keys are unchanged, so no migration was needed.

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

* Inbound: watches `chat.db` with macOS `kqueue` events (via `fsnotify`) on the database, its `-wal`/`-shm` sidecars, and the parent directory, with a 250 ms debounce and a 5 s poll as a backstop for dropped or coalesced events. It filters self-sent/system/reaction/group messages and tracks a ROWID watermark in `~/Library/Application Support/clark-bridge/state.json`. A message is marked delivered only after the host accepts it. Watches are re-armed when a SQLite checkpoint rotates the sidecars; if the event watcher cannot start, the bridge falls back to polling alone.
* Backlog is kept, never dropped. A message the bridge could not see arrive — the Mac was asleep, the bridge was down — is delivered with a `replay` flag and is stored as history **without** being answered, so it can inform the next live reply instead of being deleted. The same applies to any message sent before clark's status was last switched ON. A stale `get him to me` in the backlog does **not** trigger the alert cascade.
* If `chat.db` is unreadable (Full Disk Access revoked) the bridge keeps retrying with backoff and starts the watcher the moment access is restored — no manual restart. Outbound delivery is independent of this, since sending is gated on Automation permission rather than Full Disk Access.
* Outbound: polls the host queue, sends via AppleScript `send` on the iMessage service, then **verifies the outgoing row actually appeared in `chat.db`** before acking. AppleScript exiting 0 does not mean delivery — on macOS 26 Messages can report success while writing an empty unjoined row instead of sending.
* Delivery outcomes are classified, because only some are safe to retry:

| Outcome | Cause | Action |
|---|---|---|
| `delivered` | outgoing row observed | ack |
| `not_started` | the send script refused | retry with backoff |
| `ghost` | empty unjoined row (macOS 26) | retry with backoff |
| `unknown` | script succeeded, no row appeared | **never retried** — the message may already be on the device, so a retry could duplicate it |

* Failures back off exponentially (30s → 30m) up to 5 attempts, then land in a `dead` state with the reason recorded. `GET /outbound/dead` lists them, so an undeliverable message is visible rather than silently lost. A row stranded in `picked` by a crashed bridge becomes claimable again once its 2-minute lease expires.

## Calendar

Clark talks to the calendar **directly over CalDAV from the server**, so it keeps
working when the Mac is asleep, the bridge is down, or the laptop is on battery.

Free: an Apple ID plus an app-specific password. No Apple developer account, no
per-request cost.

```sh
CALDAV_URL=https://caldav.icloud.com
CALDAV_USER=you@icloud.com
CALDAV_APP_PASSWORD=xxxx-xxxx-xxxx-xxxx   # appleid.apple.com → App-specific passwords
CALDAV_CALENDAR_HREF=                     # optional; blank = first discovered
```

The Mac holds **no** calendar permission and proxies nothing.

Notes:

- Calendars are discovered with `PROPFIND` and cached; iCloud has no primary-calendar
  alias, and the collection URL is region-redirected.
- Recurring events are expanded into individual occurrences with exact times, so
  a weekly standup still answers "what's tomorrow?". `EXDATE` and `RDATE` are honoured.
- Each expanded occurrence carries an `occurrenceId` of `<uid>:<timestamp>`.
  Deleting that id cancels **one** date via `EXDATE`; deleting the bare uid removes
  the whole series.
- Reads are chunked under iCloud's one-year-per-request cap.
- `GET /web/api/calendars` lists the collections so the console can offer a picker
  when an account has more than one. A supplied href is validated against the
  discovered set before it is used as a write target.
- A rejected password is reported as an **urgent** tool-health failure, since it is
  rare and actionable.

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
