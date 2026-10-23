// Package audit provides redaction helpers and a minimal audit logger.
// Passwords, command bodies and full output must never be passed to it.
package audit

//脱敏日志包装，只有 ID/状态/错误码，永不写密码、命令正文、完整输出
import (
	"log/slog"
)

// RedactID returns a short, stable identifier prefix suitable for logs. It
// keeps logs correlatable without echoing full opaque identifiers.
func RedactID(id string) string {
	if id == "" {
		return ""
	}
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}

// Logger wraps slog with audit helpers. It intentionally exposes only Event,
// so callers cannot accidentally log arbitrary message bodies through it.
type Logger struct {
	log *slog.Logger
}

// New returns an audit logger.
func New(log *slog.Logger) *Logger {
	if log == nil {
		log = slog.Default()
	}
	return &Logger{log: log}
}

// Event records an audit event. Attributes must not contain secrets, command
// text or unbounded output. Use RedactID for identifiers.
func (l *Logger) Event(event string, attrs ...any) {
	if l == nil || l.log == nil {
		return
	}
	args := make([]any, 0, len(attrs)+2)
	args = append(args, "event", event)
	args = append(args, attrs...)
	l.log.Info("audit", args...)
}

// Warn records a failed or suspicious audit event.
func (l *Logger) Warn(event string, attrs ...any) {
	if l == nil || l.log == nil {
		return
	}
	args := make([]any, 0, len(attrs)+2)
	args = append(args, "event", event)
	args = append(args, attrs...)
	l.log.Warn("audit", args...)
}
