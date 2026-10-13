package evolution

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Synthetic credentials only — never real tokens (AGENTS.md §9).
const (
	testGlobalKey = "synthetic-global-key"
	testToken     = "synthetic-instance-token"
	testInstance  = "synthetic-instance"
)

// newTestClient starts an httptest server with handler and returns a Client
// pointing at it, using testToken as the instance credential.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	return newTestClientWithToken(t, handler, testToken)
}

// newTestClientWithToken is newTestClient with an explicit instance token; an
// empty token exercises the global-key fallback.
func newTestClientWithToken(t *testing.T, handler http.HandlerFunc, token string) *Client {
	t.Helper()
	return newTestClientWithConfig(t, func(cfg *Config) { cfg.InstanceToken = token }, handler)
}

// newTestClientWithConfig builds a client against a test server, allowing the
// config to be adjusted before New.
func newTestClientWithConfig(t *testing.T, mutate func(*Config), handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := Config{
		APIURL:        srv.URL,
		GlobalAPIKey:  testGlobalKey,
		InstanceName:  testInstance,
		InstanceToken: testToken,
	}
	mutate(&cfg)
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

// newTransportClient builds a Client whose requests go through rt, so a test
// can shape responses (large bodies, read failures) without a real server.
func newTransportClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	c, err := New(Config{
		APIURL:        "https://evolution.invalid",
		GlobalAPIKey:  testGlobalKey,
		InstanceName:  testInstance,
		InstanceToken: testToken,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	c.httpClient = &http.Client{Transport: rt}
	return c
}

// jsonResponse builds an HTTP response with a string body.
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// responseWithBody builds an HTTP response around an arbitrary body, so a test
// can observe body close or force a read failure.
func responseWithBody(status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}
}

// assertRequest checks the method, path and headers of a captured request.
func assertRequest(t *testing.T, r *http.Request, wantMethod, wantPath, wantKey string) {
	t.Helper()
	if r.Method != wantMethod {
		t.Errorf("method = %q, want %q", r.Method, wantMethod)
	}
	if r.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
	}
	if got := r.Header.Get(headerAPIKey); got != wantKey {
		t.Errorf("apikey = %q, want %q", got, wantKey)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// decodeRequestBody decodes a JSON request body into a map.
func decodeRequestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	defer r.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	return body
}

// writeJSON writes a JSON response body with 200.
func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
