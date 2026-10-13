package evolution

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSendText(t *testing.T) {
	t.Run("request shape and Info.ID extraction", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assertRequest(t, r, http.MethodPost, "/send/text", testToken)
			body := decodeRequestBody(t, r)
			if body["number"] != "5511900000001" {
				t.Errorf("number = %v, want 5511900000001", body["number"])
			}
			if body["text"] != "hello" {
				t.Errorf("text = %v, want hello", body["text"])
			}
			if body["id"] != "bridge-id-1" {
				t.Errorf("id = %v, want bridge-id-1", body["id"])
			}
			writeJSON(w, `{"message":"success","data":{"Info":{"ID":"3EB0GENERATED0000000001"}}}`)
		})

		result, err := c.SendText(t.Context(), "5511900000001", "hello", SendOptions{ID: "bridge-id-1"})
		if err != nil {
			t.Fatalf("SendText() error = %v", err)
		}
		if result.MessageID != "3EB0GENERATED0000000001" {
			t.Errorf("MessageID = %q", result.MessageID)
		}
	})

	t.Run("id omitted when caller provides none", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			body := decodeRequestBody(t, r)
			if _, ok := body["id"]; ok {
				t.Errorf("id present, want omitted: %v", body["id"])
			}
			writeJSON(w, `{"data":{"Info":{"ID":"x"}}}`)
		})
		if _, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{}); err != nil {
			t.Fatalf("SendText() error = %v", err)
		}
	})
}

func TestSendMedia(t *testing.T) {
	t.Run("request shape and key.id fallback", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assertRequest(t, r, http.MethodPost, "/send/media", testToken)
			body := decodeRequestBody(t, r)
			want := map[string]any{
				"number":   "5511900000001",
				"type":     "image",
				"url":      "https://media.example.com/a.jpg",
				"caption":  "a caption",
				"filename": "a.jpg",
				"id":       "bridge-id-2",
			}
			for key, wantValue := range want {
				if body[key] != wantValue {
					t.Errorf("%s = %v, want %v", key, body[key], wantValue)
				}
			}
			writeJSON(w, `{"data":{"key":{"id":"3EB0KEY0000000002"}}}`)
		})

		result, err := c.SendMedia(
			t.Context(),
			"5511900000001", "image", "https://media.example.com/a.jpg", "a caption", "a.jpg",
			SendOptions{ID: "bridge-id-2"},
		)
		if err != nil {
			t.Fatalf("SendMedia() error = %v", err)
		}
		if result.MessageID != "3EB0KEY0000000002" {
			t.Errorf("MessageID = %q", result.MessageID)
		}
	})

	t.Run("caption always present, filename omitted when empty", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			body := decodeRequestBody(t, r)
			if caption, ok := body["caption"]; !ok || caption != "" {
				t.Errorf("caption = %v (present=%v), want empty string", caption, ok)
			}
			if _, ok := body["filename"]; ok {
				t.Errorf("filename present, want omitted")
			}
			writeJSON(w, `{"data":{"Info":{"ID":"x"}}}`)
		})
		if _, err := c.SendMedia(t.Context(), "5511900000001", "document", "https://x", "", "", SendOptions{}); err != nil {
			t.Fatalf("SendMedia() error = %v", err)
		}
	})

	t.Run("non-2xx returns typed error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"invalid media type"}`))
		})
		_, err := c.SendMedia(t.Context(), "5511900000001", "bogus", "https://x", "", "", SendOptions{})
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusInternalServerError {
			t.Fatalf("SendMedia() error = %v, want HTTPError 500", err)
		}
		if !errors.Is(err, ErrUnconfirmedSend) {
			t.Errorf("SendMedia() error = %v, want ErrUnconfirmedSend", err)
		}
	})

	t.Run("body carries exactly the contracted fields", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			body := decodeRequestBody(t, r)
			want := map[string]struct{}{
				"number": {}, "type": {}, "url": {}, "caption": {}, "filename": {}, "id": {},
			}
			if len(body) != len(want) {
				t.Errorf("body has %d keys, want %d: %v", len(body), len(want), body)
			}
			for key := range body {
				if _, ok := want[key]; !ok {
					t.Errorf("unexpected field %q in body", key)
				}
			}
			writeJSON(w, `{"data":{"Info":{"ID":"x"}}}`)
		})
		if _, err := c.SendMedia(
			t.Context(),
			"5511900000001", "document", "https://x/doc.pdf", "cap", "doc.pdf",
			SendOptions{ID: "id-1"},
		); err != nil {
			t.Fatalf("SendMedia() error = %v", err)
		}
	})
}

func TestExtractMessageID(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "data Info ID", body: `{"message":"success","data":{"Info":{"ID":"A"}}}`, want: "A"},
		{name: "data Info lower id", body: `{"data":{"Info":{"id":"B"}}}`, want: "B"},
		{name: "data info uppercase ID", body: `{"data":{"info":{"ID":"F"}}}`, want: "F"},
		{name: "data info lower id", body: `{"data":{"info":{"id":"G"}}}`, want: "G"},
		{name: "Info takes precedence over info", body: `{"data":{"Info":{},"info":{"ID":"wrong"},"key":{"id":"right"}}}`, want: "right"},
		{name: "null Info uses info", body: `{"data":{"Info":null,"info":{"ID":"right"}}}`, want: "right"},
		{name: "malformed truthy Info falls to key id", body: `{"data":{"Info":"truthy malformed","info":{"ID":"wrong"},"key":{"id":"right"}}}`, want: "right"},
		{name: "flat Info ID", body: `{"Info":{"ID":"C"}}`, want: "C"},
		{name: "data key id", body: `{"data":{"key":{"id":"D"}}}`, want: "D"},
		{name: "flat key id", body: `{"key":{"id":"E"}}`, want: "E"},
		{name: "data id", body: `{"data":{"id":"11632"}}`, want: "11632"},
		{name: "root numeric id", body: `{"id":123}`, want: "123"},
		{name: "empty Info ID", body: `{"data":{"Info":{"ID":""}}}`, want: ""},
		{name: "zero data id", body: `{"data":{"id":0}}`, want: ""},
		{name: "key ID uppercase ignored", body: `{"data":{"key":{"ID":"x"}}}`, want: ""},
		{name: "MessageID not read", body: `{"message":"success","data":{"MessageID":"x"}}`, want: ""},
		{name: "null body", body: `null`, want: ""},
		{name: "array body", body: `[]`, want: ""},
		{name: "string body", body: `"ok"`, want: ""},
		{name: "null data", body: `{"data":null}`, want: ""},
		{name: "empty body", body: ``, want: ""},
		{name: "malformed", body: `not json`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractMessageID([]byte(tt.body)); got != tt.want {
				t.Errorf("extractMessageID(%s) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestSetWebhook(t *testing.T) {
	subscribe := []string{"MESSAGE", "SEND_MESSAGE", "CONNECTION"}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertRequest(t, r, http.MethodPost, "/instance/connect", testToken)
		body := decodeRequestBody(t, r)
		if body["webhookUrl"] != "https://bridge.example.com/api/webhooks/evolution/1" {
			t.Errorf("webhookUrl = %v", body["webhookUrl"])
		}
		got, ok := body["subscribe"].([]any)
		if !ok || len(got) != len(subscribe) {
			t.Fatalf("subscribe = %v, want %v", body["subscribe"], subscribe)
		}
		for i, value := range got {
			if value != subscribe[i] {
				t.Errorf("subscribe[%d] = %v, want %q", i, value, subscribe[i])
			}
		}
		writeJSON(w, `{"message":"success"}`)
	})

	if err := c.SetWebhook(t.Context(), "https://bridge.example.com/api/webhooks/evolution/1", subscribe); err != nil {
		t.Fatalf("SetWebhook() error = %v", err)
	}
}

func TestSetWebhookNon2xx(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"invalid subscribe"}`))
	})
	err := c.SetWebhook(t.Context(), "https://bridge.example.com/hook", []string{"MESSAGE"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("SetWebhook() error = %v, want HTTPError 422", err)
	}
}

func TestMarkRead(t *testing.T) {
	t.Run("default path uses GO body", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assertRequest(t, r, http.MethodPost, defaultMarkReadPath, testToken)
			body := decodeRequestBody(t, r)
			if len(body) != 2 {
				t.Errorf("body keys = %v, want exactly id and number", body)
			}
			if _, ok := body["chat"]; ok {
				t.Error("legacy chat field present, want none")
			}
			if _, ok := body["messageId"]; ok {
				t.Error("legacy messageId field present, want none")
			}
			ids, ok := body["id"].([]any)
			if !ok || len(ids) != 1 || ids[0] != "3EB0EXAMPLE" {
				t.Errorf("id = %v, want [3EB0EXAMPLE]", body["id"])
			}
			if body["number"] != "120363000000000001@g.us" {
				t.Errorf("number = %v", body["number"])
			}
			writeJSON(w, `{"message":"success"}`)
		})
		if err := c.MarkRead(t.Context(), "120363000000000001@g.us", "3EB0EXAMPLE"); err != nil {
			t.Fatalf("MarkRead() error = %v", err)
		}
	})

	t.Run("404 is non-fatal typed error without path", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		err := c.MarkRead(t.Context(), "x@g.us", "id")
		if !errors.Is(err, ErrNotSupported) {
			t.Fatalf("MarkRead() error = %v, want ErrNotSupported", err)
		}
		if strings.Contains(err.Error(), defaultMarkReadPath) {
			t.Errorf("error echoed configured path: %v", err)
		}
	})

	t.Run("500 stays an HTTPError, not not-supported", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		err := c.MarkRead(t.Context(), "x@g.us", "id")
		if errors.Is(err, ErrNotSupported) {
			t.Fatalf("MarkRead() error = %v, want HTTPError not ErrNotSupported", err)
		}
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusInternalServerError {
			t.Fatalf("MarkRead() error = %v, want HTTPError 500", err)
		}
	})
}

func TestDownloadMedia(t *testing.T) {
	t.Run("disabled on default path with zero io", func(t *testing.T) {
		var calls int32
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
		})
		got, err := c.DownloadMedia(t.Context(), DownloadOptions{MessageID: "3EB0MEDIA"})
		if !errors.Is(err, ErrNotSupported) {
			t.Fatalf("DownloadMedia() error = %v, want ErrNotSupported", err)
		}
		if got != (MediaDownload{}) {
			t.Errorf("DownloadMedia() = %+v, want zero value", got)
		}
		if got := atomic.LoadInt32(&calls); got != 0 {
			t.Errorf("transport calls = %d, want 0", got)
		}
	})

	t.Run("disabled on custom path and convertToMp4 with zero io", func(t *testing.T) {
		var calls int32
		c := newTestClientWithConfig(t, func(cfg *Config) {
			cfg.DownloadMediaPath = "/media/synthetic-key-in-path"
		}, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
		})
		_, err := c.DownloadMedia(t.Context(), DownloadOptions{MessageID: "x", ConvertToMp4: true})
		if !errors.Is(err, ErrNotSupported) {
			t.Fatalf("DownloadMedia() error = %v, want ErrNotSupported", err)
		}
		if got := atomic.LoadInt32(&calls); got != 0 {
			t.Errorf("transport calls = %d, want 0", got)
		}
	})
}

// TestEvolutionOptionalRoutes covers ADR-0008 §3: verified mark-read shape, a
// 404 that is non-fatal but not proof of absence, and disabled download.
func TestEvolutionOptionalRoutes(t *testing.T) {
	const (
		chatJID   = "120363000000000001@g.us"
		messageID = "3EB0EXAMPLE"
	)

	t.Run("mark read schema enforced", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != defaultMarkReadPath {
				t.Errorf("path = %q, want %q", r.URL.Path, defaultMarkReadPath)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if len(body) != 2 {
				t.Fatalf("body keys = %v, want exactly id and number", body)
			}
			var ids []string
			if err := json.Unmarshal(body["id"], &ids); err != nil {
				t.Fatalf("id not a string array: %v", err)
			}
			if len(ids) != 1 || ids[0] != messageID {
				t.Errorf("id = %v, want [%s]", ids, messageID)
			}
			var number string
			if err := json.Unmarshal(body["number"], &number); err != nil {
				t.Fatalf("number not a string: %v", err)
			}
			if number != chatJID {
				t.Errorf("number = %q, want %q", number, chatJID)
			}
			writeJSON(w, `{"message":"success"}`)
		})
		if err := c.MarkRead(t.Context(), chatJID, messageID); err != nil {
			t.Fatalf("MarkRead() error = %v", err)
		}
	})

	t.Run("custom mark read path refused without io", func(t *testing.T) {
		const secretPath = "/optional/synthetic-key-in-path"
		var calls int32
		c := newTestClientWithConfig(t, func(cfg *Config) {
			cfg.MarkReadPath = secretPath
		}, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
		})
		err := c.MarkRead(t.Context(), chatJID, messageID)
		if !errors.Is(err, ErrNotSupported) {
			t.Fatalf("MarkRead() error = %v, want ErrNotSupported", err)
		}
		if got := atomic.LoadInt32(&calls); got != 0 {
			t.Errorf("transport calls = %d, want 0", got)
		}
		if strings.Contains(err.Error(), "synthetic-key-in-path") {
			t.Errorf("error echoed configured path: %v", err)
		}
	})

	t.Run("mark read rejects empty inputs without io", func(t *testing.T) {
		var calls int32
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
		})
		for _, tc := range []struct{ jid, id string }{{"", messageID}, {chatJID, ""}} {
			if err := c.MarkRead(t.Context(), tc.jid, tc.id); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("MarkRead(%q,%q) error = %v, want ErrInvalidRequest", tc.jid, tc.id, err)
			}
		}
		if got := atomic.LoadInt32(&calls); got != 0 {
			t.Errorf("transport calls = %d, want 0", got)
		}
	})
}

// TestEvolutionAliasPriority covers ADR-0008 §5: the contracted sent-id alias
// order for valid shapes and fail-closed handling of a malformed truthy Info.
func TestEvolutionAliasPriority(t *testing.T) {
	valid := []struct {
		name string
		body string
		want string
	}{
		{name: "Info.ID wins", body: `{"data":{"Info":{"ID":"A"},"info":{"ID":"B"},"key":{"id":"C"}}}`, want: "A"},
		{name: "info.ID before key.id", body: `{"data":{"info":{"ID":"B"},"key":{"id":"C"}}}`, want: "B"},
		{name: "key.id before data.id", body: `{"data":{"key":{"id":"C"},"id":"D"}}`, want: "C"},
		{name: "empty Info shadows info", body: `{"data":{"Info":{},"info":{"ID":"B"},"key":{"id":"C"}}}`, want: "C"},
		{name: "null Info uses info", body: `{"data":{"Info":null,"info":{"ID":"B"}}}`, want: "B"},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, tt.body)
			})
			result, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{})
			if err != nil {
				t.Fatalf("SendText() error = %v", err)
			}
			if result.MessageID != tt.want {
				t.Errorf("MessageID = %q, want %q", result.MessageID, tt.want)
			}
		})
	}

	t.Run("malformed truthy Info fails closed", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, `{"data":{"Info":"truthy malformed","info":{"ID":"wrong"},"key":{"id":"right"}}}`)
		})
		result, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{})
		if err == nil {
			t.Fatal("SendText() error = nil, want unconfirmed")
		}
		if !errors.Is(err, ErrUnconfirmedSend) {
			t.Errorf("error = %v, want ErrUnconfirmedSend", err)
		}
		if result.MessageID != "" {
			t.Errorf("MessageID = %q, want empty", result.MessageID)
		}
		if strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), "right") {
			t.Errorf("error leaked an id: %v", err)
		}
	})
}
