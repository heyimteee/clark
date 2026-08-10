package config

import (
	"os"
	"path/filepath"
	"testing"
)

func withEnvFile(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
}

func TestLoadDefaults(t *testing.T) {
	withEnvFile(t, "OLLAMA_MODEL=gemma4:e2b\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OllamaURL != "http://localhost:11434" {
		t.Errorf("OllamaURL = %q, want default", cfg.OllamaURL)
	}
	if cfg.OllamaModel != "gemma4:e2b" {
		t.Errorf("OllamaModel = %q, want gemma4:e2b", cfg.OllamaModel)
	}
	if cfg.DBPath != "mystore.db" {
		t.Errorf("DBPath = %q, want mystore.db", cfg.DBPath)
	}
}

func TestLoadReadsEnv(t *testing.T) {
	withEnvFile(t, "OLLAMA_MODEL=gemma4:e2b\nOLLAMA_URL=http://ollama:11434\nCLARK_DB=/tmp/x.db\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OllamaURL != "http://ollama:11434" {
		t.Errorf("OllamaURL = %q, want http://ollama:11434", cfg.OllamaURL)
	}
	if cfg.DBPath != "/tmp/x.db" {
		t.Errorf("DBPath = %q, want /tmp/x.db", cfg.DBPath)
	}
}

func TestLoadRequiresModel(t *testing.T) {
	withEnvFile(t, "")
	t.Setenv("OLLAMA_MODEL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded without OLLAMA_MODEL, want error")
	}
}

func TestLoadMissingEnvFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("OLLAMA_MODEL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded without .env, want error")
	}
}
