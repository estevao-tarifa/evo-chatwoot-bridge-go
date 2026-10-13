package chatwoot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// errorSurfaces returns every representation of err an attacker or a log
// aggregator can reach: Error(), %v, %+v, JSON and the whole Unwrap chain.
func errorSurfaces(err error) []string {
	surfaces := []string{
		err.Error(),
		fmt.Sprintf("%v", err),
		fmt.Sprintf("%+v", err),
	}
	if encoded, mErr := json.Marshal(err); mErr == nil {
		surfaces = append(surfaces, string(encoded))
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		surfaces = append(surfaces, e.Error())
	}
	return surfaces
}

func assertNoLeak(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	for _, surface := range errorSurfaces(err) {
		for _, value := range forbidden {
			if value != "" && strings.Contains(surface, value) {
				t.Errorf("error surface %q leaked %q", surface, value)
			}
		}
	}
}

// A transport failure must never expose the request URL, its query (a phone/JID)
// or the token through any surface (AGENTS.md §8, finding H4). The raw
// *url.Error cause must not be reachable via Unwrap/errors.As.
func TestAPIError_TransportErrorRedactsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := New(
		Config{APIURL: srv.URL, APIToken: testToken, AccountID: testAccountID},
		WithLogger(discardLogger()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.Close() // force a dial failure on the next request

	const phone = "15550000001"
	_, err = c.SearchContactExact(context.Background(), phone, KindDirect)
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}

	assertNoLeak(t, err, phone, testToken, "://")

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not *APIError: %T", err)
	}
	if apiErr.Kind != ErrorKindTransport {
		t.Errorf("Kind = %v, want transport", apiErr.Kind)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		t.Errorf("transport *url.Error is reachable: %v", urlErr)
	}
	if apiErr.Unwrap() != nil {
		t.Errorf("opaque boundary: Unwrap() = %v, want nil for a non-context cause", apiErr.Unwrap())
	}
}

// A 2xx body that overflows a JSON number ({"secret":1e9999}) must fail as
// malformed without exposing the decoder cause on any surface.
func TestAPIError_JSONOverflowDoesNotLeakUpstreamText(t *testing.T) {
	const secret = "synthetic-secret-value"
	body := `{"id":1e9999,"secret":"` + secret + `"}`
	c, _ := newRecordingClient(t, http.StatusOK, body)

	_, err := c.SendText(context.Background(), 10, "hi", MessageTypeIncoming)
	if err == nil {
		t.Fatal("numeric overflow must be a malformed error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorKindMalformed {
		t.Fatalf("error = %v, want ErrorKindMalformed", err)
	}
	assertNoLeak(t, err, secret, "1e9999")
	if apiErr.Unwrap() != nil {
		t.Errorf("decoder cause must be opaque, got %v", apiErr.Unwrap())
	}
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	if errors.As(err, &typeErr) || errors.As(err, &syntaxErr) {
		t.Error("decoder cause must not be reachable via errors.As")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClient_ErrorStringsDiscardUntrustedText(t *testing.T) {
	const private = "synthetic private text"
	const identity = "15550000001"
	leak := testToken + " " + private + " " + identity
	for _, tt := range []struct {
		name           string
		status         int
		body           string
		transportError error
	}{
		{name: "http json", status: 422, body: `{"error":"` + leak + `"}`},
		{name: "http free text", status: 500, body: leak},
		{name: "decode type error", status: 200, body: `{"id":"` + leak + `"}`},
		{name: "custom decode error", status: 200, body: `{"id":1,"message_type":{"` + leak + `":true}}`},
		{name: "transport error", transportError: errors.New(leak)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(
				Config{APIURL: "http://chatwoot.invalid", APIToken: testToken, AccountID: testAccountID},
				WithLogger(discardLogger()),
				WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					if tt.transportError != nil {
						return nil, tt.transportError
					}
					return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
				})}),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.SendText(context.Background(), 10, private, MessageTypeIncoming)
			if err == nil {
				t.Fatal("expected error")
			}
			assertNoLeak(t, err, testToken, private, identity)
			if tt.transportError != nil && errors.Is(err, tt.transportError) {
				t.Error("raw transport cause must not be inspectable")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Error("error must be an *APIError")
			}
		})
	}
	c, _ := newRecordingClient(t, http.StatusOK, `{}`)
	_, err := c.CreateContact(context.Background(), ContactParams{Kind: KindDirect, Identity: private})
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatal("validation must reject digit-less identity without echoing it")
	}
}
