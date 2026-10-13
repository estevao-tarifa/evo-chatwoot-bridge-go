package mysql

import (
	"database/sql"
	"testing"
)

// TestEchoStore locks down B4: the bridge-sent lookup is byte-exact and scoped
// by connection and direction (source_system), so a case/accent variant, another
// connection's send, or the other direction's send never matches.
func TestEchoStore(t *testing.T) {
	pool := testRepo(t).db
	store := NewEchoStore(pool)
	ctx := t.Context()

	seedProcessedRow(t, pool, 1, "chatwoot", "sent", "SRC-A", "DEST-1")  // bridge → Evolution
	seedProcessedRow(t, pool, 1, "evolution", "sent", "SRC-B", "DEST-2") // bridge → Chatwoot
	seedProcessedRow(t, pool, 2, "chatwoot", "sent", "SRC-C", "DEST-3")  // other connection
	seedProcessedRow(t, pool, 1, "chatwoot", "failed", "SRC-D", "DEST-4")
	seedProcessedRow(t, pool, 1, "chatwoot", "sent", "SRC-E", "café-5")

	tests := []struct {
		name       string
		connection int
		id         string
		want       bool
	}{
		{name: "chatwoot send matches", connection: 1, id: "DEST-1", want: true},
		{name: "case variant does not match", connection: 1, id: "dest-1", want: false},
		{name: "accent variant does not match", connection: 1, id: "cafe-5", want: false},
		{name: "other connection does not match", connection: 2, id: "DEST-1", want: false},
		{name: "other direction does not match", connection: 1, id: "DEST-2", want: false},
		{name: "unsent row does not match", connection: 1, id: "DEST-4", want: false},
		{name: "unknown id does not match", connection: 1, id: "no-such-id", want: false},
		{name: "empty id does not match", connection: 1, id: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.WasBridgeSent(ctx, tt.connection, tt.id)
			if err != nil {
				t.Fatalf("WasBridgeSent() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("WasBridgeSent(%d, %q) = %v, want %v", tt.connection, tt.id, got, tt.want)
			}
		})
	}

	if _, err := store.WasBridgeSent(ctx, 0, "DEST-1"); err == nil {
		t.Fatal("WasBridgeSent(0, ...) error = nil, want a validation error")
	}
}

// TestEchoStoreForwardedToChatwoot locks down the F5 loop-break query: the
// Evolution→Chatwoot destination lookup is byte-exact and scoped by connection
// and direction (source_system='evolution'), the inverse of WasBridgeSent.
func TestEchoStoreForwardedToChatwoot(t *testing.T) {
	pool := testRepo(t).db
	store := NewEchoStore(pool)
	ctx := t.Context()

	seedProcessedRow(t, pool, 1, "evolution", "sent", "SRC-A", "CW-1")   // bridge → Chatwoot
	seedProcessedRow(t, pool, 1, "chatwoot", "sent", "SRC-B", "CW-2")    // other direction
	seedProcessedRow(t, pool, 2, "evolution", "sent", "SRC-C", "CW-3")   // other connection
	seedProcessedRow(t, pool, 1, "evolution", "failed", "SRC-D", "CW-4") // unsent
	seedProcessedRow(t, pool, 1, "evolution", "sent", "SRC-E", "café-5")

	tests := []struct {
		name       string
		connection int
		id         string
		want       bool
	}{
		{name: "evolution forward matches", connection: 1, id: "CW-1", want: true},
		{name: "case variant does not match", connection: 1, id: "cw-1", want: false},
		{name: "accent variant does not match", connection: 1, id: "cafe-5", want: false},
		{name: "other connection does not match", connection: 2, id: "CW-1", want: false},
		{name: "other direction does not match", connection: 1, id: "CW-2", want: false},
		{name: "unsent row does not match", connection: 1, id: "CW-4", want: false},
		{name: "unknown id does not match", connection: 1, id: "no-such-id", want: false},
		{name: "empty id does not match", connection: 1, id: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.WasForwardedToChatwoot(ctx, tt.connection, tt.id)
			if err != nil {
				t.Fatalf("WasForwardedToChatwoot() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("WasForwardedToChatwoot(%d, %q) = %v, want %v", tt.connection, tt.id, got, tt.want)
			}
		})
	}

	if _, err := store.WasForwardedToChatwoot(ctx, 0, "CW-1"); err == nil {
		t.Fatal("WasForwardedToChatwoot(0, ...) error = nil, want a validation error")
	}
}

func TestNewEchoStoreNilPoolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewEchoStore(nil) did not panic")
		}
	}()
	_ = NewEchoStore(nil)
}

// seedProcessedRow inserts one processed_messages row directly. destination is
// NULL when empty.
func seedProcessedRow(t *testing.T, db *sql.DB, connectionID int, sourceSystem, status, sourceMessageID, destination string) {
	t.Helper()
	var dest any
	if destination != "" {
		dest = destination
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO processed_messages
		(connection_id, source_system, event_type, source_message_id, status, destination_message_id, expires_at)
		VALUES (?, ?, 'message', ?, ?, ?, NOW(6) + INTERVAL 7 DAY)`,
		connectionID, sourceSystem, sourceMessageID, status, dest); err != nil {
		t.Fatalf("seed processed row: %v", err)
	}
}
