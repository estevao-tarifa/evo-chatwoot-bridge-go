package chatwoot

// qa_verify_test.go — independent QA verification for the feat/cw-client slice.
//
// These tests reproduce the slice's claims from the specification (not the
// implementation's assumptions) and probe edge cases the implementation tests
// did not cover. Synthetic data only. No production code is modified.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// ─── Claim 1: search matches by identifier even when phone is null ───────────

// QA: a 1:1 contact whose phone_number is null but whose identifier equals the
// identity must be found. This is the M5 fix (contacts.md §3.3): the legacy
// matcher inspected phone_number only, so a null-phone contact was missed.
func TestQA_SearchMatchesIdentifierWithNullPhone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[` +
			`{"id":41,"name":"Ana","phone_number":null,"identifier":"15550000001"}]}`))
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err != nil {
		t.Fatalf("SearchContactExact() error = %v", err)
	}
	if contact == nil || contact.ID != 41 {
		t.Fatalf("contact = %+v, want id 41 (identifier match with null phone)", contact)
	}
}

// ─── Claim 2: HTTP 500 on search is an error, never (nil,nil) ────────────────

func TestQA_SearchHTTP500IsErrorNotNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err == nil {
		t.Fatal("SearchContactExact() error = nil on HTTP 500, want an error (G0)")
	}
	if contact != nil {
		t.Errorf("contact = %+v, want nil on error", contact)
	}
	if !IsRetryable(err) {
		t.Errorf("HTTP 500 must be classified retryable, got %v", err)
	}
}

// ─── Claim 3: signature verification rejects tampering ───────────────────────

func TestQA_WebhookSignatureRejectsTampering(t *testing.T) {
	const (
		secret    = "qa-secret"
		timestamp = "1750000000"
	)
	body := []byte(`{"event":"message_created","id":10001}`)
	valid := sign(secret, timestamp, body)

	tests := []struct {
		name      string
		secret    string
		timestamp string
		body      []byte
		header    string
		want      bool
	}{
		{name: "valid", secret: secret, timestamp: timestamp, body: body, header: valid, want: true},
		{name: "tampered body", secret: secret, timestamp: timestamp,
			body: []byte(`{"event":"message_created","id":99999}`), header: valid, want: false},
		{name: "wrong secret", secret: "other-secret", timestamp: timestamp, body: body, header: valid, want: false},
		{name: "wrong timestamp", secret: secret, timestamp: "1750000001", body: body, header: valid, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyWebhookSignature(tt.secret, tt.timestamp, tt.body, tt.header); got != tt.want {
				t.Errorf("VerifyWebhookSignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ─── Claim 4: message_type parses from both the integer and string forms ─────

func TestQA_MessageTypeIntAndStringForms(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "int 0", raw: `{"message_type":0}`, want: "incoming"},
		{name: "int 1", raw: `{"message_type":1}`, want: "outgoing"},
		{name: "string outgoing", raw: `{"message_type":"outgoing"}`, want: "outgoing"},
		{name: "string incoming", raw: `{"message_type":"incoming"}`, want: "incoming"},
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

// ─── Edge probes not covered by the implementation tests ─────────────────────

// QA: exact matching must NOT degrade into the legacy substring match (M8).
// A stored phone whose digits merely *contain* the query digits is a different
// identity and must not be reused. This is the identity-integrity fix (G0).
func TestQA_SearchExactRejectsSubstringPhone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// "+155500000010" -> digits "155500000010" contains query "15550000001".
		_, _ = w.Write([]byte(`{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[` +
			`{"id":50,"name":"Ana","phone_number":"+155500000010","identifier":"other"}]}`))
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err != nil {
		t.Fatalf("SearchContactExact() error = %v", err)
	}
	if contact != nil {
		t.Fatalf("contact = %+v, want nil (substring phone must not match exactly)", contact)
	}
}

// QA: a 2xx search whose body is not the contract shape must be an error, never
// a silent "not found" (G0, contacts.md M2 is the legacy bug being fixed).
func TestQA_SearchMalformedBodyIsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json at all`))
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err == nil {
		t.Fatal("malformed 2xx body must be an error, not (nil,nil)")
	}
	if contact != nil {
		t.Errorf("contact = %+v, want nil on error", contact)
	}
}

// QA: pagination must be bounded — a server that always reports has_more=true
// with no match must not loop forever; the client stops at its page cap with an
// error, never a conclusive miss (G0).
func TestQA_SearchPaginationIsBounded(t *testing.T) {
	requests := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		payload := make([]Contact, 0, resultsPerPage)
		for i := 0; i < resultsPerPage; i++ {
			payload = append(payload, Contact{ID: 1000 + i, Identifier: "other"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]any{
				"count":        resultsPerPage,
				"current_page": r.URL.Query().Get("page"),
				"has_more":     true,
			},
			"payload": payload,
		})
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err == nil {
		t.Fatal("pagination exhaustion must be an error, never not-found (G0)")
	}
	if contact != nil {
		t.Fatalf("contact = %+v, want nil", contact)
	}
	if requests > maxSearchPages {
		t.Errorf("requests = %d, want <= maxSearchPages (%d)", requests, maxSearchPages)
	}
	if requests != maxSearchPages {
		t.Errorf("requests = %d, want exactly %d (cap reached, no match)", requests, maxSearchPages)
	}
}
