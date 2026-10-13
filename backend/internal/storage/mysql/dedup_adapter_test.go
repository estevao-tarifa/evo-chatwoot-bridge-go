package mysql

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/dedup"
)

// TestDedupAdapterTransitions locks down the bridge.Dedup adapter: Claim
// arbitrates a single winner, and the ownership guard is surfaced as the
// bridge-owned ErrNotOwner so the engine fails closed.
func TestDedupAdapterTransitions(t *testing.T) {
	repo := testRepo(t)
	adapter := NewDedupAdapter(repo.db)
	ctx := t.Context()

	key := bridge.Key{ConnectionID: 1, SourceSystem: bridge.SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-ADAPTER-1"}
	claimed, id, token, err := adapter.Claim(ctx, key, time.Hour)
	if err != nil || !claimed || id <= 0 || token == "" {
		t.Fatalf("Claim() = (%v, %d, %q, %v), want a winner with a token", claimed, id, token, err)
	}

	claimedAgain, againID, againToken, err := adapter.Claim(ctx, key, time.Hour)
	if err != nil || claimedAgain || againID != id || againToken != "" {
		t.Fatalf("second Claim() = (%v, %d, %q, %v), want a loser with no token", claimedAgain, againID, againToken, err)
	}

	if err := adapter.Complete(ctx, id, token, "DEST-1"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	// The row is terminal: the old owner's transition is rejected as
	// bridge.ErrNotOwner (the dedup sentinel stays in the chain).
	err = adapter.MarkFailed(ctx, id, token, "classified")
	if !errors.Is(err, bridge.ErrNotOwner) {
		t.Fatalf("MarkFailed(terminal) error = %v, want bridge.ErrNotOwner", err)
	}
	if !errors.Is(err, dedup.ErrNotOwner) {
		t.Errorf("MarkFailed(terminal) error = %v, want the dedup sentinel preserved", err)
	}
}

// TestDedupAdapterTerminalStates locks down MarkFailed/MarkUnconfirmed/
// MarkDuplicate mapping onto the dedup.Store transitions.
func TestDedupAdapterTerminalStates(t *testing.T) {
	repo := testRepo(t)
	adapter := NewDedupAdapter(repo.db)
	ctx := t.Context()

	tests := []struct {
		name       string
		apply      func(id int64, token string) error
		wantStatus string
		wantError  sql.NullString
	}{
		{
			name:       "mark unconfirmed",
			apply:      func(id int64, token string) error { return adapter.MarkUnconfirmed(ctx, id, token) },
			wantStatus: "failed",
			wantError:  sql.NullString{String: "unconfirmed_send", Valid: true},
		},
		{
			name:       "mark duplicate",
			apply:      func(id int64, token string) error { return adapter.MarkDuplicate(ctx, id, token) },
			wantStatus: "duplicate",
		},
		{
			name:       "mark failed",
			apply:      func(id int64, token string) error { return adapter.MarkFailed(ctx, id, token, "upstream_5xx") },
			wantStatus: "failed",
			wantError:  sql.NullString{String: "upstream_5xx", Valid: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := bridge.Key{ConnectionID: 1, SourceSystem: bridge.SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-ADAPTER-TERM-" + tt.name}
			claimed, id, token, err := adapter.Claim(ctx, key, time.Hour)
			if err != nil || !claimed {
				t.Fatalf("Claim() = (%v, %d, %v), want a winner", claimed, id, err)
			}
			if err := tt.apply(id, token); err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}
			var status string
			var errorCode sql.NullString
			if err := repo.db.QueryRowContext(ctx,
				`SELECT status, error_code FROM processed_messages WHERE id = ?`, id).Scan(&status, &errorCode); err != nil {
				t.Fatalf("read row %d: %v", id, err)
			}
			if status != tt.wantStatus || errorCode != tt.wantError {
				t.Errorf("row = (%q, %+v), want (%q, %+v)", status, errorCode, tt.wantStatus, tt.wantError)
			}
		})
	}
}

// TestDedupAdapterReclaim locks down that Reclaim returns the new owner token
// the repository rotated to, and that it is usable for the quarantine
// transition.
func TestDedupAdapterReclaim(t *testing.T) {
	repo := testRepo(t)
	adapter := NewDedupAdapter(repo.db)
	ctx := t.Context()

	key := bridge.Key{ConnectionID: 1, SourceSystem: bridge.SourceEvolution, EventType: "message", SourceMessageID: "SYNTHETIC-ADAPTER-RECLAIM"}
	claimed, id, _, err := adapter.Claim(ctx, key, time.Hour)
	if err != nil || !claimed {
		t.Fatalf("Claim() = (%v, %d, %v), want a winner", claimed, id, err)
	}
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE processed_messages SET claimed_at = NOW(6) - INTERVAL 30 MINUTE WHERE id = ?`, id); err != nil {
		t.Fatalf("age row: %v", err)
	}

	token, won, err := adapter.Reclaim(ctx, id, dbCutoff(t, repo))
	if err != nil || !won || token == "" {
		t.Fatalf("Reclaim() = (%q, %v, %v), want a new owner token", token, won, err)
	}
	if err := adapter.MarkUnconfirmed(ctx, id, token); err != nil {
		t.Fatalf("MarkUnconfirmed(reclaimed) error = %v", err)
	}
	if _, wonAgain, err := adapter.Reclaim(ctx, id, dbCutoff(t, repo)); err != nil || wonAgain {
		t.Fatalf("Reclaim(terminal) = (%v, %v), want no takeover", wonAgain, err)
	}
}

func TestNewDedupAdapterNilPoolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewDedupAdapter(nil) did not panic")
		}
	}()
	_ = NewDedupAdapter(nil)
}
