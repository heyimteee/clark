package alert

import (
	"context"
	"strings"
	"testing"
	"time"
)

type stubButler struct {
	reply string
	err   error
}

func (b *stubButler) ReplyLLM(_ context.Context, _, _ string, _ bool) (string, string, error) {
	if b.err != nil {
		return "", "", b.err
	}
	return b.reply, "", nil
}

type recorder struct {
	desktopN int
	waTexts  []string
	imTexts  []string
	webTexts []string
	webSpeak []bool
	facetime int
	banners  int
	mode     string
}

func (r *recorder) svc(butler Butler) (*Service, *recorder) {
	s := New(butler)
	s.SetDesktop(func(_, _ string) error { r.desktopN++; return nil })
	s.SetWASender(func(_ context.Context, t string) error { r.waTexts = append(r.waTexts, t); return nil })
	s.SetIMessageSender(func(_ context.Context, t string) error { r.imTexts = append(r.imTexts, t); return nil })
	s.SetBroadcast(func(t string, speak bool) { r.webTexts = append(r.webTexts, t); r.webSpeak = append(r.webSpeak, speak) })
	s.SetFaceTime(func(_ string) error { r.facetime++; return nil })
	s.SetBanner(func(_, _ string) error { r.banners++; return nil })
	s.SetModeReader(func() string { return r.mode })
	return s, r
}

func TestDeliverHardcodedTemplate(t *testing.T) {
	s, r := (&recorder{}).svc(nil)
	s.Deliver(context.Background(), "overheat", "Heat", "CPU 92°C")
	if len(r.waTexts) != 1 || len(r.webTexts) != 1 {
		t.Fatalf("wa=%d web=%d, want one each", len(r.waTexts), len(r.webTexts))
	}
	if !strings.Contains(r.waTexts[0], "running hot") || !strings.Contains(r.waTexts[0], "CPU 92°C") {
		t.Errorf("wa text = %q, want template with body", r.waTexts[0])
	}
	if !strings.Contains(r.waTexts[0], "(") {
		t.Errorf("wa text missing time suffix: %q", r.waTexts[0])
	}
	if r.waTexts[0] != r.webTexts[0] {
		t.Errorf("web text != wa text")
	}
	if len(r.imTexts) != 1 || r.imTexts[0] != r.waTexts[0] {
		t.Errorf("imessage delivery missing/mismatch: %v", r.imTexts)
	}
	if r.desktopN != 1 {
		t.Errorf("desktop calls = %d, want 1", r.desktopN)
	}
	if len(r.webSpeak) != 1 || !r.webSpeak[0] {
		t.Errorf("voice-mode web broadcast speak = %v, want true", r.webSpeak)
	}
	if r.facetime != 0 || r.banners != 0 {
		t.Errorf("voice mode should not facetime/banner: f=%d b=%d", r.facetime, r.banners)
	}
}

func TestDeliverSilentModeShowsButDoesNotSpeak(t *testing.T) {
	s, r := (&recorder{mode: "silent"}).svc(nil)
	s.Deliver(context.Background(), "bypass", "Attention Sir!", "Tiara needs you!")
	// Messaging channels stay for redundancy.
	if len(r.waTexts) != 1 || len(r.imTexts) != 1 || len(r.webTexts) != 1 {
		t.Fatalf("wa=%d im=%d web=%d, want one each", len(r.waTexts), len(r.imTexts), len(r.webTexts))
	}
	// Web chat shows the alert but does NOT speak it.
	if len(r.webSpeak) != 1 || r.webSpeak[0] {
		t.Errorf("silent-mode web broadcast speak = %v, want false", r.webSpeak)
	}
	// FaceTime call + macOS banner fire instead of speech.
	if r.facetime != 1 || r.banners != 1 {
		t.Errorf("silent mode facetime/banner = %d/%d, want 1/1", r.facetime, r.banners)
	}
}

func TestDeliverUnknownKindUsesAI(t *testing.T) {
	s, r := (&recorder{}).svc(&stubButler{reply: "API latency spiked to 900ms."})
	s.Deliver(context.Background(), "api_latency", "Latency", "p99 900ms")
	if r.waTexts[0] != "API latency spiked to 900ms." {
		t.Errorf("wa text = %q, want AI reply", r.waTexts[0])
	}
}

func TestDeliverUnknownKindAIUnavailableUsesGeneric(t *testing.T) {
	s, r := (&recorder{}).svc(&stubButler{err: context.DeadlineExceeded})
	s.Deliver(context.Background(), "mystery", "Mystery title", "Mystery body")
	got := r.waTexts[0]
	if !strings.Contains(got, "What: Mystery title") || !strings.Contains(got, "How: Mystery body") {
		t.Errorf("generic text missing What/How: %q", got)
	}
	if !strings.Contains(got, "When:") {
		t.Errorf("generic text missing When: %q", got)
	}
}

func TestDeliverNoButlerUsesGeneric(t *testing.T) {
	s, r := (&recorder{}).svc(nil)
	s.Deliver(context.Background(), "mystery", "X", "Y")
	if !strings.Contains(r.waTexts[0], "What: X") {
		t.Errorf("no-butler fallback wrong: %q", r.waTexts[0])
	}
}

func TestNotifyGatewayInterface(t *testing.T) {
	s, r := (&recorder{}).svc(nil)
	if err := s.Notify("Attention Sir!", "someone needs you"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(r.waTexts) != 1 || !strings.Contains(r.waTexts[0], "something went wrong") {
		t.Errorf("Notify delivery = %q, want generic path", r.waTexts)
	}
}

func TestAlertBypassKind(t *testing.T) {
	s, r := (&recorder{}).svc(nil)
	s.Alert(context.Background(), "bypass", "Attention Sir!", "Tiara needs you!")
	if len(r.waTexts) != 1 || !strings.Contains(r.waTexts[0], "Tiara needs you!") {
		t.Errorf("bypass delivery = %q", r.waTexts)
	}
	if !strings.HasPrefix(r.waTexts[0], "Sir,") {
		t.Errorf("bypass should use bypass template: %q", r.waTexts[0])
	}
}

func TestFillAlwaysIncludesTime(t *testing.T) {
	got := fill("{body}", "", "boom")
	if !strings.Contains(got, time.Now().Format("15:04")) {
		t.Errorf("no time in %q", got)
	}
}
