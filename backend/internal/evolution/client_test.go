package evolution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid",
			cfg:  Config{APIURL: "https://evo.example.com", GlobalAPIKey: "key"},
		},
		{
			name:    "missing url",
			cfg:     Config{GlobalAPIKey: "key"},
			wantErr: true,
		},
		{
			name:    "missing global key",
			cfg:     Config{APIURL: "https://evo.example.com"},
			wantErr: true,
		},
		{
			name:    "relative url rejected",
			cfg:     Config{APIURL: "/api", GlobalAPIKey: "key"},
			wantErr: true,
		},
		{
			name:    "userinfo rejected",
			cfg:     Config{APIURL: "https://user:pass@evo.example.com", GlobalAPIKey: "key"},
			wantErr: true,
		},
		{
			name:    "query rejected",
			cfg:     Config{APIURL: "https://evo.example.com?x=1", GlobalAPIKey: "key"},
			wantErr: true,
		},
		{
			name:    "fragment rejected",
			cfg:     Config{APIURL: "https://evo.example.com#x", GlobalAPIKey: "key"},
			wantErr: true,
		},
		{
			name:    "empty query rejected",
			cfg:     Config{APIURL: "https://evo.example.com?", GlobalAPIKey: "key"},
			wantErr: true,
		},
		{
			name:    "empty fragment rejected",
			cfg:     Config{APIURL: "https://evo.example.com#", GlobalAPIKey: "key"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewTrimsTrailingSlashAndDefaultsPath(t *testing.T) {
	c, err := New(Config{APIURL: "https://evo.example.com/", GlobalAPIKey: "key"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.apiURL != "https://evo.example.com" {
		t.Errorf("apiURL = %q, want trimmed base", c.apiURL)
	}
	if c.markReadPath != defaultMarkReadPath {
		t.Errorf("markReadPath = %q, want %q", c.markReadPath, defaultMarkReadPath)
	}
	if c.logger == nil {
		t.Error("logger = nil, want a non-nil default logger")
	}
}

func TestNormalizePathRejectsQueryAndFragment(t *testing.T) {
	for _, path := range []string{"/custom?x=1", "/custom#frag", "/custom?", "/custom#", "/bad%escape"} {
		t.Run(path, func(t *testing.T) {
			for _, field := range []string{"mark read", "download media"} {
				t.Run(field, func(t *testing.T) {
					cfg := Config{APIURL: "https://evo.example.com", GlobalAPIKey: testGlobalKey}
					if field == "mark read" {
						cfg.MarkReadPath = path
					} else {
						cfg.DownloadMediaPath = path
					}
					if _, err := New(cfg); err == nil {
						t.Fatal("New() accepted invalid configured path")
					} else if strings.Contains(err.Error(), path) {
						t.Fatal("New() echoed invalid configured path")
					}
				})
			}
		})
	}
}

func TestHealthCheck(t *testing.T) {
	t.Run("sends admin key to /server/ok", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assertRequest(t, r, http.MethodGet, "/server/ok", testGlobalKey)
			writeJSON(w, `{"status":"ok"}`)
		})
		if err := c.HealthCheck(t.Context()); err != nil {
			t.Fatalf("HealthCheck() error = %v", err)
		}
	})

	t.Run("non-2xx returns typed error with status only", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		})
		err := c.HealthCheck(t.Context())
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) {
			t.Fatalf("HealthCheck() error = %v, want *HTTPError", err)
		}
		if httpErr.StatusCode != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", httpErr.StatusCode)
		}
		if strings.Contains(err.Error(), "boom") {
			t.Errorf("error leaked upstream body: %v", err)
		}
	})
}

// TestRedirectDoesNotForwardCredential proves the client never follows a
// redirect, so the custom `apikey` header cannot be replayed to another host.
func TestRedirectDoesNotForwardCredential(t *testing.T) {
	var (
		mu         sync.Mutex
		secondHits int
		secondKey  string
	)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		secondHits++
		secondKey = r.Header.Get(headerAPIKey)
		mu.Unlock()
		writeJSON(w, `{"ok":true}`)
	}))
	t.Cleanup(second.Close)

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/stolen", http.StatusFound)
	}))
	t.Cleanup(first.Close)

	c, err := New(Config{
		APIURL:        first.URL,
		GlobalAPIKey:  testGlobalKey,
		InstanceToken: testToken,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = c.HealthCheck(t.Context())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound {
		t.Fatalf("HealthCheck() error = %v, want HTTPError 302", err)
	}

	mu.Lock()
	hits, key := secondHits, secondKey
	mu.Unlock()
	if hits != 0 {
		t.Fatalf("redirect target received %d request(s), want 0", hits)
	}
	if key != "" {
		t.Fatalf("redirect target received apikey %q, want none", key)
	}

	// Replacing the HTTP client must not bypass the request boundary's policy.
	for _, scope := range []credentialScope{scopeAdmin, scopeInstance} {
		custom := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			t.Error("custom redirect callback was invoked")
			return nil
		}}
		c.httpClient = custom
		_, err := c.doJSON(t.Context(), http.MethodGet, "/server/ok", scope, nil)
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound {
			t.Errorf("custom client error = %v, want HTTPError 302", err)
		}
		if custom.Timeout != time.Second || custom.CheckRedirect == nil {
			t.Error("caller-owned client configuration was mutated")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if secondHits != 0 {
		t.Errorf("custom client followed redirect %d times", secondHits)
	}
}

func TestHTTPErrorStatuses(t *testing.T) {
	for _, status := range []int{401, 403, 404, 422, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"denied"}`))
			})
			err := c.HealthCheck(t.Context())
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("HealthCheck() error = %v, want *HTTPError", err)
			}
			if httpErr.StatusCode != status {
				t.Errorf("status = %d, want %d", httpErr.StatusCode, status)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Errorf("error %q does not mention status %d", err, status)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// countingTransport records how many requests reached the transport.
type countingTransport struct {
	mu    sync.Mutex
	calls int
	fn    roundTripFunc
}

func (t *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return t.fn(r)
}

func (t *countingTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type failingBody struct{ err error }

func (b failingBody) Read([]byte) (int, error) { return 0, b.err }
func (b failingBody) Close() error             { return nil }

// trackingBody reports whether the client closed it.
type trackingBody struct {
	r      io.Reader
	closed bool
}

func (b *trackingBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *trackingBody) Close() error               { b.closed = true; return nil }

func TestTransportAndReadErrorsOmitPrivateDetails(t *testing.T) {
	for _, stage := range []string{"transport", "read"} {
		t.Run(stage, func(t *testing.T) {
			c, err := New(Config{
				APIURL:       "https://evo.example.com/synthetic-private-path",
				GlobalAPIKey: testGlobalKey, InstanceToken: testToken,
			})
			if err != nil {
				t.Fatal(err)
			}
			c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				key := r.Header.Get(headerAPIKey)
				c.setInstanceToken("rotated-token")
				failure := &url.Error{
					Op: "Get", URL: r.URL.String() + "?apikey=" + key,
					Err: fmt.Errorf("synthetic private message %s: %w", key, context.DeadlineExceeded),
				}
				if stage == "transport" {
					return nil, failure
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: failingBody{err: failure}}, nil
			})}
			_, err = c.GetInstanceStatus(t.Context())
			if err == nil {
				t.Fatal("expected upstream error")
			}
			for _, private := range []string{testToken, "synthetic-private-path", "synthetic private message", "apikey="} {
				if strings.Contains(err.Error(), private) {
					t.Errorf("error leaked %q", private)
				}
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Error("error lost deadline classification")
			}
			var networkErr net.Error
			if !errors.As(err, &networkErr) || !networkErr.Timeout() {
				t.Error("error lost net.Error timeout classification")
			}
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				t.Error("error chain exposes the private url.Error")
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	// Cleanups run LIFO: release the handler before the server is closed.
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err := c.HealthCheck(ctx)
	if err == nil {
		t.Fatal("HealthCheck() error = nil, want timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestMalformedJSONResponse(t *testing.T) {
	t.Run("list parser errors opaquely", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, `this is not json`)
		})
		_, err := c.ResolveInstanceToken(t.Context())
		if !errors.Is(err, ErrMalformedResponse) {
			t.Fatalf("ResolveInstanceToken() error = %v, want ErrMalformedResponse", err)
		}
	})

	t.Run("send reports unconfirmed for malformed body", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, `this is not json`)
		})
		result, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{})
		if !errors.Is(err, ErrUnconfirmedSend) || !errors.Is(err, ErrMalformedResponse) {
			t.Fatalf("SendText() error = %v, want unconfirmed malformed", err)
		}
		if result.MessageID != "" {
			t.Errorf("MessageID = %q, want empty", result.MessageID)
		}
	})
}

// TestClientConcurrentUse exercises the "safe for concurrent use" contract:
// ResolveInstanceToken mutates the stored token while sends and status reads run
// against it. Run under -race to prove the mutex protects instanceToken.
func TestClientConcurrentUse(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	c := newTestClientWithToken(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/instance/all":
			writeJSON(w, `{"data":[{"name":"synthetic-instance","token":"resolved-token"}]}`)
		case "/instance/status":
			writeJSON(w, `{"state":"open"}`)
		case "/send/text":
			writeJSON(w, `{"data":{"Info":{"ID":"x"}}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}, "")

	const n = 25
	var wg sync.WaitGroup
	errCh := make(chan error, 3*n)
	for i := 0; i < n; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			if _, err := c.ResolveInstanceToken(t.Context()); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := c.GetInstanceStatus(t.Context()); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{ID: "id"}); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent call error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/instance/all", "/instance/status", "/send/text"} {
		if hits[path] != n {
			t.Errorf("%s hits = %d, want %d", path, hits[path], n)
		}
	}
}

// assertOpaqueError fails if any private marker is reachable through the
// error's string forms, its JSON serialization, or its Unwrap chain, or if a
// raw decoder/transport cause is exposed.
func assertOpaqueError(t *testing.T, err error, markers ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("assertOpaqueError() got nil error")
	}
	forms := []string{
		err.Error(),
		fmt.Sprintf("%v", err),
		fmt.Sprintf("%+v", err),
		fmt.Sprintf("%q", err),
	}
	if raw, marshalErr := json.Marshal(err); marshalErr == nil {
		forms = append(forms, string(raw))
	}
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		forms = append(forms, cur.Error())
	}
	for _, form := range forms {
		for _, marker := range markers {
			if strings.Contains(form, marker) {
				t.Errorf("error form %q leaked %q", form, marker)
			}
		}
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		t.Error("error chain exposes *json.UnmarshalTypeError")
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		t.Error("error chain exposes *json.SyntaxError")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		t.Error("error chain exposes *url.Error")
	}
}

// TestEvolutionOpaqueErrors covers ADR-0008 §1/§2: every decode boundary
// returns a fixed category and no upstream content survives Error(),
// formatting, JSON serialization or the public error chain.
func TestEvolutionOpaqueErrors(t *testing.T) {
	const (
		fieldMarker = "synthetic-token"
		numMarker   = "123456789123456789e9999"
		jidMarker   = "120363000000000001@g.us"
		textMarker  = "synthetic-private-message"
	)

	t.Run("status overflow key", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, `{"`+fieldMarker+`":1e9999}`)
		})
		_, err := c.GetInstanceStatus(t.Context())
		if !errors.Is(err, ErrMalformedResponse) {
			t.Fatalf("error = %v, want ErrMalformedResponse", err)
		}
		assertOpaqueError(t, err, fieldMarker, "1e9999")
	})

	t.Run("status numeric secret", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, `{"status":`+numMarker+`}`)
		})
		_, err := c.GetInstanceStatus(t.Context())
		if !errors.Is(err, ErrMalformedResponse) {
			t.Fatalf("error = %v, want ErrMalformedResponse", err)
		}
		assertOpaqueError(t, err, numMarker)
	})

	t.Run("instance list overflow", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, `{"data":[{"name":1e9999}]}`)
		})
		_, err := c.ResolveInstanceToken(t.Context())
		if !errors.Is(err, ErrMalformedResponse) {
			t.Fatalf("error = %v, want ErrMalformedResponse", err)
		}
		assertOpaqueError(t, err, "1e9999")
	})

	t.Run("contacts overflow", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, `{"data":[{"`+fieldMarker+`":1e9999}]}`)
		})
		_, err := c.FetchContacts(t.Context())
		if !errors.Is(err, ErrMalformedResponse) {
			t.Fatalf("error = %v, want ErrMalformedResponse", err)
		}
		assertOpaqueError(t, err, fieldMarker)
	})

	t.Run("http error keeps status only", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"%s %s"}`, textMarker, jidMarker)
		})
		err := c.HealthCheck(t.Context())
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("error = %v, want HTTPError 400", err)
		}
		serialized, marshalErr := json.Marshal(httpErr)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if string(serialized) != `{"StatusCode":400}` {
			t.Errorf("HTTPError serialization = %s, want only StatusCode", serialized)
		}
		assertOpaqueError(t, err, textMarker, jidMarker)
	})
}

// TestEvolutionBodyLimits covers ADR-0008 §4: finite outbound/response budgets,
// limit+1 reads, and no truncated prefix treated as success.
func TestEvolutionBodyLimits(t *testing.T) {
	t.Run("outbound below budget accepted", func(t *testing.T) {
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"data":{"Info":{"ID":"x"}}}`), nil
		}}
		c := newTransportClient(t, rt)
		text := strings.Repeat("a", maxRequestBytes-24)
		if _, err := c.SendText(t.Context(), "", text, SendOptions{}); err != nil {
			t.Fatalf("SendText() error = %v", err)
		}
		if rt.count() != 1 {
			t.Errorf("transport calls = %d, want 1", rt.count())
		}
	})

	t.Run("outbound at budget accepted", func(t *testing.T) {
		var gotLen int
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			gotLen = len(body)
			return jsonResponse(http.StatusOK, `{"data":{"Info":{"ID":"x"}}}`), nil
		}}
		c := newTransportClient(t, rt)
		text := strings.Repeat("a", maxRequestBytes-23)
		if _, err := c.SendText(t.Context(), "", text, SendOptions{}); err != nil {
			t.Fatalf("SendText() error = %v", err)
		}
		if gotLen != maxRequestBytes {
			t.Errorf("encoded request = %d bytes, want %d", gotLen, maxRequestBytes)
		}
	})

	t.Run("outbound one byte over rejected before io", func(t *testing.T) {
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{}`), nil
		}}
		c := newTransportClient(t, rt)
		text := strings.Repeat("a", maxRequestBytes-22)
		_, err := c.SendText(t.Context(), "", text, SendOptions{})
		if !errors.Is(err, ErrRequestTooLarge) {
			t.Fatalf("SendText() error = %v, want ErrRequestTooLarge", err)
		}
		if errors.Is(err, ErrUnconfirmedSend) {
			t.Error("local rejection marked unconfirmed, want known not sent")
		}
		if rt.count() != 0 {
			t.Errorf("transport calls = %d, want 0", rt.count())
		}
	})

	t.Run("escaped expansion rejected before io", func(t *testing.T) {
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{}`), nil
		}}
		c := newTransportClient(t, rt)
		// Each NUL escapes to 6 bytes; the raw string is under budget but the
		// encoded JSON is not.
		text := strings.Repeat("\x00", maxRequestBytes/6+64)
		_, err := c.SendText(t.Context(), "", text, SendOptions{})
		if !errors.Is(err, ErrRequestTooLarge) {
			t.Fatalf("SendText() error = %v, want ErrRequestTooLarge", err)
		}
		if rt.count() != 0 {
			t.Errorf("transport calls = %d, want 0", rt.count())
		}
	})

	t.Run("success response at budget accepted", func(t *testing.T) {
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, strings.Repeat("a", maxResponseBytes)), nil
		}}
		c := newTransportClient(t, rt)
		if err := c.HealthCheck(t.Context()); err != nil {
			t.Fatalf("HealthCheck() error = %v, want nil", err)
		}
	})

	t.Run("success response one over rejected", func(t *testing.T) {
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, strings.Repeat("a", maxResponseBytes+1)), nil
		}}
		c := newTransportClient(t, rt)
		err := c.HealthCheck(t.Context())
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("HealthCheck() error = %v, want ErrResponseTooLarge", err)
		}
	})

	t.Run("error response at budget accepted", func(t *testing.T) {
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusBadRequest, strings.Repeat("a", maxErrorBytes)), nil
		}}
		c := newTransportClient(t, rt)
		err := c.HealthCheck(t.Context())
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("HealthCheck() error = %v, want HTTPError 400", err)
		}
	})

	t.Run("error response one over rejected", func(t *testing.T) {
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusBadRequest, strings.Repeat("a", maxErrorBytes+1)), nil
		}}
		c := newTransportClient(t, rt)
		err := c.HealthCheck(t.Context())
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("HealthCheck() error = %v, want ErrResponseTooLarge", err)
		}
	})

	t.Run("oversized send is not a success prefix", func(t *testing.T) {
		prefix := `{"data":{"Info":{"ID":"synthetic-id"}}}`
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			body := prefix + strings.Repeat(" ", maxResponseBytes-len(prefix)) + `{"x":1}`
			return jsonResponse(http.StatusOK, body), nil
		}}
		c := newTransportClient(t, rt)
		result, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{})
		if !errors.Is(err, ErrResponseTooLarge) || !errors.Is(err, ErrUnconfirmedSend) {
			t.Fatalf("SendText() error = %v, want oversized unconfirmed", err)
		}
		if result.MessageID != "" {
			t.Errorf("MessageID = %q, want empty", result.MessageID)
		}
	})

	t.Run("read failure is opaque and body closed", func(t *testing.T) {
		body := &trackingBody{r: failingBody{err: errors.New("synthetic-read-failure")}}
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			return responseWithBody(http.StatusOK, body), nil
		}}
		c := newTransportClient(t, rt)
		_, err := c.GetInstanceStatus(t.Context())
		if err == nil {
			t.Fatal("expected read failure")
		}
		if strings.Contains(err.Error(), "synthetic-read-failure") {
			t.Errorf("error leaked read cause: %v", err)
		}
		if !body.closed {
			t.Error("response body was not closed")
		}
	})
}

// TestEvolutionSafeCallLog covers ADR-0008 §6: one fixed-attribute log event per
// call with no PII, and no upstream-call log for a local rejection.
func TestEvolutionSafeCallLog(t *testing.T) {
	const (
		rotatedToken = "synthetic-rotated-token-9f3a"
		jidMarker    = "120363000000000001@g.us"
		textMarker   = "synthetic-private-message"
	)

	t.Run("fixed attributes only", func(t *testing.T) {
		var buf bytes.Buffer
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"%s %s apikey %s"}`, textMarker, jidMarker, rotatedToken)
		})
		c.logger = slog.New(slog.NewJSONHandler(&buf, nil))

		if err := c.HealthCheck(t.Context()); err == nil {
			t.Fatal("HealthCheck() error = nil, want error")
		}
		logged := buf.String()
		for _, want := range []string{`"operation":"health"`, `"status":400`, `"category":"http"`, `"duration_ms":`} {
			if !strings.Contains(logged, want) {
				t.Errorf("log missing %s: %s", want, logged)
			}
		}
		for _, private := range []string{testGlobalKey, testToken, rotatedToken, jidMarker, textMarker, "apikey"} {
			if strings.Contains(logged, private) {
				t.Errorf("log leaked %q: %s", private, logged)
			}
		}
	})

	t.Run("local rejection has no upstream-call log", func(t *testing.T) {
		var buf bytes.Buffer
		rt := &countingTransport{fn: func(r *http.Request) (*http.Response, error) {
			t.Error("transport called for a locally rejected request")
			return jsonResponse(http.StatusOK, `{}`), nil
		}}
		c := newTransportClient(t, rt)
		c.logger = slog.New(slog.NewJSONHandler(&buf, nil))

		_, err := c.SendText(t.Context(), "5511900000001", strings.Repeat("a", maxRequestBytes+1), SendOptions{})
		if err == nil {
			t.Fatal("expected local rejection")
		}
		if buf.Len() != 0 {
			t.Errorf("local rejection logged: %s", buf.String())
		}
	})
}

// TestEvolutionUnknownSend covers ADR-0008 §5: exactly one attempt per invocation
// and no confirmed-sent result for any unknown outcome.
func TestEvolutionUnknownSend(t *testing.T) {
	assertUnconfirmed := func(t *testing.T, result SendResult, err error, wantIs ...error) {
		t.Helper()
		if err == nil {
			t.Fatal("send returned nil error, want unconfirmed")
		}
		if result.MessageID != "" {
			t.Errorf("MessageID = %q, want empty", result.MessageID)
		}
		if !errors.Is(err, ErrUnconfirmedSend) {
			t.Errorf("error = %v, want ErrUnconfirmedSend", err)
		}
		for _, want := range wantIs {
			if !errors.Is(err, want) {
				t.Errorf("error = %v, want errors.Is %v", err, want)
			}
		}
	}

	t.Run("connection closed after consume", func(t *testing.T) {
		var calls int32
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			_, _ = io.Copy(io.Discard, r.Body)
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("response writer cannot hijack")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				return
			}
			_ = conn.Close()
		})
		result, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{ID: "bridge-id"})
		assertUnconfirmed(t, result, err)
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		var calls int32
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			<-release
		})
		// Cleanups run LIFO: release the handler before the server is closed.
		t.Cleanup(func() { close(release) })

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		result, err := c.SendText(ctx, "5511900000001", "hi", SendOptions{})
		assertUnconfirmed(t, result, err)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want context.DeadlineExceeded", err)
		}
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})

	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run("status "+strconv.Itoa(status), func(t *testing.T) {
			var calls int32
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(status)
			})
			result, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{ID: "bridge-id"})
			assertUnconfirmed(t, result, err)
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != status {
				t.Errorf("error = %v, want HTTPError %d reachable", err, status)
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Errorf("attempts = %d, want 1", got)
			}
		})
	}

	t.Run("oversized 2xx", func(t *testing.T) {
		var calls int32
		prefix := `{"data":{"Info":{"ID":"synthetic-id"}}}`
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			_, _ = io.WriteString(w, prefix+strings.Repeat(" ", maxResponseBytes-len(prefix))+`{"x":1}`)
		})
		result, err := c.SendMedia(t.Context(), "5511900000001", "image", "https://x", "", "", SendOptions{})
		assertUnconfirmed(t, result, err, ErrResponseTooLarge)
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})

	t.Run("malformed 2xx", func(t *testing.T) {
		var calls int32
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			writeJSON(w, `not json`)
		})
		result, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{})
		assertUnconfirmed(t, result, err, ErrMalformedResponse)
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})

	t.Run("2xx without id", func(t *testing.T) {
		var calls int32
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			writeJSON(w, `{"message":"success"}`)
		})
		result, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{})
		assertUnconfirmed(t, result, err)
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})
}
