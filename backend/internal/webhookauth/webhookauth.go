// Package webhookauth holds the pure, transport-free webhook signature
// verification shared by the Chatwoot client and the HTTP ingress. It imports
// only the standard library, so both adapters can depend on it without coupling
// to each other (ARCHITECTURE §2: the transport must not import the concrete
// clients).
package webhookauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const signaturePrefix = "sha256="

// VerifySignature reports whether signatureHeader authenticates rawBody for the
// given secret and timestamp, using the Chatwoot scheme (webhooks.md §4): the
// header is
//
//	sha256=<hex(HMAC-SHA256(secret, timestamp + "." + rawBody))>
//
// The comparison is constant time. An empty secret, timestamp or signature, or
// a malformed header, is never valid. Replay-window enforcement (timestamp age)
// is left to the caller, which owns the request clock.
func VerifySignature(secret, timestamp string, rawBody []byte, signatureHeader string) bool {
	if secret == "" || timestamp == "" || signatureHeader == "" {
		return false
	}

	provided, ok := strings.CutPrefix(signatureHeader, signaturePrefix)
	if !ok {
		provided = signatureHeader
	}
	provided = strings.TrimSpace(provided)
	if provided == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(rawBody)
	expected := mac.Sum(nil)

	got, err := hex.DecodeString(provided)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, got)
}
