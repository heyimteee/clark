package assistant

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Hardcoded, fast mutation confirmations. These use WhatsApp rich text:
// *bold*, _italic_, ~strikethrough~, `inline code`, ```monospace```,
// "> quote" and "* "-prefixed bulleted lines.
const (
	contextUpdatedHeader = "*Context Updated*"

	vipAddedHeader    = "*Inner Circle Updated*"
	vipDeletedReply   = "*Inner Circle Updated*\n\nThe entry has been struck from the ledger, Sir."
	clearContextReply = "*Context Cleared*\n\nMaster's context has been emptied, Sir."
	clearVIPsReply    = "*Inner Circle Cleared*\n\nThe ledger is empty, Sir. Every entry has been struck, Sir."
	commandErrorTmpl  = "*Command Not Performed*\n\n_%s_"
)

func (s *Service) statusOnReply() string {
	return "*Status Updated*\n\n" + s.name + " is now *On* and at your service, Sir."
}

func (s *Service) statusOffReply() string {
	return "*Status Updated*\n\n" + s.name + " is now *Off*, Sir. He will still answer you here."
}

func (s *Service) perVIPStatusOnReply(recipient string) string {
	return "*Status Updated*\n\n_" + recipient + "_ may now reach me personally, Sir."
}

func (s *Service) perVIPStatusOffReply(recipient string) string {
	return "*Status Updated*\n\n_" + recipient + "_ has been personally silenced, Sir."
}

// prehandleCommand handles deterministic commands that the LLM does not
// know about (thinking mode, history-limit). It runs even in the web
// session where the full fast-path is bypassed.
func (s *Service) prehandleCommand(userMsg string) (string, bool, error) {
	switch {
	case isThinkingCommand(userMsg):
		on, toggle, _ := parseThinkingCommand(userMsg)
		if toggle {
			if err := s.ToggleThinking(); err != nil {
				return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
			}
		} else if err := s.SetThinking(on); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return thinkingReply(s.Thinking()), true, nil

	case isHistoryLimitCommand(userMsg):
		limit, _ := parseHistoryLimitCommand(userMsg)
		if err := s.SetHistoryLimit(limit); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return historyLimitReply(limit), true, nil
	}
	return "", false, nil
}

// fastPath handles deterministic commands synchronously: hardcoded views and,
// for the Master only, mutations of status, context, the inner circle, and
// tool access. Returns a ready-to-send message and true when the request was
// consumed without needing the model.
func (s *Service) fastPath(senderJID, userMsg string, isSelf bool) (string, bool, error) {
	if view := s.viewRequest(userMsg); view != viewNone {
		text, err := s.renderView(view, senderJID, isSelf)
		if err != nil {
			return "", false, err
		}
		return text, true, nil
	}

	if !isSelf {
		return "", false, nil
	}

	switch {
	case isThinkingCommand(userMsg):
		on, toggle, _ := parseThinkingCommand(userMsg)
		if toggle {
			if err := s.ToggleThinking(); err != nil {
				return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
			}
		} else if err := s.SetThinking(on); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return thinkingReply(s.Thinking()), true, nil

	case isHistoryLimitCommand(userMsg):
		limit, _ := parseHistoryLimitCommand(userMsg)
		if err := s.SetHistoryLimit(limit); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return historyLimitReply(limit), true, nil

	case s.isPerVIPStatusCommand(userMsg):
		recipient, on, everyone, _ := s.parsePerVIPStatusCommand(userMsg)
		if everyone {
			if err := s.SetStatus(on); err != nil {
				return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
			}
			if on {
				return s.statusOnReply(), true, nil
			}
			return s.statusOffReply(), true, nil
		}
		if err := s.SetVIPStatus(recipient, on); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		if on {
			return s.perVIPStatusOnReply(recipient), true, nil
		}
		return s.perVIPStatusOffReply(recipient), true, nil

	case isStatusCommand(userMsg):
		if err := s.applyStatusCommand(userMsg); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		if s.status {
			return s.statusOnReply(), true, nil
		}
		return s.statusOffReply(), true, nil

	case isContextCommand(userMsg):
		text, _ := parseContextCommand(userMsg)
		if err := s.SetContext(text); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return contextUpdatedReply(text), true, nil

	case isClearContextCommand(userMsg):
		if err := s.SetContext(""); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return clearContextReply, true, nil

	case isAddVIPCommand(userMsg):
		payload, _ := parseAddVIPCommand(userMsg)
		if err := s.AddVIP(payload); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return vipAddedReply(payload), true, nil

	case isDeleteVIPCommand(userMsg):
		payload, _ := parseDeleteVIPCommand(userMsg)
		if err := s.deleteVIP(payload); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return vipDeletedReply, true, nil

	case isClearVIPsCommand(userMsg):
		if err := s.ClearVIPs(); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return clearVIPsReply, true, nil

	case isGuidanceCommand(userMsg):
		return s.guidanceText(), true, nil

	case isAccessCommand(userMsg):
		recipient, tool, enabled, _ := parseAccessCommand(userMsg)
		if err := s.mutateAccess(recipient, tool, enabled); err != nil {
			return fmt.Sprintf(commandErrorTmpl, err.Error()), true, nil
		}
		return accessReply(recipient, tool, enabled), true, nil
	}

	return "", false, nil
}

// Prehandle implements whatsapp.Butler: it consumes fast deterministic
// commands and persists them to history like a normal Reply would.
func (s *Service) Prehandle(senderJID, userMsg string, isSelf bool) (string, bool, error) {
	reply, ok, err := s.fastPath(senderJID, userMsg, isSelf)
	if err != nil || !ok {
		return reply, ok, err
	}
	if err := s.history.SaveMessage(senderJID, "user", userMsg); err != nil {
		return "", false, err
	}
	if _, err := s.saveReply(senderJID, reply); err != nil {
		return "", false, err
	}
	return reply, true, nil
}

func (s *Service) applyStatusCommand(userMsg string) error {
	m := strings.ToLower(userMsg)
	if hasAny(m, "toggle") {
		return s.Toggle()
	}
	on, _ := parseStatusCommand(userMsg)
	return s.SetStatus(on)
}

// deleteVIP removes a VIP by name or number.
func (s *Service) deleteVIP(input string) error {
	if jid, ok := s.vip.Lookup(input); ok {
		number := strings.Split(jid, "@")[0]
		return s.DeleteVIP(number)
	}
	return s.DeleteVIP(input)
}

// mutateAccess grants or revokes a tool for a VIP, shared with the set_access
// tool so the model path and the fast path behave identically.
func (s *Service) mutateAccess(recipient, tool string, enabled bool) error {
	jid, ok := s.vip.Lookup(recipient)
	if !ok {
		return fmt.Errorf("no VIP found matching %q", recipient)
	}

	grants, _, _ := s.AccessFor(jid)
	has := false
	for _, g := range grants {
		if g == tool {
			has = true
			break
		}
	}

	if enabled && !has {
		grants = append(grants, tool)
	} else if !enabled && has {
		next := grants[:0]
		for _, g := range grants {
			if g != tool {
				next = append(next, g)
			}
		}
		grants = next
	}

	sort.Strings(grants)
	return s.access.SetTools(jid, grants)
}

func contextUpdatedReply(text string) string {
	return contextUpdatedHeader + "\n\nMaster's context is now:\n\n> " + text
}

func vipAddedReply(payload string) string {
	if entries, ok := parseBulkVIP(payload); ok && len(entries) > 1 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\n_%d_ members have been welcomed into the inner circle, Sir.", vipAddedHeader, len(entries))
		for _, e := range entries {
			name := e
			if parts := strings.SplitN(e, ",", 3); len(parts) == 3 {
				name = strings.TrimSpace(parts[1])
			}
			b.WriteString("\n- _")
			b.WriteString(name)
			b.WriteString("_")
		}
		return b.String()
	}

	parts := strings.SplitN(payload, ",", 3)
	if len(parts) == 3 {
		number := numberedPrefixRe.ReplaceAllString(strings.TrimSpace(parts[0]), "")
		name := strings.TrimSpace(parts[1])
		relation := strings.TrimSpace(parts[2])
		return vipAddedHeader + "\n\n_" + name + "_ has been welcomed as *" + relation + "*.\n\nNumber: `" + number + "`"
	}
	return vipAddedHeader + "\n\nThe entry has been welcomed into the inner circle, Sir."
}

// numberedPrefixRe strips a leading list marker ("1. ", "2) ") from a number
// shown in a confirmation, for entries typed as single numbered lines.
var numberedPrefixRe = regexp.MustCompile(`^\d+[.)]\s*`)

func accessReply(recipient, tool string, enabled bool) string {
	if enabled {
		return "*Access Updated*\n\n_" + recipient + "_ has been *granted* `" + tool + "`."
	}
	return "*Access Updated*\n\n_" + recipient + "'s_ access to `" + tool + "` has been *revoked*."
}

func thinkingReply(on bool) string {
	if on {
		return "*Thinking Mode Updated*\n\nReasoning is now *On*, Sir. I shall ponder before I speak."
	}
	return "*Thinking Mode Updated*\n\nReasoning is now *Off*, Sir. I shall answer at once."
}

func historyLimitReply(limit int) string {
	return fmt.Sprintf("*History Limit Updated*\n\nI now review the %d most recent messages on every turn, Sir.", limit)
}

var historyLimitRes = regexp.MustCompile(`(?i)(?:set|change|update|make)\s+(?:my|clark(?:'s)?|the)?\s*history\s+limit\s+(?:to\s+)?(\d+)`)

// isHistoryLimitCommand reports whether the Master is sizing the history window.
func isHistoryLimitCommand(userMsg string) bool {
	_, ok := parseHistoryLimitCommand(userMsg)
	return ok
}

func parseHistoryLimitCommand(userMsg string) (int, bool) {
	m := historyLimitRes.FindStringSubmatch(userMsg)
	if m == nil {
		return 0, false
	}
	limit, err := strconv.Atoi(m[1])
	if err != nil || limit < 1 {
		return 0, false
	}
	return limit, true
}

// isThinkingCommand reports whether the Master is commanding the reasoning mode.
func isThinkingCommand(userMsg string) bool {
	_, _, ok := parseThinkingCommand(userMsg)
	return ok
}

// parseThinkingCommand extracts the requested polarity. toggle is true when the
// user asks to flip reasoning rather than pin it to a specific state.
func parseThinkingCommand(userMsg string) (on, toggle, ok bool) {
	m := strings.ToLower(userMsg)
	if !hasAny(m, "thinking", "reasoning") {
		return false, false, false
	}
	if hasAny(m, "toggle", "flip") {
		return false, true, true
	}
	if hasAny(m, "on", "enable", "start") {
		return true, false, true
	}
	if hasAny(m, "off", "disable", "stop") {
		return false, false, true
	}
	return false, false, false
}

// --- command detection -----------------------------------------------------

var statusCmdRe = regexp.MustCompile(`(?i)\b(on|off|online|offline|awake)\b`)

// perVIPStatusRes matches per-VIP status requests. The captured group names
// the target (a VIP, or "everyone"/"all" for a global command).
var perVIPStatusRes = []struct {
	re *regexp.Regexp
	on bool
}{
	{regexp.MustCompile(`(?i)(?:wake|wake\s+up)\s+(?:clark\s+)?for\s+([^\s].+)$`), true},
	{regexp.MustCompile(`(?i)(?:silence|silent|sleep|shut)\s+(?:clark\s+)?for\s+([^\s].+)$`), false},
	{regexp.MustCompile(`(?i)for\s+([^\s].+)\s+(?:wake|wake\s+up)\s+clark\s*$`), true},
	{regexp.MustCompile(`(?i)for\s+([^\s].+)\s+(?:silence|silent|sleep|shut)\s+clark\s*$`), false},
	{regexp.MustCompile(`(?i)\bwake\s+up\s+([^\s].+)$`), true},
	{regexp.MustCompile(`(?i)\bsilence\s+([^\s].+)$`), false},
}

// isPerVIPStatusCommand reports whether the Master is commanding a per-VIP or
// everyone status change rather than clark's global status.
func (s *Service) isPerVIPStatusCommand(userMsg string) bool {
	_, _, _, ok := s.parsePerVIPStatusCommand(userMsg)
	return ok
}

// parsePerVIPStatusCommand extracts a per-VIP status request. A target of
// "everyone"/"all" reports everyone=true so the caller can set the global
// status. Messages naming an unknown target are not consumed so the generic
// status or model path can handle them ("wake up buddy" stays global).
func (s *Service) parsePerVIPStatusCommand(userMsg string) (recipient string, on bool, everyone bool, ok bool) {
	m := strings.ToLower(strings.TrimSpace(userMsg))
	for _, c := range perVIPStatusRes {
		subm := c.re.FindStringSubmatch(m)
		if subm == nil {
			continue
		}
		target := strings.TrimSpace(subm[1])
		if isEveryoneTarget(target) {
			return "", c.on, true, true
		}
		if _, found := s.vip.Lookup(target); !found {
			return "", false, false, false
		}
		return target, c.on, false, true
	}
	return "", false, false, false
}

func isEveryoneTarget(target string) bool {
	t := strings.TrimSpace(strings.ToLower(target))
	return t == "everyone" || t == "all"
}

// isStatusCommand reports whether the message commands a status change.
func isStatusCommand(userMsg string) bool {
	m := strings.ToLower(userMsg)
	if hasAny(m, "toggle") {
		return hasAny(m, "status", "clark", "turn", "toggle", "on", "off")
	}
	if hasAny(m, "silence clark", "silent clark", "sleep clark", "shut clark", "power clark off", "wake clark", "wake up buddy") {
		return true
	}
	if hasAny(m, "status") || hasAny(m, "operational status") ||
		hasAny(m, "turn clark") || hasAny(m, "turn me") || hasAny(m, "switch clark") ||
		hasAny(m, "set clark") || hasAny(m, "change my status") || hasAny(m, "update my status") ||
		hasAny(m, "go offline") || hasAny(m, "go online") || hasAny(m, "go off") || hasAny(m, "go on") {
		return statusCmdRe.MatchString(m)
	}
	return false
}

// parseStatusCommand extracts the requested polarity from a status command.
func parseStatusCommand(userMsg string) (on bool, ok bool) {
	m := strings.ToLower(userMsg)
	switch {
	case hasAny(m, "silence clark", "silent clark", "sleep clark", "power clark off", "shut clark", "offline", "go off"):
		return false, true
	case hasAny(m, "wake clark", "awake", "online", "go on", "wake up buddy"):
		return true, true
	}
	match := statusCmdRe.FindStringSubmatch(m)
	if match == nil {
		return false, false
	}
	switch match[1] {
	case "on", "online", "awake":
		return true, true
	default:
		return false, true
	}
}

var contextCmdRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:set|update|change)\s+(?:my|clark(?:'s)?|the)?\s*context\s*(?:to|as|:)?\s*(.+)$`),
	regexp.MustCompile(`(?i)my\s+(?:new\s+)?context\s+(?:is|is now)\s+(.+)$`),
	regexp.MustCompile(`(?i)(?:set|change|update)\s+(?:the\s+)?context\s+to\s+(.+)$`),
}

// isContextCommand reports whether the message sets the master context.
func isContextCommand(userMsg string) bool {
	_, ok := parseContextCommand(userMsg)
	return ok
}

func parseContextCommand(userMsg string) (string, bool) {
	for _, re := range contextCmdRes {
		if m := re.FindStringSubmatch(userMsg); m != nil && len(m) > 1 {
			text := strings.TrimSpace(m[1])
			text = strings.TrimRight(text, ". ")
			if text != "" && !hasAny(text, "status", "vip", "access") {
				return text, true
			}
		}
	}
	return "", false
}

var clearContextRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:clear|empty|erase|wipe|reset|forget)\s+(?:the\s+|my\s+|clark(?:'s)?\s+)?(?:master\s+)?context\s*\.?\s*$`),
}

func isClearContextCommand(userMsg string) bool {
	_, ok := parseClearContextCommand(userMsg)
	return ok
}

func parseClearContextCommand(userMsg string) (string, bool) {
	for _, re := range clearContextRes {
		if re.MatchString(userMsg) {
			return "", true
		}
	}
	return "", false
}

var addVIPRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:add|register)\s+(?:a|new|the)?\s*(?:vip|member)\s+(.+)$`),
	regexp.MustCompile(`(?i)(?:add|register)\s+(.+?)\s+to\s+(?:the\s+)?(?:vip\s*list|inner\s+circle)`),
	// Bulk lists: "add vip:\n1. ..., ...\n2. ..., ..." or "register these vips: ..."
	regexp.MustCompile(`(?is)(?:add|register)\s+(?:these|the\s+following|the\s+below)?\s*(?:vips?|vip\s+members|members|people|numbers|contacts)?\s*[:.]\s*(.+)$`),
}

func isAddVIPCommand(userMsg string) bool {
	_, ok := parseAddVIPCommand(userMsg)
	return ok
}

func parseAddVIPCommand(userMsg string) (string, bool) {
	// A bare numbered list whose lines all look like VIP entries is itself an
	// add command: "1. <number>, <name>, <relation>\n2. ..."
	trimmed := strings.TrimSpace(userMsg)
	if entries, ok := parseBulkVIP(trimmed); ok {
		for _, e := range entries {
			if !isVIPEntry(e) {
				return "", false
			}
		}
		return trimmed, true
	}

	for _, re := range addVIPRes {
		if m := re.FindStringSubmatch(userMsg); m != nil && len(m) > 1 {
			payload := strings.TrimSpace(m[1])
			if payload != "" {
				return payload, true
			}
		}
	}
	return "", false
}

// isVIPEntry reports whether a payload looks like one "number, name, relation"
// entry, mirroring the single-entry validation in VIP.addSingle.
func isVIPEntry(s string) bool {
	return vipInputRe.MatchString(strings.TrimPrefix(strings.TrimSpace(s), "+"))
}

var delVIPRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:delete|remove)\s+(?:the|a)?\s*(?:vip|member)\s+(.+)$`),
	regexp.MustCompile(`(?i)(?:delete|remove)\s+(.+?)\s+from\s+(?:the\s+)?(?:vip\s*list|inner\s+circle|vips)`),
}

func isDeleteVIPCommand(userMsg string) bool {
	_, ok := parseDeleteVIPCommand(userMsg)
	return ok
}

func parseDeleteVIPCommand(userMsg string) (string, bool) {
	for _, re := range delVIPRes {
		if m := re.FindStringSubmatch(userMsg); m != nil && len(m) > 1 {
			payload := strings.TrimSpace(m[1])
			if payload != "" {
				return payload, true
			}
		}
	}
	return "", false
}

var clearVIPsRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:clear|empty|erase|wipe|reset)\s+(?:the\s+)?(?:vip\s*list|vips|inner\s+circle|ledger)\s*\.?\s*$`),
	regexp.MustCompile(`(?i)\bremove\s+all\s+(?:the\s+)?vips\s*\.?\s*$`),
}

func isClearVIPsCommand(userMsg string) bool {
	_, ok := parseClearVIPsCommand(userMsg)
	return ok
}

func parseClearVIPsCommand(userMsg string) (string, bool) {
	for _, re := range clearVIPsRes {
		if re.MatchString(userMsg) {
			return "", true
		}
	}
	return "", false
}

// guidancePhrases are the hardcoded phrases that summon the Master's manual.
// Deliberately distinct from viewRequest's report phrases so VIPs keep their
// granted-tools report while only the Master gets the full command guide.
var guidancePhrases = []string{
	"tool guidance", "show guidance",
	"butler's manual", "show me your manual",
	"show commands", "show me the commands", "show your commands",
	"list commands", "list of commands", "command list", "list your commands",
	"what commands do you have", "what commands can you run", "which commands do you have",
	"how do i use you", "how do i command you", "how to use you", "how do you work",
}

// isGuidanceCommand reports whether the Master is asking for the command guide.
func isGuidanceCommand(userMsg string) bool {
	m := strings.ToLower(strings.TrimSpace(userMsg))
	if m == "help" {
		return true
	}
	return hasAny(m, guidancePhrases...)
}

// guidanceText is the Master's manual: the hardcoded commands he can run plus
// the tools at Clark's disposal. It is only ever served through the fast path,
// which is gated to the Master's own chat.
func (s *Service) guidanceText() string {
	return "*" + s.name + "'s Manual*\n\n" +
		"*Hardcoded commands* (Master's own chat only):\n" +
		"- `wake up buddy` / `wake clark` — turn me on for everyone\n" +
		"- `silence clark` / `sleep clark` — turn me off for everyone\n" +
		"- `wake clark for <name>` — turn me on just for that person\n" +
		"- `silence <name>` — turn me off just for that person\n" +
		"- `wake clark for everyone` / `silence clark for all` — reset everyone to one status\n" +
		"- `thinking mode on` / `thinking mode off` — toggle reasoning\n" +
		"- `set history limit to 10` — how many past messages I review each turn\n" +
		"- `set my context to ...` — update your context\n" +
		"- `clear context` — empty your context\n" +
		"- `add vip <number>, <name>, <relation>` — admit someone (a numbered list adds several at once)\n" +
		"- `delete vip <name>` — remove someone\n" +
		"- `clear vips` — empty the inner circle\n" +
		"- `grant <name> access to <tool>` — grant a tool\n" +
		"- `revoke <name> access to <tool>` — revoke a tool\n" +
		"- `show me everything` — full report\n\n" +
		formatTools(s.tools.List())
}

var accessCmdRes = []struct {
	re      *regexp.Regexp
	enabled bool
}{
	{regexp.MustCompile(`(?i)(?:give|grant|allow|let)\s+(\S+?)\s+(?:access\s+to|to\s+use|the\s+tool)\s+(\S+)$`), true},
	{regexp.MustCompile(`(?i)set\s+access\s+(?:for\s+|to\s+)?(\S+)\s+(\S+)\s+(on|off)$`), true},
	{regexp.MustCompile(`(?i)(?:revoke|remove|take\s+away)\s+(\S+?)(?:'s)?\s+(?:access\s+to|to\s+use)\s+(\S+)$`), false},
}

func isAccessCommand(userMsg string) bool {
	_, _, _, ok := parseAccessCommand(userMsg)
	return ok
}

func parseAccessCommand(userMsg string) (recipient, tool string, enabled, ok bool) {
	for _, c := range accessCmdRes {
		m := c.re.FindStringSubmatch(userMsg)
		if m == nil {
			continue
		}
		enabled = c.enabled
		if m[2] == "off" || m[2] == "on" {
			enabled = m[2] == "on"
		}
		return m[1], m[2], enabled, true
	}
	return "", "", false, false
}
