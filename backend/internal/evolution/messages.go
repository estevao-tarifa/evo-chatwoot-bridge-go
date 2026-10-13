package evolution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// Configurable endpoint paths. The public Evolution GO builds use
// /message/markread and /message/downloadmedia; the legacy /chat/* paths exist
// in no public release (OPEN_CONFLICTS C4, send-api.md §6). [needs live validation]
const (
	defaultMarkReadPath      = "/message/markread"
	defaultDownloadMediaPath = "/message/downloadmedia"
)

// SendOptions carries optional per-send parameters. ID is the caller-provided
// message id: the primary loop-prevention primitive (ROUTING_MATRIX §3.1). It
// is for echo correlation only and never authorizes an automatic replay
// (ADR-0008 §5).
type SendOptions struct {
	ID string
}

// SendResult is confirmed only when the response contains a message id.
type SendResult struct {
	MessageID string
}

// sendTextRequest is the POST /send/text body (send-api.md §2).
type sendTextRequest struct {
	Number string `json:"number"`
	Text   string `json:"text"`
	ID     string `json:"id,omitempty"`
}

// sendMediaRequest is the POST /send/media body (send-api.md §3). Caption is
// always sent (legacy sends ""); Filename is omitted when empty.
type sendMediaRequest struct {
	Number   string `json:"number"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	Caption  string `json:"caption"`
	Filename string `json:"filename,omitempty"`
	ID       string `json:"id,omitempty"`
}

// SendText posts to /send/text with the instance key and returns the sent id.
func (c *Client) SendText(ctx context.Context, number, text string, opts SendOptions) (SendResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := sendTextRequest{
		Number: number,
		Text:   text,
		ID:     opts.ID,
	}
	raw, err := c.doJSON(ctx, http.MethodPost, "/send/text", scopeInstance, req)
	return sendOutcome("sending text", raw, err)
}

// SendMedia posts to /send/media with the instance key and returns the sent id.
func (c *Client) SendMedia(
	ctx context.Context,
	number, mediaType, url, caption, filename string,
	opts SendOptions,
) (SendResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := sendMediaRequest{
		Number:   number,
		Type:     mediaType,
		URL:      url,
		Caption:  caption,
		Filename: filename,
		ID:       opts.ID,
	}
	raw, err := c.doJSON(ctx, http.MethodPost, "/send/media", scopeInstance, req)
	return sendOutcome("sending media", raw, err)
}

// unconfirmedError marks a send whose outcome is unknown. Its message is fixed
// and exposes no upstream content; the wrapped cause stays reachable only
// through errors.Is/errors.As for safe sentinels and status codes.
type unconfirmedError struct{ cause error }

func (e *unconfirmedError) Error() string { return "evolution: send outcome unconfirmed" }

func (e *unconfirmedError) Unwrap() error { return e.cause }

func (e *unconfirmedError) Is(target error) bool { return target == ErrUnconfirmedSend }

// sendOutcome classifies a send. A request rejected locally before I/O is known
// not sent. Any other failure after an attempt reached the transport, a 2xx
// without an id, and a malformed 2xx are all reported unconfirmed so the caller
// can never treat them as sent or replay them automatically (ADR-0008 §5).
func sendOutcome(operation string, raw []byte, callErr error) (SendResult, error) {
	if callErr != nil {
		if isPreflightRejection(callErr) {
			return SendResult{}, fmt.Errorf("evolution: %s: %w", operation, callErr)
		}
		return SendResult{}, fmt.Errorf("evolution: %s: %w", operation, &unconfirmedError{cause: callErr})
	}
	id, err := parseMessageID(raw)
	if err != nil {
		return SendResult{}, fmt.Errorf("evolution: %s: %w", operation, &unconfirmedError{cause: err})
	}
	return SendResult{MessageID: id}, nil
}

// isPreflightRejection reports a request rejected locally before any I/O, whose
// outcome is therefore known not sent.
func isPreflightRejection(err error) bool {
	return errors.Is(err, ErrRequestTooLarge) || errors.Is(err, ErrInvalidRequest)
}

// setWebhookRequest is the POST /instance/connect body. Subscribe is
// caller-provided uppercase constants; the legacy PascalCase list is never
// hardcoded here (OPEN_CONFLICTS C4).
type setWebhookRequest struct {
	WebhookURL string   `json:"webhookUrl"`
	Subscribe  []string `json:"subscribe"`
}

// SetWebhook configures the instance webhook via POST /instance/connect with
// the instance key.
func (c *Client) SetWebhook(ctx context.Context, webhookURL string, subscribe []string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := setWebhookRequest{
		WebhookURL: webhookURL,
		Subscribe:  subscribe,
	}
	if _, err := c.doJSON(ctx, http.MethodPost, "/instance/connect", scopeInstance, req); err != nil {
		return fmt.Errorf("evolution: setting webhook: %w", err)
	}
	return nil
}

// markReadRequest is the documented Evolution GO body:
// `{ "id": ["<message-id>"], "number": "<jid-or-number>" }`.
type markReadRequest struct {
	ID     []string `json:"id"`
	Number string   `json:"number"`
}

// MarkRead uses only the documented GO path and body. An unverified custom path
// is refused without I/O. A 404 makes this optional call unavailable, but does
// not prove that the endpoint is absent; the configured path is never echoed.
func (c *Client) MarkRead(ctx context.Context, chatJID, messageID string) error {
	if c.markReadPath != defaultMarkReadPath {
		return ErrNotSupported
	}
	if chatJID == "" || messageID == "" {
		return ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := markReadRequest{
		ID:     []string{messageID},
		Number: chatJID,
	}
	if _, err := c.doJSON(ctx, http.MethodPost, c.markReadPath, scopeInstance, req); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("evolution: mark read returned 404: %w", ErrNotSupported)
		}
		return fmt.Errorf("evolution: marking read: %w", err)
	}
	return nil
}

// DownloadOptions identifies a media message to download. It maps the legacy
// getBase64FromMediaMessage(messageId, convertToMp4) parameters.
type DownloadOptions struct {
	MessageID    string
	ConvertToMp4 bool
}

// MediaDownload is a downloaded media message.
type MediaDownload struct {
	Base64   string
	MIMEType string
}

// DownloadMedia is disabled for every configuration: a message id cannot
// construct Evolution GO's full media-message request, and a configured path
// does not make the body valid. It performs zero network I/O and returns a
// fixed unsupported category. Prefer media already delivered in a webhook.
// Re-enabling it requires a source-proven full-message input contract, the
// legacy MIME alias precedence and a finite media-response budget (ADR-0008 §3).
func (c *Client) DownloadMedia(_ context.Context, _ DownloadOptions) (MediaDownload, error) {
	return MediaDownload{}, ErrNotSupported
}

// parseMessageID decodes a send response and returns its id, or a fixed
// malformed/unconfirmed category. A truthy non-object Info fails closed instead
// of falling through to a conflicting lowercase alias (L1).
func parseMessageID(raw []byte) (string, error) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "", ErrMalformedResponse
	}
	root, ok := value.(map[string]any)
	if !ok {
		return "", ErrMalformedResponse
	}
	node := root
	if data, exists := root["data"]; exists && data != nil {
		m, ok := data.(map[string]any)
		if !ok {
			return "", ErrMalformedResponse
		}
		node = m
	}
	if info, exists := node["Info"]; exists && info != nil {
		if _, ok := info.(map[string]any); !ok {
			return "", ErrMalformedResponse
		}
	}
	id := extractMessageID(raw)
	if id == "" {
		return "", ErrUnconfirmedSend
	}
	return id, nil
}

// extractMessageID reproduces the legacy extractSentMessageId parser
// (response-formats.md §1): take data when present, else the whole body, then
// the first truthy of info.ID, info.id (info = Info || info), key.id, data.id.
// Anything else is "". It is a pure parser; parseMessageID adds the fail-closed
// guard for malformed truthy Info.
func extractMessageID(raw []byte) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return ""
	}
	node := root
	if data, ok := root["data"]; ok && !isJSONNull(data) {
		var m map[string]json.RawMessage
		if json.Unmarshal(data, &m) == nil {
			node = m
		}
	}
	info := decodeObject(node["Info"])
	if info == nil && (len(node["Info"]) == 0 || isJSONNull(node["Info"])) {
		info = decodeObject(node["info"])
	}
	if info != nil {
		if id := decodeID(info["ID"]); id != "" {
			return id
		}
		if id := decodeID(info["id"]); id != "" {
			return id
		}
	}
	if key := decodeObject(node["key"]); key != nil {
		if id := decodeID(key["id"]); id != "" {
			return id
		}
	}
	return decodeID(node["id"])
}

// decodeID stringifies a JSON value, treating null and numeric zero as absent
// (the legacy `||` chain skips falsy values).
func decodeID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case float64:
		if v == 0 {
			return ""
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

// decodeObject decodes raw into an object, or returns nil when it is not one.
func decodeObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// isJSONNull reports whether raw is the JSON null literal.
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}
