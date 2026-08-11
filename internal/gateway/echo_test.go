package gateway

import "testing"

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
