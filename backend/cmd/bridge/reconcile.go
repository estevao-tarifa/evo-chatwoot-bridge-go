package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// Boot-reconcile bounds: a single scan is never treated as the whole backlog.
const (
	reconcileBatchSize  = 1000
	reconcileMaxBatches = 100
)

// staleRecoverer lists bounded ids of stale 'processing' claims.
type staleRecoverer interface {
	RecoverStale(ctx context.Context, olderThan time.Duration) ([]int64, error)
}

// reconcileFunc quarantines one stale claim. bridge.Engine.ReconcileStale
// satisfies it and never sends.
type reconcileFunc func(ctx context.Context, id int64, staleBefore time.Time) (bridge.Outcome, error)

// reconcileStale quarantines stale 'processing' claims at boot:
// RecoverStale → Reclaim → MarkUnconfirmed, never a send (ADR-0004 §8). It pages
// the bounded backlog (≤ reconcileBatchSize per scan, capped iterations) and
// warns if the cap is hit instead of pretending the backlog is empty. A failed
// per-claim reconcile is logged and the loop continues.
func reconcileStale(
	ctx context.Context,
	store staleRecoverer,
	reconcile reconcileFunc,
	staleAge time.Duration,
	logger *slog.Logger,
) error {
	staleBefore := time.Now().Add(-staleAge)
	scanned := 0
	for range reconcileMaxBatches {
		ids, err := store.RecoverStale(ctx, staleAge)
		if err != nil {
			return fmt.Errorf("list stale claims: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			scanned++
			if _, err := reconcile(ctx, id, staleBefore); err != nil {
				logger.Warn("boot reconcile: claim not quarantined", "claim_id", id, "error", err)
			}
		}
		if len(ids) < reconcileBatchSize {
			return nil
		}
	}
	logger.Warn("boot reconcile: batch cap reached; remaining stale claims deferred", "scanned", scanned)
	return nil
}
