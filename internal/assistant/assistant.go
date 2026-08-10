// Package assistant implements clark's butler: settings, inner circle, and replies.
package assistant

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
)

//go:embed prompt.md
var promptTemplate string

// LLM generates replies from a chat history.
type LLM interface {
	Chat(ctx context.Context, messages []ollama.Message) (string, error)
}

// Service is the assistant's orchestration layer. It satisfies whatsapp.Butler.
type Service struct {
	settings store.Settings
	history  store.HistoryStore
	vip      *VIP
	llm      LLM
	model    string
	name     string
	status   bool
	context  string
}

// New loads persisted state and returns a ready Service.
func New(cfg *config.Config, st *store.Store, llm LLM) (*Service, error) {
	s := &Service{
		settings: st,
		history:  st,
		vip:      NewVIP(st),
		llm:      llm,
		model:    cfg.OllamaModel,
	}

	if err := s.vip.Load(); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) load() error {
	name, err := s.settings.Get("name")
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("Name is empty. Error occured Sir.")
	}

	ctxValue, err := s.settings.Get("context")
	if err != nil {
		return err
	}
	if ctxValue == "" {
		return fmt.Errorf("Master Context is empty. Error occured Sir.")
	}

	statusStr, err := s.settings.Get("status")
	if err != nil {
		return err
	}
	status, err := strconv.ParseBool(statusStr)
	if err != nil {
		return fmt.Errorf("Invalid status value Sir. Error: %w", err)
	}

	s.name = name
	s.context = ctxValue
	s.status = status
	return nil
}

// Name returns the assistant's display name.
func (s *Service) Name() string { return s.name }

// Model returns the configured Ollama model.
func (s *Service) Model() string { return s.model }

// Context returns the master context.
func (s *Service) Context() string { return s.context }

// Enabled reports whether the assistant accepts and answers messages.
func (s *Service) Enabled() bool { return s.status }

// Relation resolves a jid to its "Name (Relation)" label.
func (s *Service) Relation(jid string) (string, bool) {
	return s.vip.Check(jid)
}

// AddVIP parses and persists a "[number], [name], [relation]" entry.
func (s *Service) AddVIP(input string) error {
	return s.vip.Add(input)
}

// DeleteVIP removes a VIP by number.
func (s *Service) DeleteVIP(input string) error {
	return s.vip.Delete(input)
}

// VIPList returns the current inner circle keyed by jid.
func (s *Service) VIPList() map[string]string {
	return s.vip.List()
}

// Init seeds the default settings.
func (s *Service) Init() error {
	return s.settings.InitDefaults()
}

// IsInitialized reports whether defaults have been seeded.
func (s *Service) IsInitialized() (bool, error) {
	return s.settings.IsInitialized()
}

// Toggle flips the enabled status.
func (s *Service) Toggle() error {
	cur, err := s.settings.Get("status")
	if err != nil {
		return err
	}

	statusBool, err := strconv.ParseBool(cur)
	if err != nil {
		return err
	}

	newStatus := fmt.Sprintf("%v", !statusBool)
	if err := s.settings.Set("status", newStatus); err != nil {
		return err
	}

	s.status = !statusBool
	logging.Log("CLARK", logging.SevInfo, "STATUS", "Assistant status changed", "enabled", s.status)
	return nil
}

// SetContext updates the master context.
func (s *Service) SetContext(contextInput string) error {
	if err := s.settings.Set("context", contextInput); err != nil {
		return err
	}

	s.context = contextInput
	logging.Log("CLARK", logging.SevInfo, "CONTEXT", "Master context loaded", "context", s.context)
	return nil
}

// Reply produces an answer for a VIP's message, persisting the exchange.
func (s *Service) Reply(ctx context.Context, senderJID, userMsg string) (string, error) {
	if senderJID == "" {
		return "", fmt.Errorf("empty sender JID")
	}

	if _, isVIP := s.vip.Check(senderJID); !isVIP {
		return "", fmt.Errorf("sender not in VIP list")
	}

	if userMsg == "" {
		return "", fmt.Errorf("empty message content")
	}

	if err := s.history.SaveMessage(senderJID, "user", userMsg); err != nil {
		return "", err
	}

	history, err := s.history.Messages(senderJID)
	if err != nil {
		return "", err
	}

	if len(history) == 0 {
		return "", fmt.Errorf("no chat history available")
	}

	relation, _ := s.vip.Check(senderJID)
	systemPrompt := fmt.Sprintf(promptTemplate, s.name, s.context, s.vip.list(), relation)

	messages := make([]ollama.Message, 0, len(history)+1)
	messages = append(messages, ollama.Message{Role: "system", Content: systemPrompt})
	for _, m := range history {
		messages = append(messages, ollama.Message{Role: m.Role, Content: m.Content})
	}

	logging.Log("OLLAMA", logging.SevInfo, "REQUEST", "Generating response", "model", s.model)
	start := time.Now()

	aiReply, err := s.llm.Chat(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("failed to execute model: %w", err)
	}

	logging.Log("OLLAMA", logging.SevInfo, "RESPONSE", "Generation completed",
		"model", s.model,
		"duration", time.Since(start).Round(time.Millisecond))

	if err := s.history.SaveMessage(senderJID, "assistant", aiReply); err != nil {
		return "", err
	}

	return aiReply, nil
}
