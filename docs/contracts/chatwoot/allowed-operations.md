# Decision Matrix Input — Reuse vs Create (contacts & conversations)

Purpose: freeze the input matrix for the Go routing policy. For every inbound
event: when the bridge may **reuse** an existing Chatwoot contact/conversation and
when it may **create** one, with exact preconditions.

Normative basis: AGENTS.md §7 (identity integrity), mission rules. Legacy
evidence: `evo-chatwoot-bridge/` @ `daeb04b` (v5.4.4). Chatwoot behaviour evidence:
`contacts.md`, `conversations.md`, `messages.md`, `webhooks.md` (this folder).

Legend: **R** = reuse allowed, **C** = create allowed, **I** = ignore (no entity
work), **F** = fail-attend (log/alert, no entity created). `[legacy]` notes the
current implementation; where legacy and the required guarantee differ, the
difference is marked **⚠ divergence** and needs an ADR before changing behaviour
(AGENTS §2).

---

## 1. Terms

- **Connection** `(connectionId, accountId, inboxId)` — the scope of every mapping.
- **Identity key** — the only admissible contact identity:
  - 1:1 → phone digits from the chat JID (e.g. `15550000001`);
  - group → the full group JID (e.g. `120363400000000000@g.us`).
- **Mapped** — a `contacts_map` row exists for `(connectionId, identityKey)`
  with `chatwootContactId` (and, after a message, `chatwootConversationId`).
- **Usable conversation** — conversation with `contact_id = mapped contact`,
  `inbox_id = connection.inboxId`, `status != "resolved"` (`conversations.md` §1.1).
- **Own/echo message** — Evolution event with `IsFromMe=true` whose message id is
  in the persistent dedup store (ADR-004). External own message = `IsFromMe=true`
  and *not* in dedup.
- **Bridge-authored CW message** — `message_created` id in the Chatwoot-side dedup
  store (`webhooks.md` §5).

## 2. Event matrix

| # | Event | Contact: R/C/I/F | Conversation: R/C/I/F | Message action |
|---|---|---|---|---|
| E1 | EVO `Message`, 1:1, `IsFromMe=false` | R (mapped or found by identity) · C only if identity truly absent | R (usable conv) · C only if none usable | send `incoming` |
| E2 | EVO `Message`/`SendMessage`, 1:1, `IsFromMe=true`, **not** dedup → external automation (n8n/WhatsApp Web) | R (same resolution as E1) · C only if absent · **name update forbidden** (pushName is ours, not the contact's) | R (same as E1) · C only if none usable | send `outgoing` |
| E3 | EVO event, 1:1, `IsFromMe=true`, **in** dedup | I | I | none (echo discarded) |
| E4 | EVO `Message`, group, any `IsFromMe` | R by group JID identifier · C only if JID absent | R · C only if none usable | send with `*<sender>:*` prefix; direction by `IsFromMe` |
| E5 | EVO group, `IsFromMe=true`, in dedup | I | I | none |
| E6 | EVO message with `groupData.Name` empty/absent | — | — | **no name update**; only update name if a valid, non-placeholder name is present and differs |
| E7 | EVO `status@broadcast`, `@broadcast`, `@newsletter` | I | I | none |
| E8 | EVO protocol/reaction/sticker (legacy also skips) | I | I | none |
| E9 | EVO `call` event with `rejectCalls` | I | I | reply with configured text to caller JID (1:1 only) |
| E10 | CW `message_created`, `incoming` / `private` / `agent_bot` sender / non-`message_created` event | I | I | none |
| E11 | CW `message_created`, agent sender, mapped conversation | I (never touched) | R (map lookup only) | send to WhatsApp via `contacts_map.whatsappPhone` |
| E12 | CW `message_created`, agent sender, **no mapping** | F | F | log error; **never** create contact/conversation from a CW webhook |
| E13 | Duplicate delivery of any CW event (same message id, already processed) | I | I | none (persistent dedup, ADR-004) |
| E14 | Any event with unresolved/malformed identity (empty JID, `<5` chars, JID with `@` in the phone slot, no digits) | F | F | log, no entity |

`[legacy]` mapping of the matrix: E1 `bridgeProcessor.ts:616-622`; E2 same path
with `contactName=null` (`:613-615`) and `messageDirection="outgoing"` (`:658-659`);
E3 `:515-519`; E4 `:544-546,609,616`; E7 `:537-540`; E8 `:573-577`; E10
`:759-763`; E11 `:804-809`; E12 `:804-806` (compliant); E14 `:552-560`.

## 3. Exact preconditions

### 3.1 Contact resolution (algorithm to freeze)

```text
resolveContact(conn, identityKey):
  1. row := mapping[(conn.id, identityKey)]                  # source of truth
     if row exists and row.chatwootContactId valid:
         return row.chatwootContactId                          # R
  2. found := chatwoot.SearchContactExact(identityKey)        # see precondition P2
     if found: upsert mapping; return found.id                # R
  3. return CREATE(identityKey)                               # C, P1..P4 must hold
```

| # | Precondition for **C**reate contact | Detail |
|---|---|---|
| P1 | Connection is active and has a non-null `chatwootInboxId` | schema nullable (`drizzle/schema.ts:36`); H5 shows creation fails without it — G0 requires fail-attend instead of a doomed create |
| P2 | Search was executed and returned **zero** contacts whose identity key matches exactly — not "search errored" and not "some result failed a loose client-side filter" | legacy ⚠ divergence: `searchContact` returns `null` on error (M1) and on matcher/identity mismatch (M5/M7) → creation despite existing identity (H1) |
| P3 | Identity is well-formed for its type: 1:1 digits ≥ 5 and `+[1-9]\d{1,14}` after `+`; group JID contains `@g.us` | `[legacy]` partial (`bridgeProcessor.ts:552-560`; Chatwoot format rule in `contacts.md` §3) |
| P4 | No group name/identity placeholder is used as identity | AGENTS §7 rule 3 |

### 3.2 Conversation resolution

| # | Precondition for **R**euse | Detail |
|---|---|---|
| Q1 | Contact id is known (from §3.1) | `[legacy]` `chatwootClient.ts:144-150` |
| Q2 | A conversation exists with `contact_id = contact` **and** `inbox_id = conn.chatwootInboxId` **and** `status != "resolved"` | `chatwootClient.ts:125-126` |
| Q3 | Selection among several candidates is deterministic (e.g. most recent `last_activity_at`/`id`) and never depends on server pagination order | legacy relies on server order + window (C7) |

| # | Precondition for **C**reate conversation | Detail |
|---|---|---|
| Q4 | Q2 evaluated and returned no candidate — including: search/API errors must be `F`, not `C`; missing/expired stored `conversation_id` must be re-verified by Q2 before creating | legacy ⚠ divergence: catch→null treats errors as "none" (`chatwootClient.ts:128-130`); stored map id is not re-verified |
| Q5 | `conn.chatwootInboxId` non-null (same as P1) | H5; Chatwoot rejects/errors on `inbox_id: null` |
| Q6 | Contact exists in Chatwoot (id from §3.1) | `[legacy]` sends `contact_id` |
| Q7 | Routing policy allows a new conversation for this event (E1/E2/E4 yes; E3/E5/E7/E8/E10/E12/E13 no) | AGENTS §7 rule 7 |
| Q8 | Creation is race-safe: a per-identity lock or unique mapping insert decides the winner; loser re-runs Q2 | `[legacy]` no lock; two parallel handlers both create (H2) |

### 3.3 Forbidden creations (never create on these events)

- E3/E5 (bridge echo) — AGENTS §7 rule 6.
- E7/E8 (broadcast/newsletter/status; protocol/reaction/sticker) — no user-visible message.
- E10 (incoming/private/agent_bot CW events) — they are not WhatsApp-bound messages.
- E12 (CW webhook without mapping) — the CW side never introduces new WhatsApp identities.
- E13 (duplicate) — idempotency.
- E14 (malformed identity).
- Any event for a connection without inbox (P1/Q5) or inactive connection
  (`[legacy]` `bridgeProcessor.ts:526-527`).

Preconditions whose exact failure behaviour depends on the live instance (P1/Q5
via H5, Q8 via H2, P2 error semantics via H1) are exercised by the Phase 7 tests
in `HYPOTHESES.md` and marked there `[needs live validation]`.

## 4. The guarantee rule (normative statement)

> **G0.** For every event, entity creation is permitted only after the identity
> resolution of §3.1 has been executed against the current Chatwoot state and
> returned *no match*, **and** the conversation resolution of §3.2 has been
> executed and returned *no usable candidate*. Search/HTTP/transport failures are
> never interpreted as "not found" (`F`), and a mapping row takes precedence over
> any search. Therefore, an externally-sent message (`IsFromMe=true` outside the
> dedup store) can never create a contact or conversation when the correct
> identity already exists.

Consequences to encode in the Go client contract:

1. Distinguish `not_found` from `lookup_failed` in every Chatwoot call
   (`contacts/search`, `contacts/{id}/conversations`); legacy collapses both to
   `null` (`contacts.md` M1, `conversations.md` C1).
2. The persistent mapping (`processed_messages` + mapping table per ADR-004) is
   the first and authoritative lookup; Chatwoot search is the second; creation is
   the last resort.
3. Creation is idempotent under concurrency (Q8): unique constraint decides a
   single winner; the loser reuses the winner's entity.
4. Name updates are never part of identity resolution; group name is updated only
   from a valid `groupData.Name` (E6) — never `"Grupo"`, never a phone/JID
   (AGENTS §7 rule 4).
5. The conversation title is decided by Chatwoot; the bridge never sends JIDs as
   titles `[legacy]`: the bridge sends no title at all (AGENTS §7 rule 5).
6. All of the above is scoped by connection + account + inbox + identity as
   available (AGENTS §7 rule 8); at minimum the connection is never ignored.

## 5. Divergence register (legacy vs G0)

| # | Divergence | Evidence | Owning hypothesis |
|---|---|---|---|
| D1 | Search error / matcher rejection treated as absent → creation | `chatwootClient.ts:68,73-78` | H1 |
| D2 | No race protection on contact/conversation creation or mapping upsert | `bridgeProcessor.ts:616-630`, `db.ts:291-310`, no UNIQUE in `drizzle/0001` | H2 |
| D3 | Group name forced to `"Grupo"` when `groupData.Name` absent | `bridgeProcessor.ts:563-565,613-616`, `chatwootClient.ts:109-114` | H3 |
| D4 | Dedup in memory only | `bridgeProcessor.ts:34-91` | H4 |
| D5 | Null inbox: reuse impossible, creation invalid | `chatwootClient.ts:126,137` | H5 |
| D6 | Search account-wide (cross-connection mixing) | `chatwootClient.ts:65` | H7 |
| D7 | CW webhook not validated against account/inbox | `bridgeProcessor.ts:755-806` | H8 |

Each divergence is proven/refuted in `HYPOTHESES.md` (live tests, Phase 7) before
any code change (AGENTS §5).

## 6. Test hooks required by the matrix (for Phase 1 fixtures)

- E1/E2/E4 with identity present → assert **zero** Chatwoot contact/conversation
  POSTs (only GET/list/search).
- E1 with identity absent → exactly one contact POST and one conversation POST.
- E2 with matching identity → outgoing message, no `PUT /contacts/{id}` name update.
- E4 with `groupData.Name` absent → message delivered, no contact PUT.
- E11 with mapping → EVO send; E12 without mapping → no Chatwoot entity call.
- Q8: N concurrent E1s for a new identity → exactly one contact, exactly one
  conversation (after fix); legacy measurement first (`legacy_bug`).
