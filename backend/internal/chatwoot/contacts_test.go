package chatwoot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestSearchContactExact(t *testing.T) {
	tests := []struct {
		name      string
		identity  string
		kind      Kind
		payload   string
		wantID    int
		wantFound bool
	}{
		{
			name:      "direct match by identifier while phone is null",
			identity:  "15550000001",
			kind:      KindDirect,
			payload:   `{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":7,"name":"Ana","phone_number":null,"identifier":"15550000001"}]}`,
			wantID:    7,
			wantFound: true,
		},
		{
			name:      "direct match by phone digits",
			identity:  "15550000001",
			kind:      KindDirect,
			payload:   `{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":8,"name":"Ana","phone_number":"+1 (555) 000-0001","identifier":"other"}]}`,
			wantID:    8,
			wantFound: true,
		},
		{
			name:      "direct phone digits differ",
			identity:  "15550000001",
			kind:      KindDirect,
			payload:   `{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":9,"name":"Ana","phone_number":"+15550000002","identifier":"other"}]}`,
			wantFound: false,
		},
		{
			name:      "direct no match",
			identity:  "15550000001",
			kind:      KindDirect,
			payload:   `{"meta":{"count":0,"current_page":1,"has_more":false},"payload":[]}`,
			wantFound: false,
		},
		{
			name:      "group exact identifier",
			identity:  "120363400000000000@g.us",
			kind:      KindGroup,
			payload:   `{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":11,"name":"Team","identifier":"120363400000000000@g.us"}]}`,
			wantID:    11,
			wantFound: true,
		},
		{
			name:      "group identifier case differs is a miss",
			identity:  "120363400000000000@g.us",
			kind:      KindGroup,
			payload:   `{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":12,"name":"Team","identifier":"120363400000000000@G.US"}]}`,
			wantFound: false,
		},
		{
			name:      "group substring is a miss",
			identity:  "120363400000000000@g.us",
			kind:      KindGroup,
			payload:   `{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":13,"name":"Team","identifier":"x120363400000000000@g.us"}]}`,
			wantFound: false,
		},
		{
			name:      "group ignores phone match",
			identity:  "120363400000000000@g.us",
			kind:      KindGroup,
			payload:   `{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":14,"name":"Team","phone_number":"+12036340000","identifier":null}]}`,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.payload))
			})

			contact, err := c.SearchContactExact(context.Background(), tt.identity, tt.kind)
			if err != nil {
				t.Fatalf("SearchContactExact() error = %v", err)
			}
			if tt.wantFound {
				if contact == nil {
					t.Fatalf("SearchContactExact() = nil, want contact %d", tt.wantID)
				}
				if contact.ID != tt.wantID {
					t.Errorf("contact.ID = %d, want %d", contact.ID, tt.wantID)
				}
			} else if contact != nil {
				t.Errorf("SearchContactExact() = %+v, want nil", contact)
			}
		})
	}
}

func TestSearchContactExact_RequestShape(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"meta":{"count":0,"current_page":1,"has_more":false},"payload":[]}`)

	if _, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect); err != nil {
		t.Fatalf("SearchContactExact() error = %v", err)
	}

	if rec.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", rec.Method)
	}
	if want := "/api/v1/accounts/42/contacts/search"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
	if got := rec.Query.Get("q"); got != "15550000001" {
		t.Errorf("q = %q, want %q", got, "15550000001")
	}
	if got := rec.Query.Get("include_contacts"); got != "true" {
		t.Errorf("include_contacts = %q, want true", got)
	}
	if got := rec.Query.Get("page"); got != "1" {
		t.Errorf("page = %q, want 1", got)
	}
	assertAuthHeader(t, rec.Header)
}

func TestSearchContactExact_HTTPErrorIsNotNotFound(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
			})

			contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
			if err == nil {
				t.Fatal("HTTP failure must be an error, never nil")
			}
			if contact != nil {
				t.Errorf("contact = %+v, want nil on error", contact)
			}
		})
	}
}

func TestSearchContactExact_Pagination(t *testing.T) {
	requests := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			var payload []Contact
			for i := 1; i <= resultsPerPage; i++ {
				payload = append(payload, Contact{ID: i, Identifier: "other"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"count": resultsPerPage, "current_page": "1", "has_more": true}, "payload": payload})
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"count":1,"current_page":"2","has_more":false},"payload":[{"id":20,"identifier":"15550000001"}]}`))
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err != nil {
		t.Fatalf("SearchContactExact() error = %v", err)
	}
	if contact == nil || contact.ID != 20 {
		t.Fatalf("contact = %+v, want id 20", contact)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestSearchContactExact_Validation(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{"meta":{"count":0,"current_page":1,"has_more":false},"payload":[]}`)

	if _, err := c.SearchContactExact(context.Background(), "  ", KindDirect); err == nil {
		t.Error("empty identity must be rejected")
	}
	if _, err := c.SearchContactExact(context.Background(), "15550000001", KindUnknown); err == nil {
		t.Error("unknown kind must be rejected")
	}
	if rec.Method != "" {
		t.Error("no request should be sent for invalid input")
	}
}

func TestCreateContact(t *testing.T) {
	tests := []struct {
		name     string
		params   ContactParams
		wantBody string
	}{
		{
			name:     "direct default name",
			params:   ContactParams{Kind: KindDirect, Identity: "15550000001"},
			wantBody: `{"name":"15550000001","phone_number":"+15550000001","identifier":"15550000001"}`,
		},
		{
			name:     "direct explicit name and avatar",
			params:   ContactParams{Kind: KindDirect, Identity: "15550000001", Name: "Ana", AvatarURL: "https://img.invalid/a.png"},
			wantBody: `{"name":"Ana","phone_number":"+15550000001","identifier":"15550000001","avatar_url":"https://img.invalid/a.png"}`,
		},
		{
			name:     "group explicit name",
			params:   ContactParams{Kind: KindGroup, Identity: "120363400000000000@g.us", Name: "Team"},
			wantBody: `{"name":"Team","identifier":"120363400000000000@g.us"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newRecordingClient(t, http.StatusOK, `{"payload":{"contact":{"id":5,"name":"Ana"}}}`)

			contact, err := c.CreateContact(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("CreateContact() error = %v", err)
			}
			if contact == nil || contact.ID != 5 {
				t.Fatalf("contact = %+v, want id 5", contact)
			}
			if rec.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", rec.Method)
			}
			if want := "/api/v1/accounts/42/contacts"; rec.Path != want {
				t.Errorf("path = %s, want %s", rec.Path, want)
			}
			assertJSONBody(t, rec.Body, tt.wantBody)
		})
	}
}

func TestCreateContact_Validation(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{}`)

	if _, err := c.CreateContact(context.Background(), ContactParams{Kind: KindDirect, Identity: "abc"}); err == nil {
		t.Error("digit-less direct identity must be rejected")
	}
	if _, err := c.CreateContact(context.Background(), ContactParams{Kind: KindGroup}); err == nil {
		t.Error("empty identity must be rejected")
	}
	for _, name := range []string{"", "  ", "Grupo", "120363400000000000@g.us"} {
		if _, err := c.CreateContact(context.Background(), ContactParams{Kind: KindGroup, Identity: "120363400000000000@g.us", Name: name}); err == nil {
			t.Error("untrusted group name must be rejected")
		}
	}
	if rec.Method != "" {
		t.Error("no request should be sent for invalid input")
	}
}

// A group display name must be the authoritative groupData.Name. The placeholder
// "Grupo" (case-insensitive), the raw JID, a JID-like value, a raw phone number
// and a blank name are rejected; legitimate names (including short numeric
// tokens) are accepted (ADR-0009 §1, AGENTS.md §7 rule 4).
func TestCreateContact_GroupDisplayNameGuard(t *testing.T) {
	const identity = "120363400000000000@g.us"
	tests := []struct {
		name        string
		displayName string
		wantErr     bool
	}{
		{name: "placeholder exact case", displayName: "Grupo", wantErr: true},
		{name: "placeholder lower case", displayName: "grupo", wantErr: true},
		{name: "placeholder upper case", displayName: "GRUPO", wantErr: true},
		{name: "raw phone", displayName: "15550000001", wantErr: true},
		{name: "raw phone with plus", displayName: "+15550000001", wantErr: true},
		{name: "identity JID", displayName: identity, wantErr: true},
		{name: "JID upper case domain", displayName: "120363400000000000@G.US", wantErr: true},
		{name: "other JID", displayName: "999888777666555444@lid", wantErr: true},
		{name: "blank", displayName: "   ", wantErr: true},
		{name: "legitimate name", displayName: "Equipe Suporte"},
		{name: "legitimate name with digits", displayName: "Time 2026"},
		{name: "short numeric name", displayName: "2026"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newRecordingClient(t, http.StatusOK, `{"payload":{"contact":{"id":5,"name":"x"}}}`)

			_, err := c.CreateContact(context.Background(), ContactParams{Kind: KindGroup, Identity: identity, Name: tt.displayName})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("group name %q must be rejected", tt.displayName)
				}
				if rec.Method != "" {
					t.Errorf("no request should be sent for rejected name %q", tt.displayName)
				}
				return
			}
			if err != nil {
				t.Fatalf("group name %q must be accepted: %v", tt.displayName, err)
			}
			if rec.Method != http.MethodPost {
				t.Errorf("method = %q, want POST", rec.Method)
			}
		})
	}
}

func TestUpdateContactName(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{}`)

	if err := c.UpdateContactName(context.Background(), 7, "Ana"); err != nil {
		t.Fatalf("UpdateContactName() error = %v", err)
	}
	if rec.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", rec.Method)
	}
	if want := "/api/v1/accounts/42/contacts/7"; rec.Path != want {
		t.Errorf("path = %s, want %s", rec.Path, want)
	}
	assertJSONBody(t, rec.Body, `{"name":"Ana"}`)
}

func TestUpdateContactName_RejectsEmpty(t *testing.T) {
	c, rec := newRecordingClient(t, http.StatusOK, `{}`)

	if err := c.UpdateContactName(context.Background(), 7, "  "); err == nil {
		t.Error("empty name must be rejected")
	}
	if rec.Method != "" {
		t.Error("no request should be sent for an empty name")
	}
}

func TestListContacts_Pagination(t *testing.T) {
	requests := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch r.URL.Query().Get("page") {
		case "1":
			body = `{"meta":{"count":3,"current_page":1},"payload":[{"id":1},{"id":2}]}`
		default:
			body = `{"meta":{"count":3,"current_page":2},"payload":[{"id":3}]}`
		}
		_, _ = w.Write([]byte(body))
	})

	contacts, err := c.ListContacts(context.Background())
	if err != nil {
		t.Fatalf("ListContacts() error = %v", err)
	}
	if len(contacts) != 3 {
		t.Fatalf("len(contacts) = %d, want 3", len(contacts))
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestSearchContactExact_EmptyPayloadIsNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"meta":{"count":0,"current_page":1,"has_more":false},"payload":[]}`))
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if contact != nil {
		t.Errorf("contact = %+v, want nil", contact)
	}
}

// A 2xx body that is valid JSON but not the frozen {meta, payload} shape must be
// a malformed error, never decoded as "no contacts" — otherwise the engine would
// CREATE a contact that already exists (G0, finding M2).
func TestSearchContactExact_MalformedBodyIsError(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "error object", body: `{"error":"something went wrong"}`},
		{name: "payload without meta", body: `{"payload":[]}`},
		{name: "meta without payload", body: `{"meta":{"has_more":false}}`},
		{name: "null meta", body: `{"meta":null,"payload":[]}`},
		{name: "null payload", body: `{"meta":{"count":0,"current_page":1,"has_more":false},"payload":null}`},
		{name: "missing pagination", body: `{"meta":{},"payload":[]}`},
		{name: "null pagination", body: `{"meta":{"count":0,"current_page":1,"has_more":null},"payload":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
			if err == nil {
				t.Fatal("malformed 2xx body must be an error, never (nil,nil) (G0)")
			}
			if contact != nil {
				t.Errorf("contact = %+v, want nil on error", contact)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Kind != ErrorKindMalformed {
				t.Fatalf("error = %v, want ErrorKindMalformed", err)
			}
		})
	}
}

func TestCreateContact_MalformedResponse(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{name: "missing payload", body: `{}`},
		{name: "null payload", body: `{"payload":null}`},
		{name: "missing contact", body: `{"payload":{}}`},
		{name: "null contact", body: `{"payload":{"contact":null}}`},
		{name: "missing id", body: `{"payload":{"contact":{}}}`},
		{name: "null id", body: `{"payload":{"contact":{"id":null}}}`},
		{name: "zero id", body: `{"payload":{"contact":{"id":0}}}`},
		{name: "negative id", body: `{"payload":{"contact":{"id":-1}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newRecordingClient(t, http.StatusOK, tt.body)
			contact, err := c.CreateContact(context.Background(), ContactParams{Kind: KindDirect, Identity: "15550000001"})
			var apiErr *APIError
			if contact != nil || !errors.As(err, &apiErr) || apiErr.Kind != ErrorKindMalformed {
				t.Fatalf("contact = %+v, error = %v, want nil and malformed", contact, err)
			}
		})
	}
}

// Any absent, invalid, contradictory or mismatched search pagination field is an
// inconclusive error, never (nil,nil) and never permission to create (ADR-0009).
func TestSearchContactExact_MetaValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "absent count", body: `{"meta":{"current_page":1,"has_more":false},"payload":[]}`},
		{name: "absent page", body: `{"meta":{"count":1,"has_more":false},"payload":[{"id":1}]}`},
		{name: "null page", body: `{"meta":{"count":1,"current_page":null,"has_more":false},"payload":[{"id":1}]}`},
		{name: "invalid page text", body: `{"meta":{"count":1,"current_page":"abc","has_more":false},"payload":[{"id":1}]}`},
		{name: "zero page", body: `{"meta":{"count":1,"current_page":0,"has_more":false},"payload":[{"id":1}]}`},
		{name: "negative page", body: `{"meta":{"count":1,"current_page":-1,"has_more":false},"payload":[{"id":1}]}`},
		{name: "fractional page", body: `{"meta":{"count":1,"current_page":"1.5","has_more":false},"payload":[{"id":1}]}`},
		{name: "page mismatch", body: `{"meta":{"count":1,"current_page":2,"has_more":false},"payload":[{"id":1}]}`},
		{name: "count exceeds page length", body: `{"meta":{"count":2,"current_page":1,"has_more":false},"payload":[{"id":1}]}`},
		{name: "count below page length", body: `{"meta":{"count":0,"current_page":1,"has_more":false},"payload":[{"id":1}]}`},
		{name: "count above page size", body: `{"meta":{"count":16,"current_page":1,"has_more":false},"payload":[{"id":1}]}`},
		{name: "absent has_more", body: `{"meta":{"count":0,"current_page":1},"payload":[]}`},
		{name: "null has_more", body: `{"meta":{"count":0,"current_page":1,"has_more":null},"payload":[]}`},
		{name: "has_more with partial page", body: `{"meta":{"count":1,"current_page":1,"has_more":true},"payload":[{"id":1}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			posts := 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts++
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
			if err == nil {
				t.Fatal("inconclusive metadata must be an error, never (nil,nil) (G0)")
			}
			if contact != nil {
				t.Errorf("contact = %+v, want nil on error", contact)
			}
			if posts != 0 {
				t.Errorf("posts = %d, want 0 (no creation after an inconclusive search)", posts)
			}
		})
	}
}

// A server that never advances the page number must stop with an error, not loop.
func TestSearchContactExact_NonProgressingPage(t *testing.T) {
	requests := 0
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		payload := make([]Contact, 0, resultsPerPage)
		for i := 0; i < resultsPerPage; i++ {
			payload = append(payload, Contact{ID: 900 + i, Identifier: "other"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":    map[string]any{"count": resultsPerPage, "current_page": "1", "has_more": true},
			"payload": payload,
		})
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err == nil || contact != nil {
		t.Fatalf("contact = %+v, err = %v, want error on a nonprogressing page", contact, err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2 (stop at the first nonprogressing page)", requests)
	}
}

// A failed later page is an error, never a conclusive miss.
func TestSearchContactExact_LaterPageFailure(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("page") == "1" {
					payload := make([]Contact, 0, resultsPerPage)
					for i := 0; i < resultsPerPage; i++ {
						payload = append(payload, Contact{ID: 900 + i, Identifier: "other"})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"meta":    map[string]any{"count": resultsPerPage, "current_page": "1", "has_more": true},
						"payload": payload,
					})
					return
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
			})

			contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
			if err == nil || contact != nil {
				t.Fatalf("contact = %+v, err = %v, want error on a failed later page", contact, err)
			}
			if requests != 2 {
				t.Errorf("requests = %d, want 2", requests)
			}
		})
	}
}

// Reaching the finite page ceiling with has_more still true is an error, never a
// conclusive miss.
func TestSearchContactExact_PageCeiling(t *testing.T) {
	requests := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		payload := make([]Contact, 0, resultsPerPage)
		for i := 0; i < resultsPerPage; i++ {
			payload = append(payload, Contact{ID: 900 + i, Identifier: "other"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":    map[string]any{"count": resultsPerPage, "current_page": r.URL.Query().Get("page"), "has_more": true},
			"payload": payload,
		})
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if err == nil || contact != nil {
		t.Fatalf("contact = %+v, err = %v, want ceiling error", contact, err)
	}
	if !errors.Is(err, ErrInconclusive) {
		t.Errorf("err = %v, want ErrInconclusive", err)
	}
	if requests != maxSearchPages {
		t.Errorf("requests = %d, want %d", requests, maxSearchPages)
	}
}

// Two distinct exact contacts across pages must fail closed, never pick the
// first (identity integrity, AGENTS.md §7).
func TestSearchContactExact_DuplicateIdentityAcrossPages(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			payload := []Contact{{ID: 1, Identifier: "15550000001"}}
			for i := 0; i < resultsPerPage-1; i++ {
				payload = append(payload, Contact{ID: 100 + i, Identifier: "other"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta":    map[string]any{"count": resultsPerPage, "current_page": "1", "has_more": true},
				"payload": payload,
			})
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"count":1,"current_page":"2","has_more":false},"payload":[{"id":2,"identifier":"15550000001"}]}`))
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("err = %v, want ErrIdentityConflict", err)
	}
	if contact != nil {
		t.Errorf("contact = %+v, want nil on conflict", contact)
	}
}

func TestSearchContactExact_DuplicateIdentitySamePage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"meta":{"count":2,"current_page":1,"has_more":false},"payload":[` +
			`{"id":1,"identifier":"15550000001"},{"id":2,"identifier":"15550000001"}]}`))
	})

	contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
	if !errors.Is(err, ErrIdentityConflict) || contact != nil {
		t.Fatalf("contact = %+v, err = %v, want identity conflict", contact, err)
	}
}

// A matched contact with a non-positive id is a malformed response, not a miss.
func TestSearchContactExact_InvalidMatchedID(t *testing.T) {
	for _, id := range []string{"0", "-5"} {
		t.Run("id "+id, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"meta":{"count":1,"current_page":1,"has_more":false},"payload":[{"id":` + id + `,"identifier":"15550000001"}]}`))
			})
			contact, err := c.SearchContactExact(context.Background(), "15550000001", KindDirect)
			if err == nil || contact != nil {
				t.Fatalf("contact = %+v, err = %v, want error for invalid matched id", contact, err)
			}
			if !errors.Is(err, ErrInconclusive) {
				t.Errorf("err = %v, want ErrInconclusive", err)
			}
		})
	}
}

// The index reports a total count and has no has_more; stopping before the total
// is an error, not a successful partial list (ADR-0009 §2).
func TestListContacts_StopsBeforeTotalIsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`{"meta":{"count":5,"current_page":1},"payload":[{"id":1},{"id":2}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"count":5,"current_page":2},"payload":[]}`))
	})

	contacts, err := c.ListContacts(context.Background())
	if err == nil || contacts != nil {
		t.Fatalf("contacts = %+v, err = %v, want error before reaching the total", contacts, err)
	}
}

func TestListContacts_CeilingIsError(t *testing.T) {
	requests := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		payload := make([]Contact, 0, 15)
		for i := 0; i < 15; i++ {
			payload = append(payload, Contact{ID: requests*100 + i})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":    map[string]any{"count": 100000, "current_page": r.URL.Query().Get("page")},
			"payload": payload,
		})
	})

	contacts, err := c.ListContacts(context.Background())
	if err == nil || contacts != nil {
		t.Fatalf("contacts = %+v, err = %v, want ceiling error", contacts, err)
	}
	if requests != maxListPages {
		t.Errorf("requests = %d, want %d", requests, maxListPages)
	}
}

// guard: the search response must decode has_more from the contract shape.
func TestSearchContactExact_DecodesMeta(t *testing.T) {
	var resp contactsResponse
	raw := `{"meta":{"count":1,"current_page":1,"has_more":true},"payload":[{"id":1}]}`
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Meta.HasMore == nil || !*resp.Meta.HasMore || len(resp.Payload) != 1 {
		t.Errorf("decoded meta = %+v, payload = %+v", resp.Meta, resp.Payload)
	}
}
