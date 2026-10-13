package bridge

import (
	"context"
	"errors"
	"testing"
)

func newTestResolver(h *harness) *resolver {
	return &resolver{
		mappings: h.mapping,
		chatwoot: h.cw,
		logger:   h.engine.logger,
		locks:    newKeyedMutex(),
		retry:    fastReadRetry(),
	}
}

func directIdentity() Identity {
	return Identity{Kind: KindDirect, Key: testPhone, DisplayName: "Ana"}
}

func TestResolveContact_MappingWins(t *testing.T) {
	h := newHarness()
	h.mapping.rows[mappingKey(1, testPhone)] = Mapping{
		ConnectionID: 1, IdentityKey: testPhone, ChatwootContactID: 5, ChatwootConversationID: 7,
	}
	h.cw.searchErr = errSentinel // must never be reached

	got, err := newTestResolver(h).resolveContact(context.Background(), h.conn, directIdentity())
	if err != nil {
		t.Fatalf("resolveContact() error = %v", err)
	}
	if got.ID != 5 || !got.FromMap || got.MappedConvID != 7 {
		t.Errorf("resolveContact() = %+v", got)
	}
	if c := h.counters.snapshot(); c.CC != 0 || c.MP != 0 {
		t.Errorf("counts = %+v, want no create and no upsert", c)
	}
}

func TestResolveContact_SearchErrorFailsClosed(t *testing.T) {
	h := newHarness()
	h.cw.searchErr = errSentinel

	if _, err := newTestResolver(h).resolveContact(context.Background(), h.conn, directIdentity()); err == nil {
		t.Fatal("resolveContact() error = nil, want error (error is never a miss)")
	}
	if c := h.counters.snapshot(); c.CC != 0 {
		t.Errorf("contacts created = %d, want 0", c.CC)
	}
}

func TestResolveContact_IdentityConflict(t *testing.T) {
	h := newHarness()
	h.cw.searchResults = []Contact{{ID: 5}, {ID: 6}}

	_, err := newTestResolver(h).resolveContact(context.Background(), h.conn, directIdentity())
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("resolveContact() error = %v, want ErrIdentityConflict", err)
	}
	if c := h.counters.snapshot(); c.CC != 0 || c.MP != 0 {
		t.Errorf("counts = %+v, want no create and no upsert", c)
	}
}

func TestResolveContact_SearchHitUpserts(t *testing.T) {
	h := newHarness()
	h.cw.searchResults = []Contact{{ID: 5, Name: "Ana", Identifier: testPhone}}

	got, err := newTestResolver(h).resolveContact(context.Background(), h.conn, directIdentity())
	if err != nil {
		t.Fatalf("resolveContact() error = %v", err)
	}
	if got.ID != 5 || got.FromMap {
		t.Errorf("resolveContact() = %+v", got)
	}
	if c := h.counters.snapshot(); c.CC != 0 || c.MP != 1 {
		t.Errorf("counts = %+v, want CC0 MP1", c)
	}
}

func TestResolveContact_CreatesWhenAbsent(t *testing.T) {
	h := newHarness()
	got, err := newTestResolver(h).resolveContact(context.Background(), h.conn, directIdentity())
	if err != nil {
		t.Fatalf("resolveContact() error = %v", err)
	}
	if got.ID <= 0 {
		t.Errorf("resolveContact() id = %d", got.ID)
	}
	if c := h.counters.snapshot(); c.CC != 1 || c.MP != 1 {
		t.Errorf("counts = %+v, want CC1 MP1", c)
	}
}

func TestResolveContact_InactiveConnectionFailsP1(t *testing.T) {
	h := newHarness()
	conn := Connection{ID: 1, ChatwootInboxID: 0, IsActive: true} // no inbox

	if _, err := newTestResolver(h).resolveContact(context.Background(), conn, directIdentity()); err == nil {
		t.Fatal("resolveContact() error = nil, want P1 failure")
	}
	if c := h.counters.snapshot(); c.CC != 0 {
		t.Errorf("contacts created = %d, want 0", c.CC)
	}
}

func TestResolveContact_GroupRequiresNameP4(t *testing.T) {
	h := newHarness()
	identity := Identity{Kind: KindGroup, Key: testGroupJID} // no authoritative name

	if _, err := newTestResolver(h).resolveContact(context.Background(), h.conn, identity); err == nil {
		t.Fatal("resolveContact() error = nil, want P4 failure")
	}
	if c := h.counters.snapshot(); c.CC != 0 {
		t.Errorf("contacts created = %d, want 0", c.CC)
	}
}

func TestResolveConversation_ListErrorIsNotNoCandidate(t *testing.T) {
	h := newHarness()
	h.cw.listErr = errSentinel

	if _, err := newTestResolver(h).resolveConversation(context.Background(), h.conn, 5, 0, true); err == nil {
		t.Fatal("resolveConversation() error = nil, want error")
	}
	if c := h.counters.snapshot(); c.CV != 0 {
		t.Errorf("conversations created = %d, want 0", c.CV)
	}
}

func TestResolveConversation_ReusesUsable(t *testing.T) {
	h := newHarness()
	h.cw.conversations = []Conversation{
		{ID: 1, InboxID: 99, Status: "open", LastActivityAt: 5}, // wrong inbox
		{ID: 2, InboxID: 10, Status: "resolved", LastActivityAt: 6},
		{ID: 3, InboxID: 10, Status: "open", LastActivityAt: 3},
		{ID: 4, InboxID: 10, Status: "open", LastActivityAt: 4},
	}

	got, err := newTestResolver(h).resolveConversation(context.Background(), h.conn, 5, 0, true)
	if err != nil {
		t.Fatalf("resolveConversation() error = %v", err)
	}
	if got != 4 { // most recent usable
		t.Errorf("resolveConversation() = %d, want 4", got)
	}
	if c := h.counters.snapshot(); c.CV != 0 {
		t.Errorf("conversations created = %d, want 0", c.CV)
	}
}

func TestResolveConversation_PrefersStoredMapID(t *testing.T) {
	h := newHarness()
	h.cw.conversations = []Conversation{
		{ID: 3, InboxID: 10, Status: "open", LastActivityAt: 9},
		{ID: 7, InboxID: 10, Status: "open", LastActivityAt: 1},
	}

	got, err := newTestResolver(h).resolveConversation(context.Background(), h.conn, 5, 7, true)
	if err != nil {
		t.Fatalf("resolveConversation() error = %v", err)
	}
	if got != 7 {
		t.Errorf("resolveConversation() = %d, want stored map id 7 (re-verified)", got)
	}
}

func TestResolveConversation_CreatesWhenNone(t *testing.T) {
	h := newHarness()
	got, err := newTestResolver(h).resolveConversation(context.Background(), h.conn, 5, 0, true)
	if err != nil {
		t.Fatalf("resolveConversation() error = %v", err)
	}
	if got <= 0 {
		t.Errorf("resolveConversation() = %d, want a new conversation", got)
	}
	if c := h.counters.snapshot(); c.CV != 1 {
		t.Errorf("conversations created = %d, want 1", c.CV)
	}
}

func TestResolveConversation_CreateForbidden(t *testing.T) {
	h := newHarness()
	_, err := newTestResolver(h).resolveConversation(context.Background(), h.conn, 5, 0, false)
	if !errors.Is(err, ErrConversationUnavailable) {
		t.Fatalf("resolveConversation() error = %v, want ErrConversationUnavailable", err)
	}
	if c := h.counters.snapshot(); c.CV != 0 {
		t.Errorf("conversations created = %d, want 0", c.CV)
	}
}
