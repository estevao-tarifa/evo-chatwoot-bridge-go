package chatwoot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

// MessageType is the direction requested when creating a message
// (messages.md §2.1): incoming = customer, outgoing = agent/external own.
type MessageType string

const (
	MessageTypeIncoming MessageType = "incoming"
	MessageTypeOutgoing MessageType = "outgoing"
)

func (m MessageType) valid() bool {
	return m == MessageTypeIncoming || m == MessageTypeOutgoing
}

// MessageTypeCode is the direction of a message decoded from a REST response.
//
// The binding contract (messages.md §2.2) says Chatwoot renders the integer enum
// 0..3 while the research note (research/chatwoot.md §4) says the string form.
// Both are accepted: the bridge keys loop suppression off the message id, so a
// divergent render must never fail the whole response decode and lose that id.
//
// The zero value is unknown, so an absent or unrecognised message_type is never
// mistaken for a real direction (finding LOW-4). The internal values are
// deliberately not the wire enum.
type MessageTypeCode int

const (
	MessageTypeCodeUnknown MessageTypeCode = iota // zero value: absent/unrecognised
	MessageTypeCodeIncoming
	MessageTypeCodeOutgoing
	MessageTypeCodeActivity
	MessageTypeCodeTemplate
)

// String returns the string form of the code ("incoming", "outgoing", ...).
func (c MessageTypeCode) String() string {
	switch c {
	case MessageTypeCodeIncoming:
		return "incoming"
	case MessageTypeCodeOutgoing:
		return "outgoing"
	case MessageTypeCodeActivity:
		return "activity"
	case MessageTypeCodeTemplate:
		return "template"
	default:
		return "unknown"
	}
}

// UnmarshalJSON accepts the integer enum or the string form of message_type.
// Absent, null and unrecognised values decode to MessageTypeCodeUnknown.
func (c *MessageTypeCode) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*c = MessageTypeCodeUnknown
		return nil
	}
	if unquoted, err := strconv.Unquote(s); err == nil {
		*c = messageTypeCodeFromName(unquoted)
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return errors.New("chatwoot: invalid message_type")
	}
	*c = messageTypeCodeFromWire(n)
	return nil
}

// messageTypeCodeFromName maps the webhook string form to an internal code.
func messageTypeCodeFromName(name string) MessageTypeCode {
	switch name {
	case "incoming":
		return MessageTypeCodeIncoming
	case "outgoing":
		return MessageTypeCodeOutgoing
	case "activity":
		return MessageTypeCodeActivity
	case "template":
		return MessageTypeCodeTemplate
	default:
		return MessageTypeCodeUnknown
	}
}

// messageTypeCodeFromWire maps the REST integer enum 0..3 to an internal code.
func messageTypeCodeFromWire(n int) MessageTypeCode {
	switch n {
	case 0:
		return MessageTypeCodeIncoming
	case 1:
		return MessageTypeCodeOutgoing
	case 2:
		return MessageTypeCodeActivity
	case 3:
		return MessageTypeCodeTemplate
	default:
		return MessageTypeCodeUnknown
	}
}

// Message is the subset of a Chatwoot message the bridge needs. ID is cached to
// suppress the Chatwoot -> WhatsApp echo loop (messages.md §2.3).
type Message struct {
	ID             int             `json:"id"`
	Content        string          `json:"content"`
	ConversationID int             `json:"conversation_id"`
	MessageType    MessageTypeCode `json:"message_type"`
	Status         string          `json:"status"`
	Private        bool            `json:"private"`
}

// Attachment is an in-memory file to upload.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// SendText posts a JSON text message (messages.md §2).
func (c *Client) SendText(ctx context.Context, conversationID int, content string, messageType MessageType) (*Message, error) {
	if conversationID <= 0 {
		return nil, errors.New("chatwoot: conversation id must be positive")
	}
	if !messageType.valid() {
		return nil, errors.New("chatwoot: invalid message type")
	}

	body := struct {
		Content     string `json:"content"`
		MessageType string `json:"message_type"`
		Private     bool   `json:"private"`
	}{
		Content:     content,
		MessageType: string(messageType),
		Private:     false,
	}

	var msg Message
	path := c.accountURL("conversations/" + strconv.Itoa(conversationID) + "/messages")
	if err := c.doJSON(ctx, http.MethodPost, path, body, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// SendAttachment posts a multipart message with one file (messages.md §3).
func (c *Client) SendAttachment(ctx context.Context, conversationID int, file Attachment, caption string, messageType MessageType) (*Message, error) {
	if conversationID <= 0 {
		return nil, errors.New("chatwoot: conversation id must be positive")
	}
	if !messageType.valid() {
		return nil, errors.New("chatwoot: invalid message type")
	}
	if len(file.Data) == 0 {
		return nil, errors.New("chatwoot: attachment data is empty")
	}
	filename := sanitizeFilename(file.Filename)
	if filename == "" {
		return nil, errors.New("chatwoot: attachment filename is required")
	}
	contentType := file.ContentType
	if strings.ContainsAny(contentType, "\r\n") {
		return nil, errors.New("chatwoot: attachment content type contains control characters")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachments[]"; filename="%s"`, escapeQuotes(filename)))
	header.Set("Content-Type", contentType)
	part, err := mw.CreatePart(header)
	if err != nil {
		return nil, fmt.Errorf("chatwoot: building multipart attachment: %w", err)
	}
	if _, err := part.Write(file.Data); err != nil {
		return nil, fmt.Errorf("chatwoot: writing multipart attachment: %w", err)
	}
	if err := mw.WriteField("message_type", string(messageType)); err != nil {
		return nil, fmt.Errorf("chatwoot: writing message_type: %w", err)
	}
	if caption != "" {
		if err := mw.WriteField("content", caption); err != nil {
			return nil, fmt.Errorf("chatwoot: writing caption: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("chatwoot: closing multipart body: %w", err)
	}

	path := c.accountURL("conversations/" + strconv.Itoa(conversationID) + "/messages")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return nil, errors.New("chatwoot: building request failed")
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	var msg Message
	if err := c.execute(req, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// sanitizeFilename removes control characters (CR, LF, NUL, DEL, ...) from a
// wire-controlled attachment filename. A CRLF in the Content-Disposition value
// would otherwise inject a multipart header or an extra part (RFC 7578,
// finding M1).
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
}

// escapeQuotes escapes a multipart filename per RFC 7578.
func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, `\"`).Replace(s)
}
