package whatsapp

import "testing"

func TestConnectedNilSafe(t *testing.T) {
	if NewMessenger(nil, nil).Connected() {
		t.Fatal("nil client reports connected")
	}
	if (&WAMessenger{}).Connected() {
		t.Fatal("zero messenger reports connected")
	}
}
