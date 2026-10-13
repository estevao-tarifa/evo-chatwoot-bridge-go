package mysql

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// TestMessageLogsInsert locks down the bridge.MessageLogs adapter: one row per
// entry, the direction/status translation to the legacy enum, and NULL for the
// columns the port does not carry (contactPhone, rawPayload, Chatwoot contact
// id).
func TestMessageLogsInsert(t *testing.T) {
	repo := NewMessageLogs(testRepo(t).db)
	ctx := t.Context()

	entries := []bridge.LogEntry{
		{ConnectionID: 1, Direction: string(bridge.DirectionIncoming), MessageType: "text", Status: "sent",
			SourceMessageID: "SRC-1", DestinationMessageID: "DEST-1", ContactID: 7, ContactName: "Ana", Preview: "hello"},
		{ConnectionID: 1, Direction: string(bridge.DirectionOutgoing), MessageType: "image", Status: "unconfirmed",
			SourceMessageID: "SRC-2", ContactName: "Ana", Preview: "[image]"},
		{ConnectionID: 1, Direction: string(bridge.DirectionOutgoing), MessageType: "text", Status: "duplicate",
			SourceMessageID: "SRC-3"},
		{ConnectionID: 1, Direction: string(bridge.DirectionIncoming), MessageType: "text", Status: "failed",
			SourceMessageID: "SRC-4"},
	}
	for _, entry := range entries {
		if err := repo.Insert(ctx, entry); err != nil {
			t.Fatalf("Insert(%s) error = %v", entry.SourceMessageID, err)
		}
	}
	assertRowCount(t, repo.db, "message_logs", len(entries))

	rows, err := repo.db.QueryContext(ctx, `SELECT direction, messageType, status,
		sourceMessageId, destinationMessageId, contactPhone, contactName, contentPreview, processedAt
		FROM message_logs ORDER BY id`)
	if err != nil {
		t.Fatalf("read message_logs: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type logRow struct {
		direction, messageType, status     string
		sourceID, destinationID            sql.NullString
		contactPhone, contactName, preview sql.NullString
		processedAt                        sql.NullTime
	}
	var got []logRow
	for rows.Next() {
		var r logRow
		if err := rows.Scan(&r.direction, &r.messageType, &r.status,
			&r.sourceID, &r.destinationID, &r.contactPhone, &r.contactName, &r.preview, &r.processedAt); err != nil {
			t.Fatalf("scan message_logs: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate message_logs: %v", err)
	}

	want := []logRow{
		{direction: "whatsapp_to_chatwoot", messageType: "text", status: "sent",
			sourceID: sql.NullString{String: "SRC-1", Valid: true}, destinationID: sql.NullString{String: "DEST-1", Valid: true},
			contactName: sql.NullString{String: "Ana", Valid: true}, preview: sql.NullString{String: "hello", Valid: true}},
		{direction: "whatsapp_to_chatwoot", messageType: "image", status: "failed",
			sourceID:    sql.NullString{String: "SRC-2", Valid: true},
			contactName: sql.NullString{String: "Ana", Valid: true}, preview: sql.NullString{String: "[image]", Valid: true}},
		{direction: "whatsapp_to_chatwoot", messageType: "text", status: "received",
			sourceID: sql.NullString{String: "SRC-3", Valid: true}},
		{direction: "whatsapp_to_chatwoot", messageType: "text", status: "failed",
			sourceID: sql.NullString{String: "SRC-4", Valid: true}},
	}
	if len(got) != len(want) {
		t.Fatalf("message_logs rows = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		g.processedAt = sql.NullTime{} // assert presence separately
		if g != w {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
		if !got[i].processedAt.Valid {
			t.Errorf("row %d processedAt is NULL, want a timestamp", i)
		}
	}
}

// TestMessageLogsChatwootDirection locks down the Chatwoot→WhatsApp transport
// direction: an entry with bridge.LogDirectionChatwootToWhatsapp is stored as
// the chatwoot_to_whatsapp enum value (spec §2.7).
func TestMessageLogsChatwootDirection(t *testing.T) {
	repo := NewMessageLogs(testRepo(t).db)
	ctx := t.Context()

	entry := bridge.LogEntry{
		ConnectionID: 1, Direction: bridge.LogDirectionChatwootToWhatsapp, MessageType: "outgoing",
		Status: "sent", SourceMessageID: "CW-1", DestinationMessageID: "EVO-1", Preview: "hi",
	}
	if err := repo.Insert(ctx, entry); err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	var direction, messageType, status string
	if err := repo.db.QueryRowContext(ctx, `SELECT direction, messageType, status FROM message_logs LIMIT 1`).
		Scan(&direction, &messageType, &status); err != nil {
		t.Fatalf("read message_logs: %v", err)
	}
	if direction != "chatwoot_to_whatsapp" || messageType != "outgoing" || status != "sent" {
		t.Errorf("row = %s/%s/%s, want chatwoot_to_whatsapp/outgoing/sent", direction, messageType, status)
	}
}

func TestMessageLogsValidation(t *testing.T) {
	repo := NewMessageLogs(unconnectedPool(t))
	valid := bridge.LogEntry{ConnectionID: 1, Direction: string(bridge.DirectionIncoming), MessageType: "text", Status: "sent"}

	tests := []struct {
		name  string
		entry bridge.LogEntry
	}{
		{name: "non-positive connection", entry: bridge.LogEntry{Direction: "incoming", MessageType: "text", Status: "sent"}},
		{name: "empty message type", entry: bridge.LogEntry{ConnectionID: 1, Direction: "incoming", Status: "sent"}},
		{name: "oversized message type", entry: bridge.LogEntry{ConnectionID: 1, Direction: "incoming", MessageType: strings.Repeat("t", maxLogMessageTypeLen+1), Status: "sent"}},
		{name: "unknown direction", entry: bridge.LogEntry{ConnectionID: 1, Direction: "sideways", MessageType: "text", Status: "sent"}},
		{name: "unknown status", entry: bridge.LogEntry{ConnectionID: 1, Direction: "incoming", MessageType: "text", Status: "pending"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := repo.Insert(t.Context(), tt.entry)
			if err == nil || !strings.HasPrefix(err.Error(), "mysql:") {
				t.Fatalf("Insert() error = %v, want a validation error before any database access", err)
			}
		})
	}
	if err := repo.Insert(t.Context(), valid); err == nil || strings.HasPrefix(err.Error(), "mysql:") {
		t.Fatalf("Insert(valid) error = %v, want a database error, not a validation error", err)
	}
}

func TestNewMessageLogsNilPoolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewMessageLogs(nil) did not panic")
		}
	}()
	_ = NewMessageLogs(nil)
}
