package mysql

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// TestContactMappingsRoundTrip locks down the bridge.MappingStore adapter over
// the legacy contacts_map table: absence is (nil, nil), upsert inserts then
// updates in place, and the identity is scoped by connection.
func TestContactMappingsRoundTrip(t *testing.T) {
	repo := NewContactMappings(testRepo(t).db)
	ctx := t.Context()

	got, err := repo.Find(ctx, 1, "15550000001")
	if err != nil || got != nil {
		t.Fatalf("Find(missing) = %+v, %v; want nil, nil", got, err)
	}

	if err := repo.Upsert(ctx, bridge.Mapping{
		ConnectionID: 1, IdentityKey: "15550000001", ChatwootContactID: 7,
	}); err != nil {
		t.Fatalf("Upsert(insert) error = %v", err)
	}
	got, err = repo.Find(ctx, 1, "15550000001")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	want := &bridge.Mapping{ConnectionID: 1, IdentityKey: "15550000001", ChatwootContactID: 7}
	if got == nil || *got != *want {
		t.Fatalf("Find() = %+v, want %+v", got, want)
	}
	assertRowCount(t, repo.db, "contacts_map", 1)

	// A second upsert for the same (connection, identity) updates in place.
	if err := repo.Upsert(ctx, bridge.Mapping{
		ConnectionID: 1, IdentityKey: "15550000001", ChatwootContactID: 8, ChatwootConversationID: 9,
	}); err != nil {
		t.Fatalf("Upsert(update) error = %v", err)
	}
	got, err = repo.Find(ctx, 1, "15550000001")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	want = &bridge.Mapping{ConnectionID: 1, IdentityKey: "15550000001", ChatwootContactID: 8, ChatwootConversationID: 9}
	if got == nil || *got != *want {
		t.Fatalf("Find() after update = %+v, want %+v", got, want)
	}
	assertRowCount(t, repo.db, "contacts_map", 1)

	// The mapping is scoped by connection.
	if other, err := repo.Find(ctx, 2, "15550000001"); err != nil || other != nil {
		t.Fatalf("Find(other connection) = %+v, %v; want nil, nil", other, err)
	}

	// A group identity is the stable JID, stored in whatsappPhone.
	if err := repo.Upsert(ctx, bridge.Mapping{
		ConnectionID: 1, IdentityKey: "120363400000000000@g.us", ChatwootContactID: 11,
	}); err != nil {
		t.Fatalf("Upsert(group) error = %v", err)
	}
	group, err := repo.Find(ctx, 1, "120363400000000000@g.us")
	if err != nil || group == nil || group.ChatwootContactID != 11 {
		t.Fatalf("Find(group) = %+v, %v; want contact 11", group, err)
	}
	assertRowCount(t, repo.db, "contacts_map", 2)
}

// TestContactMappingsFindByConversation locks down the reverse lookup the
// Chatwoot → WhatsApp path uses (E11): absence is (nil, nil), the identity is
// the stored whatsappPhone (phone or group JID), and the lookup is scoped by
// connection and conversation.
func TestContactMappingsFindByConversation(t *testing.T) {
	repo := NewContactMappings(testRepo(t).db)
	ctx := t.Context()

	if got, err := repo.FindByConversation(ctx, 1, 5001); err != nil || got != nil {
		t.Fatalf("FindByConversation(missing) = %+v, %v; want nil, nil", got, err)
	}

	if err := repo.Upsert(ctx, bridge.Mapping{
		ConnectionID: 1, IdentityKey: "15550000001", ChatwootContactID: 7, ChatwootConversationID: 5001,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	got, err := repo.FindByConversation(ctx, 1, 5001)
	if err != nil {
		t.Fatalf("FindByConversation() error = %v", err)
	}
	want := &bridge.Mapping{ConnectionID: 1, IdentityKey: "15550000001", ChatwootContactID: 7, ChatwootConversationID: 5001}
	if got == nil || *got != *want {
		t.Fatalf("FindByConversation() = %+v, want %+v", got, want)
	}

	// Scoped by connection and by conversation.
	if other, err := repo.FindByConversation(ctx, 2, 5001); err != nil || other != nil {
		t.Fatalf("FindByConversation(other connection) = %+v, %v; want nil, nil", other, err)
	}
	if other, err := repo.FindByConversation(ctx, 1, 5002); err != nil || other != nil {
		t.Fatalf("FindByConversation(other conversation) = %+v, %v; want nil, nil", other, err)
	}

	// A group identity is the stable JID.
	if err := repo.Upsert(ctx, bridge.Mapping{
		ConnectionID: 1, IdentityKey: "120363400000000000@g.us", ChatwootContactID: 11, ChatwootConversationID: 6001,
	}); err != nil {
		t.Fatalf("Upsert(group) error = %v", err)
	}
	group, err := repo.FindByConversation(ctx, 1, 6001)
	if err != nil || group == nil || group.IdentityKey != "120363400000000000@g.us" {
		t.Fatalf("FindByConversation(group) = %+v, %v; want the group JID", group, err)
	}

	// Validation runs before any database access.
	if _, err := repo.FindByConversation(ctx, 0, 5001); err == nil {
		t.Error("FindByConversation(0, ...) error = nil, want a validation error")
	}
	if _, err := repo.FindByConversation(ctx, 1, 0); err == nil {
		t.Error("FindByConversation(..., 0) error = nil, want a validation error")
	}
}

func TestContactMappingsValidation(t *testing.T) {
	repo := NewContactMappings(unconnectedPool(t))
	valid := bridge.Mapping{ConnectionID: 1, IdentityKey: "15550000001", ChatwootContactID: 7}

	tests := []struct {
		name    string
		mapping bridge.Mapping
	}{
		{name: "non-positive connection", mapping: bridge.Mapping{IdentityKey: "x", ChatwootContactID: 7}},
		{name: "empty identity", mapping: bridge.Mapping{ConnectionID: 1, ChatwootContactID: 7}},
		{name: "oversized identity", mapping: bridge.Mapping{ConnectionID: 1, IdentityKey: strings.Repeat("9", maxMappingIdentityLen+1), ChatwootContactID: 7}},
		{name: "non-positive contact id", mapping: bridge.Mapping{ConnectionID: 1, IdentityKey: "15550000001"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := repo.Upsert(t.Context(), tt.mapping)
			if err == nil || !strings.HasPrefix(err.Error(), "mysql:") {
				t.Fatalf("Upsert() error = %v, want a validation error before any database access", err)
			}
		})
	}
	// A valid mapping passes validation and only then reaches the dead pool.
	if err := repo.Upsert(t.Context(), valid); err == nil || strings.HasPrefix(err.Error(), "mysql:") {
		t.Fatalf("Upsert(valid) error = %v, want a database error, not a validation error", err)
	}
	if _, err := repo.Find(t.Context(), 0, "15550000001"); err == nil {
		t.Fatal("Find() error = nil, want a validation error")
	}
}

// assertRowCount fails unless table holds exactly want rows.
func assertRowCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM `"+table+"`").Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Errorf("%s rows = %d, want %d", table, got, want)
	}
}

func TestNewContactMappingsNilPoolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewContactMappings(nil) did not panic")
		}
	}()
	_ = NewContactMappings(nil)
}
