package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/calendar"
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
		"version":      s.version,
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

/* ---------------- chat sessions ---------------- */

// sessionPreview truncates the latest message for sidebar display.
func sessionPreview(entries []store.Message) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(entries[i].Content); s != "" {
			if len(s) > 80 {
				return s[:80] + "…"
			}
			return s
		}
	}
	return ""
}

// handleChatSessions serves GET (list) and POST (create) on the collection.
func (s *Server) handleChatSessions(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.EnsureDefaultWebSession(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load sessions"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := s.store.ListWebSessions()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to list sessions"})
			return
		}
		views := make([]map[string]any, 0, len(rows))
		for _, ws := range rows {
			msgs, err := s.store.RecentMessages(store.WebSessionJID(ws.ID), 1)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load session preview"})
				return
			}
			views = append(views, map[string]any{
				"id":         ws.ID,
				"title":      ws.Title,
				"preview":    sessionPreview(msgs),
				"updated_at": ws.UpdatedAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		_ = decodeBody(w, r, &body)
		ws, err := s.store.CreateWebSession(body.Title)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to create session"})
			return
		}
		s.broadcastChanged("sessions_changed")
		writeJSON(w, http.StatusCreated, map[string]any{"session": ws})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// handleChatSessionAction serves one session: GET messages, PUT rename,
// DELETE with history.
func (s *Server) handleChatSessionAction(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/web/api/chat/sessions/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id is required"})
		return
	}
	if _, err := s.store.GetWebSession(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	switch {
	case r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "messages":
		msgs, err := s.store.Messages(store.WebSessionJID(id))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load messages"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
	case r.Method == http.MethodPut && len(parts) == 1:
		var body struct {
			Title string `json:"title"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Title == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title is required"})
			return
		}
		ws, err := s.store.RenameWebSession(id, body.Title)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		s.broadcastChanged("sessions_changed")
		writeJSON(w, http.StatusOK, map[string]any{"session": ws})
	case r.Method == http.MethodDelete && len(parts) == 1:
		if err := s.store.DeleteWebSession(id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		// Never leave the sidebar empty: hand the client a fresh session
		// to switch to when it just deleted the last one.
		next, err := s.store.EnsureDefaultWebSession()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load sessions"})
			return
		}
		s.broadcastChanged("sessions_changed")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next": next})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

/* ---------------- protocols + schedules ---------------- */
func (s *Server) broadcastChanged(kind string) {
	s.hub.broadcast(map[string]any{"type": kind})
}

func (s *Server) handleProtocols(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		protocols, err := s.store.ListProtocols()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to list protocols"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"protocols": protocols})
	case http.MethodPost:
		var body struct {
			Title  string `json:"title"`
			Body   string `json:"body"`
			Origin string `json:"origin"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Title == "" || body.Body == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title and body are required"})
			return
		}
		slug := store.SlugifyProtocolTitle(body.Title)
		if len(slug) < 2 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title must contain letters or digits"})
			return
		}
		p, err := s.store.UpsertProtocol(store.Protocol{Slug: slug, Title: body.Title, Body: body.Body, Origin: body.Origin})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.broadcastChanged("protocols_changed")
		writeJSON(w, http.StatusCreated, map[string]any{"protocol": p})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) handleProtocolAction(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/web/api/protocols/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id is required"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Title == "" || body.Body == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title and body are required"})
			return
		}
		p, err := s.store.GetProtocolByID(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		p.Title = body.Title
		p.Body = body.Body
		p.Origin = "master"
		saved, err := s.store.UpsertProtocol(p)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.broadcastChanged("protocols_changed")
		writeJSON(w, http.StatusOK, map[string]any{"protocol": saved})
	case http.MethodDelete:
		if err := s.store.DeleteProtocol(id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		s.broadcastChanged("protocols_changed")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// resolveScheduleTiming turns schedule-builder fields (or a legacy raw cron
// spec) into the (spec, kind, runAt) triple for Scheduler.UpsertFull. Empty
// everything means "no timing change" — kind comes back "" and the scheduler
// keeps the existing timing via merge semantics.
func resolveScheduleTiming(kind, spec string, days []int, timeStr, runAtStr string) (string, string, *time.Time, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "" && kind != "recurring" && kind != "once" {
		return "", "", nil, fmt.Errorf("kind must be recurring or once")
	}
	if kind == "once" || (kind == "" && runAtStr != "" && spec == "" && timeStr == "" && days == nil) {
		if runAtStr == "" {
			return "", "", nil, fmt.Errorf("run date and time are required for a one-time schedule")
		}
		runAt, err := parseRunAt(runAtStr)
		if err != nil {
			return "", "", nil, err
		}
		return "", "once", &runAt, nil
	}
	if kind == "" && spec == "" && timeStr == "" && runAtStr == "" && days == nil {
		return "", "", nil, nil
	}
	if timeStr != "" || days != nil {
		built, err := buildCronFromDays(days, timeStr)
		if err != nil {
			return "", "", nil, err
		}
		return built, "recurring", nil, nil
	}
	if spec == "" {
		return "", "", nil, fmt.Errorf("time or cron spec is required")
	}
	return spec, "recurring", nil, nil
}

// buildCronFromDays builds "M H * * DOW" from weekday numbers (0=Sunday)
// and a "HH:MM" time. All seven days collapse to "*".
func buildCronFromDays(days []int, timeStr string) (string, error) {
	hour, min, err := parseTimeOfDay(timeStr)
	if err != nil {
		return "", err
	}
	if len(days) == 0 {
		return "", fmt.Errorf("pick at least one day")
	}
	seen := map[int]bool{}
	uniq := []int{}
	for _, d := range days {
		if d < 0 || d > 6 {
			return "", fmt.Errorf("invalid day %d: must be 0-6 (Sun-Sat)", d)
		}
		if !seen[d] {
			seen[d] = true
			uniq = append(uniq, d)
		}
	}
	sort.Ints(uniq)
	dow := "*"
	if len(uniq) < 7 {
		parts := make([]string, len(uniq))
		for i, d := range uniq {
			parts[i] = strconv.Itoa(d)
		}
		dow = strings.Join(parts, ",")
	}
	return fmt.Sprintf("%d %d * * %s", min, hour, dow), nil
}

// parseTimeOfDay parses "HH:MM" 24-hour time.
func parseTimeOfDay(s string) (hour, min int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("time must be HH:MM")
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("hour must be 00-23")
	}
	min, err = strconv.Atoi(parts[1])
	if err != nil || min < 0 || min > 59 {
		return 0, 0, fmt.Errorf("minute must be 00-59")
	}
	return hour, min, nil
}

// parseRunAt parses a one-time run moment: RFC3339 or the datetime-local
// "2006-01-02T15:04" form (interpreted in local time).
func parseRunAt(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("run date must be a valid date and time")
}

func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "scheduler not available"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, next, err := s.sched.List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to list schedules"})
			return
		}
		type scheduleView struct {
			store.Schedule
			NextRun *time.Time `json:"next_run,omitempty"`
		}
		views := []scheduleView{}
		for i := range rows {
			v := scheduleView{Schedule: rows[i]}
			if rows[i].Enabled && !next[i].IsZero() {
				t := next[i]
				v.NextRun = &t
			}
			views = append(views, v)
		}
		writeJSON(w, http.StatusOK, map[string]any{"schedules": views})
	case http.MethodPost:
		var body struct {
			Name    string `json:"name"`
			Task    string `json:"task"`
			Spec    string `json:"spec"`
			Kind    string `json:"kind"`
			Days    []int  `json:"days"`
			Time    string `json:"time"`
			RunAt   string `json:"run_at"`
			Enabled *bool  `json:"enabled"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name is required"})
			return
		}
		spec, kind, runAt, err := resolveScheduleTiming(body.Kind, body.Spec, body.Days, body.Time, body.RunAt)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		sc, err := s.sched.UpsertFull(body.Name, body.Task, spec, kind, runAt, body.Enabled)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.broadcastChanged("schedules_changed")
		writeJSON(w, http.StatusCreated, map[string]any{"schedule": sc})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) handleScheduleAction(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "scheduler not available"})
		return
	}
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/web/api/schedules/"))
	if err != nil || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name is required"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Task    string `json:"task"`
			Spec    string `json:"spec"`
			Kind    string `json:"kind"`
			Days    []int  `json:"days"`
			Time    string `json:"time"`
			RunAt   string `json:"run_at"`
			NewName string `json:"new_name"`
			Enabled *bool  `json:"enabled"`
		}
		if err := decodeBody(w, r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
			return
		}
		// Rename first so timing validation errors don't orphan a rename.
		target := name
		if body.NewName != "" && body.NewName != name {
			renamed, err := s.sched.Rename(name, body.NewName)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			target = renamed.Name
		}
		spec, kind, runAt, err := resolveScheduleTiming(body.Kind, body.Spec, body.Days, body.Time, body.RunAt)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		sc, err := s.sched.UpsertFull(target, body.Task, spec, kind, runAt, body.Enabled)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.broadcastChanged("schedules_changed")
		writeJSON(w, http.StatusOK, map[string]any{"schedule": sc})
	case http.MethodDelete:
		if err := s.sched.Delete(name); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		s.broadcastChanged("schedules_changed")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

/* ---------------- calendar tile ---------------- */

// handleCalendarEvents feeds the dashboard tile directly from the Mac
// bridge — no chat round-trip. Window: local midnight through +7 days.
func (s *Server) handleCalendarEvents(w http.ResponseWriter, r *http.Request) {
	if s.cal == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "calendar not configured (no Mac bridge)"})
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	now := time.Now()
	y, m, d := now.Date()
	from := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	events, err := s.cal.List(r.Context(), from, from.Add(7*24*time.Hour))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "calendar unavailable: " + err.Error()})
		return
	}
	if events == nil {
		events = []calendar.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleCalendarAdd creates an event straight from the tile form.
func (s *Server) handleCalendarAdd(w http.ResponseWriter, r *http.Request) {
	if s.cal == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "calendar not configured (no Mac bridge)"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Title    string `json:"title"`
		Start    string `json:"start"`
		End      string `json:"end"`
		Location string `json:"location"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.Title == "" || body.Start == "" || body.End == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title, start, and end are required"})
		return
	}
	start, err := time.Parse(time.RFC3339, body.Start)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid start time"})
		return
	}
	end, err := time.Parse(time.RFC3339, body.End)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid end time"})
		return
	}
	if !end.After(start) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "end must be after start"})
		return
	}
	id, err := s.cal.Create(r.Context(), calendar.Event{Title: body.Title, Start: start, End: end, Location: body.Location})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "calendar unavailable: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}
