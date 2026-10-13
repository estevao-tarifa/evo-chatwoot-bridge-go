package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/observability"
)

// defaultClaimTTL is the dedup retention window (ADR-0004 §6).
const defaultClaimTTL = 7 * 24 * time.Hour

// retryAfterSeconds is advertised on a pre-admission 503 (queue full) so the
// sender retries after the backlog drains.
const retryAfterSeconds = 5

// Chatwoot signature header names (webhooks.md §4). The account HMAC is only
// required when CHATWOOT_WEBHOOK_SECRET is configured (ADR-0006 Amendment 3).
const (
	chatwootSignatureHeader = "X-Chatwoot-Signature"
	chatwootTimestampHeader = "X-Chatwoot-Timestamp"
)

// chatwootMessageCreated is the only Chatwoot event the bridge acts on (F1).
const chatwootMessageCreated = "message_created"

// evolutionMessageEvents is the accepted Evolution→Chatwoot event set: exactly
// the legacy MSG_EVENTS names, matched case-sensitively
// (evo-chatwoot-bridge/server/services/bridgeProcessor.ts:501; frozen contract
// docs/contracts/evolution/webhooks.md §2). Every other event — Presence,
// Receipt, ChatPresence, ConnectionUpdate, Label, QrcodeUpdated, Picture,
// UserAbout, HistorySync, Contact, Group, Newsletter, ButtonClick, and an
// absent/empty `event` — is dropped pre-claim, so it is never stored, claimed or
// forwarded. Those payloads parse into an identity whose MessageType is
// unknown, which forwardableType treats as forwardable, so without this filter
// they would reach the client's Chatwoot as `[unknown]` messages and create
// spurious contacts/conversations (AGENTS.md §7).
//
// Call events are deliberately NOT accepted. The legacy handled
// `call`/`CALL`/`Call` via handleCallEvent (bridgeProcessor.ts:502-504):
// rejection only, never a Chatwoot message. Evolution GO 0.7.2 emits the
// different names CallOffer/CallAccept/CallTerminate/CallOfferNotice/
// CallRelayLatency (webhooks.md §2, `[needs live validation]`). This engine
// implements no call handling, so accepting a call event would fall through to
// the message path and create the very `[unknown]` conversation the legacy
// never produced; dropping calls keeps the Chatwoot-visible behaviour identical.
var evolutionMessageEvents = map[string]struct{}{
	"Message":         {},
	"MESSAGE":         {},
	"messages.upsert": {},
	"MESSAGES_UPSERT": {},
	"SendMessage":     {},
	"SENDMESSAGE":     {},
}

// ignoreEvolutionEvent is the pre-claim Evolution ingress filter used as the
// ingressSource.Ignore seam: true drops the delivery with no claim, no audit
// write and no enqueue. An empty/absent `event` is not accepted (legacy
// `String(payload.event || "")` is never in MSG_EVENTS).
func ignoreEvolutionEvent(eventType string) bool {
	_, accepted := evolutionMessageEvents[eventType]
	return !accepted
}

// Enqueuer is the consumer-side port over the bounded worker queue. TryEnqueue
// is non-blocking; false means the queue is full and the job was not admitted.
type Enqueuer interface {
	TryEnqueue(job func(context.Context) error, size int) bool
}

// IngressStore is the consumer-side port over the persistent claim repository.
// Claim builds the dedup key; StorePayload writes the sanitized audit envelope;
// Release removes a claim admitted but not delivered. It is satisfied by the
// MySQL dedup adapter, so httpapi imports neither storage/mysql nor dedup.
type IngressStore interface {
	Claim(ctx context.Context, key bridge.Key, ttl time.Duration) (claimed bool, id int64, token string, err error)
	StorePayload(ctx context.Context, id int64, token string, payload []byte) error
	Release(ctx context.Context, id int64, token string) error
}

// IngressEngine handles a claim the ingress already won: HandleClaimed for the
// Evolution direction, HandleChatwootClaimed for the Chatwoot direction.
type IngressEngine interface {
	HandleClaimed(ctx context.Context, ev bridge.Event, claimID int64, token string) (bridge.Outcome, error)
	HandleChatwootClaimed(ctx context.Context, ev bridge.Event, claimID int64, token string) (bridge.Outcome, error)
}

// ConnectionRegistry resolves a webhook's connection id to its runtime. ok is
// false for an unknown connection; an inactive connection returns ok=true with
// a false IsActive so the handler fails closed with 403.
type ConnectionRegistry interface {
	Lookup(connectionID int) (bridge.Connection, IngressEngine, bool)
}

// ingressSource parameterises the shared claim-before-ACK core. One core serves
// both the Evolution and Chatwoot routes so their auth, cap, claim-before-ACK
// and release semantics are byte-identical (spec §1.1).
type ingressSource struct {
	SourceSystem string // dedup key source_system
	Parse        func([]byte) (eventType, messageID string, err error)
	Ignore       func(eventType string) bool // pre-claim drop (nil = never)
	Sanitize     func([]byte) []byte         // audit envelope (observability)
	Deliver      func(ctx context.Context, engine IngressEngine, ev bridge.Event, id int64, token string) error
	Signed       func(r *http.Request, body []byte) bool // nil = unsigned (token-only)
}

// WebhookOptions configures the Evolution webhook ingress handler.
type WebhookOptions struct {
	// Secret is WEBHOOK_TOKEN_SECRET. An empty secret disables authentication:
	// the handler answers 503 rather than deriving a predictable token.
	Secret   string
	Registry ConnectionRegistry
	Store    IngressStore
	Enqueuer Enqueuer
	Logger   *slog.Logger
	ClaimTTL time.Duration
}

// ChatwootWebhookOptions configures the Chatwoot webhook ingress handler. The
// path token is derived from Secret exactly like Evolution; ChatwootSecret is
// the optional account HMAC secret (empty = unsigned API-inbox mode).
type ChatwootWebhookOptions struct {
	Secret         string
	ChatwootSecret string
	Registry       ConnectionRegistry
	Store          IngressStore
	Enqueuer       Enqueuer
	Logger         *slog.Logger
	ClaimTTL       time.Duration
}

type webhookHandler struct {
	secret   string
	registry ConnectionRegistry
	store    IngressStore
	enqueuer Enqueuer
	logger   *slog.Logger
	claimTTL time.Duration
	source   ingressSource
}

// NewWebhookHandler builds the Evolution webhook ingress handler
// (POST /api/webhooks/evolution/{connectionID}/{token}).
func NewWebhookHandler(opts WebhookOptions) http.Handler {
	return newWebhookHandler(webhookHandler{
		secret:   opts.Secret,
		registry: opts.Registry,
		store:    opts.Store,
		enqueuer: opts.Enqueuer,
		logger:   opts.Logger,
		claimTTL: opts.ClaimTTL,
		source:   evolutionSource(),
	})
}

// NewChatwootWebhookHandler builds the Chatwoot webhook ingress handler
// (POST /api/webhooks/chatwoot/{connectionID}/{token}).
func NewChatwootWebhookHandler(opts ChatwootWebhookOptions) http.Handler {
	return newWebhookHandler(webhookHandler{
		secret:   opts.Secret,
		registry: opts.Registry,
		store:    opts.Store,
		enqueuer: opts.Enqueuer,
		logger:   opts.Logger,
		claimTTL: opts.ClaimTTL,
		source:   chatwootSource(opts.ChatwootSecret),
	})
}

func newWebhookHandler(h webhookHandler) http.Handler {
	if h.logger == nil {
		h.logger = slog.Default()
	}
	if h.claimTTL <= 0 {
		h.claimTTL = defaultClaimTTL
	}
	return &h
}

// evolutionSource is the Evolution→Chatwoot ingress source: token-only
// (unsigned), the message-event allowlist as pre-claim ignore and
// HandleClaimed.
func evolutionSource() ingressSource {
	return ingressSource{
		SourceSystem: bridge.SourceEvolution,
		Parse:        evolutionParse,
		Ignore:       ignoreEvolutionEvent,
		Sanitize:     observability.SanitizeWebhookPayload,
		Deliver: func(ctx context.Context, engine IngressEngine, ev bridge.Event, id int64, token string) error {
			_, err := engine.HandleClaimed(ctx, ev, id, token)
			return err
		},
		Signed: nil,
	}
}

// chatwootSource is the Chatwoot→WhatsApp ingress source: F1 pre-filter, the
// Chatwoot audit sanitizer, HandleChatwootClaimed and, when secret is set, the
// fail-closed account HMAC (never a silent downgrade to token-only).
func chatwootSource(secret string) ingressSource {
	var signed func(r *http.Request, body []byte) bool
	if secret != "" {
		signed = func(r *http.Request, body []byte) bool {
			return ChatwootSignatureValid(
				secret,
				r.Header.Get(chatwootTimestampHeader),
				body,
				r.Header.Get(chatwootSignatureHeader),
				time.Now(),
			)
		}
	}
	return ingressSource{
		SourceSystem: bridge.SourceChatwoot,
		Parse:        chatwootParse,
		Ignore:       func(eventType string) bool { return eventType != chatwootMessageCreated },
		Sanitize:     observability.SanitizeChatwootPayload,
		Deliver: func(ctx context.Context, engine IngressEngine, ev bridge.Event, id int64, token string) error {
			_, err := engine.HandleChatwootClaimed(ctx, ev, id, token)
			return err
		},
		Signed: signed,
	}
}

// evolutionParse adapts bridge.Parse to the source descriptor. bridge.Parse
// fails when the envelope carries no `data` object (a bare Presence/
// ConnectionUpdate during a reconnect). The legacy ACKed every delivery 200, so
// the raw `event` field is decoded on its own in that case and returned for a
// name outside the accepted set, letting the shared Ignore core answer 200
// `ignored` instead of an ingress retry-triggering 400. An accepted name keeps
// the original error: a malformed message body stays a fail-closed 400.
func evolutionParse(body []byte) (eventType, messageID string, err error) {
	parsed, err := bridge.Parse(body)
	if err == nil {
		return parsed.EventName, parsed.MessageID, nil
	}
	if name, ok := rawEventName(body); ok && ignoreEvolutionEvent(name) {
		return name, "", nil
	}
	return "", "", err
}

// rawEventName decodes only the top-level `event` field of a webhook envelope.
// ok is false unless the body's first non-whitespace byte opens a JSON object:
// `null`, `[]`, `"str"`, `123`, `true`, an empty body and malformed JSON are not
// envelopes, so the caller keeps bridge.Parse's error instead of reading an
// empty name off them. An absent or null `event` field yields ("", true) — an
// empty name is not accepted.
func rawEventName(body []byte) (name string, ok bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", false
	}
	var envelope struct {
		Event *string `json:"event"`
	}
	if json.Unmarshal(trimmed, &envelope) != nil {
		return "", false
	}
	if envelope.Event == nil {
		return "", true
	}
	return *envelope.Event, true
}

// chatwootParse adapts bridge.ParseChatwoot to the source descriptor. A
// non-`message_created` envelope with no id still parses so it can be ignored.
func chatwootParse(body []byte) (eventType, messageID string, err error) {
	ev, err := bridge.ParseChatwoot(body)
	if err != nil {
		return "", "", err
	}
	return ev.EventName, ev.MessageID, nil
}

// ServeHTTP implements the claim-before-ACK flow (spec §1.4) for the handler's
// source.
func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.claimAndEnqueue(w, r, h.source)
}

// claimAndEnqueue is the shared claim-before-ACK core: token first (cheap, from
// the path, before the body is read), then the registry, the body, the optional
// signature, parse/ignore, claim, the sanitized audit write and a non-blocking
// enqueue. Release happens only before admission; the enqueue and the release
// are never gated on r.Context(), so a post-admission cancellation cannot
// release the claim.
func (h *webhookHandler) claimAndEnqueue(w http.ResponseWriter, r *http.Request, src ingressSource) {
	// 1. connection id
	connectionID, err := strconv.Atoi(r.PathValue("connectionID"))
	if err != nil || connectionID <= 0 || uint64(connectionID) > 1<<32-1 {
		writeJSON(h.logger, w, http.StatusNotFound, errorResponse{Error: "not found"})
		return
	}

	// 2. token: verified before the body is read, so an unauthenticated caller
	// never forces a 16 MiB read. An empty secret must never derive a token.
	if h.secret == "" {
		writeJSON(h.logger, w, http.StatusServiceUnavailable, errorResponse{Error: "webhook ingress not configured"})
		return
	}
	if !verifyWebhookToken(h.secret, connectionID, r.PathValue("token")) {
		writeJSON(h.logger, w, http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}

	// 3. registry: unknown → 404, inactive → 403 (fails closed, no claim).
	conn, engine, ok := h.registry.Lookup(connectionID)
	if !ok {
		writeJSON(h.logger, w, http.StatusNotFound, errorResponse{Error: "not found"})
		return
	}
	if !conn.IsActive {
		writeJSON(h.logger, w, http.StatusForbidden, errorResponse{Error: "connection inactive"})
		return
	}
	if engine == nil {
		writeJSON(h.logger, w, http.StatusServiceUnavailable, errorResponse{Error: "connection unavailable"})
		return
	}

	// 4. the declared-body cap is enforced by the bodyLimit middleware.
	// 5. read the body: overflow → 413, other read error → 400.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(h.logger, w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		writeJSON(h.logger, w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	// 6. optional signed mode (Chatwoot account HMAC): fail closed, after the
	// body read (it needs the raw bytes) and before parse/claim, so a rejected
	// request touches no port.
	if src.Signed != nil && !src.Signed(r, body) {
		writeJSON(h.logger, w, http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}

	// 7. parse: an unparseable envelope is 400. A pre-claim ignore (F1) answers
	// 200 ignored with no claim; an empty message id has no dedup key → 400.
	eventType, messageID, err := src.Parse(body)
	if err != nil {
		writeJSON(h.logger, w, http.StatusBadRequest, errorResponse{Error: "invalid webhook payload"})
		return
	}
	if src.Ignore != nil && src.Ignore(eventType) {
		writeJSON(h.logger, w, http.StatusOK, acceptedResponse{Status: "ignored"})
		return
	}
	if messageID == "" {
		writeJSON(h.logger, w, http.StatusBadRequest, errorResponse{Error: "invalid webhook payload"})
		return
	}

	ctx := r.Context()

	// 8. claim before ACK.
	key := bridge.Key{
		ConnectionID:    connectionID,
		SourceSystem:    src.SourceSystem,
		EventType:       eventType,
		SourceMessageID: messageID,
	}
	claimed, id, token, err := h.store.Claim(ctx, key, h.claimTTL)
	if err != nil {
		h.logger.Error("webhook: claim failed", "connection_id", connectionID, "error", err)
		writeJSON(h.logger, w, http.StatusServiceUnavailable, errorResponse{Error: "temporarily unavailable"})
		return
	}
	if !claimed {
		writeJSON(h.logger, w, http.StatusOK, acceptedResponse{Status: "duplicate"})
		return
	}

	// 9. sanitized audit write. A failure is never reported as success: release
	// the pre-admission claim and 503 (ADR-0004 §1).
	envelope := src.Sanitize(body)
	if err := h.store.StorePayload(ctx, id, token, envelope); err != nil {
		h.logger.Error("webhook: audit write failed", "connection_id", connectionID, "error", err)
		h.release(ctx, id, token)
		writeJSON(h.logger, w, http.StatusServiceUnavailable, errorResponse{Error: "temporarily unavailable"})
		return
	}

	// 10. non-blocking enqueue. The job runs on the worker's process context,
	// never r.Context(): the claim is not released on post-admission
	// cancellation.
	correlationID := observability.CorrelationID(ctx)
	job := func(jobCtx context.Context) error {
		return src.Deliver(
			observability.WithCorrelationID(jobCtx, correlationID),
			engine,
			bridge.Event{Connection: conn, Payload: body},
			id, token,
		)
	}
	if !h.enqueuer.TryEnqueue(job, len(body)) {
		h.logger.Warn("webhook: queue full, releasing pre-admission claim", "connection_id", connectionID)
		h.release(ctx, id, token)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
		writeJSON(h.logger, w, http.StatusServiceUnavailable, errorResponse{Error: "temporarily unavailable"})
		return
	}

	// 11. accepted/observed, not delivered.
	writeJSON(h.logger, w, http.StatusOK, acceptedResponse{Status: "accepted"})
}

// release removes a pre-admission claim. A failure is logged (the row stays
// 'processing' for the next-boot reconcile), never swallowed.
func (h *webhookHandler) release(ctx context.Context, id int64, token string) {
	if err := h.store.Release(ctx, id, token); err != nil {
		h.logger.Error("webhook: release failed", "claim_id", id, "error", err)
	}
}

type acceptedResponse struct {
	Status string `json:"status"`
}
