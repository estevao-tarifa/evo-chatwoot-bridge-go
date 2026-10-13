# Testing strategy

Applies to the Go backend and the React frontend. Rules: tests assert the
**specification** (`docs/bridge/ROUTING_MATRIX.md`, `docs/contracts/*`,
ADRs), never the implementation's assumptions. No test is weakened to make a
build pass. Synthetic data only — never real phone numbers, group names or
credentials.

## Layers

| Layer | Scope | How |
|---|---|---|
| Unit | parsers, routing decisions, dedup key, DSN/config | stdlib `testing`, table-driven |
| Contract | EVO/Chatwoot clients against `httptest` servers | assert exact request shapes + failure modes (401/403/404/422/5xx/timeout/malformed) |
| Storage | migrator, claim atomicity, convergence | real MySQL via `TEST_MYSQL_DSN` (gated); CI runs it against a `mysql:8` service |
| Engine | webhook → route → upstream, end-to-end | `httptest` fake EVO + Chatwoot; assert **counts**, not just status |
| Concurrency | double-claim, two instances, restart recovery | `-race`, parallel goroutines, ≥8 concurrent claims |
| Frontend | REST client + components | Vitest; contract tests against the documented API |

## Mandatory regression matrix (mission §7)

| Scenario group | Test intent | Assert |
|---|---|---|
| Inbound WhatsApp message (E1) | contact+conversation reuse, message forwarded `incoming` | contacts created, conversations created, messages sent |
| External automation `IsFromMe` (E2) | reuse identity; **no name update**; forward `outgoing` | contacts created = 0 when identity exists |
| Bridge-sent to EVO / echo (E3/E5) | discarded, no forward | messages sent = 0 |
| Bridge-created in Chatwoot | its `message_created` ignored | messages sent = 0 |
| Repeated webhook (same id) | persistent dedup | second event = `duplicate`; entities unchanged |
| Different messages, identical content | not deduped by content | both processed |
| Late/out-of-order event | handled without duplicate entity | counts stable |
| Retry after timeout | recoverable only; unknown-outcome send **not** re-sent | attempts, `unconfirmed_send` |
| Restart between send and result | stale `processing` recovered, not duplicated | 1 row, 1 send |
| Same event on two instances | exactly one processes | 1 claim winner |
| Contact exists / not exists / resolved conv / multiple convs / two inboxes | reuse rules Q1–Q8 | conversations created only per policy |
| Group known / unknown / renamed / missing JID-LID / sender≠group name | identity by JID; name rules E4/E6 | name updated only with a valid name |
| Stale/inconsistent mapping | re-verified, never trusted blindly | G0 |
| Text / image / video / audio / document | forwarded with correct type | destination payload |
| Agent message / private note | forwarded / ignored (E10/E11) | messages sent |
| Reaction/protocol/status/broadcast/newsletter | ignored (E7/E8) | no entities |
| LID mention | resolved to phone when available | text content |
| Call event | reject configured text (E9) | message sent to caller |
| API unavailable / invalid token / HTTP error / oversized payload | typed errors, no crash, correct retry class | error category + no entity |

## Evidence protocol

- Every claim in a report carries the exact command and raw output.
- Defects classified CRITICAL/HIGH/MEDIUM/LOW with a minimal reproduction.
- QA and review run in sessions independent from the implementer.

## Chatwoot client boundary regressions

`backend/internal/chatwoot/*_test.go` checks every JSON-returning operation
(including multipart sends) against empty/whitespace/null/empty-object responses.
Malformed responses are errors, never conclusive misses; a bodyless contact-name
update remains valid. Contact creation requires `payload.contact` and a positive
id. Search requires explicit `meta.has_more` and errors on pagination exhaustion.
Foreign redirects are blocked even with an injected HTTP client; all redirects
are disabled without changing that supplied client. Error strings discard
untrusted response/cause text rather than attempting free-text regex redaction.
Attachment filenames strip controls and content types reject CR/LF before any
request. See the package tests for the exact assertions and for the mutation
checks that proved each one can fail.

## Environment

- Unit/contract tests: no external deps.
- Integration: `TEST_MYSQL_DSN` (any local MySQL 8/9; a `mysql:8` service in CI). CI **fails if the integration tests skip**.
- Live Evolution GO / Chatwoot are not part of the test environment: behaviours that depend on them are marked `[needs live validation]` and must be re-checked against a live instance before you rely on them.
