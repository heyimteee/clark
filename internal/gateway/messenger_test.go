package gateway

import "testing"

func TestPrefixMessage(t *testing.T) {
	want := MessagePrefix + "hello"
	if got := PrefixMessage("hello"); got != want {
		t.Errorf("PrefixMessage(hello) = %q, want %q", got, want)
	}
}

func TestPrefixMessageEmpty(t *testing.T) {
	if got := PrefixMessage(""); got != MessagePrefix {
		t.Errorf("PrefixMessage(empty) = %q, want prefix only", got)
	}
}

func TestPrefixMessageMultiline(t *testing.T) {
	text := "first line\nsecond line"
	got := PrefixMessage(text)
	if got != MessagePrefix+text {
		t.Errorf("PrefixMessage(multiline) = %q, want prefix + text", got)
	}
}

func TestPrefixIMessage(t *testing.T) {
	if got := PrefixIMessage("hi"); got != IMessagePrefix+"hi" {
		t.Errorf("PrefixIMessage(hi) = %q, want iMessage prefix + text", got)
	}
	if got := PrefixIMessage(""); got != IMessagePrefix {
		t.Errorf("PrefixIMessage(empty) = %q, want prefix only", got)
	}
}
