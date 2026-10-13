package bridge

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

func TestEngine_NameUpdateOnSearchHit(t *testing.T) {
	h := newHarness()
	h.cw.searchResults = []Contact{{ID: 5, Name: "Old", Identifier: testPhone}}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "N1", pushName: "Ana",
		msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	// A search-resolved contact exposes its name, so the authoritative push name
	// updates it; a mapping reuse never would (see TestG0_E1_mapping_exists).
	h.assertCounts(t, counts{MS: 1, MP: 2, NU: 1, LG: 1})
}

func TestEngine_MappingReuseNeverUpdatesName(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "N2", pushName: "Ana",
		msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{MS: 1, LG: 1}) // NU0: the name is unknown, never overwritten
}

func TestBuildContent_GroupPrefix(t *testing.T) {
	parsed := &ParsedEvent{PushName: "Ana", MessageType: MessageTypeText, Text: "bom dia"}
	if got := buildContent(parsed, Identity{Kind: KindGroup}); got != "*Ana:*\nbom dia" {
		t.Errorf("buildContent() = %q", got)
	}
	if got := buildContent(parsed, Identity{Kind: KindDirect}); got != "bom dia" {
		t.Errorf("buildContent() = %q", got)
	}
}

func TestEngine_EchoLookupErrorSurfaced(t *testing.T) {
	h := newHarness()
	h.echo.err = errSentinel

	_, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testChatJID, sender: testChatJID, id: "N3",
		isFromMe: true, msgType: "conversation", text: "x",
	}))
	if err == nil {
		t.Fatal("HandleEvent error = nil, want the echo lookup error")
	}
	h.assertCounts(t, counts{}) // no port effect
}

// TestEngine_MalformedPayloadDistinctOutcome covers M4: an unparseable payload
// gets its own outcome and a typed error so ingress can answer 400, instead of
// sharing OutcomeIgnored with the E7/E8 drops.
func TestEngine_MalformedPayloadDistinctOutcome(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":     `{`,
		"empty":        ``,
		"null payload": `null`,
		"no data":      `{"event":"Message"}`,
		"null data":    `{"event":"Message","data":null}`,
		"bad data":     `{"event":"Message","data":123}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			out, err := h.handle(t, []byte(payload))
			if out != OutcomeMalformed {
				t.Fatalf("HandleEvent = %v, want malformed", out)
			}
			if !errors.Is(err, ErrMalformedPayload) {
				t.Fatalf("HandleEvent error = %v, want ErrMalformedPayload", err)
			}
			h.assertCounts(t, counts{}) // zero port calls, no entity
		})
	}
}

func TestEngine_MissingMessageIDFailsClosed(t *testing.T) {
	h := newHarness()
	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleEvent = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{}) // zero port calls
}

func TestEngine_InactiveConnectionNoPorts(t *testing.T) {
	h := newHarness()
	h.conn.IsActive = false
	h.echo.err = errSentinel // must never be reached

	out, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testChatJID, sender: testChatJID, id: "N4",
		isFromMe: true, msgType: "conversation", text: "x",
	}))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleEvent = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{})
}

// retryableErr classifies itself as safe to retry (like a Chatwoot GET error).
type retryableErr struct{}

func (retryableErr) Error() string   { return "retryable" }
func (retryableErr) Retryable() bool { return true }

func TestRetryRead_RetriesRetryable(t *testing.T) {
	attempts := 0
	got, err := retryRead(context.Background(), fastReadRetry(), func() (string, error) {
		attempts++
		if attempts < 2 {
			return "", retryableErr{}
		}
		return "ok", nil
	})
	if err != nil || got != "ok" || attempts != 2 {
		t.Errorf("retryRead() = %q, %v, attempts=%d; want ok, nil, 2", got, err, attempts)
	}
}

func TestRetryRead_StopsOnNonRetryable(t *testing.T) {
	attempts := 0
	_, err := retryRead(context.Background(), fastReadRetry(), func() (string, error) {
		attempts++
		return "", errSentinel
	})
	if err == nil || attempts != 1 {
		t.Errorf("retryRead() err=%v attempts=%d; want error after 1 attempt", err, attempts)
	}
}

func TestRetryRead_HonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := retryRead(ctx, fastReadRetry(), func() (string, error) { return "", retryableErr{} }); err == nil {
		t.Error("retryRead() error = nil, want context.Canceled")
	}
}

// TestReadRetryPolicy_BackoffCapsAndJitters pins the injectable jitter and the
// cap so the delay is deterministic under test (M2).
func TestReadRetryPolicy_BackoffCapsAndJitters(t *testing.T) {
	pinned := readRetryPolicy{
		attempts: 3,
		base:     10 * time.Millisecond,
		max:      time.Second,
		jitter:   func(d time.Duration) time.Duration { return d + time.Second },
	}
	if got := pinned.backoff(1); got != 10*time.Millisecond+time.Second {
		t.Errorf("backoff(1) = %v, want 10ms+jitter", got)
	}
	if got := pinned.backoff(2); got != 20*time.Millisecond+time.Second {
		t.Errorf("backoff(2) = %v, want 20ms+jitter", got)
	}

	capped := readRetryPolicy{base: 40 * time.Millisecond, max: 50 * time.Millisecond}
	if got := capped.backoff(2); got != 50*time.Millisecond {
		t.Errorf("backoff(2) = %v, want cap 50ms", got)
	}
}

// TestEngine_EchoLookupRetriesReadOnly proves the echo lookup is routed through
// the bounded read retry (M2): one retryable failure is retried, not surfaced.
func TestEngine_EchoLookupRetriesReadOnly(t *testing.T) {
	h := newHarness()
	h.useFastRetry()
	h.echo.err = retryableErr{}
	h.echo.errUntil = 1 // first lookup fails, the retry succeeds
	h.echo.sent[echoKey(1, "E1")] = true

	out, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testChatJID, sender: testChatJID, id: "E1",
		isFromMe: true, msgType: "conversation", text: "eco",
	}))
	if err != nil || out != OutcomeEcho {
		t.Fatalf("HandleEvent = %v, %v; want echo after retry", out, err)
	}
	if h.echo.calls != 2 {
		t.Errorf("echo lookups = %d, want 2 (one retry)", h.echo.calls)
	}
	h.assertCounts(t, counts{LG: 1})
}

// TestEngine_MutatingSendNeverRetried proves a mutating call is never retried
// even when its error claims to be retryable (M2): one attempt, then quarantine.
func TestEngine_MutatingSendNeverRetried(t *testing.T) {
	h := newHarness()
	h.useFastRetry()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}
	h.cw.sendErr = retryableErr{}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "E2", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeQuarantined {
		t.Fatalf("HandleEvent = %v, %v; want quarantined", out, err)
	}
	h.assertCounts(t, counts{MS: 1, LG: 1}) // exactly one send attempt
	if h.dedup.unconfirmed != 1 {
		t.Errorf("MarkUnconfirmed calls = %d, want 1", h.dedup.unconfirmed)
	}
}

// TestEngine_SpawnsNoGoroutines is the §6 no-leak obligation: HandleEvent is
// synchronous and must not retain goroutines across calls. go.mod has no goleak,
// so the check is a runtime count delta.
func TestEngine_SpawnsNoGoroutines(t *testing.T) {
	h := newHarness()
	if _, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "WARM", msgType: "conversation", text: "x",
	})); err != nil {
		t.Fatalf("warm-up HandleEvent error = %v", err)
	}

	before := runtime.NumGoroutine()
	for i := range 25 {
		if _, err := h.handle(t, buildPayload(msgOpts{
			chat: testChatJID, sender: testChatJID,
			id: fmt.Sprintf("G%d", i), msgType: "conversation", text: "x",
		})); err != nil {
			t.Fatalf("HandleEvent error = %v", err)
		}
	}
	after := runtime.NumGoroutine()
	if after > before {
		t.Errorf("goroutines grew from %d to %d across HandleEvent calls", before, after)
	}
}

// TestEnumsHaveUnknownZeroValue covers L3: the zero value is a sentinel, not a
// real state.
func TestEnumsHaveUnknownZeroValue(t *testing.T) {
	if OutcomeUnknown != 0 {
		t.Errorf("OutcomeUnknown = %d, want 0", OutcomeUnknown)
	}
	if OutcomeUnknown.String() != "unknown" {
		t.Errorf("OutcomeUnknown.String() = %q, want unknown", OutcomeUnknown.String())
	}
	if OutcomeMalformed.String() != "malformed" {
		t.Errorf("OutcomeMalformed.String() = %q, want malformed", OutcomeMalformed.String())
	}
}

func TestNew_PanicsOnNilDeps(t *testing.T) {
	base := func() Deps {
		h := newHarness()
		return Deps{
			Mappings: h.mapping, Chatwoot: h.cw, Echo: h.echo, Dedup: h.dedup, Logs: h.logs,
		}
	}
	cases := map[string]func(*Deps){
		"mappings": func(d *Deps) { d.Mappings = nil },
		"chatwoot": func(d *Deps) { d.Chatwoot = nil },
		"echo":     func(d *Deps) { d.Echo = nil },
		"dedup":    func(d *Deps) { d.Dedup = nil },
		"logs":     func(d *Deps) { d.Logs = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			deps := base()
			mutate(&deps)
			defer func() {
				if recover() == nil {
					t.Error("New() did not panic on a nil dependency")
				}
			}()
			New(deps)
		})
	}
}

func TestNew_DefaultsClaimTTL(t *testing.T) {
	h := newHarness()
	engine := New(Deps{Mappings: h.mapping, Chatwoot: h.cw, Echo: h.echo, Dedup: h.dedup, Logs: h.logs})
	if engine.claimTTL != defaultClaimTTL {
		t.Errorf("claimTTL = %v, want %v", engine.claimTTL, defaultClaimTTL)
	}
	if engine.claimTTL < time.Hour {
		t.Error("claimTTL below the one-hour retention floor")
	}
}
