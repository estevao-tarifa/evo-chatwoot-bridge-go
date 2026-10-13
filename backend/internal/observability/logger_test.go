package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	t.Run("writes json and honours the level", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewLogger(&buf, slog.LevelInfo)

		logger.Debug("hidden")
		logger.Info("visible", "key", "value")

		out := buf.String()
		if strings.Contains(out, "hidden") {
			t.Errorf("debug line emitted at info level: %s", out)
		}

		var line map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &line); err != nil {
			t.Fatalf("log line is not valid json: %v (%s)", err, out)
		}
		if line["msg"] != "visible" {
			t.Errorf("msg = %v, want visible", line["msg"])
		}
		if line["level"] != "INFO" {
			t.Errorf("level = %v, want INFO", line["level"])
		}
		if line["key"] != "value" {
			t.Errorf("key = %v, want value", line["key"])
		}
	})
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    slog.Level
		wantErr bool
	}{
		{name: "debug", input: "debug", want: slog.LevelDebug},
		{name: "info", input: "info", want: slog.LevelInfo},
		{name: "warn", input: "warn", want: slog.LevelWarn},
		{name: "error", input: "error", want: slog.LevelError},
		{name: "unknown level", input: "verbose", wantErr: true},
		{name: "empty level", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevel(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) error = nil, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q) unexpected error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCorrelationID(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		ctx := WithCorrelationID(context.Background(), "req-123")
		if got := CorrelationID(ctx); got != "req-123" {
			t.Errorf("CorrelationID() = %q, want req-123", got)
		}
	})

	t.Run("absent", func(t *testing.T) {
		if got := CorrelationID(context.Background()); got != "" {
			t.Errorf("CorrelationID() = %q, want empty", got)
		}
	})

	t.Run("fresh ids are usable and unique", func(t *testing.T) {
		first := NewCorrelationID()
		second := NewCorrelationID()
		if SanitizeCorrelationID(first) == "" {
			t.Errorf("NewCorrelationID() = %q, not usable as a correlation id", first)
		}
		if first == second {
			t.Errorf("NewCorrelationID() returned the same id twice: %q", first)
		}
	})
}

func TestSanitizeCorrelationID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "valid base32", input: "K7PM2Q4WX8Z1", want: "K7PM2Q4WX8Z1"},
		{name: "valid with separators", input: "req-123_abc.def:1", want: "req-123_abc.def:1"},
		{name: "trimmed", input: "  abc123  ", want: "abc123"},
		{name: "empty", input: "", want: ""},
		{name: "blank", input: "   ", want: ""},
		{name: "max length", input: strings.Repeat("a", maxCorrelationIDLength), want: strings.Repeat("a", maxCorrelationIDLength)},
		{name: "too long", input: strings.Repeat("a", maxCorrelationIDLength+1), want: ""},
		{name: "inline space", input: "abc 123", want: ""},
		{name: "newline injection", input: "abc\nfake", want: ""},
		{name: "header injection", input: "abc\r\nInjected: x", want: ""},
		{name: "non ascii", input: "café", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeCorrelationID(tt.input); got != tt.want {
				t.Errorf("SanitizeCorrelationID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFromContext(t *testing.T) {
	t.Run("attaches correlation and trace id", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewLogger(&buf, slog.LevelInfo)
		ctx := WithCorrelationID(context.Background(), "req-abc")

		FromContext(ctx, logger).Info("hello")

		var line map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line); err != nil {
			t.Fatalf("log line is not valid json: %v (%s)", err, buf.String())
		}
		if line["correlation_id"] != "req-abc" {
			t.Errorf("correlation_id = %v, want req-abc", line["correlation_id"])
		}
		if line["trace_id"] != "req-abc" {
			t.Errorf("trace_id = %v, want req-abc", line["trace_id"])
		}
	})

	t.Run("no id leaves the logger untouched", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewLogger(&buf, slog.LevelInfo)

		FromContext(context.Background(), logger).Info("hello")

		if strings.Contains(buf.String(), "correlation_id") {
			t.Errorf("unexpected correlation_id in %s", buf.String())
		}
	})

	t.Run("nil base logger falls back to default", func(t *testing.T) {
		if got := FromContext(context.Background(), nil); got == nil {
			t.Error("FromContext() = nil, want non-nil logger")
		}
	})
}
