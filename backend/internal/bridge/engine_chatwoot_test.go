package bridge

import (
	"context"
	"errors"
	"testing"
)

// TestSlice5_* is the counted acceptance suite for the Chatwoot → WhatsApp
// direction (E10–E13, spec §5). Every case asserts the exact effect counters
// CC/CV/MS/MP/NU/LG and the Evolution-send counter ES, plus the claim
// transition kind, not just an outcome. All fixtures are synthetic (AGENTS §9).

// claimChatwoot wins a claim for a CW payload on the harness connection, as the
// ingress does before ACK, and returns the claim id and owner token.
func (h *harness) claimChatwoot(t *testing.T, payload []byte) (int64, string) {
	t.Helper()
	claimed, id, token, err := h.tryClaimChatwoot(payload)
	if err != nil || !claimed {
		t.Fatalf("chatwoot Claim() = %v, %v; want a won claim", claimed, err)
	}
	return id, token
}

func (h *harness) tryClaimChatwoot(payload []byte) (bool, int64, string, error) {
	cw, err := ParseChatwoot(payload)
	if err != nil {
		return false, 0, "", err
	}
	key := Key{
		ConnectionID:    h.conn.ID,
		SourceSystem:    SourceChatwoot,
		EventType:       cw.EventName,
		SourceMessageID: cw.MessageID,
	}
	return h.dedup.Claim(context.Background(), key, h.engine.claimTTL)
}

func (h *harness) handleChatwoot(t *testing.T, payload []byte) (Outcome, error) {
	t.Helper()
	id, token := h.claimChatwoot(t, payload)
	return h.engine.HandleChatwootClaimed(context.Background(), Event{Connection: h.conn, Payload: payload}, id, token)
}

// cwForwardOpts is the reusable "agent reply with a mapping" payload.
func cwForwardOpts(id any, content string) cwOpts {
	return cwOpts{
		id: id, messageType: "outgoing", senderType: "user",
		content: content, conversationID: 5001,
	}
}

// TestSlice5_C10_IgnoredEvents covers E10 (spec §5 C10a–e): each ignored event
// closes the claim as duplicate once, sends nothing and touches no entity.
func TestSlice5_C10_IgnoredEvents(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "C10a incoming (F2)", payload: buildChatwootPayload(cwOpts{
			id: 1, messageType: "incoming", content: "oi", conversationID: 5001, senderType: "user"})},
		{name: "C10b private (F3)", payload: buildChatwootPayload(cwOpts{
			id: 2, messageType: "outgoing", private: true, content: "nota", conversationID: 5001, senderType: "user"})},
		{name: "C10c agent_bot (F4)", payload: buildChatwootPayload(cwOpts{
			id: 3, messageType: "outgoing", senderType: "agent_bot", content: "bot", conversationID: 5001})},
		{name: "C10d event not message_created (F1)", payload: buildChatwootPayload(cwOpts{
			event: "conversation_created", messageType: "outgoing"})},
		{name: "C10e no conversation id (F6)", payload: buildChatwootPayload(cwOpts{
			id: 5, messageType: "outgoing", senderType: "user", content: "x"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness()
			out, err := h.handleChatwoot(t, tt.payload)
			if err != nil || out != OutcomeIgnored {
				t.Fatalf("HandleChatwootClaimed() = %v, %v; want ignored", out, err)
			}
			h.assertCounts(t, counts{}) // ES0 CC0 CV0 MS0 MP0 NU0 LG0
			if h.dedup.duplicate != 1 {
				t.Errorf("MarkDuplicate calls = %d, want 1", h.dedup.duplicate)
			}
			if h.dedup.failed != 0 || h.dedup.completed != 0 || h.dedup.unconfirmed != 0 {
				t.Errorf("terminal transitions = failed:%d completed:%d unconfirmed:%d, want all 0",
					h.dedup.failed, h.dedup.completed, h.dedup.unconfirmed)
			}
		})
	}
}

// TestSlice5_C11_Forward covers E11: an agent reply with a mapping is sent to
// WhatsApp exactly once, completes the claim and logs one row. The Evolution
// number is the stored contacts_map.whatsappPhone and the text is verbatim.
func TestSlice5_C11_Forward(t *testing.T) {
	h := newHarness()
	h.convs.rows[conversationKey(1, 5001)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 5001,
	}
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts(10001, "resposta do agente")))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{ES: 1, LG: 1}) // no CC/CV/MP/NU: E12 creates no entity
	if h.dedup.completed != 1 {
		t.Errorf("Complete calls = %d, want 1", h.dedup.completed)
	}
	if len(h.evo.numbers) != 1 || h.evo.numbers[0] != testPhone {
		t.Errorf("Evolution number = %v, want [%s]", h.evo.numbers, testPhone)
	}
	if len(h.evo.texts) != 1 || h.evo.texts[0] != "resposta do agente" {
		t.Errorf("Evolution text = %v, want verbatim content", h.evo.texts)
	}
}

// TestSlice5_C11b_TemplateForward proves a template message (the other allowed
// message_type) is forwarded.
func TestSlice5_C11b_TemplateForward(t *testing.T) {
	h := newHarness()
	h.convs.rows[conversationKey(1, 5001)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 5001,
	}
	payload := buildChatwootPayload(cwOpts{
		id: 10002, messageType: "template", senderType: "user", content: "template body", conversationID: 5001,
	})
	out, err := h.handleChatwoot(t, payload)
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{ES: 1, LG: 1})
}

// TestSlice5_C12_NoMapping covers E12: no contacts_map row fails attend with no
// entity created and nothing sent.
func TestSlice5_C12_NoMapping(t *testing.T) {
	h := newHarness()
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts(10003, "sem mapeamento")))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{}) // ES0 CC0 CV0 MS0 MP0 NU0 LG0
	if h.dedup.failed != 1 || !containsString(h.dedup.failCodes, "no_mapping") {
		t.Errorf("MarkFailed calls/codes = %d/%v, want 1 with no_mapping", h.dedup.failed, h.dedup.failCodes)
	}
}

// TestSlice5_C13_DuplicateDelivery covers E13: a re-delivery of the same CW
// message id loses the ingress claim and is never sent again (ES total 1).
func TestSlice5_C13_DuplicateDelivery(t *testing.T) {
	h := newHarness()
	h.convs.rows[conversationKey(1, 5001)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 5001,
	}
	payload := buildChatwootPayload(cwForwardOpts(10004, "uma vez"))
	id, token := h.claimChatwoot(t, payload)
	out, err := h.engine.HandleChatwootClaimed(context.Background(), Event{Connection: h.conn, Payload: payload}, id, token)
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("first delivery = %v, %v; want forwarded", out, err)
	}

	// The second delivery of the same id loses the claim: no enqueue, no send.
	claimed, _, _, err := h.tryClaimChatwoot(payload)
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if claimed {
		t.Fatal("second delivery won the claim, want duplicate")
	}
	if got := h.counters.snapshot().ES; got != 1 {
		t.Errorf("Evolution sends = %d, want 1 (E13)", got)
	}
}

// TestSlice5_CLoop_F5 covers the F5 loop-break: a Chatwoot message the bridge
// itself created (recorded as an evolution/sent destination) is ignored.
func TestSlice5_CLoop_F5(t *testing.T) {
	h := newHarness()
	h.echo.forwarded[echoKey(1, "10005")] = true
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts(10005, "eco do bridge")))
	if err != nil || out != OutcomeIgnored {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want ignored", out, err)
	}
	h.assertCounts(t, counts{}) // ES0
	if h.dedup.duplicate != 1 {
		t.Errorf("MarkDuplicate calls = %d, want 1", h.dedup.duplicate)
	}
}

// TestSlice5_CCollation_F5 covers C-Collation: F5 is byte-exact, so a
// case-variant destination id is NOT a loop and the event is treated as
// external. Without a mapping it then fails closed (never sent). The real
// CAST(... AS BINARY) behaviour is proven in the MySQL integration test.
func TestSlice5_CCollation_F5(t *testing.T) {
	h := newHarness()
	h.echo.forwarded[echoKey(1, "DEST-1")] = true
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts("dest-1", "variante")))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want failed (not a loop)", out, err)
	}
	h.assertCounts(t, counts{}) // ES0
	if h.dedup.duplicate != 0 {
		t.Errorf("MarkDuplicate calls = %d, want 0 (case variant is not bridge-authored)", h.dedup.duplicate)
	}
	if !containsString(h.dedup.failCodes, "no_mapping") {
		t.Errorf("fail codes = %v, want no_mapping", h.dedup.failCodes)
	}
}

// TestSlice5_CUnconfirmed covers C-Unconfirmed: an unknown Evolution send
// outcome quarantines the claim exactly once and is never retried.
func TestSlice5_CUnconfirmed(t *testing.T) {
	h := newHarness()
	h.convs.rows[conversationKey(1, 5001)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 5001,
	}
	h.evo.sendErr = ErrUnconfirmed
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts(10006, "incerto")))
	if err != nil || out != OutcomeQuarantined {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want quarantined", out, err)
	}
	h.assertCounts(t, counts{ES: 1, LG: 1}) // exactly one attempt, no retry
	if h.dedup.unconfirmed != 1 || h.dedup.completed != 0 {
		t.Errorf("unconfirmed/completed = %d/%d, want 1/0", h.dedup.unconfirmed, h.dedup.completed)
	}
}

// TestSlice5_CEmptyContent covers C-EmptyContent: an attachment-only message
// (empty content) is not partially forwarded and creates nothing.
func TestSlice5_CEmptyContent(t *testing.T) {
	h := newHarness()
	h.convs.rows[conversationKey(1, 5001)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 5001,
	}
	payload := buildChatwootPayload(cwOpts{
		id: 10007, messageType: "outgoing", senderType: "user", content: "",
		conversationID: 5001, attachments: []map[string]any{{"file_type": "image"}},
	})
	out, err := h.handleChatwoot(t, payload)
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{}) // ES0
	if !containsString(h.dedup.failCodes, "no_content") {
		t.Errorf("fail codes = %v, want no_content", h.dedup.failCodes)
	}
}

// TestSlice5_NotConfigured proves the CW entry point fails closed (no entity,
// no send) when the Chatwoot-direction ports are not wired (spec §2.6).
func TestSlice5_NotConfigured(t *testing.T) {
	h := newHarness()
	h.engine.evolution = nil
	h.engine.conversations = nil
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts(10008, "sem portas")))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{})
	if !containsString(h.dedup.failCodes, "not_configured") {
		t.Errorf("fail codes = %v, want not_configured", h.dedup.failCodes)
	}
}

// TestSlice5_EchoLookupErrorFailsClosed proves a loop-break lookup error fails
// the claim rather than sending the message.
func TestSlice5_EchoLookupErrorFailsClosed(t *testing.T) {
	h := newHarness()
	h.useFastRetry()
	h.echo.forwardedErr = errSentinel
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts(10009, "erro de eco")))
	if err == nil || out != OutcomeFailed {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want failed with error", out, err)
	}
	h.assertCounts(t, counts{}) // no send on a lookup error
	if !containsString(h.dedup.failCodes, "echo_lookup_failed") {
		t.Errorf("fail codes = %v, want echo_lookup_failed", h.dedup.failCodes)
	}
}

// TestSlice5_MappingLookupErrorFailsClosed proves a reverse-mapping error is
// never treated as "no mapping" and never sends.
func TestSlice5_MappingLookupErrorFailsClosed(t *testing.T) {
	h := newHarness()
	h.useFastRetry()
	h.convs.err = errSentinel
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts(10010, "erro de mapa")))
	if err == nil || out != OutcomeFailed {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want failed with error", out, err)
	}
	h.assertCounts(t, counts{}) // ES0
	if !containsString(h.dedup.failCodes, "mapping_lookup_failed") {
		t.Errorf("fail codes = %v, want mapping_lookup_failed", h.dedup.failCodes)
	}
}

// TestSlice5_MalformedPayload proves a malformed CW payload closes the claim
// fail-closed as malformed, touching no port.
func TestSlice5_MalformedPayload(t *testing.T) {
	h := newHarness()
	id, token := func() (int64, string) {
		claimed, id, token, err := h.dedup.Claim(context.Background(), Key{
			ConnectionID: 1, SourceSystem: SourceChatwoot, EventType: "message_created", SourceMessageID: "MAL-1",
		}, h.engine.claimTTL)
		if err != nil || !claimed {
			t.Fatalf("Claim() = %v, %v; want a won claim", claimed, err)
		}
		return id, token
	}()
	out, err := h.engine.HandleChatwootClaimed(context.Background(), Event{Connection: h.conn, Payload: []byte("{not json")}, id, token)
	if err == nil || out != OutcomeMalformed {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want malformed", out, err)
	}
	h.assertCounts(t, counts{})
	if !containsString(h.dedup.failCodes, "malformed") {
		t.Errorf("fail codes = %v, want malformed", h.dedup.failCodes)
	}
}

// TestSlice5_SendPreflightFailureFailsAttend proves a known-not-sent Evolution
// error (not ErrUnconfirmed) is a fail-attend, never a quarantine or a retry.
func TestSlice5_SendPreflightFailureFailsAttend(t *testing.T) {
	h := newHarness()
	h.convs.rows[conversationKey(1, 5001)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 5001,
	}
	h.evo.sendErr = errors.New("synthetic preflight rejection")
	out, err := h.handleChatwoot(t, buildChatwootPayload(cwForwardOpts(10011, "rejeitado")))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleChatwootClaimed() = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{ES: 1, LG: 1}) // one attempt, no retry
	if !containsString(h.dedup.failCodes, "send_failed") {
		t.Errorf("fail codes = %v, want send_failed", h.dedup.failCodes)
	}
}

// containsString reports whether want is in values.
func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
