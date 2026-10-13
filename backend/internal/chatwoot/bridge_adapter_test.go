package chatwoot

import (
	"errors"
	"net/http"
	"testing"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

func TestBridgeAdapter_SearchContactExact(t *testing.T) {
	t.Run("unique match maps to a one-element slice", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeAdapterJSON(w, `{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":7,"name":"Ana","phone_number":"+15550000001","identifier":"15550000001"}]}`)
		})
		got, err := NewBridgeAdapter(c).SearchContactExact(t.Context(), "15550000001", bridge.KindDirect)
		if err != nil {
			t.Fatalf("SearchContactExact() error = %v", err)
		}
		want := []bridge.Contact{{ID: 7, Name: "Ana", PhoneNumber: "+15550000001", Identifier: "15550000001"}}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("SearchContactExact() = %+v, want %+v", got, want)
		}
	})

	t.Run("no match maps to nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeAdapterJSON(w, `{"meta":{"count":0,"current_page":1,"has_more":false},"payload":[]}`)
		})
		got, err := NewBridgeAdapter(c).SearchContactExact(t.Context(), "15550000001", bridge.KindDirect)
		if err != nil || got != nil {
			t.Fatalf("SearchContactExact() = %+v, %v; want nil, nil", got, err)
		}
	})

	t.Run("two distinct exact matches fail closed with the bridge sentinel", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeAdapterJSON(w, `{"meta":{"count":2,"current_page":1,"has_more":false},"payload":[{"id":1,"identifier":"15550000001"},{"id":2,"identifier":"15550000001"}]}`)
		})
		got, err := NewBridgeAdapter(c).SearchContactExact(t.Context(), "15550000001", bridge.KindDirect)
		if got != nil {
			t.Fatalf("SearchContactExact() = %+v, want nil", got)
		}
		if !errors.Is(err, bridge.ErrIdentityConflict) {
			t.Fatalf("SearchContactExact() error = %v, want bridge.ErrIdentityConflict", err)
		}
	})

	t.Run("a read error is never a miss and keeps its retry classification", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		})
		got, err := NewBridgeAdapter(c).SearchContactExact(t.Context(), "15550000001", bridge.KindDirect)
		if got != nil || err == nil {
			t.Fatalf("SearchContactExact() = %+v, %v; want nil and an error", got, err)
		}
		if errors.Is(err, bridge.ErrIdentityConflict) {
			t.Error("a transport error must not be reported as an identity conflict")
		}
		if !IsRetryable(err) {
			t.Error("a retryable GET error must keep IsRetryable() true through the adapter")
		}
	})

	t.Run("an invalid bridge kind is rejected before I/O", func(t *testing.T) {
		c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
			t.Error("no request must be made for an invalid kind")
		})
		if _, err := NewBridgeAdapter(c).SearchContactExact(t.Context(), "15550000001", bridge.KindUnknown); err == nil {
			t.Fatal("SearchContactExact() error = nil, want a kind error")
		}
	})
}

func TestBridgeAdapter_CreateContact(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"payload":{"contact":{"id":5,"name":"Equipe X","identifier":"120363400000000000@g.us"}}}`)

	got, err := NewBridgeAdapter(c).CreateContact(t.Context(), bridge.ContactParams{
		Kind: bridge.KindGroup, Identity: "120363400000000000@g.us", Name: "Equipe X",
	})
	if err != nil {
		t.Fatalf("CreateContact() error = %v", err)
	}
	if got == nil || got.ID != 5 || got.Name != "Equipe X" {
		t.Fatalf("CreateContact() = %+v, want id 5 named Equipe X", got)
	}
	assertJSONBody(t, rec.Body, `{"name":"Equipe X","identifier":"120363400000000000@g.us"}`)
}

func TestBridgeAdapter_UpdateContactName(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, ``)
	if err := NewBridgeAdapter(c).UpdateContactName(t.Context(), 5, "Ana"); err != nil {
		t.Fatalf("UpdateContactName() error = %v", err)
	}
	if rec.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", rec.Method)
	}
	assertJSONBody(t, rec.Body, `{"name":"Ana"}`)
}

func TestBridgeAdapter_ListConversations(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeAdapterJSON(w, `{"payload":[{"id":9,"inbox_id":10,"status":"open","last_activity_at":123}]}`)
	})
	got, err := NewBridgeAdapter(c).ListConversations(t.Context(), 5)
	if err != nil {
		t.Fatalf("ListConversations() error = %v", err)
	}
	want := bridge.Conversation{ID: 9, InboxID: 10, Status: "open", LastActivityAt: 123}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("ListConversations() = %+v, want %+v", got, want)
	}
}

func TestBridgeAdapter_CreateConversation(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"id":9,"inbox_id":10,"status":"open"}`)
	got, err := NewBridgeAdapter(c).CreateConversation(t.Context(), 5, 10)
	if err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	if got == nil || got.ID != 9 || got.InboxID != 10 {
		t.Fatalf("CreateConversation() = %+v, want id 9 inbox 10", got)
	}
	assertJSONBody(t, rec.Body, `{"contact_id":5,"inbox_id":10,"status":"open"}`)
}

func TestBridgeAdapter_SendText(t *testing.T) {
	tests := []struct {
		name      string
		direction bridge.MessageDirection
		wantType  string
	}{
		{name: "incoming", direction: bridge.DirectionIncoming, wantType: "incoming"},
		{name: "outgoing", direction: bridge.DirectionOutgoing, wantType: "outgoing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newRecordingClient(t, http.StatusOK, `{"id":99,"conversation_id":5,"message_type":"outgoing"}`)
			got, err := NewBridgeAdapter(c).SendText(t.Context(), 5, "hello", tt.direction)
			if err != nil {
				t.Fatalf("SendText() error = %v", err)
			}
			if got == nil || got.MessageID != "99" {
				t.Fatalf("SendText() = %+v, want message id 99", got)
			}
			assertJSONBody(t, rec.Body, `{"content":"hello","message_type":"`+tt.wantType+`","private":false}`)
		})
	}

	t.Run("an invalid direction is rejected before I/O", func(t *testing.T) {
		c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
			t.Error("no request must be made for an invalid direction")
		})
		if _, err := NewBridgeAdapter(c).SendText(t.Context(), 5, "hi", bridge.MessageDirection("sideways")); err == nil {
			t.Fatal("SendText() error = nil, want a direction error")
		}
	})
}

// writeAdapterJSON writes a JSON response body with 200.
func writeAdapterJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
