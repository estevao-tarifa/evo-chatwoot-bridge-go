package mysql

// processed_messages_qa_test.go — independent QA verification, relocated from
// internal/dedup with the MySQL adapter.
//
// Reproduces the slice's claims from ADR-0004 (not the implementation) and
// probes edge cases the implementation tests did not cover. Synthetic data only.
// Updated for the binding Amendment 3 API; requires independent re-review.
// MySQL-gated tests reuse the testRepo helper.

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// ─── Claim: validation rejects bad input before touching the database ────────

func TestQA_ClaimValidation(t *testing.T) {
	repo := NewProcessedMessages(unconnectedPool(t))
	valid := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-VALID"}

	tests := []struct {
		name string
		key  Key
		ttl  time.Duration
	}{
		{name: "unknown source system", key: Key{ConnectionID: 1, SourceSystem: "telegram", EventType: "message", SourceMessageID: "x"}, ttl: time.Hour},
		{name: "empty source system", key: Key{ConnectionID: 1, EventType: "message", SourceMessageID: "x"}, ttl: time.Hour},
		{name: "zero ttl", key: valid, ttl: 0},
		{name: "negative ttl", key: valid, ttl: -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claimed, _, _, err := repo.Claim(t.Context(), tt.key, tt.ttl)
			if err == nil || !strings.HasPrefix(err.Error(), "dedup:") {
				t.Fatal("Claim() error = nil, want a validation error")
			}
			if claimed {
				t.Error("Claim() claimed = true for invalid input")
			}
		})
	}
}

// ─── Claim: terminal states are never re-claimed (ADR-0004 §2) ───────────────

func TestQA_ClaimTerminalStatesNotReclaimed(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	tests := []struct {
		name  string
		state string
		apply func(t *testing.T, id int64, token string)
	}{
		{
			name:  "sent",
			state: "sent",
			apply: func(t *testing.T, id int64, token string) {
				if err := repo.Complete(ctx, id, token, "SYNTHETIC-DEST"); err != nil {
					t.Fatalf("Complete() error = %v", err)
				}
			},
		},
		{
			name:  "failed",
			state: "failed",
			apply: func(t *testing.T, id int64, token string) {
				if err := repo.Fail(ctx, id, token, "upstream_5xx"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
		},
		{
			name:  "duplicate",
			state: "duplicate",
			apply: func(t *testing.T, id int64, token string) {
				if err := repo.MarkDuplicate(ctx, id, token); err != nil {
					t.Fatalf("set duplicate: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-TERMINAL-" + tt.state}
			claimed, id, token, err := repo.Claim(ctx, key, time.Hour)
			if err != nil || !claimed {
				t.Fatalf("first Claim() = (%v, %d, %v), want a winner", claimed, id, err)
			}
			tt.apply(t, id, token)

			reclaimed, againID, againToken, err := repo.Claim(ctx, key, time.Hour)
			if err != nil {
				t.Fatalf("second Claim() error = %v", err)
			}
			if reclaimed {
				t.Errorf("second Claim() claimed = true, want false (%s is terminal)", tt.state)
			}
			if againID != id {
				t.Errorf("second Claim() id = %d, want %d", againID, id)
			}
			if againToken != "" {
				t.Error("terminal redelivery received an owner token")
			}

			var status string
			if err := repo.db.QueryRowContext(ctx, `SELECT status FROM processed_messages WHERE id = ?`, id).Scan(&status); err != nil {
				t.Fatalf("read row: %v", err)
			}
			if status != tt.state {
				t.Errorf("status = %q, want %q (terminal row must be untouched)", status, tt.state)
			}
		})
	}
}

// ─── Claim: source_system is part of the key ─────────────────────────────────

func TestQA_ClaimDistinctSourceSystems(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	evo := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-SAME-ID"}
	cw := Key{ConnectionID: 1, SourceSystem: SourceChatwoot, EventType: "message", SourceMessageID: "SYNTHETIC-SAME-ID"}

	claimedEvo, evoID, _, err := repo.Claim(ctx, evo, time.Hour)
	if err != nil || !claimedEvo {
		t.Fatalf("Claim(evolution) = (%v, %d, %v), want a winner", claimedEvo, evoID, err)
	}
	claimedCw, cwID, _, err := repo.Claim(ctx, cw, time.Hour)
	if err != nil || !claimedCw {
		t.Fatalf("Claim(chatwoot) = (%v, %d, %v), want a winner", claimedCw, cwID, err)
	}
	if evoID == cwID {
		t.Errorf("same id under different source systems share row %d, want distinct rows", evoID)
	}

	var rows int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_messages`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Errorf("rows = %d, want 2", rows)
	}
}

// ─── RecoverStale: boundary around the threshold ─────────────────────────────

func TestQA_RecoverStaleBoundary(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	justBelow, _ := claimOne(t, repo, "SYNTHETIC-STALE-BELOW", "message") // 14m59s old
	justAbove, _ := claimOne(t, repo, "SYNTHETIC-STALE-ABOVE", "message") // 15m01s old

	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET claimed_at = NOW(6) - INTERVAL ? MICROSECOND WHERE id = ?`,
		(14*60+59)*1_000_000, justBelow); err != nil {
		t.Fatalf("age justBelow: %v", err)
	}
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET claimed_at = NOW(6) - INTERVAL ? MICROSECOND WHERE id = ?`,
		(15*60+1)*1_000_000, justAbove); err != nil {
		t.Fatalf("age justAbove: %v", err)
	}

	ids, err := repo.RecoverStale(ctx, 15*time.Minute)
	if err != nil {
		t.Fatalf("RecoverStale() error = %v", err)
	}
	if contains(ids, justBelow) {
		t.Errorf("RecoverStale() = %v, must not contain id %d (just under threshold)", ids, justBelow)
	}
	if !contains(ids, justAbove) {
		t.Errorf("RecoverStale() = %v, want it to contain id %d (just over threshold)", ids, justAbove)
	}
}

// ─── CleanupExpired: keeps rows whose expiry is still in the future ──────────

func TestQA_CleanupExpiredKeepsFutureRows(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	// A 1h claim (future expiry) and a row with a small explicit future margin.
	futureID, futureToken := claimOne(t, repo, "SYNTHETIC-CLEANUP-FUTURE", "message")
	marginID, marginToken := claimOne(t, repo, "SYNTHETIC-CLEANUP-MARGIN", "message")
	for id, token := range map[int64]string{futureID: futureToken, marginID: marginToken} {
		if err := repo.MarkDuplicate(ctx, id, token); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET expires_at = NOW(6) + INTERVAL 1 SECOND WHERE id = ?`, marginID); err != nil {
		t.Fatalf("set margin: %v", err)
	}

	removed, err := repo.CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	if removed != 0 {
		t.Errorf("CleanupExpired() removed = %d, want 0", removed)
	}

	var futureExists, marginExists bool
	if err := repo.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_messages WHERE id = ?), EXISTS(SELECT 1 FROM processed_messages WHERE id = ?)`,
		futureID, marginID).Scan(&futureExists, &marginExists); err != nil {
		t.Fatalf("check rows: %v", err)
	}
	if !futureExists || !marginExists {
		t.Errorf("future rows deleted: future=%v margin=%v, want both true", futureExists, marginExists)
	}
}

// Missing claims must be distinguishable from durable completion.
func TestQA_CompleteMissingIDRejected(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	const missingID = 987654321
	if err := repo.Complete(ctx, missingID, strings.Repeat("a", 32), "SYNTHETIC-DEST"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Complete(missing) error = %v, want ErrNotFound", err)
	}

	var exists bool
	if err := repo.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_messages WHERE id = ?)`, missingID).Scan(&exists); err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Errorf("row %d exists, want no row", missingID)
	}
}

// Sub-microsecond retention must fail without creating an already-expired row.
func TestQA_ClaimSubMicrosecondTTL(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-TTL-SUB"}

	claimed, id, token, err := repo.Claim(ctx, key, time.Nanosecond)
	if err == nil || claimed || id != 0 || token != "" {
		t.Fatal("Claim(1ns) must reject with zero result values")
	}

	var rows int
	if err := repo.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM processed_messages`).Scan(&rows); err != nil {
		t.Fatalf("read expiry: %v", err)
	}
	if rows != 0 {
		t.Errorf("invalid ttl created %d rows", rows)
	}
}

// Cleanup must never delete processing, even if its retention expired.

func TestQA_ConcurrentCompleteAndCleanup(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	ids := make([]int64, 0, 16)
	tokens := make(map[int64]string)
	for i := range 16 {
		id, token := claimOne(t, repo, "SYNTHETIC-RACE-"+string(rune('A'+i)), "message")
		ids = append(ids, id)
		tokens[id] = token
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE processed_messages SET expires_at = NOW(6) - INTERVAL 1 DAY`); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 2)
	go func() {
		for _, id := range ids {
			if err := repo.Complete(ctx, id, tokens[id], "SYNTHETIC-DEST"); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	go func() {
		_, err := repo.CleanupExpired(ctx)
		done <- err
	}()

	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent op error = %v", err)
		}
	}

	// Complete must succeed for every active owner; cleanup may delete only after
	// each row transitions to sent. Any remaining rows must be terminal.
	var nonSent int
	if err := repo.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM processed_messages WHERE status <> 'sent'`).Scan(&nonSent); err != nil {
		t.Fatalf("count: %v", err)
	}
	if nonSent != 0 {
		t.Errorf("non-sent rows = %d, want 0", nonSent)
	}
}

// ─── helper guard: errors.Is sanity for the domain errors ────────────────────

func TestQA_SentinelErrorShape(t *testing.T) {
	repo := NewProcessedMessages(unconnectedPool(t))
	_, _, _, err := repo.Claim(t.Context(), Key{SourceSystem: "nope"}, time.Hour)
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Error("validation error must not be sql.ErrNoRows")
	}
}
