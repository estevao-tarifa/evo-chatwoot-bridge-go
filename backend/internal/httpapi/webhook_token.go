package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
)

// webhookTokenPrefix namespaces the derived token so the secret is never used
// directly as a MAC input (ADR-0006 Amendment 2).
const webhookTokenPrefix = "webhook:"

// DeriveWebhookToken returns the per-connection webhook path token:
//
//	base64url( HMAC-SHA256(secret, "webhook:" + connectionID) )
//
// It is stable per connection and secret (rotation = rotate the secret) and is
// never stored (ADR-0006 Amendment 2). It is exported so ops can build the
// webhook URL; the token is a bearer secret and must never be logged.
func DeriveWebhookToken(secret string, connectionID int) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(webhookTokenPrefix + strconv.Itoa(connectionID)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyWebhookToken reports whether the presented path token authenticates the
// connection. An empty secret never derives a token (it would be predictable)
// and an empty presented value is never valid. The comparison is constant time.
func verifyWebhookToken(secret string, connectionID int, presented string) bool {
	if secret == "" || presented == "" {
		return false
	}
	expected := DeriveWebhookToken(secret, connectionID)
	return hmac.Equal([]byte(expected), []byte(presented))
}
