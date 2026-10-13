package chatwoot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ptrBool(b bool) *bool { return &b }

func ptrStr(s string) *string { return &s }

func TestListInboxes(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"payload":[{"id":1,"name":"Email","channel_type":"Channel::Email"},{"id":2,"name":"WhatsApp","channel_type":"Channel::Api"}]}`)

	inboxes, err := c.ListInboxes(context.Background())
	if err != nil {
		t.Fatalf("ListInboxes() error = %v", err)
	}
	if len(inboxes) != 2 {
		t.Fatalf("len = %d, want 2", len(inboxes))
	}
	if inboxes[1].ChannelType != "Channel::Api" {
		t.Errorf("channel_type = %q", inboxes[1].ChannelType)
	}
	if rec.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", rec.Method)
	}
	if want := "/api/v1/accounts/42/inboxes"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
}

func TestGetInbox(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":2,"name":"WhatsApp","channel_type":"Channel::Api","callback_webhook_url":null}`)

	inbox, err := c.GetInbox(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetInbox() error = %v", err)
	}
	if inbox == nil || inbox.ID != 2 {
		t.Fatalf("inbox = %+v, want id 2", inbox)
	}
	if inbox.CallbackWebhookURL != "" {
		t.Errorf("callback_webhook_url = %q, want empty for API inbox", inbox.CallbackWebhookURL)
	}
	if want := "/api/v1/accounts/42/inboxes/2"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
}

func TestCreateInbox(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":42,"name":"WhatsApp - x","channel_type":"Channel::Api","callback_webhook_url":null}`)

	params := InboxParams{
		Name:       "WhatsApp - x",
		WebhookURL: "https://bridge.invalid/api/webhooks/chatwoot/1",
	}
	inbox, err := c.CreateInbox(context.Background(), params)
	if err != nil {
		t.Fatalf("CreateInbox() error = %v", err)
	}
	if inbox == nil || inbox.ID != 42 {
		t.Fatalf("inbox = %+v, want id 42", inbox)
	}
	// The write response is not readback proof: the desired URL must never be
	// echoed as if Chatwoot verified it. Here the response omits webhook_url.
	if inbox.WebhookURL != "" {
		t.Errorf("WebhookURL = %q, want empty (never echoed from the request)", inbox.WebhookURL)
	}
	if rec.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", rec.Method)
	}
	if want := "/api/v1/accounts/42/inboxes"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
	assertJSONBody(t, rec.Body, `{"name":"WhatsApp - x","channel":{"type":"api","webhook_url":"https://bridge.invalid/api/webhooks/chatwoot/1"}}`)
}

func TestCreateInbox_DefaultsWebhookToEmpty(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":42}`)

	if _, err := c.CreateInbox(context.Background(), InboxParams{Name: "N"}); err != nil {
		t.Fatalf("CreateInbox() error = %v", err)
	}
	assertJSONBody(t, rec.Body, `{"name":"N","channel":{"type":"api","webhook_url":""}}`)
}

func TestCreateInbox_WithSettings(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":42}`)

	params := InboxParams{
		Name: "N",
		InboxSettings: InboxSettings{
			GreetingEnabled:      ptrBool(true),
			GreetingMessage:      ptrStr("hi"),
			EnableAutoAssignment: ptrBool(false),
		},
	}
	if _, err := c.CreateInbox(context.Background(), params); err != nil {
		t.Fatalf("CreateInbox() error = %v", err)
	}
	assertJSONBody(t, rec.Body, `{"name":"N","channel":{"type":"api","webhook_url":""},"greeting_enabled":true,"greeting_message":"hi","enable_auto_assignment":false}`)
}

func TestCreateInbox_RequiresName(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{}`)

	if _, err := c.CreateInbox(context.Background(), InboxParams{}); err == nil {
		t.Error("empty name must be rejected")
	}
	if rec.Method != "" {
		t.Error("no request should be sent for an invalid inbox")
	}
}

func TestUpdateInbox(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":42,"name":"N"}`)

	params := InboxParams{
		Name:       "N",
		WebhookURL: "https://bridge.invalid/api/webhooks/chatwoot/1",
		InboxSettings: InboxSettings{
			GreetingEnabled:    ptrBool(true),
			EnableEmailCollect: ptrBool(false),
		},
	}
	inbox, err := c.UpdateInbox(context.Background(), 42, params)
	if err != nil {
		t.Fatalf("UpdateInbox() error = %v", err)
	}
	// The PATCH response omits webhook_url; the desired URL must not be echoed.
	if inbox.WebhookURL != "" {
		t.Errorf("WebhookURL = %q, want empty (never echoed from the request)", inbox.WebhookURL)
	}
	if rec.Method != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", rec.Method)
	}
	if want := "/api/v1/accounts/42/inboxes/42"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
	// channel must carry only webhook_url (no type) on update.
	assertJSONBody(t, rec.Body, `{"name":"N","channel":{"webhook_url":"https://bridge.invalid/api/webhooks/chatwoot/1"},"greeting_enabled":true,"enable_email_collect":false}`)
}

func TestUpdateInbox_OmitsUnsetKeys(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":42}`)

	if _, err := c.UpdateInbox(context.Background(), 42, InboxParams{}); err != nil {
		t.Fatalf("UpdateInbox() error = %v", err)
	}
	assertJSONBody(t, rec.Body, `{}`)
}

// GetInbox returns Chatwoot's observed webhook_url, distinct from the legacy
// callback_webhook_url (null for API inboxes).
func TestGetInbox_ObservedWebhookURL(t *testing.T) {
	const observed = "https://chatwoot.invalid/api/webhooks/observed-token"
	c, _ := newRecordingClient(t, http.StatusOK,
		`{"id":2,"name":"WhatsApp","channel_type":"Channel::Api","callback_webhook_url":null,"webhook_url":"`+observed+`"}`)

	inbox, err := c.GetInbox(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetInbox() error = %v", err)
	}
	if inbox.WebhookURL != observed {
		t.Errorf("WebhookURL = %q, want observed %q", inbox.WebhookURL, observed)
	}
	if inbox.CallbackWebhookURL != "" {
		t.Errorf("CallbackWebhookURL = %q, want empty", inbox.CallbackWebhookURL)
	}
}

// A write response carrying a different or omitted webhook_url must never be
// rewritten to the desired value.
func TestWriteInbox_ObservedDiffersFromDesired(t *testing.T) {
	const desired = "https://bridge.invalid/api/webhooks/chatwoot/1"
	const observed = "https://chatwoot.invalid/api/webhooks/old-token"

	for _, tt := range []struct {
		name     string
		response string
		want     string
	}{
		{name: "different value", response: `{"id":42,"webhook_url":"` + observed + `"}`, want: observed},
		{name: "omitted value", response: `{"id":42}`, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newRecordingClient(t, http.StatusOK, tt.response)
			inbox, err := c.UpdateInbox(context.Background(), 42, InboxParams{WebhookURL: desired})
			if err != nil {
				t.Fatalf("UpdateInbox() error = %v", err)
			}
			if inbox.WebhookURL != tt.want {
				t.Errorf("WebhookURL = %q, want %q", inbox.WebhookURL, tt.want)
			}
			if inbox.WebhookURL == desired {
				t.Error("desired URL was echoed as observed state")
			}
		})
	}
}

// A tokenized webhook URL is a secret: a failed write must not leak it through
// the error or the structured logs.
func TestInboxWebhookURLNeverLeaksInLogsOrErrors(t *testing.T) {
	const token = "secret-path-token-abc123"
	const webhookURL = "https://bridge.invalid/api/webhooks/chatwoot/" + token

	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(
		Config{APIURL: srv.URL, APIToken: testToken, AccountID: testAccountID},
		WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := c.UpdateInbox(context.Background(), 42, InboxParams{WebhookURL: webhookURL}); err == nil {
		t.Fatal("expected failure")
	} else {
		assertNoLeak(t, err, token, webhookURL)
	}
	if logged := buf.String(); strings.Contains(logged, token) || strings.Contains(logged, webhookURL) {
		t.Errorf("log leaked the webhook URL: %s", logged)
	}
}
