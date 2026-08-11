package assistant

import (
	"context"
	"fmt"
	"sort"

	"github.com/heyimteee/clark/internal/tools"
)

// registerManagementTools wires the Master-only management capabilities.
func (s *Service) registerManagementTools() {
	s.tools.RegisterFunc(
		"set_status",
		"Turn clark on or off. Only the Master may use this.",
		toolParams(map[string]any{
			"on": map[string]any{"type": "boolean", "description": "true to wake clark, false to silence him"},
		}, "on"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}
			on := tools.BoolArg(args, "on")
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
		"set_context",
		"Update the master context that describes the Master's current status. Only the Master may use this.",
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
		"Add a person to the inner circle. Only the Master may use this.",
		toolParams(map[string]any{
			"number":   map[string]any{"type": "string", "description": "Phone number, digits only"},
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
		"Remove a person from the inner circle. Only the Master may use this.",
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
		"Grant or revoke a tool for a VIP. VIPs may only ever hold web_search. Only the Master may use this.",
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
		"Report clark's current status, context, inner circle, each VIP's granted tools, and every available tool. Only the Master may use this.",
		toolParams(nil),
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnly(ctx); err != nil {
				return "", err
			}

			out := fmt.Sprintf("Status: %v\nContext: %s", s.status, s.context)

			var vipLines []string
			for jid, label := range s.vip.List() {
				grants, _, _ := s.AccessFor(jid)
				vipLines = append(vipLines, fmt.Sprintf("%s (%s): %s", label, jid, joinLines(grants)))
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
