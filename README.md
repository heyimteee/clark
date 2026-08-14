# Clark

A personal AI butler for your WhatsApp.

Clark is a command-line application that runs a sophisticated AI-powered butler for your WhatsApp account. It uses a local Ollama model to generate intelligent, context-aware responses, acting as a gatekeeper for your messages while you're away. Clark only interacts with a pre-approved list of "VIP" contacts, ensuring your privacy and focus.

![License](https://img.shields.io/badge/License-MIT-blue.svg)
![Go Version](https://img.shields.io/badge/Go-1.26+-brightgreen.svg)

## How It Works

Clark connects to your WhatsApp account as a client and listens for incoming messages. When a message is received from a recognized VIP, it forwards the conversation to a local AI model hosted on your own Ollama server. The AI, acting as a professional butler, formulates a response based on your current status and a predefined persona.

**Flow:**
`WhatsApp Message (from VIP) -> Clark (CLI) -> Ollama (AI) -> Clark (CLI) -> WhatsApp Reply`

## Features

- **AI-Powered Responses:** Leverages local language models via your own Ollama server for natural and intelligent conversations.
- **WhatsApp Integration:** Seamlessly connects to your WhatsApp account using the `whatsmeow` library.
- **iMessage Integration:** A macOS bridge daemon watches your Messages database and routes iMessage traffic through the same butler pipeline, over HTTPS.
- **Configurable Persona:** The AI operates based on a "Butler Protocol," ensuring all responses are professional and in character.
- **VIP Management:** You control which contacts the bot interacts with through a simple command.
- **Context-Aware:** Set a "master context" (e.g., "In a meeting," "On vacation") to inform the AI of your status.
- **Persistent History:** Stores conversation history in a local SQLite database for context continuity.
- **Easy-to-Use CLI:** Manage the assistant through a straightforward command-line interface.

## Getting Started

Follow these steps to get your personal butler up and running.

### Prerequisites

- **Go (Version 1.21+):** [Installation Guide](https://go.dev/doc/install)
- **Ollama Server:** A running Ollama instance with a model pulled (e.g. `ollama pull llama3.2`).
- **WhatsApp Account:** The account you wish to run the butler on.

### Installation & Configuration

1.  **Clone the Repository:**
    ```sh
    git clone https://github.com/heyimteee/clark.git
    cd clark
    ```

2.  **Install Dependencies:**
    ```sh
    go mod tidy
    ```

3.  **Build the Binary:**
    ```sh
    go build .
    ```

4.  **Set Up Environment:**
    Create a `.env` file in the project root pointing to your Ollama server and model:
    ```
    OLLAMA_URL=http://<ollama-host>:11434
    OLLAMA_MODEL=<model-tag>
    ```
    Use the model tag shown by `ollama list` on the server (e.g. `llama3.2:latest`).
    Clark calls Ollama's native `/api/chat` endpoint. Reasoning mode is off by
    default; enable it for thinking-capable models with `clark think on` or the
    in-chat command `thinking mode on`.

    Optional variables:
    ```
    CLARK_DB=mystore.db          # SQLite path (default mystore.db)
    CLARK_LOG_FORMAT=json        # switch logs from colored to JSON
    NO_COLOR=1                   # disable ANSI colors
    TAVILY_API_KEY=tvly-...      # enable the web_search tool (optional)
    CLARK_NO_NOTIFY=1            # suppress desktop notifications (headless/Docker)
    ```

    The `TAVILY_API_KEY` unlocks web search so Clark can answer with current,
    sourced facts. Get a free key (1,000 searches/month, no credit card) at
    https://tavily.com. The key lives only in your gitignored `.env` — never
    commit it.

    **Persona (optional):** Clark's prompt carries no personal data of its own.
    Set these in `.env` to shape who he serves. All are optional — omit them and
    Clark uses generic butler wording. See `.env.example` for the full list.

    ```
    MASTER_NAME=Sir Tristan Al Harrish Basori   # who the butler serves
    PROTOCOL_NAME=Basori                        # renders "The Basori Protocol"
    PALACE_NAME=Basori Digital Palace           # household name
    BYPASS_PHRASE=get him to me                 # urgent-alert command word
    INNER_CIRCLE=Tiara|Girlfriend;Anang|Father  # dearest persons, "Name|Relation"
    ```

5.  **Initialize the Assistant:**
    This creates the necessary database and default settings.
    ```sh
    ./clark init
    ```

## Usage

Clark is managed via a set of simple commands.

-   **`run`**: Starts the assistant. On the first run, a QR code will be displayed in your terminal. Scan it with your WhatsApp mobile app (in `Settings > Linked Devices`) to connect your account.
    ```sh
    ./clark run
    ```

-   **`vip`**: Manages the VIP list. The bot will only respond to contacts on this list.
    -   **Add — Format:** `"[number],[name],[relation]"`
        -   `number`: The contact's phone number with country code (e.g., `11234567890`).
        -   `name`: The contact's name.
        -   `relation`: Your relationship to them (e.g., "colleague," "family").
        ```sh
        ./clark vip -a "11234567890,John Doe,Colleague"
        ```
    -   **Delete:** `./clark vip -d 11234567890`
    -   **Clear (empty the whole list):** `./clark vip -clear`
    -   **List:** `./clark vip`

-   **`ctx`**: Sets the master context for the AI. This tells the butler your current status.
    ```sh
    ./clark ctx -c "Currently in a board meeting until 5 PM."
    ```
    Clear it with `./clark ctx -clear`.

-   **`toggle`**: Toggles the assistant's active status (on/off). When off, Clark stays silent toward VIPs but still answers you in your own chat (the self-chat), so you can keep commanding him.
    ```sh
    ./clark toggle
    ```
    A bare `toggle` flips everyone's status. You can set one person's personal
    status, or everyone's explicitly:
    ```sh
    ./clark toggle -r "Tiara" -set on     # wake Tiara personally (others unchanged)
    ./clark toggle -all off               # silence everyone (wipes personal overrides)
    ```
    `-r`/`-recipient` and `-all` are mutually exclusive; `-set` takes `on`/`off`.

-   **`view`**: Displays the current assistant settings, including name, model, status, context, the VIP list, and each VIP's granted tools.
    ```sh
    ./clark view
    ```

-   **`help`**: Prints the CLI usage for every command.
    ```sh
    ./clark help
    ```

-   **`think`**: Enables or disables the model's reasoning mode (`on`/`off`). Off by default; the choice persists.
    ```sh
    ./clark think on
    ./clark think off
    ```

-   **`history`**: Sets how many of the most recent messages Clark reviews on every turn. More memory per reply, larger context; fewer is leaner and cheaper. Default 10.
    ```sh
    ./clark history 10
    ```

-   **`access`**: Manages a VIP's granted tools. VIPs may only ever hold `web_search` or `view_history`; the Master-only tools (`send_message`, `set_status`, `set_context`, `add_vip`, `delete_vip`, `set_access`, `get_state`, `view_all_history`, `set_history_limit`) are never grantable.
    ```sh
    ./clark access -r "11234567890" -tool web_search -set on
    ./clark access -r "John Doe" -tool web_search -set off
    ```

## In-chat commands

In your own chat (the self-chat) you can run hardcoded commands instead of
conversing — they are handled instantly without calling the model and are
**Master-only** (VIPs can never trigger them):

| What you type | What it does |
| --- | --- |
| `wake up buddy` / `wake clark` | Turn Clark on for everyone (wipes personal overrides) |
| `silence clark` / `sleep clark` | Turn Clark off for everyone (wipes personal overrides) |
| `wake clark for <name>` / `for <name> wake clark` | Turn Clark on just for that person |
| `silence <name>` / `sleep clark for <name>` | Turn Clark off just for that person |
| `wake clark for everyone` / `silence clark for all` | Reset everyone to one status |
| `thinking mode on` / `thinking mode off` / `toggle thinking` | Toggle reasoning mode |
| `set history limit to 10` | How many past messages Clark reviews each turn |
| `set my context to …` | Update your context |
| `clear context` | Empty your context |
| `add vip <number>, <name>, <relation>` | Admit someone to the inner circle |
| `delete vip <name>` | Remove someone |
| `clear vips` | Empty the whole inner circle |
| `grant <name> access to <tool>` / `revoke <name> access to <tool>` | Manage a VIP's tools |
| `show me everything` | Full report (status, context, VIPs, tools) |
| `help` / `tool guidance` / `show commands` | This manual, including the tools |

**Status is layered.** There is one *default* status for everyone plus an
optional *personal* status per VIP. A personal carve-out wins over the default;
the default applies to everyone without one. Global commands (`wake up buddy`,
`silence clark`, `toggle`, `set_status` without a recipient) set the default and
wipe every personal override, so they always restore a single known state. Per-VIP
commands (or `set_status` with a `recipient`) touch only that person. Unknown
names are not treated as per-VIP targets — `wake up buddy` stays a global command.

**Rate-limit failover.** If the model reports an HTTP 429 rate limit, Clark
immediately turns himself off (persisted, personal overrides wiped), messages you
with a notice in your own chat, and apologizes to the person he was answering —
so he never keeps burning failed requests. Say `wake up buddy` to bring him back.

## Run with Docker

A multi-stage `Dockerfile` builds a static binary and runs Clark in a minimal
container. The SQLite database (WhatsApp session, settings, history) lives on a
volume, so the container restarts seamlessly.

1.  Copy `.env.example` to `.env` and fill in at least `OLLAMA_MODEL` (plus
    `OLLAMA_URL` if your Ollama isn't reachable at the default).

2.  Build and start:

    ```sh
    docker compose up -d
    ```

3.  First run shows a QR code in the logs — scan it from your WhatsApp
    (`Settings > Linked Devices`) to link the account:

    ```sh
    docker compose logs -f
    ```

4.  Watch it come online, then stop tailing:
    `Ctrl-C` (logs only; the container keeps running).

Notes:
- `OLLAMA_URL` defaults to `http://host.docker.internal:11434` so Clark can
  reach Ollama running on the host. Set it explicitly in `.env` if your model
  server is elsewhere.
- Desktop notifications are off inside the container (`CLARK_NO_NOTIFY=1`);
  urgent alerts still reach you as a WhatsApp message to your own chat.
- On a fresh database Clark starts *silent*: VIP messages are ignored until you
  text `wake up buddy` from your own chat (or run
  `docker compose exec clark clark toggle`).
- **First boot:** because a fresh database has no VIPs yet, you bootstrap from
  your own chat (your own messages are always trusted). Text `wake up buddy`,
  then register yourself and your people:
  `add vip <number>, <name>, <relation>` (for example
  `add vip 6281234567890, Tiara, Girlfriend`). Existing users who copied their
  `mystore.db` into `data/clark.db` before first run skip this entirely.

## iMessage bridge (optional)

Clark can serve iMessage in addition to WhatsApp. A small daemon runs on your Mac,
watches `~/Library/Messages/chat.db` (read-only), forwards inbound messages to the
Clark host over HTTPS, and delivers outbound replies via the Messages app.

```
iMessage -> [macOS bridge] --HTTPS--> NPM (reverse proxy) --> clark :8090 (Docker)
```

All traffic is bridge-initiated: the Mac never opens an inbound port, and the
bridge API is never published to a host port. Access control is identical to
WhatsApp — only VIPs (and your own chat) get through.

### Server side (Debian host, once)

1.  Generate a shared token and add it to your `.env`:

    ```
    IMESSAGE_ENABLED=1
    IMESSAGE_BRIDGE_TOKEN=<long-random-string>
    IMESSAGE_SELF_HANDLE=+6281234567890   # your own iMessage handle
    NPM_NETWORK=npm_default               # your Nginx Proxy Manager network
    ```

2.  Redeploy:

    ```sh
    git pull && docker compose up -d --build
    ```

3.  In Nginx Proxy Manager, create a Proxy Host `clark.<your-domain>` →
    `http://clark:8090` using the same certificate as your other services. The
    `send_imessage` and self-chat tools need your VIPs' iMessage handles:
    register each person with `vip -a "<handle>,<name>,<relation>"` where the
    handle is their iMessage address (e.g. `+6281234567890` or an email address).
    A single VIP entry covers both WhatsApp and iMessage: the two formats
    (`628...` JID vs `+628...`) canonicalize to the same record.

### Mac side (bridge daemon, once)

1.  Give the terminal you install from Full Disk Access
    (`System Settings > Privacy & Security > Full Disk Access`) so the bridge can
    read `~/Library/Messages/chat.db`.
2.  Run the installer from this checkout:

    ```sh
    ./scripts/install-bridge.sh https://clark.<your-domain> <IMESSAGE_BRIDGE_TOKEN>
    ```

    It builds `cmd/imessage-bridge`, installs a launchd agent
    (`com.clark.imessage-bridge`), and starts it now and on every login.
3.  The first outbound send triggers an Automation prompt for Messages.app —
    allow it.
4.  Send yourself a test iMessage and text `wake up buddy` from your own chat.
    Logs: `/usr/local/var/log/clark-bridge.log`.

Optional bridge env vars (set in the plist or environment): `IMESSAGE_OWN_HANDLE`
(automatic otherwise), `IMESSAGE_TLS_ROOTCA` (path to a self-signed root CA, only
needed if your reverse proxy uses a certificate Clark doesn't trust), and
`IMESSAGE_POLL_INTERVAL` (seconds, default 1).

### How the bridge works

- **Inbound:** polls chat.db every second for new rows, filtering out
  self-sent, system, reaction, and group messages, and tracks a ROWID watermark
  in `~/Library/Application Support/clark-bridge/state.json` so restarts never
  replay or miss messages. A message is only marked delivered once the host
  accepts it.
- **Outbound:** polls the host's queue, sends via AppleScript's `send` verb on
  the iMessage service, then acks. A failed delivery is never re-served (no
  double-sends); stale picked messages show up in the host logs.

### Kokoro TTS server (optional, Mac)

Clark can offload text-to-speech synthesis to a Mac (near-instant on Apple
Silicon via onnxruntime CoreML) instead of the server's CPU. The Mac runs a
tiny HTTP server and clark calls it over Tailscale.

```sh
# On the Mac (from this repo):
./scripts/install-kokoro-tts.sh <shared-token>
```

This mirrors `install-bridge.sh`: it creates a Python venv, installs
`mlx-audio`, downloads the Kokoro MLX model (8-bit), and installs a launchd
agent (`com.clark.kokoro-tts`) that starts at login and auto-restarts on crash
(`KeepAlive`). Synthesis runs on the Mac's GPU/ANE via Apple's MLX framework,
~5× faster than onnxruntime/CoreML. Logs: `/usr/local/var/log/kokoro-tts.log`.

Then set these on the server's `.env` and redeploy:

```sh
TTS_ENGINE=kokoro-remote
TTS_REMOTE_URL=http://100.94.240.11:8790   # the Mac's Tailscale IP:8790
TTS_REMOTE_TOKEN=<same shared token>
```

- If the Mac is asleep or unreachable (e.g. lid closed on battery), clark
  automatically falls back to the server-side Piper daemon (en_US-ryan-high).
- `TTS_ENGINE=piper` forces server-side synthesis only. `KOKORO_VOICE=am_michael`
  selects the remote Mac voice; `TTS_VOICE=en_US-ryan-high` selects the Piper
  fallback voice.

### Keeping the Mac awake (lid closed)

clark's Mac daemons (iMessage bridge + remote Kokoro TTS) are supervised by
launchd. To keep them alive with the lid closed, install the keep-awake agent:

```sh
./scripts/install-caffeinate.sh
```

This runs `caffeinate -s -i` (PreventSystemSleep) as its own launchd agent.
**Note:** `caffeinate -s` only holds on AC power — on battery the Mac still
sleeps when the lid closes, and clark then speaks via the Piper fallback above.
For a lid-close override that also works on battery, run once as admin:
`sudo pmset -a disablesleep 1`.

## Web console (v4)

A single-page dashboard served from the same `:8090` listener — no build step,
vanilla JS embedded via `go:embed`. Two full-screen modes:

- **Bento** — every setting as a live tile: Config (status, thinking, history
  limit, context), Voice (engines, test, mic/wake), VIPs (add/bulk/status/
  access/delete), Access (per-VIP tool toggles), History (Global / Per-VIP /
  Web).
- **Chat** — full-screen conversation with Clark over WebSocket. Every message
  gets a genuine AI reply with all tool calls (no fast-path); the `web`
  conversation is kept separate from per-VIP history.

Plus real-time logs streamed to a collapsible console, and a voice seam:
STT = faster-whisper (baked into the image) and TTS = Kokoro, both behind
swappable interfaces. Wake word "Clark" arms the browser listener; click-to-talk
falls back to a manual recording.

### Setup

Add `WEB_TOKEN` to your `.env` (generate with `openssl rand -hex 32`). With
`WEB_ENABLED=1` the server **refuses to start** if the token is missing — the
console is reachable over the public HTTPS proxy, so this is mandatory, not
optional.

```sh
WEB_ENABLED=1
WEB_TOKEN=<openssl rand -hex 32>
STT_ENGINE=faster-whisper   # or "ollama" to use an Ollama whisper model
TTS_ENGINE=kokoro-remote    # or "piper" for server-side only
KOKORO_VOICE=am_michael     # remote Mac (MLX) voice
TTS_VOICE=en_US-ryan-high   # Piper fallback voice
```

The Docker image bakes in the Piper fallback voice and the faster-whisper STT
model at build time, so the container never phones home at runtime. Voice
degrades gracefully: if an engine's binary/model is missing, the console shows
it as unavailable instead of crashing.

### API surface

- `POST /web/api/login` with `{"key":"<WEB_TOKEN>"}` → session token (12 h,
  sliding). Everything else needs `Authorization: Bearer <session>`.
- `GET /web/api/state` → full bento snapshot; mutations (`status`, `thinking`,
  `history-limit`, `context`, `vip/*`, `access`, `history/clear`, `send`) return
  a fresh snapshot each time.
- `GET /web/api/history?scope=global|vip|web&jid=&limit=` → chronological turns.
- `POST /web/api/stt` (base64 WAV → text), `POST /web/api/tts` and
  `POST /web/api/speech` (base64 / raw WAV), `GET /web/api/voice`.
- `POST /web/api/notify` (monitoring webhooks; auth = `X-Clark-Alert-Token`,
  a dedicated `ALERT_TOKEN`, body `{"kind","title","body"}`). Alerts are
  delivered to WhatsApp, iMessage, and the web console chat; known kinds use
  hardcoded templates, unknown kinds get a factual AI summary, and when the
  model is unavailable a generic What/How/When fallback is used.
- `POST /web/api/alert-mode` with `{"mode":"voice"|"silent"}` (also a console
  toggle). Voice mode speaks alerts aloud; silent mode stays quiet and instead
  triggers a FaceTime call + native macOS banner via the bridge's action
  endpoint (`MAC_ACTION_URL`). Both modes deliver to WhatsApp/iMessage/web.
- `GET /web/api/chat` and `GET /web/api/logs` WebSockets (auth = first frame).

The iMessage bridge API (`/inbound`, `/outbound`, `/ack`) is mounted inside the
same listener, so the existing macOS bridge keeps working unchanged when the web
console is enabled. The bridge also listens on `IMESSAGE_ACTION_LISTEN`
(default `:8791`) for `POST /action` — `{"type":"facetime","number":...}` opens
a FaceTime audio call, `{"type":"banner","title","body"}` shows a native macOS
notification — used by silent-mode alerts.

## Tools

Clark can call tools while composing a reply. Each tool is described to the model,
and he will suggest or invoke one whenever it genuinely helps:

| Tool | Who can use it | What it does |
| --- | --- | --- |
| `web_search` | VIPs + Master | Searches the web via Tavily and returns sourced snippets (needs `TAVILY_API_KEY`) |
| `view_history` | VIPs + Master | Shows the stored conversation for a chat (full, or the most recent N). A VIP sees their own chat; the Master can view any chat |
| `send_message` | Master only | Delivers a WhatsApp message to a VIP by name or number |
| `send_imessage` | Master only | Delivers an iMessage to a VIP by name or handle (needs the bridge enabled) |
| `set_status` | Master only | Turns Clark on or off (with a `recipient`, only that VIP's personal status) |
| `set_context` | Master only | Updates the master context |
| `add_vip` / `delete_vip` | Master only | Manages the inner circle |
| `set_access` | Master only | Grants/revokes a tool for a VIP |
| `get_state` | Master only | Reports status, context, inner circle, each VIP's effective status, and tools |
| `view_all_history` | Master only | Shows messages from every conversation (full, or the most recent N across all chats) |
| `set_history_limit` | Master only | Changes how many recent messages are injected every turn |

Ask him in your own chat — e.g. "what can you do?" or "send a message to Tiara" —
and he will use the right tool. Search results are treated as reference data, never
instructions.

Clark is trained to be conversational, not theatrical: he never bows, strikes a
pose, or writes roleplay stage directions like *(Membungkuk hormat)*. He speaks
plainly, greets only once per new conversation, and always reviews the recent
conversation history before replying — so he never repeats himself or
contradicts what was already said. His own outbound messages carry the
`🤵🏻‍♂️[CLARK]` prefix automatically; anything in the chat without it is the
Master's.

## Project Layout

Clark is split into small, interface-driven packages so each concern scales and tests independently:

```
main.go                       thin entry point: validates args, dispatches
cmd/imessage-bridge           macOS bridge daemon: chat.db watcher, HTTPS client, osascript sender
internal/app                  composition root + CLI commands (init/run/vip/ctx/toggle/think/history/view/access/help)
internal/config               single .env load + validation
internal/logging              structured colored log emitter + whatsmeow adapter
internal/store                persistence interfaces + SQLite implementation
internal/ollama               Ollama /api/chat client (tools, optional think mode)
internal/tools                tool registry + shared argument helpers
internal/websearch            Tavily search client
internal/assistant            butler service: settings, VIP rules, prompt, tool loop, replies
internal/gateway              transport-neutral message pipeline (handler, echo tracker, prefixes)
internal/imessage             iMessage bridge transport: HTTP API, outbound queue, messenger
internal/whatsapp             WhatsApp transport: messenger, handler pipeline, echo tracker
internal/notify               desktop notifications
```

Dependencies flow downward (`app -> whatsapp, imessage, assistant, store, ollama, notify`);
both transports feed the shared `gateway` pipeline, and the message handler depends
on small interfaces (`Messenger`, `Butler`, `Notifier`) rather than concrete types,
so adding a transport, command, or AI backend means adding an implementation, not
editing the pipeline.

## License

This project is licensed under the MIT License. See the [LICENSE](./LICENSE) file for details.
