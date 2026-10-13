package chatwoot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testToken     = "test-token"
	testAccountID = 42
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestClient starts an httptest server and returns a client pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := New(
		Config{APIURL: srv.URL, APIToken: testToken, AccountID: testAccountID},
		WithLogger(discardLogger()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// recordedRequest captures what the client sent.
type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// newRecordingClient returns a client plus a recorder that answers every request
// with status and response.
func newRecordingClient(t *testing.T, status int, response string) (*Client, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		rec.Method = r.Method
		rec.Path = r.URL.Path
		rec.Query = r.URL.Query()
		rec.Header = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		rec.Body = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}
	return newTestClient(t, handler), rec
}

func assertJSONBody(t *testing.T, got []byte, wantJSON string) {
	t.Helper()
	var gotAny, wantAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatalf("request body is not valid json: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(wantJSON), &wantAny); err != nil {
		t.Fatalf("expected json is invalid: %v", err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("request body = %s, want %s", got, wantJSON)
	}
}

func assertAuthHeader(t *testing.T, header http.Header) {
	t.Helper()
	if got := header.Get("api_access_token"); got != testToken {
		t.Errorf("api_access_token = %q, want %q", got, testToken)
	}
}

func TestNew_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "valid", cfg: Config{APIURL: "https://chatwoot.example.com", APIToken: "t", AccountID: 1}},
		{name: "trailing slash valid", cfg: Config{APIURL: "https://chatwoot.example.com/", APIToken: "t", AccountID: 1}},
		{name: "missing url", cfg: Config{APIToken: "t", AccountID: 1}, wantErr: true},
		{name: "relative url", cfg: Config{APIURL: "/api", APIToken: "t", AccountID: 1}, wantErr: true},
		{name: "userinfo", cfg: Config{APIURL: "https://user:pass@host", APIToken: "t", AccountID: 1}, wantErr: true},
		{name: "query", cfg: Config{APIURL: "https://host?x=1", APIToken: "t", AccountID: 1}, wantErr: true},
		{name: "missing token", cfg: Config{APIURL: "https://host", AccountID: 1}, wantErr: true},
		{name: "zero account", cfg: Config{APIURL: "https://host", APIToken: "t"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg, WithLogger(discardLogger()))
			if (err != nil) != tt.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNew_BaseURL(t *testing.T) {
	c, err := New(Config{APIURL: "https://chatwoot.example.com/", APIToken: "t", AccountID: 1}, WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := "https://chatwoot.example.com/api/v1"; c.baseURL != want {
		t.Errorf("baseURL = %q, want %q", c.baseURL, want)
	}
}

func TestClient_StatusClassification(t *testing.T) {
	tests := []struct {
		status    int
		wantKind  ErrorKind
		retryable bool
	}{
		{status: http.StatusBadRequest, wantKind: ErrorKindValidation},
		{status: http.StatusUnauthorized, wantKind: ErrorKindUnauthorized},
		{status: http.StatusForbidden, wantKind: ErrorKindForbidden},
		{status: http.StatusNotFound, wantKind: ErrorKindNotFound},
		{status: http.StatusConflict, wantKind: ErrorKindConflict},
		{status: http.StatusUnprocessableEntity, wantKind: ErrorKindValidation},
		{status: http.StatusTooManyRequests, wantKind: ErrorKindRateLimited, retryable: true},
		{status: http.StatusInternalServerError, wantKind: ErrorKindServer, retryable: true},
		{status: http.StatusServiceUnavailable, wantKind: ErrorKindServer, retryable: true},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, `{"message":"boom"}`)
			})

			_, err := c.GetInbox(context.Background(), 1)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error is not *APIError: %T", err)
			}
			if apiErr.Kind != tt.wantKind {
				t.Errorf("Kind = %v, want %v", apiErr.Kind, tt.wantKind)
			}
			if apiErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tt.status)
			}
			if apiErr.Retryable() != tt.retryable {
				t.Errorf("Retryable() = %v, want %v", apiErr.Retryable(), tt.retryable)
			}
			if IsRetryable(err) != tt.retryable {
				t.Errorf("IsRetryable = %v, want %v", IsRetryable(err), tt.retryable)
			}
			if got := IsNotFound(err); got != (tt.wantKind == ErrorKindNotFound) {
				t.Errorf("IsNotFound = %v", got)
			}
		})
	}
}

func TestClient_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := New(Config{APIURL: srv.URL, APIToken: testToken, AccountID: testAccountID}, WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.Close() // force a dial failure

	_, err = c.ListInboxes(context.Background())
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}
	if !IsRetryable(err) {
		t.Errorf("transport error must be retryable: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorKindTransport {
		t.Fatalf("error = %v, want ErrorKindTransport", err)
	}
}

func TestClient_Timeout(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := c.ListInboxes(ctx)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap context.DeadlineExceeded: %v", err)
	}
	if !IsRetryable(err) {
		t.Errorf("timeout must be retryable: %v", err)
	}
}

func TestClient_MalformedResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "not json")
	})

	_, err := c.ListInboxes(context.Background())
	if err == nil {
		t.Fatal("expected malformed error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorKindMalformed {
		t.Fatalf("error = %v, want ErrorKindMalformed", err)
	}
}

// Every JSON-returning sibling uses the same executor, including multipart.
func TestClient_RequiredJSONResponse(t *testing.T) {
	ctx := context.Background()
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{name: "search contacts", call: func(c *Client) error {
			_, err := c.SearchContactExact(ctx, "15550000001", KindDirect)
			return err
		}},
		{name: "list contacts", call: func(c *Client) error { _, err := c.ListContacts(ctx); return err }},
		{name: "create contact", call: func(c *Client) error {
			_, err := c.CreateContact(ctx, ContactParams{Kind: KindDirect, Identity: "15550000001"})
			return err
		}},
		{name: "list conversations", call: func(c *Client) error { _, err := c.ListConversations(ctx, 7); return err }},
		{name: "create conversation", call: func(c *Client) error { _, err := c.CreateConversation(ctx, 7, 2); return err }},
		{name: "send text", call: func(c *Client) error {
			_, err := c.SendText(ctx, 10, "synthetic", MessageTypeIncoming)
			return err
		}},
		{name: "send attachment", call: func(c *Client) error {
			_, err := c.SendAttachment(ctx, 10, Attachment{Filename: "f.txt", Data: []byte("x")}, "", MessageTypeIncoming)
			return err
		}},
		{name: "list inboxes", call: func(c *Client) error { _, err := c.ListInboxes(ctx); return err }},
		{name: "get inbox", call: func(c *Client) error { _, err := c.GetInbox(ctx, 2); return err }},
		{name: "create inbox", call: func(c *Client) error { _, err := c.CreateInbox(ctx, InboxParams{Name: "synthetic"}); return err }},
		{name: "update inbox", call: func(c *Client) error { _, err := c.UpdateInbox(ctx, 2, InboxParams{}); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, response := range []struct{ name, body string }{
				{name: "empty", body: ""},
				{name: "whitespace", body: " \r\n\t"},
				{name: "null", body: "null"},
				{name: "empty object", body: "{}"},
				{name: "null payload", body: `{"meta":{"has_more":false},"payload":null}`},
			} {
				t.Run(response.name, func(t *testing.T) {
					c, _ := newRecordingClient(t, http.StatusOK, response.body)
					err := operation.call(c)
					var apiErr *APIError
					if !errors.As(err, &apiErr) || apiErr.Kind != ErrorKindMalformed || IsNotFound(err) {
						t.Fatalf("error = %v, want malformed, never not-found", err)
					}
				})
			}
		})
	}
	c, _ := newRecordingClient(t, http.StatusNoContent, "")
	if err := c.UpdateContactName(ctx, 7, "synthetic"); err != nil {
		t.Fatalf("bodyless update must stay valid: %v", err)
	}
}

func TestClient_RedirectDoesNotForwardToken(t *testing.T) {
	for _, injected := range []bool{false, true} {
		name := "default client"
		if injected {
			name = "injected client"
		}
		t.Run(name, func(t *testing.T) {
			var foreignRequests int
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				foreignRequests++
				if r.Header.Get("api_access_token") != "" {
					t.Error("foreign redirect received api token")
				}
				_, _ = io.WriteString(w, `{"payload":[]}`)
			}))
			defer foreign.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertAuthHeader(t, r.Header)
				http.Redirect(w, r, foreign.URL+"/target", http.StatusFound)
			}))
			defer origin.Close()
			opts := []Option{WithLogger(discardLogger())}
			redirectChecks := 0
			var supplied *http.Client
			if injected {
				supplied = &http.Client{
					Timeout:       time.Second,
					CheckRedirect: func(*http.Request, []*http.Request) error { redirectChecks++; return nil },
				}
				opts = append(opts, WithHTTPClient(supplied))
			}
			c, err := New(Config{APIURL: origin.URL, APIToken: testToken, AccountID: testAccountID}, opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.ListInboxes(context.Background()); err == nil {
				t.Fatal("foreign redirect must fail closed")
			}
			if foreignRequests != 0 {
				t.Fatalf("foreign requests = %d, want 0", foreignRequests)
			}
			if supplied != nil {
				if c.http == supplied || c.http.Timeout != supplied.Timeout {
					t.Fatal("injected client must be cloned without losing timeout")
				}
				if err := supplied.CheckRedirect(nil, nil); err != nil || redirectChecks != 1 {
					t.Fatal("injected client's redirect policy was modified or called by the API client")
				}
			}
		})
	}
}

func TestClient_LogsNeverContainSecretsOrPII(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{APIURL: srv.URL, APIToken: testToken, AccountID: testAccountID}, WithLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const phone = "15550000001"
	if _, err := c.SearchContactExact(context.Background(), phone, KindDirect); err == nil {
		t.Fatal("expected error, got nil")
	}

	logged := buf.String()
	if strings.Contains(logged, testToken) {
		t.Error("log leaked the api token")
	}
	if strings.Contains(logged, phone) {
		t.Error("log leaked the search query (PII)")
	}
}

// Severity must follow the method: a failed mutation is an unknown-outcome write
// and logs at Error, while a failed read logs at Warn (MEDIUM-2).
func TestClient_LogLevelForReadsAndWrites(t *testing.T) {
	tests := []struct {
		name      string
		call      func(*Client) error
		wantLevel string
	}{
		{
			name: "failed mutation logs at error",
			call: func(c *Client) error {
				_, err := c.SendText(context.Background(), 10, "synthetic", MessageTypeIncoming)
				return err
			},
			wantLevel: `"level":"ERROR"`,
		},
		{
			name: "retryable read logs at warn",
			call: func(c *Client) error {
				_, err := c.ListInboxes(context.Background())
				return err
			},
			wantLevel: `"level":"WARN"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"message":"boom"}`)
			}))
			t.Cleanup(srv.Close)

			c, err := New(Config{APIURL: srv.URL, APIToken: testToken, AccountID: testAccountID}, WithLogger(logger))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := tt.call(c); err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(buf.String(), tt.wantLevel) {
				t.Errorf("log = %q, want level %s", buf.String(), tt.wantLevel)
			}
		})
	}
}
