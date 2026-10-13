package chatwoot

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// BridgeAdapter exposes a *Client as the bridge.Chatwoot consumer-side port
// (ARCHITECTURE §2 rule 3: the adapter depends on the domain, never the
// reverse). It translates chatwoot-owned values and sentinels into bridge-owned
// ones so internal/bridge never imports this package.
type BridgeAdapter struct {
	client *Client
}

// BridgeAdapter implements the bridge.Chatwoot port.
var _ bridge.Chatwoot = (*BridgeAdapter)(nil)

// NewBridgeAdapter wraps client as the bridge.Chatwoot port. A nil client is a
// wiring bug and panics, matching the other constructor contracts.
func NewBridgeAdapter(client *Client) *BridgeAdapter {
	if client == nil {
		panic("chatwoot: NewBridgeAdapter requires a non-nil client")
	}
	return &BridgeAdapter{client: client}
}

// SearchContactExact returns the unique exact match as a one-element slice, or
// an empty slice when none exists. The client already collapses two or more
// distinct exact matches into ErrIdentityConflict; the adapter maps that to the
// bridge-owned sentinel so the resolver fails closed instead of creating an
// entity (ROUTING_MATRIX §1, G0).
//
// Every other error (transport, HTTP, inconclusive) is passed through with its
// chain intact: the client's *APIError keeps Retryable() so the resolver's
// bounded read retry can classify it, and no error is ever a "not found".
func (a *BridgeAdapter) SearchContactExact(ctx context.Context, identity string, kind bridge.Kind) ([]bridge.Contact, error) {
	cwKind, err := toChatwootKind(kind)
	if err != nil {
		return nil, err
	}
	match, err := a.client.SearchContactExact(ctx, identity, cwKind)
	if err != nil {
		if errors.Is(err, ErrIdentityConflict) {
			return nil, fmt.Errorf("%w: %w", bridge.ErrIdentityConflict, err)
		}
		return nil, err
	}
	if match == nil {
		return nil, nil
	}
	return []bridge.Contact{toBridgeContact(*match)}, nil
}

// CreateContact creates a contact. Its error outcome is unconfirmed; the
// resolver wraps any returned error in bridge.ErrUnconfirmed, so the adapter
// returns the client error unchanged.
func (a *BridgeAdapter) CreateContact(ctx context.Context, params bridge.ContactParams) (*bridge.Contact, error) {
	cwKind, err := toChatwootKind(params.Kind)
	if err != nil {
		return nil, err
	}
	created, err := a.client.CreateContact(ctx, ContactParams{
		Kind:     cwKind,
		Identity: params.Identity,
		Name:     params.Name,
	})
	if err != nil {
		return nil, err
	}
	contact := toBridgeContact(*created)
	return &contact, nil
}

// UpdateContactName sets a contact's display name. The name policy lives in the
// engine; the adapter only forwards the call.
func (a *BridgeAdapter) UpdateContactName(ctx context.Context, contactID int, name string) error {
	return a.client.UpdateContactName(ctx, contactID, name)
}

// ListConversations returns a contact's conversations. A client error is never
// "no candidate" (Q4) and is returned unchanged.
func (a *BridgeAdapter) ListConversations(ctx context.Context, contactID int) ([]bridge.Conversation, error) {
	conversations, err := a.client.ListConversations(ctx, contactID)
	if err != nil {
		return nil, err
	}
	out := make([]bridge.Conversation, 0, len(conversations))
	for _, conversation := range conversations {
		out = append(out, toBridgeConversation(conversation))
	}
	return out, nil
}

// CreateConversation creates an open conversation in inboxID. Its error outcome
// is unconfirmed; the resolver wraps any returned error in bridge.ErrUnconfirmed.
func (a *BridgeAdapter) CreateConversation(ctx context.Context, contactID, inboxID int) (*bridge.Conversation, error) {
	created, err := a.client.CreateConversation(ctx, contactID, inboxID)
	if err != nil {
		return nil, err
	}
	conversation := toBridgeConversation(*created)
	return &conversation, nil
}

// SendText posts a text message and returns the Chatwoot message id as the
// correlation id for echo suppression. Its error outcome is unconfirmed; the
// engine quarantines any error or missing id and never retries.
func (a *BridgeAdapter) SendText(
	ctx context.Context,
	conversationID int,
	content string,
	direction bridge.MessageDirection,
) (*bridge.SendResult, error) {
	messageType, err := toChatwootMessageType(direction)
	if err != nil {
		return nil, err
	}
	message, err := a.client.SendText(ctx, conversationID, content, messageType)
	if err != nil {
		return nil, err
	}
	if message.ID <= 0 {
		// The client's decoder rejects a non-positive id; keep the guard so an
		// empty correlation id can never look like a confirmed send.
		return nil, errors.New("chatwoot: sent message has no id")
	}
	return &bridge.SendResult{MessageID: strconv.Itoa(message.ID)}, nil
}

func toChatwootKind(kind bridge.Kind) (Kind, error) {
	switch kind {
	case bridge.KindDirect:
		return KindDirect, nil
	case bridge.KindGroup:
		return KindGroup, nil
	default:
		return KindUnknown, fmt.Errorf("chatwoot: invalid bridge contact kind %d", kind)
	}
}

func toChatwootMessageType(direction bridge.MessageDirection) (MessageType, error) {
	switch direction {
	case bridge.DirectionIncoming:
		return MessageTypeIncoming, nil
	case bridge.DirectionOutgoing:
		return MessageTypeOutgoing, nil
	default:
		return "", fmt.Errorf("chatwoot: invalid bridge message direction %q", direction)
	}
}

func toBridgeContact(contact Contact) bridge.Contact {
	return bridge.Contact{
		ID:          contact.ID,
		Name:        contact.Name,
		PhoneNumber: contact.PhoneNumber,
		Identifier:  contact.Identifier,
	}
}

func toBridgeConversation(conversation Conversation) bridge.Conversation {
	return bridge.Conversation{
		ID:             conversation.ID,
		InboxID:        conversation.InboxID,
		Status:         conversation.Status,
		LastActivityAt: conversation.LastActivityAt,
	}
}
