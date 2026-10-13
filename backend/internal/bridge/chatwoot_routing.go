package bridge

// Chatwoot webhook enum values the bridge consumes (docs/contracts/chatwoot/
// webhooks.md §3). `message_type` is a STRING in webhooks, not the integer enum.
const (
	chatwootEventMessageCreated = "message_created"
	chatwootMessageTypeIncoming = "incoming"
	chatwootMessageTypeOutgoing = "outgoing"
	chatwootMessageTypeTemplate = "template"
	chatwootSenderAgentBot      = "agent_bot"
)

// ChatwootDecision is the frozen routing verdict for one Chatwoot event
// (ROUTING_MATRIX E10–E13).
type ChatwootDecision struct {
	Action Action
	Reason string
}

// RouteChatwoot applies the payload-only frozen filters in order:
//
//	F1 `event != message_created`              → ignore (E10)
//	F2 `message_type == incoming`              → ignore (E10)
//	   message_type allowlist hardening        → fail   (E12, no entity)
//	F3 `private == true`                       → ignore (E10)
//	F4 `sender.type == agent_bot`              → ignore (E10)
//
// F5 (loop-break), F6 (conversation.id) and F7 (contacts_map) are applied by
// the engine, which owns the ports and preserves the frozen order. Keeping them
// out of this function means the database is only touched for an event that
// passes the payload-only prefix.
//
// Per the Orchestrator decision (spec §7.3), the F2 hardening forwards only
// `outgoing`/`template`; any other non-`incoming` value (e.g. `activity`, "")
// fails closed — MarkFailed with no entity — instead of being forwarded.
func RouteChatwoot(ev *ChatwootEvent, conn Connection) ChatwootDecision {
	switch {
	case !conn.IsActive:
		return ChatwootDecision{Action: ActionFail, Reason: "connection inactive"}
	case ev.EventName != chatwootEventMessageCreated: // F1 → E10
		return ChatwootDecision{Action: ActionIgnore, Reason: "event not message_created"}
	case ev.MessageType == chatwootMessageTypeIncoming: // F2 → E10
		return ChatwootDecision{Action: ActionIgnore, Reason: "incoming message"}
	case !forwardableChatwootType(ev.MessageType): // F2 hardening → E12, fail closed
		return ChatwootDecision{Action: ActionFail, Reason: "unsupported message type"}
	case ev.Private: // F3 → E10
		return ChatwootDecision{Action: ActionIgnore, Reason: "private note"}
	case ev.SenderType == chatwootSenderAgentBot: // F4 → E10
		return ChatwootDecision{Action: ActionIgnore, Reason: "agent bot sender"}
	}
	return ChatwootDecision{Action: ActionForward, Reason: "forward"}
}

// forwardableChatwootType reports whether a Chatwoot message_type is forwarded
// to WhatsApp. Only `outgoing` (an agent reply) and `template` are allowed;
// `incoming` is handled earlier and every other value fails closed.
func forwardableChatwootType(messageType string) bool {
	switch messageType {
	case chatwootMessageTypeOutgoing, chatwootMessageTypeTemplate:
		return true
	default:
		return false
	}
}
