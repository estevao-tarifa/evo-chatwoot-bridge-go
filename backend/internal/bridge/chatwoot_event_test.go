package bridge

import (
	"testing"
)

// cwOpts builds a synthetic Chatwoot webhook. A nil field is omitted; a
// non-nil field is emitted verbatim so tests can drive divergent types. No real
// client phone numbers, names or group ids are used (AGENTS §9).
type cwOpts struct {
	event          string
	id             any
	messageType    any
	private        any
	senderType     string
	senderName     string
	content        any
	conversationID any
	inboxID        any
	attachments    []map[string]any
	extra          map[string]any
}

func buildChatwootPayload(o cwOpts) []byte {
	event := o.event
	if event == "" {
		event = "message_created"
	}
	payload := map[string]any{"event": event}
	if o.id != nil {
		payload["id"] = o.id
	}
	if o.messageType != nil {
		payload["message_type"] = o.messageType
	}
	if o.private != nil {
		payload["private"] = o.private
	}
	if o.content != nil {
		payload["content"] = o.content
	}
	conversation := map[string]any{}
	if o.conversationID != nil {
		conversation["id"] = o.conversationID
	}
	if o.inboxID != nil {
		conversation["inbox_id"] = o.inboxID
	}
	if len(conversation) > 0 {
		payload["conversation"] = conversation
	}
	sender := map[string]any{}
	if o.senderType != "" {
		sender["type"] = o.senderType
	}
	if o.senderName != "" {
		sender["name"] = o.senderName
	}
	if len(sender) > 0 {
		payload["sender"] = sender
	}
	if o.attachments != nil {
		payload["attachments"] = o.attachments
	}
	for key, value := range o.extra {
		payload[key] = value
	}
	return mustJSON(payload)
}

// TestParseChatwoot covers the field decoding rules of spec §2.2: `id` accepts
// number or string, `conversation.id` is the display_id, `message_type` is a
// STRING, `private` is a bool and every field is decoded leniently.
func TestParseChatwoot(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		assert  func(t *testing.T, ev *ChatwootEvent)
	}{
		{
			name: "full envelope",
			payload: buildChatwootPayload(cwOpts{
				id: 10001, messageType: "outgoing", private: false, senderType: "user",
				senderName: "Agent Synthetic", content: "hello", conversationID: 5001,
				inboxID: 10, attachments: []map[string]any{{"file_type": "image"}},
			}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.EventName != "message_created" || ev.MessageID != "10001" {
					t.Errorf("event/id = %q/%q", ev.EventName, ev.MessageID)
				}
				if ev.MessageType != "outgoing" || ev.Private {
					t.Errorf("message_type/private = %q/%v", ev.MessageType, ev.Private)
				}
				if ev.SenderType != "user" || ev.SenderName != "Agent Synthetic" {
					t.Errorf("sender = %q/%q", ev.SenderType, ev.SenderName)
				}
				if ev.Content != "hello" || ev.ConversationID != 5001 || ev.InboxID != 10 {
					t.Errorf("content/conv/inbox = %q/%d/%d", ev.Content, ev.ConversationID, ev.InboxID)
				}
				if ev.AttachmentCount != 1 || len(ev.AttachmentTypes) != 1 || ev.AttachmentTypes[0] != "image" {
					t.Errorf("attachments = %d/%v", ev.AttachmentCount, ev.AttachmentTypes)
				}
			},
		},
		{
			name:    "string id and numeric-string conversation",
			payload: buildChatwootPayload(cwOpts{id: "10001", messageType: "outgoing", conversationID: "5001"}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.MessageID != "10001" || ev.ConversationID != 5001 {
					t.Errorf("id/conv = %q/%d", ev.MessageID, ev.ConversationID)
				}
			},
		},
		{
			name:    "content is preserved verbatim",
			payload: buildChatwootPayload(cwOpts{id: 1, content: "  spaced  "}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.Content != "  spaced  " {
					t.Errorf("content = %q, want verbatim", ev.Content)
				}
			},
		},
		{
			name:    "absent id is empty, not an error",
			payload: buildChatwootPayload(cwOpts{event: "conversation_created", messageType: "outgoing"}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.EventName != "conversation_created" || ev.MessageID != "" {
					t.Errorf("event/id = %q/%q", ev.EventName, ev.MessageID)
				}
			},
		},
		{
			name:    "zero and non-positive id are empty",
			payload: buildChatwootPayload(cwOpts{id: 0}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.MessageID != "" {
					t.Errorf("id = %q, want empty", ev.MessageID)
				}
			},
		},
		{
			name:    "non-string message_type is empty",
			payload: buildChatwootPayload(cwOpts{id: 1, messageType: 1}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.MessageType != "" {
					t.Errorf("message_type = %q, want empty", ev.MessageType)
				}
			},
		},
		{
			name:    "non-bool private is false",
			payload: buildChatwootPayload(cwOpts{id: 1, private: "true"}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.Private {
					t.Error("private = true, want false for a non-bool render")
				}
			},
		},
		{
			name:    "null content is empty",
			payload: buildChatwootPayload(cwOpts{id: 1, extra: map[string]any{"content": nil}}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.Content != "" {
					t.Errorf("content = %q, want empty", ev.Content)
				}
			},
		},
		{
			name:    "missing conversation is zero",
			payload: buildChatwootPayload(cwOpts{id: 1, messageType: "outgoing"}),
			assert: func(t *testing.T, ev *ChatwootEvent) {
				if ev.ConversationID != 0 {
					t.Errorf("conversation id = %d, want 0", ev.ConversationID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := ParseChatwoot(tt.payload)
			if err != nil {
				t.Fatalf("ParseChatwoot() error = %v", err)
			}
			tt.assert(t, ev)
		})
	}
}

// TestParseChatwootMalformed proves malformed JSON is an error, never an empty
// event the engine could act on.
func TestParseChatwootMalformed(t *testing.T) {
	if _, err := ParseChatwoot([]byte("{not json")); err == nil {
		t.Fatal("ParseChatwoot(malformed) error = nil, want an error")
	}
	if _, err := ParseChatwoot([]byte(`"just a string"`)); err == nil {
		t.Fatal("ParseChatwoot(non-object) error = nil, want an error")
	}
}
