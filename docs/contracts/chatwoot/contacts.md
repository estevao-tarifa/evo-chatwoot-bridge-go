# Contract — Chatwoot Contacts (bridge operations)

Scope: every contact operation evo-chatwoot-bridge performs against Chatwoot, its
exact matching logic, failure modes and identity assumptions.

Sources of truth (in priority order):

- Legacy repo `evo-chatwoot-bridge/` @ `daeb04b` (v5.4.4) — code + its 167 passing tests.
- Chatwoot source: tag `v3.0.0` (the legacy README targets Chatwoot 3.x) and
  `develop` (state on 2026-10-10), paths cited inline.
- Official docs: <https://developers.chatwoot.com/api-reference/contacts/*>.

Marker legend: `[legacy]` proven by legacy code/tests · `[cw-src]` proven by
Chatwoot source · `[cw-docs]` official docs · `[needs live validation]` cannot be
proven without a running instance (Phase 7) · `[unverified]` + open question.

Synthetic data only. No tokens, no real phone numbers.

---

## 1. Transport

`ChatwootClient` (base `chatwootClient.ts:47-58`):

- Base URL `{apiUrl without trailing /}/api/v1`, header `api_access_token: <token>`,
  `Content-Type: application/json`, axios timeout **30 s** `[legacy]`.
- Every method is on this one client; there is no per-inbox or per-connection
  scoping inside Chatwoot calls beyond what is listed below.

## 2. Operations overview

| # | Operation | Endpoint | Caller |
|---|---|---|---|
| 2.1 | `searchContact(phone, isGroup)` | `GET /accounts/{acc}/contacts/search?q=…&include_contacts=true` | `findOrCreateContact` (`chatwootClient.ts:106`); 1:1 and group flows |
| 2.2 | `createContact(phone, name, avatar?, isGroup)` | `POST /accounts/{acc}/contacts` | `findOrCreateContact` (`chatwootClient.ts:108`) |
| 2.3 | update name | `PUT /accounts/{acc}/contacts/{id}` body `{name}` | `findOrCreateContact` (`chatwootClient.ts:112`), only when name differs |
| 2.4 | batch sync from Evolution contacts | same as 2.1+2.2 | `routers/contacts.ts:96` (`contacts.sync`), groups/broadcast/newsletter filtered out (`routers/contacts.ts:38-40,84`) |

There is **no** delete, no label, no merge, no custom-attribute operation in the bridge.

## 3. `searchContact` — exact matching logic

Request `[legacy]` `chatwootClient.ts:63-79`:

```http
GET /api/v1/accounts/{accountId}/contacts/search?q=15550000001&include_contacts=true
api_access_token: <token>
```

- `q` is the raw `contactKey`:
  - 1:1 → phone digits from the chat JID, e.g. `15550000001` (`bridgeProcessor.ts:546`);
  - group → full group JID, e.g. `120363400000000000@g.us` (`bridgeProcessor.ts:544-545`).
- `include_contacts: true` is **not** a documented or controller-consumed parameter —
  current controller reads `include_contact_inboxes` (default `true`); unknown query
  params are ignored by Rails. Treat `include_contacts` as a harmless legacy no-op
  `[cw-src]` (`contacts_controller.rb`, `set_include_contact_inboxes`). `[needs live validation]`
  only for exotic forks.

Response handling `[legacy]`:

```ts
const contacts = data?.payload || [];        // chatwootClient.ts:68
if (isGroup) return contacts.find(c => c.identifier === phone) || null;   // :71
return contacts.find(c =>
  c.phone_number?.replace(/\D/g, "").includes(phone.replace(/\D/g, ""))   // :73-75
) || null;
```

- Response shape `{ meta: {count, current_page, has_more}, payload: [contact] }`
  `[cw-docs]` + `[cw-src]` (`search.json.jbuilder`); contact items contain
  `id, name, phone_number, identifier, email, …` `[cw-docs]`.

Pinned v4.12.1 clarification (commit `899fce1c9286b793d165c8ad6c0200ab676f185b`):
search queries account contacts, **not** `resolved_contacts`; only index uses that
scope. Search `count` is this page's length (0–15), `has_more` boolean is authoritative
for pagination, and supplied `current_page` is not explicitly coerced (string on
the wire is possible). Index `count` is total and index lacks `has_more`.
See the Chatwoot 4.12.1 source validation for pinned code/test references
(source inspection only, not live behavioral tests). Missing/malformed pagination
or exhaustion of a client ceiling must be an error, not permission to create.

### 3.1 Group branch — exact identity

- `c.identifier === phone` — **exact, case-sensitive, full-string equality**
  against the JID `[legacy]` `chatwootClient.ts:71`.
- Consequence: if the existing contact's `identifier` differs in **any** character
  (different case, missing `@g.us`, JID stored elsewhere, identifier `null`) the
  bridge MISSES even though Chatwoot's search returned the contact by name/email
  ILIKE. Canonical first step of H1/H3 scenarios.

### 3.2 1:1 branch — digit-substring

- Only `phone_number` is inspected. Digits are stripped from both sides, then
  `storedDigits.includes(queryDigits)` — a **substring** test, not equality `[legacy]`
  `chatwootClient.ts:73-75`.
- The first contact in Chatwoot's returned order that satisfies it wins.
  Return order of `contacts/search` is not specified by the docs; the controller
  passes `filtrate(contacts)` with no bridge-supplied `sort`/`page`
  (`contacts_controller.rb`) `[unverified]` — exact tie-break order.
- Edge: if `phone` contains no digits at all, query digits are `""` and
  `includes("") === true` — **any** contact with a non-null `phone_number` matches.
  The upstream guard only rejects `senderPhone.length < 5` / containing `@`
  (`bridgeProcessor.ts:557-560`), so a 5+ char non-numeric JID part would pass.
  `[legacy]` logic, reachability of such payloads `[needs live validation]`.

### 3.3 What makes `searchContact` MISS
(all paths return `null`, indistinguishable to callers)

| # | Condition | Evidence |
|---|---|---|
| M1 | Any HTTP/transport error (401/403/5xx/timeout) → `catch { return null }` | `chatwootClient.ts:76-78` |
| M2 | Body without `payload`/non-array → empty list | `chatwootClient.ts:68` |
| M3 | Hub result pagination: max **15** contacts per page, default page 1; `q` matches `name ILIKE`, `email ILIKE`, `phone_number ILIKE`, `identifier LIKE` (`LIKE` is case-sensitive) | `[cw-docs]` "Page size = 15"; `[cw-src]` `RESULTS_PER_PAGE = 15`, `contacts_controller.rb#search` |
| M4 | Contact is "unresolved": **v3.0.0** search scopes `resolved_contacts`; pinned **v4.12.1** and researched develop search query `Current.account.contacts`, not that scope | v3 source; pinned v4.12.1 controller L19–32 distinguishes index vs search; see version validation report |
| M5 | 1:1: Chatwoot found the contact via `identifier`/`name`/`email`, but `phone_number` is `null` or in a divergent format (no DDI, 9th digit, spaces are impossible in storage but different country-code assumption is possible) → local matcher rejects | `chatwootClient.ts:73-75` |
| M6 | >15 contacts match `q` and the intended one is not on page 1 | M3 + no `page` param in `chatwootClient.ts:65-67` |
| M7 | Group: contact found but `identifier` not exactly equal to the raw JID | `chatwootClient.ts:71` |
| M8 | Multi-contact match: another contact's phone digits contain the query as a substring (longer number) and appears first → wrong contact is **used** (identity mix, not a miss) | `chatwootClient.ts:73-75` |

Chatwoot storage format constraint (why M5 exists): the contact model validates
`phone_number` against `/\A\+[1-9]\d{1,14}\z/` and requires uniqueness per account
`[cw-src]` `app/models/contact.rb` (3.0.0 and develop). On `develop`, a
non-conforming assignment is reverted to the previous value
(`phone_number_format` callback), i.e. the invalid phone may be **silently dropped
to NULL** instead of returning 422 → later searches match via `identifier` only and
M5 triggers `[cw-src]`, `[needs live validation]` on the deployed version.
On `v3.0.0` the invalid value fails validation → HTTP 422 `[cw-src]`.

## 4. `createContact` — payloads

1:1 `[legacy]` `chatwootClient.ts:92-101`:

```json
POST /api/v1/accounts/{acc}/contacts
{
  "name": "<name or digits-only phone>",     // cleanPhone fallback, e.g. "15550000001"
  "phone_number": "+15550000001",            // "+" forced if absent (line 92)
  "avatar_url": "<url or omitted>",          // undefined → key dropped by JSON
  "identifier": "15550000001"                // digits only (line 94)
}
```

Group `[legacy]` `chatwootClient.ts:83-91`:

```json
POST /api/v1/accounts/{acc}/contacts
{
  "name": "<name or literal \"Grupo\">",     // line 86
  "identifier": "<full group JID>",          // line 87
  "avatar_url": "<url or omitted>"
}
```

- No `phone_number` for groups (comment line 84). Group identity is the JID in
  `identifier`; name is a separate attribute (AGENTS §7 rule 4).
- 1:1 identity is duplicated by design: `identifier == digits`, `phone_number == "+"+digits`.
- Response parsing accepts three shapes: `data?.payload?.contact || data?.payload || data`
  (lines 90/101). Chatwoot returns `{payload: {contact: {…}, contact_inbox: {…}}}`
  `[cw-src]` `create.json.jbuilder` → first branch is the verified one; the
   fallbacks silently mask contract drift. Pinned v4.12.1 create ID is
   `payload.contact.id`; contact read ID is `payload.id`, search ID is
   `payload[i].id`. IDs are numeric. Missing/null/zero contact must be a malformed
   response, not a successful creation or a new-identity signal.

### 4.1 Failure modes of `createContact`

| Mode | Cause | Bridge behaviour |
|---|---|---|
| HTTP 422 | Unique violation: `phone_number` or `identifier` already used in the account (`[cw-src]` contact model validations, both 3.0.0 and develop) | exception propagates → `findOrCreateContact` throws → message marked `failed`, `recordFailure` (`bridgeProcessor.ts:719-723`) |
| HTTP 422 | `phone_number` not `+[1-9]\d{1,14}` (v3.0.0) | same |
| Silently no phone | `develop`: invalid phone reverted, record may save with `identifier` only | contact exists but is unreachable by the 1:1 matcher (M5) `[needs live validation]` |
| 401/403 | bad token / token user has no account permission | exception propagates |
| 5xx / timeout (30 s) | Chatwoot unavailable | exception propagates; no retry in legacy |
| Empty/duplicated create | concurrent webhooks (see HYPOTHESES H2) | Identifier/account has a DB UNIQUE index, phone/account only model validation and non-unique DB index on pinned v4.12.1. A phone race is not atomically prevented; model validation alone is not a guarantee. |

## 5. `findOrCreateContact` — decision sequence

`[legacy]` `chatwootClient.ts:104-116`; caller `bridgeProcessor.ts:609-617`:

```text
searchContact(key, isGroup)
  ├─ hit  → if (name && contact.name !== name) PUT {name}   // exact string compare
  │         PUT errors swallowed: catch { /* ignore */ }     // :111-114
  └─ miss → createContact(key, name, avatar, isGroup)
```

- `contactName` passed by the processor `[legacy]` `bridgeProcessor.ts:613-616`:
  - group → `displayName` = `groupName || "Grupo"` (never null);
  - 1:1 `IsFromMe=true` (external automation) → `null` (v5.4.3 fix, commit `5438dfb`):
    `pushName` on own messages is the **sender**, not the contact, so the name
    update is skipped;
  - 1:1 incoming → `pushName || senderPhone`.
- Because the group branch always passes a name, **every** group message whose
  payload lacks `groupData.Name` passes `"Grupo"` and triggers
  `PUT /contacts/{id} {name: "Grupo"}` when the stored name differs — the
  group-name overwrite path (H3; mechanism in `HYPOTHESES.md` §6 and
  `allowed-operations.md` D3).
- Name comparison is exact; no normalisation, no quality check (`"Grupo"`, raw
  JID or phone all pass `name &&`).

### 5.1 Identity assumptions

1. 1:1 contact identity = phone digits (account-wide).
2. Group contact identity = the JID string in `identifier` (account-wide).
3. Search is **account-scoped, not connection-scoped and not inbox-scoped**
   (`chatwootClient.ts:65` has no scope filter) `[legacy]` — two bridge connections
   on the same Chatwoot account share the same contact record (H7 in
   the rewrite assessment). The per-connection `contacts_map`
   (`db.ts:280-289`) does not isolate Chatwoot contacts.
4. A contact's `name` is presentation data, not identity (AGENTS §7 rules 3-4).
5. The processor treats "contact exists" as `contact?.id` present; otherwise
   throws `Failed to find/create Chatwoot contact` (`bridgeProcessor.ts:617`).

## 6. Legacy regression evidence (tests that pin the current contract)

| Test | What it pins |
|---|---|
| `server/v5.3-fixes.test.ts:327-399` | group display name = `groupData.Name`, fallback literal `"Grupo"`, 1:1 fallback to phone |
| `server/v5.3-fixes.test.ts:459-508` | mention resolution only affects text, not identity |
| legacy commit `5438dfb` (v5.4.3) | 1:1 `IsFromMe` must not update contact name; name fallback = digits-only phone |
| `server/bugfixes.test.ts:236-248` | accepted Evolution event names |

## 7. Open points `[needs live validation]`

1. Exact HTTP status/body of Chatwoot when `createContact` hits an existing
   `phone_number`/`identifier` in the operator's Chatwoot version.
2. Whether the operator's version silently nulls invalid `phone_number` (develop
   behaviour) or rejects with 422 (v3.0.0 behaviour).
3. Real-world payload variants that produce `@lid`/non-numeric `Chat` JIDs for 1:1
   (would flow into `extractPhone` and become a fake "phone").
4. Tie-break order of `contacts/search` with multiple matches (M8).
