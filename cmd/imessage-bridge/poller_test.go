package main

import (
	"context"
	"errors"
	"testing"

	"github.com/heyimteee/clark/internal/store"
)

type fakeOutboundClient struct {
	queue    []store.OutboundMessage
	acked    []int64
	failPick bool
	sendErr  error
}

func (f *fakeOutboundClient) NextOutbound(_ context.Context) (store.OutboundMessage, bool, error) {
	if f.failPick {
		return store.OutboundMessage{}, false, errors.New("pick failed")
	}
	if len(f.queue) == 0 {
		return store.OutboundMessage{}, false, nil
	}
	msg := f.queue[0]
	f.queue = f.queue[1:]
	return msg, true, nil
}

func (f *fakeOutboundClient) Ack(_ context.Context, id int64) error {
	f.acked = append(f.acked, id)
	return nil
}

type fakeSender struct {
	recipient string
	text      string
	callCount int
	err       error
}

func (f *fakeSender) Send(recipient, text string) error {
	f.callCount++
	f.recipient = recipient
	f.text = text
	return f.err
}

func TestPollerDeliversAndAcks(t *testing.T) {
	fake := &fakeOutboundClient{queue: []store.OutboundMessage{{ID: 1, Recipient: "+6281267858909", Text: "hello"}}}
	sender := &fakeSender{}
	p := NewPoller(fake, sender, 0)

	p.pollOnce(context.Background())

	if sender.callCount != 1 || sender.recipient != "+6281267858909" || sender.text != "hello" {
		t.Errorf("sender = %+v, want one delivery", sender)
	}
	if len(fake.acked) != 1 || fake.acked[0] != 1 {
		t.Errorf("acked = %v, want [1]", fake.acked)
	}
}

func TestPollerEmptyQueueIsNoop(t *testing.T) {
	fake := &fakeOutboundClient{}
	sender := &fakeSender{}
	p := NewPoller(fake, sender, 0)

	p.pollOnce(context.Background())

	if sender.callCount != 0 || len(fake.acked) != 0 {
		t.Errorf("empty queue should deliver nothing; sender=%+v acked=%v", sender, fake.acked)
	}
}

func TestPollerSkipsAckOnDeliveryFailure(t *testing.T) {
	fake := &fakeOutboundClient{queue: []store.OutboundMessage{{ID: 1, Recipient: "+6281267858909", Text: "hello"}}}
	sender := &fakeSender{err: errors.New("delivery failed")}
	p := NewPoller(fake, sender, 0)

	p.pollOnce(context.Background())

	if sender.callCount != 1 {
		t.Errorf("send attempted %d times, want 1", sender.callCount)
	}
	if len(fake.acked) != 0 {
		t.Errorf("acked = %v, want none (message stays unacked)", fake.acked)
	}
}

func TestPollerPickFailureIsLoggedNotFatal(t *testing.T) {
	fake := &fakeOutboundClient{failPick: true}
	sender := &fakeSender{}
	p := NewPoller(fake, sender, 0)

	p.pollOnce(context.Background()) // must not panic
	if sender.callCount != 0 {
		t.Error("nothing should be sent when pick fails")
	}
}
