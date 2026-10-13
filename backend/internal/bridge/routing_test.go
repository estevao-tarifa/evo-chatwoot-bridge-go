package bridge

import "testing"

// TestActionUnknownZeroValue covers L3: the zero Action is a sentinel, so an
// unset Decision fails closed instead of reading as a real verdict.
func TestActionUnknownZeroValue(t *testing.T) {
	if ActionUnknown != 0 {
		t.Errorf("ActionUnknown = %d, want 0", ActionUnknown)
	}
}

func TestRoute(t *testing.T) {
	active := Connection{ID: 1, ChatwootInboxID: 10, IsActive: true}
	cases := []struct {
		name    string
		parsed  ParsedEvent
		conn    Connection
		action  Action
		dir     MessageDirection
		allowCV bool
	}{
		{
			name:    "E1 inbound 1:1",
			parsed:  ParsedEvent{ChatJID: testChatJID, MessageType: MessageTypeText},
			conn:    active,
			action:  ActionForward,
			dir:     DirectionIncoming,
			allowCV: true,
		},
		{
			name:    "E2 external IsFromMe",
			parsed:  ParsedEvent{ChatJID: testChatJID, IsFromMe: true, MessageType: MessageTypeText},
			conn:    active,
			action:  ActionForward,
			dir:     DirectionOutgoing,
			allowCV: true,
		},
		{
			name:   "E7 broadcast",
			parsed: ParsedEvent{ChatJID: "status@broadcast", MessageType: MessageTypeText},
			conn:   active,
			action: ActionIgnore,
		},
		{
			name:   "E7 newsletter",
			parsed: ParsedEvent{ChatJID: "123@newsletter", MessageType: MessageTypeText},
			conn:   active,
			action: ActionIgnore,
		},
		{
			name:   "E8 protocol",
			parsed: ParsedEvent{ChatJID: testChatJID, MessageType: MessageTypeProtocol},
			conn:   active,
			action: ActionIgnore,
		},
		{
			name:   "E8 sticker",
			parsed: ParsedEvent{ChatJID: testChatJID, MessageType: MessageTypeSticker},
			conn:   active,
			action: ActionIgnore,
		},
		{
			name:   "groups ignored",
			parsed: ParsedEvent{ChatJID: testGroupJID, IsGroup: true, MessageType: MessageTypeText},
			conn:   Connection{ID: 1, ChatwootInboxID: 10, IsActive: true, IgnoreGroups: true},
			action: ActionIgnore,
		},
		{
			name:   "inactive connection",
			parsed: ParsedEvent{ChatJID: testChatJID, MessageType: MessageTypeText},
			conn:   Connection{ID: 1, IsActive: false},
			action: ActionFail,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Route(&tc.parsed, tc.conn)
			if got.Action != tc.action {
				t.Fatalf("Action = %v, want %v", got.Action, tc.action)
			}
			if tc.action == ActionForward {
				if got.Direction != tc.dir {
					t.Errorf("Direction = %q, want %q", got.Direction, tc.dir)
				}
				if got.AllowNewConversation != tc.allowCV {
					t.Errorf("AllowNewConversation = %v, want %v", got.AllowNewConversation, tc.allowCV)
				}
			}
		})
	}
}
