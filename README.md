# Clark

A personal AI butler for your WhatsApp.

Clark is a command-line application that runs a sophisticated AI-powered butler for your WhatsApp account. It uses a local Ollama model to generate intelligent, context-aware responses, acting as a gatekeeper for your messages while you're away. Clark only interacts with a pre-approved list of "VIP" contacts, ensuring your privacy and focus.

![License](https://img.shields.io/badge/License-MIT-blue.svg)
![Go Version](https://img.shields.io/badge/Go-1.25+-brightgreen.svg)

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
    Clark calls Ollama's native `/api/chat` endpoint with thinking disabled, so reasoning is off even for thinking-capable models.

    Optional variables:
    ```
    CLARK_DB=mystore.db          # SQLite path (default mystore.db)
    CLARK_LOG_FORMAT=json        # switch logs from colored to JSON
    NO_COLOR=1                   # disable ANSI colors
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
    -   **List:** `./clark vip`

-   **`ctx`**: Sets the master context for the AI. This tells the butler your current status.
    ```sh
    ./clark ctx -c "Currently in a board meeting until 5 PM."
    ```

-   **`toggle`**: Toggles the assistant's active status (on/off). The bot will not respond if toggled off.
    ```sh
    ./clark toggle
    ```

-   **`view`**: Displays the current assistant settings, including name, model, status, context, and the VIP list.
    ```sh
    ./clark view
    ```

## Project Layout

Clark is split into small, interface-driven packages so each concern scales and tests independently:

```
main.go                       thin entry point: validates args, dispatches
internal/app                  composition root + CLI commands (init/run/vip/ctx/toggle/view)
internal/config               single .env load + validation
internal/logging              structured colored log emitter + whatsmeow adapter
internal/store                persistence interfaces + SQLite implementation
internal/ollama               Ollama /api/chat client
internal/assistant            butler service: settings, VIP rules, prompt, replies
internal/whatsapp             WhatsApp transport: messenger, handler pipeline, echo tracker
internal/notify               desktop notifications
```

Dependencies flow downward (`app -> whatsapp, assistant, store, ollama, notify`); the
message handler depends on small interfaces (`Messenger`, `Butler`, `Notifier`) rather
than concrete types, so adding a transport, command, or AI backend means adding an
implementation, not editing the pipeline.

## License

This project is licensed under the MIT License. See the [LICENSE](./LICENSE) file for details.
