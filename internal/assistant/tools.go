package assistant

import (
	"context"
	"fmt"
	"sort"

	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/tools"
)

// registerManagementTools wires the Master-only management capabilities.
func (s *Service) registerManagementTools() {
	s.tools.RegisterFunc(
		"set_status",
		"Turn clark on or off. With a recipient, only that VIP's personal status changes; without one, clark's status changes for everyone. Triggered by phrasings like 'wake clark', 'silence clark', 'wake clark for <name>', 'go online/offline'. Only the Master may use this.",
		toolParams(map[string]any{
			"on":        map[string]any{"type": "boolean", "description": "true to wake clark, false to silence him"},
			"recipient": map[string]any{"type": "string", "description": "Optional: a VIP's name or phone number. When set, only that VIP's status changes."},
		}, "on"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			on := tools.BoolArg(args, "on")
			recipient := tools.StringArg(args, "recipient")
			if recipient != "" {
				if err := s.SetVIPStatus(recipient, on); err != nil {
					return "", err
				}
				if on {
					return "Understood, Master. " + recipient + " is now personally woken.", nil
				}
				return "Understood, Master. " + recipient + " is now personally silenced.", nil
			}
			if err := s.SetStatus(on); err != nil {
				return "", err
			}
			if on {
				return "I am awake and at your service, Master.", nil
			}
			return "I have fallen quiet as ordered, Master. I shall still answer you here.", nil
		},
	)

	s.tools.RegisterFunc(
		"set_thinking",
		"Toggle clark's reasoning/thinking mode. When enabled, clark thinks through problems step-by-step before answering. Only the Master may use this.",
		toolParams(map[string]any{
			"on": map[string]any{"type": "boolean", "description": "true to enable thinking mode, false to disable it"},
		}, "on"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			on := tools.BoolArg(args, "on")
			if err := s.SetThinking(on); err != nil {
				return "", err
			}
			if on {
				return "Thinking mode is now on, Sir. I will reason through problems before answering.", nil
			}
			return "Thinking mode is now off, Sir. I will respond directly.", nil
		},
	)

	s.tools.RegisterFunc(
		"set_context",
		"Update the master context that describes the Master's current status. Triggered by phrasings like 'my context is ...', 'remember that ...', 'note that ...', 'set my context to ...'. Only the Master may use this.",
		toolParams(map[string]any{
			"text": map[string]any{"type": "string", "description": "The new context, e.g. 'Busy in a meeting until noon'"},
		}, "text"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			text := tools.StringArg(args, "text")
			if text == "" {
				return "", fmt.Errorf("context text is required")
			}
			if err := s.SetContext(text); err != nil {
				return "", err
			}
			return "Noted, Master. Your context has been updated.", nil
		},
	)

	s.tools.RegisterFunc(
		"add_vip",
		"Add a person to the inner circle. Triggered by 'add vip <number>, <name>, <relation>', 'welcome <name> to the inner circle', or a numbered list of such entries. Phone numbers may be formatted ('+62 821-7450-0836'). Only the Master may use this.",
		toolParams(map[string]any{
			"number":   map[string]any{"type": "string", "description": "Phone number; formatting like +, spaces, dashes, parentheses is fine"},
			"name":     map[string]any{"type": "string", "description": "The person's name"},
			"relation": map[string]any{"type": "string", "description": "Their relation to the Master, e.g. Friend"},
		}, "number", "name", "relation"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			number := tools.StringArg(args, "number")
			name := tools.StringArg(args, "name")
			relation := tools.StringArg(args, "relation")
			if number == "" || name == "" || relation == "" {
				return "", fmt.Errorf("number, name, and relation are required")
			}
			if err := s.vip.Add(fmt.Sprintf("%s, %s, %s", number, name, relation)); err != nil {
				return "", err
			}
			return "Most excellent. " + name + " has been welcomed into the inner circle.", nil
		},
	)

	s.tools.RegisterFunc(
		"delete_vip",
		"Remove a person from the inner circle. Triggered by 'delete vip <name>' or 'remove <name> from the inner circle'. Only the Master may use this.",
		toolParams(map[string]any{
			"number": map[string]any{"type": "string", "description": "Phone number, digits only"},
		}, "number"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			number := tools.StringArg(args, "number")
			if number == "" {
				return "", fmt.Errorf("number is required")
			}
			if err := s.vip.Delete(number); err != nil {
				return "", err
			}
			return "As you command. The entry has been struck from the ledger.", nil
		},
	)

	s.tools.RegisterFunc(
		"set_access",
		"Grant or revoke a tool for a VIP. VIPs may only ever hold web_search or view_history. Triggered by 'grant <name> access to <tool>', 'let <name> use <tool>', 'revoke <name> access to <tool>'. Only the Master may use this.",
		toolParams(map[string]any{
			"recipient": map[string]any{"type": "string", "description": "A VIP's name or phone number"},
			"tool":      map[string]any{"type": "string", "description": "The tool name, e.g. web_search"},
			"enabled":   map[string]any{"type": "boolean", "description": "true to grant, false to revoke"},
		}, "recipient", "tool", "enabled"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			recipient := tools.StringArg(args, "recipient")
			tool := tools.StringArg(args, "tool")
			enabled := tools.BoolArg(args, "enabled")
			if recipient == "" || tool == "" {
				return "", fmt.Errorf("recipient and tool are required")
			}

			if err := s.mutateAccess(recipient, tool, enabled); err != nil {
				return "", err
			}
			state := "granted"
			if !enabled {
				state = "revoked"
			}
			return fmt.Sprintf("%s has been %s the tool %s.", recipient, state, tool), nil
		},
	)

	s.tools.RegisterFunc(
		"get_state",
		"Report clark's current status, context, inner circle, each VIP's effective status and granted tools, and every available tool. Triggered by questions like 'what is your status', 'who can reach me', 'show me everything'. Only the Master may use this.",
		toolParams(nil),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}

			out := fmt.Sprintf("Status: %v\nContext: %s", s.status, s.context)

			var vipLines []string
			for jid, label := range s.vip.List() {
				grants, _, _ := s.AccessFor(jid)
				state := statusLabel(s.EnabledFor(jid))
				if _, hasOverride := s.vip.IsEnabled(jid); hasOverride {
					state += " (personal)"
				}
				vipLines = append(vipLines, fmt.Sprintf("%s (%s): %s [%s]", label, jid, state, joinLines(grants)))
			}
			sort.Strings(vipLines)
			if len(vipLines) == 0 {
				out += "\nInner circle: None"
			} else {
				out += "\nInner circle:\n" + joinLines(vipLines)
			}

			out += "\nAvailable tools:\n" + describeTools(s.tools.List())
			return out, nil
		},
	)

	s.tools.RegisterFunc(
		"view_history",
		"Show the stored conversation history for a chat. Without a recipient, shows the current chat; a limit shows only the most recent messages. Triggered by 'what did we talk about', 'show me our past messages', 'what did <name> say'. Review the injected recent history first and call this only when you need more of the conversation.",
		toolParams(map[string]any{
			"recipient": map[string]any{"type": "string", "description": "Optional: a VIP's name or phone number. Only the Master may view another chat."},
			"limit":     map[string]any{"type": "integer", "description": "Optional: how many of the most recent messages to show. Omit for the full history."},
		}),
		func(ctx context.Context, args map[string]any) (string, error) {
			jid := tools.Sender(ctx)
			if recipient := tools.StringArg(args, "recipient"); recipient != "" {
				if err := masterOnly(ctx); err != nil {
					return "", err
				}
				rjid, ok := s.vip.Lookup(recipient)
				if !ok {
					return "", fmt.Errorf("no VIP found matching %q", recipient)
				}
				jid = rjid
			}
			return s.formatHistory(jid, tools.IntArg(args, "limit", 0))
		},
	)

	s.tools.RegisterFunc(
		"view_all_history",
		"Show messages from every conversation, newest last. Optionally limit to the most recent N messages across all chats. Triggered by 'show me everything across all chats' or 'what has been said everywhere'. Only the Master may use this.",
		toolParams(map[string]any{
			"limit": map[string]any{"type": "integer", "description": "Optional: show only the most recent N messages across all chats. Omit for everything stored."},
		}),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			entries, err := s.history.AllRecentMessages(tools.IntArg(args, "limit", 0))
			if err != nil {
				return "", err
			}
			if len(entries) == 0 {
				return "No conversation history is stored anywhere.", nil
			}
			lines := make([]string, 0, len(entries))
			for _, e := range entries {
				lines = append(lines, s.historySpeaker(e.JID, e.Role)+": "+e.Content)
			}
			return joinLines(lines), nil
		},
	)

	s.tools.RegisterFunc(
		"set_history_limit",
		"Change how many recent messages are injected into every turn. Larger gives clark more memory per reply, smaller makes replies leaner and cheaper. Triggered by 'set history limit to 10' or 'remember fewer/more messages'. Only the Master may use this.",
		toolParams(map[string]any{
			"limit": map[string]any{"type": "integer", "description": "Number of recent messages to keep in context, e.g. 10 or 5"},
		}, "limit"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			limit := tools.IntArg(args, "limit", 0)
			if limit < 1 {
				return "", fmt.Errorf("the limit must be at least 1")
			}
			if err := s.SetHistoryLimit(limit); err != nil {
				return "", err
			}
			return fmt.Sprintf("Understood, Master. I now review the %d most recent messages on every turn.", limit), nil
		},
	)
}

// formatHistory renders a chat's stored history as speaker-labelled lines.
// A positive limit keeps only the most recent messages; otherwise the full
// history is returned.
func (s *Service) formatHistory(jid string, limit int) (string, error) {
	var msgs []store.Message
	var err error
	if limit > 0 {
		msgs, err = s.history.RecentMessages(jid, limit)
	} else {
		msgs, err = s.history.Messages(jid)
	}
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "No conversation history for this chat yet.", nil
	}

	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, s.historySpeaker(jid, m.Role)+": "+m.Content)
	}
	return joinLines(lines), nil
}

// historySpeaker maps a stored message role to the speaker label used when
// rendering history: clark himself for assistant messages, otherwise the chat
// partner (a VIP's name, or the Master in his own chat).
func (s *Service) historySpeaker(jid, role string) string {
	if role == "assistant" {
		return s.name
	}
	if contact, _ := s.vip.Check(jid); contact != "" {
		return contact
	}
	return "The Master"
}

func masterOnly(ctx context.Context) error {
	if !tools.IsMaster(ctx) {
		return fmt.Errorf("forbidden: only the Master may manage clark")
	}
	return nil
}

func toolParams(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	p := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		p["required"] = required
	}
	return p
}
