package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/dedup"
)

// The integration tests below were relocated from internal/dedup unchanged.
// These aliases let the bodies keep reading against the dedup domain types
// while the implementation under test is now mysql.ProcessedMessages.
type Key = dedup.Key

const (
	SourceEvolution = dedup.SourceEvolution
	SourceChatwoot  = dedup.SourceChatwoot
)

var (
	ErrNotFound         = dedup.ErrNotFound
	ErrNotOwner         = dedup.ErrNotOwner
	ErrAttemptExhausted = dedup.ErrAttemptExhausted
)

// ─── integration tests: gated by TEST_MYSQL_DSN ──────────────────────────────
//
// Every test below shares the single schema named by TEST_MYSQL_DSN. They are
// NOT process-parallel-safe: they must not run concurrently with another
// `go test` process against the same database. Each case resets the disposable
// schema (cleanTables) and reapplies the migrations, so the shuffled order in
// one serial package run (`go test -p 1 ...`) is safe.

// TestClaimConcurrentSingleWinner locks down ADR-0004 §1: ≥8 concurrent claims
// on the same key yield exactly one winner and exactly one row, every loser
// receives the winner's row id, and only the winner receives a claim token.
func TestClaimConcurrentSingleWinner(t *testing.T) {
	repo := testRepo(t)
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-CONCURRENT-1"}

	const workers = 8
	type outcome struct {
		claimed bool
		id      int64
		token   string
		err     error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, id, token, err := NewProcessedMessages(repo.db).Claim(t.Context(), key, time.Hour)
			outcomes <- outcome{claimed, id, token, err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	collected := make([]outcome, 0, workers)
	for o := range outcomes {
		collected = append(collected, o)
	}

	var winners int
	var winnerID int64
	var winnerToken string
	for _, o := range collected {
		if o.err != nil {
			t.Fatalf("concurrent Claim() error = %v", o.err)
		}
		if o.id <= 0 {
			t.Errorf("Claim() id = %d, want a positive row id", o.id)
		}
		if o.claimed {
			winners++
			winnerID = o.id
			winnerToken = o.token
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1", winners)
	}
	if winnerToken == "" {
		t.Error("winner token is empty, want a non-empty claim token")
	}

	// Every loser must receive the winner's row id and no token.
	var losers int
	for _, o := range collected {
		if o.claimed {
			continue
		}
		losers++
		if o.id != winnerID {
			t.Errorf("loser id = %d, want winner id %d", o.id, winnerID)
		}
		if o.token != "" {
			t.Error("loser received an owner token")
		}
	}
	if losers != workers-1 {
		t.Errorf("losers = %d, want %d", losers, workers-1)
	}

	var rows int
	if err := repo.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM processed_messages`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("processed_messages rows = %d, want 1", rows)
	}
	if winnerID <= 0 {
		t.Fatal("no winner row id recorded")
	}
}

// TestClaimCaseSensitiveIDs locks down the ADR-0004 Amendment (review M1): the
// claim key is compared byte-exactly, so ids differing only by case are two
// distinct claims, not a false duplicate.
func TestClaimCaseSensitiveIDs(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	base := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message"}

	upper := base
	upper.SourceMessageID = "AbC"
	lower := base
	lower.SourceMessageID = "abc"

	claimedUpper, upperID, _, err := repo.Claim(ctx, upper, time.Hour)
	if err != nil {
		t.Fatalf("Claim(AbC) error = %v", err)
	}
	claimedLower, lowerID, _, err := repo.Claim(ctx, lower, time.Hour)
	if err != nil {
		t.Fatalf("Claim(abc) error = %v", err)
	}
	if !claimedUpper || !claimedLower {
		t.Fatalf("case-different ids claimed = %v/%v, want both true", claimedUpper, claimedLower)
	}
	if upperID == lowerID {
		t.Errorf("AbC and abc share row id %d, want distinct rows", upperID)
	}

	// The exact id is a true duplicate.
	claimedAgain, againID, _, err := repo.Claim(ctx, upper, time.Hour)
	if err != nil {
		t.Fatalf("re-Claim(AbC) error = %v", err)
	}
	if claimedAgain {
		t.Error("re-Claim(AbC) claimed = true, want false (true duplicate)")
	}
	if againID != upperID {
		t.Errorf("re-Claim(AbC) id = %d, want %d", againID, upperID)
	}

	var rows int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_messages`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Errorf("processed_messages rows = %d, want 2", rows)
	}
}

// TestClaimTransitions covers the terminal state transitions: Complete → sent,
// Fail → failed/error_code, MarkUnconfirmed → failed/unconfirmed_send, and
// MarkDuplicate → duplicate.
func TestClaimTransitions(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	tests := []struct {
		name       string
		eventType  string
		apply      func(t *testing.T, id int64, token string)
		wantStatus string
		wantDest   sql.NullString
		wantErr    sql.NullString
	}{
		{
			name:      "complete",
			eventType: "message",
			apply: func(t *testing.T, id int64, token string) {
				if err := repo.Complete(ctx, id, token, "SYNTHETIC-DEST-1"); err != nil {
					t.Fatalf("Complete() error = %v", err)
				}
			},
			wantStatus: "sent",
			wantDest:   sql.NullString{String: "SYNTHETIC-DEST-1", Valid: true},
		},
		{
			name:      "fail",
			eventType: "message",
			apply: func(t *testing.T, id int64, token string) {
				if err := repo.Fail(ctx, id, token, "upstream_5xx"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
			wantStatus: "failed",
			wantErr:    sql.NullString{String: "upstream_5xx", Valid: true},
		},
		{
			name:      "mark unconfirmed",
			eventType: "send_message",
			apply: func(t *testing.T, id int64, token string) {
				if err := repo.MarkUnconfirmed(ctx, id, token); err != nil {
					t.Fatalf("MarkUnconfirmed() error = %v", err)
				}
			},
			wantStatus: "failed",
			wantErr:    sql.NullString{String: "unconfirmed_send", Valid: true},
		},
		{
			name:      "mark duplicate",
			eventType: "message",
			apply: func(t *testing.T, id int64, token string) {
				if err := repo.MarkDuplicate(ctx, id, token); err != nil {
					t.Fatalf("MarkDuplicate() error = %v", err)
				}
			},
			wantStatus: "duplicate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: tt.eventType, SourceMessageID: "SYNTHETIC-TRANS-" + tt.name}
			claimed, id, token, err := repo.Claim(ctx, key, time.Hour)
			if err != nil || !claimed {
				t.Fatalf("Claim() = (%v, %d, %v), want a winner", claimed, id, err)
			}

			tt.apply(t, id, token)

			var status string
			var dest, errCode sql.NullString
			if err := repo.db.QueryRowContext(ctx,
				`SELECT status, destination_message_id, error_code FROM processed_messages WHERE id = ?`, id).
				Scan(&status, &dest, &errCode); err != nil {
				t.Fatalf("read row %d: %v", id, err)
			}
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if dest != tt.wantDest {
				t.Errorf("destination_message_id = %+v, want %+v", dest, tt.wantDest)
			}
			if errCode != tt.wantErr {
				t.Errorf("error_code = %+v, want %+v", errCode, tt.wantErr)
			}
		})
	}
}

// TestClaimAfterCompleteDoesNotReclaim verifies a terminal row is never
// re-claimed: the row exists, so the second Claim is a loser.
func TestClaimAfterCompleteDoesNotReclaim(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-RECLAIM-1"}

	claimed, id, token, err := repo.Claim(ctx, key, time.Hour)
	if err != nil || !claimed {
		t.Fatalf("first Claim() = (%v, %d, %v), want a winner", claimed, id, err)
	}
	if err := repo.Complete(ctx, id, token, "SYNTHETIC-DEST-2"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	reclaimed, againID, againToken, err := repo.Claim(ctx, key, time.Hour)
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if reclaimed {
		t.Error("second Claim() claimed = true, want false (row exists)")
	}
	if againID != id {
		t.Errorf("second Claim() id = %d, want %d", againID, id)
	}
	if againToken != "" {
		t.Error("second Claim() received an owner token")
	}

	var status string
	if err := repo.db.QueryRowContext(ctx, `SELECT status FROM processed_messages WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "sent" {
		t.Errorf("status = %q, want sent (terminal row untouched)", status)
	}
}

// TestClaimOwnershipGuard locks down ADR-0004 §4 / Amendment 2: every terminal
// transition is guarded by the claim token. A non-owner update affects 0 rows
// and returns ErrNotOwner; a missing id returns ErrNotFound; the owner succeeds
// exactly once (a second transition is no longer 'processing').
func TestClaimOwnershipGuard(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	transitions := []struct {
		name  string
		apply func(id int64, token string) error
	}{
		{"complete", func(id int64, token string) error {
			return repo.Complete(ctx, id, token, "SYNTHETIC-DEST")
		}},
		{"fail", func(id int64, token string) error {
			return repo.Fail(ctx, id, token, "upstream_5xx")
		}},
		{"mark unconfirmed", func(id int64, token string) error {
			return repo.MarkUnconfirmed(ctx, id, token)
		}},
		{"mark duplicate", func(id int64, token string) error {
			return repo.MarkDuplicate(ctx, id, token)
		}},
	}

	for _, tr := range transitions {
		t.Run(tr.name, func(t *testing.T) {
			key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-OWNER-" + tr.name}
			claimed, id, token, err := repo.Claim(ctx, key, time.Hour)
			if err != nil || !claimed {
				t.Fatalf("Claim() = (%v, %d, %v), want a winner", claimed, id, err)
			}

			if err := tr.apply(id, otherToken(token)); !errors.Is(err, ErrNotOwner) {
				t.Errorf("non-owner %s error = %v, want ErrNotOwner", tr.name, err)
			}
			if err := tr.apply(987654321, token); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s on missing id error = %v, want ErrNotFound", tr.name, err)
			}
			if err := tr.apply(id, token); err != nil {
				t.Errorf("owner %s error = %v, want nil", tr.name, err)
			}
			if err := tr.apply(id, token); !errors.Is(err, ErrNotOwner) {
				t.Errorf("second %s error = %v, want ErrNotOwner (no longer processing)", tr.name, err)
			}
			// Every terminal state is immutable to every mutation, even its old owner.
			for _, mutation := range mutations(ctx, repo, id, token) {
				if err := mutation(); !errors.Is(err, ErrNotOwner) {
					t.Errorf("terminal %s mutation error = %v, want ErrNotOwner", tr.name, err)
				}
			}
		})
	}
}

// TestReclaimStale locks down ADR-0004 §2/§8: Reclaim atomically takes over a
// stale 'processing' row (new owner, attempt+1), loses on a fresh row, and never
// touches a terminal row.
func TestReclaimStale(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	staleID, _ := claimOne(t, repo, "SYNTHETIC-RECLAIM-STALE", "message")
	freshID, freshToken := claimOne(t, repo, "SYNTHETIC-RECLAIM-FRESH", "message")

	now := time.Now().UTC()
	// Pin both claimed_at values to the Go clock so the comparison does not
	// depend on the server's time zone.
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET claimed_at = ? WHERE id = ?`, now.Add(-20*time.Minute), staleID); err != nil {
		t.Fatalf("age stale row: %v", err)
	}
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET claimed_at = ? WHERE id = ?`, now, freshID); err != nil {
		t.Fatalf("pin fresh row: %v", err)
	}
	staleBefore := now.Add(-15 * time.Minute)

	newToken := strings.Repeat("a", 32)
	won, err := repo.Reclaim(ctx, staleID, newToken, staleBefore)
	if err != nil {
		t.Fatalf("Reclaim(stale) error = %v", err)
	}
	if !won {
		t.Fatal("Reclaim(stale) won = false, want true")
	}

	var owner, status string
	var attempt int
	if err := repo.db.QueryRowContext(ctx,
		`SELECT claimed_by, attempt, status FROM processed_messages WHERE id = ?`, staleID).
		Scan(&owner, &attempt, &status); err != nil {
		t.Fatalf("read reclaimed row: %v", err)
	}
	if owner != newToken {
		t.Error("claimed_by differs from the new owner")
	}
	if attempt != 2 {
		t.Errorf("attempt = %d, want 2", attempt)
	}
	if status != "processing" {
		t.Errorf("status = %q, want processing", status)
	}

	// A fresh claim is not stale: the takeover loses and the row is untouched.
	lost, err := repo.Reclaim(ctx, freshID, otherToken(freshToken), staleBefore)
	if err != nil {
		t.Fatalf("Reclaim(fresh) error = %v", err)
	}
	if lost {
		t.Error("Reclaim(fresh) won = true, want false (not stale)")
	}
	var freshOwner string
	var freshAttempt int
	if err := repo.db.QueryRowContext(ctx,
		`SELECT claimed_by, attempt FROM processed_messages WHERE id = ?`, freshID).Scan(&freshOwner, &freshAttempt); err != nil {
		t.Fatalf("read fresh row: %v", err)
	}
	if freshOwner != freshToken {
		t.Error("fresh claimed_by changed")
	}
	if freshAttempt != 1 {
		t.Errorf("fresh attempt = %d, want 1 (untouched)", freshAttempt)
	}

	// A terminal row is never reclaimed, even when its claim is old.
	sentID, sentToken := claimOne(t, repo, "SYNTHETIC-RECLAIM-SENT", "message")
	if err := repo.Complete(ctx, sentID, sentToken, "SYNTHETIC-DEST"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET claimed_at = ? WHERE id = ?`, now.Add(-20*time.Minute), sentID); err != nil {
		t.Fatalf("age sent row: %v", err)
	}
	wonSent, err := repo.Reclaim(ctx, sentID, otherToken(sentToken), staleBefore)
	if err != nil {
		t.Fatalf("Reclaim(sent) error = %v", err)
	}
	if wonSent {
		t.Error("Reclaim(sent) won = true, want false (terminal)")
	}
}

// TestRecoverStale finds an aged 'processing' row and ignores a fresh one and a
// terminal one.
func TestRecoverStale(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	freshID, _ := claimOne(t, repo, "SYNTHETIC-STALE-FRESH", "message")
	agedID, _ := claimOne(t, repo, "SYNTHETIC-STALE-AGED", "message")
	sentID, sentToken := claimOne(t, repo, "SYNTHETIC-STALE-SENT", "message")
	if err := repo.Complete(ctx, sentID, sentToken, "SYNTHETIC-DEST-3"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	// Age both the processing row and the terminal row past the threshold.
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET claimed_at = NOW(6) - INTERVAL 20 MINUTE WHERE id IN (?, ?)`,
		agedID, sentID); err != nil {
		t.Fatalf("age rows: %v", err)
	}

	ids, err := repo.RecoverStale(ctx, 15*time.Minute)
	if err != nil {
		t.Fatalf("RecoverStale() error = %v", err)
	}
	if !contains(ids, agedID) {
		t.Errorf("RecoverStale() = %v, want it to contain aged id %d", ids, agedID)
	}
	if contains(ids, freshID) {
		t.Errorf("RecoverStale() = %v, must not contain fresh id %d", ids, freshID)
	}
	if contains(ids, sentID) {
		t.Errorf("RecoverStale() = %v, must not contain terminal id %d", ids, sentID)
	}
}

// TestPayloadRoundTrip locks down StorePayload/LoadPayload: the sanitized
// envelope survives a round trip, the ownership guard applies, and oversized
// payloads become the valid-JSON truncation marker (ADR-0004 §10).
func TestPayloadRoundTrip(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	id, token := claimOne(t, repo, "SYNTHETIC-PAYLOAD", "message")

	want := []byte(`{"event":"message","version":1}`)
	if err := repo.StorePayload(ctx, id, token, want); err != nil {
		t.Fatalf("StorePayload() error = %v", err)
	}
	got, err := repo.LoadPayload(ctx, id)
	if err != nil {
		t.Fatalf("LoadPayload() error = %v", err)
	}
	assertJSONEqual(t, got, want)

	if err := repo.StorePayload(ctx, id, otherToken(token), want); !errors.Is(err, ErrNotOwner) {
		t.Errorf("StorePayload(non-owner) error = %v, want ErrNotOwner", err)
	}
	if _, err := repo.LoadPayload(ctx, 987654321); !errors.Is(err, ErrNotFound) {
		t.Errorf("LoadPayload(missing) error = %v, want ErrNotFound", err)
	}

	emptyID, _ := claimOne(t, repo, "SYNTHETIC-PAYLOAD-EMPTY", "message")
	empty, err := repo.LoadPayload(ctx, emptyID)
	if err != nil {
		t.Fatalf("LoadPayload(empty) error = %v", err)
	}
	if empty != nil {
		t.Errorf("LoadPayload(empty) = %q, want nil", empty)
	}

	if err := repo.StorePayload(ctx, id, token, []byte("not json")); err == nil {
		t.Error("StorePayload(invalid json) error = nil, want error")
	}
	if err := repo.StorePayload(ctx, id, token, nil); err == nil {
		t.Error("StorePayload(empty) error = nil, want error")
	}

	huge := append([]byte(`{"blob":"`), bytes.Repeat([]byte("x"), maxPayloadBytes)...)
	huge = append(huge, '"', '}')
	if err := repo.StorePayload(ctx, id, token, huge); err != nil {
		t.Fatalf("StorePayload(oversized) error = %v", err)
	}
	got, err = repo.LoadPayload(ctx, id)
	if err != nil {
		t.Fatalf("LoadPayload(oversized) error = %v", err)
	}
	assertJSONEqual(t, got, payloadTruncatedMarker)
}

// TestCleanupExpired removes only rows past expires_at.
func TestCleanupExpired(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()

	expiredID, expiredToken := claimOne(t, repo, "SYNTHETIC-CLEANUP-EXPIRED", "message")
	liveID, _ := claimOne(t, repo, "SYNTHETIC-CLEANUP-LIVE", "message")
	if err := repo.MarkDuplicate(ctx, expiredID, expiredToken); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET expires_at = NOW(6) - INTERVAL 1 SECOND WHERE id = ?`, expiredID); err != nil {
		t.Fatalf("expire row: %v", err)
	}

	removed, err := repo.CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	if removed != 1 {
		t.Errorf("CleanupExpired() removed = %d, want 1", removed)
	}

	var expiredExists, liveExists bool
	if err := repo.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_messages WHERE id = ?), EXISTS(SELECT 1 FROM processed_messages WHERE id = ?)`,
		expiredID, liveID).Scan(&expiredExists, &liveExists); err != nil {
		t.Fatalf("check rows: %v", err)
	}
	if expiredExists {
		t.Errorf("expired row %d still present, want deleted", expiredID)
	}
	if !liveExists {
		t.Errorf("live row %d was deleted, want it kept", liveID)
	}
}

// TestNewNilPoolPanics locks down the constructor contract: the repository
// borrows the shared pool and never opens one, so a nil pool is a programming
// error surfaced immediately.
func TestNewNilPoolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewProcessedMessages(nil) did not panic, want a panic")
		}
	}()
	_ = NewProcessedMessages(nil)
}

// TestClaimRejectsInvalidInput locks down the input validation: bad keys and
// sub-minimum ttls are rejected before any database access. The pool is never
// contacted (it points at a dead port), so a regression that reaches the DB
// would fail here instead of returning a validation error.
func TestClaimRejectsInvalidInput(t *testing.T) {
	repo := NewProcessedMessages(unconnectedPool(t))
	base := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-VALID"}

	tests := []struct {
		name string
		key  Key
		ttl  time.Duration
	}{
		{"empty source system", Key{ConnectionID: 1, EventType: "message", SourceMessageID: "x"}, time.Hour},
		{"unknown source system", Key{ConnectionID: 1, SourceSystem: "telegram", EventType: "message", SourceMessageID: "x"}, time.Hour},
		{"non-positive connection id", Key{ConnectionID: 0, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "x"}, time.Hour},
		{"empty event type", Key{ConnectionID: 1, SourceSystem: SourceEvolution, SourceMessageID: "x"}, time.Hour},
		{"oversized event type", Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: strings.Repeat("e", maxEventTypeLen+1), SourceMessageID: "x"}, time.Hour},
		{"empty source message id", Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message"}, time.Hour},
		{"oversized source message id", Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: strings.Repeat("m", maxSourceMessageIDLen+1)}, time.Hour},
		{"zero ttl", base, 0},
		{"negative ttl", base, -time.Second},
		{"ttl below minimum", base, minTTL - time.Nanosecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claimed, id, token, err := repo.Claim(t.Context(), tt.key, tt.ttl)
			if err == nil || !strings.HasPrefix(err.Error(), "dedup:") {
				t.Fatal("Claim() error = nil, want a validation error")
			}
			if claimed || id != 0 || token != "" {
				t.Errorf("Claim() = (%v, %d), want zero values and no token on rejection", claimed, id)
			}
		})
	}
}

func TestReclaimConcurrentSingleWinner(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	id, oldToken := claimOne(t, repo, "SYNTHETIC-CAS", "message")
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET claimed_at = NOW(6) - INTERVAL 30 MINUTE WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	cutoff := dbCutoff(t, repo)
	var before, after string
	const snapshot = `SELECT CONCAT(claimed_by, '/', attempt, '/', claimed_at, '/', expires_at) FROM processed_messages WHERE id = ?`
	if err := repo.db.QueryRowContext(ctx, snapshot, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-CAS"}
	if won, duplicateID, token, err := repo.Claim(ctx, key, 7*24*time.Hour); err != nil || won || duplicateID != id || token != "" {
		t.Fatal("Claim must not reclaim stale processing work")
	}
	if err := repo.db.QueryRowContext(ctx, snapshot, id).Scan(&after); err != nil || before != after {
		t.Fatal("duplicate Claim changed stale processing state")
	}

	type result struct {
		won   bool
		token string
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token := fmt.Sprintf("%032x", i+1)
			<-start
			won, err := NewProcessedMessages(repo.db).Reclaim(ctx, id, token, cutoff)
			results <- result{won: won, token: token, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var winners int
	var winnerToken string
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			winners++
			winnerToken = result.token
		}
	}
	if winners != 1 {
		t.Fatalf("reclaim winners = %d, want 1", winners)
	}
	var attempt int
	var owner string
	var fresh bool
	if err := repo.db.QueryRowContext(ctx, `SELECT attempt, claimed_by,
  claimed_at > NOW(6) - INTERVAL 1 MINUTE FROM processed_messages WHERE id = ?`, id).
		Scan(&attempt, &owner, &fresh); err != nil {
		t.Fatal(err)
	}
	if attempt != 2 || owner != winnerToken || !fresh {
		t.Fatal("CAS did not rotate ownership exactly once")
	}
	for _, mutate := range mutations(ctx, repo, id, oldToken) {
		if err := mutate(); !errors.Is(err, ErrNotOwner) {
			t.Fatalf("old owner mutation = %v", err)
		}
	}
	// Recovery can quarantine, not replay. The repository performs no external effects.
	if err := repo.MarkUnconfirmed(ctx, id, winnerToken); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range mutations(ctx, repo, id, winnerToken) {
		if err := mutate(); !errors.Is(err, ErrNotOwner) {
			t.Fatalf("terminal owner mutation = %v", err)
		}
	}
}

func TestReclaimSafetyFloor(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	id, token := claimOne(t, repo, "SYNTHETIC-CUTOFF", "message")
	if _, err := repo.db.ExecContext(ctx, `UPDATE processed_messages
  SET claimed_at = NOW(6) - INTERVAL 30 MINUTE WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	cutoff := dbCutoff(t, repo)
	tests := []struct {
		name      string
		token     string
		cutoff    time.Time
		wantError bool
	}{
		{name: "same owner", token: token, cutoff: cutoff, wantError: true},
		{name: "zero cutoff", token: otherToken(token), wantError: true},
		{name: "future cutoff", token: otherToken(token), cutoff: time.Now().Add(time.Hour), wantError: true},
		// An application clock ahead by 14 minutes computes this unsafe cutoff.
		{name: "clock skew", token: otherToken(token), cutoff: cutoff.Add(14 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			won, err := repo.Reclaim(ctx, id, tt.token, tt.cutoff)
			if won || (tt.wantError && err == nil) {
				t.Fatal("unsafe reclaim was not rejected")
			}
			var owner string
			var attempt int
			if err := repo.db.QueryRowContext(ctx, `SELECT claimed_by, attempt FROM processed_messages WHERE id = ?`, id).
				Scan(&owner, &attempt); err != nil {
				t.Fatal(err)
			}
			if owner != token || attempt != 1 {
				t.Fatal("rejected reclaim changed ownership/attempt")
			}
		})
	}
	// Strict '<' boundary: exactly at the cutoff is not stale.
	if _, err := repo.db.ExecContext(ctx, `UPDATE processed_messages SET claimed_at = ? WHERE id = ?`, cutoff, id); err != nil {
		t.Fatal(err)
	}
	if won, err := repo.Reclaim(ctx, id, otherToken(token), cutoff); err != nil || won {
		t.Fatal("equal cutoff reclaimed")
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE processed_messages
  SET claimed_at = NOW(6) - INTERVAL 30 MINUTE, attempt = 255 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if won, err := repo.Reclaim(ctx, id, otherToken(token), cutoff); won || !errors.Is(err, ErrAttemptExhausted) {
		t.Fatalf("attempt 255 reclaim error = %v, want exhaustion", err)
	}
	var attempt int
	var owner string
	if err := repo.db.QueryRowContext(ctx, `SELECT attempt, claimed_by FROM processed_messages WHERE id = ?`, id).
		Scan(&attempt, &owner); err != nil {
		t.Fatal(err)
	}
	if attempt != 255 || owner != token {
		t.Fatal("exhausted row mutated")
	}
	if won, err := repo.Reclaim(ctx, 987654321, otherToken(token), cutoff); err != nil || won {
		t.Fatal("missing row reclaimed")
	}
}

func TestPayloadRepeatedWrites(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	id, token := claimOne(t, repo, "SYNTHETIC-REPEATED-AUDIT", "message")
	payload := []byte(`{"event":"message","version":1}`)
	equivalent := []byte(`{ "version": 1, "event": "message" }`)
	for _, value := range [][]byte{payload, payload, equivalent} {
		if err := repo.StorePayload(ctx, id, token, value); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.LoadPayload(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, payload)
	got[0] = 'x'
	got, err = repo.LoadPayload(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, payload)
	if err := repo.StorePayload(ctx, id, otherToken(token), equivalent); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("no-op by non-owner = %v", err)
	}
	if err := repo.StorePayload(ctx, 987654321, token, payload); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// Exactly 64 KiB is retained; the next byte yields the lossy audit marker.
	exact := []byte(`{"auditPadding":"` + strings.Repeat("x", maxPayloadBytes-len(`{"auditPadding":""}`)) + `"}`)
	if len(exact) != maxPayloadBytes {
		t.Fatal("bad boundary fixture")
	}
	if err := repo.StorePayload(ctx, id, token, exact); err != nil {
		t.Fatal(err)
	}
	got, err = repo.LoadPayload(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, exact)
	oversized := append([]byte(" "), exact...)
	for range 2 {
		if err := repo.StorePayload(ctx, id, token, oversized); err != nil {
			t.Fatal(err)
		}
	}
	got, err = repo.LoadPayload(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, payloadTruncatedMarker)
	if err := repo.MarkDuplicate(ctx, id, token); err != nil {
		t.Fatal(err)
	}
	if err := repo.StorePayload(ctx, id, token, oversized); !errors.Is(err, ErrNotOwner) {
		t.Fatal("terminal no-op write succeeded")
	}
}

func TestCleanupPreservesUnresolvedAndBatches(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	processingID, processingToken := claimOne(t, repo, "SYNTHETIC-EXPIRED-PROCESSING", "message")
	unknownID, unknownToken := claimOne(t, repo, "SYNTHETIC-EXPIRED-UNKNOWN", "message")
	if err := repo.MarkUnconfirmed(ctx, unknownID, unknownToken); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE processed_messages SET expires_at = NOW(6) - INTERVAL 1 DAY`); err != nil {
		t.Fatal(err)
	}
	// A backlog exceeds the per-statement bound; only resolved terminal rows drain.
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 1005 {
		status := []string{"sent", "failed", "duplicate"}[i%3]
		if _, err := tx.ExecContext(ctx, `INSERT INTO processed_messages
   (connection_id, source_system, event_type, source_message_id, status, error_code, expires_at)
   VALUES (1, 'evolution', 'message', ?, ?, 'resolved', NOW(6) - INTERVAL 1 DAY)`,
			fmt.Sprintf("SYNTHETIC-CLEANUP-BATCH-%d", i), status); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if n, err := repo.CleanupExpired(cancelled); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("cleanup ignored cancellation")
	}
	if n, err := repo.CleanupExpired(ctx); err != nil || n != 1005 {
		t.Fatalf("cleanup = %d, %v", n, err)
	}
	var count int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_messages WHERE id IN (?, ?)`, processingID, unknownID).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("cleanup erased active/unknown-outcome tombstone")
	}
	if err := repo.Complete(ctx, processingID, processingToken, "SYNTHETIC-DEST"); err != nil {
		t.Fatal("active owner lost its expired claim")
	}
	if n, err := repo.CleanupExpired(ctx); err != nil || n != 1 {
		t.Fatalf("resolved cleanup = %d, %v", n, err)
	}
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-EXPIRED-UNKNOWN"}
	if won, id, token, err := repo.Claim(ctx, key, time.Hour); err != nil || won || id != unknownID || token != "" {
		t.Fatal("unknown outcome was resurrected after cleanup")
	}
}

func TestRecoverStaleBounded(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 1001 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO processed_messages
   (connection_id, source_system, event_type, source_message_id, status, claimed_by, claimed_at, expires_at)
   VALUES (1, 'evolution', 'message', ?, 'processing', ?, NOW(6) - INTERVAL 30 MINUTE, NOW(6))`,
			fmt.Sprintf("SYNTHETIC-STALE-BATCH-%d", i), strings.Repeat("a", 32)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ids, err := repo.RecoverStale(ctx, minStaleAge)
	if err != nil || len(ids) != 1000 {
		t.Fatalf("bounded stale list = %d, %v", len(ids), err)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatal("stale list not ordered")
		}
	}
	var attempt int
	if err := repo.db.QueryRowContext(ctx, `SELECT MAX(attempt) FROM processed_messages`).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	if attempt != 1 {
		t.Fatal("listing rotated ownership")
	}
}

func TestMutationRejectsInvalidInput(t *testing.T) {
	repo := NewProcessedMessages(unconnectedPool(t))
	for tokenCase, token := range []string{"", "a", strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("a", 31) + " "} {
		for i, mutation := range mutations(t.Context(), repo, 1, token) {
			t.Run(fmt.Sprintf("token case %d mutation %d", tokenCase, i), func(t *testing.T) {
				if err := mutation(); err == nil || !strings.Contains(err.Error(), "lowercase hex") {
					t.Fatalf("token validation = %v", err)
				}
			})
		}
		if _, err := repo.Reclaim(t.Context(), 1, token, time.Now().Add(-time.Hour)); err == nil || !strings.Contains(err.Error(), "lowercase hex") {
			t.Fatal("reclaim accepted alias token")
		}
	}
	for _, mutation := range mutations(t.Context(), repo, 0, strings.Repeat("a", 32)) {
		if err := mutation(); err == nil || !strings.Contains(err.Error(), "id must be positive") {
			t.Fatalf("id validation = %v", err)
		}
	}
	for _, age := range []time.Duration{0, -time.Second, time.Nanosecond, minStaleAge - time.Nanosecond} {
		if _, err := repo.RecoverStale(t.Context(), age); err == nil || !strings.Contains(err.Error(), "at least") {
			t.Fatal("unsafe stale age accepted")
		}
	}
	for _, value := range []string{"", "id ", string([]byte{0xff}), strings.Repeat("界", 256)} {
		if err := repo.Complete(t.Context(), 1, strings.Repeat("a", 32), value); err == nil || !strings.HasPrefix(err.Error(), "dedup: destination message id") {
			t.Fatalf("destination validation = %v", err)
		}
	}
	base := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "synthetic"}
	for _, value := range []string{"id ", string([]byte{0xff}), strings.Repeat("界", 256)} {
		key := base
		key.SourceMessageID = value
		if won, id, token, err := repo.Claim(t.Context(), key, time.Hour); err == nil || won || id != 0 || token != "" || !strings.HasPrefix(err.Error(), "dedup: source message id") {
			t.Fatal("invalid identity was not rejected before SQL")
		}
	}
	key := base
	key.EventType = "message "
	if err := validateKey(key); err == nil {
		t.Fatal("event type trailing space accepted")
	}
	key = base
	key.ConnectionID = int(uint64(1) << 32)
	if err := validateKey(key); err == nil {
		t.Fatal("connection id overflow accepted")
	}
}

func TestClaimIdentityAndRetention(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	ids := make(map[int64]bool)
	for _, identity := range []string{"e", "E", "é", "e\u0301", "界", strings.Repeat("界", 255)} {
		key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: strings.Repeat("界", 64), SourceMessageID: identity}
		won, id, token, err := repo.Claim(ctx, key, time.Hour)
		if err != nil || !won || ids[id] {
			t.Fatal("distinct unicode/case/accent identities collapsed")
		}
		if err := validateOwner(id, token); err != nil {
			t.Fatal("winner token is not canonical")
		}
		ids[id] = true
		var micros int64
		if err := repo.db.QueryRowContext(ctx, `SELECT TIMESTAMPDIFF(MICROSECOND, claimed_at, expires_at)
   FROM processed_messages WHERE id = ?`, id).Scan(&micros); err != nil {
			t.Fatal(err)
		}
		if micros != time.Hour.Microseconds() {
			t.Fatal("retention not based on DB claim time")
		}
	}
	// Default deployment retention is accepted without silently replacing the floor.
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-7DAYS"}
	if won, _, _, err := repo.Claim(ctx, key, 7*24*time.Hour); err != nil || !won {
		t.Fatal(err)
	}
}

func TestClaimFoundRowsContract(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	// Deliberately incompatible diagnostic pool: never pass it to New/Claim.
	cfg, err := mysqldriver.ParseDSN(os.Getenv("TEST_MYSQL_DSN"))
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	cfg.ClientFoundRows = true
	incompatible, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer incompatible.Close()
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-FOUND-ROWS"}
	id, token := claimOne(t, repo, key.SourceMessageID, key.EventType)
	for _, tt := range []struct {
		name string
		db   *sql.DB
		want int64
	}{
		{name: "production changed rows", db: repo.db, want: 0},
		{name: "incompatible found rows", db: incompatible, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tt.db.ExecContext(ctx, claimSQL, key.ConnectionID, key.SourceSystem,
				key.EventType, key.SourceMessageID, otherToken(token), time.Hour.Microseconds())
			if err != nil {
				t.Fatal(err)
			}
			n, err := res.RowsAffected()
			if err != nil || n != tt.want {
				t.Fatalf("ODKU affected = %d, %v; want %d", n, err, tt.want)
			}
		})
	}
	var owner string
	var attempt int
	if err := repo.db.QueryRowContext(ctx, `SELECT claimed_by, attempt FROM processed_messages WHERE id = ?`, id).
		Scan(&owner, &attempt); err != nil {
		t.Fatal(err)
	}
	if owner != token || attempt != 1 {
		t.Fatal("diagnostic ODKU changed owner")
	}
	// Production configuration rejects opting in; no multi-winner API is exposed.
	if _, err := ConfigFromURL(mysqlURL(cfg) + "?clientFoundRows=true"); err == nil {
		t.Fatal("production accepted found-rows")
	}
}

func TestCleanupCancellationDuringQuery(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	id, token := claimOne(t, repo, "SYNTHETIC-LOCKED-CLEANUP", "message")
	if err := repo.MarkDuplicate(ctx, id, token); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE processed_messages SET expires_at = NOW(6) - INTERVAL 1 DAY WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	lock, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	var lockedID int64
	if err := lock.QueryRowContext(ctx, `SELECT id FROM processed_messages WHERE id = ? FOR UPDATE`, id).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	// Cleanup blocks on the held row. Cancellation must interrupt the SQL call,
	// not wait for the DB lock timeout; the fixture remains intact.
	cancelled, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if n, err := repo.CleanupExpired(cancelled); n != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked cleanup = %d, %v; want deadline cancellation", n, err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := repo.db.QueryRowContext(ctx, existsByIDSQL, id).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("cancelled cleanup removed locked row")
	}
}

func mutations(ctx context.Context, repo *ProcessedMessages, id int64, token string) []func() error {
	return []func() error{
		func() error { return repo.Complete(ctx, id, token, "SYNTHETIC-DEST") },
		func() error { return repo.Fail(ctx, id, token, "classified_failure") },
		func() error { return repo.MarkUnconfirmed(ctx, id, token) },
		func() error { return repo.MarkDuplicate(ctx, id, token) },
		func() error { return repo.StorePayload(ctx, id, token, []byte(`{"event":"message"}`)) },
	}
}

func otherToken(token string) string {
	if token == strings.Repeat("a", 32) {
		return strings.Repeat("b", 32)
	}
	return strings.Repeat("a", 32)
}

func dbCutoff(t *testing.T, repo *ProcessedMessages) time.Time {
	t.Helper()
	var cutoff time.Time
	if err := repo.db.QueryRowContext(t.Context(), `SELECT NOW(6) - INTERVAL 15 MINUTE`).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	return cutoff.UTC()
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Error("stored audit JSON differs from supplied JSON value")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func claimOne(t *testing.T, repo *ProcessedMessages, sourceMessageID, eventType string) (id int64, token string) {
	t.Helper()
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: eventType, SourceMessageID: sourceMessageID}
	claimed, id, token, err := repo.Claim(t.Context(), key, time.Hour)
	if err != nil || !claimed {
		t.Fatalf("Claim(%s) = (%v, %d, %v), want a winner", sourceMessageID, claimed, id, err)
	}
	return id, token
}

func contains(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// unconnectedPool returns a *sql.DB whose driver is registered but which never
// connects (the address points at a dead port). Validation tests rely on Claim
// rejecting bad input before it ever reaches the pool.
func unconnectedPool(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", "bridge@tcp(127.0.0.1:1)/dedup_validation")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestSlice4_ReleaseGuardedDelete locks down ADR-0004 §1: Release deletes only
// the caller's own 'processing' row, reports ErrNotFound for an absent row and
// ErrNotOwner for a wrong token — never a silent no-op.
func TestSlice4_ReleaseGuardedDelete(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-RELEASE-1"}

	claimed, id, token, err := repo.Claim(ctx, key, time.Hour)
	if err != nil || !claimed {
		t.Fatalf("Claim() = %v, %v; want a won claim", claimed, err)
	}
	if err := repo.Release(ctx, id, token); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	// The row is gone: releasing again is ErrNotFound, not a silent success.
	if err := repo.Release(ctx, id, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Release() after delete = %v, want ErrNotFound", err)
	}

	// A wrong owner token is ErrNotOwner and preserves the row.
	claimed, id, token, err = repo.Claim(ctx, key, time.Hour)
	if err != nil || !claimed {
		t.Fatalf("re-Claim() = %v, %v; want a won claim", claimed, err)
	}
	if err := repo.Release(ctx, id, "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Release() with a wrong token = %v, want ErrNotOwner", err)
	}
	var status string
	if err := repo.db.QueryRowContext(ctx, "SELECT status FROM processed_messages WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "processing" {
		t.Errorf("status after a failed release = %q, want processing", status)
	}
	if err := repo.Release(ctx, id, token); err != nil {
		t.Fatalf("Release() with the owner token = %v", err)
	}
}

// TestSlice4_QuarantineClassification locks down the crash-recovery terminal
// state: Reclaim → MarkUnconfirmed yields status='failed' with
// error_code='unconfirmed_send' (never a resend).
func TestSlice4_QuarantineClassification(t *testing.T) {
	repo := testRepo(t)
	ctx := t.Context()
	key := Key{ConnectionID: 1, SourceSystem: SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-QUARANTINE-1"}

	_, id, _, err := repo.Claim(ctx, key, time.Hour)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	// Simulate a crash after the external effect: the row stays processing and
	// is now stale.
	if _, err := repo.db.ExecContext(ctx,
		"UPDATE processed_messages SET claimed_at = NOW(6) - INTERVAL 20 MINUTE WHERE id = ?", id); err != nil {
		t.Fatalf("age the claim: %v", err)
	}

	newToken, err := newClaimToken()
	if err != nil {
		t.Fatalf("newClaimToken() error = %v", err)
	}
	won, err := repo.Reclaim(ctx, id, newToken, time.Now().Add(-15*time.Minute))
	if err != nil || !won {
		t.Fatalf("Reclaim() = %v, %v; want a won reclaim", won, err)
	}
	if err := repo.MarkUnconfirmed(ctx, id, newToken); err != nil {
		t.Fatalf("MarkUnconfirmed() error = %v", err)
	}

	var status, errorCode string
	if err := repo.db.QueryRowContext(ctx,
		"SELECT status, COALESCE(error_code, '') FROM processed_messages WHERE id = ?", id).Scan(&status, &errorCode); err != nil {
		t.Fatalf("read terminal state: %v", err)
	}
	if status != "failed" || !dedup.IsUnconfirmedSend(errorCode) {
		t.Errorf("terminal state = %q/%q, want failed/unconfirmed_send", status, errorCode)
	}
}

// testRepo opens the dedicated integration database from TEST_MYSQL_DSN, resets
// the disposable schema, applies the migrations through the real migrator, and
// returns a ProcessedMessages over the production pool contract. Destructive
// fixtures run only against a *_test database.
func testRepo(t *testing.T) *ProcessedMessages {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not set; skipping MySQL integration test")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("TEST_MYSQL_DSN is not a valid go-sql-driver DSN: %v", err)
	}
	if cfg.Net != "tcp" {
		t.Fatalf("TEST_MYSQL_DSN must use tcp, got net %q", cfg.Net)
	}
	if !strings.HasSuffix(cfg.DBName, "_test") {
		t.Fatalf("TEST_MYSQL_DSN must point at a dedicated *_test database, got %q", cfg.DBName)
	}
	if cfg.ClientFoundRows {
		// clientFoundRows=true reports 1 for the no-op duplicate update too,
		// making every concurrent claimer a "winner" (ADR-0004 §1).
		t.Fatal("TEST_MYSQL_DSN must not set clientFoundRows=true")
	}

	// Exercise the production pool contract: UTC, parseTime, changed-row semantics.
	appConfig, err := ConfigFromURL(mysqlURL(cfg))
	if err != nil || appConfig.ClientFoundRows {
		t.Fatal("production pool must disable found-rows")
	}
	db, err := Open(mysqlURL(cfg))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("test database unreachable: %v", err)
	}

	// Storage's shuffled tests intentionally leave partial/checksum-drift schemas,
	// so reset the disposable fixture before applying the migrations. Never
	// repair production.
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), mysqlURL(cfg)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return NewProcessedMessages(db)
}

// mysqlURL converts the driver DSN into the mysql:// URL Migrate expects.
func mysqlURL(cfg *mysqldriver.Config) string {
	u := &url.URL{Scheme: "mysql", Host: cfg.Addr, Path: "/" + cfg.DBName}
	if cfg.User != "" {
		if cfg.Passwd == "" {
			u.User = url.User(cfg.User)
		} else {
			u.User = url.UserPassword(cfg.User, cfg.Passwd)
		}
	}
	return u.String()
}
