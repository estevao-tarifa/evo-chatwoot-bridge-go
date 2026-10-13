package httpapi

import "testing"

// TestMaskURLToken locks the inbox-URL masker: the final path segment (the
// bridge webhook token) is replaced with "***", query-string values are masked
// (parameter names kept) and the fragment is masked, so no credential survives
// outside the path. A URL with nothing to mask is returned unchanged.
func TestMaskURLToken(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bridge webhook url", "https://bridge.example/api/webhooks/chatwoot/1/abc-DEF_123", "https://bridge.example/api/webhooks/chatwoot/1/***"},
		{"single segment", "https://bridge.example/token", "https://bridge.example/***"},
		{"no path", "https://bridge.example", "https://bridge.example"},
		{"root path", "https://bridge.example/", "https://bridge.example/"},
		{"empty", "", ""},
		{"query string token", "https://bridge.example/api/webhooks/chatwoot/1/abc?token=secret", "https://bridge.example/api/webhooks/chatwoot/1/***?token=***"},
		{"multiple query params", "https://bridge.example/api/webhooks/chatwoot/1/abc?a=1&b=2", "https://bridge.example/api/webhooks/chatwoot/1/***?a=***&b=***"},
		{"valueless query param", "https://bridge.example/api/webhooks/chatwoot/1/abc?flag", "https://bridge.example/api/webhooks/chatwoot/1/***?***"},
		{"fragment", "https://bridge.example/api/webhooks/chatwoot/1/abc#secret", "https://bridge.example/api/webhooks/chatwoot/1/***#***"},
		{"query token with no path segment", "https://bridge.example?token=secret", "https://bridge.example?token=***"},
		{"non-url", "not a url", "not a url"},
		{"non-url with slash", "foo/bar", "foo/***"},
		{"unicode path token", "https://bridge.example/api/webhooks/chatwoot/1/токен", "https://bridge.example/api/webhooks/chatwoot/1/***"},
		{"unicode query value", "https://bridge.example/api/webhooks/chatwoot/1/abc?token=секрет", "https://bridge.example/api/webhooks/chatwoot/1/***?token=***"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskURLToken(tt.in); got != tt.want {
				t.Errorf("maskURLToken(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
