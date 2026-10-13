package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"sync"
	"time"
)

// resolver implements the frozen G0 resolution algorithm (ROUTING_MATRIX.md
// §5/§6): mapping first, then exact search, then create only under P1–P4 /
// Q1–Q8. Search and list errors are never interpreted as "not found".
type resolver struct {
	mappings MappingStore
	chatwoot Chatwoot
	logger   *slog.Logger
	locks    *keyedMutex
	retry    readRetryPolicy
}

// contactResult is a resolved contact and the information the engine needs to
// apply the name policy. FromMap means the contact came from the mapping (no
// search), so its current display name is unknown and must not be overwritten.
// MappedConvID is the stored conversation id to re-verify during conversation
// resolution (Q4), or 0 when none is known.
type contactResult struct {
	ID           int
	Name         string
	FromMap      bool
	MappedConvID int
}

// resolveContact returns the Chatwoot contact id for the identity, creating one
// only when the preconditions hold. A create failure is wrapped in
// ErrUnconfirmed because the contact may have been created.
func (r *resolver) resolveContact(ctx context.Context, conn Connection, id Identity) (contactResult, error) {
	unlock := r.locks.lock("contact:" + id.Key)
	defer unlock()

	mapping, err := r.mappings.Find(ctx, conn.ID, id.Key)
	if err != nil {
		return contactResult{}, fmt.Errorf("lookup mapping: %w", err)
	}
	if mapping != nil && mapping.ChatwootContactID > 0 {
		return contactResult{
			ID:           mapping.ChatwootContactID,
			FromMap:      true,
			MappedConvID: mapping.ChatwootConversationID,
		}, nil
	}

	contacts, err := retryRead(ctx, r.retry, func() ([]Contact, error) {
		return r.chatwoot.SearchContactExact(ctx, id.Key, id.Kind)
	})
	if err != nil {
		return contactResult{}, fmt.Errorf("search contact: %w", err) // never a miss
	}

	switch {
	case len(contacts) > 1:
		return contactResult{}, fmt.Errorf("%w: %d distinct exact matches", ErrIdentityConflict, len(contacts))
	case len(contacts) == 1:
		contact := contacts[0]
		if contact.ID <= 0 {
			return contactResult{}, errors.New("bridge: matched contact has invalid id")
		}
		if err := r.upsertMapping(ctx, conn, id.Key, contact.ID, mappedConversationID(mapping)); err != nil {
			return contactResult{}, err
		}
		return contactResult{ID: contact.ID, Name: contact.Name}, nil
	}

	// No exact match: create, but only under P1–P4.
	if !conn.IsActive || conn.ChatwootInboxID <= 0 { // P1
		return contactResult{}, errors.New("bridge: contact creation requires an active connection with an inbox")
	}
	if id.Kind == KindGroup && id.DisplayName == "" { // P4
		return contactResult{}, errors.New("bridge: a new group requires an authoritative name")
	}
	created, err := r.chatwoot.CreateContact(ctx, ContactParams{
		Kind:     id.Kind,
		Identity: id.Key,
		Name:     id.DisplayName,
	})
	if err != nil {
		return contactResult{}, fmt.Errorf("%w: create contact failed", ErrUnconfirmed)
	}
	if created == nil || created.ID <= 0 {
		return contactResult{}, fmt.Errorf("%w: created contact has invalid id", ErrUnconfirmed)
	}
	if err := r.upsertMapping(ctx, conn, id.Key, created.ID, 0); err != nil {
		return contactResult{}, err
	}
	return contactResult{ID: created.ID, Name: created.Name}, nil
}

// resolveConversation returns the conversation the engine may reuse, creating
// one only when no usable candidate exists and the policy allows it. A list
// error is never "no candidate" (Q4); a create failure is ErrUnconfirmed.
func (r *resolver) resolveConversation(
	ctx context.Context,
	conn Connection,
	contactID, mappedConvID int,
	allowCreate bool,
) (int, error) {
	unlock := r.locks.lock("conv:" + strconv.Itoa(contactID))
	defer unlock()

	conversations, err := retryRead(ctx, r.retry, func() ([]Conversation, error) {
		return r.chatwoot.ListConversations(ctx, contactID)
	})
	if err != nil {
		return 0, fmt.Errorf("list conversations: %w", err) // never "no candidate"
	}
	if best := selectConversation(conversations, conn.ChatwootInboxID, mappedConvID); best != nil {
		return best.ID, nil
	}

	// Q4–Q8.
	if !allowCreate {
		return 0, ErrConversationUnavailable
	}
	if conn.ChatwootInboxID <= 0 { // Q5
		return 0, errors.New("bridge: conversation creation requires an inbox")
	}
	if contactID <= 0 { // Q6
		return 0, errors.New("bridge: conversation creation requires a contact")
	}
	created, err := r.chatwoot.CreateConversation(ctx, contactID, conn.ChatwootInboxID)
	if err != nil {
		return 0, fmt.Errorf("%w: create conversation failed", ErrUnconfirmed)
	}
	if created == nil || created.ID <= 0 {
		return 0, fmt.Errorf("%w: created conversation has invalid id", ErrUnconfirmed)
	}
	return created.ID, nil
}

// upsertMapping persists the resolved link. A mapping write failure is a real
// error, not an absence, so the caller fails closed.
func (r *resolver) upsertMapping(ctx context.Context, conn Connection, identityKey string, contactID, convID int) error {
	if err := r.mappings.Upsert(ctx, Mapping{
		ConnectionID:           conn.ID,
		IdentityKey:            identityKey,
		ChatwootContactID:      contactID,
		ChatwootConversationID: convID,
	}); err != nil {
		return fmt.Errorf("upsert mapping: %w", err)
	}
	return nil
}

// persistConversation writes the resolved Chatwoot conversation id into the
// contacts_map for the identity (F4/E11). It is the reverse of the lookup:
// without it, a WhatsApp-first conversation stays unmapped and the Chatwoot →
// WhatsApp direction fails closed with no_mapping.
//
// It skips the write when the stored value already equals conversationID, so
// the mapping-reuse hot path stays read-only. A lookup or write failure is a
// real error, never an absence: the caller fails closed.
func (r *resolver) persistConversation(
	ctx context.Context,
	conn Connection,
	identityKey string,
	contactID, conversationID int,
) error {
	mapping, err := r.mappings.Find(ctx, conn.ID, identityKey)
	if err != nil {
		return fmt.Errorf("lookup mapping: %w", err)
	}
	if mapping != nil && mapping.ChatwootConversationID == conversationID {
		return nil // already persisted: keep the hot path read-only
	}
	return r.upsertMapping(ctx, conn, identityKey, contactID, conversationID)
}

func mappedConversationID(mapping *Mapping) int {
	if mapping == nil {
		return 0
	}
	return mapping.ChatwootConversationID
}

// selectConversation returns the conversation to reuse (Q2/Q3): same inbox,
// not resolved, the stored mapping id preferred when it is still usable,
// otherwise the most recently active with id as tie-break. It returns nil when
// no candidate qualifies; a list error is handled by the caller and is never
// "no candidate".
func selectConversation(conversations []Conversation, inboxID, mappedConvID int) *Conversation {
	var best *Conversation
	for i := range conversations {
		conv := &conversations[i]
		if conv.InboxID != inboxID || conv.Status == "resolved" {
			continue
		}
		if conv.ID == mappedConvID {
			return conv // stored map id re-verified (Q4)
		}
		if best == nil || newerConversation(conv, best) {
			best = conv
		}
	}
	return best
}

func newerConversation(a, b *Conversation) bool {
	if a.LastActivityAt != b.LastActivityAt {
		return a.LastActivityAt > b.LastActivityAt
	}
	return a.ID > b.ID
}

// Read-only retries are bounded and jittered (ADR-0005: exponential backoff
// 2s → ×2 → cap 60s, ±20% jitter, max 3 attempts).
// Mutating calls are never retried: an unknown mutation outcome is quarantined,
// not replayed.
const (
	maxReadAttempts = 3
	readRetryBase   = 2 * time.Second
	readRetryCap    = 60 * time.Second
)

// readRetryPolicy parameterises the bounded read-only retry. jitter is
// injectable so tests can pin the delay deterministically.
type readRetryPolicy struct {
	attempts int
	base     time.Duration
	max      time.Duration
	jitter   func(time.Duration) time.Duration
}

func defaultReadRetryPolicy() readRetryPolicy {
	return readRetryPolicy{
		attempts: maxReadAttempts,
		base:     readRetryBase,
		max:      readRetryCap,
		jitter:   jitter20,
	}
}

// backoff returns the delay before the given attempt, capped then jittered.
func (p readRetryPolicy) backoff(attempt int) time.Duration {
	delay := p.base << (attempt - 1)
	if p.max > 0 && delay > p.max {
		delay = p.max
	}
	if p.jitter != nil {
		delay = p.jitter(delay)
	}
	return delay
}

// jitter20 perturbs delay by up to ±20%. math/rand's top-level functions are
// concurrency-safe, so no lock is needed.
func jitter20(delay time.Duration) time.Duration {
	delta := int64(delay) / 5
	if delta <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int63n(2*delta+1)-delta)
}

// retryable is satisfied structurally by errors that classify themselves as
// safe to retry (e.g. the Chatwoot APIError on GET requests). A mutation error
// never reports Retryable() == true.
type retryable interface{ Retryable() bool }

// retryRead calls op up to policy.attempts times while the error is retryable
// and the context is live, waiting the jittered exponential backoff between
// attempts. Only read-only operations may use it.
func retryRead[T any](ctx context.Context, policy readRetryPolicy, op func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= policy.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := op()
		if err == nil {
			return result, nil
		}
		lastErr = err
		var r retryable
		if !errors.As(err, &r) || !r.Retryable() || attempt == policy.attempts {
			return zero, err
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(policy.backoff(attempt)):
		}
	}
	return zero, lastErr
}

// keyedMutex serialises resolution per identity so concurrent events cannot
// create duplicate Chatwoot entities (C1/Q8). The lock is held across port I/O
// because that I/O is exactly the race being closed.
//
// ponytail: one unbounded map of mutexes, never evicted. Identities are bounded
// in practice; add a TTL/LRU eviction if an account ever sees unbounded growth.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: map[string]*sync.Mutex{}}
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	m, ok := k.locks[key]
	if !ok {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()

	m.Lock()
	return m.Unlock
}
