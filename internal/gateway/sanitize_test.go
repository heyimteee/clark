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

func TestSanitizeWhatsAppRichText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"paired emphasis untouched", "*Gemma* by Google", "*Gemma* by Google"},
		{"prompt example untouched", "_Ha! *Very* droll,_ Sir.", "_Ha! *Very* droll,_ Sir."},
		{"lone opener dropped", "Ha! *wow this is great", "Ha! wow this is great"},
		{"lone closer dropped", "say _yes", "say yes"},
		{"trailing lone asterisk", "*bold* and trailing *", "*bold* and trailing "},
		{"snake_case untouched", "run moon_protocol now", "run moon_protocol now"},
		{"tool names untouched", "call current_time then report", "call current_time then report"},
		{"math untouched", "2*3=6 and 5_4", "2*3=6 and 5_4"},
		{"inline code protected", "use `a*b_c` now", "use `a*b_c` now"},
		{"fenced code protected", "```*x* _y_``` done", "```*x* _y_``` done"},
		{"brand prefix protected", "`🤵🏻‍♂️[CLARK]`\n*Hi* there", "`🤵🏻‍♂️[CLARK]`\n*Hi* there"},
		{"list bullets kept", "* item one\n* item two", "* item one\n* item two"},
		{"mixed pairs plus lone", "*bold* and _it_ and stray *", "*bold* and _it_ and stray "},
		{"empty untouched", "", ""},
		{"single asterisk kept", "*", "*"},
		{"italic across words kept", "_good morning_ Sir", "_good morning_ Sir"},
	}
	for _, c := range cases {
		if got := SanitizeWhatsAppRichText(c.in); got != c.want {
			t.Errorf("%s: SanitizeWhatsAppRichText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
