package httpapi

import (
	"strconv"
	"strings"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/webhookauth"
)

// chatwootSignatureMaxAge bounds the replay window of a signed Chatwoot
// webhook. The exact timestamp unit the upstream sends is
// [needs live validation]; Unix seconds is assumed until the Phase 7 pilot.
const chatwootSignatureMaxAge = 5 * time.Minute

// ChatwootSignatureValid reports whether a Chatwoot webhook request is
// authorized in signed mode. It is the frozen fail-closed contract for the
// deferred Chatwoot ingress handler: an empty secret, a missing or
// invalid signature, or a timestamp outside the replay window is never valid.
// There is no silent fallback to token-only when the secret is configured
// (ADR-0006 Amendment 3, ADR-0009 §3).
func ChatwootSignatureValid(secret, timestamp string, rawBody []byte, signatureHeader string, now time.Time) bool {
	if secret == "" || !webhookauth.VerifySignature(secret, timestamp, rawBody, signatureHeader) {
		return false
	}
	unixSeconds, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(unixSeconds, 0))
	return age <= chatwootSignatureMaxAge && age >= -chatwootSignatureMaxAge
}
