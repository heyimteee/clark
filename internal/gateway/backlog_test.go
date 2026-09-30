package gateway

import (
	"errors"
	"testing"
	"time"
)

// sentCount totals every delivery attempt on the fake messenger, so a test can
// assert that nothing at all was sent (no ack, no reply, no alert).
func sentCount(m *fakeMessenger) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sentTo) + m.sentSelf
}

func notified(n *fakeNotifier) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls)
}

// TestBacklogIsKeptButNotAnswered is the core #206 guarantee: a message clark
// could not have seen live is stored as history and never answered.
func TestBacklogIsKeptButNotAnswered(t *testing.T) {
	b := &fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour)}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "are you there?", Timestamp: time.Now().Add(-4 * time.Hour), Replay: true,
	})

	b.mu.Lock()
	recorded := append([]string(nil), b.recorded...)
	replied := append([]string(nil), b.replied...)
	b.mu.Unlock()

	if len(recorded) != 1 || recorded[0] != "are you there?" {
		t.Errorf("recorded = %v, want the backlog message kept as history", recorded)
	}
	if len(replied) != 0 {
		t.Errorf("replied = %v, want no reply to backlog", replied)
	}
	if sentCount(msgr) != 0 {
		t.Errorf("sent %d messages, want 0 (no ack, no reply)", sentCount(msgr))
	}
}

// TestLiveMessageIsAnswered proves ordinary conversation is untouched: a message
// sent after the status transition and delivered live is answered normally.
func TestLiveMessageIsAnswered(t *testing.T) {
	b := &fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour)}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "are you there?", Timestamp: time.Now(),
	})

	waitFor(t, func() bool { return len(b.replies()) > 0 })
	if got := b.replies(); len(got) != 1 {
		t.Errorf("replied = %v, want one live reply", got)
	}
	if got := b.recorded; len(got) != 0 {
		t.Errorf("recorded = %v, want none for a live message", got)
	}
}

// TestMessageBeforeStatusIsBacklog covers the transition boundary: clark was OFF
// when it was sent, so it is history even though the transport saw it live.
func TestMessageBeforeStatusIsBacklog(t *testing.T) {
	statusSince := time.Now().Add(-time.Minute)
	b := &fakeButler{enabled: true, statusSince: statusSince}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "sent while you were off", Timestamp: statusSince.Add(-time.Hour),
	})

	if got := b.recorded; len(got) != 1 {
		t.Errorf("recorded = %v, want the pre-ON message kept as history", got)
	}
	if got := b.replies(); len(got) != 0 {
		t.Errorf("replied = %v, want none before the status boundary", got)
	}
}

// TestNoStatusWatermarkAnswersEverything is the safe default for installs that
// predate the watermark: with no recorded transition, nothing is backlog.
func TestNoStatusWatermarkAnswersEverything(t *testing.T) {
	b := &fakeButler{enabled: true} // statusSince zero
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "ancient", Timestamp: time.Now().Add(-72 * time.Hour),
	})

	waitFor(t, func() bool { return len(b.replies()) > 0 })
	if got := b.replies(); len(got) != 1 {
		t.Errorf("replied = %v, want the message answered when no watermark exists", got)
	}
}

// TestBacklogBypassPhraseDoesNotAlert proves a stale "get him to me" cannot wake
// the Master hours after the fact — the backlog return precedes the bypass check.
func TestBacklogBypassPhraseDoesNotAlert(t *testing.T) {
	b := &fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour)}
	msgr := &fakeMessenger{}
	notifier := &fakeNotifier{}
	h := NewHandler("IMESSAGE", msgr, b, notifier, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "get him to me", Timestamp: time.Now().Add(-5 * time.Hour), Replay: true,
	})

	if notified(notifier) != 0 {
		t.Errorf("notifier fired %d times, want 0 for a stale bypass phrase", notified(notifier))
	}
	if sentCount(msgr) != 0 {
		t.Errorf("sent %d messages, want 0 for a stale bypass phrase", sentCount(msgr))
	}
}

// TestBacklogDoesNotRunFastPathCommands proves stale commands are not executed.
// A backloged "wake up buddy" must not silently flip status hours later.
func TestBacklogDoesNotRunFastPathCommands(t *testing.T) {
	b := &fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour), prehandled: "Awake, Sir."}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "wake up buddy", Timestamp: time.Now().Add(-5 * time.Hour), Replay: true,
	})

	if sentCount(msgr) != 0 {
		t.Errorf("sent %d messages, want 0; a backloged command must not execute", sentCount(msgr))
	}
	if got := b.recorded; len(got) != 1 {
		t.Errorf("recorded = %v, want the command kept as history", got)
	}
}

// TestStrangerBacklogIsStillDiscarded proves the VIP gate still runs first, so
// WhatsApp spam from unknown numbers cannot use the backlog path to fill history.
func TestStrangerBacklogIsStillDiscarded(t *testing.T) {
	b := &nonVIPButler{fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour)}}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "199@s.whatsapp.net", Chat: "199@s.whatsapp.net",
		Text: "buy my thing", Timestamp: time.Now().Add(-4 * time.Hour), Replay: true,
	})

	if got := b.recorded; len(got) != 0 {
		t.Errorf("recorded = %v, want a non-VIP discarded outright", got)
	}
	if sentCount(msgr) != 0 {
		t.Errorf("sent %d messages, want 0 for a non-VIP", sentCount(msgr))
	}
}

// TestBacklogRecordFailureIsNotFatal proves a store failure is logged and the
// pipeline keeps running rather than panicking or replying.
func TestBacklogRecordFailureIsNotFatal(t *testing.T) {
	b := &fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour), recordErr: errors.New("store down")}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "hello", Timestamp: time.Now().Add(-time.Hour), Replay: true,
	})

	if sentCount(msgr) != 0 {
		t.Errorf("sent %d messages, want 0; a failed record must not fall through to a reply", sentCount(msgr))
	}
}

// TestBacklogRecordsTextVerbatim proves the history-only path stores exactly what
// was sent. It must not invent a "delayed" annotation or otherwise rewrite the
// turn: the backlog is context for the model, so it has to read as the real
// conversation. (Note the Clark-echo filter runs earlier and independently, so
// branded impersonation is already dropped before this point.)
func TestBacklogRecordsTextVerbatim(t *testing.T) {
	const sent = "Are we still on for 8pm? I need to know before I leave."
	b := &fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour)}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: sent, Timestamp: time.Now().Add(-time.Hour), Replay: true,
	})

	if got := b.recorded; len(got) != 1 || got[0] != sent {
		t.Errorf("recorded = %v, want exactly [%q]", got, sent)
	}
}

// TestBacklogEmptyTextIsNotRecorded keeps media-only backlog out of history as an
// empty turn, which would otherwise be injected into the next prompt as noise.
func TestBacklogEmptyTextIsNotRecorded(t *testing.T) {
	b := &fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour)}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "", MediaType: "image", Timestamp: time.Now().Add(-time.Hour), Replay: true,
	})

	if got := b.recorded; len(got) != 0 {
		t.Errorf("recorded = %v, want no empty turn for a media-only backlog message", got)
	}
	if sentCount(msgr) != 0 {
		t.Errorf("sent %d messages, want 0", sentCount(msgr))
	}
}

// TestBacklogMasterGetsNoAck proves the "_One moment, Sir..._" acknowledgement is
// suppressed, since nothing is being worked on.
func TestBacklogMasterGetsNoAck(t *testing.T) {
	b := &fakeButler{enabled: true, statusSince: time.Now().Add(-time.Hour)}
	msgr := &fakeMessenger{}
	h := NewHandler("IMESSAGE", msgr, b, nil, "get him to me")

	h.Handle(Message{
		ID: "1", Sender: "628@s.whatsapp.net", Chat: "628@s.whatsapp.net",
		Text: "ping", IsSelf: true, Timestamp: time.Now().Add(-time.Hour), Replay: true,
	})

	if sentCount(msgr) != 0 {
		t.Errorf("sent %d messages, want no master ack for backlog", sentCount(msgr))
	}
}

func (b *fakeButler) replies() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.replied...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
