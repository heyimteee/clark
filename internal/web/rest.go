package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
)

// state builds the full console snapshot the SPA re-renders from. Shape is
// fixed by V4_PLAN §6.2.
func (s *Server) state() map[string]any {
	b := s.butler
	return map[string]any{
		"name":         b.Name(),
		"model":        b.Model(),
		"enabled":      b.Enabled(),
		"thinking":     b.Thinking(),
		"alertMode":    b.AlertMode(),
		"historyLimit": b.HistoryLimit(),
		"context":      b.Context(),
		"sttModel":     s.sttModel,
		"ttsEngine":    s.ttsEngine,
		"ttsVoice":     s.ttsVoice(),
		"vips":         s.vipEntries(),
		"tools":        s.toolList(),
	}
}

func (s *Server) ttsVoice() string {
	if s.voice != nil && s.voice.TTS != nil {
		return s.voice.TTS.Voice()
	}
	return ""
}

func (s *Server) toolList() []map[string]any {
	tools := s.butler.Tools().List()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Definition.Name,
			"description": t.Definition.Description,
			"parameters":  t.Definition.Parameters,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["name"].(string)
		b, _ := out[j]["name"].(string)
		return a < b
	})
	return out
}

// vipEntries renders the inner circle as the plan's array of
// {jid, name, relation, enabled, access}.
func (s *Server) vipEntries() []map[string]any {
	out := make([]map[string]any, 0)
	if s.store == nil {
		return out
	}
	entries, err := s.store.All()
	if err != nil {
		logging.Log("WEB", logging.SevWarn, "VIPLOAD", "Failed to load VIP entries", "error", err.Error())
		return out
	}
	for _, e := range entries {
		access, _, err := s.butler.AccessFor(e.JID)
		if err != nil {
			access = nil
		}
		on, hasOverride, _ := s.store.Enabled(e.JID)
		enabled := s.butler.Enabled()
		if hasOverride {
			enabled = on
		}
		out = append(out, map[string]any{
			"jid":      e.JID,
			"name":     e.Name,
			"relation": e.Relation,
			"enabled":  enabled,
			"access":   access,
		})
	}
	return out
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

// handleHistory serves chat history scoped to global, vip, or web (chronological).
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	switch scope {
	case "global":
		entries, err := s.store.AllRecentMessages(limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load history"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	case "vip":
		jid := r.URL.Query().Get("jid")
		if jid == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "jid is required for vip scope"})
			return
		}
		entries, err := s.store.RecentMessages(jid, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load history"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	case "web", "":
		entries, err := s.store.RecentMessages(webJID, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load history"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "scope must be global, vip, or web"})
	}
}

// handleTodos serves the per-conversation todo list.
func (s *Server) handleTodos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		status := r.URL.Query().Get("status")
		limit := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		todos, err := s.store.ListTodos(webJID, status, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to list todos"})
			return
		}
		if todos == nil {
			todos = []store.Todo{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"todos": todos})
	case http.MethodPost:
		var body struct {
			Text        string `json:"text"`
			Description string `json:"description"`
			Priority    *int   `json:"priority"`
			Due         string `json:"due"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is required"})
			return
		}
		priority := 0
		if body.Priority != nil {
			priority = *body.Priority
		}
		var dueAt *time.Time
		if body.Due != "" {
			if t, err := time.Parse(time.RFC3339, body.Due); err == nil {
				dueAt = &t
			}
		}
		id, err := s.store.AddTodo(webJID, body.Text, body.Description, priority, dueAt)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to add todo"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) handleTodoAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/web/api/todos/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id is required"})
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid id"})
		return
	}
	if len(parts) == 2 && parts[1] == "complete" {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		if err := s.store.CompleteTodo(id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to complete todo"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if len(parts) == 2 && parts[1] == "status" {
		if r.Method != http.MethodPost && r.Method != http.MethodPatch {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		var body struct {
			Status string `json:"status"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Status == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "status is required"})
			return
		}
		if err := s.store.UpdateTodoStatus(id, body.Status); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if err := s.store.DeleteTodo(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to delete todo"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Mutations (every one returns a fresh state snapshot) ---

func (s *Server) handleSetStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "enabled is required"})
		return
	}
	if err := s.butler.SetStatus(*body.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to set status"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

// handleKill is a no-body emergency endpoint that instantly silences Clark.
// POST /web/api/kill with auth — one tap from a phone Shortcut or curl.
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	if err := s.butler.SetStatus(false); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to kill"})
		return
	}
	logging.Log("WEB", logging.SevWarn, "KILL", "Emergency kill switch activated")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Clark silenced"})
}

func (s *Server) handleSetThinking(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "enabled is required"})
		return
	}
	if err := s.butler.SetThinking(*body.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to set thinking"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

func (s *Server) handleSetAlertMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if body.Mode != "voice" && body.Mode != "silent" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mode must be voice or silent"})
		return
	}
	if err := s.butler.SetAlertMode(body.Mode); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to set alert mode"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

func (s *Server) handleSetHistoryLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Limit *int `json:"limit"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.Limit == nil || *body.Limit < 1 || *body.Limit > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "limit must be between 1 and 50"})
		return
	}
	if err := s.butler.SetHistoryLimit(*body.Limit); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to set history limit"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

func (s *Server) handleSetContext(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Context string `json:"context"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if err := s.butler.SetContext(body.Context); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to set context"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

// handleAddVIP accepts the plan's {input: "628…,Name,Relation"} payload.
func (s *Server) handleAddVIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input string `json:"input"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.Input == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "input is required"})
		return
	}
	if err := s.butler.AddVIP(body.Input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

func (s *Server) handleAddVIPBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Entries []string `json:"entries"`
	}
	if err := decodeBody(w, r, &body); err != nil || len(body.Entries) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "entries are required"})
		return
	}
	if err := s.butler.AddVIPBulk(body.Entries); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

func (s *Server) handleDeleteVIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JID string `json:"jid"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.JID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "jid is required"})
		return
	}
	if err := s.butler.DeleteVIP(body.JID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

func (s *Server) handleSetVIPStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JID     string `json:"jid"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.JID == "" || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "jid and enabled are required"})
		return
	}
	if err := s.butler.SetVIPStatus(body.JID, *body.Enabled); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

// handleSetAccess toggles one tool for one VIP, mirroring the CLI access logic.
func (s *Server) handleSetAccess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JID     string `json:"jid"`
		Tool    string `json:"tool"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.JID == "" || body.Tool == "" || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "jid, tool, and enabled are required"})
		return
	}

	grants, _, err := s.butler.AccessFor(body.JID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load access"})
		return
	}
	next := make([]string, 0, len(grants)+1)
	found := false
	for _, g := range grants {
		if g == body.Tool {
			found = true
			if *body.Enabled {
				next = append(next, g)
			}
			continue
		}
		next = append(next, g)
	}
	if *body.Enabled && !found {
		next = append(next, body.Tool)
	}
	if err := s.butler.SetAccess(body.JID, next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JID string `json:"jid"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.JID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "jid is required"})
		return
	}
	if err := s.store.ClearHistory(body.JID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to clear history"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state()})
}

// handleSend delivers a message through the full AI path and returns a fresh
// snapshot. Intended for the web chat session and scripting; the Chat WS is
// the primary UI channel.
//
// Security (#58): only the web session's own conversation may be targeted. A
// caller-supplied VIP jid would poison that person's stored history with
// master-context turns, so any other value is rejected outright.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JID  string `json:"jid"`
		Text string `json:"text"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.JID == "" || body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "jid and text are required"})
		return
	}
	if body.JID != webJID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `jid must be "web"`})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	reply, thinking, err := s.butler.ReplyLLM(ctx, webJID, body.Text, true)
	if err != nil {
		if errors.Is(err, ollama.ErrRateLimited) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "I'm a bit swamped. Try again in a minute or two."})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to deliver message"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.state(), "reply": reply, "thinking": thinking})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}
