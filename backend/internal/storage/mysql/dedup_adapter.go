package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/dedup"
)

// DedupAdapter exposes ProcessedMessages as the bridge.Dedup consumer-side port
// (ADR-0004). The two ports differ only where the bridge contract is stricter:
// MarkFailed is dedup.Store.Fail, and bridge.Dedup.Reclaim returns the new owner
// token it generated (dedup.Store.Reclaim takes one).
type DedupAdapter struct {
	repo *ProcessedMessages
}

// DedupAdapter implements the bridge.Dedup port.
var _ bridge.Dedup = (*DedupAdapter)(nil)

// NewDedupAdapter returns a DedupAdapter over pool, the shared *sql.DB. A nil
// pool is a programming error and panics (via NewProcessedMessages).
func NewDedupAdapter(pool *sql.DB) *DedupAdapter {
	return &DedupAdapter{repo: NewProcessedMessages(pool)}
}

// Claim atomically claims the bridge event key. Exactly one concurrent caller
// wins; a loser receives no token.
func (a *DedupAdapter) Claim(
	ctx context.Context,
	key bridge.Key,
	ttl time.Duration,
) (claimed bool, id int64, token string, err error) {
	return a.repo.Claim(ctx, dedup.Key{
		ConnectionID:    key.ConnectionID,
		SourceSystem:    key.SourceSystem,
		EventType:       key.EventType,
		SourceMessageID: key.SourceMessageID,
	}, ttl)
}

// Complete records a successful delivery and its destination id.
func (a *DedupAdapter) Complete(ctx context.Context, id int64, token, destinationMessageID string) error {
	return mapDedupError(a.repo.Complete(ctx, id, token, destinationMessageID))
}

// MarkDuplicate closes a bridge-originated/echo claim without delivering it.
func (a *DedupAdapter) MarkDuplicate(ctx context.Context, id int64, token string) error {
	return mapDedupError(a.repo.MarkDuplicate(ctx, id, token))
}

// MarkUnconfirmed moves a claim to the terminal unknown-outcome state. The
// engine never resends such a claim (ADR-0004 §5).
func (a *DedupAdapter) MarkUnconfirmed(ctx context.Context, id int64, token string) error {
	return mapDedupError(a.repo.MarkUnconfirmed(ctx, id, token))
}

// MarkFailed records a classified terminal failure.
func (a *DedupAdapter) MarkFailed(ctx context.Context, id int64, token, errorCode string) error {
	return mapDedupError(a.repo.Fail(ctx, id, token, errorCode))
}

// Reclaim takes over a stale processing row for reconciliation only. It
// generates the new owner token the bridge port returns; the repository
// independently bounds the cutoff by the DB clock's fifteen-minute floor.
func (a *DedupAdapter) Reclaim(ctx context.Context, id int64, staleBefore time.Time) (string, bool, error) {
	token, err := newClaimToken()
	if err != nil {
		return "", false, err
	}
	won, err := a.repo.Reclaim(ctx, id, token, staleBefore)
	if err != nil || !won {
		return "", false, err
	}
	return token, true, nil
}

// StorePayload persists the sanitized audit envelope for a claim already owned
// by the ingress. It is a pass-through so the adapter satisfies the ingress
// consumer-side port (httpapi.IngressStore).
func (a *DedupAdapter) StorePayload(ctx context.Context, id int64, token string, payload []byte) error {
	return a.repo.StorePayload(ctx, id, token, payload)
}

// Release removes a claim admitted but not delivered (pre-admission failure).
// It is a pass-through so the adapter satisfies the ingress consumer-side port
// (httpapi.IngressStore, ADR-0004 §1).
func (a *DedupAdapter) Release(ctx context.Context, id int64, token string) error {
	return mapDedupError(a.repo.Release(ctx, id, token))
}

// mapDedupError translates the dedup-owned ownership sentinel into the
// bridge-owned one so the engine's fail-closed handling
// (errors.Is(err, bridge.ErrNotOwner)) works without internal/bridge importing
// dedup. Every other error is returned unchanged.
func mapDedupError(err error) error {
	if err == nil || !errors.Is(err, dedup.ErrNotOwner) {
		return err
	}
	return fmt.Errorf("%w: %w", bridge.ErrNotOwner, err)
}
