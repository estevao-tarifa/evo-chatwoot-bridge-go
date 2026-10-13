# ADR-0009 — Exact Chatwoot identity and observed webhook state

- Status: Accepted (2026-10-11; design-reviewed and accepted)
- Scope: Client and future API-inbox bridge policy; **no account-webhook migration**.
- Design: Chatwoot client v4.12.1.

## Evidence and reason to change

The pinned official Chatwoot v4.12.1 source commit
`899fce1c9286b793d165c8ad6c0200ab676f185b` shows account-scoped
substring contact search; search `count` is page length, `has_more` is its
continuation signal and supplied `current_page` may serialize as a string.
Index differs: resolved contacts, total count, no `has_more`. API-inbox JSON
returns `webhook_url`; legacy mapped `callback_webhook_url` (usually null).
Account webhook HMAC and unsigned API-inbox delivery are different modes.
Observed production image tag is not proof of source/build equivalence;
deployment remains `[needs live validation]`.

The legacy code selected the first digit-substring match, treated search errors
as misses, stopped after the first page, could create placeholder group names,
and reported desired webhook URLs as configured state. These are unsafe for
identity integrity/security. Changing those proven behaviors is **approved**
here; it is not unintentional parity. Keep account/inbox/connection boundaries.

## Decision

1. Search all pages needed until `has_more=false`, accepting positive integer or
   decimal string current_page; invalid/absent/contradictory metadata, failed
   later page or page ceiling → inconclusive **error**, never permission to
   create. A page whose `count` contradicts `len(payload)` is malformed and also
   yields an inconclusive error. Administrative index pagination remains separate. Return an explicit
   identity conflict on multiple distinct exact contacts; never choose first.
   Matched contact IDs must be positive. Exact group identifier and exact
   canonical 1:1 identity win only after mapping verification. No substring
   match, no overwrite/creation from placeholder names such as `"Grupo"` or
   raw phone/JID. **A new group is created only when an authoritative,
   non-placeholder name (`groupData.Name`) is available**; absent it, do not
   create (never `"Grupo"`/raw phone/JID as identity or display name). Missing
   authoritative group name also means no blind name update on an existing
   contact. Do not manufacture a display name in the client.
2. Separate desired webhook URL from observed `webhook_url` in inbox readbacks.
   Creation/update response does not verify persistent state. A successful GET
   of the existing inbox endpoint is required before claiming configured URL
   matches; missing/mismatch becomes unverified/operator-visible. Treat tokenized
   URLs as secrets in logs/errors and unauthorized responses.
3. Existing API-inbox webhook delivery remains authenticated by the path token.
   Never assume an inbox HMAC secret; optional account HMAC helper is **not** an
   ingress authentication mode. Switching to account webhooks would need its
   own ADR and tests for account-wide scope, inbox filtering, replay and double
   delivery. A future configured signed mode fails closed, never falls back.
4. HTTP status/transport retry classification does **not** authorize retrying
   a mutating POST/PUT/PATCH after unknown outcome. Safe reads may retry within
   bounds; creation/message sends need reconciliation or quarantine, never
   alternate entity creation on error. A caller-provided flag `Retryable()`
   cannot imply safe mutation replay. Actual end-to-end retry and no-loss
   behavior remains an engine gate.

## Gate and limits

Synthetic `httptest` acceptance fixtures from the signed-off spec must assert
string pagination, malformed/ceiling/ambiguous cases with zero mutation calls,
readback distinctions, and one attempt after unknown write outcomes. Run Go
tests/race; independent QA and review must approve immutable correction commits.
No live contacts, chat or webhooks are touched by this ADR. Client-level tests
alone cannot close the message-engine or migration gate.
