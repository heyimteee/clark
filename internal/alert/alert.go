// Package alert renders and delivers urgent notifications to the Master across
// every channel clark owns (WhatsApp, the web console chat, and spoken voice).
// Known alert kinds use hardcoded, Clark-voiced templates; unknown kinds fall
// back to a purely factual AI summary; if the model is unavailable during the
// alert, a generic "What / How / When" message is used instead so the Master is
// never left without an explanation.
package alert

import (
	"context"
	"strings"
	"time"

	"github.com/heyimteee/clark/internal/logging"
)

// Summarizer renders an unmapped alert kind as a short factual message. It is
// deliberately narrow: implementations must be tool-less and non-persistent,
// because webhook text is external input that must never reach a privileged
// agent loop (#58). *assistant.Service implements this via SummarizeAlert.
type Summarizer interface {
	SummarizeAlert(ctx context.Context, kind, title, body string) (string, error)
}

// Service holds the alert templates and the delivery hooks. Hooks are injected
// by the app so this package has no transport imports.
type Service struct {
	summarizer Summarizer
	hardcoded  map[string]string
	sendWA     func(ctx context.Context, text string) error
	sendIM     func(ctx context.Context, text string) error
	broadcast  func(text string, speak bool)
	desktop    func(title, body string) error
	mode       func() string
	faceTime   func(number string) error
	banner     func(title, body string) error
}

// New returns a Service with the default hardcoded alert templates.
// summarizer may be nil, in which case unmapped alerts always use the generic
// fallback.
func New(summarizer Summarizer) *Service {
	return &Service{
		summarizer: summarizer,
		hardcoded:  defaultMessages(),
	}
}

// SetWASender wires WhatsApp delivery (typically *whatsapp.WAMessenger.SendSelf,
// which reaches the Master's own chat).
func (s *Service) SetWASender(fn func(ctx context.Context, text string) error) { s.sendWA = fn }

// SetIMessageSender wires iMessage delivery (the imessage messenger's SendSelf,
// which enqueues a message for the macOS bridge to deliver).
func (s *Service) SetIMessageSender(fn func(ctx context.Context, text string) error) { s.sendIM = fn }

// SetBroadcast wires the web console chat broadcast. speak tells the console
// whether to also read the alert aloud; silent-mode alerts set it false so the
// console shows the alert without any audio.
func (s *Service) SetBroadcast(fn func(text string, speak bool)) { s.broadcast = fn }

// SetDesktop wires the desktop notification path (beeep), used in addition to
// the other channels on the Master's Mac.
func (s *Service) SetDesktop(fn func(title, body string) error) { s.desktop = fn }

// SetModeReader wires the alert-mode reader ("voice" or "silent"). The web
// console toggle persists the mode; delivery consults it per alert.
func (s *Service) SetModeReader(fn func() string) { s.mode = fn }

// SetFaceTime wires the FaceTime-call action (the macOS bridge triggers the
// call). Used by silent-mode alerts so the Master is physically buzzed.
func (s *Service) SetFaceTime(fn func(number string) error) { s.faceTime = fn }

// SetBanner wires the native macOS banner action (the macOS bridge shows it).
// Used by silent-mode alerts as a visible-on-the-Mac fallback to speech.
func (s *Service) SetBanner(fn func(title, body string) error) { s.banner = fn }

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

// Deliver renders the message for kind and pushes it to every wired channel.
// Voice mode: desktop, WhatsApp, iMessage, and web console (spoken aloud).
// Silent mode: desktop, WhatsApp, iMessage, web console (shown, not spoken),
// plus a FaceTime call and a native macOS banner so the Master is buzzed even
// when clark must stay quiet. Any channel that is not yet wired (nil hook) is
// skipped; the modes share all messaging channels for redundancy.
func (s *Service) Deliver(ctx context.Context, kind, title, body string) {
	text := s.render(ctx, kind, title, body)
	silent := s.silent()

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
	if s.sendIM != nil {
		if err := s.sendIM(ctx, text); err != nil {
			logf("ALERT", "imessage delivery failed: %v", err)
		}
	}
	if s.broadcast != nil {
		s.broadcast(text, !silent)
	}
	if silent {
		if s.faceTime != nil {
			if err := s.faceTime(""); err != nil {
				logf("ALERT", "facetime trigger failed: %v", err)
			}
		}
		if s.banner != nil {
			if err := s.banner(title, text); err != nil {
				logf("ALERT", "macos banner failed: %v", err)
			}
		}
	}
	logf("ALERT", "alert delivered", "kind", kind, "title", title, "silent", silent)
}

// silent reports whether alert mode is "silent" (default "voice").
func (s *Service) silent() bool {
	if s.mode == nil {
		return false
	}
	return strings.TrimSpace(strings.ToLower(s.mode())) == "silent"
}

// render picks the message text for an alert by precedence:
//  1. a hardcoded template for the known kind;
//  2. a purely factual AI summary for unknown kinds (model permitting);
//  3. a generic fallback that always states What, How, and When.
func (s *Service) render(ctx context.Context, kind, title, body string) string {
	if tpl, ok := s.hardcoded[kind]; ok {
		return fill(tpl, title, body)
	}
	if s.summarizer != nil {
		if txt := s.aiFallback(ctx, kind, title, body); txt != "" {
			return txt
		}
	}
	return fill(genericMessage, title, body)
}

// aiFallback asks the summarizer for a one-line factual description of the
// alert. It returns "" when the model is unavailable, rate-limited, or returns
// nothing useful; the caller then uses the generic template.
func (s *Service) aiFallback(ctx context.Context, kind, title, body string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if txt, err := s.summarizer.SummarizeAlert(ctx, kind, title, body); err == nil {
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
