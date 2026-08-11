package whatsapp

import "testing"

func TestPrefixMessage(t *testing.T) {
	want := messagePrefix + "hello"
	if got := prefixMessage("hello"); got != want {
		t.Errorf("prefixMessage(hello) = %q, want %q", got, want)
	}
}

func TestPrefixMessageEmpty(t *testing.T) {
	if got := prefixMessage(""); got != messagePrefix {
		t.Errorf("prefixMessage(empty) = %q, want prefix only", got)
	}
}

func TestPrefixMessageMultiline(t *testing.T) {
	text := "first line\nsecond line"
	got := prefixMessage(text)
	if got != messagePrefix+text {
		t.Errorf("prefixMessage(multiline) = %q, want prefix + text", got)
	}
}
