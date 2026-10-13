package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// counts are the exact effect counters asserted by the G0 suite (§6):
// CC contacts created, CV conversations created, MS messages sent,
// MP mapping upserts, NU name updates, LG logs written, ES Evolution sends
// (the Chatwoot→WhatsApp direction; the Evolution path never increments it).
type counts struct {
	CC int
	CV int
	MS int
	MP int
	NU int
	LG int
	ES int
}

// counters is a race-safe counter shared by every fake in a harness.
type counters struct {
	mu sync.Mutex
	c  counts
}

func (c *counters) add(field *int) {
	c.mu.Lock()
	*field++
	c.mu.Unlock()
}

func (c *counters) snapshot() counts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.c
}

// fakeChatwoot counts every effect and lets a test inject results and errors.
type fakeChatwoot struct {
	mu       sync.Mutex
	counters *counters

	searchResults []Contact
	searchErr     error

	createErr    error
	createSeq    int
	createParams []ContactParams // captured for the port-boundary name assertion

	updateNameErr error
	updatedNames  []string // captured for the port-boundary name assertion

	conversations []Conversation
	listErr       error

	createConvErr error
	createConvSeq int

	sendErr     error
	sendEmptyID bool   // force a confirmed send with no destination id
	sendID      string // force a confirmed send with this destination id
	sendSeq     int
}

func (f *fakeChatwoot) SearchContactExact(_ context.Context, _ string, _ Kind) ([]Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return append([]Contact(nil), f.searchResults...), nil
}

func (f *fakeChatwoot) CreateContact(_ context.Context, params ContactParams) (*Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters.add(&f.counters.c.CC)
	f.createParams = append(f.createParams, params)
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.createSeq++
	return &Contact{ID: 1000 + f.createSeq, Name: params.Name, Identifier: params.Identity}, nil
}

func (f *fakeChatwoot) UpdateContactName(_ context.Context, _ int, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters.add(&f.counters.c.NU)
	f.updatedNames = append(f.updatedNames, name)
	return f.updateNameErr
}

func (f *fakeChatwoot) ListConversations(_ context.Context, _ int) ([]Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]Conversation(nil), f.conversations...), nil
}

func (f *fakeChatwoot) CreateConversation(_ context.Context, contactID, inboxID int) (*Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters.add(&f.counters.c.CV)
	if f.createConvErr != nil {
		return nil, f.createConvErr
	}
	f.createConvSeq++
	return &Conversation{ID: 2000 + f.createConvSeq, InboxID: inboxID, Status: "open"}, nil
}

func (f *fakeChatwoot) SendText(_ context.Context, _ int, _ string, _ MessageDirection) (*SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters.add(&f.counters.c.MS)
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	if f.sendEmptyID {
		return &SendResult{MessageID: ""}, nil
	}
	if f.sendID != "" {
		return &SendResult{MessageID: f.sendID}, nil
	}
	f.sendSeq++
	return &SendResult{MessageID: fmt.Sprintf("cw-%d", f.sendSeq)}, nil
}

// fakeMapping is a connection-scoped contacts_map.
type fakeMapping struct {
	mu       sync.Mutex
	counters *counters
	rows     map[string]Mapping

	upsertErr error
	// failConvPersist makes upsertErr fire only for a mapping that carries a
	// conversation id (the F4 persist path), leaving the contact-only upsert
	// from resolveContact intact — the exact F4 write the fail-closed case
	// must exercise.
	failConvPersist bool
}

func mappingKey(connectionID int, identityKey string) string {
	return fmt.Sprintf("%d\x00%s", connectionID, identityKey)
}

func (f *fakeMapping) Find(_ context.Context, connectionID int, identityKey string) (*Mapping, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mapping, ok := f.rows[mappingKey(connectionID, identityKey)]
	if !ok {
		return nil, nil
	}
	return &mapping, nil
}

func (f *fakeMapping) Upsert(_ context.Context, mapping Mapping) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil && (!f.failConvPersist || mapping.ChatwootConversationID > 0) {
		return f.upsertErr
	}
	f.counters.add(&f.counters.c.MP)
	f.rows[mappingKey(mapping.ConnectionID, mapping.IdentityKey)] = mapping
	return nil
}

// fakeEcho is the bridge-sent destination-id store.
type fakeEcho struct {
	mu   sync.Mutex
	sent map[string]bool
	// forwarded is the Evolution→Chatwoot destination-id store (F5 loop-break).
	forwarded    map[string]bool
	forwardedErr error
	err          error
	// errUntil > 0 makes err fail only the first errUntil calls (to exercise the
	// bounded read retry); 0 means err always fails.
	errUntil int
	calls    int
}

func echoKey(connectionID int, destinationMessageID string) string {
	return fmt.Sprintf("%d\x00%s", connectionID, destinationMessageID)
}

func (f *fakeEcho) WasBridgeSent(_ context.Context, connectionID int, destinationMessageID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil && (f.errUntil == 0 || f.calls <= f.errUntil) {
		return false, f.err
	}
	return f.sent[echoKey(connectionID, destinationMessageID)], nil
}

// WasForwardedToChatwoot reports whether an Evolution→Chatwoot forward recorded
// the Chatwoot message id (F5). It is the inverse scope of WasBridgeSent.
func (f *fakeEcho) WasForwardedToChatwoot(_ context.Context, connectionID int, chatwootMessageID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forwardedErr != nil {
		return false, f.forwardedErr
	}
	return f.forwarded[echoKey(connectionID, chatwootMessageID)], nil
}

// fakeEvolution counts Evolution sends and captures the number/text so the
// Chatwoot→WhatsApp tests can assert the port boundary (E11).
type fakeEvolution struct {
	mu       sync.Mutex
	counters *counters

	sendErr     error
	sendID      string
	sendEmptyID bool
	numbers     []string
	texts       []string
}

func (f *fakeEvolution) SendText(_ context.Context, number, text string) (*SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters.add(&f.counters.c.ES)
	f.numbers = append(f.numbers, number)
	f.texts = append(f.texts, text)
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	if f.sendEmptyID {
		return &SendResult{MessageID: ""}, nil
	}
	if f.sendID != "" {
		return &SendResult{MessageID: f.sendID}, nil
	}
	return &SendResult{MessageID: fmt.Sprintf("evo-%d", len(f.numbers))}, nil
}

// fakeConversations is the reverse contacts_map lookup (E11/F7).
type fakeConversations struct {
	mu    sync.Mutex
	rows  map[string]Mapping
	err   error
	calls int
}

func conversationKey(connectionID, chatwootConversationID int) string {
	return fmt.Sprintf("%d\x00%d", connectionID, chatwootConversationID)
}

func (f *fakeConversations) FindByConversation(_ context.Context, connectionID, chatwootConversationID int) (*Mapping, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	mapping, ok := f.rows[conversationKey(connectionID, chatwootConversationID)]
	if !ok {
		return nil, nil
	}
	return &mapping, nil
}

// dedupRow is one claimed event in the fake dedup store.
type dedupRow struct {
	id        int64
	token     string
	status    string
	attempt   int
	claimedAt time.Time
}

// fakeDedup is an atomic, race-safe Claim/transition store (ADR-0004 §2/§4).
type fakeDedup struct {
	mu          sync.Mutex
	byKey       map[string]int64
	rows        map[int64]*dedupRow
	seq         int
	completeErr error

	completed   int
	unconfirmed int
	duplicate   int
	failed      int
	released    int
	failCodes   []string
}

func dedupKeyString(key Key) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s",
		key.ConnectionID, key.SourceSystem, key.EventType, key.SourceMessageID)
}

func (f *fakeDedup) Claim(_ context.Context, key Key, _ time.Duration) (bool, int64, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byKey[dedupKeyString(key)]; ok {
		return false, id, "", nil
	}
	f.seq++
	id := int64(f.seq)
	token := fmt.Sprintf("%032x", f.seq)
	f.byKey[dedupKeyString(key)] = id
	f.rows[id] = &dedupRow{id: id, token: token, status: "processing", claimedAt: time.Now()}
	return true, id, token, nil
}

// transition moves a processing row to a terminal status, ownership-guarded.
// The terminal kind is recorded verbatim (sent / duplicate / failed /
// unconfirmed_send) so tests can assert which transition ran, not just that the
// row left 'processing'.
func (f *fakeDedup) transition(id int64, token, terminal string) error {
	row, ok := f.rows[id]
	if !ok || row.token != token || row.status != "processing" {
		return fmt.Errorf("%w: id %d", ErrNotOwner, id)
	}
	row.status = terminal
	return nil
}

func (f *fakeDedup) Complete(_ context.Context, id int64, token, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed++
	if f.completeErr != nil {
		return f.completeErr
	}
	return f.transition(id, token, "sent")
}

func (f *fakeDedup) MarkDuplicate(_ context.Context, id int64, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.duplicate++
	return f.transition(id, token, "duplicate")
}

func (f *fakeDedup) MarkUnconfirmed(_ context.Context, id int64, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unconfirmed++
	return f.transition(id, token, "unconfirmed_send")
}

func (f *fakeDedup) MarkFailed(_ context.Context, id int64, token, errorCode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed++
	f.failCodes = append(f.failCodes, errorCode)
	return f.transition(id, token, "failed")
}

func (f *fakeDedup) Reclaim(_ context.Context, id int64, staleBefore time.Time) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.status != "processing" || row.claimedAt.After(staleBefore) {
		return "", false, nil // missing, terminal, or not yet stale
	}
	row.attempt++
	row.token = fmt.Sprintf("reclaimed-%d", id)
	return row.token, true, nil
}

// Release removes a pre-admission claim (the ingress release path). It is
// ownership-guarded like every transition.
func (f *fakeDedup) Release(_ context.Context, id int64, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.token != token || row.status != "processing" {
		return fmt.Errorf("%w: id %d", ErrNotOwner, id)
	}
	delete(f.rows, id)
	for key, rowID := range f.byKey {
		if rowID == id {
			delete(f.byKey, key)
		}
	}
	f.released++
	return nil
}

// status returns the stored status of a row ("" when absent).
func (f *fakeDedup) status(id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return ""
	}
	return row.status
}

// seedProcessing inserts a processing row claimed at the given time for the
// recovery test.
func (f *fakeDedup) seedProcessing(id int64, claimedAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id] = &dedupRow{id: id, token: "seeded", status: "processing", claimedAt: claimedAt}
}

// fakeLogs counts message_logs inserts.
type fakeLogs struct {
	mu       sync.Mutex
	counters *counters
	entries  []LogEntry
	err      error
}

func (f *fakeLogs) Insert(_ context.Context, entry LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters.add(&f.counters.c.LG)
	f.entries = append(f.entries, entry)
	return f.err
}

// harness wires an Engine to counting fakes for one connection.
type harness struct {
	counters *counters
	mapping  *fakeMapping
	cw       *fakeChatwoot
	echo     *fakeEcho
	dedup    *fakeDedup
	logs     *fakeLogs
	evo      *fakeEvolution
	convs    *fakeConversations
	engine   *Engine
	conn     Connection
}

func newHarness() *harness {
	c := &counters{}
	h := &harness{
		counters: c,
		mapping:  &fakeMapping{counters: c, rows: map[string]Mapping{}},
		cw:       &fakeChatwoot{counters: c},
		echo:     &fakeEcho{sent: map[string]bool{}, forwarded: map[string]bool{}},
		dedup:    &fakeDedup{byKey: map[string]int64{}, rows: map[int64]*dedupRow{}},
		logs:     &fakeLogs{counters: c},
		evo:      &fakeEvolution{counters: c},
		convs:    &fakeConversations{rows: map[string]Mapping{}},
		conn:     Connection{ID: 1, ChatwootInboxID: 10, IsActive: true},
	}
	h.engine = New(Deps{
		Mappings:      h.mapping,
		Chatwoot:      h.cw,
		Echo:          h.echo,
		Dedup:         h.dedup,
		Logs:          h.logs,
		Evolution:     h.evo,
		Conversations: h.convs,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return h
}

// fastReadRetry is an instant, jitter-free read-retry policy for tests.
func fastReadRetry() readRetryPolicy {
	return readRetryPolicy{
		attempts: maxReadAttempts,
		base:     time.Millisecond,
		max:      10 * time.Millisecond,
		jitter:   func(d time.Duration) time.Duration { return d },
	}
}

// useFastRetry swaps the harness's read-retry policy for the fast one, on both
// the engine (echo lookup) and the resolver (search/list).
func (h *harness) useFastRetry() {
	h.engine.retry = fastReadRetry()
	h.engine.resolver.retry = fastReadRetry()
}

func (h *harness) handle(t *testing.T, payload []byte) (Outcome, error) {
	t.Helper()
	return h.engine.HandleEvent(context.Background(), Event{Connection: h.conn, Payload: payload})
}

func (h *harness) assertCounts(t *testing.T, want counts) {
	t.Helper()
	if got := h.counters.snapshot(); got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
}

// msgOpts builds a synthetic Evolution payload. No real phone numbers or group
// names are used (AGENTS §9).
type msgOpts struct {
	event     string
	chat      string
	sender    string
	id        string
	isFromMe  bool
	isGroup   bool
	pushName  string
	groupName string
	msgType   string
	text      string
}

func buildPayload(o msgOpts) []byte {
	event := o.event
	if event == "" {
		event = "Message"
	}
	info := map[string]any{
		"Chat":     o.chat,
		"Sender":   o.sender,
		"ID":       o.id,
		"IsFromMe": o.isFromMe,
		"IsGroup":  o.isGroup,
		"PushName": o.pushName,
	}
	data := map[string]any{"Info": info}
	if o.groupName != "" {
		data["groupData"] = map[string]any{"Name": o.groupName}
	}
	switch o.msgType {
	case "", "none":
	case "conversation":
		data["Message"] = map[string]any{"conversation": o.text}
	case "extendedTextMessage":
		data["Message"] = map[string]any{"extendedTextMessage": map[string]any{"text": o.text}}
	case "imageMessage":
		data["Message"] = map[string]any{"imageMessage": map[string]any{"caption": o.text}}
	case "protocolMessage":
		data["Message"] = map[string]any{"protocolMessage": map[string]any{}}
	case "reactionMessage":
		data["Message"] = map[string]any{"reactionMessage": map[string]any{}}
	case "stickerMessage":
		data["Message"] = map[string]any{"stickerMessage": map[string]any{}}
	}
	return mustJSON(map[string]any{"event": event, "data": data})
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// errSentinel is a fixed, non-retryable error used to inject failures.
var errSentinel = errors.New("synthetic failure")
