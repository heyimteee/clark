package imessage

import (
	"context"
	"strings"
	"testing"

	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/tools"
)

func masterCtx() context.Context {
	return tools.WithMaster(context.Background())
}

// TestSendQueuedReturnsID proves the send path surfaces the queue id, so a
// caller can later ask what happened to it.
func TestSendQueuedReturnsID(t *testing.T) {
	out := &fakeOutbound{}
	m := NewMessenger(out, "+6281111111111")

	id, err := m.SendQueued(context.Background(), "6281267858909@s.whatsapp.net", "greetings")
	if err != nil {
		t.Fatalf("SendQueued: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want a usable queue id", id)
	}
	if len(out.enqueued) != 1 {
		t.Fatalf("enqueued %d, want 1", len(out.enqueued))
	}
}

// TestSendImessageDoesNotClaimDelivery is the point of #215. The bridge has not
// sent anything at this point, and may never do so, so the tool must not tell
// the Master it was delivered.
func TestSendImessageDoesNotClaimDelivery(t *testing.T) {
	reg := tools.NewRegistry()
	out := &fakeOutbound{}
	m := NewMessenger(out, "+6281111111111")
	RegisterSendMessageTool(reg, m, func(string) (string, bool) { return "+6281267858909", true })

	got, err := reg.Execute(masterCtx(), "send_imessage",
		[]byte(`{"recipient":"Someone","message":"greetings"}`))
	if err != nil {
		t.Fatalf("send_imessage: %v", err)
	}
	if strings.Contains(strings.ToLower(got), "delivered to") {
		t.Errorf("tool said %q; it must not claim delivery when the message is only queued", got)
	}
	if !strings.Contains(strings.ToLower(got), "queued") {
		t.Errorf("tool said %q, want it to state the message was queued", got)
	}
	// The id must be reported so the status tool can be used afterwards.
	if !strings.Contains(got, "queue id") {
		t.Errorf("tool said %q, want it to return the queue id", got)
	}
}

// TestDeliveryStatusReportsFailure proves a dead-lettered message is reported as
// a failure with its reason, not as a success.
func TestDeliveryStatusReportsFailure(t *testing.T) {
	out := &fakeOutbound{dead: []store.DeadOutbound{{
		ID: 7, Recipient: "+6281267858909", Attempts: 5,
		LastError: "no outgoing row appeared in chat.db",
	}}}
	m := NewMessenger(out, "+6281111111111")

	got, err := m.DeliveryStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("DeliveryStatus: %v", err)
	}
	if !strings.Contains(strings.ToLower(got), "could not be delivered") {
		t.Errorf("status = %q, want an explicit failure", got)
	}
	if !strings.Contains(got, "no outgoing row appeared") {
		t.Errorf("status = %q, want the recorded reason included", got)
	}
	if strings.Contains(strings.ToLower(got), "do not claim it was sent") == false {
		t.Errorf("status = %q, want it to steer the model away from claiming success", got)
	}
}

// TestDeliveryStatusForUnknownIDIsHonest proves an unknown id is reported as
// uncertain rather than delivered. The bridge deletes a row on success, so a
// missing id usually means delivered — but "usually" is not "confirmed", and
// saying so is the whole point.
func TestDeliveryStatusForUnknownIDIsHonest(t *testing.T) {
	m := NewMessenger(&fakeOutbound{}, "+6281111111111")

	got, err := m.DeliveryStatus(context.Background(), 99)
	if err != nil {
		t.Fatalf("DeliveryStatus: %v", err)
	}
	if strings.Contains(strings.ToLower(got), "delivered") && !strings.Contains(strings.ToLower(got), "only once") {
		t.Errorf("status = %q, want it to avoid asserting delivery", got)
	}
	if !strings.Contains(got, "queue:") {
		t.Errorf("status = %q, want it to report the queue composition", got)
	}
}

// TestSendImessageRequiresMaster keeps the existing gate.
func TestSendImessageRequiresMaster(t *testing.T) {
	reg := tools.NewRegistry()
	m := NewMessenger(&fakeOutbound{}, "+6281111111111")
	RegisterSendMessageTool(reg, m, func(string) (string, bool) { return "+6281267858909", true })

	if _, err := reg.Execute(context.Background(), "send_imessage",
		[]byte(`{"recipient":"Someone","message":"hi"}`)); err == nil {
		t.Fatal("send_imessage succeeded for a non-Master caller")
	}
}
