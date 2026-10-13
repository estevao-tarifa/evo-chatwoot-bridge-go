package bridge

import "testing"

// TestRouteChatwoot is the frozen-order table for the payload-only filters
// F1–F4 plus the message_type allowlist (spec §2.3, Orchestrator decision 3).
// F5/F6/F7 are applied by the engine and are covered in engine_chatwoot_test.go.
func TestRouteChatwoot(t *testing.T) {
	active := Connection{ID: 1, ChatwootInboxID: 10, IsActive: true}

	tests := []struct {
		name string
		ev   *ChatwootEvent
		conn Connection
		want Action
	}{
		{
			name: "F1 event not message_created",
			ev:   &ChatwootEvent{EventName: "conversation_created", MessageType: "outgoing"},
			conn: active, want: ActionIgnore,
		},
		{
			name: "F2 incoming is ignored",
			ev:   &ChatwootEvent{EventName: "message_created", MessageType: "incoming"},
			conn: active, want: ActionIgnore,
		},
		{
			name: "F2 hardening activity fails closed",
			ev:   &ChatwootEvent{EventName: "message_created", MessageType: "activity"},
			conn: active, want: ActionFail,
		},
		{
			name: "F2 hardening empty type fails closed",
			ev:   &ChatwootEvent{EventName: "message_created", MessageType: ""},
			conn: active, want: ActionFail,
		},
		{
			name: "F3 private note is ignored",
			ev:   &ChatwootEvent{EventName: "message_created", MessageType: "outgoing", Private: true},
			conn: active, want: ActionIgnore,
		},
		{
			name: "F4 agent bot is ignored",
			ev:   &ChatwootEvent{EventName: "message_created", MessageType: "outgoing", SenderType: "agent_bot"},
			conn: active, want: ActionIgnore,
		},
		{
			name: "outgoing forwards",
			ev:   &ChatwootEvent{EventName: "message_created", MessageType: "outgoing", SenderType: "user"},
			conn: active, want: ActionForward,
		},
		{
			name: "template forwards",
			ev:   &ChatwootEvent{EventName: "message_created", MessageType: "template"},
			conn: active, want: ActionForward,
		},
		{
			name: "inactive connection fails closed",
			ev:   &ChatwootEvent{EventName: "message_created", MessageType: "outgoing"},
			conn: Connection{ID: 1, IsActive: false}, want: ActionFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RouteChatwoot(tt.ev, tt.conn)
			if got.Action != tt.want {
				t.Errorf("RouteChatwoot() action = %v (%s), want %v", got.Action, got.Reason, tt.want)
			}
		})
	}
}
