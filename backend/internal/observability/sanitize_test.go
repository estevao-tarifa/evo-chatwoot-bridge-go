package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSlice4_I14_SanitizerExcludesSecrets locks down the allowlist choke point:
// the stored envelope never carries media bytes/base64, message text, captions,
// tokens/cookies, credential URLs or any unlisted field (ADR-0006 §9, B3).
func TestSlice4_I14_SanitizerExcludesSecrets(t *testing.T) {
	payload := []byte(`{
		"event": "Message",
		"apikey": "SECRET-APIKEY",
		"cookie": "SECRET-COOKIE",
		"data": {
			"Info": {
				"ID": "SYNTH-1",
				"Chat": "5511900000001@s.whatsapp.net",
				"Sender": "5511900000001@s.whatsapp.net",
				"PushName": "Synthetic",
				"IsFromMe": false,
				"IsGroup": false,
				"MediaType": "image",
				"Type": "image",
				"Timestamp": "1700000000",
				"MediaUrl": "https://alice:hunter2@evil.example/SECRET-URL"
			},
			"Key": {"Participant": "5511900000002@s.whatsapp.net"},
			"Message": {
				"imageMessage": {
					"caption": "SECRET-CAPTION",
					"fileLength": 12345,
					"base64": "SECRET-BASE64",
					"url": "https://alice:hunter2@evil.example/SECRET-URL"
				}
			}
		}
	}`)

	stored := SanitizeWebhookPayload(payload)
	text := string(stored)

	for _, secret := range []string{
		"SECRET-APIKEY", "SECRET-COOKIE", "SECRET-CAPTION", "SECRET-BASE64",
		"SECRET-URL", "hunter2", "alice",
	} {
		if strings.Contains(text, secret) {
			t.Errorf("stored envelope leaked %q: %s", secret, text)
		}
	}

	var envelope sanitizeEnvelope
	if err := json.Unmarshal(stored, &envelope); err != nil {
		t.Fatalf("stored envelope is not valid JSON: %v (%s)", err, text)
	}
	if envelope.Schema != sanitizeSchemaVersion {
		t.Errorf("schema = %d, want %d", envelope.Schema, sanitizeSchemaVersion)
	}
	if envelope.Event != "Message" {
		t.Errorf("event = %q, want Message", envelope.Event)
	}
	if envelope.Info.ID != "SYNTH-1" {
		t.Errorf("info.id = %q, want SYNTH-1", envelope.Info.ID)
	}
	if envelope.Participant != "5511900000002@s.whatsapp.net" {
		t.Errorf("participant = %q", envelope.Participant)
	}
	want := sanitizeMessage{Kind: "image", CaptionLen: 14, MediaPresent: true, MediaBytes: 12345}
	if envelope.Message != want {
		t.Errorf("message = %+v, want %+v", envelope.Message, want)
	}
}

// TestSanitizeChatwootPayloadExcludesSecrets locks down the Chatwoot audit
// choke point (spec §1.7): the stored envelope never carries the content text,
// an attachment data_url/url, the sender email, tokens or the raw payload.
func TestSanitizeChatwootPayloadExcludesSecrets(t *testing.T) {
	payload := []byte(`{
		"event": "message_created",
		"id": 10001,
		"content": "SECRET-CONTENT",
		"message_type": "outgoing",
		"private": false,
		"token": "SECRET-TOKEN",
		"conversation": {"id": 5001, "inbox_id": 10},
		"inbox": {"id": 10, "name": "SECRET-INBOX"},
		"sender": {"id": 7, "name": "Agent", "email": "SECRET-EMAIL@example.invalid", "type": "user"},
		"attachments": [
			{"id": 9001, "file_type": "image", "content_type": "image/png",
			 "data_url": "https://chatwoot.invalid/SECRET-DATA-URL", "url": "https://chatwoot.invalid/SECRET-URL"}
		]
	}`)

	stored := SanitizeChatwootPayload(payload)
	text := string(stored)
	for _, secret := range []string{
		"SECRET-CONTENT", "SECRET-TOKEN", "SECRET-EMAIL", "SECRET-DATA-URL",
		"SECRET-URL", "SECRET-INBOX", "data_url", "content_type",
	} {
		if strings.Contains(text, secret) {
			t.Errorf("stored envelope leaked %q: %s", secret, text)
		}
	}

	var envelope chatwootEnvelope
	if err := json.Unmarshal(stored, &envelope); err != nil {
		t.Fatalf("stored envelope is not valid JSON: %v (%s)", err, text)
	}
	if envelope.Schema != sanitizeSchemaVersion || envelope.System != "chatwoot" {
		t.Errorf("schema/system = %d/%q", envelope.Schema, envelope.System)
	}
	if envelope.ID != "10001" || envelope.MessageType != "outgoing" || envelope.SenderType != "user" {
		t.Errorf("id/type/sender_type = %q/%q/%q", envelope.ID, envelope.MessageType, envelope.SenderType)
	}
	if envelope.ConversationID != 5001 || envelope.InboxID != 10 {
		t.Errorf("conversation/inbox = %d/%d", envelope.ConversationID, envelope.InboxID)
	}
	if envelope.ContentLen != len("SECRET-CONTENT") {
		t.Errorf("content_len = %d, want %d (length only)", envelope.ContentLen, len("SECRET-CONTENT"))
	}
	if envelope.AttachmentCount != 1 || len(envelope.AttachmentTypes) != 1 || envelope.AttachmentTypes[0] != "image" {
		t.Errorf("attachments = %d/%v", envelope.AttachmentCount, envelope.AttachmentTypes)
	}
}

// TestSanitizeChatwootPayloadBounds proves the lossy markers: undecodable JSON
// becomes the unparsed marker and an oversized projection the truncated one.
func TestSanitizeChatwootPayloadBounds(t *testing.T) {
	if got := string(SanitizeChatwootPayload([]byte("not json"))); got != `{"schema":1,"unparsed":true}` {
		t.Errorf("undecodable JSON = %s, want unparsed marker", got)
	}
	oversized := []byte(`{"event":"message_created","message_type":"` + strings.Repeat("a", maxSanitizedPayloadBytes+1) + `"}`)
	if got := string(SanitizeChatwootPayload(oversized)); got != `{"schema":1,"truncated":true}` {
		t.Errorf("oversized projection = %s, want truncated marker", got)
	}
}

// TestSanitizeChatwootPayloadControlCharsStripped proves an audit value cannot
// inject a newline into a log line.
func TestSanitizeChatwootPayloadControlCharsStripped(t *testing.T) {
	payload := []byte(`{"event":"message_created","id":"S","message_type":"outgoing\ninjected"}`)
	var envelope chatwootEnvelope
	if err := json.Unmarshal(SanitizeChatwootPayload(payload), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.MessageType != "outgoinginjected" {
		t.Errorf("message_type = %q, want control characters stripped", envelope.MessageType)
	}
}

// TestSanitizeChatwootPayloadEmptyObject proves an empty object still yields a
// valid, empty projection rather than the unparsed marker.
func TestSanitizeChatwootPayloadEmptyObject(t *testing.T) {
	var envelope chatwootEnvelope
	if err := json.Unmarshal(SanitizeChatwootPayload([]byte(`{}`)), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.System != "chatwoot" || envelope.Schema != sanitizeSchemaVersion {
		t.Errorf("empty projection = %+v", envelope)
	}
	if envelope.AttachmentTypes == nil {
		t.Error("attachment_types = nil, want [] for a deterministic envelope")
	}
}

// TestSanitizeWebhookPayloadProjectsText checks that text is represented by its
// length only, never its content.
func TestSanitizeWebhookPayloadProjectsText(t *testing.T) {
	payload := []byte(`{"event":"Message","data":{"Info":{"ID":"S","Chat":"5511900000001@s.whatsapp.net"},"Message":{"conversation":"SECRET-TEXT-12345"}}}`)

	var envelope sanitizeEnvelope
	if err := json.Unmarshal(SanitizeWebhookPayload(payload), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Contains(string(SanitizeWebhookPayload(payload)), "SECRET-TEXT") {
		t.Error("stored envelope leaked message text")
	}
	if envelope.Message.Kind != "text" || envelope.Message.TextLen != len("SECRET-TEXT-12345") {
		t.Errorf("message = %+v, want kind=text text_len=%d", envelope.Message, len("SECRET-TEXT-12345"))
	}
}

// TestSanitizeWebhookPayloadGroup verifies the group identity is the JID and the
// name is the authoritative groupData.Name.
func TestSanitizeWebhookPayloadGroup(t *testing.T) {
	payload := []byte(`{"event":"Message","data":{"Info":{"ID":"S","Chat":"120363000000000000@g.us","IsGroup":true},"groupData":{"Name":"Synthetic Group"}}}`)

	var envelope sanitizeEnvelope
	if err := json.Unmarshal(SanitizeWebhookPayload(payload), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Group.JID != "120363000000000000@g.us" {
		t.Errorf("group.jid = %q", envelope.Group.JID)
	}
	if envelope.Group.Name != "Synthetic Group" {
		t.Errorf("group.name = %q", envelope.Group.Name)
	}
	if !envelope.Info.IsGroup {
		t.Error("info.is_group = false, want true")
	}
}

// TestSanitizeWebhookPayloadControlCharsStripped proves a value cannot inject a
// newline into the audit log line.
func TestSanitizeWebhookPayloadControlCharsStripped(t *testing.T) {
	payload := []byte(`{"event":"Message","data":{"Info":{"ID":"S","Chat":"5511900000001@s.whatsapp.net","PushName":"line1\nline2\r\t"}}}`)

	var envelope sanitizeEnvelope
	if err := json.Unmarshal(SanitizeWebhookPayload(payload), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Info.PushName != "line1line2" {
		t.Errorf("push_name = %q, want control characters stripped", envelope.Info.PushName)
	}
}

// TestSanitizeWebhookPayloadDualCasing proves the camelCase (v2) shape decodes
// through the same allowlisted fields.
func TestSanitizeWebhookPayloadDualCasing(t *testing.T) {
	payload := []byte(`{"event":"messages.upsert","data":{"key":{"remoteJid":"5511900000001@s.whatsapp.net","participant":"5511900000002@s.whatsapp.net","fromMe":false,"id":"CAMEL-1"},"pushName":"Camel","message":{"extendedTextMessage":{"text":"hello"}}}}`)

	var envelope sanitizeEnvelope
	if err := json.Unmarshal(SanitizeWebhookPayload(payload), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Info.ID != "CAMEL-1" || envelope.Info.Chat != "5511900000001@s.whatsapp.net" {
		t.Errorf("camelCase info = %+v", envelope.Info)
	}
	if envelope.Message.Kind != "text" || envelope.Message.TextLen != 5 {
		t.Errorf("message = %+v, want text/5", envelope.Message)
	}
}

// TestSanitizeWebhookPayloadBounds proves the two lossy markers: an oversized
// projection becomes the truncated marker and undecodable JSON the unparsed one.
func TestSanitizeWebhookPayloadBounds(t *testing.T) {
	oversized := []byte(`{"event":"Message","data":{"Info":{"ID":"S","Chat":"` + strings.Repeat("a", maxSanitizedPayloadBytes+1) + `"}}}`)
	if got := string(SanitizeWebhookPayload(oversized)); got != `{"schema":1,"truncated":true}` {
		t.Errorf("oversized projection = %s, want truncated marker", got)
	}

	if got := string(SanitizeWebhookPayload([]byte("not json"))); got != `{"schema":1,"unparsed":true}` {
		t.Errorf("undecodable JSON = %s, want unparsed marker", got)
	}

	if got := string(SanitizeWebhookPayload([]byte(`{"event":"Message"}`))); got != `{"schema":1,"unparsed":true}` {
		t.Errorf("missing data = %s, want unparsed marker", got)
	}
}
