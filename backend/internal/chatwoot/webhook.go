package chatwoot

import "github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/webhookauth"

// VerifyWebhookSignature reports whether signatureHeader authenticates rawBody
// for the given secret and timestamp, using the Chatwoot scheme
// (webhooks.md §4):
//
//	sha256=<hex(HMAC-SHA256(secret, timestamp + "." + rawBody))>
//
// The comparison is constant time. An empty secret, timestamp or signature, or
// a malformed header, is never valid. Replay-window enforcement (timestamp age)
// is left to the caller, which owns the request clock.
//
// The pure verification lives in the stdlib-only internal/webhookauth leaf so
// the HTTP ingress can verify the same signature without importing this client
// (ARCHITECTURE §2); this method is the frozen Chatwoot-client contract over it.
func VerifyWebhookSignature(secret, timestamp string, rawBody []byte, signatureHeader string) bool {
	return webhookauth.VerifySignature(secret, timestamp, rawBody, signatureHeader)
}
