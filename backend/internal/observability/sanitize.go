package observability

import (
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"
)

// maxSanitizedPayloadBytes caps the stored audit envelope (ADR-0004 §10, 64 KiB).
const maxSanitizedPayloadBytes = 64 * 1024

// sanitizeSchemaVersion identifies the canonical envelope shape.
const sanitizeSchemaVersion = 1

// SanitizeWebhookPayload is the single choke point that produces the audit
// envelope stored in processed_messages.sanitized_payload (ADR-0006 §9, B3).
// Nothing else shapes that JSON.
//
// It copies only an allowlisted, canonicalised projection of an Evolution
// webhook: the event name, Info.*, groupData, the in-group participant and a
// content-free message summary (kind and lengths/flags). Media bytes/base64,
// message text and captions (only their lengths), tokens, cookies, credential
// URLs and any unknown field are never copied. String fields have control
// characters stripped and keys are matched case-insensitively (Evolution's dual
// casing). A projection larger than 64 KiB becomes an explicitly lossy
// {"schema":1,"truncated":true} marker; undecodable JSON becomes
// {"schema":1,"unparsed":true} — never the raw bytes.
func SanitizeWebhookPayload(raw []byte) []byte {
	envelope, ok := projectWebhook(raw)
	if !ok {
		return []byte(`{"schema":1,"unparsed":true}`)
	}
	out, err := json.Marshal(envelope)
	if err != nil || len(out) > maxSanitizedPayloadBytes {
		return []byte(`{"schema":1,"truncated":true}`)
	}
	return out
}

// sanitizeEnvelope is the canonical allowlisted projection. Field order is
// stable (struct order), so the stored JSON is deterministic.
type sanitizeEnvelope struct {
	Schema      int             `json:"schema"`
	Event       string          `json:"event"`
	Info        sanitizeInfo    `json:"info"`
	Group       sanitizeGroup   `json:"group"`
	Participant string          `json:"participant"`
	Message     sanitizeMessage `json:"message"`
}

type sanitizeInfo struct {
	ID        string `json:"id"`
	Chat      string `json:"chat"`
	Sender    string `json:"sender"`
	PushName  string `json:"push_name"`
	IsFromMe  bool   `json:"is_from_me"`
	IsGroup   bool   `json:"is_group"`
	MediaType string `json:"media_type"`
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

type sanitizeGroup struct {
	JID  string `json:"jid"`
	Name string `json:"name"`
}

type sanitizeMessage struct {
	Kind         string `json:"kind"`
	TextLen      int    `json:"text_len"`
	CaptionLen   int    `json:"caption_len"`
	MediaPresent bool   `json:"media_present"`
	MediaBytes   int64  `json:"media_bytes"`
}

// The raw structs below rely on encoding/json's case-insensitive key matching,
// so the GO PascalCase and v2 camelCase families decode through the same fields.
type rawWebhook struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type rawWebhookData struct {
	Info        rawWebhookInfo  `json:"Info"`
	Key         rawWebhookKey   `json:"key"`
	GroupData   rawWebhookGroup `json:"groupData"`
	Participant string          `json:"participant"`
	PushName    string          `json:"pushName"`
	Message     json.RawMessage `json:"Message"`
}

type rawWebhookInfo struct {
	ID        string          `json:"ID"`
	Chat      string          `json:"Chat"`
	Sender    string          `json:"Sender"`
	PushName  string          `json:"PushName"`
	IsFromMe  bool            `json:"IsFromMe"`
	IsGroup   bool            `json:"IsGroup"`
	MediaType string          `json:"MediaType"`
	Type      string          `json:"Type"`
	Timestamp json.RawMessage `json:"Timestamp"`
}

type rawWebhookKey struct {
	RemoteJID   string `json:"remoteJid"`
	Participant string `json:"participant"`
	ID          string `json:"id"`
}

type rawWebhookGroup struct {
	Name string `json:"Name"`
}

// projectWebhook decodes raw into the allowlisted envelope. ok is false for
// input that is not a decodable webhook envelope with a data object.
func projectWebhook(raw []byte) (sanitizeEnvelope, bool) {
	var webhook rawWebhook
	if err := json.Unmarshal(raw, &webhook); err != nil {
		return sanitizeEnvelope{}, false
	}
	if len(webhook.Data) == 0 || strings.TrimSpace(string(webhook.Data)) == "null" {
		return sanitizeEnvelope{}, false
	}
	var data rawWebhookData
	if err := json.Unmarshal(webhook.Data, &data); err != nil {
		return sanitizeEnvelope{}, false
	}

	chat := cleanField(firstNonEmpty(data.Info.Chat, data.Key.RemoteJID))
	isGroup := data.Info.IsGroup || strings.Contains(chat, "@g.us")
	groupJID := ""
	if isGroup {
		groupJID = chat
	}
	kind, textLen, captionLen, mediaPresent, mediaBytes := projectMessage(data.Message)

	return sanitizeEnvelope{
		Schema: sanitizeSchemaVersion,
		Event:  cleanField(webhook.Event),
		Info: sanitizeInfo{
			ID:        cleanField(firstNonEmpty(data.Info.ID, data.Key.ID)),
			Chat:      chat,
			Sender:    cleanField(firstNonEmpty(data.Info.Sender, data.Key.Participant)),
			PushName:  cleanField(firstNonEmpty(data.Info.PushName, data.PushName)),
			IsFromMe:  data.Info.IsFromMe,
			IsGroup:   isGroup,
			MediaType: cleanField(data.Info.MediaType),
			Type:      cleanField(data.Info.Type),
			Timestamp: cleanTimestamp(data.Info.Timestamp),
		},
		Group:       sanitizeGroup{JID: groupJID, Name: cleanField(data.GroupData.Name)},
		Participant: cleanField(data.Key.Participant),
		Message: sanitizeMessage{
			Kind:         kind,
			TextLen:      textLen,
			CaptionLen:   captionLen,
			MediaPresent: mediaPresent,
			MediaBytes:   mediaBytes,
		},
	}, true
}

// projectMessage classifies data.Message without copying any content: only the
// kind, the text/caption rune counts and the media presence/size are returned.
func projectMessage(raw json.RawMessage) (kind string, textLen, captionLen int, mediaPresent bool, mediaBytes int64) {
	if len(raw) == 0 {
		return "unknown", 0, 0, false, 0
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(raw, &message) != nil {
		return "unknown", 0, 0, false, 0
	}

	if text := decodeJSONString(message["conversation"]); text != "" {
		return "text", utf8.RuneCountInString(text), 0, false, 0
	}
	if node, ok := message["extendedTextMessage"]; ok {
		return "text", utf8.RuneCountInString(nestedString(node, "text")), 0, false, 0
	}
	for _, media := range []struct{ field, kind string }{
		{"imageMessage", "image"},
		{"videoMessage", "video"},
		{"audioMessage", "audio"},
		{"documentMessage", "document"},
	} {
		if node, ok := message[media.field]; ok {
			return media.kind, 0, utf8.RuneCountInString(nestedString(node, "caption")), true, nestedInt(node, "fileLength")
		}
	}
	switch {
	case hasJSONKey(message, "stickerMessage"):
		return "sticker", 0, 0, false, 0
	case hasJSONKey(message, "reactionMessage"):
		return "reaction", 0, 0, false, 0
	case hasJSONKey(message, "protocolMessage"):
		return "protocol", 0, 0, false, 0
	case hasJSONKey(message, "contactMessage"), hasJSONKey(message, "contactsArrayMessage"):
		return "contact", 0, 0, false, 0
	case hasJSONKey(message, "locationMessage"):
		return "location", 0, 0, false, 0
	}
	return "unknown", 0, 0, false, 0
}

// SanitizeChatwootPayload is the single choke point that produces the audit
// envelope stored for a Chatwoot webhook (spec §1.7). It projects only an
// allowlist: the event, the message id/type, the private flag, the sender type,
// the conversation/inbox ids and content/attachment *counts and types*. The
// content text (only its rune length), every attachment data_url/url, the sender
// email, tokens, headers, cookies and the raw payload are never copied. The
// Evolution sanitizer would render every CW row {"unparsed":true} (the envelope
// has no `data` object); this projection gives a real audit trail without
// inventing fields. Undecodable JSON becomes {"schema":1,"unparsed":true}.
func SanitizeChatwootPayload(raw []byte) []byte {
	envelope, ok := projectChatwoot(raw)
	if !ok {
		return []byte(`{"schema":1,"unparsed":true}`)
	}
	out, err := json.Marshal(envelope)
	if err != nil || len(out) > maxSanitizedPayloadBytes {
		return []byte(`{"schema":1,"truncated":true}`)
	}
	return out
}

// chatwootEnvelope is the canonical allowlisted Chatwoot projection. Field
// order is stable (struct order), so the stored JSON is deterministic.
type chatwootEnvelope struct {
	Schema          int      `json:"schema"`
	System          string   `json:"system"`
	Event           string   `json:"event"`
	ID              string   `json:"id"`
	MessageType     string   `json:"message_type"`
	Private         bool     `json:"private"`
	SenderType      string   `json:"sender_type"`
	ConversationID  int      `json:"conversation_id"`
	InboxID         int      `json:"inbox_id"`
	ContentLen      int      `json:"content_len"`
	AttachmentCount int      `json:"attachment_count"`
	AttachmentTypes []string `json:"attachment_types"`
}

type rawChatwootWebhook struct {
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

// projectChatwoot decodes raw into the allowlisted envelope. ok is false for
// input that is not decodable JSON (there is no required object to demand: an
// empty object still yields a valid, empty projection).
func projectChatwoot(raw []byte) (chatwootEnvelope, bool) {
	var webhook rawChatwootWebhook
	if err := json.Unmarshal(raw, &webhook); err != nil {
		return chatwootEnvelope{}, false
	}
	content := decodeJSONString(webhook.Content)
	return chatwootEnvelope{
		Schema:          sanitizeSchemaVersion,
		System:          "chatwoot",
		Event:           cleanField(decodeJSONString(webhook.Event)),
		ID:              cleanField(firstNonEmpty(decodeJSONString(webhook.ID), jsonNumberString(webhook.ID))),
		MessageType:     cleanField(decodeJSONString(webhook.MessageType)),
		Private:         decodeJSONBool(webhook.Private),
		SenderType:      cleanField(nestedString(webhook.Sender, "type")),
		ConversationID:  chatwootInt(webhook.Conversation, "id"),
		InboxID:         firstNonZero(chatwootInt(webhook.Conversation, "inbox_id"), chatwootInt(webhook.Inbox, "id")),
		ContentLen:      utf8.RuneCountInString(content),
		AttachmentCount: chatwootAttachmentCount(webhook.Attachments),
		AttachmentTypes: chatwootAttachmentTypes(webhook.Attachments),
	}, true
}

// chatwootInt decodes obj[field] as a positive int, or 0.
func chatwootInt(obj json.RawMessage, field string) int {
	value := nestedInt(obj, field)
	if value <= 0 || value > math.MaxInt {
		return 0
	}
	return int(value)
}

// chatwootAttachmentCount returns len(obj), or 0 when obj is absent/non-array.
func chatwootAttachmentCount(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return 0
	}
	return len(list)
}

// chatwootAttachmentTypes returns the non-empty file_type values (only the
// type, never the attachment body/URL).
func chatwootAttachmentTypes(raw json.RawMessage) []string {
	types := []string{}
	if len(raw) == 0 {
		return types
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return types
	}
	for _, item := range list {
		if ft := cleanField(nestedString(item, "file_type")); ft != "" {
			types = append(types, ft)
		}
	}
	return types
}

// jsonNumberString renders a JSON number as its literal, or "" for a non-number.
func jsonNumberString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return ""
	}
	return number.String()
}

// decodeJSONBool decodes a JSON boolean, or false for absent/non-bool.
func decodeJSONBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false
	}
	return b
}

// firstNonZero returns the first non-zero value, or 0.
func firstNonZero(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// cleanField trims a string and strips ASCII control characters so an audit
// value can never carry a newline/log-injection payload.
func cleanField(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

// cleanTimestamp renders an Evolution timestamp (string or number) as a clean
// string, or "" when absent/unrecognised.
func cleanTimestamp(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return cleanField(text)
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func nestedString(obj json.RawMessage, field string) string {
	var node map[string]json.RawMessage
	if json.Unmarshal(obj, &node) != nil {
		return ""
	}
	return decodeJSONString(node[field])
}

func nestedInt(obj json.RawMessage, field string) int64 {
	var node map[string]json.RawMessage
	if json.Unmarshal(obj, &node) != nil {
		return 0
	}
	var number json.Number
	if json.Unmarshal(node[field], &number) != nil {
		return 0
	}
	value, err := number.Int64()
	if err != nil {
		return 0
	}
	return value
}

func decodeJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}

func hasJSONKey(message map[string]json.RawMessage, key string) bool {
	raw, ok := message[key]
	return ok && len(raw) > 0 && string(raw) != "null"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
