package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

const testWebhookSecret = "synthetic-webhook-token-secret"

// wellFormedPayload is a synthetic E1 Evolution message.
var wellFormedPayload = []byte(`{"event":"Message","data":{"Info":{"ID":"SYNTH-1","Chat":"5511900000001@s.whatsapp.net","Sender":"5511900000001@s.whatsapp.net","IsFromMe":false,"IsGroup":false},"Message":{"conversation":"hello"}}}`)

// fakeStore counts every claim/audit/release call and lets a test inject errors.
type fakeStore struct {
	mu sync.Mutex

	claimCalls   int
	payloadCalls int
	releaseCalls int

	claimed  bool
	claimErr error
	storeErr error

	lastEnvelope []byte
}

func (f *fakeStore) Claim(_ context.Context, _ bridge.Key, _ time.Duration) (bool, int64, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	if f.claimErr != nil {
		return false, 0, "", f.claimErr
	}
	if !f.claimed {
		return false, 7, "", nil
	}
	return true, 7, "0123456789abcdef0123456789abcdef", nil
}

func (f *fakeStore) StorePayload(_ context.Context, _ int64, _ string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloadCalls++
	f.lastEnvelope = append([]byte(nil), payload...)
	return f.storeErr
}

func (f *fakeStore) Release(_ context.Context, _ int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	return nil
}

func (f *fakeStore) counts() (claim, payload, release int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimCalls, f.payloadCalls, f.releaseCalls
}

// fakeEnqueuer counts enqueue attempts and returns a scripted admission result.
type fakeEnqueuer struct {
	mu    sync.Mutex
	calls int
	ok    bool
	// onEnqueue runs inside TryEnqueue (e.g. to cancel the request context).
	onEnqueue func()
}

func (f *fakeEnqueuer) TryEnqueue(_ func(context.Context) error, _ int) bool {
	f.mu.Lock()
	f.calls++
	hook := f.onEnqueue
	ok := f.ok
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return ok
}

func (f *fakeEnqueuer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// stubEngine records whether the queued job ever ran (it must not, in-handler).
type stubEngine struct {
	mu    sync.Mutex
	calls int
}

func (e *stubEngine) HandleClaimed(_ context.Context, _ bridge.Event, _ int64, _ string) (bridge.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return bridge.OutcomeForwarded, nil
}

func (e *stubEngine) HandleChatwootClaimed(_ context.Context, _ bridge.Event, _ int64, _ string) (bridge.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return bridge.OutcomeForwarded, nil
}

func (e *stubEngine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// fakeRegistry resolves connection ids to a runtime.
type fakeRegistry struct {
	conns  map[int]bridge.Connection
	engine IngressEngine
}

func (r *fakeRegistry) Lookup(id int) (bridge.Connection, IngressEngine, bool) {
	conn, ok := r.conns[id]
	if !ok {
		return bridge.Connection{}, nil, false
	}
	if !conn.IsActive {
		return conn, nil, true
	}
	return conn, r.engine, true
}

// webhookHarness builds the full server chain (access log + body limit + route)
// with counting fakes.
type webhookHarness struct {
	srv      *http.Server
	logs     *bytes.Buffer
	store    *fakeStore
	enqueuer *fakeEnqueuer
	engine   *stubEngine
	registry *fakeRegistry
}

func newWebhookHarness(t *testing.T, secret string) *webhookHarness {
	t.Helper()
	logger, buf := testLogger()
	h := &webhookHarness{
		logs:     buf,
		store:    &fakeStore{claimed: true},
		enqueuer: &fakeEnqueuer{ok: true},
		engine:   &stubEngine{},
		registry: &fakeRegistry{conns: map[int]bridge.Connection{
			1: {ID: 1, ChatwootInboxID: 10, IsActive: true},
		}},
	}
	h.registry.engine = h.engine
	handler := NewWebhookHandler(WebhookOptions{
		Secret:   secret,
		Registry: h.registry,
		Store:    h.store,
		Enqueuer: h.enqueuer,
		Logger:   logger,
	})
	h.srv = NewServer(Options{Logger: logger, Webhook: handler})
	return h
}

func (h *webhookHarness) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.srv.Handler.ServeHTTP(rec, req)
	return rec
}

func webhookPath(connectionID, token string) string {
	return "/api/webhooks/evolution/" + connectionID + "/" + token
}

// TestSlice4_I1_ValidTokenAccepted proves a valid token + well-formed E1 is
// accepted: exactly one claim, one audit write, one enqueue, no entity created
// inside the handler.
func TestSlice4_I1_ValidTokenAccepted(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	claim, payload, release := h.store.counts()
	if claim != 1 || payload != 1 || release != 0 {
		t.Errorf("claim/payload/release = %d/%d/%d, want 1/1/0", claim, payload, release)
	}
	if got := h.enqueuer.count(); got != 1 {
		t.Errorf("enqueue = %d, want 1", got)
	}
	if got := h.engine.count(); got != 0 {
		t.Errorf("engine calls = %d, want 0 (the handler must not run the job)", got)
	}
}

// TestSlice4_I2_BadTokenRejectedBeforeBody proves an invalid token is 401 and
// the body is never read.
func TestSlice4_I2_BadTokenRejectedBeforeBody(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{name: "absent token", token: ""},
		{name: "wrong token", token: "not-the-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newWebhookHarness(t, testWebhookSecret)
			req := httptest.NewRequest(http.MethodPost, webhookPath("1", tt.token), nil)
			body := &trackingBody{}
			req.Body = body

			rec := h.do(req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if body.reads != 0 {
				t.Errorf("body reads = %d, want 0", body.reads)
			}
			claim, _, _ := h.store.counts()
			if claim != 0 || h.enqueuer.count() != 0 {
				t.Errorf("claim/enqueue = %d/%d, want 0/0", claim, h.enqueuer.count())
			}
		})
	}
}

// TestSlice4_I3_UnknownConnection proves an unknown connection id is 404 with no
// claim.
func TestSlice4_I3_UnknownConnection(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 99)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("99", token), bytes.NewReader(wellFormedPayload)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
}

// TestSlice4_I4_InactiveConnection proves an inactive connection is 403 with no
// claim and no entity.
func TestSlice4_I4_InactiveConnection(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	h.registry.conns[1] = bridge.Connection{ID: 1, ChatwootInboxID: 10, IsActive: false}
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
	if got := h.engine.count(); got != 0 {
		t.Errorf("engine calls = %d, want 0", got)
	}
}

// TestSlice4_I5_MalformedJSON proves a malformed payload is 400 with no claim.
func TestSlice4_I5_MalformedJSON(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), strings.NewReader("{not json")))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
}

// TestSlice4_I6_DeclaredBodyTooLarge proves the middleware rejects a declared
// body over 16 MiB with 413 and no claim.
func TestSlice4_I6_DeclaredBodyTooLarge(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(make([]byte, maxBodyBytes+1))))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
}

// TestSlice4_I7_MidReadOverflow proves a chunked body over the cap fails while
// reading with 413 and no claim.
func TestSlice4_I7_MidReadOverflow(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	req := httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(make([]byte, maxBodyBytes+1)))
	req.ContentLength = -1 // simulate chunked encoding
	rec := h.do(req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
}

// TestSlice4_I8_DuplicateClaim proves a lost claim is 200 with no enqueue.
func TestSlice4_I8_DuplicateClaim(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	h.store.claimed = false
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := h.enqueuer.count(); got != 0 {
		t.Errorf("enqueue = %d, want 0", got)
	}
	if claim, payload, _ := h.store.counts(); claim != 1 || payload != 0 {
		t.Errorf("claim/payload = %d/%d, want 1/0", claim, payload)
	}
}

// TestSlice4_I9_AuditWriteFailureReleases proves a StorePayload failure is 503
// with exactly one release and no enqueue.
func TestSlice4_I9_AuditWriteFailureReleases(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	h.store.storeErr = errors.New("audit write failed")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if _, _, release := h.store.counts(); release != 1 {
		t.Errorf("release = %d, want 1", release)
	}
	if got := h.enqueuer.count(); got != 0 {
		t.Errorf("enqueue = %d, want 0", got)
	}
}

// TestSlice4_I10_QueueFullReleases proves a full queue is 503 + Retry-After with
// exactly one release.
func TestSlice4_I10_QueueFullReleases(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	h.enqueuer.ok = false
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != strconv.Itoa(retryAfterSeconds) {
		t.Errorf("Retry-After = %q, want %d", got, retryAfterSeconds)
	}
	if _, _, release := h.store.counts(); release != 1 {
		t.Errorf("release = %d, want 1", release)
	}
}

// TestSlice4_I11_ClaimError proves a claim error is 503 with no entity.
func TestSlice4_I11_ClaimError(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	h.store.claimErr = errors.New("database down")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := h.engine.count(); got != 0 {
		t.Errorf("engine calls = %d, want 0", got)
	}
}

// TestSlice4_I12_TokenNeverLogged proves the raw path token never reaches the
// captured logs.
func TestSlice4_I12_TokenNeverLogged(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

	if strings.Contains(h.logs.String(), token) {
		t.Errorf("access log leaked the webhook token: %s", h.logs.String())
	}
}

// TestSlice4_I13_WebhookPathMasked proves the access log masks the token
// segment.
func TestSlice4_I13_WebhookPathMasked(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

	logs := h.logs.String()
	if !strings.Contains(logs, `"path":"/api/webhooks/evolution/***"`) {
		t.Errorf("access log does not mask the webhook path: %s", logs)
	}
	if strings.Contains(logs, token) {
		t.Errorf("access log leaked the token: %s", logs)
	}
}

// TestSlice4_High1_WebhookTokenNeverLeaks is the HIGH-1 regression suite: the
// bearer path token must never reach the access log nor any response header for
// any path shape, including the ones ServeMux would 307-redirect (whose
// Location header carries the raw token). ADR-0006 Amendment 2, CRITICAL.
func TestSlice4_High1_WebhookTokenNeverLeaks(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	tests := []struct {
		name     string
		target   string
		wantCode int
	}{
		{name: "clean path is accepted", target: "/api/webhooks/evolution/1/" + token, wantCode: http.StatusOK},
		{name: "leading duplicate slash", target: "//api/webhooks/evolution/1/" + token, wantCode: http.StatusNotFound},
		{name: "case-varied anchor", target: "/API/webhooks/evolution/1/" + token, wantCode: http.StatusNotFound},
		{name: "prefixed path", target: "/x/api/webhooks/evolution/1/" + token, wantCode: http.StatusNotFound},
		{name: "inner duplicate slash", target: "/api/webhooks/evolution/1//" + token, wantCode: http.StatusNotFound},
		{name: "dot segment", target: "/api/webhooks/evolution/1/./" + token, wantCode: http.StatusNotFound},
		{name: "dot-dot segment", target: "/api/webhooks/evolution/1/foo/../" + token, wantCode: http.StatusNotFound},
		{name: "tokenless form fails closed", target: "/api/webhooks/evolution/1", wantCode: http.StatusUnauthorized},
		{name: "token in the connection-id slot", target: "/api/webhooks/evolution/" + token, wantCode: http.StatusNotFound},
		{name: "token in the system slot", target: "/api/webhooks/" + token + "/1/x", wantCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com"+tt.target, bytes.NewReader(wellFormedPayload))
			rec := h.do(req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if strings.Contains(h.logs.String(), token) {
				t.Errorf("access log leaked the webhook token: %s", h.logs.String())
			}
			for name, values := range rec.Header() {
				for _, value := range values {
					if strings.Contains(value, token) {
						t.Errorf("response header %s leaked the webhook token: %q", name, value)
					}
				}
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("unexpected Location header %q; a redirect must not carry the token", loc)
			}
		})
	}
}

// TestSlice4_High1_UnicodeEncodedPathNeverLeaks drives the full middleware
// chain (correlation → access log → recovery → body limit → guard → mux) with
// the percent-encoded runes that triggered the index-shift leak and the
// pre-auth panic: %C8%BA is U+023A and %C8%BE is U+023E, each of which
// strings.ToLower expands from 2 to 3 bytes. For every prefix length 1..80 the
// token must never reach the access log nor a response header, and the request
// must not panic (a panic in the access log sits outside recoverPanic).
func TestSlice4_High1_UnicodeEncodedPathNeverLeaks(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	for _, enc := range []string{"%C8%BA", "%C8%BE"} {
		for n := 1; n <= 80; n++ {
			t.Run(fmt.Sprintf("%s/n=%d", enc, n), func(t *testing.T) {
				target := "/" + strings.Repeat(enc, n) + "/api/webhooks/evolution/1/" + token
				req := httptest.NewRequest(http.MethodPost, "http://example.com"+target, bytes.NewReader(wellFormedPayload))
				rec := h.do(req) // must not panic

				if strings.Contains(h.logs.String(), token) {
					t.Fatalf("access log leaked the webhook token: %s", h.logs.String())
				}
				for name, values := range rec.Header() {
					for _, value := range values {
						if strings.Contains(value, token) {
							t.Errorf("response header %s leaked the webhook token: %q", name, value)
						}
					}
				}
			})
		}
	}
}

// TestSlice4_M2_TokenlessRouteFailsClosed proves POST
// /api/webhooks/evolution/1 (no trailing slash, no token) fails closed with 401
// instead of the ServeMux 307 that would add a trailing slash (MEDIUM-2).
func TestSlice4_M2_TokenlessRouteFailsClosed(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)

	rec := h.do(httptest.NewRequest(http.MethodPost, "/api/webhooks/evolution/1", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want empty (no redirect)", loc)
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
}

// TestSlice4_I15_ChatwootHMACFailClosed proves the frozen signed-mode contract
// fails closed on a missing/invalid signature and accepts a valid one.
func TestSlice4_I15_ChatwootHMACFailClosed(t *testing.T) {
	const secret = "synthetic-chatwoot-webhook-secret"
	body := []byte(`{"event":"message_created"}`)
	now := time.Unix(1_700_000_000, 0)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	valid := signChatwoot(secret, timestamp, body)

	tests := []struct {
		name      string
		secret    string
		timestamp string
		signature string
		want      bool
	}{
		{name: "valid", secret: secret, timestamp: timestamp, signature: valid, want: true},
		{name: "missing signature", secret: secret, timestamp: timestamp, signature: "", want: false},
		{name: "invalid signature", secret: secret, timestamp: timestamp, signature: "sha256=deadbeef", want: false},
		{name: "expired timestamp", secret: secret, timestamp: strconv.FormatInt(now.Add(-time.Hour).Unix(), 10), signature: signChatwoot(secret, strconv.FormatInt(now.Add(-time.Hour).Unix(), 10), body), want: false},
		{name: "empty secret never accepts", secret: "", timestamp: timestamp, signature: valid, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChatwootSignatureValid(tt.secret, tt.timestamp, body, tt.signature, now)
			if got != tt.want {
				t.Errorf("ChatwootSignatureValid = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSlice4_WebhookMethodAndRoute proves non-POST and unmatched paths are
// rejected by the mux.
func TestSlice4_WebhookMethodAndRoute(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)

	rec := h.do(httptest.NewRequest(http.MethodGet, webhookPath("1", "x"), nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET webhook = %d, want 405", rec.Code)
	}
	rec = h.do(httptest.NewRequest(http.MethodPost, "/api/webhooks/chatwoot/1/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("chatwoot route (deferred) = %d, want 404", rec.Code)
	}
}

// TestSlice4_C4_NoReleaseAfterAdmission proves a request-context cancellation
// after a successful enqueue does not release the admitted claim.
func TestSlice4_C4_NoReleaseAfterAdmission(t *testing.T) {
	h := newWebhookHarness(t, testWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.enqueuer.onEnqueue = cancel // cancel the request ctx the instant it is admitted

	req := httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)).WithContext(ctx)
	rec := h.do(req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := h.enqueuer.count(); got != 1 {
		t.Errorf("enqueue = %d, want 1", got)
	}
	if _, _, release := h.store.counts(); release != 0 {
		t.Errorf("release = %d, want 0 (no release after admission)", release)
	}
}

// trackingBody counts reads so a test can prove the body was never touched.
type trackingBody struct {
	reads int
}

func (b *trackingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (b *trackingBody) Close() error { return nil }

// signChatwoot builds the Chatwoot signature header for a body.
func signChatwoot(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// evolutionNonMessageEvents is a representative set of the 16 events the live
// instance subscribes to (docs/contracts/evolution/webhooks.md §2.1,
// [live-verified 2026-10-13]) that are NOT message events, in both the
// manager-panel/legacy spelling and the GO uppercase enum spelling; none may be
// claimed, audited or enqueued. The call names are included: this engine has no
// call handling, so a call is dropped like any other non-message event.
var evolutionNonMessageEvents = []string{
	"Presence", "PRESENCE",
	"Receipt", "READ_RECEIPT",
	"ChatPresence", "CHAT_PRESENCE",
	"ConnectionUpdate", "CONNECTION",
	"Label", "LABEL",
	"QrcodeUpdated", "QRCODE",
	"Picture", "PICTURE",
	"UserAbout", "USER_ABOUT",
	"HistorySync", "HISTORY_SYNC",
	"Contact", "CONTACT",
	"Group", "GROUP",
	"Newsletter", "NEWSLETTER",
	"ButtonClick", "BUTTON_CLICK",
	"call", "CALL", "Call",
	"CallOffer", "CallAccept", "CallTerminate", "CallOfferNotice", "CallRelayLatency",
}

// evolutionUnacceptedEvents is the unaccepted set driven through both the unit
// filter and the full ingress handler: the non-message events plus an
// absent/empty name and unknown names (including the standard-API
// `SEND_MESSAGE` subscribe name, which the legacy MSG_EVENTS never accepted).
var evolutionUnacceptedEvents = append(
	[]string{"", "unknown", "MessageCreated", "SEND_MESSAGE"},
	evolutionNonMessageEvents...,
)

// evolutionSyntheticData is a well-formed data node so Parse succeeds for any
// event name: an ignored event must still be a parseable envelope, otherwise the
// test would prove the 400 path, not the filter.
const evolutionSyntheticData = `{"Info":{"ID":"SYNTH-EVT-1","Chat":"5511900000002@s.whatsapp.net","Sender":"5511900000002@s.whatsapp.net","IsFromMe":false,"IsGroup":false},"Message":{"conversation":"hi"}}`

// evolutionPayloadWithEvent builds a synthetic envelope; an empty event omits
// the `event` field entirely (absent, not null).
func evolutionPayloadWithEvent(event string) []byte {
	if event == "" {
		return fmt.Appendf(nil, `{"data":%s}`, evolutionSyntheticData)
	}
	return fmt.Appendf(nil, `{"event":%q,"data":%s}`, event, evolutionSyntheticData)
}

func evolutionSubtestName(event string) string {
	if event == "" {
		return "<absent>"
	}
	return event
}

// TestEvolutionIgnore_AcceptedEvents proves every name in the accepted set
// passes the pre-claim filter, and that the set is exactly the legacy MSG_EVENTS
// names (webhooks.md §2) — a seventh accepted name is a contract change.
func TestEvolutionIgnore_AcceptedEvents(t *testing.T) {
	want := []string{"Message", "MESSAGE", "messages.upsert", "MESSAGES_UPSERT", "SendMessage", "SENDMESSAGE"}
	if len(evolutionMessageEvents) != len(want) {
		t.Fatalf("accepted set size = %d, want %d", len(evolutionMessageEvents), len(want))
	}
	for _, event := range want {
		t.Run(event, func(t *testing.T) {
			if _, ok := evolutionMessageEvents[event]; !ok {
				t.Errorf("%q missing from the accepted set", event)
			}
			if ignoreEvolutionEvent(event) {
				t.Errorf("ignoreEvolutionEvent(%q) = true, want false", event)
			}
		})
	}
	for event := range evolutionMessageEvents {
		if ignoreEvolutionEvent(event) {
			t.Errorf("ignoreEvolutionEvent(%q) = true for a name in the accepted set", event)
		}
	}
}

// TestEvolutionIgnore_DroppedEvents proves every non-message event name, an
// absent/empty name and unknown names are dropped by the pre-claim filter.
func TestEvolutionIgnore_DroppedEvents(t *testing.T) {
	for _, event := range evolutionUnacceptedEvents {
		t.Run(evolutionSubtestName(event), func(t *testing.T) {
			if !ignoreEvolutionEvent(event) {
				t.Errorf("ignoreEvolutionEvent(%q) = false, want true", event)
			}
		})
	}
}

// TestEvolutionIngress_DroppedEventsTouchNoPort proves an ignored event is 200
// `ignored` and never reaches the claim repository, the audit write, the
// enqueuer or the engine.
func TestEvolutionIngress_DroppedEventsTouchNoPort(t *testing.T) {
	for _, event := range evolutionUnacceptedEvents {
		t.Run(evolutionSubtestName(event), func(t *testing.T) {
			h := newWebhookHarness(t, testWebhookSecret)
			token := DeriveWebhookToken(testWebhookSecret, 1)

			rec := h.do(httptest.NewRequest(
				http.MethodPost,
				webhookPath("1", token),
				bytes.NewReader(evolutionPayloadWithEvent(event)),
			))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			assertJSONField(t, rec, "status", "ignored")
			claim, payload, release := h.store.counts()
			if claim != 0 || payload != 0 || release != 0 {
				t.Errorf("claim/payload/release = %d/%d/%d, want 0/0/0", claim, payload, release)
			}
			if got := h.enqueuer.count(); got != 0 {
				t.Errorf("enqueue = %d, want 0", got)
			}
			if got := h.engine.count(); got != 0 {
				t.Errorf("engine calls = %d, want 0", got)
			}
		})
	}
}

// TestEvolutionIngress_AcceptedEventsClaimAndEnqueue proves the accepted message
// events still take the full claim → audit → enqueue path.
func TestEvolutionIngress_AcceptedEventsClaimAndEnqueue(t *testing.T) {
	for _, event := range []string{"Message", "MESSAGE", "messages.upsert", "MESSAGES_UPSERT", "SendMessage", "SENDMESSAGE"} {
		t.Run(event, func(t *testing.T) {
			h := newWebhookHarness(t, testWebhookSecret)
			token := DeriveWebhookToken(testWebhookSecret, 1)

			rec := h.do(httptest.NewRequest(
				http.MethodPost,
				webhookPath("1", token),
				bytes.NewReader(evolutionPayloadWithEvent(event)),
			))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			assertJSONField(t, rec, "status", "accepted")
			claim, payload, release := h.store.counts()
			if claim != 1 || payload != 1 || release != 0 {
				t.Errorf("claim/payload/release = %d/%d/%d, want 1/1/0", claim, payload, release)
			}
			if got := h.enqueuer.count(); got != 1 {
				t.Errorf("enqueue = %d, want 1", got)
			}
			if got := h.engine.count(); got != 0 {
				t.Errorf("engine calls = %d, want 0 (the handler must not run the job)", got)
			}
		})
	}
}

// TestEvolutionParse_DataLessEnvelope proves the parse adapter hands a
// non-accepted event name to the shared Ignore core even when bridge.Parse
// rejected the body for a missing `data` object, while an accepted name keeps the
// fail-closed error.
func TestEvolutionParse_DataLessEnvelope(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantEvent string
		wantErr   bool
	}{
		{name: "non-message event without data", body: `{"event":"Presence"}`, wantEvent: "Presence"},
		{name: "subscription-spelling event without data", body: `{"event":"CONNECTION"}`, wantEvent: "CONNECTION"},
		{name: "call event without data", body: `{"event":"CALL"}`, wantEvent: "CALL"},
		{name: "no event and no data", body: `{}`, wantEvent: ""},
		{name: "null event and no data", body: `{"event":null}`, wantEvent: ""},
		{name: "accepted name without data", body: `{"event":"Message"}`, wantErr: true},
		{name: "accepted name with null data", body: `{"event":"Message","data":null}`, wantErr: true},
		{name: "accepted name with non-object data", body: `{"event":"Message","data":"nope"}`, wantErr: true},
		{name: "malformed json", body: "{not json", wantErr: true},
		{name: "null body", body: "null", wantErr: true},
		{name: "array body", body: "[]", wantErr: true},
		{name: "string body", body: `"str"`, wantErr: true},
		{name: "number body", body: "123", wantErr: true},
		{name: "boolean body", body: "true", wantErr: true},
		{name: "empty body", body: "", wantErr: true},
		{name: "event is not a string", body: `{"event":123}`, wantErr: true},
		{name: "accepted name with valid data", body: string(wellFormedPayload), wantEvent: "Message"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, id, err := evolutionParse([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if event != tt.wantEvent {
				t.Errorf("event = %q, want %q", event, tt.wantEvent)
			}
			if tt.wantEvent == "Message" && id == "" {
				t.Errorf("accepted message parsed with an empty id")
			}
		})
	}
}

// TestEvolutionIngress_DataLessEnvelope proves an envelope with no `data` object
// is ACKed 200 `ignored` for a non-message event: the legacy answered 200
// unconditionally, and a 400 would make Evolution GO retry the delivery during a
// reconnect. An accepted name without usable data must stay fail-closed 400.
func TestEvolutionIngress_DataLessEnvelope(t *testing.T) {
	assertUntouched := func(t *testing.T, h *webhookHarness) {
		t.Helper()
		claim, payload, release := h.store.counts()
		if claim != 0 || payload != 0 || release != 0 {
			t.Errorf("claim/payload/release = %d/%d/%d, want 0/0/0", claim, payload, release)
		}
		if got := h.enqueuer.count(); got != 0 {
			t.Errorf("enqueue = %d, want 0", got)
		}
		if got := h.engine.count(); got != 0 {
			t.Errorf("engine calls = %d, want 0", got)
		}
	}

	t.Run("non-message events are ignored", func(t *testing.T) {
		events := []string{
			"Presence", "Receipt", "ChatPresence", "ConnectionUpdate", "Label",
			"QrcodeUpdated", "Picture", "UserAbout", "HistorySync", "Contact",
			"Group", "Newsletter", "ButtonClick",
		}
		for _, event := range events {
			t.Run(event, func(t *testing.T) {
				h := newWebhookHarness(t, testWebhookSecret)
				token := DeriveWebhookToken(testWebhookSecret, 1)

				rec := h.do(httptest.NewRequest(
					http.MethodPost,
					webhookPath("1", token),
					bytes.NewReader(fmt.Appendf(nil, `{"event":%q}`, event)),
				))

				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
				}
				assertJSONField(t, rec, "status", "ignored")
				assertUntouched(t, h)
			})
		}
	})

	t.Run("no event field is ignored", func(t *testing.T) {
		h := newWebhookHarness(t, testWebhookSecret)
		token := DeriveWebhookToken(testWebhookSecret, 1)

		rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), strings.NewReader(`{}`)))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		assertJSONField(t, rec, "status", "ignored")
		assertUntouched(t, h)
	})

	t.Run("non-object bodies stay 400", func(t *testing.T) {
		for _, body := range []string{"null", "[]", `"str"`, "123", "true", "", " \t\n", `{"event":123}`} {
			t.Run(body, func(t *testing.T) {
				h := newWebhookHarness(t, testWebhookSecret)
				token := DeriveWebhookToken(testWebhookSecret, 1)

				rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), strings.NewReader(body)))

				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
				}
				assertUntouched(t, h)
			})
		}
	})

	t.Run("padded object is still read", func(t *testing.T) {
		h := newWebhookHarness(t, testWebhookSecret)
		token := DeriveWebhookToken(testWebhookSecret, 1)

		rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), strings.NewReader("  {\"event\":\"Presence\"}\n")))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		assertJSONField(t, rec, "status", "ignored")
		assertUntouched(t, h)
	})

	t.Run("accepted name without usable data stays 400", func(t *testing.T) {
		for _, body := range []string{
			`{"event":"Message"}`,
			`{"event":"Message","data":null}`,
			`{"event":"Message","data":"nope"}`,
			`{"event":"SendMessage"}`,
		} {
			t.Run(body, func(t *testing.T) {
				h := newWebhookHarness(t, testWebhookSecret)
				token := DeriveWebhookToken(testWebhookSecret, 1)

				rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), strings.NewReader(body)))

				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
				}
				assertUntouched(t, h)
			})
		}
	})

	t.Run("accepted name with valid data still accepted", func(t *testing.T) {
		h := newWebhookHarness(t, testWebhookSecret)
		token := DeriveWebhookToken(testWebhookSecret, 1)

		rec := h.do(httptest.NewRequest(http.MethodPost, webhookPath("1", token), bytes.NewReader(wellFormedPayload)))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		assertJSONField(t, rec, "status", "accepted")
		claim, payload, release := h.store.counts()
		if claim != 1 || payload != 1 || release != 0 {
			t.Errorf("claim/payload/release = %d/%d/%d, want 1/1/0", claim, payload, release)
		}
		if got := h.enqueuer.count(); got != 1 {
			t.Errorf("enqueue = %d, want 1", got)
		}
	})
}
