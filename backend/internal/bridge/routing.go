package bridge

// Action is the routing verdict for one event.
type Action int

const (
	// ActionUnknown is the zero value: an unset or unrecognised verdict. The
	// engine fails closed on it rather than assuming a forward.
	ActionUnknown Action = iota
	// ActionIgnore drops the event without touching any port (E7/E8 and
	// connection policy).
	ActionIgnore
	// ActionFail is fail-attend: the event is logged and the claim closed, but
	// no entity is created and nothing is sent.
	ActionFail
	// ActionForward resolves identity and forwards the message.
	ActionForward
)

// Decision is the frozen matrix verdict (ROUTING_MATRIX.md §4, E1–E8/E14).
type Decision struct {
	Action               Action
	Direction            MessageDirection
	AllowNewConversation bool // Q7: creation policy for this event
	Reason               string
}

// Route maps a parsed event and connection policy to a Decision. Identity is
// validated separately (E14) so a malformed identity touches no port.
func Route(p *ParsedEvent, conn Connection) Decision {
	switch {
	case !conn.IsActive:
		return Decision{Action: ActionFail, Reason: "connection inactive"}
	case isBroadcastJID(p.ChatJID):
		return Decision{Action: ActionIgnore, Reason: "broadcast or newsletter"}
	case !forwardableType(p.MessageType):
		return Decision{Action: ActionIgnore, Reason: "unsupported message type"}
	case conn.IgnoreGroups && p.IsGroup:
		return Decision{Action: ActionIgnore, Reason: "groups ignored"}
	}

	direction := DirectionIncoming
	if p.IsFromMe {
		direction = DirectionOutgoing
	}
	return Decision{
		Action:               ActionForward,
		Direction:            direction,
		AllowNewConversation: true, // E1/E2/E4 allow; E3/E5/E7/E8/E10/E12/E13 never reach here
		Reason:               "forward",
	}
}

// forwardableType reports whether the message type is forwarded. E8 protocol,
// reaction and sticker messages are ignored (ROUTING_MATRIX.md §4).
func forwardableType(messageType string) bool {
	switch messageType {
	case MessageTypeProtocol, MessageTypeReaction, MessageTypeSticker:
		return false
	default:
		return true
	}
}
