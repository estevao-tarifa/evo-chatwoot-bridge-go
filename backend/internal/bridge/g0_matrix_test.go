package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestG0_* is the counted acceptance suite of the G0 guarantee
// (docs/bridge/ROUTING_MATRIX.md §8). Every case asserts the exact effect
// counters CC/CV/MS/MP/NU/LG on the fakes, not an HTTP status.
//
// Fail-before / pass-after mutation controls (each makes the named test exit 1;
// restoring the correct code exits 0). Reproduce with
// `go test -run <target> ./internal/bridge/`:
//
//	MUT-A search-before-mapping   → TestG0_E1_mapping_exists     (search first ⇒ CC1)
//	MUT-B search-error-as-miss    → TestG0_search_error_not_miss (error ⇒ create ⇒ CC1)
//	MUT-C placeholder group name  → TestG0_group_name_absent_new (create "Grupo" ⇒ CC1)
//	MUT-D retry send on timeout   → TestG0_unknown_send_outcome  (retry ⇒ MS2)
//	MUT-E pick-first on conflict  → TestG0_identity_conflict     (pick ⇒ MS1)
//
// All fixtures are synthetic (AGENTS §9).

const (
	testPhone    = "5511900000001"
	testChatJID  = testPhone + "@s.whatsapp.net"
	testGroupJID = "120363000000000001@g.us"
)

// openConversation is the reusable candidate used across cases.
func openConversation(id int) Conversation {
	return Conversation{ID: id, InboxID: 10, Status: "open", LastActivityAt: 1}
}

func TestG0_E1_mapping_exists(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M1", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{MS: 1, LG: 1}) // CC0 CV0 MS1 MP0 NU0 LG1
}

func TestG0_E1_search_hit(t *testing.T) {
	h := newHarness()
	h.cw.searchResults = []Contact{{ID: 5, Name: "Ana", Identifier: testPhone}}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M2", pushName: "Ana",
		msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{MS: 1, MP: 2, LG: 1}) // CC0 CV0 MS1 MP2 NU0 LG1 (contact upsert + F4 persist)
}

func TestG0_E1_search_zero_creates(t *testing.T) {
	h := newHarness()
	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M3", pushName: "Ana",
		msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{CC: 1, CV: 1, MS: 1, MP: 2, LG: 1})
}

// TestG0_persist_conversation_id_new_identity covers F4: an identity first seen
// from WhatsApp with no mapping creates a conversation and the resolved
// Chatwoot conversation id is persisted into contacts_map (E11). Without the
// persist, the reverse direction cannot map the agent's reply and fails closed
// with no_mapping.
func TestG0_persist_conversation_id_new_identity(t *testing.T) {
	h := newHarness() // no seeded mapping; no reusable conversation

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "F4-1", pushName: "Ana",
		msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	// MP counts both the contact-only upsert (resolveContact) and the F4
	// conversation-id persist.
	h.assertCounts(t, counts{CC: 1, CV: 1, MS: 1, MP: 2, LG: 1})

	got, ok := h.mapping.rows[mappingKey(1, testPhone)]
	if !ok {
		t.Fatal("no contacts_map row was written for the new identity")
	}
	if got.ChatwootContactID != 1001 || got.ChatwootConversationID != 2001 {
		t.Errorf("persisted mapping = %+v, want contact 1001 / conversation 2001", got)
	}
}

// TestG0_persist_conversation_id_failure_fails_closed covers the F4 negative: a
// failure persisting the resolved conversation id is a real error, so the claim
// fails closed and nothing is sent to Chatwoot — never a silent drop, never an
// alternate entity.
func TestG0_persist_conversation_id_failure_fails_closed(t *testing.T) {
	h := newHarness()
	h.mapping.upsertErr = errSentinel
	h.mapping.failConvPersist = true // only the F4 persist fails, not the contact upsert

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "F4-2", pushName: "Ana",
		msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleEvent = %v, %v; want failed", out, err)
	}
	// The contact-only upsert succeeded (MP1); the conversation persist failed,
	// so no message was sent (MS0) and the claim is a fail-attend.
	h.assertCounts(t, counts{CC: 1, CV: 1, MP: 1, LG: 1})
	if h.dedup.failed != 1 {
		t.Errorf("MarkFailed calls = %d, want 1", h.dedup.failed)
	}
}

// TestG0_persist_keeps_stored_conversation_id covers the F4 reuse path: an
// existing mapping whose stored conversation id is still selectable must keep
// that id — persistConversation skips the write, so the mapping is never
// blanked.
func TestG0_persist_keeps_stored_conversation_id(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "F4-3", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	// The stored id is still selectable (Q4): persistConversation skips the
	// write, so no mapping upsert happens at all (MP0).
	h.assertCounts(t, counts{MS: 1, LG: 1})
	if got := h.mapping.rows[mappingKey(1, testPhone)].ChatwootConversationID; got != 7 {
		t.Errorf("stored conversation id = %d, want 7 (never blanked)", got)
	}
}

func TestG0_external_isfromme_mapping_exists(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testChatJID, sender: testChatJID, id: "M4",
		isFromMe: true, pushName: "Ana", msgType: "conversation", text: "externo",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	// NU0 comes from the mapping reuse (unknown current name), not from
	// IsFromMe — see T5 (TestG0_external_isfromme_search_hit) for the IsFromMe
	// guard that also suppresses the update.
	h.assertCounts(t, counts{MS: 1, LG: 1})
}

func TestG0_external_isfromme_search_hit(t *testing.T) {
	h := newHarness()
	h.cw.searchResults = []Contact{{ID: 5, Name: "Old", Identifier: testPhone}}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testChatJID, sender: testChatJID, id: "M5",
		isFromMe: true, pushName: "Ana", msgType: "conversation", text: "externo",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{MS: 1, MP: 2, LG: 1}) // CC0 CV0 MS1 MP2 NU0
}

func TestG0_external_isfromme_search_zero(t *testing.T) {
	h := newHarness()
	out, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testChatJID, sender: testChatJID, id: "M6",
		isFromMe: true, pushName: "Ana", msgType: "conversation", text: "externo",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{CC: 1, CV: 1, MS: 1, MP: 2, LG: 1})
}

func TestG0_echo_destination_match(t *testing.T) {
	h := newHarness()
	h.echo.sent[echoKey(1, "M7")] = true

	out, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testChatJID, sender: testChatJID, id: "M7",
		isFromMe: true, msgType: "conversation", text: "eco",
	}))
	if err != nil || out != OutcomeEcho {
		t.Fatalf("HandleEvent = %v, %v; want echo", out, err)
	}
	h.assertCounts(t, counts{LG: 1}) // CC0 CV0 MS0 MP0 NU0 LG1
}

func TestG0_group_name_present_mapping_exists(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testGroupJID)] = Mapping{
		ConnectionID: 1, IdentityKey: testGroupJID, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testGroupJID, sender: testPhone + "@s.whatsapp.net", id: "M8",
		isGroup: true, groupName: "Equipe X", pushName: "Ana",
		msgType: "conversation", text: "bom dia",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{MS: 1, LG: 1}) // mapping reuse: identity stable, no name touch
}

func TestG0_group_name_present_new(t *testing.T) {
	h := newHarness()
	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testGroupJID, sender: testPhone + "@s.whatsapp.net", id: "M9",
		isGroup: true, groupName: "Equipe X", pushName: "Ana",
		msgType: "conversation", text: "bom dia",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{CC: 1, CV: 1, MS: 1, MP: 2, LG: 1})
}

func TestG0_group_name_absent_mapping_exists(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testGroupJID)] = Mapping{
		ConnectionID: 1, IdentityKey: testGroupJID, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testGroupJID, sender: testPhone + "@s.whatsapp.net", id: "M10",
		isGroup: true, pushName: "Ana", msgType: "conversation", text: "bom dia",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	h.assertCounts(t, counts{MS: 1, LG: 1}) // never overwrite with a placeholder
}

func TestG0_group_name_absent_new(t *testing.T) {
	h := newHarness()
	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testGroupJID, sender: testPhone + "@s.whatsapp.net", id: "M11",
		isGroup: true, pushName: "Ana", msgType: "conversation", text: "bom dia",
	}))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleEvent = %v, %v; want failed (P4)", out, err)
	}
	h.assertCounts(t, counts{LG: 1}) // no group without an authoritative name
}

func TestG0_group_echo(t *testing.T) {
	h := newHarness()
	h.echo.sent[echoKey(1, "M12")] = true

	out, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testGroupJID, sender: testPhone + "@s.whatsapp.net",
		id: "M12", isFromMe: true, isGroup: true, groupName: "Equipe X",
		msgType: "conversation", text: "eco",
	}))
	if err != nil || out != OutcomeEcho {
		t.Fatalf("HandleEvent = %v, %v; want echo", out, err)
	}
	h.assertCounts(t, counts{LG: 1})
}

func TestG0_broadcast_ignored(t *testing.T) {
	h := newHarness()
	out, err := h.handle(t, buildPayload(msgOpts{
		chat: "status@broadcast", sender: "status@broadcast", id: "M13",
		msgType: "conversation", text: "status",
	}))
	if err != nil || out != OutcomeIgnored {
		t.Fatalf("HandleEvent = %v, %v; want ignored", out, err)
	}
	h.assertCounts(t, counts{})
}

func TestG0_protocol_reaction_sticker_ignored(t *testing.T) {
	for _, msgType := range []string{"protocolMessage", "reactionMessage", "stickerMessage"} {
		t.Run(msgType, func(t *testing.T) {
			h := newHarness()
			out, err := h.handle(t, buildPayload(msgOpts{
				chat: testChatJID, sender: testChatJID, id: "M14", msgType: msgType,
			}))
			if err != nil || out != OutcomeIgnored {
				t.Fatalf("HandleEvent = %v, %v; want ignored", out, err)
			}
			h.assertCounts(t, counts{})
		})
	}
}

func TestG0_malformed_identity(t *testing.T) {
	cases := map[string]msgOpts{
		"empty jid":   {chat: "", sender: "", id: "M15", msgType: "conversation", text: "x"},
		"short jid":   {chat: "1234", sender: "1234", id: "M15", msgType: "conversation", text: "x"},
		"at in phone": {chat: "user@name", sender: "user@name", id: "M15", msgType: "conversation", text: "x"},
		"no digits":   {chat: "abcdef", sender: "abcdef", id: "M15", msgType: "conversation", text: "x"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			out, err := h.handle(t, buildPayload(opts))
			if err != nil || out != OutcomeFailed {
				t.Fatalf("HandleEvent = %v, %v; want failed", out, err)
			}
			h.assertCounts(t, counts{}) // zero port calls
		})
	}
}

func TestG0_search_error_not_miss(t *testing.T) {
	h := newHarness()
	h.cw.searchErr = errSentinel

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M16", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleEvent = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{LG: 1}) // CC0 CV0 MS0
}

func TestG0_list_error_not_no_candidate(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.listErr = errSentinel

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M17", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleEvent = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{LG: 1}) // CV0
}

func TestG0_identity_conflict(t *testing.T) {
	h := newHarness()
	h.cw.searchResults = []Contact{
		{ID: 5, Name: "A", Identifier: testPhone},
		{ID: 6, Name: "B", Identifier: testPhone},
	}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M18", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeFailed {
		t.Fatalf("HandleEvent = %v, %v; want failed", out, err)
	}
	h.assertCounts(t, counts{LG: 1}) // no pick-first
}

func TestG0_unknown_send_outcome(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}
	h.cw.sendErr = errSentinel

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M19", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeQuarantined {
		t.Fatalf("HandleEvent = %v, %v; want quarantined", out, err)
	}
	h.assertCounts(t, counts{MS: 1, LG: 1}) // exactly one attempt, no retry, no CC/CV after
	if h.dedup.unconfirmed != 1 {
		t.Errorf("MarkUnconfirmed calls = %d, want 1", h.dedup.unconfirmed)
	}
}

// TestG0_empty_send_id_quarantined covers M1: a confirmed send with an empty
// destination id is an unknown outcome. Without an id the echo store cannot
// correlate, so forwarding it would let the echo be re-forwarded (rule 6). The
// claim is quarantined, never completed, never retried, no alternate entity.
func TestG0_empty_send_id_quarantined(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}
	h.cw.sendEmptyID = true

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M19b", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeQuarantined {
		t.Fatalf("HandleEvent = %v, %v; want quarantined (empty destination id)", out, err)
	}
	h.assertCounts(t, counts{MS: 1, LG: 1}) // one attempt, no retry, no CC/CV
	if h.dedup.unconfirmed != 1 {
		t.Errorf("MarkUnconfirmed calls = %d, want 1", h.dedup.unconfirmed)
	}
	if h.dedup.completed != 0 {
		t.Errorf("Complete calls = %d, want 0 (no correlation recorded)", h.dedup.completed)
	}
}

// TestG0_noncorrelatable_send_id_quarantined covers M1's correlation guard: a
// confirmed send whose destination id is not a real, correlatable id (empty,
// whitespace-only or zero-width/format-only) is an unknown outcome. The echo
// store cannot correlate it, so the claim is quarantined, never completed,
// never retried, no alternate entity (rule 6). A real id is forwarded normally.
//
// Mutation control: reverting the guard to the literal `result.MessageID == ""`
// (dropping isCorrelatableMessageID) makes every non-empty case below exit 1
// (whitespace/zero-width ids would be forwarded instead of quarantined).
func TestG0_noncorrelatable_send_id_quarantined(t *testing.T) {
	tests := []struct {
		name          string
		sendID        string
		wantOutcome   Outcome
		wantCompleted int
		wantUnconfirm int
	}{
		{name: "space", sendID: " ", wantOutcome: OutcomeQuarantined, wantUnconfirm: 1},
		{name: "tab", sendID: "\t", wantOutcome: OutcomeQuarantined, wantUnconfirm: 1},
		{name: "newline", sendID: "\n", wantOutcome: OutcomeQuarantined, wantUnconfirm: 1},
		{name: "crlf", sendID: "\r\n", wantOutcome: OutcomeQuarantined, wantUnconfirm: 1},
		{name: "nbsp", sendID: "\u00a0", wantOutcome: OutcomeQuarantined, wantUnconfirm: 1},
		{name: "zero width space", sendID: "\u200b", wantOutcome: OutcomeQuarantined, wantUnconfirm: 1},
		{name: "bom", sendID: "\ufeff", wantOutcome: OutcomeQuarantined, wantUnconfirm: 1},
		{name: "mixed whitespace and zero width", sendID: "  \u200b  ", wantOutcome: OutcomeQuarantined, wantUnconfirm: 1},
		{name: "real id", sendID: "3EB0A1B2C3D4", wantOutcome: OutcomeForwarded, wantCompleted: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness()
			h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
				ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
			}
			h.cw.conversations = []Conversation{openConversation(7)}
			h.cw.sendID = tt.sendID

			out, err := h.handle(t, buildPayload(msgOpts{
				chat: testChatJID, sender: testChatJID, id: "M19c", msgType: "conversation", text: "oi",
			}))
			if err != nil || out != tt.wantOutcome {
				t.Fatalf("HandleEvent = %v, %v; want %v", out, err, tt.wantOutcome)
			}
			h.assertCounts(t, counts{MS: 1, LG: 1}) // one attempt, no retry, no CC/CV
			if h.dedup.unconfirmed != tt.wantUnconfirm {
				t.Errorf("MarkUnconfirmed calls = %d, want %d", h.dedup.unconfirmed, tt.wantUnconfirm)
			}
			if h.dedup.completed != tt.wantCompleted {
				t.Errorf("Complete calls = %d, want %d", h.dedup.completed, tt.wantCompleted)
			}
		})
	}
}

func TestG0_unknown_create_outcome(t *testing.T) {
	h := newHarness()
	h.cw.createErr = errSentinel

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M20", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeQuarantined {
		t.Fatalf("HandleEvent = %v, %v; want quarantined", out, err)
	}
	h.assertCounts(t, counts{CC: 1, LG: 1}) // one create attempt, no second create, no CV
	if h.dedup.unconfirmed != 1 {
		t.Errorf("MarkUnconfirmed calls = %d, want 1", h.dedup.unconfirmed)
	}
}

func TestG0_stale_recovery_never_sends(t *testing.T) {
	// A row old enough to be stale is reclaimed and quarantined, never sent.
	h := newHarness()
	h.dedup.seedProcessing(42, time.Now().Add(-2*time.Hour))

	out, err := h.engine.ReconcileStale(context.Background(), 42, time.Now().Add(-time.Hour))
	if err != nil || out != OutcomeQuarantined {
		t.Fatalf("ReconcileStale(stale) = %v, %v; want quarantined", out, err)
	}
	h.assertCounts(t, counts{}) // MS0
	if h.dedup.unconfirmed != 1 {
		t.Errorf("MarkUnconfirmed calls = %d, want 1", h.dedup.unconfirmed)
	}

	// A fresh row is not stale: Reclaim loses the guard, nothing is quarantined
	// and nothing is sent.
	fresh := newHarness()
	fresh.dedup.seedProcessing(43, time.Now())
	out, err = fresh.engine.ReconcileStale(context.Background(), 43, time.Now().Add(-time.Hour))
	if err != nil || out != OutcomeDuplicate {
		t.Fatalf("ReconcileStale(fresh) = %v, %v; want duplicate", out, err)
	}
	fresh.assertCounts(t, counts{})
	if fresh.dedup.unconfirmed != 0 {
		t.Errorf("fresh MarkUnconfirmed calls = %d, want 0", fresh.dedup.unconfirmed)
	}
}

func TestG0_late_owner_not_swallowed(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.conversations = []Conversation{openConversation(7)}
	h.dedup.completeErr = ErrNotOwner

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M22", msgType: "conversation", text: "oi",
	}))
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("HandleEvent error = %v, want ErrNotOwner", err)
	}
	if out != OutcomeFailed {
		t.Fatalf("HandleEvent = %v, want failed", out)
	}
	h.assertCounts(t, counts{MS: 1}) // MS unchanged: the send already happened once
}

func TestG0_concurrent_same_event_sends_once(t *testing.T) {
	h := newHarness()
	payload := buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M23", msgType: "conversation", text: "oi",
	})

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.engine.HandleEvent(context.Background(), Event{Connection: h.conn, Payload: payload})
		}()
	}
	wg.Wait()

	if got := h.counters.snapshot().MS; got != 1 {
		t.Errorf("messages sent = %d, want 1 (exactly one claim wins)", got)
	}
}

func TestG0_loop_echo_by_destination_id(t *testing.T) {
	h := newHarness()
	h.echo.sent[echoKey(1, "DEST-1")] = true

	out, err := h.handle(t, buildPayload(msgOpts{
		event: "SendMessage", chat: testChatJID, sender: testChatJID, id: "DEST-1",
		isFromMe: true, msgType: "conversation", text: "eco",
	}))
	if err != nil || out != OutcomeEcho {
		t.Fatalf("HandleEvent = %v, %v; want echo", out, err)
	}
	h.assertCounts(t, counts{LG: 1}) // MS0
}

func TestG0_cross_connection_isolation(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.conn = Connection{ID: 2, ChatwootInboxID: 10, IsActive: true}

	out, err := h.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "M25", msgType: "conversation", text: "oi",
	}))
	if err != nil || out != OutcomeForwarded {
		t.Fatalf("HandleEvent = %v, %v; want forwarded", out, err)
	}
	// Connection B must not reuse connection A's mapping: it creates its own.
	h.assertCounts(t, counts{CC: 1, CV: 1, MS: 1, MP: 2, LG: 1})
	if _, ok := h.mapping.rows[mappingKey(2, testPhone)]; !ok {
		t.Error("connection B mapping was not created")
	}
	if got := h.mapping.rows[mappingKey(1, testPhone)].ChatwootContactID; got != 5 {
		t.Errorf("connection A mapping contact = %d, want 5 (untouched)", got)
	}
}

// TestG0_names_never_jid_phone_placeholder asserts at the port boundary that the
// engine never hands CreateContact or UpdateContactName a JID, a raw/formatted
// phone or the "Grupo" placeholder as a display name (ROUTING_MATRIX.md §5 P4,
// AGENTS §7 rule 4). The fakes capture the actual arguments.
func TestG0_names_never_jid_phone_placeholder(t *testing.T) {
	// New group with an authoritative name: CreateContact gets the real name.
	group := newHarness()
	if _, err := group.handle(t, buildPayload(msgOpts{
		chat: testGroupJID, sender: testPhone + "@s.whatsapp.net", id: "B1",
		isGroup: true, groupName: "Equipe X", msgType: "conversation", text: "oi",
	})); err != nil {
		t.Fatalf("group HandleEvent error = %v", err)
	}
	assertDisplayNamesSafe(t, group)

	// Search-resolved direct contact: UpdateContactName gets the push name.
	direct := newHarness()
	direct.cw.searchResults = []Contact{{ID: 5, Name: "Old", Identifier: testPhone}}
	direct.cw.conversations = []Conversation{openConversation(7)}
	if _, err := direct.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "B2", pushName: "Ana",
		msgType: "conversation", text: "oi",
	})); err != nil {
		t.Fatalf("direct HandleEvent error = %v", err)
	}
	assertDisplayNamesSafe(t, direct)

	// A push name that is a formatted phone must not reach the port.
	phone := newHarness()
	phone.cw.searchResults = []Contact{{ID: 5, Name: "Old", Identifier: testPhone}}
	phone.cw.conversations = []Conversation{openConversation(7)}
	if _, err := phone.handle(t, buildPayload(msgOpts{
		chat: testChatJID, sender: testChatJID, id: "B3", pushName: "+55 11 90000-0001",
		msgType: "conversation", text: "oi",
	})); err != nil {
		t.Fatalf("phone HandleEvent error = %v", err)
	}
	assertDisplayNamesSafe(t, phone)

	if len(group.cw.createParams) == 0 || len(direct.cw.updatedNames) == 0 {
		t.Fatal("expected a captured CreateContact and UpdateContactName to assert on")
	}
}

// assertDisplayNamesSafe fails if any captured CreateContact name or
// UpdateContactName name is a JID, a phone or "Grupo".
func assertDisplayNamesSafe(t *testing.T, h *harness) {
	t.Helper()
	for _, params := range h.cw.createParams {
		if !isDisplayNameSafe(params.Name) {
			t.Errorf("CreateContact name %q is not a display-safe name", params.Name)
		}
	}
	for _, name := range h.cw.updatedNames {
		if !isDisplayNameSafe(name) {
			t.Errorf("UpdateContactName name %q is not a display-safe name", name)
		}
	}
}

// isDisplayNameSafe reports whether name is acceptable as a Chatwoot display
// name: empty (the adapter falls back to the phone digits) or a real name,
// never a JID, a raw/formatted phone or the "Grupo" placeholder.
func isDisplayNameSafe(name string) bool {
	if name == "" {
		return true
	}
	if strings.Contains(name, "@") || strings.EqualFold(name, "Grupo") {
		return false
	}
	return !looksLikePhone(name)
}
