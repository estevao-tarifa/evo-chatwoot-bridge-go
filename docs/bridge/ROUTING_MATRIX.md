# Routing decision matrix (canonical)

Frozen spec for the Go bridge routing engine. It consolidates the Evolution GO
contract (`docs/contracts/evolution/`), the Chatwoot contract
(`docs/contracts/chatwoot/`, event matrix E1–E14 + preconditions P1–P4/Q1–Q8),
the legacy audit and the upstream source research. Conflicts
are tracked in `docs/bridge/OPEN_CONFLICTS.md`.

## 1. Normative guarantee (G0)

> Entity creation is permitted **only after** identity resolution has been
> executed against the current Chatwoot state and returned *no match*, **and**
> conversation resolution returned *no usable candidate*. Search/HTTP/transport
> failures are never interpreted as "not found", and a mapping row takes
> precedence over any search. Therefore an externally-sent message
> (`IsFromMe=true`, not in the dedup store) can never create a contact or a
> conversation when the correct identity already exists.

## 2. Identity keys (stable, scoped by connection)

| Type | Identity key | Chatwoot representation |
|---|---|---|
| 1:1 | digits of the sender phone (from `chatJid`) | `phone_number=+<digits>`, `identifier=<digits>` |
| Group | the group **JID** (`…@g.us`) | `identifier=<jid>` (no `phone_number`) |

A name is **never** an identity. Group name and contact name are separate
attributes (AGENTS §7).

## 3. Loop prevention (primitives)

1. **Deterministic send id** (correlation, not idempotency): Evolution GO
   `/send/*` accepts an `id` field; the bridge sets its own id and records it,
   then matches a later `SendMessage` echo by that id to discard it. The
   provider's echo/replay behavior is **not** contractually guaranteed
   `[needs live validation]`; correlation is best-effort. Fallback: cache the
   returned `data.Info.ID`. Neither form proves the provider deduplicates, so an
   unknown send outcome is still never auto-resent (ADR-0004/0008).
2. **Persistent dedup store** keyed by
   `(connection_id, source_system, event_type, source_message_id)` — atomic
   claim, survives restart, works across replicas (ADR-0004).
3. **Chatwoot-originated outgoing** created by the bridge is remembered by
   Chatwoot message id so its `message_created` webhook is ignored.
4. **Unknown send outcome is never auto-resent** (`unconfirmed_send`) — avoids
   double sends (ADR-0005).

## 4. Decision matrix

`R` = reuse, `C` = create (only if preconditions hold), `I` = ignore,
`F` = fail/attend (log, no entity). Full event definitions: Chatwoot contract
`contracts/chatwoot/allowed-operations.md` §2.

| Event | Identity resolved | Conversation expected | Allowed action | Reason |
|---|---|---|---|---|
| E1 EVO `Message` 1:1, `IsFromMe=false` | phone → R mapping/search, C only if absent | open conv in `inbox_id`, C only if none usable | forward as `incoming` | inbound customer message |
| E2 EVO `Message`/`SendMessage` 1:1, `IsFromMe=true`, not dedup | same as E1 | same as E1 | forward as `outgoing`; **name update forbidden** | external automation (n8n/WhatsApp Web) — AGENTS §6.6 |
| E3 EVO 1:1 `IsFromMe=true`, in dedup | — | — | I | bridge echo |
| E4 EVO group, any `IsFromMe` | group JID (stable) | open conv for group contact | forward with `*<sender>:*`; direction by `IsFromMe` | groups keyed by JID |
| E5 EVO group `IsFromMe=true`, in dedup | — | — | I | echo |
| E6 EVO message, `groupData.Name` empty/absent | — | — | **no name update** (keep current) | prevents `"Grupo"`/phone overwrite |
| E7 EVO `status@broadcast`/`@broadcast`/`@newsletter` | — | — | I | stories/newsletters |
| E8 EVO protocol/reaction/sticker | — | — | I | unsupported/ignored |
| E9 EVO call event + `rejectCalls` | caller phone (1:1 only) | — | reply configured text | feature; event name casing `[needs live validation]` |
| E10 CW `message_created` incoming/private/agent_bot or other event | — | — | I | not agent replies |
| E11 CW `message_created`, agent, mapped conv | mapping lookup only (never touch contact) | reuse map | send to WA via `contacts_map.whatsapp_phone` | agent reply |
| E12 CW `message_created`, agent, no mapping | F | F | log error; **never** create from CW webhook | G0 |
| E13 duplicate delivery of any CW event (same id) | — | — | I | persistent dedup |
| E14 malformed identity (empty JID, `<5` chars, `@` in phone slot, no digits) | F | F | log, no entity | input validation |

## 5. Contact resolution (frozen algorithm)

```text
resolveContact(conn, identityKey, kind):
  1. row := mapping[(conn.id, identityKey)]            # mapping wins
     if row valid: return row.chatwoot_contact_id       # R
  2. found := chatwoot.SearchContactExact(identityKey, kind)
       # kind=group: identifier == JID (exact, case-sensitive)
       # kind=1:1  : phone digits == key (exact) OR identifier == key
       # HTTP/transport error => FAIL (F), never "not found"
     if found: upsert mapping; return found.id          # R
  3. CREATE(identityKey)  only if P1..P4 hold            # C
```

| # | Create-contact precondition |
|---|---|
| P1 | connection active **and** `chatwoot_inbox_id` non-null (else fail-attend) |
| P2 | search executed and returned **zero exact** matches (not "errored", not "filtered out") |
| P3 | identity well-formed for its type |
| P4 | For a **new group** creation: an authoritative, non-placeholder name is available (`groupData.Name` non-empty). If absent, do **not** create; never use `"Grupo"`, a raw phone or a JID as the display name, and never as identity. |

## 6. Conversation resolution

```text
resolveConversation(conn, contactId):
  1. candidates := chatwoot.ListConversations(contactId)   # error => F
  2. usable := [c for c in candidates
                if c.inbox_id == conn.chatwoot_inbox_id and c.status != "resolved"]
  3. if usable: return pick_deterministic(usable)   # most recent last_activity_at, tie-break id
  4. CREATE only if Q4..Q8 hold
```

| # | Reuse precondition | Create precondition |
|---|---|---|
| Q1 | contact id known | — |
| Q2 | conv matches contact + inbox + not resolved | — |
| Q3 | deterministic selection (no reliance on server pagination order) | — |
| Q4 | — | search returned no usable candidate **and** was not an error; stored map id re-verified |
| Q5 | — | `chatwoot_inbox_id` non-null |
| Q6 | — | contact exists |
| Q7 | — | routing policy allows a new conversation (E1/E2/E4 yes; E3/E5/E7/E8/E10/E12/E13 no) |
| Q8 | — | creation is race-safe (per-identity lock; loser re-runs Q2) |

## 7. Webhook configuration (bootstrap)

- Evolution GO `subscribe[]` uses **uppercase, case-sensitive** constants
  (`MESSAGE`, `SEND_MESSAGE`, …); the legacy PascalCase list is invalid upstream
  `[needs live validation]`. The list must be **configurable**; failures degrade
  gracefully.
- Evolution GO webhooks are **unauthenticated upstream** → the bridge must
  authenticate ingress itself (per-connection path token; see ADR-0006).
- Version correction (2026-10-11, pinned official v4.12.1): API-inbox webhook
  delivery is **unsigned**; use the protected per-connection URL token. Account
  webhook delivery can sign timestamp/raw-body HMAC with its account webhook
  secret. These are different routing modes, not an optional inbox-secret flag.
  Any mode switch needs architecture approval and deployment verification;
  configured signed mode fails closed, never falls back when a signature is
  missing. Newer channel-signing source must not be assumed for v4.12.1.
  Evidence: the Chatwoot 4.12.1 source validation (source inspection,
  not a passing live integration test). E-row policies remain unchanged.

## 8. Test obligations

Every E-row above is a regression test (see the mission §7 matrix). Assertions
must include **counts** (contacts created, conversations created, messages sent,
mappings changed, logs written), not only HTTP status. Phase 4 gate requires the
auditor to approve the matrix as implemented.
