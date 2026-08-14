// Package alert renders and delivers urgent notifications to the Master across
// every channel clark owns (WhatsApp, the web console chat, and spoken voice).
// Known alert kinds use hardcoded, Clark-voiced templates; unknown kinds fall
// back to a purely factual AI summary; if the model is unavailable during the
// alert, a generic "What / How / When" message is used instead so the Master is
// never left without an explanation.
package alert

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/logging"
)

// Butler is the model-backed conversational brain used only for the AI
// fallback of unmapped alert kinds. The web server's *assistant.Service
// satisfies it.
type Butler interface {
	ReplyLLM(ctx context.Context, senderJID, userMsg string, isSelf bool) (string, string, error)
}

// Service holds the alert templates and the delivery hooks. Hooks are injected
// by the app so this package has no transport imports.
type Service struct {
	butler    Butler
	hardcoded map[string]string
	sendWA    func(ctx context.Context, text string) error
	broadcast func(text string)
	desktop   func(title, body string) error
}

// New returns a Service with the default hardcoded alert templates. butler may
// be nil, in which case unmapped alerts always use the generic fallback.
func New(butler Butler) *Service {
	return &Service{
		butler:    butler,
		hardcoded: defaultMessages(),
	}
}

// SetWASender wires WhatsApp delivery (typically *whatsapp.WAMessenger.SendSelf,
// which reaches the Master's own chat).
func (s *Service) SetWASender(fn func(ctx context.Context, text string) error) { s.sendWA = fn }

// SetBroadcast wires the web console chat broadcast (the chat hub).
func (s *Service) SetBroadcast(fn func(text string)) { s.broadcast = fn }

// SetDesktop wires the desktop notification path (beeep), used in addition to
// the other channels on the Master's Mac.
func (s *Service) SetDesktop(fn func(title, body string) error) { s.desktop = fn }

// Notify implements gateway.Notifier. It is the fallback delivery path used by
// transports that only know the two-argument form; alerts reach the Master on
// every channel with the generic/unknown rendering.
func (s *Service) Notify(title, body string) error {
	s.Deliver(context.Background(), "", title, body)
	return nil
}

// Alert delivers a fully-rendered, kind-aware alert (used by the urgent-command
// path so "get him to me" is classified as kind=bypass).
func (s *Service) Alert(ctx context.Context, kind, title, body string) {
	s.Deliver(ctx, kind, title, body)
}

// Deliver renders the message for kind and pushes it to every wired channel:
// desktop notification, WhatsApp, and the web console chat (which the browser
// also speaks aloud). Any channel that is not yet wired (nil hook) is skipped.
func (s *Service) Deliver(ctx context.Context, kind, title, body string) {
	text := s.render(ctx, kind, title, body)
	if s.desktop != nil {
		if err := s.desktop(title, text); err != nil {
			logf("ALERT", "desktop notification failed: %v", err)
		}
	}
	if s.sendWA != nil {
		if err := s.sendWA(ctx, text); err != nil {
			logf("ALERT", "whatsapp delivery failed: %v", err)
		}
	}
	if s.broadcast != nil {
		s.broadcast(text)
	}
	logf("ALERT", "alert delivered", "kind", kind, "title", title)
}

// render picks the message text for an alert by precedence:
//  1. a hardcoded template for the known kind;
//  2. a purely factual AI summary for unknown kinds (model permitting);
//  3. a generic fallback that always states What, How, and When.
func (s *Service) render(ctx context.Context, kind, title, body string) string {
	if tpl, ok := s.hardcoded[kind]; ok {
		return fill(tpl, title, body)
	}
	if s.butler != nil {
		if txt := s.aiFallback(ctx, kind, title, body); txt != "" {
			return txt
		}
	}
	return fill(genericMessage, title, body)
}

// aiFallback asks the model to summarise the alert factually, in one short
// message addressed to the Master. It returns "" when the model is unavailable,
// rate-limited, or returns nothing useful.
func (s *Service) aiFallback(ctx context.Context, kind, title, body string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prompt := fmt.Sprintf(
		"An automated monitoring alert just fired. Kind: %q. Title: %q. Details: %q. "+
			"Write one short, purely factual message for the Master summarising what happened. "+
			"Do not speculate, do not add advice, do not use markdown.", kind, title, body)
	reply, _, err := s.butler.ReplyLLM(ctx, "web", prompt, true)
	if err != nil {
		return ""
	}
	if txt := strings.TrimSpace(reply); txt != "" {
		return txt
	}
	return ""
}

// fill substitutes {title} and {body} placeholders in a template and appends
// the local time so the Master always knows when an alert fired.
func fill(tpl, title, body string) string {
	out := strings.ReplaceAll(tpl, "{title}", strings.TrimSpace(title))
	out = strings.ReplaceAll(out, "{body}", strings.TrimSpace(body))
	if !strings.Contains(out, "{time}") {
		out += " (" + time.Now().Format("15:04") + ")"
	}
	return out
}

// genericMessage is the last-resort fallback: it always states What, How, and
// When so the Master is never left guessing.
const genericMessage = "Sir, something went wrong.\nWhat: {title}\nHow: {body}\nWhen: {time}"

func logf(event, msg string, fields ...any) {
	logging.Log("ALERT", logging.SevInfo, event, msg, fields...)
}

func defaultMessages() map[string]string {
	return map[string]string{
		"overheat":       "Sir, the server is running hot. CPU temperature is elevated. {body}",
		"temp_high":      "Sir, the server is running hot. CPU temperature is elevated. {body}",
		"disk_high":      "Sir, disk space is running low. {body}",
		"memory_high":    "Sir, memory usage is high. {body}",
		"cpu_high":       "Sir, CPU load is high. {body}",
		"container_down": "Sir, a service container has gone down or is restarting. {body}",
		"service_down":   "Sir, a service is unreachable. {body}",
		"reboot":         "Sir, the server rebooted. {body}",
		"dns_failure":    "Sir, DNS resolution failed inside a container. {body}",
		"bypass":         "Sir, {body}",
		"generic":        "Sir, {body}",
	}
}
