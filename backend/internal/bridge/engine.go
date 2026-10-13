package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// previewLimit bounds the stored message_logs preview (message-engine.md).
const previewLimit = 500

// Outcome is the terminal disposition of a handled event.
type Outcome int

const (
	// OutcomeUnknown is the zero value: no disposition was assigned.
	OutcomeUnknown Outcome = iota
	// OutcomeIgnored means the event was dropped without touching a port.
	OutcomeIgnored
	// OutcomeForwarded means the message was sent and the claim completed.
	OutcomeForwarded
	// OutcomeEcho means a bridge-originated message was recognised and marked
	// duplicate (rule 6).
	OutcomeEcho
	// OutcomeDuplicate means the claim was already owned elsewhere.
	OutcomeDuplicate
	// OutcomeFailed means a fail-attend outcome: no entity, claim closed failed.
	OutcomeFailed
	// OutcomeMalformed means the payload was unparseable: no port was touched
	// and no entity was created. The ingress slice maps it to 400.
	OutcomeMalformed
	// OutcomeQuarantined means an unknown mutation outcome was recorded
	// unconfirmed; it is never retried (ADR-0004 §5).
	OutcomeQuarantined
)

// String returns the outcome name.
func (o Outcome) String() string {
	switch o {
	case OutcomeUnknown:
		return "unknown"
	case OutcomeIgnored:
		return "ignored"
	case OutcomeForwarded:
		return "forwarded"
	case OutcomeEcho:
		return "echo"
	case OutcomeDuplicate:
		return "duplicate"
	case OutcomeFailed:
		return "failed"
	case OutcomeMalformed:
		return "malformed"
	case OutcomeQuarantined:
		return "quarantined"
	default:
		return "unknown"
	}
}

// Deps are the ports and configuration of an Engine.
type Deps struct {
	Mappings MappingStore
	Chatwoot Chatwoot
	Echo     EchoStore
	Dedup    Dedup
	Logs     MessageLogs
	// Evolution and Conversations are the Chatwoot → WhatsApp ports (E11). They
	// are optional: New does not panic on a nil value (the Evolution path never
	// reads them), and HandleChatwootClaimed fails the claim closed when either
	// is nil (spec §2.6).
	Evolution     Evolution
	Conversations ConversationMappingStore
	Logger        *slog.Logger
	ClaimTTL      time.Duration
}

// Engine is the pure Evolution → Chatwoot routing engine. It owns no transport
// and no storage: every effect goes through a port.
type Engine struct {
	chatwoot      Chatwoot
	echo          EchoStore
	dedup         Dedup
	logs          MessageLogs
	evolution     Evolution
	conversations ConversationMappingStore
	logger        *slog.Logger
	claimTTL      time.Duration
	retry         readRetryPolicy
	resolver      *resolver
}

// New builds an Engine. A nil required port is a wiring bug and panics, matching
// dedup.New's contract: the failure is a programming error, not a runtime input.
func New(deps Deps) *Engine {
	switch {
	case deps.Mappings == nil:
		panic("bridge: New requires a MappingStore")
	case deps.Chatwoot == nil:
		panic("bridge: New requires a Chatwoot port")
	case deps.Echo == nil:
		panic("bridge: New requires an EchoStore")
	case deps.Dedup == nil:
		panic("bridge: New requires a Dedup port")
	case deps.Logs == nil:
		panic("bridge: New requires a MessageLogs port")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ttl := deps.ClaimTTL
	if ttl <= 0 {
		ttl = defaultClaimTTL
	}
	retry := defaultReadRetryPolicy()
	return &Engine{
		chatwoot:      deps.Chatwoot,
		echo:          deps.Echo,
		dedup:         deps.Dedup,
		logs:          deps.Logs,
		evolution:     deps.Evolution,
		conversations: deps.Conversations,
		logger:        logger,
		claimTTL:      ttl,
		retry:         retry,
		resolver: &resolver{
			mappings: deps.Mappings,
			chatwoot: deps.Chatwoot,
			logger:   logger,
			locks:    newKeyedMutex(),
			retry:    retry,
		},
	}
}

// HandleEvent parses, validates the identity, decides and acts. Exactly one send
// attempt is made per claim; an unknown mutation outcome is terminal.
func (e *Engine) HandleEvent(ctx context.Context, ev Event) (Outcome, error) {
	parsed, err := Parse(ev.Payload)
	if err != nil {
		// A malformed payload is a distinct, caller-actionable signal (not the
		// E7/E8 OutcomeIgnored): the ingress slice maps OutcomeMalformed to 400.
		// It touches no port and creates no entity. The caller owns the log
		// (single-handling rule), so the engine does not also log it.
		return OutcomeMalformed, fmt.Errorf("%w: %v", ErrMalformedPayload, err)
	}

	decision := Route(parsed, ev.Connection)
	switch decision.Action {
	case ActionIgnore:
		e.logger.Debug("bridge: event ignored", "connection_id", ev.Connection.ID, "reason", decision.Reason)
		return OutcomeIgnored, nil
	case ActionFail:
		e.logger.Warn("bridge: fail-attend", "connection_id", ev.Connection.ID, "reason", decision.Reason)
		return OutcomeFailed, nil
	case ActionForward:
		// resolved below
	default:
		// ActionUnknown (zero value) is a wiring bug: fail closed, no port.
		e.logger.Warn("bridge: unknown routing action", "connection_id", ev.Connection.ID)
		return OutcomeFailed, nil
	}

	identity, err := ParseIdentity(parsed)
	if err != nil {
		// E14: malformed identity touches no port and creates nothing.
		e.logger.Warn("bridge: malformed identity", "connection_id", ev.Connection.ID, "event", parsed.EventName)
		return OutcomeFailed, nil
	}
	if parsed.MessageID == "" {
		e.logger.Warn("bridge: event has no message id", "connection_id", ev.Connection.ID)
		return OutcomeFailed, nil
	}

	if parsed.IsFromMe {
		// The echo lookup is a read-only call: it goes through the same bounded
		// backoff+jitter retry as the resolver's reads (ADR-0005).
		sent, echoErr := retryRead(ctx, e.retry, func() (bool, error) {
			return e.echo.WasBridgeSent(ctx, ev.Connection.ID, parsed.MessageID)
		})
		if echoErr != nil {
			e.logger.Warn("bridge: echo lookup failed", "connection_id", ev.Connection.ID, "error", echoErr)
			return OutcomeFailed, echoErr
		}
		if sent {
			return e.handleEcho(ctx, ev, parsed, identity)
		}
	}

	return e.forward(ctx, ev, parsed, identity, decision)
}

// ReconcileStale takes over a stale processing row for quarantine only. It never
// sends: a stale row cannot prove an external effect did not begin (ADR-0004 §8).
func (e *Engine) ReconcileStale(ctx context.Context, id int64, staleBefore time.Time) (Outcome, error) {
	token, won, err := e.dedup.Reclaim(ctx, id, staleBefore)
	if err != nil {
		e.logger.Error("bridge: reclaim failed", "claim_id", id, "error", err)
		return OutcomeFailed, err
	}
	if !won {
		return OutcomeDuplicate, nil // lost the race, or the row is not stale
	}
	if err := e.dedup.MarkUnconfirmed(ctx, id, token); err != nil {
		return e.finishError(ctx, "quarantine stale claim", err)
	}
	e.logger.Warn("bridge: stale claim quarantined", "claim_id", id)
	return OutcomeQuarantined, nil
}

// handleEcho recognises a bridge-originated message and closes its claim as
// duplicate (rule 6). It never sends.
func (e *Engine) handleEcho(ctx context.Context, ev Event, parsed *ParsedEvent, identity Identity) (Outcome, error) {
	claimed, claimID, token, err := e.dedup.Claim(ctx, e.eventKey(ev.Connection.ID, parsed), e.claimTTL)
	if err != nil {
		e.logger.Error("bridge: echo claim failed", "connection_id", ev.Connection.ID, "error", err)
		return OutcomeFailed, err
	}
	if !claimed {
		return OutcomeDuplicate, nil
	}
	if err := e.dedup.MarkDuplicate(ctx, claimID, token); err != nil {
		return e.finishError(ctx, "mark duplicate", err)
	}
	e.insertLog(ctx, e.logEntry(ev.Connection.ID, parsed, identity, "duplicate", DirectionOutgoing, 0, ""))
	return OutcomeEcho, nil
}

// forward claims the event, then delivers it. It is the pure/test entry point:
// HandleClaimed is the ingress entry point for a claim already won.
func (e *Engine) forward(
	ctx context.Context,
	ev Event,
	parsed *ParsedEvent,
	identity Identity,
	decision Decision,
) (Outcome, error) {
	claimed, claimID, token, err := e.dedup.Claim(ctx, e.eventKey(ev.Connection.ID, parsed), e.claimTTL)
	if err != nil {
		e.logger.Error("bridge: claim failed", "connection_id", ev.Connection.ID, "error", err)
		return OutcomeFailed, err
	}
	if !claimed {
		return OutcomeDuplicate, nil
	}
	return e.deliver(ctx, ev, parsed, identity, decision, claimID, token)
}

// HandleClaimed runs the engine for a claim already won by the ingress. It
// never calls Claim: the ingress owns the durable claim before ACK and the
// worker must not re-claim (a second claim would lose and silently drop the
// event as duplicate). Every path reaches a terminal transition for the given
// claim: forward → Complete | MarkUnconfirmed; ignore → MarkDuplicate;
// fail/malformed → MarkFailed. A malformed payload (which cannot arrive from
// ingress, which parses first) closes the claim fail-closed rather than leaving
// a stuck row.
func (e *Engine) HandleClaimed(ctx context.Context, ev Event, claimID int64, token string) (Outcome, error) {
	parsed, err := Parse(ev.Payload)
	if err != nil {
		if markErr := e.dedup.MarkFailed(ctx, claimID, token, "malformed"); markErr != nil {
			return e.finishError(ctx, "mark failed malformed", markErr)
		}
		return OutcomeMalformed, fmt.Errorf("%w: %v", ErrMalformedPayload, err)
	}

	decision := Route(parsed, ev.Connection)
	switch decision.Action {
	case ActionIgnore:
		// An ignored event is intentionally not delivered; close the claim once
		// so it is not mis-quarantined at boot (ADR-0004 §3).
		if err := e.dedup.MarkDuplicate(ctx, claimID, token); err != nil {
			return e.finishError(ctx, "mark duplicate", err)
		}
		e.logger.Debug("bridge: claimed event ignored", "connection_id", ev.Connection.ID, "reason", decision.Reason)
		return OutcomeIgnored, nil
	case ActionFail:
		if err := e.dedup.MarkFailed(ctx, claimID, token, "fail_attend"); err != nil {
			return e.finishError(ctx, "mark failed", err)
		}
		e.logger.Warn("bridge: claimed fail-attend", "connection_id", ev.Connection.ID, "reason", decision.Reason)
		return OutcomeFailed, nil
	case ActionForward:
		// handled below
	default:
		// ActionUnknown (zero value) is a wiring bug: fail closed, no port.
		if err := e.dedup.MarkFailed(ctx, claimID, token, "unknown_action"); err != nil {
			return e.finishError(ctx, "mark failed", err)
		}
		e.logger.Warn("bridge: unknown routing action", "connection_id", ev.Connection.ID)
		return OutcomeFailed, nil
	}

	identity, err := ParseIdentity(parsed)
	if err != nil {
		if markErr := e.dedup.MarkFailed(ctx, claimID, token, "malformed_identity"); markErr != nil {
			return e.finishError(ctx, "mark failed", markErr)
		}
		e.logger.Warn("bridge: malformed identity", "connection_id", ev.Connection.ID, "event", parsed.EventName)
		return OutcomeFailed, nil
	}
	if parsed.MessageID == "" {
		if markErr := e.dedup.MarkFailed(ctx, claimID, token, "missing_message_id"); markErr != nil {
			return e.finishError(ctx, "mark failed", markErr)
		}
		e.logger.Warn("bridge: event has no message id", "connection_id", ev.Connection.ID)
		return OutcomeFailed, nil
	}

	if parsed.IsFromMe {
		sent, echoErr := retryRead(ctx, e.retry, func() (bool, error) {
			return e.echo.WasBridgeSent(ctx, ev.Connection.ID, parsed.MessageID)
		})
		if echoErr != nil {
			if markErr := e.dedup.MarkFailed(ctx, claimID, token, "echo_lookup_failed"); markErr != nil {
				return e.finishError(ctx, "mark failed", markErr)
			}
			e.logger.Warn("bridge: echo lookup failed", "connection_id", ev.Connection.ID, "error", echoErr)
			return OutcomeFailed, echoErr
		}
		if sent {
			if err := e.dedup.MarkDuplicate(ctx, claimID, token); err != nil {
				return e.finishError(ctx, "mark duplicate", err)
			}
			e.insertLog(ctx, e.logEntry(ev.Connection.ID, parsed, identity, "duplicate", DirectionOutgoing, 0, ""))
			return OutcomeEcho, nil
		}
	}

	return e.deliver(ctx, ev, parsed, identity, decision, claimID, token)
}

// HandleChatwootClaimed runs the Chatwoot → WhatsApp engine (E10–E13) for a
// claim already won by the ingress. It never calls Claim. Every path reaches a
// terminal transition: ignore/loop → MarkDuplicate; fail-attend → MarkFailed
// (no entity); forward → Complete | MarkUnconfirmed | MarkFailed. It creates no
// Chatwoot entity (E12): no contact, no conversation, no name update — the
// reverse mapping is only read.
func (e *Engine) HandleChatwootClaimed(ctx context.Context, ev Event, claimID int64, token string) (Outcome, error) {
	cw, err := ParseChatwoot(ev.Payload)
	if err != nil {
		if markErr := e.dedup.MarkFailed(ctx, claimID, token, "malformed"); markErr != nil {
			return e.finishError(ctx, "mark failed malformed", markErr)
		}
		return OutcomeMalformed, fmt.Errorf("%w: %v", ErrMalformedPayload, err)
	}

	// F1–F4 + the message_type allowlist: payload-only, no port touched.
	switch decision := RouteChatwoot(cw, ev.Connection); decision.Action {
	case ActionIgnore:
		return e.ignoreChatwootClaim(ctx, ev.Connection.ID, claimID, token, decision.Reason)
	case ActionFail:
		return e.failChatwootClaim(ctx, ev.Connection.ID, claimID, token, "fail_attend", decision.Reason)
	case ActionForward:
		// handled below
	default:
		return e.failChatwootClaim(ctx, ev.Connection.ID, claimID, token, "unknown_action", "unknown routing action")
	}

	// A stable message id is required for F5 and the audit row (E13's ingress
	// claim already dedups re-deliveries by this id).
	if cw.MessageID == "" {
		return e.failChatwootClaim(ctx, ev.Connection.ID, claimID, token, "missing_message_id", "no message id")
	}
	// The Chatwoot direction needs both ports; nil means unwired (spec §2.6).
	if e.evolution == nil || e.conversations == nil {
		return e.failChatwootClaim(ctx, ev.Connection.ID, claimID, token, "not_configured", "chatwoot ports not wired")
	}

	// F5 loop-break: a Chatwoot message the bridge itself created in Chatwoot
	// (the EVO→CW forward) is ignored, never sent back to WhatsApp. Read-only,
	// under the same bounded retry as the other reads.
	forwarded, echoErr := retryRead(ctx, e.retry, func() (bool, error) {
		return e.echo.WasForwardedToChatwoot(ctx, ev.Connection.ID, cw.MessageID)
	})
	if echoErr != nil {
		if markErr := e.dedup.MarkFailed(ctx, claimID, token, "echo_lookup_failed"); markErr != nil {
			return e.finishError(ctx, "mark failed", markErr)
		}
		e.logger.Warn("bridge: chatwoot echo lookup failed", "connection_id", ev.Connection.ID, "error", echoErr)
		return OutcomeFailed, echoErr
	}
	if forwarded {
		return e.ignoreChatwootClaim(ctx, ev.Connection.ID, claimID, token, "bridge-authored chatwoot message")
	}

	// F6: no conversation (display_id) means no reverse mapping is possible.
	if cw.ConversationID <= 0 {
		return e.ignoreChatwootClaim(ctx, ev.Connection.ID, claimID, token, "no conversation id")
	}

	// F7: reuse the existing mapping, never create a Chatwoot entity (E12).
	mapping, mapErr := retryRead(ctx, e.retry, func() (*Mapping, error) {
		return e.conversations.FindByConversation(ctx, ev.Connection.ID, cw.ConversationID)
	})
	if mapErr != nil {
		if markErr := e.dedup.MarkFailed(ctx, claimID, token, "mapping_lookup_failed"); markErr != nil {
			return e.finishError(ctx, "mark failed", markErr)
		}
		e.logger.Warn("bridge: chatwoot mapping lookup failed", "connection_id", ev.Connection.ID, "error", mapErr)
		return OutcomeFailed, mapErr
	}
	if mapping == nil || mapping.IdentityKey == "" {
		return e.failChatwootClaim(ctx, ev.Connection.ID, claimID, token, "no_mapping", "no contacts_map row")
	}

	// E11: text/caption forwarded verbatim. An attachment-only message (empty
	// content) is never partially forwarded (spec §4).
	if strings.TrimSpace(cw.Content) == "" {
		return e.failChatwootClaim(ctx, ev.Connection.ID, claimID, token, "no_content", "attachment-only message")
	}

	result, sendErr := e.evolution.SendText(ctx, mapping.IdentityKey, cw.Content)
	if sendErr != nil {
		if errors.Is(sendErr, ErrUnconfirmed) {
			if markErr := e.dedup.MarkUnconfirmed(ctx, claimID, token); markErr != nil {
				return e.finishError(ctx, "mark unconfirmed", markErr)
			}
			e.insertLog(ctx, e.chatwootLogEntry(ev.Connection.ID, cw, "unconfirmed", ""))
			return OutcomeQuarantined, nil
		}
		// A pre-flight/known-not-sent error: fail attend, never retry.
		if markErr := e.dedup.MarkFailed(ctx, claimID, token, "send_failed"); markErr != nil {
			return e.finishError(ctx, "mark failed", markErr)
		}
		e.logger.Warn("bridge: chatwoot send failed", "connection_id", ev.Connection.ID, "error", sendErr)
		e.insertLog(ctx, e.chatwootLogEntry(ev.Connection.ID, cw, "failed", ""))
		return OutcomeFailed, nil
	}
	if result == nil || !isCorrelatableMessageID(result.MessageID) {
		// A confirmed send without a correlatable destination id is an unknown
		// outcome: the echo store cannot correlate it, so the Evolution echo
		// would be forwarded again (rule 6). Quarantine: never retry.
		if markErr := e.dedup.MarkUnconfirmed(ctx, claimID, token); markErr != nil {
			return e.finishError(ctx, "mark unconfirmed", markErr)
		}
		e.insertLog(ctx, e.chatwootLogEntry(ev.Connection.ID, cw, "unconfirmed", ""))
		return OutcomeQuarantined, nil
	}
	if err := e.dedup.Complete(ctx, claimID, token, result.MessageID); err != nil {
		return e.finishError(ctx, "complete", err)
	}
	e.insertLog(ctx, e.chatwootLogEntry(ev.Connection.ID, cw, "sent", result.MessageID))
	return OutcomeForwarded, nil
}

// ignoreChatwootClaim closes a claimed Chatwoot event that is intentionally not
// delivered (E10/loop) without touching any entity port.
func (e *Engine) ignoreChatwootClaim(ctx context.Context, connectionID int, claimID int64, token, reason string) (Outcome, error) {
	if err := e.dedup.MarkDuplicate(ctx, claimID, token); err != nil {
		return e.finishError(ctx, "mark duplicate", err)
	}
	e.logger.Debug("bridge: chatwoot event ignored", "connection_id", connectionID, "reason", reason)
	return OutcomeIgnored, nil
}

// failChatwootClaim closes a claimed Chatwoot event fail-closed. No entity is
// created and no message is sent (E12).
func (e *Engine) failChatwootClaim(ctx context.Context, connectionID int, claimID int64, token, errorCode, reason string) (Outcome, error) {
	if err := e.dedup.MarkFailed(ctx, claimID, token, errorCode); err != nil {
		return e.finishError(ctx, "mark failed", err)
	}
	e.logger.Warn("bridge: chatwoot fail-attend", "connection_id", connectionID, "reason", reason)
	return OutcomeFailed, nil
}

// chatwootLogEntry builds one sanitized message_logs row for the Chatwoot →
// WhatsApp direction. The sender name is never persisted (spec §2.2/§1.7).
func (e *Engine) chatwootLogEntry(connectionID int, cw *ChatwootEvent, status, destination string) LogEntry {
	return LogEntry{
		ConnectionID:         connectionID,
		Direction:            LogDirectionChatwootToWhatsapp,
		MessageType:          cw.MessageType,
		Status:               status,
		SourceMessageID:      cw.MessageID,
		DestinationMessageID: destination,
		Preview:              preview(cw.Content),
	}
}

// deliver resolves the identity, sends exactly once, and completes or
// quarantines the given claim. The caller owns the claim.
func (e *Engine) deliver(
	ctx context.Context,
	ev Event,
	parsed *ParsedEvent,
	identity Identity,
	decision Decision,
	claimID int64,
	token string,
) (Outcome, error) {
	contact, err := e.resolver.resolveContact(ctx, ev.Connection, identity)
	if err != nil {
		return e.failClaim(ctx, ev, parsed, identity, decision, claimID, token, "resolve contact", err)
	}
	conversationID, err := e.resolver.resolveConversation(
		ctx, ev.Connection, contact.ID, contact.MappedConvID, decision.AllowNewConversation,
	)
	if err != nil {
		return e.failClaim(ctx, ev, parsed, identity, decision, claimID, token, "resolve conversation", err)
	}

	// Persist the resolved Chatwoot conversation id (F4/E11) before the send:
	// without it, an identity first seen through WhatsApp keeps a NULL
	// contacts_map.chatwootConversationId, so the agent's later reply cannot be
	// reverse-mapped and fails closed with no_mapping. A persistence failure is
	// a real error, never "absence": fail closed, no send, no alternate entity.
	if persistErr := e.resolver.persistConversation(
		ctx, ev.Connection, identity.Key, contact.ID, conversationID,
	); persistErr != nil {
		return e.failClaim(ctx, ev, parsed, identity, decision, claimID, token, "persist mapping", persistErr)
	}

	// Name policy: only a search-resolved contact (whose current name is known),
	// only for an inbound (never external IsFromMe, E2) message, and only with an
	// authoritative non-placeholder name (E6). Mapping reuse skips this: its name
	// is unknown and must never be overwritten blindly.
	if name := identity.DisplayName; name != "" && !parsed.IsFromMe && !contact.FromMap && contact.Name != name {
		if nameErr := e.chatwoot.UpdateContactName(ctx, contact.ID, name); nameErr != nil {
			// Cosmetic, best-effort: it never blocks the message and never
			// authorises an alternate entity.
			e.logger.Warn("bridge: contact name update failed", "contact_id", contact.ID, "error", nameErr)
		}
	}

	result, err := e.chatwoot.SendText(ctx, conversationID, buildContent(parsed, identity), decision.Direction)
	if err != nil || result == nil || !isCorrelatableMessageID(result.MessageID) {
		// One attempt per claim. A confirmed send without a correlatable
		// destination id (empty, whitespace-only or zero-width/format-only) is
		// still an unknown outcome: the echo store cannot correlate it, so the
		// echo would be classified external and forwarded again (rule 6).
		// Quarantine: never retry, never create an alternate entity
		// (ADR-0004 §5, ADR-0009 §4).
		if markErr := e.dedup.MarkUnconfirmed(ctx, claimID, token); markErr != nil {
			return e.finishError(ctx, "mark unconfirmed", markErr)
		}
		e.insertLog(ctx, e.logEntry(ev.Connection.ID, parsed, identity, "unconfirmed", decision.Direction, contact.ID, ""))
		return OutcomeQuarantined, nil
	}

	if err := e.dedup.Complete(ctx, claimID, token, result.MessageID); err != nil {
		// A takeover means the audit write is lost, not the send: the external
		// attempt already happened. Logged, never swallowed (ADR-0004 §4).
		return e.finishError(ctx, "complete", err)
	}
	e.insertLog(ctx, e.logEntry(ev.Connection.ID, parsed, identity, "sent", decision.Direction, contact.ID, result.MessageID))
	return OutcomeForwarded, nil
}

// failClaim closes a claimed event that will not be delivered. An unconfirmed
// mutation is quarantined; anything else is a definite fail-attend.
func (e *Engine) failClaim(
	ctx context.Context,
	ev Event,
	parsed *ParsedEvent,
	identity Identity,
	decision Decision,
	claimID int64,
	token, op string,
	cause error,
) (Outcome, error) {
	if errors.Is(cause, ErrUnconfirmed) {
		if err := e.dedup.MarkUnconfirmed(ctx, claimID, token); err != nil {
			return e.finishError(ctx, "mark unconfirmed", err)
		}
		e.insertLog(ctx, e.logEntry(ev.Connection.ID, parsed, identity, "unconfirmed", decision.Direction, 0, ""))
		return OutcomeQuarantined, nil
	}
	if err := e.dedup.MarkFailed(ctx, claimID, token, "resolve_failed"); err != nil {
		return e.finishError(ctx, "mark failed", err)
	}
	e.logger.Warn("bridge: fail-attend", "connection_id", ev.Connection.ID, "op", op, "error", cause)
	e.insertLog(ctx, e.logEntry(ev.Connection.ID, parsed, identity, "failed", decision.Direction, 0, ""))
	return OutcomeFailed, nil
}

// finishError classifies a claim-transition failure. A lost ownership (takeover)
// is a warning, not an error, but it is surfaced to the caller.
func (e *Engine) finishError(_ context.Context, op string, err error) (Outcome, error) {
	if errors.Is(err, ErrNotOwner) {
		e.logger.Warn("bridge: late owner lost claim transition", "op", op, "error", err)
	} else {
		e.logger.Error("bridge: claim transition failed", "op", op, "error", err)
	}
	return OutcomeFailed, err
}

// insertLog writes one sanitized message_logs row. A log write failure is
// reported but never changes the delivery outcome (the effect already happened).
func (e *Engine) insertLog(ctx context.Context, entry LogEntry) {
	if err := e.logs.Insert(ctx, entry); err != nil {
		e.logger.Warn("bridge: message log insert failed", "error", err)
	}
}

func (e *Engine) eventKey(connectionID int, parsed *ParsedEvent) Key {
	return Key{
		ConnectionID:    connectionID,
		SourceSystem:    SourceEvolution,
		EventType:       parsed.EventName,
		SourceMessageID: parsed.MessageID,
	}
}

func (e *Engine) logEntry(
	connectionID int,
	parsed *ParsedEvent,
	identity Identity,
	status string,
	direction MessageDirection,
	contactID int,
	destinationMessageID string,
) LogEntry {
	return LogEntry{
		ConnectionID:         connectionID,
		Direction:            string(direction),
		MessageType:          parsed.MessageType,
		Status:               status,
		SourceMessageID:      parsed.MessageID,
		DestinationMessageID: destinationMessageID,
		ContactID:            contactID,
		ContactName:          identity.DisplayName,
		Preview:              preview(parsed.Content()),
	}
}

// buildContent prefixes a group message with the sender label
// (behaviours-to-preserve §C, ROUTING_MATRIX E4).
func buildContent(parsed *ParsedEvent, identity Identity) string {
	if identity.Kind != KindGroup {
		return parsed.Content()
	}
	return fmt.Sprintf("*%s:*\n%s", senderLabel(parsed), parsed.Content())
}

// senderLabel is the in-group sender name, falling back to the sender phone.
func senderLabel(parsed *ParsedEvent) string {
	if parsed.PushName != "" {
		return parsed.PushName
	}
	if digits := digitsOnly(parsed.SenderJID); digits != "" {
		return digits
	}
	return "participante"
}

// preview truncates a stored preview on a rune boundary.
func preview(content string) string {
	content = strings.TrimSpace(content)
	if runes := []rune(content); len(runes) > previewLimit {
		return string(runes[:previewLimit])
	}
	return content
}
