# Hypotheses — identity bug (H1, H2, H5, H6-family) with Phase 7 live tests

Companion to the rewrite assessment (H1–H8) and the legacy baseline audit.
This file deepens the four hypotheses assigned to the Chatwoot contract work:
**H1** (search miss → duplicate contact → new conversation), **H2** (creation
race), **H5** (null inbox), **H6-family** (typing/representation issues). H3/H4/H7/H8
are out of scope here (see §6).

Evidence conventions: `[legacy]` = proven by code/tests in `evo-chatwoot-bridge/`
@ `daeb04b` (v5.4.4); `[cw-src]` = proven by Chatwoot source (v3.0.0 + develop);
`[needs live validation]` = only a running instance can confirm.

All Phase 7 tests use **synthetic** identifiers (e.g. `+1555000xxxx`,
`120363400000000xxx@g.us`), throwaway Chatwoot contacts, and capture sanitized raw
evidence (no tokens, no real message content).

---

## H1 — Contact search miss leads to a duplicate contact and a new conversation

**Statement.** For an existing identity, `searchContact` returns `null` (or the
wrong contact), so `findOrCreateContact` creates a new Chatwoot contact and
`findOrCreateConversation` creates a new conversation.

### Evidence `[legacy]`

| Mechanism | Evidence |
|---|---|
| H1a — format divergence: Chatwoot search matches only substrings of stored `phone_number` / exact `identifier`; bridge's 1:1 lookup uses fresh `+digits` | `chatwootClient.ts:65-75`; create stores `phone_number="+"+digits`, `identifier=digits` (`:92-100`) |
| H1b — search hit but client matcher rejects: contact found via `identifier`/`name`, but `phone_number` null/different → local `phone_number?.…includes` fails | `chatwootClient.ts:73-75` vs `contacts.md` §3.3 M5 |
| H1c — group: search hit via name, but `identifier !== phone` exactly | `chatwootClient.ts:71` vs M7 |
| H1d — search/HTTP error → `null` → creation attempted | `chatwootClient.ts:76-78` (M1) |
| H1e — pagination: 15 results/page, no `page` param | `chatwootClient.ts:65-67`; `[cw-src]` `RESULTS_PER_PAGE = 15`, `[cw-docs]` "Page size = 15" |
| Consequence chain: creation on miss | `findOrCreateContact` `chatwootClient.ts:106-108`; conversation on miss `:144-150` |
| Chatwoot uniqueness limits the damage: `phone_number`/`identifier` unique per account → some duplicate creates fail 422 instead of duplicating | `[cw-src]` `app/models/contact.rb` (both versions) |

### Expected failure mode (to confirm, not assume)

- H1a/H1c with an existing contact whose identity fields do **not** collide with
  the new one (different format / identifier absent) → **duplicate contact is
  created** and, because the new contact has no conversations, a **new
  conversation** as well. The original contact freezes with its old history.
- H1b/H1d when the new payload collides with the existing contact's
  `identifier`/`phone_number` → create returns **422**, message is marked `failed`
  (`bridgeProcessor.ts:719-723`), no duplicate. The user-visible symptom is a
  lost/failed message instead of a duplicate — both satisfy "the bridge did not
  reuse the identity".
- Exact behaviour per variant depends on the deployment's Chatwoot version
  (silent phone nulling vs 422) `[needs live validation]`.

### Live tests (Phase 7)

**T-H1-1 (format divergence).**
1. In staging Chatwoot, create contact `C1`: name "Synth One", `phone_number`
   `+15550000001`, **no** identifier (via API, to simulate a non-bridge contact).
2. Confirm `GET /contacts/search?q=15550000001` returns `C1` (sanity).
3. Send an EVO `Message` webhook to the bridge for connection X with
   `Info.Chat=15550000002@s.whatsapp.net`, `IsFromMe=false`, `PushName="Synth Two"`,
   unique message id. (Second identity to make the divergence explicit: `C1` holds
   a *different* format of the same logical number, e.g. `phone_number`
   `1550000002` without `+`/prefix.)
4. Observe Chatwoot: search `q=15500000002` → count contacts; `GET
   /contacts/{contactId}/conversations` for the created contact.
5. **Proves H1a** if a second contact exists whose identifier equals the webhook
   digits, or if create logged 422 (check bridge `message_logs`).

**T-H1-2 (identifier hit + null phone → matcher rejects).**
1. Create `C2` with `identifier=15550000003`, **no phone_number**, no email.
2. Send EVO 1:1 message from `15550000003@s.whatsapp.net`, `IsFromMe=false`.
3. Observe: bridge log/`message_logs` for the webhook; Chatwoot contact count for
   `q=15550000003`.
4. **Proves H1b** if the bridge attempts `POST /contacts` (→ 422 on identifier
   uniqueness) instead of reusing `C2`; the message should be `failed`.

**T-H1-3 (group identifier mismatch).**
1. Create group contact `C3` named "Synthetic Group" with **no** identifier.
2. Send EVO group message with `Info.Chat=120363400000000123@g.us`, group name
   "Synthetic Group", `IsFromMe=false`.
3. Observe: contact count for the JID; new conversation created.
4. **Proves H1c** if a second contact with `identifier=120363400000000123@g.us`
   is created while `C3` remains.

**T-H1-4 (search failure treated as absence).**
1. Using a proxy or firewall (staging only), make
   `GET /contacts/search` fail (e.g. 500) for a short window while
   `POST /contacts` still works.
2. Send an EVO message for an identity that already exists.
3. **Proves H1d** if a duplicate contact is created during the window.

Instrumentation to capture per test: bridge `message_logs` row (status, error,
destinationMessageId), Chatwoot contact/conversation counts before/after, HTTP
trace of bridge→Chatwoot calls. All synthetic.

---

## H2 — Concurrent creation race

**Statement.** Two webhooks for the same new identity are processed concurrently;
each handler's lookup misses and both execute creation, producing duplicate
Chatwoot entities and/or duplicate `contacts_map` rows.

### Evidence `[legacy]`

| Mechanism | Evidence |
|---|---|
| Handlers run concurrently after the 200 response (no queue/serialization) | `webhooks.ts:22-30,46-56` |
| Mapping upsert is read-then-write (`findContactMap` then insert/update) — no transaction, no unique key | `db.ts:280-310`; table has **no UNIQUE** `drizzle/0001_burly_gressill.sql:19-30`, `drizzle/schema.ts:105-118` |
| Conversation creation has no Chatwoot-side uniqueness: `createConversation` always POSTs; `lock_to_single_conversation` is a no-op without `source_id` | `chatwootClient.ts:133-141`; `[cw-src]` `ConversationBuilder` + `ContactInboxBuilder` (`conversations.md` §2.1) |
| Contact creation **is** protected by Chatwoot uniqueness (phone/identifier per account) → loser typically 422 | `[cw-src]` `app/models/contact.rb` |

### Expected failure mode

- **Confirmed by source:** two open conversations for one contact are
  representable and likely (both handlers search before either commits).
- Duplicate contacts only if the two creates don't collide on `phone_number`/
  `identifier` (e.g. one handler sees `IsFromMe=true` with different resolution) —
  otherwise one handler fails 422 and its message is `failed`.
- `contacts_map`: two rows for `(connectionId, whatsappPhone)` after the race
  (read-then-write) `[legacy]`; later `findContactMapByChatwootConversation`
  `.limit(1)` picks one arbitrarily (`db.ts:312-329`).
- Cross-process/multi-instance deployment widens the race (no shared lock).

### Live tests (Phase 7)

**T-H2-1 (conversation duplication).**
1. Create contact `C4` with a synthetic phone; ensure **no** open conversation in
   connection X's inbox.
2. Fire **10 parallel** EVO webhooks for `C4`'s identity with distinct message
   ids, `IsFromMe=false` (e.g. `hey -n 10 -c 10` against the bridge webhook route).
3. Wait for completion; query Chatwoot: all conversations of `C4`.
4. **Proves H2** if >1 conversation exists with `status != "resolved"` in the
   inbox. Capture ids and creation timestamps.

**T-H2-2 (mapping duplication).**
1. Same burst as T-H2-1.
2. Query the bridge MySQL: `SELECT * FROM contacts_map WHERE connectionId=X AND
   whatsappPhone='…'`.
3. **Proves H2** if >1 row. (DB access required in Phase 7; schema is synthetic
   data only.)

**T-H2-3 (contact duplication — discriminating test).**
Run T-H2-1 with an identity whose existing contact has a **divergent** format
(H1a setup). Expect either duplicate contacts (race wins uniqueness gap) or one
422 `failed` message. Record which one for the rewrite's failure taxonomy
`[needs live validation]`.

---

## H5 — `chatwootInboxId` null

**Statement.** With a connection whose `chatwootInboxId` is null, conversation
reuse can never match and creation sends `inbox_id: null`.

### Evidence `[legacy]`

| Mechanism | Evidence |
|---|---|
| Reuse criterion compares `c.inbox_id === this.inboxId`; `this.inboxId` may be null | `chatwootClient.ts:126`, constructor `:49` |
| `chatwootInboxId` nullable | `drizzle/schema.ts:36`, `drizzle/0001_burly_gressill.sql:10` |
| Creation sends `inbox_id: inboxId || this.inboxId` → `null` | `chatwootClient.ts:135-139` |
| Bootstrap skips webhook config without inbox (side effect: no CW→bridge events either) | `webhookBootstrap.ts:90-93` |
| Chatwoot server path: blank `inbox_id` → `contact_inbox` lookup with nil source_id → `find_by!` → 404 | `[cw-src]` `conversations_controller.rb#contact_inbox`, `ContactInboxBuilder` (`conversations.md` §2.2) |

### Expected failure mode

- Every message from the connection: `findConversation` → `null` (C4), then
  `createConversation` → HTTP 404 (or 422) → message logged `failed`; no contact
  or conversation is created. The bug is **not** duplicate entities here, it is
  total loss of the WhatsApp→Chatwoot path for that connection.
- If the operator's Chatwoot version treated blank `inbox_id` differently (e.g.
  default inbox), the failure mode changes → `[needs live validation]`.

### Live tests (Phase 7)

**T-H5-1 (null inbox).**
1. Create connection X pointing at staging Chatwoot with `chatwootInboxId = NULL`
   (set via DB/API, staging only).
2. Send one EVO 1:1 message.
3. Capture: bridge `message_logs` (status/error), raw HTTP status of the
   `POST /conversations` attempt, whether any conversation was created.
4. **Proves H5** if the message is `failed` and/or the POST returns 4xx; record the
   exact status/body in the Phase 7 report.

**T-H5-2 (stale inbox id).**
1. Set `chatwootInboxId` to an id that does not exist in the account (or was
   deleted).
2. Send one message; observe 404 on `findConversation` (caught → null) and on
   `createConversation`.
3. Distinguishes "no inbox configured" from "inbox gone" in the failure taxonomy.

---

## H6-family — typing / representation issues

### H6.1 `IsFromMe` mis-typed (`Boolean("false") === true`)

- Evidence: `bridgeProcessor.ts:209`
  (`Boolean(goInfo.IsFromMe ?? goInfo.isFromMe ?? stdKey.fromMe ?? false)`).
- Expected failure mode: an incoming customer message whose payload carries the
  string `"false"` is processed as **own/external** message → sent to Chatwoot as
  `outgoing` (`:658-659`), name update skipped (`:613-615`), and the
  `sentMessageCache` check is applied (`:515-519`). Direction inversion is the
  user-visible symptom; contact/conversation resolution still reuses identity
  (no duplicate by itself).
- Live test **T-H6-1**: craft an EVO `Message` payload with `Info.IsFromMe="false"`
  (string) for a synthetic 1:1 chat; verify the message appears as `outgoing` in
  Chatwoot and that no `PUT /contacts/{id}` name update happened; compare with
  `IsFromMe=false` (boolean). Both variants must produce `incoming` after the fix.
- `[needs live validation]` for which real producers send strings (Evolution GO
  vs n8n re-serialization).

### H6.2 `IsGroup` mis-typed

- Evidence: `bridgeProcessor.ts:210` (`Boolean(goInfo.IsGroup ?? …)`); consequence
  path `:544-545` (identity becomes the full JID), `:616` (search with
  `isGroup=true`).
- Expected failure mode: a 1:1 message with `IsGroup="false"` is treated as group
  → identity key becomes `1555…@s.whatsapp.net` → identifier search exact-match
  fails (M7) → duplicate contact attempt (422 on identifier only if a contact
  with that identifier exists; usually a bogus "group" contact is created), and if
  accepted the conversation title/naming follows group rules.
- Live test **T-H6-2**: same payload variant as T-H6-1 with `IsGroup="false"`;
  assert no contact with `identifier=<jid>` is created and the message lands in
  the 1:1 contact. `[needs live validation]`.

### H6.3 `message_type` representation asymmetry (REST integer vs webhook string)

- Evidence: `[cw-src]` REST responses render `message_type_before_type_cast`
  (integer `0..3`) while webhooks send the enum getter (string) —
  `messages.md` §2.2; bridge compares to `"incoming"` only
  (`bridgeProcessor.ts:762`).
- Expected failure mode (latent): if a future Chatwoot version (or a framing
  component) delivered the integer form, F2 fails → an incoming customer message
  from Chatwoot would be forwarded back to WhatsApp (loop / duplicate send).
  Same risk for any payload where `private` arrives as `"false"`-style truthy
  string (`:762`): the note is then skipped rather than forwarded (safe
  direction) — document, don't "fix".
- Live test **T-H6-3**: capture one real `message_created` payload per event
  class (agent text, agent media, agent private note, contact message) and assert
  the exact JSON types of `message_type`, `private`, `id`, `conversation.id`.
  This is the authoritative fixture set for the Go decoder. `[needs live validation]`.

### H6.4 JID families that are not phone numbers (`@lid`)

- Evidence: `extractPhone` only strips `@…` (`bridgeProcessor.ts:149-151`); no
  format validation of the remainder beyond length ≥ 5 and no `@`
  (`:552-560`); createContact then forces `+` (`chatwootClient.ts:92`).
- Expected failure mode: a 1:1 chat addressed by an `@lid` JID produces a fake
  "phone" that either fails Chatwoot's `+[1-9]\d{1,14}` rule (422 on 3.x, silent
  phone drop on develop) or creates a wrong-format contact; subsequent messages
  may then duplicate (H1a) or fail.
- Live test **T-H6-4**: search Phase 7 captures for EVO payloads with `Info.Chat`
  ending `@lid` (the group fixtures already show LIDs for participants); if none
  exist in production captures, downgrade to a synthetic replay to observe the
  bridge's behaviour only. `[needs live validation]` for real-world occurrence.

### H6.5 attachment `filename`/`url` never present

- Evidence: `bridgeProcessor.ts:848-849` reads `att.data_url || att.url`;
  `[cw-src]` attachment `push_event_data` never contains `url`; `filename` is not
  produced either (`messages.md` §4 / `webhooks.md` §3).
- Expected failure mode: for CW→EVO attachments only `data_url` matters (works);
  `filename` sent to Evolution is always `""` (bridge passes it to
  `sendMedia(..., String(att.filename || ""))`, `bridgeProcessor.ts:859`). Not a
  duplication bug, but a contract gap to close in the rewrite.
- Live test **T-H6-5**: capture a real webhook with an image attachment; assert
  the `data_url` 301-redirect URL is fetchable and note the exact attachment keys.

---

## Verdict table after Phase 7 (to be filled)

| Hypothesis | Status | Evidence link |
|---|---|---|
| H1a/b/c/d | pending Phase 7 | T-H1-x raw captures |
| H2 conversation race | pending Phase 7 | T-H2-1 |
| H2 mapping race | pending Phase 7 | T-H2-2 (needs MySQL) |
| H5 null inbox | pending Phase 7 | T-H5-1/2 |
| H6.1–H6.5 | pending Phase 7 | T-H6-x |

No fix is implemented before the corresponding test demonstrably fails on legacy
behaviour (AGENTS §5; ADR practice from `docs/adr/0004-persistent-idempotency.md`).

## 6. Related hypotheses covered elsewhere

- **H3** group name overwrite — mechanism documented in `contacts.md` §5 and
  `allowed-operations.md` §5 D3; its evidence chain is complete without a live
  instance (payload fixture test planned in Phase 1).
- **H4** in-memory dedup — `webhooks.md` §5.
- **H7** account-wide search across connections — `contacts.md` §5.1(3).
- **H8** CW webhook not validated against account/inbox — `webhooks.md` §2 F7
  note + §7.
