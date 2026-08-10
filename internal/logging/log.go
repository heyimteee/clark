// Package logging provides clark's structured, colored log emitter.
//
// Line format: TIMESTAMP LEVEL COMPONENT EVENT: message (key=value ...)
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// Severity mirrors the syslog severity scale (0=most severe, 7=debug).
type Severity int

const (
	SevEmerg  Severity = iota // 0
	SevAlert                  // 1
	SevCrit                   // 2
	SevErr                    // 3
	SevWarn                   // 4
	SevNotice                 // 5
	SevInfo                   // 6
	SevDebug                  // 7
)

const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiRed     = "\033[1;31m"
	ansiYellow  = "\033[33m"
	ansiGreen   = "\033[32m"
	ansiCyan    = "\033[36m"
	ansiMagenta = "\033[35m"
	ansiBlue    = "\033[34m"
	ansiGray    = "\033[90m"
)

func componentColor(component string) string {
	switch component {
	case "CLARK":
		return ansiCyan
	case "WHATSAPP":
		return ansiGreen
	case "OLLAMA":
		return ansiYellow
	case "MEMORY":
		return ansiMagenta
	case "TOOLS":
		return ansiBlue
	case "SYSTEM":
		return ansiRed
	}
	return ""
}

func levelWord(l slog.Level) (string, string) {
	switch {
	case l >= slog.LevelError:
		return "ERROR", ansiRed
	case l >= slog.LevelWarn:
		return "WARN", ansiYellow
	case l >= slog.Level(2):
		return "NOTICE", ansiCyan
	case l >= slog.LevelInfo:
		return "INFO", ansiGreen
	default:
		return "DEBUG", ansiGray
	}
}

func slogLevel(sev Severity) slog.Level {
	switch sev {
	case SevDebug:
		return slog.LevelDebug
	case SevInfo:
		return slog.LevelInfo
	case SevNotice:
		return slog.Level(2)
	case SevWarn:
		return slog.LevelWarn
	default:
		return slog.LevelError
	}
}

type colorHandler struct {
	w       io.Writer
	min     slog.Level
	attrs   []slog.Attr
	noColor bool
}

func (h *colorHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.min
}

func (h *colorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &colorHandler{w: h.w, min: h.min, attrs: merged, noColor: h.noColor}
}

func (h *colorHandler) WithGroup(string) slog.Handler { return h }

func (h *colorHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, r.NumAttrs()+len(h.attrs))
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	var component, event string
	var fields []string
	for _, a := range attrs {
		switch a.Key {
		case "component":
			component = a.Value.String()
		case "event":
			event = a.Value.String()
		default:
			fields = append(fields, renderField(a.Key, a.Value))
		}
	}
	if component == "" {
		component = "CLARK"
	}

	ts := r.Time.Format("15:04:05.000")
	word, color := levelWord(r.Level)
	line := fmt.Sprintf("%s %-6s %-9s %-8s: %s",
		ts, word, component, event, r.Message)
	if len(fields) > 0 {
		line += " (" + strings.Join(fields, " ") + ")"
	}

	if h.noColor {
		fmt.Fprintln(h.w, line)
		return nil
	}

	c := componentColor(component)
	parts := []string{ansiDim + ts + ansiReset}
	parts = append(parts, color+word+ansiReset)
	if c != "" {
		parts = append(parts, fmt.Sprintf("%s%-9s%s", c, component, ansiReset))
	} else {
		parts = append(parts, component)
	}
	parts = append(parts, ansiBold+event+ansiReset+": "+r.Message)
	colored := strings.Join(parts, " ")
	if len(fields) > 0 {
		colored += ansiDim + " (" + strings.Join(fields, " ") + ")" + ansiReset
	}
	fmt.Fprintln(h.w, colored)
	return nil
}

func renderField(key string, v slog.Value) string {
	var val string
	switch v.Kind() {
	case slog.KindString:
		val = fmt.Sprintf("%q", v.String())
	case slog.KindBool:
		val = fmt.Sprintf("%t", v.Bool())
	case slog.KindDuration:
		val = v.Duration().String()
	case slog.KindTime:
		val = v.Time().Format(time.RFC3339)
	case slog.KindInt64:
		val = strconv.FormatInt(v.Int64(), 10)
	case slog.KindUint64:
		val = strconv.FormatUint(v.Uint64(), 10)
	case slog.KindFloat64:
		val = strconv.FormatFloat(v.Float64(), 'g', -1, 64)
	default:
		val = fmt.Sprintf("%q", fmt.Sprint(v.Any()))
	}
	return fmt.Sprintf("%s=%s", key, val)
}

var stdLogger *slog.Logger

func init() {
	min := slog.LevelDebug
	if os.Getenv("CLARK_LOG_FORMAT") == "json" {
		stdLogger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: min}))
		return
	}
	stdLogger = slog.New(&colorHandler{w: os.Stdout, min: min, noColor: !useColor()})
}

func useColor() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Log emits a structured log line of the form:
//
//	TIMESTAMP LEVEL  COMPONENT  EVENT: message (key=value ...)
func Log(component string, sev Severity, event, msg string, fields ...any) {
	attrs := make([]any, 0, 2+len(fields))
	attrs = append(attrs, "component", component, "event", event)
	attrs = append(attrs, fields...)
	stdLogger.Log(context.Background(), slogLevel(sev), msg, attrs...)
}

// Fatalf logs a SYSTEM severity-3 error and terminates the process.
func Fatalf(event, msg string, fields ...any) {
	Log("SYSTEM", SevErr, event, msg, fields...)
	os.Exit(1)
}

// waLogger adapts clark's structured formatter to whatsmeow's Logger interface.
type waLogger struct {
	module string
	min    Severity
}

// NewWALogger returns a whatsmeow-compatible logger emitting WHATSAPP <MODULE> lines.
func NewWALogger(module string, min Severity) waLog.Logger {
	return &waLogger{module: sanitizeMnemonic(module), min: min}
}

func sanitizeMnemonic(s string) string {
	s = strings.ToUpper(s)
	replacer := strings.NewReplacer("/", "-", ".", "-", " ", "-")
	return replacer.Replace(s)
}

func (l *waLogger) emit(sev Severity, format string, args ...any) {
	if sev > l.min {
		return
	}
	msg := strings.TrimRight(fmt.Sprintf(format, args...), " ")
	Log("WHATSAPP", sev, l.module, msg)
}

func (l *waLogger) Debugf(format string, args ...any) { l.emit(SevDebug, format, args...) }
func (l *waLogger) Infof(format string, args ...any)  { l.emit(SevInfo, format, args...) }
func (l *waLogger) Warnf(format string, args ...any)  { l.emit(SevWarn, format, args...) }
func (l *waLogger) Errorf(format string, args ...any) { l.emit(SevErr, format, args...) }
func (l *waLogger) Sub(module string) waLog.Logger {
	return &waLogger{module: sanitizeMnemonic(module), min: l.min}
}
