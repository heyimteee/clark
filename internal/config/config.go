// Package config loads clark's configuration from the environment (and .env).
package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// Config holds clark's runtime configuration.
type Config struct {
	OllamaURL   string
	OllamaModel string
	DBPath      string
}

// Load reads .env once and validates the configuration.
func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
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

	return &Config{
		OllamaURL:   ollamaURL,
		OllamaModel: model,
		DBPath:      dbPath,
	}, nil
}
