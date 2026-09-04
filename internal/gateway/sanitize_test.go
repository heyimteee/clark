package gateway

import "testing"

func TestSanitizeInbound(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean text untouched", "good evening", "good evening"},
		{"whatsapp prefix stripped", "`🤵🏻‍♂️[CLARK]`\nI am awake.", "I am awake."},
		{"imessage prefix stripped", "🤵🏻‍♂️[CLARK]\n\nI am awake.", "I am awake."},
		{"prefix without newline body", "🤵🏻‍♂️[CLARK]hello", "hello"},
		{"double prefix collapsed once", "`🤵🏻‍♂️[CLARK]`\n`🤵🏻‍♂️[CLARK]`\ntwo", "two"},
		{"prefix mid-text kept", "the bot said 🤵🏻‍♂️[CLARK] earlier", "the bot said 🤵🏻‍♂️[CLARK] earlier"},
	}
	for _, c := range cases {
		if got := SanitizeInbound(c.in); got != c.want {
			t.Errorf("%s: SanitizeInbound(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestIsClarkEcho(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"clean text is human", "good evening", false},
		{"empty is human", "", false},
		{"whatsapp brand is echo", "`🤵🏻‍♂️[CLARK]`\nI am awake.", true},
		{"imessage brand is echo", "🤵🏻‍♂️[CLARK]\n\nI am awake.", true},
		{"bare brand is echo", "🤵🏻‍♂️[CLARK]hello", true},
		{"leading whitespace still echo", "  \n`🤵🏻‍♂️[CLARK]`\nhello", true},
		{"mid-text quote is human", "the bot said 🤵🏻‍♂️[CLARK] earlier", false},
		{"lookalike without emoji is human", "[CLARK] hello", false},
	}
	for _, c := range cases {
		if got := IsClarkEcho(c.in); got != c.want {
			t.Errorf("%s: IsClarkEcho(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
