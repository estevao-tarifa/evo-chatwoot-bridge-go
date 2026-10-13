package chatwoot

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestListConversations(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"payload":[{"id":10,"inbox_id":3,"status":"open","last_activity_at":100},{"id":11,"inbox_id":3,"status":"resolved","last_activity_at":200}]}`)

	convs, err := c.ListConversations(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListConversations() error = %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("len = %d, want 2", len(convs))
	}
	if rec.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", rec.Method)
	}
	if want := "/api/v1/accounts/42/contacts/7/conversations"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
	assertAuthHeader(t, rec.Header)
}

func TestListConversations_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})

	if _, err := c.ListConversations(context.Background(), 7); err == nil {
		t.Fatal("HTTP failure must be an error, never an empty list")
	}
}

// A 2xx body without payload must be a malformed error, never an empty list —
// otherwise the engine would CREATE a conversation that already exists (G0,
// finding M2).
func TestListConversations_MalformedBodyIsError(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "error object", body: `{"error":"something went wrong"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			convs, err := c.ListConversations(context.Background(), 7)
			if err == nil {
				t.Fatal("malformed 2xx body must be an error, never an empty list (G0)")
			}
			if convs != nil {
				t.Errorf("conversations = %+v, want nil on error", convs)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Kind != ErrorKindMalformed {
				t.Fatalf("error = %v, want ErrorKindMalformed", err)
			}
		})
	}
}

func TestCreateConversation(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":99,"inbox_id":3,"status":"open"}`)

	conv, err := c.CreateConversation(context.Background(), 7, 3)
	if err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	if conv == nil || conv.ID != 99 {
		t.Fatalf("conv = %+v, want id 99", conv)
	}
	if rec.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", rec.Method)
	}
	if want := "/api/v1/accounts/42/conversations"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
	assertJSONBody(t, rec.Body, `{"contact_id":7,"inbox_id":3,"status":"open"}`)
}

func TestCreateConversation_Validation(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{}`)

	if _, err := c.CreateConversation(context.Background(), 7, 0); err == nil {
		t.Error("null inbox must be rejected (Q5)")
	}
	if _, err := c.CreateConversation(context.Background(), 0, 3); err == nil {
		t.Error("invalid contact id must be rejected")
	}
	if rec.Method != "" {
		t.Error("no request should be sent for invalid input")
	}
}

func TestSelectConversation(t *testing.T) {
	tests := []struct {
		name          string
		conversations []Conversation
		inboxID       int
		wantID        int // 0 means nil
	}{
		{
			name:          "none",
			conversations: nil,
			inboxID:       3,
			wantID:        0,
		},
		{
			name:          "inbox mismatch",
			conversations: []Conversation{{ID: 1, InboxID: 9, Status: "open", LastActivityAt: 100}},
			inboxID:       3,
			wantID:        0,
		},
		{
			name:          "resolved skipped",
			conversations: []Conversation{{ID: 1, InboxID: 3, Status: "resolved", LastActivityAt: 100}},
			inboxID:       3,
			wantID:        0,
		},
		{
			name: "most recent wins",
			conversations: []Conversation{
				{ID: 1, InboxID: 3, Status: "open", LastActivityAt: 100},
				{ID: 2, InboxID: 3, Status: "pending", LastActivityAt: 200},
			},
			inboxID: 3,
			wantID:  2,
		},
		{
			name: "id tie-break",
			conversations: []Conversation{
				{ID: 5, InboxID: 3, Status: "open", LastActivityAt: 100},
				{ID: 9, InboxID: 3, Status: "open", LastActivityAt: 100},
			},
			inboxID: 3,
			wantID:  9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectConversation(tt.conversations, tt.inboxID)
			if tt.wantID == 0 {
				if got != nil {
					t.Errorf("SelectConversation() = %+v, want nil", got)
				}
				return
			}
			if got == nil || got.ID != tt.wantID {
				t.Errorf("SelectConversation() = %+v, want id %d", got, tt.wantID)
			}
		})
	}
}
