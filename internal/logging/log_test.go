package logging

import "testing"

func TestFormatMessage(t *testing.T) {
	tests := []struct {
		name   string
		msg    string
		fields []any
		want   string
	}{
		{"no fields unchanged", "usage: clark [cmd]", nil, "usage: clark [cmd]"},
		{"verb substituted", "unknown command '%v'", []any{"help"}, "unknown command 'help'"},
		{"multi verb", "usage: clark %v [args]", []any{"vip"}, "usage: clark vip [args]"},
		{"error verb", "fail: %v", []any{"boom"}, "fail: boom"},
		{"verb without fields left intact", "open %v", nil, "open %v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatMessage(tt.msg, tt.fields); got != tt.want {
				t.Errorf("formatMessage(%q, %v) = %q, want %q", tt.msg, tt.fields, got, tt.want)
			}
		})
	}
}

func TestBrief(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"empty", "", 10, ""},
		{"short unchanged", "hello", 10, "hello"},
		{"exact length", "abcdefghij", 10, "abcdefghij"},
		{"long truncated", "abcdefghijklmnopqrstuvwxyz", 5, "abcde…"},
		{"multibyte safe", "héllo wörld", 5, "héllo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Brief(tt.in, tt.max); got != tt.want {
				t.Errorf("Brief(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}
