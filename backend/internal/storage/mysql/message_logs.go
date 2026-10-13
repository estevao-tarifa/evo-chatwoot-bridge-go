package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// Column widths mirroring the legacy message_logs schema (ADR-0002).
const (
	maxLogMessageTypeLen = 50  // messageType VARCHAR(50) NOT NULL
	maxLogMessageIDLen   = 255 // sourceMessageId / destinationMessageId VARCHAR(255)
	maxLogContactNameLen = 255 // contactName VARCHAR(255)
)

// MessageLogs is the MySQL adapter for bridge.MessageLogs over the legacy
// message_logs table. It writes one sanitized row per processed message: the
// preview is already truncated by the engine and no raw payload is ever stored.
type MessageLogs struct {
	db *sql.DB
}

// MessageLogs implements the bridge.MessageLogs port.
var _ bridge.MessageLogs = (*MessageLogs)(nil)

// NewMessageLogs returns a MessageLogs backed by pool, the shared *sql.DB. A nil
// pool is a programming error and panics.
func NewMessageLogs(pool *sql.DB) *MessageLogs {
	if pool == nil {
		panic("mysql: NewMessageLogs requires a non-nil *sql.DB pool")
	}
	return &MessageLogs{db: pool}
}

const insertMessageLogSQL = `INSERT INTO message_logs
	(connectionId, direction, messageType, status,
	 sourceMessageId, destinationMessageId, contactName, contentPreview, processedAt)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, NOW())`

// Insert writes one sanitized message_logs row. The bridge port carries no
// contact phone or raw payload, so those columns stay NULL; the port also has no
// Chatwoot contact-id column, so LogEntry.ContactID is not persisted.
func (r *MessageLogs) Insert(ctx context.Context, entry bridge.LogEntry) error {
	if err := validateLogEntry(entry); err != nil {
		return err
	}
	direction, err := logDirection(bridge.MessageDirection(entry.Direction))
	if err != nil {
		return err
	}
	status, err := logStatus(entry.Status)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, insertMessageLogSQL,
		entry.ConnectionID,
		direction,
		entry.MessageType,
		status,
		nullText(entry.SourceMessageID),
		nullText(entry.DestinationMessageID),
		nullText(entry.ContactName),
		nullText(entry.Preview),
	)
	if err != nil {
		return fmt.Errorf("insert message log: %w", err)
	}
	return nil
}

// logDirection maps the bridge port's direction to the legacy message_logs
// enum. The Evolution→Chatwoot engine records every event as
// whatsapp_to_chatwoot; the Chatwoot→WhatsApp engine records the inverse. The
// port carries the Chatwoot message_type, not a transport direction, so the
// transport direction is a distinct constant (bridge.LogDirectionChatwootToWhatsapp).
func logDirection(direction bridge.MessageDirection) (string, error) {
	switch string(direction) {
	case string(bridge.DirectionIncoming), string(bridge.DirectionOutgoing):
		return "whatsapp_to_chatwoot", nil
	case bridge.LogDirectionChatwootToWhatsapp:
		return "chatwoot_to_whatsapp", nil
	default:
		return "", fmt.Errorf("mysql: invalid log direction %q", direction)
	}
}

// logStatus maps the engine's terminal log status to the legacy message_logs
// enum. The engine writes one terminal row per event; the enum has no value for
// an unknown outcome or an echo, so 'unconfirmed' is recorded as 'failed'
// (mirroring the dedup unconfirmed_send convention) and 'duplicate' as
// 'received' (observed, not delivered).
func logStatus(status string) (string, error) {
	switch status {
	case "sent":
		return "sent", nil
	case "failed", "unconfirmed":
		return "failed", nil
	case "duplicate":
		return "received", nil
	default:
		return "", fmt.Errorf("mysql: unknown log status %q", status)
	}
}

func validateLogEntry(entry bridge.LogEntry) error {
	if entry.ConnectionID <= 0 {
		return fmt.Errorf("mysql: log connection id must be positive, got %d", entry.ConnectionID)
	}
	if entry.MessageType == "" || !utf8.ValidString(entry.MessageType) ||
		utf8.RuneCountInString(entry.MessageType) > maxLogMessageTypeLen {
		return fmt.Errorf("mysql: log message type must be 1..%d characters", maxLogMessageTypeLen)
	}
	if err := validateOptionalLogField(entry.SourceMessageID, maxLogMessageIDLen, "source message id"); err != nil {
		return err
	}
	if err := validateOptionalLogField(entry.DestinationMessageID, maxLogMessageIDLen, "destination message id"); err != nil {
		return err
	}
	return validateOptionalLogField(entry.ContactName, maxLogContactNameLen, "contact name")
}

func validateOptionalLogField(value string, width int, field string) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("mysql: log %s must be valid utf-8", field)
	}
	if utf8.RuneCountInString(value) > width {
		return fmt.Errorf("mysql: log %s longer than %d characters", field, width)
	}
	return nil
}

// nullText stores "" as NULL: the legacy log columns are nullable and an empty
// string carries no information.
func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
