package bridge

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
)

// ChatwootEvent is the bridge-owned view of one Chatwoot webhook envelope
// (docs/contracts/chatwoot/webhooks.md). The envelope is flat — there is no
// `data` object — and `conversation.id` is the Chatwoot display_id, never the
// internal DB id (§3). Every field is decoded leniently so one divergent render
// cannot turn an otherwise classifiable event into a parse error.
type ChatwootEvent struct {
	EventName       string   // payload.event
	MessageID       string   // payload.id (number or string), stringified
	MessageType     string   // payload.message_type (webhook string form)
	Private         bool     // payload.private
	SenderType      string   // payload.sender.type ("user" | "agent_bot" | "")
	SenderName      string   // payload.sender.name (log only; never persisted raw)
	Content         string   // payload.content (may be null → "")
	ConversationID  int      // payload.conversation.id — display_id, not the DB id
	InboxID         int      // payload.conversation.inbox_id / inbox.id (audit only)
	AttachmentCount int      // len(payload.attachments)
	AttachmentTypes []string // file_type values only (audit only)
}

// ParseChatwoot decodes a Chatwoot webhook envelope. Malformed JSON is an
// error, not an empty event. A missing/non-positive `id` yields an empty
// MessageID rather than a parse error, so a non-`message_created` envelope with
// no message id is still classifiable as ignored (F1) instead of malformed; the
// ingress answers 400 for an empty id and the engine fails the claim.
func ParseChatwoot(payload []byte) (*ChatwootEvent, error) {
	var raw rawChatwootEnvelope
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, errors.New("bridge: malformed chatwoot payload")
	}
	conversationID, conversationInboxID := decodeChatwootConversation(raw.Conversation)
	senderType, senderName := decodeChatwootSender(raw.Sender)
	attachmentCount, attachmentTypes := decodeChatwootAttachments(raw.Attachments)
	return &ChatwootEvent{
		EventName:       decodeChatwootString(raw.Event),
		MessageID:       decodeChatwootID(raw.ID),
		MessageType:     decodeChatwootString(raw.MessageType),
		Private:         decodeChatwootBool(raw.Private),
		SenderType:      senderType,
		SenderName:      senderName,
		Content:         decodeChatwootText(raw.Content),
		ConversationID:  conversationID,
		InboxID:         firstPositive(conversationInboxID, decodeChatwootInbox(raw.Inbox)),
		AttachmentCount: attachmentCount,
		AttachmentTypes: attachmentTypes,
	}, nil
}

// rawChatwootEnvelope keeps every field as RawMessage: a divergent type on one
// field is decoded to its zero value rather than failing the whole envelope.
type rawChatwootEnvelope struct {
	Event        json.RawMessage `json:"event"`
	ID           json.RawMessage `json:"id"`
	MessageType  json.RawMessage `json:"message_type"`
	Private      json.RawMessage `json:"private"`
	Content      json.RawMessage `json:"content"`
	Conversation json.RawMessage `json:"conversation"`
	Inbox        json.RawMessage `json:"inbox"`
	Sender       json.RawMessage `json:"sender"`
	Attachments  json.RawMessage `json:"attachments"`
}

// decodeChatwootConversation returns conversation.id (display_id) and
// conversation.inbox_id, or (0, 0) for an absent/unrecognised object.
func decodeChatwootConversation(raw json.RawMessage) (id, inboxID int) {
	if len(raw) == 0 {
		return 0, 0
	}
	var c struct {
		ID      json.RawMessage `json:"id"`
		InboxID json.RawMessage `json:"inbox_id"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return 0, 0
	}
	return decodeChatwootInt(c.ID), decodeChatwootInt(c.InboxID)
}

// decodeChatwootInbox returns inbox.id, or 0 for an absent/unrecognised object.
func decodeChatwootInbox(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var i struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(raw, &i) != nil {
		return 0
	}
	return decodeChatwootInt(i.ID)
}

// decodeChatwootSender returns sender.type and sender.name (both trimmed).
func decodeChatwootSender(raw json.RawMessage) (senderType, name string) {
	if len(raw) == 0 {
		return "", ""
	}
	var s struct {
		Type json.RawMessage `json:"type"`
		Name json.RawMessage `json:"name"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return "", ""
	}
	return decodeChatwootString(s.Type), decodeChatwootString(s.Name)
}

// decodeChatwootAttachments returns the attachment count and the non-empty
// file_type values (audit only; attachment bodies are never copied).
func decodeChatwootAttachments(raw json.RawMessage) (int, []string) {
	types := []string{}
	if len(raw) == 0 {
		return 0, types
	}
	var list []struct {
		FileType json.RawMessage `json:"file_type"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return 0, types
	}
	for _, a := range list {
		if ft := decodeChatwootString(a.FileType); ft != "" {
			types = append(types, ft)
		}
	}
	return len(list), types
}

// decodeChatwootString decodes a JSON string, trimmed, or "" for
// absent/null/non-string.
func decodeChatwootString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// decodeChatwootText decodes a JSON string verbatim (no trimming): the
// forwarded content must reach WhatsApp byte-identical.
func decodeChatwootText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// decodeChatwootBool decodes a JSON boolean, or false for absent/non-bool.
func decodeChatwootBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false
	}
	return b
}

// decodeChatwootInt decodes a positive integer from a number or numeric string,
// or 0 for absent/null/non-numeric/non-positive/overflow.
func decodeChatwootInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		v, err := number.Int64()
		if err != nil || v <= 0 || v > math.MaxInt {
			return 0
		}
		return int(v)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, err := strconv.Atoi(strings.TrimSpace(s))
		if err == nil && v > 0 {
			return v
		}
	}
	return 0
}

// decodeChatwootID stringifies payload.id (number or string) and rejects an
// absent/non-positive/empty value ("").
func decodeChatwootID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.TrimSpace(s)
		if s == "" || s == "0" {
			return ""
		}
		return s
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		v, err := number.Int64()
		if err != nil || v <= 0 {
			return ""
		}
		return number.String()
	}
	return ""
}

// firstPositive returns the first positive value, or 0.
func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}
