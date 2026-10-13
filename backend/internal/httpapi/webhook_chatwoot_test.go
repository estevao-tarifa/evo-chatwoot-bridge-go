package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

const testChatwootWebhookSecret = "synthetic-chatwoot-account-secret"

// wellFormedChatwootPayload is a synthetic CW message_created (no real client
// phone numbers, names or group ids; AGENTS §9).
var wellFormedChatwootPayload = []byte(`{"event":"message_created","id":10001,"content":"hello","message_type":"outgoing","private":false,"conversation":{"id":5001,"inbox_id":10},"sender":{"type":"user","name":"Agent Synthetic"}}`)

// newChatwootHarness builds the full server chain with the Chatwoot route.
func newChatwootHarness(t *testing.T, tokenSecret, chatwootSecret string) *webhookHarness {
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
	handler := NewChatwootWebhookHandler(ChatwootWebhookOptions{
		Secret:         tokenSecret,
		ChatwootSecret: chatwootSecret,
		Registry:       h.registry,
		Store:          h.store,
		Enqueuer:       h.enqueuer,
		Logger:         logger,
	})
	h.srv = NewServer(Options{Logger: logger, ChatwootWebhook: handler})
	return h
}

func chatwootWebhookPath(connectionID, token string) string {
	return "/api/webhooks/chatwoot/" + connectionID + "/" + token
}

// TestSlice5_CClaim proves a valid CW message_created is accepted with exactly
// one claim, one audit write, one enqueue and no entity in the handler.
func TestSlice5_CClaim(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload)))

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
}

// TestSlice5_CAuth proves a bad/absent token is 401 before the body is read.
func TestSlice5_CAuth(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{name: "absent token", token: ""},
		{name: "wrong token", token: "not-the-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newChatwootHarness(t, testWebhookSecret, "")
			req := httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", tt.token), nil)
			body := &trackingBody{}
			req.Body = body

			rec := h.do(req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if body.reads != 0 {
				t.Errorf("body reads = %d, want 0", body.reads)
			}
			if claim, _, _ := h.store.counts(); claim != 0 || h.enqueuer.count() != 0 {
				t.Errorf("claim/enqueue = %d/%d, want 0/0", claim, h.enqueuer.count())
			}
		})
	}
}

// TestSlice5_CHMACFailClosed proves that when CHATWOOT_WEBHOOK_SECRET is set the
// route requires a valid signature and never silently downgrades to token-only.
func TestSlice5_CHMACFailClosed(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, testChatwootWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)
	now := time.Now()
	timestamp := strconv.FormatInt(now.Unix(), 10)

	tests := []struct {
		name      string
		timestamp string
		signature string
	}{
		{name: "missing signature", timestamp: timestamp, signature: ""},
		{name: "invalid signature", timestamp: timestamp, signature: "sha256=deadbeef"},
		{name: "expired timestamp", timestamp: strconv.FormatInt(now.Add(-time.Hour).Unix(), 10),
			signature: signChatwoot(testChatwootWebhookSecret, strconv.FormatInt(now.Add(-time.Hour).Unix(), 10), wellFormedChatwootPayload)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload))
			req.Header.Set(chatwootTimestampHeader, tt.timestamp)
			req.Header.Set(chatwootSignatureHeader, tt.signature)

			rec := h.do(req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if claim, _, _ := h.store.counts(); claim != 0 || h.enqueuer.count() != 0 {
				t.Errorf("claim/enqueue = %d/%d, want 0/0 (no downgrade)", claim, h.enqueuer.count())
			}
		})
	}
}

// TestSlice5_CHMACValid proves a correctly signed request is accepted.
func TestSlice5_CHMACValid(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, testChatwootWebhookSecret)
	token := DeriveWebhookToken(testWebhookSecret, 1)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	req := httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload))
	req.Header.Set(chatwootTimestampHeader, timestamp)
	req.Header.Set(chatwootSignatureHeader, signChatwoot(testChatwootWebhookSecret, timestamp, wellFormedChatwootPayload))

	rec := h.do(req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if claim, _, _ := h.store.counts(); claim != 1 {
		t.Errorf("claim = %d, want 1", claim)
	}
}

// TestSlice5_CUnsigned proves the default (no CHATWOOT_WEBHOOK_SECRET) is
// token-only unsigned delivery.
func TestSlice5_CUnsigned(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertJSONField(t, rec, "status", "accepted")
}

// TestSlice5_CIgnore proves a non-message_created event is 200 ignored with no
// claim (F1 pre-filter).
func TestSlice5_CIgnore(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)
	payload := []byte(`{"event":"conversation_created","id":5001}`)

	rec := h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(payload)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertJSONField(t, rec, "status", "ignored")
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
	if got := h.enqueuer.count(); got != 0 {
		t.Errorf("enqueue = %d, want 0", got)
	}
}

// TestSlice5_CCapDeclared proves a declared body over 16 MiB is 413 with no claim.
func TestSlice5_CCapDeclared(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(make([]byte, maxBodyBytes+1))))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
}

// TestSlice5_CCapMidRead proves a chunked body over the cap fails while reading
// with 413 and no claim.
func TestSlice5_CCapMidRead(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	req := httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(make([]byte, maxBodyBytes+1)))
	req.ContentLength = -1 // simulate chunked encoding
	rec := h.do(req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
}

// TestSlice5_CMalformed proves malformed JSON and a message with no id are 400
// with no claim.
func TestSlice5_CMalformed(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	for _, body := range []string{"{not json", `{"event":"message_created","message_type":"outgoing"}`} {
		rec := h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for %q", rec.Code, body)
		}
	}
	if claim, _, _ := h.store.counts(); claim != 0 {
		t.Errorf("claim = %d, want 0", claim)
	}
}

// TestSlice5_CDuplicate proves a re-delivered CW id is 200 duplicate with no
// enqueue.
func TestSlice5_CDuplicate(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	h.store.claimed = false
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertJSONField(t, rec, "status", "duplicate")
	if got := h.enqueuer.count(); got != 0 {
		t.Errorf("enqueue = %d, want 0", got)
	}
	if claim, payload, _ := h.store.counts(); claim != 1 || payload != 0 {
		t.Errorf("claim/payload = %d/%d, want 1/0", claim, payload)
	}
}

// TestSlice5_CStoreFail proves a StorePayload failure is 503 with one release
// and no enqueue.
func TestSlice5_CStoreFail(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	h.store.storeErr = errors.New("audit write failed")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload)))

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

// TestSlice5_CQueueFull proves a full queue is 503 + Retry-After with one
// release.
func TestSlice5_CQueueFull(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	h.enqueuer.ok = false
	token := DeriveWebhookToken(testWebhookSecret, 1)

	rec := h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload)))

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

// TestSlice5_CNoReleaseAfterAdmission proves a request-context cancellation
// after a successful enqueue does not release the admitted claim.
func TestSlice5_CNoReleaseAfterAdmission(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.enqueuer.onEnqueue = cancel

	req := httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload)).WithContext(ctx)
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

// TestSlice5_CMask proves the CW path token never reaches the access log and
// the path is masked.
func TestSlice5_CMask(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)

	h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload)))

	logs := h.logs.String()
	if !strings.Contains(logs, `"path":"/api/webhooks/chatwoot/***"`) {
		t.Errorf("access log does not mask the CW webhook path: %s", logs)
	}
	if strings.Contains(logs, token) {
		t.Errorf("access log leaked the CW token: %s", logs)
	}
}

// TestSlice5_CSanitize proves the stored CW audit envelope never carries the
// content, an attachment data_url/url, the sender email, a token or the raw
// payload.
func TestSlice5_CSanitize(t *testing.T) {
	h := newChatwootHarness(t, testWebhookSecret, "")
	token := DeriveWebhookToken(testWebhookSecret, 1)
	payload := []byte(`{"event":"message_created","id":10001,"content":"SECRET-CONTENT","message_type":"outgoing",
		"token":"SECRET-TOKEN","conversation":{"id":5001,"inbox_id":10},
		"sender":{"type":"user","email":"SECRET-EMAIL@example.invalid"},
		"attachments":[{"file_type":"image","data_url":"https://chatwoot.invalid/SECRET-DATA-URL"}]}`)

	h.do(httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(payload)))

	envelope := string(h.store.lastEnvelope)
	for _, secret := range []string{"SECRET-CONTENT", "SECRET-TOKEN", "SECRET-EMAIL", "SECRET-DATA-URL", "data_url"} {
		if strings.Contains(envelope, secret) {
			t.Errorf("stored CW envelope leaked %q: %s", secret, envelope)
		}
	}
	if !strings.Contains(envelope, `"system":"chatwoot"`) {
		t.Errorf("stored CW envelope is not the Chatwoot projection: %s", envelope)
	}
}

// ─── E13 end-to-end: two deliveries, one send ────────────────────────────────

// claimStore is an IngressStore with real claim semantics: the first claim of a
// key wins, a re-delivery loses.
type claimStore struct {
	mu       sync.Mutex
	seen     map[string]bool
	payloads int
	releases int
}

func (s *claimStore) Claim(_ context.Context, key bridge.Key, _ time.Duration) (bool, int64, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fmt.Sprintf("%d|%s|%s|%s", key.ConnectionID, key.SourceSystem, key.EventType, key.SourceMessageID)
	if s.seen[k] {
		return false, 7, "", nil
	}
	s.seen[k] = true
	return true, 7, "0123456789abcdef0123456789abcdef", nil
}

func (s *claimStore) StorePayload(_ context.Context, _ int64, _ string, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads++
	return nil
}

func (s *claimStore) Release(_ context.Context, _ int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases++
	return nil
}

// capturingEnqueuer records admitted jobs so a test can run them in-line.
type capturingEnqueuer struct {
	mu   sync.Mutex
	ok   bool
	jobs []func(context.Context) error
}

func (e *capturingEnqueuer) TryEnqueue(job func(context.Context) error, _ int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.ok {
		return false
	}
	e.jobs = append(e.jobs, job)
	return true
}

// runAll drains and runs every admitted job (each job runs once).
func (e *capturingEnqueuer) runAll() {
	e.mu.Lock()
	jobs := e.jobs
	e.jobs = nil
	e.mu.Unlock()
	for _, job := range jobs {
		_ = job(context.Background())
	}
}

// sendCountingEngine counts Chatwoot→WhatsApp sends.
type sendCountingEngine struct {
	mu    sync.Mutex
	sends int
}

func (e *sendCountingEngine) HandleClaimed(_ context.Context, _ bridge.Event, _ int64, _ string) (bridge.Outcome, error) {
	return bridge.OutcomeIgnored, nil
}

func (e *sendCountingEngine) HandleChatwootClaimed(_ context.Context, _ bridge.Event, _ int64, _ string) (bridge.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sends++
	return bridge.OutcomeForwarded, nil
}

func (e *sendCountingEngine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sends
}

// TestSlice5_C13_DuplicateDelivery proves E13 end-to-end: the second delivery of
// the same CW id loses the ingress claim (200 duplicate) and is never enqueued,
// so the total number of sends stays 1.
func TestSlice5_C13_DuplicateDelivery(t *testing.T) {
	logger, _ := testLogger()
	store := &claimStore{seen: map[string]bool{}}
	enqueuer := &capturingEnqueuer{ok: true}
	engine := &sendCountingEngine{}
	reg := &fakeRegistry{
		conns:  map[int]bridge.Connection{1: {ID: 1, ChatwootInboxID: 10, IsActive: true}},
		engine: engine,
	}
	handler := NewChatwootWebhookHandler(ChatwootWebhookOptions{
		Secret: testWebhookSecret, Registry: reg, Store: store, Enqueuer: enqueuer, Logger: logger,
	})
	srv := NewServer(Options{Logger: logger, ChatwootWebhook: handler})
	token := DeriveWebhookToken(testWebhookSecret, 1)

	deliver := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, chatwootWebhookPath("1", token), bytes.NewReader(wellFormedChatwootPayload)))
		return rec
	}

	if rec := deliver(); rec.Code != http.StatusOK {
		t.Fatalf("first delivery status = %d, want 200", rec.Code)
	}
	enqueuer.runAll()

	rec := deliver()
	if rec.Code != http.StatusOK {
		t.Fatalf("second delivery status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "duplicate") {
		t.Errorf("second delivery body = %s, want duplicate", body)
	}
	enqueuer.runAll()

	if got := engine.count(); got != 1 {
		t.Errorf("Chatwoot→WhatsApp sends = %d, want 1 (E13)", got)
	}
}
