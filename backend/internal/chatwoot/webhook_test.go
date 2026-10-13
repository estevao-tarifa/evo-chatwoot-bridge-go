package chatwoot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// sign computes the Chatwoot signature independently of the implementation.
func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature(t *testing.T) {
	const (
		secret    = "s3cr3t"
		timestamp = "1750000000"
	)
	body := []byte(`{"event":"message_created","id":10001}`)

	tests := []struct {
		name      string
		secret    string
		timestamp string
		body      []byte
		header    string
		want      bool
	}{
		{
			name:      "valid",
			secret:    secret,
			timestamp: timestamp,
			body:      body,
			header:    sign(secret, timestamp, body),
			want:      true,
		},
		{
			name:      "valid without sha256 prefix",
			secret:    secret,
			timestamp: timestamp,
			body:      body,
			header:    strings.TrimPrefix(sign(secret, timestamp, body), "sha256="),
			want:      true,
		},
		{
			name:      "valid uppercase hex",
			secret:    secret,
			timestamp: timestamp,
			body:      body,
			header:    "sha256=" + strings.ToUpper(strings.TrimPrefix(sign(secret, timestamp, body), "sha256=")),
			want:      true,
		},
		{
			name:      "wrong secret",
			secret:    secret,
			timestamp: timestamp,
			body:      body,
			header:    sign("other", timestamp, body),
			want:      false,
		},
		{
			name:      "tampered body",
			secret:    secret,
			timestamp: timestamp,
			body:      []byte(`{"event":"message_created","id":99999}`),
			header:    sign(secret, timestamp, body),
			want:      false,
		},
		{
			name:      "wrong timestamp",
			secret:    secret,
			timestamp: "1750000001",
			body:      body,
			header:    sign(secret, timestamp, body),
			want:      false,
		},
		{
			name:      "empty secret",
			secret:    "",
			timestamp: timestamp,
			body:      body,
			header:    sign(secret, timestamp, body),
			want:      false,
		},
		{
			name:      "empty timestamp",
			secret:    secret,
			timestamp: "",
			body:      body,
			header:    sign(secret, timestamp, body),
			want:      false,
		},
		{
			name:      "empty header",
			secret:    secret,
			timestamp: timestamp,
			body:      body,
			header:    "",
			want:      false,
		},
		{
			name:      "malformed hex",
			secret:    secret,
			timestamp: timestamp,
			body:      body,
			header:    "sha256=zzzz",
			want:      false,
		},
		{
			name:      "prefix only",
			secret:    secret,
			timestamp: timestamp,
			body:      body,
			header:    "sha256=",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VerifyWebhookSignature(tt.secret, tt.timestamp, tt.body, tt.header)
			if got != tt.want {
				t.Errorf("VerifyWebhookSignature() = %v, want %v", got, tt.want)
			}
		})
	}
}
