// Package assistant implements clark's butler: settings, inner circle, and replies.
package assistant

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/tools"
)

//go:embed prompt.md
var promptTemplate string

// promptTmpl is the parsed prompt template. Personal details (the Master's
// name, the protocol name, exception visitors, ...) are injected per turn so
// the prompt itself carries no personal data.
var promptTmpl = template.Must(template.New("prompt").Parse(promptTemplate))

// maxToolRounds bounds how many model round-trips a single iteration may take.
const maxToolRounds = 8

// maxNudges bounds how many times the loop may push a narrating model to act.
const maxNudges = 2

// maxTurnDuration caps the wall-clock time of a single iteration so a stalled
// or looping model can never block the WhatsApp pipeline indefinitely.
const maxTurnDuration = 2 * time.Minute

// defaultHistoryLimit is how many recent messages are injected into each turn
// unless the Master (or clark) configures a different limit.
const defaultHistoryLimit = 10

// defaultVIPGrants are the tools every VIP may invoke without an explicit
// grant. web_search covers research; view_history lets a VIP recall their own
// conversation (and lets clark honour the history-first rule mid-chat).
var defaultVIPGrants = []string{"web_search", "view_history"}

// iterationLimitMessage is returned when a genuine tool chain exhausts its
// iteration budget while tools were still running.
const iterationLimitMessage = "I have reached the limit of tool calls for this iteration, Sir. Say _continue_ and I shall resume at once."

// couldNotActMessage is returned when the model repeatedly refuses to perform a
// demanded action; the request must be repeated rather than resumed.
const couldNotActMessage = "I beg your pardon, Sir, but I could not perform that action just now. Kindly _repeat your request_ and I shall try again at once."

// tooSlowMessage is returned when a turn exceeds maxTurnDuration.
func (s *Service) tooSlowMessage() string {
	return fmt.Sprintf("My apologies, Sir, but the %s lines are running slow tonight. Kindly _repeat your request_ and I shall try again with haste.", s.palaceName)
}

// nudgeMessage is the generic reminder for a narrating model that words alone
// do not perform actions.
const nudgeMessage = "You have not actually performed the requested action. Words alone do nothing: you MUST invoke the matching tool. Respond with ONLY a tool call now."

// Current-task directives selected per turn so the model greets only on a
// genuine first message and never recites status mid-conversation.
const (
	masterFirstTurnTask = "The Master has arrived. Greet him warmly as 'Sir' and await his command. Answer him directly — never announce your own status or availability to the Master himself."
	followUpTask        = "Continue the ongoing conversation naturally. Answer exactly what the person just said, relevantly and conversationally — never recite greetings, status, or protocol boilerplate when a direct answer is due."
)

// visitorFirstTurnTask names the palace via the persona config.
func (s *Service) visitorFirstTurnTask() string {
	return fmt.Sprintf("A Visitor has arrived. Welcome them briefly, acknowledge the Master's availability, and chat with them conversationally as yourself — no roleplay, no ritual greetings, no bows. Host them with the grace befitting the %s (Whatsapp).", s.palaceName)
}

// nudgeFor tailors the corrective message to the tool the model must invoke.
func nudgeFor(hint string) string {
	switch hint {
	case "send_message":
		return "You have not actually performed the requested action. Words alone do nothing: you MUST invoke the send_message tool now, with the recipient and the exact message text. Respond with ONLY the tool call."
	case "web_search":
		return "You have not actually answered the question. Words alone do nothing: you MUST invoke the web_search tool now, with a precise, specific query. Respond with ONLY the tool call."
	case "set_status", "set_context", "set_access", "get_state":
		return "You have not actually performed the requested action. Words alone do nothing: you MUST invoke the " + hint + " tool now, with the correct arguments. Respond with ONLY the tool call."
	case "add_vip or delete_vip":
		return "You have not actually performed the requested action. Words alone do nothing: you MUST invoke the add_vip or delete_vip tool now, with the correct arguments. Respond with ONLY the tool call."
	}
	return nudgeMessage
}

// LLM generates replies from a chat history, optionally with tools.
type LLM interface {
	Chat(ctx context.Context, messages []ollama.Message, tools []ollama.Tool) (*ollama.ChatResult, error)
	ChatStream(ctx context.Context, messages []ollama.Message, tools []ollama.Tool, fn func(token string)) (*ollama.ChatResult, error)
	SetThink(on bool)
}

// pendingIter is a paused tool-calling iteration awaiting the sender's "continue".
type pendingIter struct {
	senderJID string
	isSelf    bool
	messages  []ollama.Message
}

// Service is the assistant's orchestration layer. It satisfies whatsapp.Butler.
type Service struct {
	settings  store.Settings
	history   store.HistoryStore
	access    store.AccessStore
	vip       *VIP
	tools     *tools.Registry
	llm       LLM
	model     string
	name      string
	status    bool
	context   string
	think     bool
	alertMode string // "voice" (default) or "silent"

	historyLimit int

	// Persona, applied from config with butler-agnostic defaults.
	masterName        string
	protocolName      string
	palaceName        string
	bypassPhrase      string
	exceptionVisitors string

	pendingMu sync.Mutex
	pending   map[string]*pendingIter
}

// New loads persisted state and returns a ready Service.
func New(cfg *config.Config, st *store.Store, llm LLM) (*Service, error) {
	s := &Service{
		settings: st,
		history:  st,
		access:   st,
		vip:      NewVIP(st),
		tools:    tools.NewRegistry(),
		llm:      llm,
		model:    cfg.OllamaModel,
		pending:  make(map[string]*pendingIter),

		masterName:        orDefault(cfg.MasterName, "the Master"),
		protocolName:      orDefault(cfg.ProtocolName, "Butler"),
		palaceName:        orDefault(cfg.PalaceName, "Palace"),
		bypassPhrase:      orDefault(cfg.BypassPhrase, "get him to me"),
		exceptionVisitors: formatExceptionVisitors(cfg.InnerCircle),
	}

	if err := s.vip.Load(); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	s.registerManagementTools()
	return s, nil
}

func (s *Service) load() error {
	name, err := s.settings.Get("name")
	if err != nil {
		return err
	}

	ctxValue, err := s.settings.Get("context")
	if err != nil {
		return err
	}

	statusStr, err := s.settings.Get("status")
	if err != nil {
		return err
	}
	status := false
	if statusStr != "" {
		status, err = strconv.ParseBool(statusStr)
		if err != nil {
			return fmt.Errorf("Invalid status value Sir. Error: %w", err)
		}
	}

	thinkStr, err := s.settings.Get("think")
	if err != nil {
		return err
	}
	think := false
	if thinkStr != "" {
		think, err = strconv.ParseBool(thinkStr)
		if err != nil {
			return fmt.Errorf("Invalid thinking value Sir. Error: %w", err)
		}
	}

	historyLimit := defaultHistoryLimit
	limitStr, err := s.settings.Get("history_limit")
	if err != nil {
		return err
	}
	if limitStr != "" {
		limit, perr := strconv.Atoi(limitStr)
		if perr != nil || limit < 1 {
			return fmt.Errorf("Invalid history limit value Sir. Error: %w", perr)
		}
		historyLimit = limit
	}

	s.name = name
	s.context = ctxValue
	s.status = status
	s.think = think
	s.alertMode = "voice"
	if mode, err := s.settings.Get("alert_mode"); err == nil && mode != "" {
		if mode == "silent" || mode == "voice" {
			s.alertMode = mode
		}
	}
	s.historyLimit = historyLimit
	s.llm.SetThink(think)
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

// EnabledFor reports whether clark answers a given sender. A per-VIP status
// override wins; otherwise the global status applies.
func (s *Service) EnabledFor(jid string) bool {
	if on, ok := s.vip.IsEnabled(jid); ok {
		return on
	}
	return s.status
}

// Tools returns the shared tool registry so transports can register capabilities.
func (s *Service) Tools() *tools.Registry { return s.tools }

// Relation resolves a jid to its "Name (Relation)" label.
func (s *Service) Relation(jid string) (string, bool) {
	return s.vip.Check(jid)
}

// LookupJID resolves a name or number to a VIP jid for outbound messaging.
func (s *Service) LookupJID(input string) (string, bool) {
	return s.vip.Lookup(input)
}

// LookupIMessage resolves a name or number to a canonical identity for
// iMessage outbound delivery. VIPs are stored as phone JIDs, so today this is
// identical to the WhatsApp resolution; the seam stays separate so an
// email-handle VIP (no @s.whatsapp.net JID) can be resolved here later.
func (s *Service) LookupIMessage(input string) (string, bool) {
	return s.vip.Lookup(input)
}

// AddVIP parses and persists a "[number], [name], [relation]" entry.
func (s *Service) AddVIP(input string) error {
	return s.vip.Add(input)
}

// AddVIPBulk parses and persists several entries at once, all-or-nothing. It
// reuses the v3.1 bulk semantics by numbering each entry for the shared parser.
func (s *Service) AddVIPBulk(entries []string) error {
	var sb strings.Builder
	n := 0
	for _, e := range entries {
		if strings.TrimSpace(e) == "" {
			continue
		}
		n++
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "%d. %s", n, strings.TrimSpace(e))
	}
	if sb.Len() == 0 {
		return fmt.Errorf("no VIP entries to add")
	}
	return s.vip.Add(sb.String())
}

// DeleteVIP removes a VIP by number.
func (s *Service) DeleteVIP(input string) error {
	return s.vip.Delete(input)
}

// VIPList returns the current inner circle keyed by jid.
func (s *Service) VIPList() map[string]string {
	return s.vip.List()
}

// ClearVIPs empties the inner circle and its access grants, then reloads.
func (s *Service) ClearVIPs() error {
	if err := s.vip.Clear(); err != nil {
		return err
	}
	logging.Log("MEMORY", logging.SevInfo, "VIPCLEAR", "Inner circle emptied")
	return nil
}

// AccessFor returns a VIP's granted tools. A missing row defaults to web_search.
func (s *Service) AccessFor(jid string) ([]string, bool, error) {
	grants, ok, err := s.access.GetTools(jid)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return defaultVIPGrants, false, nil
	}
	return grants, true, nil
}

// AccessMap returns every VIP's granted tools keyed by jid.
func (s *Service) AccessMap() map[string][]string {
	out := make(map[string][]string, len(s.vip.List()))
	for jid := range s.vip.List() {
		grants, _, _ := s.AccessFor(jid)
		out[jid] = grants
	}
	return out
}

// SetAccess persists a VIP's granted tools.
func (s *Service) SetAccess(jid string, grants []string) error {
	return s.access.SetTools(jid, grants)
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

	return s.SetStatus(!statusBool)
}

// SetStatus enables or disables the assistant and persists the choice. Setting
// the global status resets every per-VIP override: a global command speaks for
// the whole butler, so personal carve-outs are wiped.
func (s *Service) SetStatus(on bool) error {
	if err := s.settings.Set("status", fmt.Sprintf("%v", on)); err != nil {
		return err
	}
	if err := s.vip.ClearAllEnabled(); err != nil {
		return err
	}

	s.status = on
	logging.Log("CLARK", logging.SevInfo, "STATUS", "Assistant status changed", "enabled", s.status)
	return nil
}

// SetVIPStatus sets a per-VIP status override for a named or numbered VIP.
// The global status still applies to everyone else.
func (s *Service) SetVIPStatus(recipient string, on bool) error {
	jid, ok := s.vip.Lookup(recipient)
	if !ok {
		return fmt.Errorf("no VIP found matching %q", recipient)
	}
	if err := s.vip.SetEnabled(jid, on); err != nil {
		return err
	}
	logging.Log("CLARK", logging.SevInfo, "STATUS", "VIP status changed", "jid", jid, "enabled", on)
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

// Thinking reports whether the model reasons before replying.
func (s *Service) Thinking() bool { return s.think }

// AlertMode reports the alert delivery mode: "voice" (speak alerts aloud) or
// "silent" (show via WhatsApp/iMessage/web + FaceTime/banner, no speech).
func (s *Service) AlertMode() string { return s.alertMode }

// SetAlertMode persists and applies the alert delivery mode.
func (s *Service) SetAlertMode(mode string) error {
	if mode != "voice" && mode != "silent" {
		return fmt.Errorf("alert mode must be voice or silent, Sir")
	}
	if err := s.settings.Set("alert_mode", mode); err != nil {
		return err
	}
	s.alertMode = mode
	logging.Log("CLARK", logging.SevInfo, "ALERT", "Alert mode changed", "mode", mode)
	return nil
}

// SetThinking enables or disables reasoning mode and persists the choice.
func (s *Service) SetThinking(on bool) error {
	if err := s.settings.Set("think", fmt.Sprintf("%v", on)); err != nil {
		return err
	}

	s.think = on
	s.llm.SetThink(on)
	logging.Log("CLARK", logging.SevInfo, "THINK", "Thinking mode changed", "enabled", on)
	return nil
}

// ToggleThinking flips reasoning mode.
func (s *Service) ToggleThinking() error {
	return s.SetThinking(!s.think)
}

// HistoryLimit reports how many recent messages are injected per turn.
func (s *Service) HistoryLimit() int { return s.historyLimit }

// SetHistoryLimit configures the per-turn history window and persists it.
func (s *Service) SetHistoryLimit(n int) error {
	if n < 1 {
		return fmt.Errorf("the history limit must be at least 1, Sir")
	}
	if err := s.settings.Set("history_limit", strconv.Itoa(n)); err != nil {
		return err
	}

	s.historyLimit = n
	logging.Log("CLARK", logging.SevInfo, "HISTORY", "History limit changed", "limit", n)
	return nil
}

// Reply produces an answer for a VIP's message, running any tool calls the
// model requests. isSelf marks the Master chatting in his own chat.
func (s *Service) Reply(ctx context.Context, senderJID, userMsg string, isSelf bool) (string, error) {
	content, _, err := s.reply(ctx, senderJID, userMsg, isSelf, true)
	return content, err
}

// ReplyLLM runs the full model pipeline and skips the deterministic fast
// path, so the web console's chat always gets a genuine AI reply with every
// tool available. Returns both the reply content and any reasoning text
// (empty when thinking mode is off).
func (s *Service) ReplyLLM(ctx context.Context, senderJID, userMsg string, isSelf bool) (string, string, error) {
	return s.reply(ctx, senderJID, userMsg, isSelf, false)
}

// ReplyLLMStream runs the full model pipeline with streaming tokens delivered
// via onToken as they arrive from Ollama. Tool calls are still executed
// synchronously between rounds. The final reply is saved to history.
func (s *Service) ReplyLLMStream(ctx context.Context, senderJID, userMsg string, isSelf bool, onToken func(string)) (string, string, error) {
	return s.replyStream(ctx, senderJID, userMsg, isSelf, onToken)
}

// reply implements both entry points. allowFastPath decides whether
// deterministic commands (views and Master-only mutations) are answered
// hardcoded; the web session sets it false so the model always runs.
func (s *Service) reply(ctx context.Context, senderJID, userMsg string, isSelf, allowFastPath bool) (string, string, error) {
	if senderJID == "" {
		return "", "", fmt.Errorf("empty sender JID")
	}

	_, isVIP := s.vip.Check(senderJID)
	if !isSelf && !isVIP {
		return "", "", fmt.Errorf("sender not in VIP list")
	}

	if userMsg == "" {
		return "", "", fmt.Errorf("empty message content")
	}

	// Let tools know which conversation triggered them.
	ctx = tools.WithSender(ctx, senderJID)

	if err := s.history.SaveMessage(senderJID, "user", userMsg); err != nil {
		return "", "", err
	}

	history, err := s.history.RecentMessages(senderJID, s.historyLimit)
	if err != nil {
		return "", "", err
	}

	if len(history) == 0 {
		return "", "", fmt.Errorf("no chat history available")
	}

	// Resume a paused iteration when the sender says "continue".
	if it := s.pendingIteration(senderJID); it != nil {
		if isContinueMsg(userMsg) {
			s.clearPending(senderJID)
			it.messages = append(it.messages, ollama.Message{Role: "user", Content: userMsg})
			reply, thinking, pending, err := s.runToolLoop(ctx, it.messages, userMsg, s.toolsForSender(senderJID, it.isSelf), it.isSelf)
			if err != nil {
				return "", thinking, s.handleModelError(err)
			}
			if pending != nil {
				s.setPending(senderJID, &pendingIter{senderJID: senderJID, isSelf: it.isSelf, messages: pending})
				reply = iterationLimitMessage
			}
			saved, err := s.saveReply(senderJID, reply)
			return saved, thinking, err
		}
		s.clearPending(senderJID)
	}

	// Fast path: deterministic commands (views, and Master-only mutations of
	// status, context, the inner circle, and tool access) are answered with
	// hardcoded messages instead of a model round-trip. The web session
	// bypasses it so every console turn is a genuine AI reply.
	if allowFastPath {
		if reply, handled, err := s.fastPath(senderJID, userMsg, isSelf); err != nil {
			return "", "", err
		} else if handled {
			saved, err := s.saveReply(senderJID, reply)
			return saved, "", err
		}
	}

	available := s.toolsForSender(senderJID, isSelf)
	relation, _ := s.vip.Check(senderJID)

	// Greet only on a genuine first message; a follow-up must stay on-topic.
	task := followUpTask
	if len(history) == 1 {
		if isSelf {
			task = masterFirstTurnTask
		} else {
			task = s.visitorFirstTurnTask()
		}
	}

	systemPrompt, err := s.renderPrompt(s.name, s.context, statusLabel(s.EnabledFor(senderJID)), relation, describeTools(available), task)
	if err != nil {
		return "", "", err
	}

	messages := make([]ollama.Message, 0, len(history)+1)
	messages = append(messages, ollama.Message{Role: "system", Content: systemPrompt})
	for _, m := range history {
		messages = append(messages, ollama.Message{Role: m.Role, Content: m.Content})
	}

	logging.Log("OLLAMA", logging.SevInfo, "REQUEST", "Generating response", "model", s.model)
	start := time.Now()

	reply, thinking, pending, err := s.runToolLoop(ctx, messages, userMsg, available, isSelf)
	if err != nil {
		return "", thinking, fmt.Errorf("failed to execute model: %w", s.handleModelError(err))
	}

	logging.Log("OLLAMA", logging.SevInfo, "RESPONSE", "Generation completed",
		"model", s.model,
		"duration", time.Since(start).Round(time.Millisecond))

	if pending != nil {
		s.setPending(senderJID, &pendingIter{senderJID: senderJID, isSelf: isSelf, messages: pending})
		reply = iterationLimitMessage
	}

	saved, err := s.saveReply(senderJID, reply)
	return saved, thinking, err
}

// replyStream is like reply but streams tokens via onToken during the final
// LLM round. Tool call rounds still use non-streaming Chat().
func (s *Service) replyStream(ctx context.Context, senderJID, userMsg string, isSelf bool, onToken func(string)) (string, string, error) {
	if senderJID == "" {
		return "", "", fmt.Errorf("empty sender JID")
	}
	_, isVIP := s.vip.Check(senderJID)
	if !isSelf && !isVIP {
		return "", "", fmt.Errorf("sender not in VIP list")
	}
	if userMsg == "" {
		return "", "", fmt.Errorf("empty message content")
	}
	ctx = tools.WithSender(ctx, senderJID)
	if err := s.history.SaveMessage(senderJID, "user", userMsg); err != nil {
		return "", "", err
	}
	history, err := s.history.RecentMessages(senderJID, s.historyLimit)
	if err != nil {
		return "", "", err
	}
	if len(history) == 0 {
		return "", "", fmt.Errorf("no chat history available")
	}

	if it := s.pendingIteration(senderJID); it != nil {
		if isContinueMsg(userMsg) {
			s.clearPending(senderJID)
			it.messages = append(it.messages, ollama.Message{Role: "user", Content: userMsg})
			reply, thinking, pending, err := s.runToolLoopStream(ctx, it.messages, userMsg, s.toolsForSender(senderJID, it.isSelf), it.isSelf, onToken)
			if err != nil {
				return "", thinking, s.handleModelError(err)
			}
			if pending != nil {
				s.setPending(senderJID, &pendingIter{senderJID: senderJID, isSelf: it.isSelf, messages: pending})
				reply = iterationLimitMessage
			}
			saved, err := s.saveReply(senderJID, reply)
			return saved, thinking, err
		}
		s.clearPending(senderJID)
	}

	available := s.toolsForSender(senderJID, isSelf)
	relation, _ := s.vip.Check(senderJID)
	task := followUpTask
	if len(history) == 1 {
		if isSelf {
			task = masterFirstTurnTask
		} else {
			task = s.visitorFirstTurnTask()
		}
	}
	systemPrompt, err := s.renderPrompt(s.name, s.context, statusLabel(s.EnabledFor(senderJID)), relation, describeTools(available), task)
	if err != nil {
		return "", "", err
	}
	messages := make([]ollama.Message, 0, len(history)+1)
	messages = append(messages, ollama.Message{Role: "system", Content: systemPrompt})
	for _, m := range history {
		messages = append(messages, ollama.Message{Role: m.Role, Content: m.Content})
	}

	logging.Log("OLLAMA", logging.SevInfo, "REQUEST", "Generating response (streaming)", "model", s.model)
	start := time.Now()

	reply, thinking, pending, err := s.runToolLoopStream(ctx, messages, userMsg, available, isSelf, onToken)
	if err != nil {
		return "", thinking, fmt.Errorf("failed to execute model: %w", s.handleModelError(err))
	}

	logging.Log("OLLAMA", logging.SevInfo, "RESPONSE", "Generation completed (streaming)",
		"model", s.model,
		"duration", time.Since(start).Round(time.Millisecond))

	if pending != nil {
		s.setPending(senderJID, &pendingIter{senderJID: senderJID, isSelf: isSelf, messages: pending})
		reply = iterationLimitMessage
	}
	saved, err := s.saveReply(senderJID, reply)
	return saved, thinking, err
}

// runToolLoopStream is like runToolLoop but uses ChatStream for the final
// round (when no more tool calls are expected) to deliver tokens via onToken.
func (s *Service) runToolLoopStream(ctx context.Context, messages []ollama.Message, userMsg string, available []tools.Tool, isSelf bool, onToken func(string)) (string, string, []ollama.Message, error) {
	if isSelf {
		ctx = tools.WithMaster(ctx)
	}
	loopCtx, cancel := context.WithTimeout(ctx, maxTurnDuration)
	defer cancel()

	requestTools := toOllamaTools(available)
	ranTools := make(map[string]bool)
	nudges := 0
	var lastThinking string
	for round := 0; round < maxToolRounds; round++ {
		// Use streaming when onToken is provided so tokens are always delivered
		// to the browser regardless of which round produces the final content.
		// ChatStream handles tool calls correctly (they arrive in the final chunk).
		var result *ollama.ChatResult
		var err error
		if onToken != nil {
			result, err = s.llm.ChatStream(loopCtx, messages, requestTools, onToken)
		} else {
			result, err = s.llm.Chat(loopCtx, messages, requestTools)
		}
		if err != nil {
			if loopCtx.Err() == context.DeadlineExceeded {
				return s.tooSlowMessage(), lastThinking, nil, nil
			}
			if round == 0 {
				return "", lastThinking, nil, err
			}
			return "I beg your pardon, Sir, but something interrupted my train of thought. Pray _continue_ and I shall resume my duties at once.", lastThinking, nil, nil
		}
		if result.Thinking != "" {
			lastThinking = result.Thinking
		}
		if len(result.ToolCalls) == 0 {
			needed, hint := s.needsAction(userMsg, result.Content, available)
			if !needed || hintSatisfied(hint, ranTools) {
				return result.Content, lastThinking, nil, nil
			}
			if nudges >= maxNudges {
				return couldNotActMessage, lastThinking, nil, nil
			}
			nudges++
			messages = append(messages, ollama.Message{Role: "assistant", Content: result.Content})
			messages = append(messages, ollama.Message{Role: "system", Content: nudgeFor(hint)})
			continue
		}
		messages = append(messages, ollama.Message{Role: "assistant", Content: result.Content, ToolCalls: result.ToolCalls})
		for _, tc := range result.ToolCalls {
			logging.Log("TOOLS", logging.SevInfo, "TRIGGER", "Tool invoked", "tool", tc.Function.Name, "args", compactArgs(tc.Function.Arguments))
			out, err := s.tools.Execute(loopCtx, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				out = "Error: " + err.Error()
				logging.Log("TOOLS", logging.SevWarn, "TRIGGER", "Tool failed", "tool", tc.Function.Name, "error", err.Error())
			} else {
				preview := out
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				logging.Log("TOOLS", logging.SevInfo, "TRIGGER", "Tool result", "tool", tc.Function.Name, "result", preview)
			}
			ranTools[tc.Function.Name] = true
			messages = append(messages, ollama.Message{Role: "tool", Content: out})
		}
	}
	return "", lastThinking, messages, nil
}

// saveReply persists an assistant reply and returns it.
func (s *Service) saveReply(senderJID, reply string) (string, error) {
	if err := s.history.SaveMessage(senderJID, "assistant", reply); err != nil {
		return "", err
	}
	return reply, nil
}

// promptData is the per-turn payload injected into the prompt template.
type promptData struct {
	ButlerName        string
	MasterName        string
	MasterStatus      string
	ButlerStatus      string
	InnerCircle       string
	Visitor           string
	Tools             string
	Task              string
	ProtocolName      string
	PalaceName        string
	BypassPhrase      string
	ExceptionVisitors string
}

// handleModelError reacts to a failed model call. A rate limit immediately
// switches clark off (persisted, per-VIP overrides wiped) and is surfaced as
// ollama.ErrRateLimited so the transport can alert the Master. Any other
// failure passes through untouched.
func (s *Service) handleModelError(err error) error {
	if errors.Is(err, ollama.ErrRateLimited) {
		if serr := s.SetStatus(false); serr != nil {
			logging.Log("OLLAMA", logging.SevErr, "RATELIMIT", "Failed to switch clark off after rate limit", "error", serr)
		}
		logging.Log("OLLAMA", logging.SevErr, "RATELIMIT", "Model rate limited; clark switched off", "error", err)
		return fmt.Errorf("%w: %s", ollama.ErrRateLimited, err.Error())
	}
	return err
}

// renderPrompt fills the prompt template with the current turn's context.
// butlerStatus is the effective status for this sender (override or global).
func (s *Service) renderPrompt(name, masterStatus, butlerStatus, visitor, toolsList, task string) (string, error) {
	data := promptData{
		ButlerName:        name,
		MasterName:        s.masterName,
		MasterStatus:      masterStatus,
		ButlerStatus:      butlerStatus,
		InnerCircle:       s.vip.list(),
		Visitor:           visitor,
		Tools:             toolsList,
		Task:              task,
		ProtocolName:      s.protocolName,
		PalaceName:        s.palaceName,
		BypassPhrase:      s.bypassPhrase,
		ExceptionVisitors: s.exceptionVisitors,
	}
	var buf strings.Builder
	if err := promptTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render prompt: %w", err)
	}
	return buf.String(), nil
}

// orDefault returns def when v is empty.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// formatExceptionVisitors renders the configured dearest persons as a comma
// separated "Name (Relation)" list, or "" when none are configured.
func formatExceptionVisitors(people []config.Person) string {
	if len(people) == 0 {
		return ""
	}
	parts := make([]string, 0, len(people))
	for _, p := range people {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		if rel := strings.TrimSpace(p.Relation); rel != "" {
			parts = append(parts, name+" ("+rel+")")
		} else {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ")
}

// runToolLoop drives the model: ask, run tool calls, and repeat up to
// maxToolRounds until a plain reply is produced. Returns the content, the
// accumulated reasoning text, messages for possible continuation, and error.
func (s *Service) runToolLoop(ctx context.Context, messages []ollama.Message, userMsg string, available []tools.Tool, isSelf bool) (string, string, []ollama.Message, error) {
	if isSelf {
		ctx = tools.WithMaster(ctx)
	}

	loopCtx, cancel := context.WithTimeout(ctx, maxTurnDuration)
	defer cancel()

	requestTools := toOllamaTools(available)
	ranTools := make(map[string]bool)
	nudges := 0
	var lastThinking string
	for round := 0; round < maxToolRounds; round++ {
		result, err := s.llm.Chat(loopCtx, messages, requestTools)
		if err != nil {
			if loopCtx.Err() == context.DeadlineExceeded {
				return s.tooSlowMessage(), lastThinking, nil, nil
			}
			if round == 0 {
				return "", lastThinking, nil, err
			}
			return "I beg your pardon, Sir, but something interrupted my train of thought. Pray _continue_ and I shall resume my duties at once.", lastThinking, nil, nil
		}

		if result.Thinking != "" {
			lastThinking = result.Thinking
		}

		if len(result.ToolCalls) == 0 {
			needed, hint := s.needsAction(userMsg, result.Content, available)
			if !needed || hintSatisfied(hint, ranTools) {
				return result.Content, lastThinking, nil, nil
			}
			if nudges >= maxNudges {
				return couldNotActMessage, lastThinking, nil, nil
			}
			nudges++
			messages = append(messages, ollama.Message{Role: "assistant", Content: result.Content})
			messages = append(messages, ollama.Message{Role: "system", Content: nudgeFor(hint)})
			continue
		}

		messages = append(messages, ollama.Message{Role: "assistant", Content: result.Content, ToolCalls: result.ToolCalls})
		for _, tc := range result.ToolCalls {
			logging.Log("TOOLS", logging.SevInfo, "TRIGGER", "Tool invoked", "tool", tc.Function.Name, "args", compactArgs(tc.Function.Arguments))
			out, err := s.tools.Execute(loopCtx, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				out = "Error: " + err.Error()
				logging.Log("TOOLS", logging.SevWarn, "TRIGGER", "Tool failed", "tool", tc.Function.Name, "error", err.Error())
			} else {
				preview := out
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				logging.Log("TOOLS", logging.SevInfo, "TRIGGER", "Tool result", "tool", tc.Function.Name, "result", preview)
			}
			ranTools[tc.Function.Name] = true
			messages = append(messages, ollama.Message{Role: "tool", Content: out})
		}
	}

	return "", lastThinking, messages, nil
}

// needsAction reports whether the model must be pushed to invoke a tool for
// this turn, and returns a hint naming the tool it should call. It fires when
// (a) the reply merely claims a send/manage action that was not performed,
// (b) the sender explicitly demands a send/manage action, or (c) a research
// request was met with a refusal rather than an answer. Claims and demands are
// only honored for tools the sender may actually invoke. Questions asking the
// user for more information are never nudged — the model is allowed to seek
// clarification.
func (s *Service) needsAction(userMsg, reply string, available []tools.Tool) (bool, string) {
	hasTool := func(names ...string) bool {
		for _, t := range available {
			for _, n := range names {
				if t.Definition.Name == n {
					return true
				}
			}
		}
		return false
	}

	// If the model is asking the user a question (seeking clarification),
	// never force a tool call — the model is legitimately requesting info.
	replyLow := strings.ToLower(reply)
	if isQuestion(replyLow) {
		return false, ""
	}

	// A reply that merely claims a message was sent or delivered. The claim is
	// satisfied by either the WhatsApp send_message or the iMessage
	// send_imessage tool — whichever transport the model actually used.
	if (hasTool("send_message") || hasTool("send_imessage")) && hasAny(reply,
		"shall send", "will send", "am sending", "is sending", "sending the",
		"sending it", "sending that", "has been sent", "have sent", "had sent",
		"sent to", "sent it", "sent the", "sent via", "sent a message",
		"sent him", "sent her", "sent your", "sent the message", "delivered",
		"deliver the", "drafted", "crafted", "has been informed", "notified",
		"pinged", "relayed", "forwarded") {
		return true, "send_message"
	}

	// A reply that merely claims a setting or ledger was changed.
	if hasTool("set_status", "set_context", "add_vip", "delete_vip", "set_access", "get_state") && hasAny(reply,
		"have set", "has been set", "have turned", "has been turned", "turned clark",
		"have silenced", "has been silenced", "is now silenced", "now silenced",
		"status to off", "status to on", "set clark's status", "operational status off",
		"operational status on", "set the status", "have added", "have deleted",
		"has been added", "has been removed", "has been deleted", "have updated",
		"updated your", "changed your", "have woken", "woken", "activated",
		"deactivated", "have granted", "have revoked", "granted", "revoked",
		"welcomed", "admitted") {
		return true, manageHint(userMsg)
	}

	// The sender explicitly demanded a message be sent.
	if hasTool("send_message") {
		if hasAny(userMsg,
			"send it", "send now", "send via whatsapp", "send the message",
			"send this message", "send that message", "send the", "forward this",
			"send immediately", "send it now", "send it immediately") {
			return true, "send_message"
		}
		if hasAny(userMsg, "send", "message", "tell ", "tell her", "tell him",
			"introduce", "introduction", "whatsapp", "text ", "notify ", "ping",
			"remind", "relay", "forward", "message ", "let him know", "let her know") &&
			s.hasVIPTarget(userMsg) {
			return true, "send_message"
		}
	}

	// The sender explicitly demanded a management action.
	if hasTool("set_status", "set_context", "add_vip", "delete_vip", "set_access", "get_state") &&
		hasAny(userMsg,
			"set my status", "set the status", "set status", "status off", "status on",
			"turn me on", "turn me off", "turn on", "turn off", "turn clark",
			"toggle", "update my context", "update context", "set context", "my context",
			"add a vip", "add vip", "new vip", "delete vip", "remove vip", "remove a vip",
			"delete the vip", "change my status", "change my context", "update my status",
			"change my availability", "set my availability", "operational status",
			"wake", "silence", "silence clark", "go offline", "go online",
			"wake up clark", "wake him", "wake her", "silence him", "silence her",
			"activate", "deactivate", "sleep mode", "inner circle", "add to the inner circle",
			"remove from the inner circle", "grant", "revoke", "remember that",
			"remember this", "note that", "note this", "add a member", "new member",
			"remove a member") {
		return true, manageHint(userMsg)
	}

	// Research is only pushed when the model refused to answer; a genuine
	// content answer is always trusted over a forced web search.
	if hasTool("web_search") &&
		hasAny(userMsg,
			"current", "latest", "today", "price", "rate", "news", "weather",
			"forecast", "search", "how much", "how many", "exchange", "stock",
			"the price of", "what is the current", "google", "look up", "find out",
			"how is", "what's the", "who won", "score", "latest on", "current news") &&
		isRefusal(reply) {
		return true, "web_search"
	}

	return false, ""
}

// manageHint guesses the management tool a demand refers to.
func manageHint(userMsg string) string {
	m := strings.ToLower(userMsg)
	switch {
	case hasAny(m, "status", "toggle", "turn", "wake", "silence", "switch", "power", "shut",
		"activate", "deactivate", "sleep mode", "online", "offline"):
		return "set_status"
	case hasAny(m, "context", "remember", "note that", "note this", "save this"):
		return "set_context"
	case hasAny(m, "vip", "inner circle", "add a", "remove", "delete", "welcome", "admit", "member"):
		return "add_vip or delete_vip"
	case hasAny(m, "access", "grant", "revoke", "allow", "block"):
		return "set_access"
	}
	return "the appropriate management tool"
}

// hintSatisfied reports whether the tool category a hint names already ran in
// this iteration, in which case a follow-up summary should not be nudged.
func hintSatisfied(hint string, ran map[string]bool) bool {
	switch hint {
	case "send_message":
		return ran["send_message"] || ran["send_imessage"]
	case "web_search":
		return ran["web_search"]
	case "set_status":
		return ran["set_status"]
	case "set_context":
		return ran["set_context"]
	case "add_vip or delete_vip":
		return ran["add_vip"] || ran["delete_vip"]
	case "set_access":
		return ran["set_access"]
	case "get_state":
		return ran["get_state"]
	default:
		for _, n := range []string{"set_status", "set_context", "add_vip", "delete_vip", "set_access", "get_state"} {
			if ran[n] {
				return true
			}
		}
	}
	return false
}

// isRefusal reports whether a reply declined to answer rather than answering.
func isRefusal(s string) bool {
	return hasAny(s,
		"unable", "cannot", "can't", "can not", "not able", "no access",
		"don't have", "do not have", "without context", "insufficient",
		"unfortunately", "no information", "don't know", "do not know",
		"not sure", "i am not able", "lack the")
}

// isQuestion reports whether the reply is asking the user for more information
// rather than claiming to have performed an action. When the model is seeking
// clarification (missing tool arguments, etc.), the nudge system must not
// override the response.
func isQuestion(reply string) bool {
	return hasAny(reply,
		"please provide", "kindly provide", "could you provide",
		"what is", "what are", "what's", "who is", "who are",
		"how many", "how much", "which", "where is",
		"please share", "could you share", "can you share",
		"awaiting the details", "awaiting your", "awaiting",
		"i need", "i require", "i am missing",
		"shall i", "should i", "would you like",
		"clarify", "elaborate", "could you specify",
		"number, name, and relation",
		"number and name", "name and number",
		"which vip", "which contact")
}

func (s *Service) hasVIPTarget(userMsg string) bool {
	msg := strings.ToLower(userMsg)
	for name := range s.vip.byName {
		if name != "" && strings.Contains(msg, name) {
			return true
		}
	}
	for _, w := range []string{"darling", "girlfriend", "best friend", "bestfriend",
		"mother", "father", "mom", "dad", "him", "her"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

func hasAny(s string, subs ...string) bool {
	l := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// statusLabel renders clark's operational state for the prompt.
func statusLabel(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

type viewKind int

const (
	viewNone viewKind = iota
	viewTools
	viewVIPs
	viewAll
)

// viewRequest detects requests for the hardcoded reports.
func (s *Service) viewRequest(userMsg string) viewKind {
	m := strings.ToLower(userMsg)
	switch {
	case hasAny(m, "show me everything", "all information", "all info", "full report",
		"view all", "complete state", "full state", "full overview", "give me the overview",
		"your state", "what is your state", "show your state", "report your state",
		"everything about you", "view everything", "the whole picture",
		"all the information", "show the full report", "complete overview"):
		return viewAll
	case hasAny(m, "list of tools", "list tools", "available tools", "your tools",
		"what tools", "which tools", "tools you have", "tool list", "list of your tools",
		"show me your tools", "show your tools", "what can you do", "your capabilities",
		"capabilities", "list the tools", "what tools do you have", "what can you do for me"):
		return viewTools
	case hasAny(m, "list of vip", "list vip", "vip list", "list of your vip",
		"inner circle", "who are the vip", "who is in the inner circle", "list the vip",
		"show the vip", "show me the vip", "my vip list", "list my vip",
		"who can contact me", "list of the inner circle", "show me the inner circle"):
		return viewVIPs
	}
	return viewNone
}

// renderView builds a deterministic report for the given view kind.
func (s *Service) renderView(kind viewKind, senderJID string, isSelf bool) (string, error) {
	switch kind {
	case viewAll:
		if !isSelf {
			return "That report is reserved for the *Master* alone, I am afraid.", nil
		}
		return s.viewAllText(), nil
	case viewVIPs:
		if !isSelf {
			return "The inner circle is reserved for the *Master* alone, I am afraid.", nil
		}
		return s.viewVIPsText(), nil
	case viewTools:
		if isSelf {
			return formatTools(s.tools.List()), nil
		}
		grants, _, err := s.AccessFor(senderJID)
		if err != nil {
			return "", err
		}
		granted := make(map[string]bool, len(grants))
		for _, g := range grants {
			granted[g] = true
		}
		var list []tools.Tool
		for _, t := range s.tools.List() {
			if granted[t.Definition.Name] {
				list = append(list, t)
			}
		}
		return formatTools(list), nil
	}
	return "", fmt.Errorf("unknown view %d", kind)
}

func formatTools(ts []tools.Tool) string {
	if len(ts) == 0 {
		return "*Tools available to you:* _None._"
	}
	lines := make([]string, 0, len(ts))
	for _, t := range ts {
		lines = append(lines, "- `"+t.Definition.Name+"`: "+t.Definition.Description)
	}
	return "*Tools available to you:*\n" + joinLines(lines)
}

func (s *Service) viewAllText() string {
	var b strings.Builder
	b.WriteString("*" + s.name + " — Full Report*\n\n")
	if s.status {
		b.WriteString("*Status:* On\n")
	} else {
		b.WriteString("*Status:* Off\n")
	}
	if s.think {
		b.WriteString("*Thinking:* On\n")
	} else {
		b.WriteString("*Thinking:* Off\n")
	}
	b.WriteString("*History:* " + strconv.Itoa(s.historyLimit) + " messages per turn\n")
	b.WriteString("*Context:* " + s.context + "\n\n")
	b.WriteString(s.viewVIPsText())
	b.WriteString("\n\n")
	b.WriteString(formatTools(s.tools.List()))
	return b.String()
}

func (s *Service) viewVIPsText() string {
	vips := s.vip.List()
	var b strings.Builder
	b.WriteString("*Inner Circle*\n")
	if len(vips) == 0 {
		b.WriteString("- _None_")
		return b.String()
	}
	jids := make([]string, 0, len(vips))
	for jid := range vips {
		jids = append(jids, jid)
	}
	sort.Strings(jids)
	for _, jid := range jids {
		grants, _, _ := s.AccessFor(jid)
		toolsList := "—"
		if len(grants) > 0 {
			toolsList = strings.Join(grants, ", ")
		}
		state := statusLabel(s.EnabledFor(jid))
		if _, hasOverride := s.vip.IsEnabled(jid); hasOverride {
			state += " (personal)"
		} else {
			state += " (default)"
		}
		b.WriteString("- " + vips[jid] + " (`" + jid + "`) · *" + state + "* — `" + toolsList + "`\n")
	}
	return b.String()
}

// pendingIteration returns the paused iteration for a sender, if any.
func (s *Service) pendingIteration(senderJID string) *pendingIter {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	return s.pending[senderJID]
}

// setPending stores a paused iteration for a sender.
func (s *Service) setPending(senderJID string, it *pendingIter) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pending[senderJID] = it
}

// clearPending discards any paused iteration for a sender.
func (s *Service) clearPending(senderJID string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	delete(s.pending, senderJID)
}

// isContinueMsg reports whether a message asks to resume a paused iteration.
func isContinueMsg(userMsg string) bool {
	m := strings.ToLower(strings.TrimSpace(userMsg))
	return strings.Contains(m, "continue") ||
		strings.Contains(m, "keep going") ||
		strings.Contains(m, "go on") ||
		strings.Contains(m, "resume") ||
		strings.Contains(m, "next iteration") ||
		strings.Contains(m, "second iteration")
}

// toolsForSender returns the tools a sender may invoke. The Master gets
// everything; VIPs only the tools their access grants allow.
func (s *Service) toolsForSender(senderJID string, isSelf bool) []tools.Tool {
	all := s.tools.List()
	if isSelf {
		return all
	}

	grants, ok, err := s.access.GetTools(senderJID)
	if err != nil || !ok {
		grants = defaultVIPGrants
	}

	granted := make(map[string]bool, len(grants))
	for _, g := range grants {
		granted[g] = true
	}

	out := make([]tools.Tool, 0, len(grants))
	for _, t := range all {
		if granted[t.Definition.Name] {
			out = append(out, t)
		}
	}
	return out
}

// describeTools renders the available tools for the system prompt.
func describeTools(available []tools.Tool) string {
	if len(available) == 0 {
		return "None."
	}
	lines := make([]string, 0, len(available))
	for _, t := range available {
		lines = append(lines, "- "+t.Definition.Name+": "+t.Definition.Description)
	}
	return "You may use:\n" + joinLines(lines)
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

func toOllamaTools(ts []tools.Tool) []ollama.Tool {
	out := make([]ollama.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, ollama.Tool{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        t.Definition.Name,
				Description: t.Definition.Description,
				Parameters:  t.Definition.Parameters,
			},
		})
	}
	return out
}

// compactArgs renders a tool's JSON arguments for the log line, truncated.
func compactArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	s := string(args)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
