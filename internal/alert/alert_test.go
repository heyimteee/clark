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
	webTexts []string
}

func (r *recorder) svc(butler Butler) (*Service, *recorder) {
	s := New(butler)
	s.SetDesktop(func(_, _ string) error { r.desktopN++; return nil })
	s.SetWASender(func(_ context.Context, t string) error { r.waTexts = append(r.waTexts, t); return nil })
	s.SetBroadcast(func(t string) { r.webTexts = append(r.webTexts, t) })
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
	if r.desktopN != 1 {
		t.Errorf("desktop calls = %d, want 1", r.desktopN)
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
