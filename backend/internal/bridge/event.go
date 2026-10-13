package bridge

import (
	"encoding/json"
	"errors"
	"strings"
)

// Event is one inbound Evolution webhook delivery, already scoped to a
// connection by ingress.
type Event struct {
	Connection Connection
	Payload    []byte
}

// Message type categories used by routing (E8 ignores protocol/reaction/sticker).
const (
	MessageTypeText     = "text"
	MessageTypeImage    = "image"
	MessageTypeVideo    = "video"
	MessageTypeAudio    = "audio"
	MessageTypeDocument = "document"
	MessageTypeSticker  = "sticker"
	MessageTypeReaction = "reaction"
	MessageTypeProtocol = "protocol"
	MessageTypeContact  = "contact"
	MessageTypeLocation = "location"
	MessageTypeUnknown  = "unknown"
)

// ParsedEvent is the bridge-owned view of one Evolution message event. It
// accepts both the GO PascalCase (`data.Info.*`) and the v2 camelCase shapes
// (behaviours-to-preserve §B4). IsFromMe/IsGroup are real JSON booleans
// (production-recon); groupData.Name is the only source of a group name.
type ParsedEvent struct {
	EventName   string
	MessageID   string
	ChatJID     string
	SenderJID   string
	PushName    string
	GroupName   string // groupData.Name; "" when absent (never a placeholder)
	MediaType   string
	MessageType string
	Text        string
	IsFromMe    bool
	IsGroup     bool
}

// Content returns the textual content the bridge forwards, falling back to a
// type marker so a media message without a caption is still visible.
func (p *ParsedEvent) Content() string {
	if p.Text != "" {
		return p.Text
	}
	return "[" + p.MessageType + "]"
}

// rawEnvelope is the webhook root: {event, data}.
type rawEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// The raw structs below rely on encoding/json's case-insensitive key matching:
// the tag `Chat` also matches `chat`, so the GO PascalCase and v2 camelCase
// families decode through the same fields (behaviours-to-preserve §B4).
type rawData struct {
	Info      rawInfo         `json:"Info"`
	Key       rawKey          `json:"key"`
	GroupData rawGroupData    `json:"groupData"`
	PushName  string          `json:"pushName"`
	Message   json.RawMessage `json:"Message"`
}

type rawInfo struct {
	Chat      string `json:"Chat"`
	Sender    string `json:"Sender"`
	ID        string `json:"ID"`
	IsFromMe  bool   `json:"IsFromMe"`
	IsGroup   bool   `json:"IsGroup"`
	PushName  string `json:"PushName"`
	MediaType string `json:"MediaType"`
}

type rawKey struct {
	RemoteJID   string `json:"remoteJid"`
	Participant string `json:"participant"`
	FromMe      bool   `json:"fromMe"`
	ID          string `json:"id"`
}

type rawGroupData struct {
	Name string `json:"Name"`
}

// Parse decodes an Evolution webhook envelope. Malformed JSON is an error, not
// an empty event: the engine must not act on an unparseable payload.
func Parse(payload []byte) (*ParsedEvent, error) {
	var env rawEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, errors.New("bridge: malformed event payload")
	}
	if len(env.Data) == 0 || strings.TrimSpace(string(env.Data)) == "null" {
		// An absent or JSON-null data is not a payload: it is unparseable input,
		// not an empty event the engine may act on.
		return nil, errors.New("bridge: event has no data")
	}
	var data rawData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return nil, errors.New("bridge: malformed event data")
	}

	p := &ParsedEvent{EventName: env.Event}
	p.ChatJID = firstNonEmpty(data.Info.Chat, data.Key.RemoteJID)
	p.SenderJID = firstNonEmpty(data.Info.Sender, data.Key.Participant, data.Key.RemoteJID, p.ChatJID)
	p.IsFromMe = data.Info.IsFromMe || data.Key.FromMe
	p.IsGroup = data.Info.IsGroup || isGroupJID(p.ChatJID)
	p.PushName = firstNonEmpty(data.Info.PushName, data.PushName)
	p.GroupName = strings.TrimSpace(data.GroupData.Name)
	p.MessageID = firstNonEmpty(data.Info.ID, data.Key.ID)
	p.MediaType = data.Info.MediaType
	p.MessageType, p.Text = parseMessage(data.Message)
	return p, nil
}

// parseMessage applies the legacy type-priority chain (behaviours-to-preserve
// §B5), reduced to the categories routing and the text-forwarding engine need.
func parseMessage(raw json.RawMessage) (string, string) {
	if len(raw) == 0 {
		return MessageTypeUnknown, ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return MessageTypeUnknown, ""
	}

	if text := decodeString(m["conversation"]); text != "" {
		return MessageTypeText, text
	}
	if ext, ok := m["extendedTextMessage"]; ok {
		return MessageTypeText, nestedString(ext, "text")
	}
	for _, media := range []struct{ field, kind string }{
		{"imageMessage", MessageTypeImage},
		{"videoMessage", MessageTypeVideo},
		{"audioMessage", MessageTypeAudio},
		{"documentMessage", MessageTypeDocument},
	} {
		if node, ok := m[media.field]; ok {
			return media.kind, nestedString(node, "caption")
		}
	}
	switch {
	case hasKey(m, "stickerMessage"):
		return MessageTypeSticker, ""
	case hasKey(m, "reactionMessage"):
		return MessageTypeReaction, ""
	case hasKey(m, "protocolMessage"):
		return MessageTypeProtocol, ""
	case hasKey(m, "contactMessage"), hasKey(m, "contactsArrayMessage"):
		return MessageTypeContact, ""
	case hasKey(m, "locationMessage"):
		return MessageTypeLocation, ""
	}
	return MessageTypeUnknown, ""
}

// nestedString decodes obj[field] as a string, or "" when absent/non-string.
func nestedString(obj json.RawMessage, field string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(obj, &m) != nil {
		return ""
	}
	return decodeString(m[field])
}

// decodeString decodes a JSON string, or "" for absent/null/non-string.
func decodeString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func hasKey(m map[string]json.RawMessage, key string) bool {
	raw, ok := m[key]
	return ok && len(raw) > 0 && string(raw) != "null"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
