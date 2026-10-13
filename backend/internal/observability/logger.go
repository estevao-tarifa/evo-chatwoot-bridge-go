// Package observability owns structured logging (log/slog, JSON output) and
// the correlation identifiers attached to every log line.
package observability

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// maxCorrelationIDLength bounds ids accepted from untrusted callers.
const maxCorrelationIDLength = 64

// NewLogger returns a JSON-structured slog.Logger writing to w at the given
// level.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// ParseLevel maps a LOG_LEVEL value (debug, info, warn, error) to a slog
// level.
func ParseLevel(name string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return 0, fmt.Errorf("observability: invalid log level %q: %w", name, err)
	}
	return level, nil
}

type correlationIDContextKey struct{}

// WithCorrelationID returns a copy of ctx carrying the correlation id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDContextKey{}, id)
}

// CorrelationID returns the correlation id carried by ctx, or "" when absent.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDContextKey{}).(string)
	return id
}

// NewCorrelationID returns a fresh URL-safe correlation id.
func NewCorrelationID() string {
	return rand.Text()
}

// SanitizeCorrelationID returns id when it is safe to log and echo back,
// otherwise "". Inbound ids are attacker-controlled, so length and charset
// are restricted; callers generate a fresh id on "".
func SanitizeCorrelationID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxCorrelationIDLength {
		return ""
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return ""
		}
	}
	return id
}

// FromContext returns base enriched with the correlation id from ctx.
// trace_id mirrors correlation_id until real tracing lands (Phase 6); both
// fields exist now so log queries stay stable.
func FromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	if base == nil {
		base = slog.Default()
	}
	id := CorrelationID(ctx)
	if id == "" {
		return base
	}
	return base.With("correlation_id", id, "trace_id", id)
}
