package chatwoot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestSendText(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":55,"content":"hi","message_type":1,"conversation_id":10,"status":"sent"}`)

	msg, err := c.SendText(context.Background(), 10, "hi", MessageTypeIncoming)
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
	if msg == nil || msg.ID != 55 {
		t.Fatalf("msg = %+v, want id 55", msg)
	}
	if got := msg.MessageType.String(); got != "outgoing" {
		t.Errorf("message_type = %q, want outgoing", got)
	}
	if rec.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", rec.Method)
	}
	if want := "/api/v1/accounts/42/conversations/10/messages"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
	if got := rec.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q, want application/json", got)
	}
	assertJSONBody(t, rec.Body, `{"content":"hi","message_type":"incoming","private":false}`)
	assertAuthHeader(t, rec.Header)
}

func TestSendText_Validation(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{}`)

	if _, err := c.SendText(context.Background(), 0, "hi", MessageTypeIncoming); err == nil {
		t.Error("invalid conversation id must be rejected")
	}
	if _, err := c.SendText(context.Background(), 10, "hi", MessageType("bogus")); err == nil {
		t.Error("invalid message type must be rejected")
	}
	if rec.Method != "" {
		t.Error("no request should be sent for invalid input")
	}
}

func TestMessageTypeCode_Unmarshal(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "integer enum", raw: `{"id":1,"message_type":2}`, want: "activity"},
		{name: "string form", raw: `{"id":1,"message_type":"outgoing"}`, want: "outgoing"},
		{name: "null", raw: `{"id":1,"message_type":null}`, want: "unknown"},
		{name: "absent", raw: `{"id":1}`, want: "unknown"},
		{name: "unknown string", raw: `{"id":1,"message_type":"weird"}`, want: "unknown"},
		{name: "out-of-range integer", raw: `{"id":1,"message_type":9}`, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg Message
			if err := json.Unmarshal([]byte(tt.raw), &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := msg.MessageType.String(); got != tt.want {
				t.Errorf("MessageType = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSendAttachment(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":77,"message_type":"incoming","conversation_id":10}`)

	file := Attachment{Filename: "photo.png", ContentType: "image/png", Data: []byte("PNGDATA")}
	msg, err := c.SendAttachment(context.Background(), 10, file, "hi", MessageTypeIncoming)
	if err != nil {
		t.Fatalf("SendAttachment() error = %v", err)
	}
	if msg == nil || msg.ID != 77 {
		t.Fatalf("msg = %+v, want id 77", msg)
	}
	if rec.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", rec.Method)
	}
	if want := "/api/v1/accounts/42/conversations/10/messages"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}

	mediaType, params, err := mime.ParseMediaType(rec.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content-type = %q (err %v), want multipart/form-data", rec.Header.Get("Content-Type"), err)
	}

	fields, filename, contentType, data := parseMultipart(t, rec.Body, params["boundary"])
	if fields["message_type"] != "incoming" {
		t.Errorf("message_type field = %q, want incoming", fields["message_type"])
	}
	if fields["content"] != "hi" {
		t.Errorf("content field = %q, want hi", fields["content"])
	}
	if _, ok := fields["private"]; ok {
		t.Error("private must not be sent on multipart (defaults false)")
	}
	if filename != "photo.png" {
		t.Errorf("filename = %q, want photo.png", filename)
	}
	if contentType != "image/png" {
		t.Errorf("attachment content-type = %q, want image/png", contentType)
	}
	if string(data) != "PNGDATA" {
		t.Errorf("attachment data = %q, want PNGDATA", data)
	}
}

// A wire-controlled filename carrying CRLF must not inject a multipart header
// or an extra part: control characters are stripped before the header is built
// (finding M1).
func TestSendAttachment_FilenameControlCharsStripped(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":1}`)

	file := Attachment{
		Filename:    "evil\r\nX-Injected: 1\r\n\r\npwned",
		ContentType: "text/plain",
		Data:        []byte("x"),
	}
	if _, err := c.SendAttachment(context.Background(), 10, file, "", MessageTypeIncoming); err != nil {
		t.Fatalf("SendAttachment() error = %v", err)
	}

	if bytes.Contains(rec.Body, []byte("\r\nX-Injected")) {
		t.Errorf("filename control characters injected a header/part:\n%s", rec.Body)
	}

	_, params, err := mime.ParseMediaType(rec.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse media type: %v", err)
	}

	parts := 0
	var filename string
	mr := multipart.NewReader(bytes.NewReader(rec.Body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading multipart: %v", err)
		}
		parts++
		if part.FormName() == "attachments[]" {
			filename = part.FileName()
		}
	}
	if parts != 2 { // attachment + message_type
		t.Errorf("multipart parts = %d, want 2 (no injected part)", parts)
	}
	if strings.ContainsAny(filename, "\r\n\x00") {
		t.Errorf("filename still contains control characters: %q", filename)
	}
}

func TestSendAttachment_OmitsEmptyCaption(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":77}`)

	file := Attachment{Filename: "f.bin", Data: []byte("x")}
	if _, err := c.SendAttachment(context.Background(), 10, file, "", MessageTypeOutgoing); err != nil {
		t.Fatalf("SendAttachment() error = %v", err)
	}

	_, params, err := mime.ParseMediaType(rec.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse media type: %v", err)
	}
	fields, _, contentType, _ := parseMultipart(t, rec.Body, params["boundary"])
	if _, ok := fields["content"]; ok {
		t.Error("content must be omitted when the caption is empty")
	}
	if contentType != "application/octet-stream" {
		t.Errorf("default content-type = %q, want application/octet-stream", contentType)
	}
}

func TestSendAttachment_Validation(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{}`)

	if _, err := c.SendAttachment(context.Background(), 10, Attachment{Filename: "f.bin"}, "cap", MessageTypeIncoming); err == nil {
		t.Error("empty attachment data must be rejected")
	}
	if _, err := c.SendAttachment(context.Background(), 10, Attachment{Data: []byte("x")}, "cap", MessageTypeIncoming); err == nil {
		t.Error("empty filename must be rejected")
	}
	if _, err := c.SendAttachment(context.Background(), 10, Attachment{Filename: "\r\n", Data: []byte("x")}, "cap", MessageTypeIncoming); err == nil {
		t.Error("control-only filename must be rejected")
	}
	if rec.Method != "" {
		t.Error("no request should be sent for invalid input")
	}
}

func TestSendAttachment_RejectsContentTypeHeaderInjection(t *testing.T) {
	for _, tt := range []struct{ name, contentType string }{
		{name: "crlf", contentType: "text/plain\r\nX-Injected: 1"},
		{name: "cr", contentType: "text/plain\rX-Injected: 1"},
		{name: "lf", contentType: "text/plain\nX-Injected: 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newRecordingClient(t, http.StatusOK, `{"id":1}`)
			file := Attachment{Filename: "f.txt", ContentType: tt.contentType, Data: []byte("x")}
			if _, err := c.SendAttachment(context.Background(), 10, file, "", MessageTypeIncoming); err == nil {
				t.Fatal("header injection must be rejected")
			}
			if rec.Method != "" {
				t.Fatal("invalid content type must not reach the wire")
			}
		})
	}
}

// parseMultipart returns the non-file form fields and the first file part.
func parseMultipart(t *testing.T, body []byte, boundary string) (fields map[string]string, filename, contentType string, data []byte) {
	t.Helper()
	fields = map[string]string{}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading multipart: %v", err)
		}
		value, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("reading part: %v", err)
		}
		if part.FormName() == "attachments[]" {
			filename = part.FileName()
			contentType = part.Header.Get("Content-Type")
			data = value
			continue
		}
		fields[part.FormName()] = string(value)
	}
	return fields, filename, contentType, data
}
