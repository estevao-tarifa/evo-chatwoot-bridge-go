package bridge

import (
	"context"
	"testing"
	"time"
)

// claimFor wins a claim for payload on the harness connection, as the ingress
// does before ACK, and returns the claim id and owner token.
func (h *harness) claimFor(t *testing.T, payload []byte) (int64, string) {
	t.Helper()
	parsed, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	claimed, id, token, err := h.dedup.Claim(context.Background(), h.engine.eventKey(h.conn.ID, parsed), h.engine.claimTTL)
	if err != nil || !claimed {
		t.Fatalf("Claim() = %v, %v; want a won claim", claimed, err)
	}
	return id, token
}

func (h *harness) handleClaimed(t *testing.T, payload []byte, id int64, token string) (Outcome, error) {
	t.Helper()
	return h.engine.HandleClaimed(context.Background(), Event{Connection: h.conn, Payload: payload}, id, token)
}

// TestSlice4_H1_HandleClaimedIgnore proves an ignored event (E7) closes the
// pre-won claim as duplicate exactly once, without sending.
func TestSlice4_H1_HandleClaimedIgnore(t *testing.T) {
	h := newHarness()
	payload := buildPayload(msgOpts{chat: "status@broadcast", id: "E7-1", msgType: "conversation", text: "story"})
	id, token := h.claimFor(t, payload)

	outcome, err := h.handleClaimed(t, payload, id, token)
	if err != nil || outcome != OutcomeIgnored {
		t.Fatalf("HandleClaimed() = %v, %v; want ignored", outcome, err)
	}
	if h.dedup.duplicate != 1 {
		t.Errorf("MarkDuplicate calls = %d, want 1", h.dedup.duplicate)
	}
	if status := h.dedup.status(id); status != "duplicate" {
		t.Errorf("row status = %q, want duplicate", status)
	}
	h.assertCounts(t, counts{})
}

// TestSlice4_H2_HandleClaimedFail proves a fail-attend event closes the pre-won
// claim as failed exactly once, creating no entity.
func TestSlice4_H2_HandleClaimedFail(t *testing.T) {
	h := newHarness()
	payload := buildPayload(msgOpts{chat: "5511900000001@s.whatsapp.net", id: "E-1", msgType: "conversation", text: "hi"})
	id, token := h.claimFor(t, payload)

	conn := h.conn
	conn.IsActive = false // routing fails closed
	outcome, err := h.engine.HandleClaimed(context.Background(), Event{Connection: conn, Payload: payload}, id, token)
	if err != nil || outcome != OutcomeFailed {
		t.Fatalf("HandleClaimed() = %v, %v; want failed", outcome, err)
	}
	if h.dedup.failed != 1 {
		t.Errorf("MarkFailed calls = %d, want 1", h.dedup.failed)
	}
	if status := h.dedup.status(id); status != "failed" {
		t.Errorf("row status = %q, want failed", status)
	}
	h.assertCounts(t, counts{})
}

// TestSlice4_H3_HandleClaimedForward proves a forward completes the pre-won
// claim exactly once and sends exactly one message.
func TestSlice4_H3_HandleClaimedForward(t *testing.T) {
	h := newHarness()
	h.useFastRetry()
	payload := buildPayload(msgOpts{chat: "5511900000001@s.whatsapp.net", id: "E1-1", msgType: "conversation", text: "hi"})
	id, token := h.claimFor(t, payload)

	outcome, err := h.handleClaimed(t, payload, id, token)
	if err != nil || outcome != OutcomeForwarded {
		t.Fatalf("HandleClaimed() = %v, %v; want forwarded", outcome, err)
	}
	if h.dedup.completed != 1 {
		t.Errorf("Complete calls = %d, want 1", h.dedup.completed)
	}
	if status := h.dedup.status(id); status != "sent" {
		t.Errorf("row status = %q, want sent", status)
	}
	h.assertCounts(t, counts{CC: 1, CV: 1, MS: 1, MP: 2, LG: 1})
}

// TestSlice4_H4_HandleClaimedUnknownOutcome proves an unconfirmed send outcome
// quarantines the claim exactly once with no retry.
func TestSlice4_H4_HandleClaimedUnknownOutcome(t *testing.T) {
	h := newHarness()
	h.useFastRetry()
	h.cw.sendEmptyID = true // confirmed call, no correlatable destination id
	payload := buildPayload(msgOpts{chat: "5511900000001@s.whatsapp.net", id: "E1-2", msgType: "conversation", text: "hi"})
	id, token := h.claimFor(t, payload)

	outcome, err := h.handleClaimed(t, payload, id, token)
	if err != nil || outcome != OutcomeQuarantined {
		t.Fatalf("HandleClaimed() = %v, %v; want quarantined", outcome, err)
	}
	if h.dedup.unconfirmed != 1 {
		t.Errorf("MarkUnconfirmed calls = %d, want 1", h.dedup.unconfirmed)
	}
	if h.dedup.completed != 0 {
		t.Errorf("Complete calls = %d, want 0", h.dedup.completed)
	}
	h.assertCounts(t, counts{CC: 1, CV: 1, MS: 1, MP: 2, LG: 1})
}

// TestSlice4_C1_CrashAfterAcceptedSendRestartZeroResends locks down the
// quarantine-only recovery: a claim left 'processing' by a crash after an
// accepted external effect is reconciled to unconfirmed_send and never re-sent.
func TestSlice4_C1_CrashAfterAcceptedSendRestartZeroResends(t *testing.T) {
	h := newHarness()
	// The send was accepted but Complete never persisted before the crash.
	h.dedup.seedProcessing(42, time.Now().Add(-20*time.Minute))

	staleBefore := time.Now().Add(-15 * time.Minute)
	outcome, err := h.engine.ReconcileStale(context.Background(), 42, staleBefore)
	if err != nil || outcome != OutcomeQuarantined {
		t.Fatalf("ReconcileStale() = %v, %v; want quarantined", outcome, err)
	}
	if h.dedup.unconfirmed != 1 {
		t.Errorf("MarkUnconfirmed calls = %d, want 1", h.dedup.unconfirmed)
	}
	if status := h.dedup.status(42); status != "unconfirmed_send" {
		t.Errorf("row status = %q, want unconfirmed_send", status)
	}
	// ZERO additional sends during reconcile, and no entity was touched.
	h.assertCounts(t, counts{})
}

// TestSlice4_C2_ReconcileStrippedPayloadQuarantines proves a stale claim whose
// audit payload was never written (or was stripped) is still quarantined with
// no send.
func TestSlice4_C2_ReconcileStrippedPayloadQuarantines(t *testing.T) {
	h := newHarness()
	h.dedup.seedProcessing(43, time.Now().Add(-45*time.Minute))

	outcome, err := h.engine.ReconcileStale(context.Background(), 43, time.Now().Add(-15*time.Minute))
	if err != nil || outcome != OutcomeQuarantined {
		t.Fatalf("ReconcileStale() = %v, %v; want quarantined", outcome, err)
	}
	if h.dedup.unconfirmed != 1 {
		t.Errorf("MarkUnconfirmed calls = %d, want 1", h.dedup.unconfirmed)
	}
	if status := h.dedup.status(43); status != "unconfirmed_send" {
		t.Errorf("row status = %q, want unconfirmed_send", status)
	}
	h.assertCounts(t, counts{})
}

// TestSlice4_C3_AckFailureReleaseThenReclaim proves the ack-failure path: a
// failed audit write releases the pre-admission claim, and the sender's retry
// re-claims and processes the event exactly once.
func TestSlice4_C3_AckFailureReleaseThenReclaim(t *testing.T) {
	h := newHarness()
	h.useFastRetry()
	payload := buildPayload(msgOpts{chat: "5511900000001@s.whatsapp.net", id: "C3-1", msgType: "conversation", text: "hi"})

	// First attempt: the claim is won but the audit write fails, so ingress
	// releases it (the row is not left 'processing').
	id1, token1 := h.claimFor(t, payload)
	if err := h.dedup.Release(context.Background(), id1, token1); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if h.dedup.released != 1 {
		t.Fatalf("release calls = %d, want 1", h.dedup.released)
	}
	h.assertCounts(t, counts{})

	// Sender retry: the released key can be claimed again and is processed once.
	id2, token2 := h.claimFor(t, payload)
	outcome, err := h.handleClaimed(t, payload, id2, token2)
	if err != nil || outcome != OutcomeForwarded {
		t.Fatalf("HandleClaimed() = %v, %v; want forwarded", outcome, err)
	}
	h.assertCounts(t, counts{CC: 1, CV: 1, MS: 1, MP: 2, LG: 1})
}
