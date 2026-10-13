# Hypotheses — Evolution-side root causes (H4, H6, H3-evolution-side)

Evidence rules: legacy commit `daeb04b` (v5.4.4) is canonical; Evolution GO
`0.6.1` (`89eaae337e61`) / `main` (`9337afc47e10`) source is cited for producer
behaviour; official docs for cross-checks. No live environment exists, so every
item that can only be settled against a real instance carries
`[needs live validation]` (`AGENTS.md` §9) and an exact Phase 7 test.

## H4 — Fragile sent-ID extraction + in-memory dedup caches

**Statement.** Loop prevention depends on parsing an ID out of the send response
and holding it in process memory. If the response shape differs (or the process
restarts / TTL expires), the WhatsApp echo is re-classified as an external
message and can create/duplicate Chatwoot entities.

**Evidence.**

| Fact | Evidence |
|---|---|
| Extraction reads only `data.Info.ID`, `data.Info.id`, `data.key.id`, `data.id`; any other shape → `""` | `server/services/bridgeProcessor.ts:95-103` |
| Cache is a `Map` in process memory, TTL 5 min, cleanup 60 s | `:34-36,59,78-91` |
| Empty IDs are silently skipped; send still logged `sent` with `msgId: unknown` | `:837-840,860-861,871-873` |
| Echo with `IsFromMe=true` + cache miss is forwarded to Chatwoot as **external outgoing** | `:515-524,658-663` |
| Chatwoot-side cache is also in-memory; only populated when `result.id` is parsed | `:59-75,704-707`; CW webhook check `:768-772` |
| `contacts_map` has no UNIQUE on `(connectionId, whatsappPhone)`; upsert is read-then-write | `drizzle/0001_burly_gressill.sql:19-29`; `drizzle/schema.ts:105-118`; `server/db.ts:291-310` |
| Contact creation is search-then-create (race window) | `server/services/chatwootClient.ts:105-116` |
| Producer happy path (0.6.1) **does** return `data.Info.ID` | `0.6.1/pkg/sendMessage/handler/send_handler.go:73,212`; `send_service.go:2200-2211` |

**Failure mode.** Send succeeds → response lacks a recognized ID (proxy rewrite,
different build, 200-with-error body) or cache lost (restart, echo arriving
later than the 5 min TTL — offline sync / delayed delivery) → `SendMessage`/`Message` echo processed as
external → duplicate message in Chatwoot, and if the contact mapping is missing
at that moment, a new contact/conversation. If the Chatwoot `result.id` is also
unparsed, the CW `message_created` webhook is not skipped and a full
WhatsApp→Chatwoot→WhatsApp loop becomes possible.

**Phase 7 live test.**

1. Baseline: send one text via the bridge UI (Chatwoot→WhatsApp) to a synthetic
   test contact; capture the raw `/send/text` response body and the `SendMessage`
   webhook body. Pass condition: `data.Info.ID` == webhook `data.Info.ID`.
2. Restart the bridge process within the echo window, then trigger an echo of a
   bridge-sent message (send another text immediately after restart and watch).
   Record whether Chatwoot receives a duplicate `outgoing` message.
3. TTL expiry: send, wait > 5 min, then replay the echo (webhook retry is only
   5 × 30 s — use offline-sync or a captured-payload replay), observe duplicates.
4. With two bridge replicas pointed at the same connection (only in an isolated
   test namespace), confirm caches do not coordinate (expected duplication).

**Status.** Cache fragility and race windows are proven statically. The actual
pilot response shape is `[needs live validation]` (ARCHITECTURE.md Q1); the Go
fix is ADR-0004 (persistent dedup + explicit `unconfirmed` state), not a bigger
cache.

## H6 — `IsFromMe` typing / coercion

**Statement.** `parseInfo` coerces with `Boolean(...)`; a stringified boolean
(`"false"`, `"0"`) becomes `true` (JS truthiness). Incoming messages can be
misclassified as own messages, flipping direction in Chatwoot and skipping the
correct contact-name handling.

**Evidence.**

| Fact | Evidence |
|---|---|
| `isFromMe: Boolean(goInfo.IsFromMe ?? goInfo.isFromMe ?? stdKey.fromMe ?? false)` | `server/services/bridgeProcessor.ts:209` |
| Tests only cover real booleans | `server/v4.2-rewrite.test.ts:85,147-153`; `server/v5.3-fixes.test.ts:577` |
| Evolution GO emits real JSON booleans (`IsFromMe bool` in `types.MessageInfo`, marshalled with `encoding/json`) | whatsmeow `types/message.go:27-31`; `0.6.1/pkg/whatsmeow/service/whatsmeow.go:1843` |
| Misclassification effects: message posted as `outgoing`, contact-name update suppressed for 1:1, sent-cache branch consulted first | `bridgeProcessor.ts:515-524,610-615,658-663` |

**Failure mode.** Any intermediary that stringifies payloads (n8n forwarding a
captured webhook back, manual replays, test harnesses) sends `"IsFromMe":"false"`
→ bridge treats a customer message as ours → appears as an outgoing message in
Chatwoot and `contactName` is `null` (existing contact names preserved by luck,
new contacts fall back to phone digits). No resend loop by itself, but direction
and audit data are wrong.

**Phase 7 live test.**

1. Confirm the pilot's Evolution GO delivers `IsFromMe` as JSON `true/false`
   (raw payload capture).
2. Replay the same payload with `"IsFromMe": "false"` (string) against a staging
   bridge; verify the legacy behaviour flips to `outgoing` (regression fixture).
3. Go implementation check (not live): typed `bool` field; decide and test the
   behaviour for string input — reject with a parse error metric, or tolerant
   decode — but never silent coercion.

**Status.** Producer-side type is proven (`bool`); the coercion risk is
`[needs live validation]` only for non-Evolution producers. The Go rewrite must
not reproduce `Boolean(...)` semantics.

## H3-evolution-side — missing `groupData.Name` (camelCase / echo / lookup failure)

**Statement.** Group display names are read from a single optional field,
`data.groupData.Name`. Payloads without it (standard-API camelCase, Evolution GO
`SendMessage` echoes, `Message` events where `GetGroupInfo` failed) produce the
fallback `"Grupo"`, which is then written to the Chatwoot contact and to
`contacts_map`, overwriting a correct group name (`AGENTS.md` §7 rule 4).

**Evidence.**

| Fact | Evidence |
|---|---|
| `groupName` only from `data.groupData.Name \|\| data.groupData.name` | `server/services/bridgeProcessor.ts:212` |
| Fallback `displayName = groupName \|\| "Grupo"` | `:563-565` |
| Contact create default `name \|\| "Grupo"`; update whenever name differs | `server/services/chatwootClient.ts:82-102,105-116` |
| Map upsert overwrites `whatsappName = displayName` unconditionally | `bridgeProcessor.ts:624-630` |
| camelCase payloads carry no `groupData` | `server/v4.2-rewrite.test.ts:331-354`; official Evolution API docs (no group metadata in message events) |
| Evolution GO attaches `groupData` **only when `GetGroupInfo` succeeds** | `0.6.1/pkg/whatsmeow/service/whatsmeow.go:1470-1475`; `main` same at `:1596-1602` |
| `SendMessage` echo payload has only `Info`/`Message`/`MessageContextInfo` | `0.6.1/pkg/sendMessage/service/send_service.go:2224-2244` |
| Legacy tests already assert the `"Grupo"` fallback | `server/v5.3-fixes.test.ts:361-372` |

**Failure mode.** First event for a group lacks `groupData.Name` → contact
created as `"Grupo"`; later events with the real name overwrite it (churn); any
echo/camelCase event without `groupData` overwrites a correct name back to
`"Grupo"`. Messages from external senders (n8n / WhatsApp Web) are the exact
triggers described in `AGENTS.md` §7. Identity itself is not broken (contact key
stays the group JID), which matches “overwriting the correct group name” in the
bug report.

**Phase 7 live test.**

1. Pick a synthetic test group mapped with its real name; snapshot Chatwoot
   contact name + `contacts_map.whatsappName`.
2. Send from WhatsApp Web (external, `SendMessage` echo, no `groupData`); verify
   legacy renames the contact to `"Grupo"` (expected failure reproduction).
3. Send a standard-API camelCase fixture (no `groupData`) to the bridge webhook;
   same verification.
4. Trigger a `Message` event for the same group **with** `groupData.Name` and
   verify the name is restored; then confirm the Go build does **not** update
   names on payloads without a valid name (pass condition).

**Status.** Producer behaviour is proven from source; the deployed version's
`GetGroupInfo` failure rate is `[needs live validation]` but not required for the
fix — the Go parser must treat absent `groupData.Name` as “unknown name” and
skip the update entirely.

## Bonus findings (evidence-backed, relevant to Phase 7)

| # | Finding | Evidence | Impact |
|---|---|---|---|
| B1 | Call rejection matches `event` values `call`/`CALL`/`Call`, but Evolution GO 0.6.1+ emits `CallOffer`/`CallTerminate`/… | `bridgeProcessor.ts:502-504`; `0.6.1/.../whatsmeow.go:1676-1716`; official docs | Call rejection likely dead code on the pilot `[needs live validation]` |
| B2 | `setWebhook` sends PascalCase `subscribe` values; 0.6.1/0.7.2 validate exact uppercase enums and discard unknown ones | `evolutionClient.ts:115-158`; `0.6.1/.../instance_service.go:209-230` | Fresh `subscribe` may configure nothing; operator configures via panel (legacy bootstrap assumes this) |
| B3 | Inbound EVO payloads carry `instanceToken` (and standard API `apikey`); legacy sanitizer does not redact them before persisting `rawPayload` | `0.6.1/.../whatsmeow.go:1838-1841`; `send_service.go:2292-2294`; `bridgeProcessor.ts:107-145` | Secret leak into DB/logs; Go sanitizer must strip (`AGENTS.md` §8) |
| B4 | `resolveInstanceToken` logs the first 8 chars of the token | `evolutionClient.ts:67` | Must not be reproduced |
| B5 | 0.6.1 `/send/media` requires an http(s) `url` fetched by the server; legacy passes Chatwoot `data_url`/`url` as-is | `0.6.1/.../send_handler.go:227-237`, `send_service.go:1094`; `bridgeProcessor.ts:848` | Media sends fail if the URL is a `data:` URI or not reachable from the EVO host `[needs live validation]` |

## Verification status summary

| Hypothesis | Static evidence | Live-verifiable now | Phase 7 test defined |
|---|---|---|---|
| H4 | strong (code + schema + tests) | no | yes |
| H6 | producer type proven; coercion path proven | partially | yes |
| H3-evolution-side | strong (code + tests + producer source) | no | yes |
| B1–B5 | strong | no | B1/B2/B5 covered above; B3/B4 are implementation requirements |
