package whatsapp

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gen2brain/beeep"
	"github.com/joho/godotenv"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Assistant struct {
	Name          string
	Status        bool
	MasterContext string
	VIP           *VIP
	DB            *Database
	OllamaURL     string
	Model         string
}

//go:embed utils/newPrompt.md
var promptTemplate string

func AssistantInit() (*Assistant, error) {
	err := godotenv.Load()

	if err != nil {
		return nil, fmt.Errorf("fail to load .env: %v", err)
	}

	ollamaURL := os.Getenv("OLLAMA_URL")

	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}

	beeep.AppName = "Clark"

	model := os.Getenv("OLLAMA_MODEL")

	if model == "" {
		return nil, fmt.Errorf("no OLLAMA_MODEL set. Add OLLAMA_MODEL to your .env, e.g. OLLAMA_MODEL=llama3.2:latest")
	}

	db, err := InitDB()

	if err != nil {
		return nil, err
	}

	vip := InitVIP(db)

	ast := &Assistant{
		Name:          "",
		Status:        false,
		MasterContext: "",
		VIP:           vip,
		DB:            db,
		OllamaURL:     ollamaURL,
		Model:         model,
	}

	err = ast.VIP.LoadVIP()

	if err != nil {
		return nil, err
	}

	ast.loadAssistant()
	return ast, nil
}

func (ast *Assistant) ToggleStatus() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var newStatus string

	ast.DB.DB.QueryRowContext(ctx, "SELECT value FROM assistant_setting WHERE key = 'status'").Scan(&newStatus)

	statusBool, err := strconv.ParseBool(newStatus)

	if err != nil {
		return err

	}
	newStatusStr := fmt.Sprintf("%v", !statusBool)

	query := `INSERT OR REPLACE INTO assistant_setting (key, value) VALUES (?, ?)`
	_, err = ast.DB.DB.ExecContext(ctx, query, "status", newStatusStr)

	if err != nil {
		return fmt.Errorf("failed to update status in DB: %w", err)
	}

	ast.Status = !statusBool

	Log("CLARK", SevInfo, "STATUS", "Assistant status changed", "enabled", ast.Status)

	return nil
}

func (ast *Assistant) SetMasterContext(contextInput string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `INSERT OR REPLACE INTO assistant_setting (key, value) VALUES (?, ?)`
	_, err := ast.DB.DB.ExecContext(ctx, query, "context", contextInput)

	if err != nil {
		return err
	}

	ast.MasterContext = contextInput

	Log("CLARK", SevInfo, "CONTEXT", "Master context loaded", "context", ast.MasterContext)

	return nil
}

func (ast *Assistant) AstSettingInit() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `INSERT OR IGNORE INTO assistant_setting (key, value) VALUES (?, ?)`

	defaults := map[string]string{
		"name":    "Clark",
		"status":  "false",
		"context": "",
	}

	for key, value := range defaults {
		_, err := ast.DB.DB.ExecContext(ctx, query, key, value)

		if err != nil {
			return fmt.Errorf("fail to initialize default for %s: %w", key, err)
		}
	}

	return nil
}

func (ast *Assistant) CheckAst() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var count int
	query := `SELECT COUNT(*) FROM assistant_setting`

	err := ast.DB.DB.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("fail to load table <vip>: %w", err)
	}

	return count == 3, nil
}

func (ast *Assistant) loadAssistant() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var name, status, context string
	ast.DB.DB.QueryRowContext(ctx, "SELECT value FROM assistant_setting WHERE key = 'name'").Scan(&name)
	ast.DB.DB.QueryRowContext(ctx, "SELECT value FROM assistant_setting WHERE key = 'status'").Scan(&status)
	ast.DB.DB.QueryRowContext(ctx, "SELECT value FROM assistant_setting WHERE key = 'context'").Scan(&context)

	if name == "" {
		return fmt.Errorf("Name is empty. Error occured Sir.")
	}

	ast.Name = name

	if context == "" {
		return fmt.Errorf("Master Context is empty. Error occured Sir.")
	}

	ast.MasterContext = context

	statusBool, err := strconv.ParseBool(status)

	if err != nil {
		return fmt.Errorf("Invalid status value Sir. Error: %w", err)
	}

	ast.Status = statusBool

	return nil
}

func (ast *Assistant) GetHistory(jid string) ([]chatMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	getHistoryQuery := "SELECT role, content FROM chat_history WHERE jid = ? ORDER BY timestamp ASC"
	rows, err := ast.DB.DB.QueryContext(ctx, getHistoryQuery, jid)

	if err != nil {
		return nil, fmt.Errorf("failed to query history: %w", err)
	}

	defer rows.Close()

	var history []chatMessage
	for rows.Next() {
		var dbRole, content string
		rows.Scan(&dbRole, &content)

		switch dbRole {
		case "user":
			history = append(history, chatMessage{
				Role:    "user",
				Content: content,
			})
		case "assistant":
			history = append(history, chatMessage{
				Role:    "assistant",
				Content: content,
			})
		}
	}
	return history, nil
}

func (ast *Assistant) GetAIResponse(senderJid, userMsg string) (string, error) {
	if senderJid == "" {
		return "", fmt.Errorf("empty sender JID")
	}

	relation, isVIP := ast.VIP.CheckVIP(senderJid)

	if !isVIP {
		return "", fmt.Errorf("sender not in VIP list")
	}

	if userMsg == "" {
		return "", fmt.Errorf("empty message content")
	}

	err := ast.DB.SaveMessage(senderJid, "user", userMsg)

	if err != nil {
		return "", err
	}

	history, err := ast.GetHistory(senderJid)

	if err != nil {
		return "", err
	}

	if len(history) == 0 {
		return "", fmt.Errorf("no chat history available")
	}

	systemPrompt := fmt.Sprintf(promptTemplate, ast.Name, ast.MasterContext, ast.VIP, relation)

	messages := make([]chatMessage, 0, len(history)+1)
	messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, history...)

	Log("OLLAMA", SevInfo, "REQUEST", "Generating response", "model", ast.Model)
	start := time.Now()

	aiReply, err := ollamaChat(ast.OllamaURL, ast.Model, messages)
	if err != nil {
		return "", fmt.Errorf("failed to execute model: %w", err)
	}

	Log("OLLAMA", SevInfo, "RESPONSE", "Generation completed",
		"model", ast.Model,
		"duration", time.Since(start).Round(time.Millisecond))

	err = ast.DB.SaveMessage(senderJid, "assistant", aiReply)

	if err != nil {
		return "", err
	}

	return aiReply, nil
}

type ollamaChatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Think    bool          `json:"think"`
}

type ollamaChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

func ollamaChat(baseURL, model string, messages []chatMessage) (string, error) {
	reqBody, err := json.Marshal(ollamaChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
		Think:    false,
	})

	if err != nil {
		return "", fmt.Errorf("failed to encode request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/api/chat"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))

	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)

	if err != nil {
		return "", fmt.Errorf("failed to reach Ollama at %s: %w", url, err)
	}

	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var chatResp ollamaChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if chatResp.Message.Content == "" {
		return "", fmt.Errorf("empty response from model")
	}

	return chatResp.Message.Content, nil
}
