# Installation

Clark is a homelab service. The recommended path is the interactive wizard — it asks what you want and does the rest. You do not need to hand-edit `.env`.

## Choose how you start

| Method | Who it's for | Command |
| --- | --- | --- |
| **Homebrew** | macOS/Linux with `brew` | `brew install heyimteee/tap/clark && clark install` |
| **One-liner** | Any machine without Go or `git clone` | `curl -fsSL https://raw.githubusercontent.com/heyimteee/clark/main/install.sh \| bash` |
| **From source** | Contributors / air-gapped | `git clone … && go build -o clark . && ./clark install` |

All three end in `clark install`. Homebrew and the one-liner fetch the latest `clark_*` tarball from GitHub Releases — no `git clone` and no Go toolchain required on the target.

Non-interactive (CI/SSH): add `--yes` and flags — see below.

## What the wizard asks

The wizard is `charmbracelet/huh` TUI, same binary as `clark`. It reads any existing `.env` to prefill, validates via `config.Load` before writing, writes `.env` `0600` with a `.env.bak` backup, and is re-runnable without data loss.

1. **iMessage bridge?** `y/N` — if yes, prompts for your iMessage handle (`+628…`) and a shared bridge token (generated with `crypto/rand` if empty; must match the Mac). Disable with `IMESSAGE_ENABLED=0`.
2. **Separate server?** `y/N` — if yes, prompts for SSH host (e.g. `3studio-server-tail`, `user@host`). When a host is given the wizard probes `ssh -o ConnectTimeout=5 host true` and later uses `scp`/`ssh` to copy `.env` and run `docker compose up -d --build` remotely. Reuses `~/.ssh/config` aliases and Tailscale hostnames.
3. **Run with Docker?** `Y/n` — `Y` (recommended) uses the baked image (`Systran/faster-whisper-small` + `en_US-ryan-high`). `n` sets `NO_DOCKER=1` and does a native `go build -o clark . && ./clark init` instead; Docker-specific prompts (NPM network, `TTS_REMOTE_URL`) are skipped.
4. **Ollama URL** — default `http://localhost:11434` (native) or `http://host.docker.internal:11434` (Docker). Validated as a URL.
5. **Ollama model** — required (as shown by `ollama list`, e.g. `llama3.2:latest`). The only hard requirement besides `WEB_TOKEN` when `WEB_ENABLED=1`.
6. **Persona** — `MASTER_NAME`, `PROTOCOL_NAME`, `PALACE_NAME`, `BYPASS_PHRASE` (default `get him to me`), `INNER_CIRCLE` (`Name|Relation;Name|Relation`) — all prompt-only, empty means generic defaults.
7. **Optional tools** — `TAVILY_API_KEY` (enables `web_search`), otherwise skipped.
8. **Voice / network** — `STT_ENGINE` (`faster-whisper`/`ollama`), `TTS_ENGINE` (`kokoro-remote`/`piper`), `KOKORO_VOICE` (`am_michael`), `NPM_NETWORK` (default `npm_default`). Secrets `WEB_TOKEN`/`ALERT_TOKEN`/`TTS_REMOTE_TOKEN` are generated with `crypto/rand` if empty and never printed after.

After the prompts the wizard does, in order: `write .env` → `docker network create ${NPM_NETWORK}` (best-effort) → `clark init` → `docker compose up -d --build` (or `go build` + `init` for `--no-docker`) → `docker ps --filter name=clark` and `curl -k https://clark.studio.lab/web/api/state` when enabled. On a remote host it `scp .env` + `docker-compose.yml`/`Dockerfile` then `ssh host 'cd ~/clark && docker compose up'`.

## Topologies — pick what you actually have

All combos start; missing engines degrade to `503`/fallback rather than crash (see `requirements.md`).

**WhatsApp-only, local Docker (simplest)**
```
clark install
# iMessage? N
# Separate server? N
# Docker? Y
# OLLAMA_URL http://host.docker.internal:11434  OLLAMA_MODEL llama3.2
# → .env WEB_TOKEN/ALERT_TOKEN generated, STT faster-whisper + TTS piper baked → usable voice without a Mac
```

**WhatsApp-only, local native (no Docker)**
```
clark install --no-docker
# Docker? n
# → go build, ./clark init, ./clark run  (set STT_ENGINE=ollama if you have no local whisper files)
```

**WhatsApp-only, remote server**
```
clark install --ssh 3studio-server
# Separate server? Y, SSH 3studio-server (probed)
# → .env scp'd to ~/clark/.env, remote docker compose up
```

**Full server + Mac (iMessage + Kokoro)**
```
clark install
# iMessage? Y (handle + token)
# Separate server? Y (or N if Mac is the server)
# Run on Mac after: ./scripts/install-bridge.sh https://clark.<domain> <token>
#                   ./scripts/install-kokoro-tts.sh <token>
```

## Non-interactive

For CI or a second run without a TTY:

```sh
clark install --yes --ollama-model llama3.2 --ssh 3studio-server-tail
clark install --yes --ollama-model llama3.2 --no-docker
curl -fsSL https://raw.githubusercontent.com/heyimteee/clark/main/install.sh | bash -s -- --yes --ollama-model llama3.2
```

`--yes` requires `OLLAMA_MODEL` (via flag, env, or existing `.env`) and generates any missing `WEB_TOKEN`/`ALERT_TOKEN`. Other flags: `--ssh`, `--env .env`, `--ollama-url`, `--no-docker`.

## After install

* Change one feature later without re-running everything: `clark config` (interactive checklist, `✓`/`✗` per group) or `clark config --edit core|persona|imessage|web|voice|live`. Store-backed keys (`status`, `context`, `think`, VIPs) apply live via `SIGHUP`; `.env` keys auto-restart `docker compose restart` when you are interactive.
* Verify: `./clark view`, `docker ps --filter name=clark`, `curl -k https://clark.studio.lab/web/api/state`.
* First run still prints a WhatsApp QR code — scan in WhatsApp > Settings > Linked Devices. On a fresh DB Clark starts silent until you text `wake up buddy` from your own chat or run `clark toggle`.

## Troubleshooting

* `WEB_TOKEN` complained inside Docker but you chose web disabled — fixed in `docker-compose.yml:29` (`:-`); the wizard now writes `WEB_ENABLED=1` only when a token exists.
* `network npm_default not found` — the wizard creates it (`docker network create`); create it manually if you ran `docker compose up` by hand.
* `no OLLAMA_MODEL set` — re-run `clark install --edit core` or `clark config --edit core`.
* Permissions — `.env` is `0600`; backups are `.env.bak`. Never commit `.env`.

Next: `requirements.md` for the hardware model, `transports.md` for the Mac bridge/voice daemons, `web-console.md` for the dashboard.
