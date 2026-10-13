package dedup

import (
	"context"
	"time"
)

// Store is the processed_messages repository port (ADR-0004). It is declared by
// this package — the domain — and implemented by internal/storage/mysql
// (ProcessedMessages), so the domain never imports database/sql
// (ARCHITECTURE §2 rules 2–3). The concrete type is safe for concurrent use.
//
// The method set is exactly the repository's exported transitions: the atomic
// claim, the ownership-guarded terminal transitions, the audit payload
// accessors, the stale-claim lister, the guarded stale-owner takeover, and the
// bounded cleanup.
type Store interface {
	// Claim atomically claims the event identified by key for the retention
	// window ttl. Exactly one concurrent caller wins; claimed is true only for
	// the winner, and only the winner receives a non-empty token.
	Claim(ctx context.Context, key Key, ttl time.Duration) (claimed bool, id int64, token string, err error)
	// Complete records a successful delivery (status='sent' + destination id).
	Complete(ctx context.Context, id int64, token, destinationMessageID string) error
	// Fail records a classified terminal failure.
	Fail(ctx context.Context, id int64, token, errorCode string) error
	// MarkUnconfirmed moves a claim to the terminal unknown-outcome state.
	MarkUnconfirmed(ctx context.Context, id int64, token string) error
	// MarkDuplicate closes a bridge-originated/echo claim without delivering it.
	MarkDuplicate(ctx context.Context, id int64, token string) error
	// StorePayload persists the sanitized audit envelope for id.
	StorePayload(ctx context.Context, id int64, token string, payload []byte) error
	// Release removes a claim that was admitted (persisted) but never
	// delivered, e.g. a pre-admission audit-write or queue-full failure. It is
	// guarded by owner and status; a non-owner/non-processing row is an error,
	// never a silent no-op. A failed release preserves the row for the
	// next-boot reconcile (ADR-0004 §1).
	Release(ctx context.Context, id int64, token string) error
	// LoadPayload returns the stored audit envelope for id (nil when NULL).
	LoadPayload(ctx context.Context, id int64) ([]byte, error)
	// RecoverStale lists bounded ids of stale 'processing' rows.
	RecoverStale(ctx context.Context, olderThan time.Duration) ([]int64, error)
	// Reclaim rotates a stale processing owner for reconciliation/quarantine only.
	Reclaim(ctx context.Context, id int64, newToken string, staleBefore time.Time) (bool, error)
	// CleanupExpired deletes expired terminal rows in bounded batches.
	CleanupExpired(ctx context.Context) (int64, error)
}
