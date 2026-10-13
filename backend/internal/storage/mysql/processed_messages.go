package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/dedup"
)

// This file is the MySQL adapter for dedup.Store (ADR-0004), relocated
// verbatim from internal/dedup so the domain package holds no SQL. It borrows
// the *sql.DB pool owned by this package; it never opens a pool of its own. The
// pool must use UTC and CLIENT_FOUND_ROWS=false. Valid UTF-8 identities are
// case/accent sensitive; trailing ASCII space is rejected because utf8mb4_bin
// uses PAD SPACE (ADR-0004 Amendment 3).

// Claim row id and column widths. The identity/error columns are VARCHAR; the
// widths mirror the schema so oversized input is rejected before MySQL (which
// would otherwise truncate or error). Counted in characters (utf8mb4), matching
// MySQL's VARCHAR semantics.
const (
	maxEventTypeLen       = 64
	maxSourceMessageIDLen = 255
	maxErrorCodeLen       = 64
)

// maxPayloadBytes caps sanitized_payload (ADR-0004 §10, 64 KiB). A larger
// payload is replaced by payloadTruncatedMarker (valid JSON) rather than
// truncated mid-document, which the JSON column would reject.
//
// The marker is explicitly lossy audit metadata, never replay data.
const maxPayloadBytes = 64 * 1024

// claimTokenBytes is the size of the random claim token stored in claimed_by
// (the column is CHAR(32); 16 bytes hex-encode to 32 chars).
const claimTokenBytes = 16

// cleanupBatchSize bounds each DELETE so cleanup never holds a long lock on a
// large backlog (ADR-0004 §6).
const cleanupBatchSize = 1000

// recoverBatchSize bounds a single RecoverStale scan so a large backlog cannot
// materialise unbounded memory at boot (ADR-0004 §8).
const recoverBatchSize = 1000

// minTTL is the smallest retention window Claim accepts. ttl is the row's
// retention (expires_at), NOT claim liveness — liveness is decided by
// status/claimed_at. Deployment retention remains seven days; the one-hour
// floor is only a guardrail (ADR-0004 Amendment 3).
const minTTL = time.Hour

const minStaleAge = 15 * time.Minute

// ProcessedMessages is a handle to the processed_messages table. It is safe for
// concurrent use: it holds no mutable state and delegates to the *sql.DB pool.
type ProcessedMessages struct {
	db *sql.DB
}

// ProcessedMessages implements the dedup.Store port.
var _ dedup.Store = (*ProcessedMessages)(nil)

// NewProcessedMessages returns a ProcessedMessages backed by pool, the *sql.DB
// owned by this package. A nil pool is a programming error (the repository
// borrows the shared pool and never opens one), so it panics rather than
// deferring the failure to the first query.
func NewProcessedMessages(pool *sql.DB) *ProcessedMessages {
	if pool == nil {
		panic("mysql: NewProcessedMessages requires a non-nil *sql.DB pool")
	}
	return &ProcessedMessages{db: pool}
}

// claimSQL is the atomic claim (ADR-0004 §1). Affected rows are 1 for the
// insert (this caller owns the event) and 0 for the deliberate no-op duplicate
// update. The DSN must NOT set clientFoundRows=true: it would report 1 for the
// duplicate too and every concurrent caller would believe it had won.
const claimSQL = `INSERT INTO processed_messages
		(connection_id, source_system, event_type, source_message_id,
		 status, attempt, claimed_by, claimed_at, expires_at)
	VALUES (?, ?, ?, ?, 'processing', 1, ?, NOW(6), NOW(6) + INTERVAL ? MICROSECOND)
	ON DUPLICATE KEY UPDATE status = status`

// Claim atomically claims the event identified by key for the retention window
// ttl.
//
// Exactly one concurrent caller wins. claimed is true only for the winner, and
// only the winner receives a non-empty token (the claimed_by ownership token
// required by every transition). id is the row id in both cases (the existing
// row for a loser). A loser must not deliver the event.
//
// New rows start status='processing', attempt=1, with claimed_by/claimed_at set
// and expires_at = now + ttl.
func (r *ProcessedMessages) Claim(ctx context.Context, key dedup.Key, ttl time.Duration) (claimed bool, id int64, token string, err error) {
	if err := validateKey(key); err != nil {
		return false, 0, "", err
	}
	if err := validateTTL(ttl); err != nil {
		return false, 0, "", err
	}
	token, err = newClaimToken()
	if err != nil {
		return false, 0, "", err
	}

	res, err := r.db.ExecContext(ctx, claimSQL,
		key.ConnectionID, key.SourceSystem, key.EventType, key.SourceMessageID,
		token, ttl.Microseconds())
	if err != nil {
		return false, 0, "", fmt.Errorf("claim %s event: %w", key.SourceSystem, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, 0, "", fmt.Errorf("claim affected rows: %w", err)
	}
	if affected == 1 {
		winner, err := res.LastInsertId()
		if err != nil {
			return false, 0, "", fmt.Errorf("claim row id: %w", err)
		}
		return true, winner, token, nil
	}

	// Duplicate: no state changed; the caller does not own the claim.
	id, err = r.existingID(ctx, key)
	if err != nil {
		return false, 0, "", err
	}
	return false, id, "", nil
}

const selectIDByKeySQL = `SELECT id FROM processed_messages
	WHERE connection_id = ? AND source_system = ? AND event_type = ? AND source_message_id = ?`

// existingID returns the id of the claim row that made a Claim lose the race.
func (r *ProcessedMessages) existingID(ctx context.Context, key dedup.Key) (int64, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, selectIDByKeySQL,
		key.ConnectionID, key.SourceSystem, key.EventType, key.SourceMessageID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The row vanished between the duplicate report and this read (a
			// concurrent cleanup). The caller must not deliver.
			return 0, fmt.Errorf("dedup: claim row for %s event disappeared", key.SourceSystem)
		}
		return 0, fmt.Errorf("read existing claim id: %w", err)
	}
	return id, nil
}

const completeSQL = `UPDATE processed_messages
	SET status = 'sent', destination_message_id = ?
	WHERE id = ? AND claimed_by = ? AND status = 'processing'`

// Complete records a successful delivery: status='sent' and the destination
// API's message id (used later for echo/loop lookup via idx_processed_dest).
// The update is guarded by token (ADR-0004 §4): a non-owner, or a row that is
// no longer 'processing', affects 0 rows and returns ErrNotOwner/ErrNotFound.
func (r *ProcessedMessages) Complete(ctx context.Context, id int64, token, destinationMessageID string) error {
	if err := validateIdentity(destinationMessageID, "destination message id", maxSourceMessageIDLen); err != nil {
		return err
	}
	return r.guardedUpdate(ctx, id, token, "complete", completeSQL, destinationMessageID, id, token)
}

const failSQL = `UPDATE processed_messages
	SET status = 'failed', error_code = ?
	WHERE id = ? AND claimed_by = ? AND status = 'processing'`

// Fail records a classified terminal failure. errorCode must be a sanitized
// classification, never raw error text or message content (AGENTS.md §8), and
// must fit the error_code column. Ownership is enforced like Complete.
func (r *ProcessedMessages) Fail(ctx context.Context, id int64, token, errorCode string) error {
	if err := validateIdentity(errorCode, "error code", maxErrorCodeLen); err != nil {
		return err
	}
	return r.guardedUpdate(ctx, id, token, "fail", failSQL, errorCode, id, token)
}

// MarkUnconfirmed records an unknown send outcome (timeout). The row becomes
// terminal and the engine must never auto-resend it (ADR-0004 §5, ADR-0005).
//
// The status ENUM has no dedicated 'unconfirmed_send' value; the ADR's
// mechanism is status='failed' with error_code='unconfirmed_send', which the
// engine treats as non-retryable (classify with dedup.IsUnconfirmedSend). Adding
// a distinct status would need a new migration plus an ADR amendment, so this
// repository keeps the schema frozen.
const unconfirmedSQL = `UPDATE processed_messages
	SET status = 'failed', error_code = 'unconfirmed_send'
	WHERE id = ? AND claimed_by = ? AND status = 'processing'`

// MarkUnconfirmed moves a claim to the terminal unknown-outcome state.
func (r *ProcessedMessages) MarkUnconfirmed(ctx context.Context, id int64, token string) error {
	return r.guardedUpdate(ctx, id, token, "mark unconfirmed", unconfirmedSQL, id, token)
}

const markDuplicateSQL = `UPDATE processed_messages
	SET status = 'duplicate'
	WHERE id = ? AND claimed_by = ? AND status = 'processing'`

// MarkDuplicate closes a claim that was intentionally NOT delivered because the
// event is bridge-originated/echo traffic (loop protection, ADR-0004 §3, rule
// 6). It is the only way to reach the 'duplicate' terminal state through the
// API, and it is ownership-guarded like every other transition.
func (r *ProcessedMessages) MarkDuplicate(ctx context.Context, id int64, token string) error {
	return r.guardedUpdate(ctx, id, token, "mark duplicate", markDuplicateSQL, id, token)
}

const reclaimSQL = `UPDATE processed_messages
  SET claimed_by = ?, claimed_at = NOW(6), attempt = attempt + 1
  WHERE id = ? AND status = 'processing' AND claimed_at < ?
  AND ? <= NOW(6) - INTERVAL 15 MINUTE
  AND (claimed_by IS NULL OR claimed_by <> ?) AND attempt < 255`

// Reclaim rotates a stale processing owner for reconciliation/quarantine ONLY.
// Neither age nor ownership proves non-delivery: never resend on this authority.
// The cutoff is independently bounded by the DB clock's fifteen-minute floor.
func (r *ProcessedMessages) Reclaim(ctx context.Context, id int64, newToken string, staleBefore time.Time) (bool, error) {
	if err := validateOwner(id, newToken); err != nil {
		return false, err
	}
	if staleBefore.IsZero() || staleBefore.After(time.Now()) {
		return false, errors.New("dedup: reclaim cutoff must be nonzero and not future")
	}
	cutoff := staleBefore.UTC().Truncate(time.Microsecond)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin reclaim: %w", err)
	}
	defer tx.Rollback() // Releases the row lock on all unsuccessful paths.
	var owner sql.NullString
	var attempt int
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT claimed_by, attempt,
  COALESCE(status = 'processing' AND claimed_at < ? AND ? <= NOW(6) - INTERVAL 15 MINUTE, FALSE)
  FROM processed_messages WHERE id = ? FOR UPDATE`, cutoff, cutoff, id).Scan(&owner, &attempt, &eligible)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read reclaim candidate: %w", err)
	}
	if owner.String == newToken {
		return false, errors.New("dedup: reclaim requires a different owner token")
	}
	if !eligible {
		return false, nil
	}
	if attempt >= 255 {
		return false, dedup.ErrAttemptExhausted
	}
	res, err := tx.ExecContext(ctx, reclaimSQL, newToken, id, cutoff, cutoff, newToken)
	if err != nil {
		return false, fmt.Errorf("reclaim claim %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reclaim affected rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit reclaim: %w", err)
	}
	return affected == 1, nil
}

const storePayloadSQL = `UPDATE processed_messages
	SET sanitized_payload = ?
	WHERE id = ? AND claimed_by = ? AND status = 'processing'`

// payloadTruncatedMarker replaces a payload above maxPayloadBytes. It is valid
// JSON, but explicitly lossy and not suitable for replay (Amendment 3).
var payloadTruncatedMarker = []byte(`{"truncated":true}`)

// StorePayload persists the sanitized event envelope (media stripped, no
// secrets, ADR-0004 Amendment 3) for audit only, not replay. Ingress owns
// allowlist sanitization. payload must be non-empty and valid JSON; an
// oversized payload is replaced by payloadTruncatedMarker. Ownership is
// enforced like the transitions.
func (r *ProcessedMessages) StorePayload(ctx context.Context, id int64, token string, payload []byte) error {
	if err := validateOwner(id, token); err != nil {
		return err
	}
	if len(payload) == 0 {
		return errors.New("dedup: payload must not be empty")
	}
	if !json.Valid(payload) {
		return errors.New("dedup: payload must be valid JSON")
	}
	if len(payload) > maxPayloadBytes {
		payload = payloadTruncatedMarker
	}
	res, err := r.db.ExecContext(ctx, storePayloadSQL, payload, id, token)
	if err != nil {
		return fmt.Errorf("store payload: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store payload affected rows: %w", err)
	}
	if affected != 0 {
		return nil
	}
	// MySQL reports zero for semantically identical JSON. Check ownership/status,
	// not existence alone; never use found-rows or a heartbeat to disguise no-ops.
	var owned bool
	err = r.db.QueryRowContext(ctx, `SELECT COALESCE(claimed_by = ? AND status = 'processing', FALSE)
  FROM processed_messages WHERE id = ?`, token, id).Scan(&owned)
	if errors.Is(err, sql.ErrNoRows) {
		return dedup.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check payload owner: %w", err)
	}
	if !owned {
		return dedup.ErrNotOwner
	}
	return nil
}

const releaseSQL = `DELETE FROM processed_messages
	WHERE id = ? AND claimed_by = ? AND status = 'processing'`

// Release removes a claim that was admitted (persisted) but never delivered,
// e.g. a pre-admission audit-write or queue-full failure (ADR-0004 §1). The
// DELETE is guarded by owner and status: 0 rows means the row is absent
// (ErrNotFound) or no longer owned/processing (ErrNotOwner) — never a silent
// no-op. A failed release preserves the row for the next-boot reconcile.
func (r *ProcessedMessages) Release(ctx context.Context, id int64, token string) error {
	if err := validateOwner(id, token); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, releaseSQL, id, token)
	if err != nil {
		return fmt.Errorf("release claim %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("release claim %d affected rows: %w", id, err)
	}
	if affected == 0 {
		return r.classifyMiss(ctx, id)
	}
	return nil
}

const loadPayloadSQL = `SELECT sanitized_payload FROM processed_messages WHERE id = ?`

// LoadPayload returns the sanitized envelope stored for id, or nil when the
// column is NULL. A missing id returns ErrNotFound. Loading never authorizes
// replay; unknown outcomes require reconciliation even when payload is absent.
func (r *ProcessedMessages) LoadPayload(ctx context.Context, id int64) ([]byte, error) {
	if id <= 0 {
		return nil, errors.New("dedup: claim id must be positive")
	}
	var payload []byte
	err := r.db.QueryRowContext(ctx, loadPayloadSQL, id).Scan(&payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: id %d", dedup.ErrNotFound, id)
		}
		return nil, fmt.Errorf("load payload %d: %w", id, err)
	}
	return payload, nil
}

const recoverStaleSQL = `SELECT id FROM processed_messages
	WHERE status = 'processing' AND claimed_at < NOW(6) - INTERVAL ? MICROSECOND
	ORDER BY id
	LIMIT ?`

// RecoverStale returns up to recoverBatchSize ids of rows still 'processing'
// whose claim is older than olderThan. It is a lister only, not ownership or
// proof of non-delivery. The coordinator must drain/page the bounded backlog.
func (r *ProcessedMessages) RecoverStale(ctx context.Context, olderThan time.Duration) ([]int64, error) {
	if olderThan < minStaleAge {
		return nil, fmt.Errorf("dedup: stale age must be at least %s", minStaleAge)
	}
	rows, err := r.db.QueryContext(ctx, recoverStaleSQL, olderThan.Microseconds(), recoverBatchSize)
	if err != nil {
		return nil, fmt.Errorf("query stale claims: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan stale claim id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale claims: %w", err)
	}
	return ids, nil
}

const cleanupExpiredSQL = `DELETE FROM processed_messages
 WHERE expires_at < NOW(6) AND
 (status IN ('sent', 'duplicate') OR
  (status = 'failed' AND (error_code IS NULL OR error_code <> 'unconfirmed_send')))
 LIMIT ?`

// CleanupExpired deletes rows past expires_at in bounded batches and returns how
// many were removed. Processing and unresolved failed/unconfirmed_send rows are
// preserved regardless of expiry, pending explicit reconciliation.
func (r *ProcessedMessages) CleanupExpired(ctx context.Context) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := r.db.ExecContext(ctx, cleanupExpiredSQL, cleanupBatchSize)
		if err != nil {
			return total, fmt.Errorf("cleanup expired claims: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("cleanup affected rows: %w", err)
		}
		total += n
		if n < cleanupBatchSize {
			return total, nil
		}
	}
}

const existsByIDSQL = `SELECT EXISTS(SELECT 1 FROM processed_messages WHERE id = ?)`

// guardedUpdate runs an ownership-guarded UPDATE and maps "0 rows affected" to
// a typed error: ErrNotFound when the id does not exist, ErrNotOwner when it
// exists but the caller does not own it (wrong token, or not 'processing').
func (r *ProcessedMessages) guardedUpdate(ctx context.Context, id int64, token, what, query string, args ...any) error {
	if err := validateOwner(id, token); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s claim %d: %w", what, id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s claim %d affected rows: %w", what, id, err)
	}
	if affected == 0 {
		return r.classifyMiss(ctx, id)
	}
	return nil
}

// classifyMiss distinguishes "no such row" from "not mine" for a guarded update
// that affected 0 rows. It costs one extra read only on the error path.
func (r *ProcessedMessages) classifyMiss(ctx context.Context, id int64) error {
	var exists bool
	if err := r.db.QueryRowContext(ctx, existsByIDSQL, id).Scan(&exists); err != nil {
		return fmt.Errorf("check claim %d existence: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("%w: id %d", dedup.ErrNotFound, id)
	}
	return fmt.Errorf("%w: id %d", dedup.ErrNotOwner, id)
}

// validateKey rejects a key that cannot be a valid claim identity: an unknown
// source system, a non-positive connection, or an empty/oversized event type or
// source message id. All checks run before any database access.
func validateKey(k dedup.Key) error {
	switch k.SourceSystem {
	case dedup.SourceEvolution, dedup.SourceChatwoot:
	default:
		return fmt.Errorf("dedup: source system %q is not one of %q, %q",
			k.SourceSystem, dedup.SourceEvolution, dedup.SourceChatwoot)
	}
	if k.ConnectionID <= 0 || uint64(k.ConnectionID) > 1<<32-1 {
		return fmt.Errorf("dedup: connection id must be positive, got %d", k.ConnectionID)
	}
	if err := validateIdentity(k.EventType, "event type", maxEventTypeLen); err != nil {
		return err
	}
	return validateIdentity(k.SourceMessageID, "source message id", maxSourceMessageIDLen)
}

func validateIdentity(value, field string, width int) error {
	if value == "" || !utf8.ValidString(value) {
		return fmt.Errorf("dedup: %s must be nonempty valid utf-8", field)
	}
	if value[len(value)-1] == ' ' {
		return fmt.Errorf("dedup: %s must not end in ascii space", field)
	}
	if utf8.RuneCountInString(value) > width {
		return fmt.Errorf("dedup: %s longer than %d characters", field, width)
	}
	return nil
}

func validateOwner(id int64, token string) error {
	if id <= 0 {
		return errors.New("dedup: claim id must be positive")
	}
	if len(token) != claimTokenBytes*2 {
		return errors.New("dedup: token must be 32 lowercase hex characters")
	}
	for _, c := range token {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return errors.New("dedup: token must be 32 lowercase hex characters")
		}
	}
	return nil
}

// validateTTL enforces a sane retention window. ttl is how long the claim row
// is retained (expires_at), not how long the claim is considered live; a window
// below minTTL cannot outlive an upstream re-delivery and can be born expired.
func validateTTL(ttl time.Duration) error {
	if ttl < minTTL {
		return fmt.Errorf("dedup: ttl %s is below the %s minimum retention window", ttl, minTTL)
	}
	return nil
}

// newClaimToken returns a random claim token for claimed_by (ADR-0004: an
// instance + claim token; no instance identity is configured in this slice, so
// the token alone is stored).
func newClaimToken() (string, error) {
	var buf [claimTokenBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate claim token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
