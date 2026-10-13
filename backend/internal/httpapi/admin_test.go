package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/auth"
)

const (
	testSessionSecret = "synthetic-session-secret-0123456789abcdef"
	testBridgeURL     = "https://bridge.example"
)

// testEnv is the wired admin surface over in-memory fakes.
type testEnv struct {
	handler   http.Handler
	store     *adminState
	authStore *fakeAuthStore
	authSvc   *auth.Service
	upstream  *fakeUpstream
	clock     *testClock
	logBuf    *bytes.Buffer
}

func newTestEnv(t *testing.T, allowedOrigins ...string) *testEnv {
	t.Helper()
	clock := newTestClock()
	authStore := newFakeAuthStore()
	authSvc := auth.New(authStore, auth.Config{Secret: testSessionSecret, Now: clock.Now})
	store := newAdminState()
	upstream := newFakeUpstream()
	logger, buf := testLogger()

	handler := newHandler(Options{
		Logger: logger,
		Admin: &AdminDeps{
			Auth:           authSvc,
			Connections:    store.Connections(),
			Logs:           store.Logs(),
			Dashboard:      store.Dashboard(),
			Settings:       store.Settings(),
			Notifications:  store.Notifications(),
			Upstream:       upstream,
			BridgeURL:      testBridgeURL,
			WebhookSecret:  testWebhookSecret,
			CookieSecure:   true,
			CookieSameSite: http.SameSiteLaxMode,
		},
		CORSAllowedOrigins: allowedOrigins,
	})
	return &testEnv{
		handler: handler, store: store, authStore: authStore, authSvc: authSvc,
		upstream: upstream, clock: clock, logBuf: buf,
	}
}

func request(handler http.Handler, method, path, body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	// Mirror the SPA client (spec §3.2): mutations declare a JSON content type.
	// Tests that exercise the 415 path override this with an option.
	if body != "" || isMutation(method) {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func withCSRF() func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(csrfHeader, "1") }
}

func withSessionCookie(token string) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
}

func withOrigin(origin string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Origin", origin) }
}

// login performs a login and returns the session cookie value.
func (e *testEnv) login(t *testing.T, username, password string) string {
	t.Helper()
	rec := request(e.handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`, withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	t.Fatal("login response has no session cookie")
	return ""
}

func concretePath(pattern string) string {
	return strings.ReplaceAll(pattern, "{id}", "1")
}

func jsonField(t *testing.T, rec *httptest.ResponseRecorder, key string) any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not json: %v (%s)", err, rec.Body.String())
	}
	return payload[key]
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not the error envelope: %v (%s)", err, rec.Body.String())
	}
	return payload.Error.Code
}

// ─── A1 ───────────────────────────────────────────────────────────────────────

func TestAdminRoutesRequireAuth(t *testing.T) {
	env := newTestEnv(t)
	count := 0
	for _, rt := range apiRoutes {
		if rt.Auth != AuthSession {
			continue
		}
		count++
		t.Run(rt.Method+" "+rt.Pattern, func(t *testing.T) {
			rec := request(env.handler, rt.Method, concretePath(rt.Pattern), "", withCSRF())
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("unauthenticated %s %s = %d, want 401", rt.Method, rt.Pattern, rec.Code)
			}
		})
	}
	if count == 0 {
		t.Fatal("no session routes in apiRoutes")
	}
}

// A-LOW-1: with no admin surface wired (a no-database start) a session route
// must fail closed with 401, not panic into a recovered 500.
func TestSessionMiddlewareFailsClosedWithoutAdmin(t *testing.T) {
	handler := newHandler(Options{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	rec := request(handler, http.MethodGet, "/api/v1/connections", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-admin session route = %d, want 401 (%s)", rec.Code, rec.Body)
	}
	if got := errorCode(t, rec); got != codeUnauthorized {
		t.Errorf("code = %q, want %q", got, codeUnauthorized)
	}
}

// ─── A2 ───────────────────────────────────────────────────────────────────────

func TestLoginFailures(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("active", "correct-horse", true)
	env.authStore.seed("inactive", "correct-horse", false)

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"unknown user", `{"username":"ghost","password":"x"}`, http.StatusUnauthorized},
		{"wrong password", `{"username":"active","password":"wrong"}`, http.StatusUnauthorized},
		{"inactive admin", `{"username":"inactive","password":"correct-horse"}`, http.StatusForbidden},
		{"missing fields", `{}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := request(env.handler, http.MethodPost, "/api/v1/auth/login", tt.body, withCSRF())
			if rec.Code != tt.wantCode {
				t.Errorf("login = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}

	t.Run("unknown and wrong are indistinguishable", func(t *testing.T) {
		unknown := request(env.handler, http.MethodPost, "/api/v1/auth/login", `{"username":"ghost2","password":"x"}`, withCSRF())
		wrong := request(env.handler, http.MethodPost, "/api/v1/auth/login", `{"username":"active","password":"wrong"}`, withCSRF())
		if unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
			t.Errorf("enumeration signal: %d %s vs %d %s", unknown.Code, unknown.Body, wrong.Code, wrong.Body)
		}
	})
}

// ─── A3 ───────────────────────────────────────────────────────────────────────

func TestSetupOnlyWhenEmpty(t *testing.T) {
	t.Run("empty creates the first admin", func(t *testing.T) {
		env := newTestEnv(t)
		rec := request(env.handler, http.MethodPost, "/api/v1/auth/setup",
			`{"username":"admin","password":"secret1","displayName":"Admin"}`, withCSRF())
		if rec.Code != http.StatusOK {
			t.Fatalf("setup = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		found := false
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookieName {
				found = true
			}
		}
		if !found {
			t.Error("setup did not set the session cookie")
		}
	})
	t.Run("with an admin it is closed", func(t *testing.T) {
		env := newTestEnv(t)
		env.authStore.seed("admin", "secret1", true)
		rec := request(env.handler, http.MethodPost, "/api/v1/auth/setup",
			`{"username":"second","password":"secret1"}`, withCSRF())
		if rec.Code != http.StatusForbidden {
			t.Errorf("setup = %d, want 403", rec.Code)
		}
	})
	t.Run("a lost race is a clean 409", func(t *testing.T) {
		env := newTestEnv(t)
		// A concurrent first-run Setup that wins the race surfaces as
		// auth.ErrConflict from the UNIQUE(username) insert (A-LOW-3).
		env.authStore.createErr = auth.ErrConflict
		rec := request(env.handler, http.MethodPost, "/api/v1/auth/setup",
			`{"username":"admin","password":"secret1"}`, withCSRF())
		if rec.Code != http.StatusConflict {
			t.Fatalf("setup conflict = %d, want 409 (%s)", rec.Code, rec.Body)
		}
		if got := errorCode(t, rec); got != codeConflict {
			t.Errorf("code = %q, want %q", got, codeConflict)
		}
	})
}

// ─── A4 ───────────────────────────────────────────────────────────────────────

func TestSessionCookieFlags(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	rec := request(env.handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"secret1"}`, withCSRF())

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	if !cookie.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}
	if !cookie.Secure {
		t.Error("cookie is not Secure")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("cookie Path = %q, want /", cookie.Path)
	}
	if cookie.MaxAge != int(auth.DefaultSessionTTL.Seconds()) {
		t.Errorf("cookie MaxAge = %d, want %d", cookie.MaxAge, int(auth.DefaultSessionTTL.Seconds()))
	}
}

// ─── A5 ───────────────────────────────────────────────────────────────────────

func TestSessionRevocation(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)

	token := env.login(t, "admin", "secret1")

	t.Run("valid session works", func(t *testing.T) {
		rec := request(env.handler, http.MethodGet, "/api/v1/auth/me", "", withSessionCookie(token))
		if rec.Code != http.StatusOK || jsonField(t, rec, "user") == nil {
			t.Errorf("me = %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("tampered signature", func(t *testing.T) {
		rec := request(env.handler, http.MethodGet, "/api/v1/connections", "", withSessionCookie(token+"x"))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("tampered session = %d, want 401", rec.Code)
		}
	})
	t.Run("bumped token version", func(t *testing.T) {
		env.authSvc.Logout(token)
		rec := request(env.handler, http.MethodGet, "/api/v1/connections", "", withSessionCookie(token))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("revoked session = %d, want 401", rec.Code)
		}
	})
	t.Run("expired exp", func(t *testing.T) {
		fresh := env.login(t, "admin", "secret1")
		env.clock.Advance(auth.DefaultSessionTTL + time.Hour)
		rec := request(env.handler, http.MethodGet, "/api/v1/connections", "", withSessionCookie(fresh))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expired session = %d, want 401", rec.Code)
		}
	})
}

// ─── A6 ───────────────────────────────────────────────────────────────────────

func TestLoginRateLimit(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	body := `{"username":"admin","password":"wrong"}`

	for i := range 5 {
		rec := request(env.handler, http.MethodPost, "/api/v1/auth/login", body, withCSRF())
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i, rec.Code)
		}
	}
	blocked := request(env.handler, http.MethodPost, "/api/v1/auth/login", body, withCSRF())
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt = %d, want 429", blocked.Code)
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Error("429 is missing Retry-After")
	}

	// After the window a success resets the counter.
	env.clock.Advance(16 * time.Minute)
	rec := request(env.handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"secret1"}`, withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("post-window success = %d, want 200", rec.Code)
	}
	after := request(env.handler, http.MethodPost, "/api/v1/auth/login", body, withCSRF())
	if after.Code != http.StatusUnauthorized {
		t.Errorf("after reset = %d, want 401 (counter reset)", after.Code)
	}
}

// ─── A7 ───────────────────────────────────────────────────────────────────────

func TestCSRFRequired(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	token := env.login(t, "admin", "secret1")

	for _, rt := range apiRoutes {
		if !isMutation(rt.Method) {
			continue
		}
		t.Run(rt.Method+" "+rt.Pattern, func(t *testing.T) {
			opts := []func(*http.Request){}
			if rt.Auth == AuthSession {
				opts = append(opts, withSessionCookie(token))
			}
			without := request(env.handler, rt.Method, concretePath(rt.Pattern), "", opts...)
			if without.Code != http.StatusForbidden || errorCode(t, without) != codeCSRF {
				t.Errorf("mutation without CSRF = %d %s, want 403 csrf", without.Code, without.Body)
			}
			with := request(env.handler, rt.Method, concretePath(rt.Pattern), "", append(opts, withCSRF())...)
			if with.Code == http.StatusForbidden && errorCode(t, with) == codeCSRF {
				t.Errorf("mutation with CSRF still rejected as csrf: %s", with.Body)
			}
		})
	}
}

// A-MED-2: every mutation must declare a JSON content type, not just carry the
// CSRF header (spec §1.1); a non-JSON body is 415. Iterates the mutation routes
// in apiRoutes like A7, so a new mutation cannot ship without the guard.
func TestMutationRequiresJSONContentType(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	token := env.login(t, "admin", "secret1")

	textPlain := func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }
	missing := func(r *http.Request) { r.Header.Del("Content-Type") }

	count := 0
	for _, rt := range apiRoutes {
		if !isMutation(rt.Method) {
			continue
		}
		count++
		t.Run(rt.Method+" "+rt.Pattern, func(t *testing.T) {
			base := []func(*http.Request){withCSRF()}
			if rt.Auth == AuthSession {
				base = append(base, withSessionCookie(token))
			}
			for _, tc := range []struct {
				name string
				set  func(*http.Request)
			}{
				{"wrong content type", textPlain},
				{"missing content type", missing},
			} {
				t.Run(tc.name, func(t *testing.T) {
					opts := append(append([]func(*http.Request){}, base...), tc.set)
					rec := request(env.handler, rt.Method, concretePath(rt.Pattern), "", opts...)
					if rec.Code != http.StatusUnsupportedMediaType {
						t.Fatalf("%s %s = %d, want 415 (%s)", rt.Method, rt.Pattern, rec.Code, rec.Body)
					}
					if got := errorCode(t, rec); got != codeUnsupportedMediaType {
						t.Errorf("code = %q, want %q", got, codeUnsupportedMediaType)
					}
				})
			}
			// Negative control: the default JSON content type is never 415.
			rec := request(env.handler, rt.Method, concretePath(rt.Pattern), "", base...)
			if rec.Code == http.StatusUnsupportedMediaType {
				t.Errorf("%s %s with JSON content type = 415", rt.Method, rt.Pattern)
			}
		})
	}
	if count == 0 {
		t.Fatal("no mutation routes in apiRoutes")
	}
}

// ─── A8 ───────────────────────────────────────────────────────────────────────

func TestCORSAllowlist(t *testing.T) {
	env := newTestEnv(t, "https://admin.example")

	t.Run("allowed origin", func(t *testing.T) {
		rec := request(env.handler, http.MethodGet, "/healthz", "", withOrigin("https://admin.example"))
		if rec.Header().Get("Access-Control-Allow-Origin") != "https://admin.example" {
			t.Errorf("ACAO = %q", rec.Header().Get("Access-Control-Allow-Origin"))
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Error("credentials not allowed for an allowlisted origin")
		}
	})
	t.Run("disallowed origin", func(t *testing.T) {
		rec := request(env.handler, http.MethodGet, "/healthz", "", withOrigin("https://evil.example"))
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("disallowed origin got ACAO %q", rec.Header().Get("Access-Control-Allow-Origin"))
		}
	})
	t.Run("preflight", func(t *testing.T) {
		rec := request(env.handler, http.MethodOptions, "/api/v1/auth/login", "", withOrigin("https://admin.example"),
			func(r *http.Request) { r.Header.Set("Access-Control-Request-Method", "POST") })
		if rec.Code != http.StatusNoContent {
			t.Errorf("preflight = %d, want 204", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "https://admin.example" {
			t.Error("preflight missing ACAO")
		}
	})
}

// ─── A9 ───────────────────────────────────────────────────────────────────────

func TestNoSecretLeakInResponses(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	token := env.login(t, "admin", "secret1")

	const (
		evoKey    = "SENTINEL-EVO-KEY-1234"
		evoToken  = "SENTINEL-EVO-TOKEN-5678"
		cwToken   = "SENTINEL-CW-TOKEN-9012"
		smtpPass  = "SENTINEL-SMTP-PASS-3456"
		inboxTok  = "SENTINEL-INBOX-TOKEN-2468"
		inboxHook = "https://bridge.example/api/webhooks/chatwoot/1/" + inboxTok
	)
	env.store.connections = []admin.Connection{{
		ID: 1, Name: "Synthetic", EvolutionAPIKey: evoKey, EvolutionInstanceToken: evoToken,
		ChatwootAPIToken: cwToken, ChatwootInboxID: 10, IsActive: true,
	}}
	env.store.settings.SMTPPass = smtpPass
	env.store.settings.HasSMTPPass = true
	env.upstream.evo.resolvedToken = "SENTINEL-RESOLVED-TOKEN-7890"
	// A token-bearing inbox webhook URL, as Chatwoot returns it.
	env.upstream.cw.inboxes = []admin.Inbox{{ID: 10, Name: "Synthetic inbox", ChannelType: "api", WebhookURL: inboxHook}}
	env.upstream.cw.inboxWebhookURL = inboxHook

	paths := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/connections", ""},
		{http.MethodGet, "/api/v1/connections/1", ""},
		{http.MethodGet, "/api/v1/connections/1/webhook-status", ""},
		{http.MethodGet, "/api/v1/notifications/settings", ""},
		{http.MethodPost, "/api/v1/connections/1/resolve-token", ""},
		{http.MethodPost, "/api/v1/chatwoot/inboxes/query", `{"connectionId":1}`},
		{http.MethodPost, "/api/v1/chatwoot/inboxes", `{"connectionId":1,"inboxName":"Synthetic inbox"}`},
		{http.MethodPatch, "/api/v1/connections/1/chatwoot/inbox-settings", `{"name":"Synthetic inbox"}`},
	}
	secrets := []string{evoKey, evoToken, cwToken, smtpPass, "SENTINEL-RESOLVED-TOKEN-7890", inboxTok}
	for _, p := range paths {
		rec := request(env.handler, p.method, p.path, p.body, withCSRF(), withSessionCookie(token))
		for _, secret := range secrets {
			if strings.Contains(rec.Body.String(), secret) {
				t.Errorf("%s %s leaked %q: %s", p.method, p.path, secret, rec.Body)
			}
		}
	}
}

// ─── A10 ──────────────────────────────────────────────────────────────────────

func TestNoSecretLeakInLogs(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	token := env.login(t, "admin", "secret1")
	env.store.connections = []admin.Connection{{ID: 1, Name: "Synthetic", EvolutionAPIKey: "SENTINEL-EVO-KEY-LOG", IsActive: true}}

	request(env.handler, http.MethodGet, "/api/v1/connections?token=SENTINEL-QUERY-LOG", "", withSessionCookie(token))
	request(env.handler, http.MethodPost, "/api/v1/connections/1/resolve-token", "", withCSRF(), withSessionCookie(token),
		func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer SENTINEL-HEADER-LOG")
			r.Header.Set("Cookie", sessionCookieName+"=SENTINEL-COOKIE-LOG")
		})
	request(env.handler, http.MethodGet, "/api/v1/notifications/settings", "", withSessionCookie(token))

	logs := env.logBuf.String()
	for _, secret := range []string{"SENTINEL-EVO-KEY-LOG", "SENTINEL-QUERY-LOG", "SENTINEL-HEADER-LOG", "SENTINEL-COOKIE-LOG"} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs leaked %q: %s", secret, logs)
		}
	}
}

// ─── A11 ──────────────────────────────────────────────────────────────────────

func TestWebhookURLMasked(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	token := env.login(t, "admin", "secret1")
	env.store.connections = []admin.Connection{{
		ID: 1, Name: "Synthetic", ChatwootInboxID: 10, IsActive: true,
		ChatwootAPIURL: "https://cw.example", ChatwootAPIToken: "cw-token", ChatwootAccountID: 1,
		EvolutionAPIURL: "https://evo.example", EvolutionAPIKey: "evo-key",
	}}
	derived := DeriveWebhookToken(testWebhookSecret, 1)
	inboxHook := "https://bridge.example/api/webhooks/chatwoot/1/" + derived
	env.upstream.cw.inboxes = []admin.Inbox{{ID: 10, Name: "Synthetic inbox", ChannelType: "api", WebhookURL: inboxHook}}
	env.upstream.cw.inboxWebhookURL = inboxHook

	masked := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/connections", ""},
		{http.MethodGet, "/api/v1/connections/1", ""},
		{http.MethodGet, "/api/v1/connections/1/webhook-status", ""},
		{http.MethodPost, "/api/v1/connections/1/webhook", ""},
		{http.MethodPost, "/api/v1/connections/1/chatwoot/webhook", ""},
		{http.MethodPost, "/api/v1/chatwoot/inboxes/query", `{"connectionId":1}`},
		{http.MethodPost, "/api/v1/chatwoot/inboxes", `{"connectionId":1,"inboxName":"Synthetic inbox"}`},
		{http.MethodPatch, "/api/v1/connections/1/chatwoot/inbox-settings", `{"name":"Synthetic inbox"}`},
	}
	for _, p := range masked {
		rec := request(env.handler, p.method, p.path, p.body, withCSRF(), withSessionCookie(token))
		if strings.Contains(rec.Body.String(), derived) {
			t.Errorf("%s %s leaked the derived token: %s", p.method, p.path, rec.Body)
		}
	}

	for _, system := range []string{"evolution", "chatwoot"} {
		rec := request(env.handler, http.MethodGet, "/api/v1/connections/1/webhook-url/"+system, "", withSessionCookie(token))
		if !strings.Contains(rec.Body.String(), derived) {
			t.Errorf("webhook-url/%s did not return the derived token: %s", system, rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("webhook-url/%s Cache-Control = %q, want no-store", system, rec.Header().Get("Cache-Control"))
		}
	}
}

// ─── A12 ──────────────────────────────────────────────────────────────────────

func TestPaginationBounds(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	token := env.login(t, "admin", "secret1")
	env.store.logs = []admin.LogRow{{ID: 1, ConnectionID: 1, MessageType: "text", Status: "sent"}}
	env.store.logsTotal = 7

	bad := []string{"?limit=0", "?limit=101", "?offset=-1", "?limit=abc"}
	for _, q := range bad {
		rec := request(env.handler, http.MethodGet, "/api/v1/logs"+q, "", withSessionCookie(token))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/v1/logs%s = %d, want 400", q, rec.Code)
		}
	}

	ok := request(env.handler, http.MethodGet, "/api/v1/logs?limit=100&offset=0", "", withSessionCookie(token))
	if ok.Code != http.StatusOK {
		t.Fatalf("limit=100 = %d, want 200", ok.Code)
	}
	if got := jsonField(t, ok, "total"); got != float64(7) {
		t.Errorf("total = %v, want 7", got)
	}
	logs, _ := jsonField(t, ok, "logs").([]any)
	if len(logs) != 1 {
		t.Errorf("logs len = %d, want 1", len(logs))
	}

	def := request(env.handler, http.MethodGet, "/api/v1/logs", "", withSessionCookie(token))
	if def.Code != http.StatusOK {
		t.Errorf("default pagination = %d, want 200", def.Code)
	}
}

// ─── A14 ──────────────────────────────────────────────────────────────────────

func TestErrorEnvelope(t *testing.T) {
	env := newTestEnv(t)
	env.authStore.seed("admin", "secret1", true)
	token := env.login(t, "admin", "secret1")

	cases := []struct {
		name   string
		method string
		path   string
		opts   []func(*http.Request)
		want   int
		code   string
	}{
		{"unauthorized", http.MethodGet, "/api/v1/connections", nil, http.StatusUnauthorized, codeUnauthorized},
		{"csrf", http.MethodPost, "/api/v1/auth/login", nil, http.StatusForbidden, codeCSRF},
		{"not found", http.MethodGet, "/api/v1/connections/999", []func(*http.Request){withSessionCookie(token)}, http.StatusNotFound, codeNotFound},
		{"invalid request", http.MethodGet, "/api/v1/logs?limit=0", []func(*http.Request){withSessionCookie(token)}, http.StatusBadRequest, codeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(env.handler, tc.method, tc.path, "", tc.opts...)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body)
			}
			if got := errorCode(t, rec); got != tc.code {
				t.Errorf("code = %q, want %q", got, tc.code)
			}
		})
	}

	t.Run("500 leaks nothing", func(t *testing.T) {
		boom := failingConnections{env.store.Connections()}
		env.handler = newHandler(Options{
			Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
			Admin: &AdminDeps{
				Auth: env.authSvc, Connections: boom, Logs: env.store.Logs(), Dashboard: env.store.Dashboard(),
				Settings: env.store.Settings(), Notifications: env.store.Notifications(), Upstream: env.upstream,
			},
		})
		rec := request(env.handler, http.MethodGet, "/api/v1/connections", "", withSessionCookie(token))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if got := errorCode(t, rec); got != codeInternal {
			t.Errorf("code = %q, want internal", got)
		}
		if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "db-secret-text") {
			t.Errorf("500 leaked upstream/db text: %s", rec.Body)
		}
	})
}

// failingConnections makes Connections.List fail with a message that must never
// reach the client.
type failingConnections struct {
	admin.ConnectionsStore
}

func (f failingConnections) List(context.Context) ([]admin.Connection, error) {
	return nil, errors.New("boom: db-secret-text")
}

// ─── A15 ──────────────────────────────────────────────────────────────────────

func TestAdminBodyCap(t *testing.T) {
	env := newTestEnv(t)
	big := strings.Repeat("a", adminBodyBytes+1024)
	rec := request(env.handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"`+big+`","password":"x"}`, withCSRF())
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized admin body = %d, want 413", rec.Code)
	}
}

// ─── A17 ──────────────────────────────────────────────────────────────────────

func TestBackendServesNoFrontend(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{"/", "/index.html", "/assets/app.js"} {
		rec := request(env.handler, http.MethodGet, path, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want non-HTML", path, ct)
		}
	}

	// Source scan: the backend must not serve or embed frontend assets.
	_, thisFile, _, _ := runtime.Caller(0)
	backendDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	err := filepath.WalkDir(backendDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(data)
		for _, forbidden := range []string{"http.FileServer", "http.Dir(", "http.ServeFile"} {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s contains %q", path, forbidden)
			}
		}
		if strings.Contains(content, "go:embed") &&
			(strings.Contains(content, "frontend") || strings.Contains(content, "client/")) {
			t.Errorf("%s embeds frontend assets", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan backend sources: %v", err)
	}
}

// ─── A18 ──────────────────────────────────────────────────────────────────────

func TestPublicRoutesStayPublic(t *testing.T) {
	env := newTestEnv(t)
	cases := []struct {
		method, path, body string
		opts               []func(*http.Request)
	}{
		{http.MethodGet, "/healthz", "", nil},
		{http.MethodGet, "/readyz", "", nil},
		{http.MethodGet, "/api/v1/meta", "", nil},
		{http.MethodGet, "/api/v1/auth/state", "", nil},
		{http.MethodPost, "/api/v1/auth/login", `{}`, []func(*http.Request){withCSRF()}},
		{http.MethodPost, "/api/v1/auth/setup", `{}`, []func(*http.Request){withCSRF()}},
		{http.MethodGet, "/api/v1/auth/me", "", nil},
		{http.MethodPost, "/api/v1/auth/logout", "", []func(*http.Request){withCSRF()}},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := request(env.handler, tc.method, tc.path, tc.body, tc.opts...)
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("%s %s requires a session (401)", tc.method, tc.path)
			}
		})
	}
}
