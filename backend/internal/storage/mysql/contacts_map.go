package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// maxMappingIdentityLen mirrors contacts_map.whatsappPhone VARCHAR(50). The
// column is the stable identity key (phone digits for a 1:1 contact, the group
// JID for a group); it is never a display name (ROUTING_MATRIX §2).
const maxMappingIdentityLen = 50

// ContactMappings is the MySQL adapter for bridge.MappingStore over the legacy
// contacts_map table (ADR-0002: schema preserved, no new columns). It borrows
// the *sql.DB pool owned by this package.
type ContactMappings struct {
	db *sql.DB
}

// ContactMappings implements the bridge.MappingStore port.
var _ bridge.MappingStore = (*ContactMappings)(nil)

// ContactMappings also implements the reverse-lookup port the Chatwoot →
// WhatsApp path consumes (E11).
var _ bridge.ConversationMappingStore = (*ContactMappings)(nil)

// NewContactMappings returns a ContactMappings backed by pool, the shared
// *sql.DB. A nil pool is a programming error and panics.
func NewContactMappings(pool *sql.DB) *ContactMappings {
	if pool == nil {
		panic("mysql: NewContactMappings requires a non-nil *sql.DB pool")
	}
	return &ContactMappings{db: pool}
}

// findContactMapSQL reads the mapping for one (connection, identity). The legacy
// table has no UNIQUE on (connectionId, whatsappPhone) and may hold duplicate
// rows (audit data-model §4/§11), so ORDER BY id makes the choice deterministic
// (Q3) instead of the legacy arbitrary first row.
const findContactMapSQL = `SELECT chatwootContactId, chatwootConversationId
	FROM contacts_map
	WHERE connectionId = ? AND whatsappPhone = ?
	ORDER BY id
	LIMIT 1`

// Find returns the mapping for (connectionID, identityKey), or nil when none
// exists. A missing row is (nil, nil): absence is not an error.
func (r *ContactMappings) Find(ctx context.Context, connectionID int, identityKey string) (*bridge.Mapping, error) {
	if err := validateMappingIdentity(connectionID, identityKey); err != nil {
		return nil, err
	}
	var contactID int
	var conversationID sql.NullInt64
	err := r.db.QueryRowContext(ctx, findContactMapSQL, connectionID, identityKey).
		Scan(&contactID, &conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find contact mapping: %w", err)
	}
	return &bridge.Mapping{
		ConnectionID:           connectionID,
		IdentityKey:            identityKey,
		ChatwootContactID:      contactID,
		ChatwootConversationID: int(conversationID.Int64),
	}, nil
}

// findContactMapByConversationSQL reads the mapping for one (connection,
// chatwoot conversation display_id). The legacy table has no index on
// chatwootConversationId and may hold duplicate rows, so ORDER BY id makes the
// choice deterministic (spec §2.4) instead of the legacy arbitrary first row.
//
// ponytail: no index on chatwootConversationId — this is a bounded table scan.
// Accept it at the 1–2 connection scale; add a (connectionId,
// chatwootConversationId) index in a Phase 6 forward-only migration if E11
// latency ever matters (no migration now, spec §7.7).
const findContactMapByConversationSQL = `SELECT whatsappPhone, chatwootContactId, chatwootConversationId
	FROM contacts_map
	WHERE connectionId = ? AND chatwootConversationId = ?
	ORDER BY id
	LIMIT 1`

// FindByConversation returns the mapping for (connectionID,
// chatwootConversationID), or nil when none exists. A missing row is
// (nil, nil): absence is not an error. A query error is never "no mapping".
// IdentityKey is the stored whatsappPhone (phone digits for a 1:1 contact, the
// group JID for a group) — the same stable key the resolver writes.
func (r *ContactMappings) FindByConversation(ctx context.Context, connectionID, chatwootConversationID int) (*bridge.Mapping, error) {
	if connectionID <= 0 {
		return nil, fmt.Errorf("mysql: connection id must be positive, got %d", connectionID)
	}
	if chatwootConversationID <= 0 {
		return nil, fmt.Errorf("mysql: chatwoot conversation id must be positive, got %d", chatwootConversationID)
	}
	var identityKey string
	var contactID int
	var conversationID sql.NullInt64
	err := r.db.QueryRowContext(ctx, findContactMapByConversationSQL, connectionID, chatwootConversationID).
		Scan(&identityKey, &contactID, &conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find contact mapping by conversation: %w", err)
	}
	return &bridge.Mapping{
		ConnectionID:           connectionID,
		IdentityKey:            identityKey,
		ChatwootContactID:      contactID,
		ChatwootConversationID: int(conversationID.Int64),
	}, nil
}

const selectContactMapIDSQL = `SELECT id FROM contacts_map
	WHERE connectionId = ? AND whatsappPhone = ?
	ORDER BY id
	LIMIT 1`

const insertContactMapSQL = `INSERT INTO contacts_map
	(connectionId, whatsappPhone, chatwootContactId, chatwootConversationId)
	VALUES (?, ?, ?, ?)`

const updateContactMapSQL = `UPDATE contacts_map
	SET chatwootContactId = ?, chatwootConversationId = ?
	WHERE id = ?`

// Upsert persists the mapping for its (connection, identity). With no UNIQUE
// index on (connectionId, whatsappPhone), this reproduces the legacy
// SELECT-then-UPDATE-or-INSERT; the engine's per-identity keyed lock serialises
// concurrent upserts within the process (resolve.go Q8). whatsappName and
// whatsappProfilePic are not on the port and are left untouched.
func (r *ContactMappings) Upsert(ctx context.Context, mapping bridge.Mapping) error {
	if err := validateMapping(mapping); err != nil {
		return err
	}
	conversationID := nullableConversationID(mapping.ChatwootConversationID)

	var id int64
	err := r.db.QueryRowContext(ctx, selectContactMapIDSQL, mapping.ConnectionID, mapping.IdentityKey).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = r.db.ExecContext(ctx, insertContactMapSQL,
			mapping.ConnectionID, mapping.IdentityKey, mapping.ChatwootContactID, conversationID)
		if err != nil {
			return fmt.Errorf("insert contact mapping: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("find contact mapping for upsert: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, updateContactMapSQL, mapping.ChatwootContactID, conversationID, id); err != nil {
		return fmt.Errorf("update contact mapping: %w", err)
	}
	return nil
}

// nullableConversationID stores 0 as NULL: the legacy column is nullable and 0
// means "no conversation yet".
func nullableConversationID(id int) any {
	if id <= 0 {
		return nil
	}
	return id
}

func validateMapping(mapping bridge.Mapping) error {
	if err := validateMappingIdentity(mapping.ConnectionID, mapping.IdentityKey); err != nil {
		return err
	}
	if mapping.ChatwootContactID <= 0 {
		return fmt.Errorf("mysql: chatwoot contact id must be positive, got %d", mapping.ChatwootContactID)
	}
	return nil
}

func validateMappingIdentity(connectionID int, identityKey string) error {
	if connectionID <= 0 {
		return fmt.Errorf("mysql: connection id must be positive, got %d", connectionID)
	}
	if identityKey == "" || !utf8.ValidString(identityKey) {
		return errors.New("mysql: mapping identity key must be nonempty valid utf-8")
	}
	if utf8.RuneCountInString(identityKey) > maxMappingIdentityLen {
		return fmt.Errorf("mysql: mapping identity key longer than %d characters", maxMappingIdentityLen)
	}
	return nil
}
