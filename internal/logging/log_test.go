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
