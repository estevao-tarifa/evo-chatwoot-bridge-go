package mysql

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// f4ChatwootPort is a minimal bridge.Chatwoot double for the F4 end-to-end
// regression. The Evolution→Chatwoot forward must persist the created
// conversation id; the reverse direction never touches this port.
type f4ChatwootPort struct {
	mu       sync.Mutex
	sendDest []int
}

func (f *f4ChatwootPort) SearchContactExact(context.Context, string, bridge.Kind) ([]bridge.Contact, error) {
	return nil, nil // no pre-existing contact: the first message creates one
}

func (f *f4ChatwootPort) CreateContact(_ context.Context, params bridge.ContactParams) (*bridge.Contact, error) {
	return &bridge.Contact{ID: 501, Name: params.Name, Identifier: params.Identity}, nil
}

func (f *f4ChatwootPort) UpdateContactName(context.Context, int, string) error { return nil }

func (f *f4ChatwootPort) ListConversations(context.Context, int) ([]bridge.Conversation, error) {
	return nil, nil // no reusable candidate: the first message creates one
}

func (f *f4ChatwootPort) CreateConversation(_ context.Context, _, inboxID int) (*bridge.Conversation, error) {
	return &bridge.Conversation{ID: 7001, InboxID: inboxID, Status: "open"}, nil
}

func (f *f4ChatwootPort) SendText(
	_ context.Context,
	conversationID int,
	_ string,
	_ bridge.MessageDirection,
) (*bridge.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendDest = append(f.sendDest, conversationID)
	return &bridge.SendResult{MessageID: "cw-f4-1"}, nil
}

// f4EvolutionPort records the WhatsApp send the Chatwoot → WhatsApp direction
// must produce once the reverse mapping exists.
type f4EvolutionPort struct {
	mu      sync.Mutex
	numbers []string
}

func (f *f4EvolutionPort) SendText(_ context.Context, number, _ string) (*bridge.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.numbers = append(f.numbers, number)
	return &bridge.SendResult{MessageID: "evo-f4-1"}, nil
}

// TestEngine_F4_ConversationMappingPersistence is the end-to-end regression for
// F4: an identity first seen through WhatsApp must have its resolved Chatwoot
// conversation id persisted into contacts_map, so a later Chatwoot agent reply
// for that conversation routes to an Evolution send instead of failing closed
// with no_mapping.
func TestEngine_F4_ConversationMappingPersistence(t *testing.T) {
	db, dsn := adminPool(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	mappings := NewContactMappings(db)
	cw := &f4ChatwootPort{}
	evo := &f4EvolutionPort{}
	engine := bridge.New(bridge.Deps{
		Mappings:      mappings,
		Chatwoot:      cw,
		Echo:          NewEchoStore(db),
		Dedup:         NewDedupAdapter(db),
		Logs:          NewMessageLogs(db),
		Evolution:     evo,
		Conversations: mappings,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	conn := bridge.Connection{ID: 1, ChatwootInboxID: 10, IsActive: true}

	// WhatsApp-first: a message from an identity with no contacts_map row.
	evolutionPayload := f4JSON(t, map[string]any{
		"event": "Message",
		"data": map[string]any{
			"Info": map[string]any{
				"Chat":     "15550000001@s.whatsapp.net",
				"Sender":   "15550000001@s.whatsapp.net",
				"ID":       "F4-INT-1",
				"IsFromMe": false,
				"IsGroup":  false,
				"PushName": "Ana",
			},
			"Message": map[string]any{"conversation": "oi"},
		},
	})
	out, err := engine.HandleEvent(t.Context(), bridge.Event{Connection: conn, Payload: evolutionPayload})
	if err != nil || out != bridge.OutcomeForwarded {
		t.Fatalf("HandleEvent(evolution) = %v, %v; want forwarded", out, err)
	}

	// contacts_map.chatwootConversationId must be set (non-NULL) after the first
	// forwarded message of a new identity.
	stored, err := mappings.Find(t.Context(), conn.ID, "15550000001")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if stored == nil || stored.ChatwootContactID != 501 || stored.ChatwootConversationID != 7001 {
		t.Fatalf("contacts_map = %+v, want contact 501 / conversation 7001", stored)
	}
	if len(cw.sendDest) != 1 || cw.sendDest[0] != 7001 {
		t.Fatalf("Chatwoot send destinations = %v, want [7001]", cw.sendDest)
	}

	// A Chatwoot agent reply for that conversation must now route to WhatsApp.
	chatwootPayload := f4JSON(t, map[string]any{
		"event":        "message_created",
		"id":           99001,
		"message_type": "outgoing",
		"content":      "resposta do agente",
		"private":      false,
		"sender":       map[string]any{"type": "user"},
		"conversation": map[string]any{"id": 7001, "inbox_id": 10},
	})
	claimed, claimID, token, err := NewDedupAdapter(db).Claim(t.Context(), bridge.Key{
		ConnectionID:    conn.ID,
		SourceSystem:    bridge.SourceChatwoot,
		EventType:       "message_created",
		SourceMessageID: "99001",
	}, 7*24*time.Hour)
	if err != nil || !claimed {
		t.Fatalf("Claim(chatwoot) = %v, %v; want a won claim", claimed, err)
	}
	out, err = engine.HandleChatwootClaimed(
		t.Context(), bridge.Event{Connection: conn, Payload: chatwootPayload}, claimID, token,
	)
	if err != nil || out != bridge.OutcomeForwarded {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want forwarded (no_mapping regression)", out, err)
	}
	if len(evo.numbers) != 1 || evo.numbers[0] != "15550000001" {
		t.Fatalf("Evolution sends = %v, want [15550000001]", evo.numbers)
	}
}

// f4JSON marshals a synthetic payload. All fixtures are synthetic (AGENTS §9).
func f4JSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}
