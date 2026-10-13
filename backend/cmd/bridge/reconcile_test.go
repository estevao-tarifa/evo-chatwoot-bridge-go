package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// fakeRecoverer returns scripted batches and counts scans.
type fakeRecoverer struct {
	batches [][]int64
	err     error
	scans   int
}

func (f *fakeRecoverer) RecoverStale(_ context.Context, _ time.Duration) ([]int64, error) {
	f.scans++
	if f.err != nil {
		return nil, f.err
	}
	if f.scans-1 < len(f.batches) {
		return f.batches[f.scans-1], nil
	}
	return nil, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestSlice4_ReconcileStaleQuarantinesAndPages proves the boot reconcile
// quarantines every stale claim (never sends) and pages a full batch instead of
// treating it as the whole backlog.
func TestSlice4_ReconcileStaleQuarantinesAndPages(t *testing.T) {
	full := make([]int64, reconcileBatchSize)
	for i := range full {
		full[i] = int64(i + 1)
	}
	store := &fakeRecoverer{batches: [][]int64{full, {1001, 1002}}}

	var reconciled []int64
	reconcile := func(_ context.Context, id int64, _ time.Time) (bridge.Outcome, error) {
		reconciled = append(reconciled, id)
		return bridge.OutcomeQuarantined, nil
	}

	if err := reconcileStale(context.Background(), store, reconcile, 15*time.Minute, discardLogger()); err != nil {
		t.Fatalf("reconcileStale() error = %v", err)
	}
	if len(reconciled) != reconcileBatchSize+2 {
		t.Errorf("reconciled %d claims, want %d", len(reconciled), reconcileBatchSize+2)
	}
	if store.scans != 2 {
		t.Errorf("scans = %d, want 2 (a full batch must page)", store.scans)
	}
}

// TestSlice4_ReconcileStaleContinuesAfterError proves a single failed claim does
// not abort the loop and a store error is surfaced.
func TestSlice4_ReconcileStaleContinuesAfterError(t *testing.T) {
	store := &fakeRecoverer{batches: [][]int64{{1, 2}}}
	var calls int
	reconcile := func(_ context.Context, _ int64, _ time.Time) (bridge.Outcome, error) {
		calls++
		return bridge.OutcomeFailed, errors.New("reclaim failed")
	}
	if err := reconcileStale(context.Background(), store, reconcile, 15*time.Minute, discardLogger()); err != nil {
		t.Fatalf("reconcileStale() error = %v, want nil (per-claim errors are logged)", err)
	}
	if calls != 2 {
		t.Errorf("reconcile calls = %d, want 2", calls)
	}

	store.err = errors.New("db down")
	if err := reconcileStale(context.Background(), store, reconcile, 15*time.Minute, discardLogger()); err == nil {
		t.Fatal("reconcileStale() error = nil, want the store error")
	}
}
