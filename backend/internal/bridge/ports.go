// Package bridge is the pure routing engine for the Evolution → Chatwoot
// direction (ROUTING_MATRIX.md E1–E8, E14). It parses an Evolution webhook,
// resolves the stable identity, decides the action and drives the effect
// through consumer-side ports (ARCHITECTURE §2 rule 1). It imports no SQL, no
// net/http and no concrete Evolution/Chatwoot client: the adapters that satisfy
// these ports belong to the wiring slice.
package bridge

import (
	"context"
	"errors"
	"time"
)

// SourceEvolution is the processed_messages source_system for Evolution events
// (ADR-0004).
const SourceEvolution = "evolution"

// SourceChatwoot is the processed_messages source_system for Chatwoot events
// (ADR-0004): the Chatwoot → WhatsApp direction (E10–E13).
const SourceChatwoot = "chatwoot"

// defaultClaimTTL is the dedup retention window (ADR-0004 §6: seven days). It is
// a retention floor, not proof of the upstream retry horizon.
const defaultClaimTTL = 7 * 24 * time.Hour

// Kind is the identity class of a resolved contact.
type Kind int

const (
	// KindUnknown is the zero value: an unset or unrecognised kind.
	KindUnknown Kind = iota
	// KindDirect is a 1:1 contact, keyed by the sender phone digits.
	KindDirect
	// KindGroup is a group contact, keyed by the stable group JID.
	KindGroup
)

// Connection is the bridge-owned view of one configured connection.
type Connection struct {
	ID              int
	ChatwootInboxID int  // 0 means "not configured": contact/conversation creation fails (P1/Q5)
	IsActive        bool // inactive connections fail closed (no entity)
	IgnoreGroups    bool
}

// Mapping is a persisted (connection, identity) → Chatwoot entity link. It is
// the first source consulted by the resolver: a valid mapping wins over any
// search (G0, ROUTING_MATRIX.md §1).
type Mapping struct {
	ConnectionID           int
	IdentityKey            string
	ChatwootContactID      int
	ChatwootConversationID int
}

// Contact is a bridge-owned Chatwoot contact.
type Contact struct {
	ID          int
	Name        string
	PhoneNumber string
	Identifier  string
}

// ContactParams describes a contact to create. For a group, Name must be the
// authoritative, non-placeholder name (P4); for a direct contact Name is
// optional and the adapter falls back to the phone digits.
type ContactParams struct {
	Kind     Kind
	Identity string
	Name     string
}

// Conversation is a bridge-owned Chatwoot conversation.
type Conversation struct {
	ID             int
	InboxID        int
	Status         string
	LastActivityAt int64
}

// MessageDirection is the Chatwoot message_type the bridge requests.
type MessageDirection string

const (
	// DirectionIncoming is a customer (inbound) message.
	DirectionIncoming MessageDirection = "incoming"
	// DirectionOutgoing is an agent/external own (outbound) message.
	DirectionOutgoing MessageDirection = "outgoing"
)

// LogDirectionChatwootToWhatsapp is the transport direction recorded in
// message_logs by the Chatwoot → WhatsApp path. It is deliberately not a
// MessageDirection: that type carries the Chatwoot message_type, not the
// transport direction (spec §2.7).
const LogDirectionChatwootToWhatsapp = "chatwoot_to_whatsapp"

// SendResult is a confirmed send: the destination message id used for echo
// correlation (ROUTING_MATRIX.md §3.1).
type SendResult struct {
	MessageID string
}

// LogEntry is one sanitized message_logs row (message-engine.md "State &
// observability"). It never carries private content, media or secrets; Preview
// is truncated before it is stored.
type LogEntry struct {
	ConnectionID         int
	Direction            string
	MessageType          string
	Status               string
	SourceMessageID      string
	DestinationMessageID string
	ContactID            int
	ContactName          string
	Preview              string
}

// Key is the bridge-owned dedup identity of an event. It mirrors the frozen
// dedup key (ADR-0004) without importing the persistence package: the wiring
// adapter maps bridge.Key → dedup.Key. It is never derived from message content.
type Key struct {
	ConnectionID    int
	SourceSystem    string
	EventType       string
	SourceMessageID string
}

// MappingStore is the consumer-side port over contacts_map.
type MappingStore interface {
	// Find returns the mapping for (connectionID, identityKey), or nil when
	// none exists. A non-nil error is never "no mapping".
	Find(ctx context.Context, connectionID int, identityKey string) (*Mapping, error)
	// Upsert persists (or replaces) the mapping for its (connection, identity).
	Upsert(ctx context.Context, mapping Mapping) error
}

// ConversationMappingStore is the reverse lookup the Chatwoot → WhatsApp path
// needs (E11): resolve a Chatwoot conversation (display_id) to its WhatsApp
// identity. It is declared consumer-side so the merged MappingStore interface
// and its G0 fake stay untouched (spec §2.4). A nil store is allowed; the
// Evolution path never reads it.
type ConversationMappingStore interface {
	// FindByConversation returns the mapping for (connectionID,
	// chatwootConversationID), or nil when none exists. A missing row is
	// (nil, nil): absence is not an error. A query error is never "no mapping".
	FindByConversation(ctx context.Context, connectionID, chatwootConversationID int) (*Mapping, error)
}

// Chatwoot is the consumer-side port over the Chatwoot client. Every method
// returns a bridge-owned type, never a chatwoot.* type.
type Chatwoot interface {
	// SearchContactExact returns every exact identity match. Zero results mean
	// "absent"; an error means the result is inconclusive and must never be
	// treated as absent (G0).
	SearchContactExact(ctx context.Context, identity string, kind Kind) ([]Contact, error)
	// CreateContact creates a contact. Its error outcome is unconfirmed.
	CreateContact(ctx context.Context, params ContactParams) (*Contact, error)
	// UpdateContactName sets a contact display name.
	UpdateContactName(ctx context.Context, contactID int, name string) error
	// ListConversations returns a contact's conversations. An error is never
	// "no candidate" (Q4).
	ListConversations(ctx context.Context, contactID int) ([]Conversation, error)
	// CreateConversation creates an open conversation in inboxID.
	CreateConversation(ctx context.Context, contactID, inboxID int) (*Conversation, error)
	// SendText posts a text message. Its error outcome is unconfirmed.
	SendText(ctx context.Context, conversationID int, content string, direction MessageDirection) (*SendResult, error)
}

// Evolution is the consumer-side port over the Evolution client. E9 (call
// reply) is not currently called; the method is declared so wiring can satisfy
// the full port without re-opening the engine.
type Evolution interface {
	SendText(ctx context.Context, number, text string) (*SendResult, error)
}

// EchoStore recognises a bridge-originated message so it is never looped back
// (ROUTING_MATRIX.md §3, rule 6).
type EchoStore interface {
	// WasBridgeSent reports whether a completed Chatwoot→WhatsApp send recorded
	// destinationMessageID (the Evolution echo the Evolution engine handles).
	WasBridgeSent(ctx context.Context, connectionID int, destinationMessageID string) (bool, error)
	// WasForwardedToChatwoot reports whether a completed Evolution→Chatwoot
	// forward recorded chatwootMessageID as its destination (the Chatwoot echo
	// the Chatwoot engine ignores at F5). It is the inverse scope of
	// WasBridgeSent.
	WasForwardedToChatwoot(ctx context.Context, connectionID int, chatwootMessageID string) (bool, error)
}

// Dedup is the consumer-side port over the persistent idempotency repository
// (ADR-0004). Claim arbitrates ownership; the transitions are ownership-guarded
// and a lost claim returns ErrNotOwner.
type Dedup interface {
	Claim(ctx context.Context, key Key, ttl time.Duration) (claimed bool, id int64, token string, err error)
	Complete(ctx context.Context, id int64, token, destinationMessageID string) error
	MarkDuplicate(ctx context.Context, id int64, token string) error
	MarkUnconfirmed(ctx context.Context, id int64, token string) error
	MarkFailed(ctx context.Context, id int64, token, errorCode string) error
	// Reclaim atomically takes over a stale processing row for reconciliation
	// only. It rotates the owner and returns the new token when it won.
	Reclaim(ctx context.Context, id int64, staleBefore time.Time) (token string, won bool, err error)
}

// MessageLogs is the consumer-side port over message_logs.
type MessageLogs interface {
	Insert(ctx context.Context, entry LogEntry) error
}

// Sentinel errors. The wiring adapter translates the dedup/chatwoot equivalents
// into these bridge-owned values.
var (
	// ErrMalformedPayload means the webhook payload could not be parsed. The
	// engine returns it with OutcomeMalformed so ingress can answer 400 without
	// touching a port or creating an entity.
	ErrMalformedPayload = errors.New("bridge: malformed event payload")
	// ErrMalformedIdentity is the E14 input-validation failure. No port is
	// touched and no entity is created.
	ErrMalformedIdentity = errors.New("bridge: malformed identity")
	// ErrIdentityConflict means several distinct exact contacts claim the same
	// identity. The engine fails closed and never picks the first.
	ErrIdentityConflict = errors.New("bridge: conflicting contact identity")
	// ErrNotOwner means a guarded claim transition was rejected because another
	// owner (or a terminal state) holds the row. It is logged, never swallowed.
	ErrNotOwner = errors.New("bridge: claim not owned by caller")
	// ErrUnconfirmed marks an unknown mutation outcome: a create or send whose
	// effect may have begun. The engine quarantines the claim and never retries
	// or creates an alternate entity (ADR-0004/0005/0009).
	ErrUnconfirmed = errors.New("bridge: mutation outcome unconfirmed")
	// ErrConversationUnavailable means no usable conversation exists and the
	// routing policy forbids creating one.
	ErrConversationUnavailable = errors.New("bridge: no usable conversation")
)
