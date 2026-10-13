package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// EchoStore is the MySQL adapter for bridge.EchoStore: it recognises a
// bridge-originated message by its destination id so the engine never loops it
// back (ROUTING_MATRIX §3, rule 6).
type EchoStore struct {
	db *sql.DB
}

// EchoStore implements the bridge.EchoStore port.
var _ bridge.EchoStore = (*EchoStore)(nil)

// NewEchoStore returns an EchoStore backed by pool, the shared *sql.DB. A nil
// pool is a programming error and panics.
func NewEchoStore(pool *sql.DB) *EchoStore {
	if pool == nil {
		panic("mysql: NewEchoStore requires a non-nil *sql.DB pool")
	}
	return &EchoStore{db: pool}
}

// wasBridgeSentSQL looks up a completed bridge send whose destination was
// Evolution: source_system='chatwoot' (the Chatwoot→WhatsApp direction) with the
// Evolution send id recorded on Complete. This engine handles Evolution events,
// so a bridge echo carries an Evolution message id, which only a Chatwoot→
// WhatsApp send can have produced.
//
// destination_message_id is utf8mb4_0900_ai_ci, so a plain `=` would accept
// case/accent-variant false matches; CAST(... AS BINARY) forces a byte-exact
// comparison (B4). connection_id + source_system scoping rejects another
// connection's or the other direction's send.
const wasBridgeSentSQL = `SELECT EXISTS(
	SELECT 1 FROM processed_messages
	WHERE connection_id = ?
	  AND source_system = 'chatwoot'
	  AND status = 'sent'
	  AND destination_message_id IS NOT NULL
	  AND CAST(destination_message_id AS BINARY) = CAST(? AS BINARY)
)`

// WasBridgeSent reports whether a completed Chatwoot→WhatsApp send recorded
// destinationMessageID for connectionID. An empty id is never bridge-sent.
func (r *EchoStore) WasBridgeSent(ctx context.Context, connectionID int, destinationMessageID string) (bool, error) {
	if connectionID <= 0 {
		return false, fmt.Errorf("mysql: echo connection id must be positive, got %d", connectionID)
	}
	if destinationMessageID == "" {
		return false, nil
	}
	var found bool
	if err := r.db.QueryRowContext(ctx, wasBridgeSentSQL, connectionID, destinationMessageID).Scan(&found); err != nil {
		return false, fmt.Errorf("echo lookup: %w", err)
	}
	return found, nil
}

// wasForwardedToChatwootSQL looks up a completed Evolution→Chatwoot forward
// whose destination was the Chatwoot message id the CW webhook now reports.
// This engine handles Chatwoot events, so a bridge echo carries a Chatwoot
// message id, which only an Evolution→Chatwoot send can have produced. It is
// the inverse scope of wasBridgeSentSQL (source_system='evolution').
//
// destination_message_id is utf8mb4_0900_ai_ci, so a plain `=` would accept
// case/accent-variant false matches; CAST(... AS BINARY) forces a byte-exact
// comparison (B4). connection_id + source_system scoping rejects another
// connection's or the other direction's send. Uses idx_processed_dest
// (connection_id, destination_message_id).
const wasForwardedToChatwootSQL = `SELECT EXISTS(
	SELECT 1 FROM processed_messages
	WHERE connection_id = ?
	  AND source_system = 'evolution'
	  AND status = 'sent'
	  AND destination_message_id IS NOT NULL
	  AND CAST(destination_message_id AS BINARY) = CAST(? AS BINARY)
)`

// WasForwardedToChatwoot reports whether a completed Evolution→Chatwoot forward
// recorded chatwootMessageID for connectionID. An empty id is never a
// bridge-authored forward.
func (r *EchoStore) WasForwardedToChatwoot(ctx context.Context, connectionID int, chatwootMessageID string) (bool, error) {
	if connectionID <= 0 {
		return false, fmt.Errorf("mysql: echo connection id must be positive, got %d", connectionID)
	}
	if chatwootMessageID == "" {
		return false, nil
	}
	var found bool
	if err := r.db.QueryRowContext(ctx, wasForwardedToChatwootSQL, connectionID, chatwootMessageID).Scan(&found); err != nil {
		return false, fmt.Errorf("echo lookup: %w", err)
	}
	return found, nil
}
