# Getting started

This is the minimal path from a fresh checkout to a running assistant. For the full hardware and service model, see `requirements.md`.

## Prerequisites

* Go 1.25.5 to build from source, or Docker with `docker compose`
* A reachable Ollama instance with a model pulled (`ollama pull llama3.2`)
* A WhatsApp account that can link a device

Optional prerequisites are covered in their respective docs: the iMessage bridge and Kokoro voice require a Mac (see `transports.md`), voice and alerts require tokens (see `requirements.md`).

## 1. Clone and build

```sh
git clone https://github.com/heyimteee/clark.git
cd clark
go mod tidy
go build -o clark .
```

The repository also ships a multi-stage `Dockerfile` (`golang:1.25-alpine` build, `debian:bookworm-slim` runtime). The builder needs a C compiler for SQLite (`mattn/go-sqlite3`); the runtime bakes in `Systran/faster-whisper-small` and the Piper `en_US-ryan-high` voice so it never downloads at runtime.

## 2. Configure

Copy the example and fill in the required fields:

```sh
cp .env.example .env
```

Required in `.env`:

```sh
OLLAMA_URL=http://localhost:11434        # or http://host.docker.internal:11434 inside Docker
OLLAMA_MODEL=llama3.2:latest             # as shown by `ollama list` on the Ollama host
```

Common optional variables:

```sh
CLARK_DB=mystore.db
TAVILY_API_KEY=tvly-...                  # enables web_search (see requirements.md)
WEB_ENABLED=1
WEB_TOKEN=<openssl rand -hex 32>         # required when WEB_ENABLED=1
ALERT_TOKEN=<openssl rand -hex 32>
MASTER_NAME=the Master
PROTOCOL_NAME=Butler
PALACE_NAME=Palace
BYPASS_PHRASE=get him to me
INNER_CIRCLE=Name|Relation;Name|Relation
```

Persona variables (`MASTER_NAME`, `PROTOCOL_NAME`, `PALACE_NAME`, `BYPASS_PHRASE`, `INNER_CIRCLE`) only shape the prompt. Omit them for generic defaults. See `.env.example` for the full list including bridge and voice remotes.

## 3. Initialize

```sh
./clark init       # creates the SQLite database and default settings
./clark view       # confirms name, model, status, context, VIPs
```

## 4. Run

### Outside Docker

```sh
./clark run
# first run prints a QR code — scan it in WhatsApp > Settings > Linked Devices
```

### With Docker

```sh
cp .env.example .env   # ensure OLLAMA_MODEL and, if needed, OLLAMA_URL are set
docker compose up -d
docker compose logs -f # QR code appears here on first link
# Ctrl-C detaches; the container keeps running
```

Notes:

* On a fresh database Clark starts silent. VIP messages are ignored until you text `wake up buddy` from your own chat (the self-chat is always trusted) or run `docker compose exec clark clark toggle`.
* Bootstrap your inner circle from your own chat: `add vip <number>, <name>, <relation>` (e.g. `add vip 6281234567890, Tiara, Girlfriend`). If you copied an existing `mystore.db` to `data/clark.db` before first boot, this step is skipped.
* `OLLAMA_URL` defaults to `http://host.docker.internal:11434` inside the container. Override it in `.env` if Ollama is elsewhere.

## 5. Verify

```sh
./clark view
docker ps --filter name=clark --format '{{.Names}}\t{{.Status}}'
curl -k https://clark.studio.lab/web/api/state   # when WEB_ENABLED=1
```

Next steps: `cli.md` for day-to-day commands, `in-chat.md` for the Master-only chat surface, `transports.md` to add iMessage and voice, `web-console.md` for the dashboard, and `alerts-and-monitoring.md` for operations.
