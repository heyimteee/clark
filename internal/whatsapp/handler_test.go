package whatsapp

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func msgFixture(timestamp time.Time, isFromMe bool) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     types.JID{User: "6281234567890", Server: "s.whatsapp.net"},
				Sender:   types.JID{User: "6281234567890", Server: "s.whatsapp.net"},
				IsFromMe: isFromMe,
			},
			Timestamp: timestamp,
		},
		Message: &waE2E.Message{Conversation: proto.String("hi")},
	}
}

func TestFilterMessageNil(t *testing.T) {
	skip, reason := filterMessage(nil, time.Now())
	if !skip {
		t.Fatal("nil message not skipped")
	}
	if reason == "" {
		t.Fatal("nil message skipped without reason")
	}
}

func TestFilterMessageOldTimestamp(t *testing.T) {
	now := time.Now()
	connectedAt := now.Add(-time.Minute)
	v := msgFixture(now.Add(-time.Hour), false)

	skip, reason := filterMessage(v, connectedAt)
	if !skip {
		t.Fatal("old message not skipped")
	}
	if reason != "" {
		t.Fatalf("old message skipped with reason %q, want silent", reason)
	}
}

func TestFilterMessageFresh(t *testing.T) {
	now := time.Now()
	connectedAt := now.Add(-time.Minute)
	v := msgFixture(now, false)

	if skip, _ := filterMessage(v, connectedAt); skip {
		t.Fatal("fresh message skipped")
	}
}

func TestFilterMessageZeroTimestamp(t *testing.T) {
	v := msgFixture(time.Time{}, false)

	if skip, _ := filterMessage(v, time.Now()); !skip {
		t.Fatal("zero-timestamp message not skipped")
	}
}

func TestEchoTracker(t *testing.T) {
	e := NewEchoTracker()

	if e.Consume("missing") {
		t.Fatal("Consume matched unknown id")
	}

	e.Mark("abc")
	if !e.Consume("abc") {
		t.Fatal("Consume missed tracked id")
	}
	if e.Consume("abc") {
		t.Fatal("Consume matched id after first consume")
	}
}
