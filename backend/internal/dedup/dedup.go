// Package dedup is the domain half of the persistent idempotency repository
// over the processed_messages table (ADR-0004): the single source of truth for
// "have I seen this event and what happened to it". It owns the event identity
// (Key), the terminal unknown-outcome classification (IsUnconfirmedSend), the
// sentinel errors, and declares the Store port.
//
// It imports no database/sql, transport or client package (ARCHITECTURE §2
// rules 2–3). The SQL implementation lives in internal/storage/mysql
// (ProcessedMessages), which imports this package for its types.
//
// Claim ownership is enforced on every transition (ADR-0004 §4, Amendment 2):
// Claim returns the claimed_by token, and Complete/Fail/MarkUnconfirmed/
// MarkDuplicate/StorePayload update with `WHERE id = ? AND claimed_by = ?
// AND status = 'processing'`. A non-owner update affects 0 rows and returns a
// typed error (ErrNotOwner / ErrNotFound) — never a silent no-op.
package dedup

import "errors"

// Source systems for a dedup key. The database column is an ENUM with exactly
// these values; the adapter rejects anything else before touching MySQL.
const (
	SourceEvolution = "evolution"
	SourceChatwoot  = "chatwoot"
)

// errorCodeUnconfirmed marks a send whose outcome is unknown (a timeout on the
// send call). The row is terminal and the engine must never auto-resend it
// (ADR-0004 §5, ADR-0005, ROUTING_MATRIX §3.4): a blind retry risks a double
// send, so reconciliation is a manual/admin step. Use IsUnconfirmedSend to
// classify a stored error_code without depending on this literal.
const errorCodeUnconfirmed = "unconfirmed_send"

// ErrAttemptExhausted requires reconciliation without another ownership rotation.
var ErrAttemptExhausted = errors.New("dedup: claim attempts exhausted")

// ErrNotFound is returned when no processed_messages row has the given id.
var ErrNotFound = errors.New("dedup: claim not found")

// ErrNotOwner is returned when the row exists but is not owned by the caller's
// claim token, or is no longer 'processing'. The caller must not mutate it.
var ErrNotOwner = errors.New("dedup: claim not owned by caller")

// Key is the stable identity of an event. It is never derived from message
// content (ADR-0004). ConnectionID scopes the key to one connection; EventType
// is part of the key because message ids are namespaced by event kind (a
// reaction or receipt references the id of the message it targets).
type Key struct {
	ConnectionID    int
	SourceSystem    string
	EventType       string
	SourceMessageID string
}

// IsUnconfirmedSend reports whether errorCode is the terminal unknown-outcome
// classification (ADR-0004 §5). The engine must never auto-resend such a row;
// this predicate lets callers classify a stored error_code without depending on
// the literal.
func IsUnconfirmedSend(errorCode string) bool {
	return errorCode == errorCodeUnconfirmed
}
