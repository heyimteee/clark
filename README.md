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
| `wake up buddy` / `wake clark` | Turn Clark on |
| `silence clark` / `sleep clark` | Turn Clark off |
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

## Tools

Clark can call tools while composing a reply. Each tool is described to the model,
and he will suggest or invoke one whenever it genuinely helps:

| Tool | Who can use it | What it does |
| --- | --- | --- |
| `web_search` | VIPs + Master | Searches the web via Tavily and returns sourced snippets (needs `TAVILY_API_KEY`) |
| `view_history` | VIPs + Master | Shows the stored conversation for a chat (full, or the most recent N). A VIP sees their own chat; the Master can view any chat |
| `send_message` | Master only | Delivers a WhatsApp message to a VIP by name or number |
| `set_status` | Master only | Turns Clark on or off |
| `set_context` | Master only | Updates the master context |
| `add_vip` / `delete_vip` | Master only | Manages the inner circle |
| `set_access` | Master only | Grants/revokes a tool for a VIP |
| `get_state` | Master only | Reports status, context, inner circle, and tools |
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
internal/app                  composition root + CLI commands (init/run/vip/ctx/toggle/think/history/view/access/help)
internal/config               single .env load + validation
internal/logging              structured colored log emitter + whatsmeow adapter
internal/store                persistence interfaces + SQLite implementation
internal/ollama               Ollama /api/chat client (tools, optional think mode)
internal/tools                tool registry + shared argument helpers
internal/websearch            Tavily search client
internal/assistant            butler service: settings, VIP rules, prompt, tool loop, replies
internal/whatsapp             WhatsApp transport: messenger, handler pipeline, echo tracker
internal/notify               desktop notifications
```

Dependencies flow downward (`app -> whatsapp, assistant, store, ollama, notify`); the
message handler depends on small interfaces (`Messenger`, `Butler`, `Notifier`) rather
than concrete types, so adding a transport, command, or AI backend means adding an
implementation, not editing the pipeline.

## License

This project is licensed under the MIT License. See the [LICENSE](./LICENSE) file for details.
