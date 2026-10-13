package chatwoot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Conversation is the subset of a Chatwoot conversation the bridge needs.
// ID is Chatwoot's internal conversation id, not the account-scoped display_id
// (conversations.md §1; the legacy caller reads conversation.id).
type Conversation struct {
	ID             int    `json:"id"`
	InboxID        int    `json:"inbox_id"`
	Status         string `json:"status"`
	LastActivityAt int64  `json:"last_activity_at"`
	ContactID      int    `json:"contact_id,omitempty"`
}

type conversationsResponse struct {
	Payload []Conversation `json:"payload"`
}

// UnmarshalJSON decodes the frozen conversations shape {payload}
// (conversations.md §1). A body without payload is rejected as malformed, never
// decoded as an empty list: treating it as "no conversation" would let the
// engine CREATE a conversation that already exists (G0, ROUTING_MATRIX.md §1,
// finding M2).
func (r *conversationsResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Payload *[]Conversation `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Payload == nil {
		return errors.New("chatwoot: conversations response missing payload")
	}
	r.Payload = *raw.Payload
	return nil
}

// ListConversations returns a contact's conversations. A transport or HTTP
// failure is returned as an error, never as an empty list (G0).
func (c *Client) ListConversations(ctx context.Context, contactID int) ([]Conversation, error) {
	if contactID <= 0 {
		return nil, errors.New("chatwoot: contact id must be positive")
	}
	var resp conversationsResponse
	path := c.accountURL("contacts/" + strconv.Itoa(contactID) + "/conversations")
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Payload, nil
}

// CreateConversation creates an open conversation for contactID in inboxID
// (conversations.md §2).
func (c *Client) CreateConversation(ctx context.Context, contactID, inboxID int) (*Conversation, error) {
	if contactID <= 0 {
		return nil, errors.New("chatwoot: contact id must be positive")
	}
	if inboxID <= 0 {
		return nil, errors.New("chatwoot: inbox id must be positive") // Q5: null inbox fails upstream
	}

	body := struct {
		ContactID int    `json:"contact_id"`
		InboxID   int    `json:"inbox_id"`
		Status    string `json:"status"`
	}{
		ContactID: contactID,
		InboxID:   inboxID,
		Status:    "open",
	}

	var conv Conversation
	if err := c.doJSON(ctx, http.MethodPost, c.accountURL("conversations"), body, &conv); err != nil {
		return nil, err
	}
	return &conv, nil
}

// SelectConversation returns the conversation the bridge may reuse: same inbox
// and not resolved (Q2), the most recently active with id as tie-break (Q3).
// It returns nil when no candidate qualifies. Errors belong to ListConversations
// and are never interpreted as "no candidate" (Q4).
func SelectConversation(conversations []Conversation, inboxID int) *Conversation {
	var best *Conversation
	for i := range conversations {
		conv := &conversations[i]
		if conv.InboxID != inboxID || conv.Status == "resolved" {
			continue
		}
		if best == nil || newerConversation(conv, best) {
			best = conv
		}
	}
	return best
}

func newerConversation(a, b *Conversation) bool {
	if a.LastActivityAt != b.LastActivityAt {
		return a.LastActivityAt > b.LastActivityAt
	}
	return a.ID > b.ID
}
