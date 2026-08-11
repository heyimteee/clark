// Package config loads clark's configuration from the environment (and .env).
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds clark's runtime configuration.
type Config struct {
	OllamaURL    string
	OllamaModel  string
	DBPath       string
	TavilyAPIKey string

	// Persona shapes the butler's identity. Every field is optional and can be
	// overridden in .env or via the environment so users can run clark without
	// any personal data baked into the prompt.
	MasterName   string   // MASTER_NAME       e.g. "Sir Tristan Al Harrish Basori"
	ProtocolName string   // PROTOCOL_NAME     e.g. "Basori" (renders "The Basori Protocol")
	PalaceName   string   // PALACE_NAME       e.g. "Basori Digital Palace"
	BypassPhrase string   // BYPASS_PHRASE     e.g. "get him to me"
	InnerCircle  []Person // INNER_CIRCLE    e.g. "Tiara|Girlfriend;Anang|Father"
	NoNotify     bool     // CLARK_NO_NOTIFY   suppress desktop notifications (headless)

	// iMessage bridge transport. The bridge daemon on the Mac watches
	// chat.db and polls this transport's HTTP API over the reverse proxy.
	IMessageEnabled     bool   // IMESSAGE_ENABLED       "true" to serve the bridge API
	IMessageListenAddr  string // IMESSAGE_LISTEN_ADDR   default ":8090"
	IMessageBridgeToken string // IMESSAGE_BRIDGE_TOKEN  shared secret for the bridge
	IMessageSelfHandle  string // IMESSAGE_SELF_HANDLE   Master's own "+6281111111111"
}

// Person is a named person with an optional relation to the Master.
type Person struct {
	Name     string
	Relation string
}

// Load reads .env (if present) and validates the configuration.
func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("fail to load .env: %v", err)
	}

	ollamaURL := os.Getenv("OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}

	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		return nil, fmt.Errorf("no OLLAMA_MODEL set. Add OLLAMA_MODEL to your .env, e.g. OLLAMA_MODEL=llama3.2:latest")
	}

	dbPath := os.Getenv("CLARK_DB")
	if dbPath == "" {
		dbPath = "mystore.db"
	}

	noNotify := os.Getenv("CLARK_NO_NOTIFY")
	noNotifyOn := noNotify == "1" || noNotify == "true" || noNotify == "on"

	listenAddr := os.Getenv("IMESSAGE_LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8090"
	}

	return &Config{
		OllamaURL:    ollamaURL,
		OllamaModel:  model,
		DBPath:       dbPath,
		TavilyAPIKey: os.Getenv("TAVILY_API_KEY"),

		MasterName:   os.Getenv("MASTER_NAME"),
		ProtocolName: os.Getenv("PROTOCOL_NAME"),
		PalaceName:   os.Getenv("PALACE_NAME"),
		BypassPhrase: os.Getenv("BYPASS_PHRASE"),
		InnerCircle:  parsePeople(os.Getenv("INNER_CIRCLE")),
		NoNotify:     noNotifyOn,

		IMessageEnabled:     envOn(os.Getenv("IMESSAGE_ENABLED")),
		IMessageListenAddr:  listenAddr,
		IMessageBridgeToken: os.Getenv("IMESSAGE_BRIDGE_TOKEN"),
		IMessageSelfHandle:  os.Getenv("IMESSAGE_SELF_HANDLE"),
	}, nil
}

// envOn interprets a boolean-style environment variable ("1", "true", "on").
func envOn(v string) bool {
	return v == "1" || v == "true" || v == "on"
}

// parsePeople parses the INNER_CIRCLE list. Format: "Name|Relation;Name|Relation".
// The relation is optional: "Name" alone yields an empty Relation.
func parsePeople(raw string) []Person {
	var out []Person
	for _, seg := range strings.Split(raw, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		parts := strings.SplitN(seg, "|", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		person := Person{Name: name}
		if len(parts) == 2 {
			person.Relation = strings.TrimSpace(parts[1])
		}
		out = append(out, person)
	}
	return out
}
