package assistant

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Hardcoded, fast mutation confirmations. These use WhatsApp rich text:
// *bold*, _italic_, ~strikethrough~, `inline code`, ```monospace```,
// "> quote" and "* "-prefixed bulleted lines.
const (
	contextUpdatedHeader = "*Context Updated*"

	vipAddedHeader   = "*Inner Circle Updated*"
	vipDeletedReply  = "*Inner Circle Updated*\n\nThe entry has been struck from the ledger, Master."
	commandErrorTmpl = "*Command Not Performed*\n\n_%s_"
)

func (s *Service) statusOnReply() string {
	return "*Status Updated*\n\n" + s.name + " is now *On* and at your service, Master."
}

func (s *Service) statusOffReply() string {
	return "*Status Updated*\n\n" + s.name + " is now *Off*, Master. He will still answer you here."
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
	parts := strings.SplitN(payload, ",", 3)
	if len(parts) == 3 {
		number := strings.TrimSpace(parts[0])
		name := strings.TrimSpace(parts[1])
		relation := strings.TrimSpace(parts[2])
		return vipAddedHeader + "\n\n_" + name + "_ has been welcomed as *" + relation + "*.\n\nNumber: `" + number + "`"
	}
	return vipAddedHeader + "\n\nThe entry has been welcomed into the inner circle, Master."
}

func accessReply(recipient, tool string, enabled bool) string {
	if enabled {
		return "*Access Updated*\n\n_" + recipient + "_ has been *granted* `" + tool + "`."
	}
	return "*Access Updated*\n\n_" + recipient + "'s_ access to `" + tool + "` has been *revoked*."
}

// --- command detection -----------------------------------------------------

var statusCmdRe = regexp.MustCompile(`(?i)\b(on|off|online|offline|awake)\b`)

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

var addVIPRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:add|register)\s+(?:a|new|the)?\s*(?:vip|member)\s+(.+)$`),
	regexp.MustCompile(`(?i)(?:add|register)\s+(.+?)\s+to\s+(?:the\s+)?(?:vip\s*list|inner\s+circle)`),
}

func isAddVIPCommand(userMsg string) bool {
	_, ok := parseAddVIPCommand(userMsg)
	return ok
}

func parseAddVIPCommand(userMsg string) (string, bool) {
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
