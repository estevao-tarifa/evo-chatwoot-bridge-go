package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/observability"
)

func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return observability.NewLogger(&buf, slog.LevelDebug), &buf
}

func doRequest(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertJSONField(t *testing.T, rec *httptest.ResponseRecorder, key string, want any) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not valid json: %v (%s)", err, rec.Body.String())
	}
	if got := payload[key]; got != want {
		t.Errorf("response[%q] = %v, want %v", key, got, want)
	}
}

func TestNewServer(t *testing.T) {
	srv := NewServer(Options{Addr: ":0"})
	if srv.Handler == nil {
		t.Fatal("server handler is nil")
	}
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("timeouts must be explicit and positive: read-header=%v read=%v write=%v idle=%v",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}

func TestRouting(t *testing.T) {
	logger, _ := testLogger()
	handler := newHandler(Options{Logger: logger})

	tests := []struct {
		name     string
		method   string
		path     string
		wantCode int
	}{
		{name: "healthz", method: http.MethodGet, path: "/healthz", wantCode: http.StatusOK},
		{name: "readyz without wiring", method: http.MethodGet, path: "/readyz", wantCode: http.StatusServiceUnavailable},
		{name: "meta", method: http.MethodGet, path: "/api/v1/meta", wantCode: http.StatusOK},
		{name: "unknown path", method: http.MethodGet, path: "/nope", wantCode: http.StatusNotFound},
		{name: "healthz trailing slash", method: http.MethodGet, path: "/healthz/", wantCode: http.StatusNotFound},
		{name: "method not allowed", method: http.MethodPost, path: "/healthz", wantCode: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(handler, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.wantCode)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	logger, _ := testLogger()
	handler := newHandler(Options{Logger: logger})

	rec := doRequest(handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	assertJSONField(t, rec, "status", "ok")
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		ready      ReadinessFunc
		wantCode   int
		wantReason string
	}{
		{
			name:       "database not wired",
			wantCode:   http.StatusServiceUnavailable,
			wantReason: "database not wired",
		},
		{
			name:     "dependency healthy",
			ready:    func(context.Context) error { return nil },
			wantCode: http.StatusOK,
		},
		{
			name:       "dependency failing",
			ready:      func(context.Context) error { return errors.New("boom: connection refused") },
			wantCode:   http.StatusServiceUnavailable,
			wantReason: "dependency check failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := testLogger()
			handler := newHandler(Options{Logger: logger, Ready: tt.ready})

			rec := doRequest(handler, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("GET /readyz = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantReason != "" {
				assertJSONField(t, rec, "reason", tt.wantReason)
			}
			if strings.Contains(rec.Body.String(), "boom") {
				t.Errorf("internal error leaked to the client: %s", rec.Body.String())
			}
			if tt.ready != nil && tt.wantCode != http.StatusOK && !strings.Contains(buf.String(), "readiness check failed") {
				t.Errorf("readiness failure was not logged: %s", buf.String())
			}
		})
	}
}

func TestMeta(t *testing.T) {
	t.Run("reports build info", func(t *testing.T) {
		logger, _ := testLogger()
		handler := newHandler(Options{Logger: logger, Version: "1.2.3", Commit: "abc1234"})

		rec := doRequest(handler, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/meta = %d, want 200", rec.Code)
		}
		assertJSONField(t, rec, "name", serviceName)
		assertJSONField(t, rec, "version", "1.2.3")
		assertJSONField(t, rec, "commit", "abc1234")
	})

	t.Run("placeholders when build info is absent", func(t *testing.T) {
		logger, _ := testLogger()
		handler := newHandler(Options{Logger: logger})

		rec := doRequest(handler, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
		assertJSONField(t, rec, "version", "dev")
		assertJSONField(t, rec, "commit", "none")
	})
}

func TestCorrelationIDMiddleware(t *testing.T) {
	t.Run("generates an id when absent", func(t *testing.T) {
		logger, buf := testLogger()
		handler := newHandler(Options{Logger: logger})

		rec := doRequest(handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		id := rec.Header().Get(correlationHeader)
		if id == "" {
			t.Fatal("response is missing the correlation id header")
		}
		if !strings.Contains(buf.String(), id) {
			t.Errorf("access log does not carry correlation id %q: %s", id, buf.String())
		}

		second := doRequest(handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if second.Header().Get(correlationHeader) == id {
			t.Error("generated ids must not repeat across requests")
		}
	})

	t.Run("echoes a valid inbound id", func(t *testing.T) {
		logger, buf := testLogger()
		handler := newHandler(Options{Logger: logger})

		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set(correlationHeader, "req-abc-123")
		rec := doRequest(handler, req)

		if got := rec.Header().Get(correlationHeader); got != "req-abc-123" {
			t.Errorf("correlation id = %q, want req-abc-123", got)
		}
		if !strings.Contains(buf.String(), "req-abc-123") {
			t.Errorf("access log does not carry the inbound id: %s", buf.String())
		}
	})

	t.Run("replaces an injected id", func(t *testing.T) {
		logger, buf := testLogger()
		handler := newHandler(Options{Logger: logger})

		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set(correlationHeader, "bad id\r\nInjected: x")
		rec := doRequest(handler, req)

		got := rec.Header().Get(correlationHeader)
		if got == "" || strings.ContainsAny(got, " \r\n") {
			t.Errorf("injected id was not replaced: %q", got)
		}
		if strings.Contains(buf.String(), "Injected") {
			t.Errorf("injected header value leaked into logs: %s", buf.String())
		}
	})
}

func TestAccessLogDoesNotLeakSecrets(t *testing.T) {
	logger, buf := testLogger()
	handler := newHandler(Options{Logger: logger})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta?token=query-secret", strings.NewReader("body-secret"))
	req.Header.Set("Authorization", "Bearer header-secret")
	req.Header.Set("Cookie", "session=cookie-secret")
	doRequest(handler, req)

	logs := buf.String()
	secrets := []string{"query-secret", "body-secret", "header-secret", "cookie-secret", "Authorization", "Cookie"}
	for _, secret := range secrets {
		if strings.Contains(logs, secret) {
			t.Errorf("access log leaked %q: %s", secret, logs)
		}
	}
	if !strings.Contains(logs, `"path":"/api/v1/meta"`) {
		t.Errorf("access log is missing the path: %s", logs)
	}
	if !strings.Contains(logs, `"status":200`) {
		t.Errorf("access log is missing the status: %s", logs)
	}
}

func TestAccessLogHeadBytes(t *testing.T) {
	logger, buf := testLogger()
	handler := newHandler(Options{Logger: logger})

	// GET /healthz also matches HEAD; net/http suppresses the body on the wire
	// while the handler still writes it, so the log must report 0 bytes.
	doRequest(handler, httptest.NewRequest(http.MethodHead, "/healthz", nil))

	if !strings.Contains(buf.String(), `"bytes":0`) {
		t.Errorf("HEAD response logged transmitted bytes, want 0: %s", buf.String())
	}
}

func TestRecoverPanic(t *testing.T) {
	t.Run("panicking handler returns 500 without leaking the panic", func(t *testing.T) {
		logger, buf := testLogger()
		handler := recoverPanic(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}))

		rec := doRequest(handler, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("panic value leaked to the client: %s", rec.Body.String())
		}
		if !strings.Contains(buf.String(), "panic recovered") || !strings.Contains(buf.String(), "boom") {
			t.Errorf("panic was not logged with details: %s", buf.String())
		}
	})

	t.Run("http.ErrAbortHandler is re-panicked", func(t *testing.T) {
		logger, _ := testLogger()
		handler := recoverPanic(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))

		defer func() {
			if r := recover(); r != http.ErrAbortHandler {
				t.Errorf("recovered %v, want http.ErrAbortHandler", r)
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		t.Fatal("handler did not re-panic")
	})
}

func TestBodyLimit(t *testing.T) {
	logger, _ := testLogger()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := bodyLimit(logger)(inner)

	t.Run("declared body over the limit is rejected before reading", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, maxBodyBytes+1)))
		rec := doRequest(handler, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", rec.Code)
		}
	})

	t.Run("chunked body over the limit fails while reading", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, maxBodyBytes+1)))
		req.ContentLength = -1 // simulate chunked encoding
		rec := doRequest(handler, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", rec.Code)
		}
	})

	t.Run("body under the limit passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small"))
		rec := doRequest(handler, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

func TestJSONResponseHeaders(t *testing.T) {
	logger, _ := testLogger()
	handler := newHandler(Options{Logger: logger})

	for _, path := range []string{"/healthz", "/readyz", "/api/v1/meta"} {
		t.Run(path, func(t *testing.T) {
			rec := doRequest(handler, httptest.NewRequest(http.MethodGet, path, nil))
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

func TestCorrelationIDConcurrentUniqueness(t *testing.T) {
	// io.Discard is concurrency-safe, unlike the shared bytes.Buffer used by
	// the sequential tests.
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := newHandler(Options{Logger: logger})

	const requests = 64
	ids := make([]string, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := doRequest(handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			ids[i] = rec.Header().Get(correlationHeader)
		}()
	}
	wg.Wait()

	seen := make(map[string]struct{}, requests)
	for i, id := range ids {
		if observability.SanitizeCorrelationID(id) != id {
			t.Errorf("request %d got an unusable correlation id %q", i, id)
		}
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate correlation id %q", id)
		}
		seen[id] = struct{}{}
	}
}

// TestSanitizePath is the direct unit table for the CRITICAL token-masking
// helper (ADR-0006 Amendment 2): the token must be masked on every path shape,
// including duplicate slashes, dot segments, case-varied anchors and prefixed
// paths. LOW-2 hardening: everything after the system segment is masked, so a
// token in the connection-id or the system slot is never logged verbatim.
func TestSanitizePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "masks the token", path: "/api/webhooks/evolution/1/secret", want: "/api/webhooks/evolution/***"},
		{name: "masks a multi-segment token", path: "/api/webhooks/evolution/1/a/b", want: "/api/webhooks/evolution/***"},
		{name: "masks a leading duplicate slash", path: "//api/webhooks/evolution/1/secret", want: "//api/webhooks/evolution/***"},
		{name: "masks a case-varied anchor", path: "/API/webhooks/evolution/1/secret", want: "/API/webhooks/evolution/***"},
		{name: "masks a prefixed path", path: "/x/api/webhooks/evolution/1/secret", want: "/x/api/webhooks/evolution/***"},
		{name: "masks an inner duplicate slash", path: "/api/webhooks/evolution/1//secret", want: "/api/webhooks/evolution/***"},
		{name: "masks a dot segment", path: "/api/webhooks/evolution/1/./secret", want: "/api/webhooks/evolution/***"},
		{name: "masks a dot-dot segment", path: "/api/webhooks/evolution/1/foo/../secret", want: "/api/webhooks/evolution/***"},
		{name: "masks the chatwoot surface too", path: "/api/webhooks/chatwoot/1/secret", want: "/api/webhooks/chatwoot/***"},
		{name: "masks an empty token segment", path: "/api/webhooks/evolution/1/", want: "/api/webhooks/evolution/***"},
		{name: "masks the connection-id slot on the tokenless form", path: "/api/webhooks/evolution/1", want: "/api/webhooks/evolution/***"},
		{name: "masks a token in the connection-id slot", path: "/api/webhooks/evolution/SECRET-TOKEN", want: "/api/webhooks/evolution/***"},
		{name: "masks a token in the system slot", path: "/api/webhooks/SECRET-TOKEN/1/x", want: "/api/webhooks/***"},
		{name: "masks a token in the system slot with no rest", path: "/api/webhooks/SECRET-TOKEN", want: "/api/webhooks/***"},
		{name: "leaves the bare known system unchanged", path: "/api/webhooks/evolution", want: "/api/webhooks/evolution"},
		{name: "leaves another api path unchanged", path: "/api/v1/meta", want: "/api/v1/meta"},
		{name: "leaves an unrelated path unchanged", path: "/healthz", want: "/healthz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizePath(tt.path); got != tt.want {
				t.Errorf("sanitizePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestSanitizePathFirstAnchorWins is the regression for the LOW defect where the
// exact-match fast path in webhookAnchorIndex returned the FIRST EXACT anchor,
// which can be LATER than the first case-insensitive occurrence. With an earlier
// case-varied anchor and a later exact anchor the earlier token segment was
// logged verbatim. The anchor index must always be the first case-insensitive
// occurrence, so the earlier segment is masked.
func TestSanitizePathFirstAnchorWins(t *testing.T) {
	const token = "SECRETTOKEN0123456789abcdef"
	path := "/API/webhooks/evolution/1/" + token + "/api/webhooks/evolution/2/x"

	t.Run("masks the earlier token segment", func(t *testing.T) {
		got := sanitizePath(path) // must never panic
		if strings.Contains(got, token) {
			t.Fatalf("sanitizePath leaked the earlier token: %q", got)
		}
		if want := "/API/webhooks/evolution/***"; got != want {
			t.Errorf("sanitizePath(%q) = %q, want %q", path, got, want)
		}
	})

	t.Run("does not panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("sanitizePath panicked: %v", r)
			}
		}()
		sanitizePath(path)
	})
}

// TestSanitizePathUnicodeByteExpansion is the regression for the HIGH defect:
// strings.ToLower is not byte-length preserving — U+023A and U+023E each
// lowercase to a 3-byte rune — so an anchor index computed on the lowered
// string slices the ORIGINAL at the wrong offset. With enough runes before the
// anchor the mask window shifts and the token is returned unchanged (a leak);
// with more, the shifted slice runs past the end and panics inside the access
// log, outside recoverPanic. The masker must be byte-length preserving.
func TestSanitizePathUnicodeByteExpansion(t *testing.T) {
	const token = "SECRETTOKEN0123456789abcdef"
	expanding := []rune{0x023A, 0x023E}

	for _, r := range expanding {
		for n := 1; n <= 80; n++ {
			prefix := strings.Repeat(string(r), n)
			paths := map[string]string{
				"before anchor": prefix + "/api/webhooks/evolution/1/" + token,
				"after anchor":  "/api/webhooks/" + prefix + "evolution/1/" + token,
				"case-varied":   prefix + "/API/webhooks/evolution/1/" + token,
			}
			for name, path := range paths {
				t.Run(fmt.Sprintf("%U/n=%d/%s", r, n, name), func(t *testing.T) {
					got := sanitizePath(path) // must never panic
					if strings.Contains(got, token) {
						t.Fatalf("sanitizePath leaked the token: %q", got)
					}
					if !strings.Contains(got, "***") {
						t.Fatalf("sanitizePath did not mask the webhook path: %q", got)
					}
				})
			}
		}
	}
}

// TestUncleanPath is the direct unit table for the pre-mux guard predicate: a
// single trailing slash is tolerated, every other deviation from the cleaned
// path is rejected.
func TestUncleanPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "clean path", path: "/api/webhooks/evolution/1/secret", want: false},
		{name: "single trailing slash is clean", path: "/api/webhooks/evolution/1/", want: false},
		{name: "root is clean", path: "/", want: false},
		{name: "leading duplicate slash", path: "//api/webhooks/evolution/1/secret", want: true},
		{name: "inner duplicate slash", path: "/api/webhooks/evolution/1//secret", want: true},
		{name: "dot segment", path: "/api/webhooks/evolution/1/./secret", want: true},
		{name: "dot-dot segment", path: "/api/webhooks/evolution/1/foo/../secret", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uncleanPath(tt.path); got != tt.want {
				t.Errorf("uncleanPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
