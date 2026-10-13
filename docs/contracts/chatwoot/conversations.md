# Contract — Chatwoot Conversations (bridge lookup/reuse/creation)

Scope: how the bridge finds, reuses and creates Chatwoot conversations, and the
Chatwoot-side rules that decide whether a creation actually creates.

Sources: legacy `evo-chatwoot-bridge/` @ `daeb04b` (v5.4.4); Chatwoot source
`v3.0.0` + `develop` (2026-10-10); docs
<https://developers.chatwoot.com/api-reference/conversations/*>.

Marker legend: `[legacy]` · `[cw-src]` · `[cw-docs]` · `[needs live validation]` ·
`[unverified]`.

---

## 1. `findConversation` — lookup

`[legacy]` `chatwootClient.ts:120-131`:

```http
GET /api/v1/accounts/{accountId}/contacts/{contactId}/conversations
api_access_token: <token>
```

No query parameters (no `page`, no `sort`).

Response: `{ payload: [conversation…] }` `[cw-docs]` + `[cw-src]`
(`contacts/conversations/index.json.jbuilder`: `json.payload { array! @conversations }`).
Each conversation includes `id`, `inbox_id`, `status` (`open|resolved|pending`),
`last_activity_at`, etc. `[cw-docs]`.

### 1.1 Reuse criterion (client side — exact)

```ts
return conversations.find(c =>
  c.inbox_id === this.inboxId && c.status !== "resolved"   // chatwootClient.ts:125-126
) || null;
```

- **Inbox filter is mandatory**: only a conversation whose `inbox_id` is
  **strictly equal** (`===`, number) to the connection's `chatwootInboxId` is
  reused. `chatwootInboxId` is nullable in the schema (`drizzle/schema.ts:36`,
  `drizzle/0001_burly_gressill.sql:10`).
- **Status filter**: any status except `"resolved"` qualifies (`open`, `pending`,
  and any future/unknown status string).
- `.find()` → first match in the array order returned by Chatwoot. The bridge does
  not sort; server order decides (see §1.3).
- All errors are swallowed → `null` (`chatwootClient.ts:128-130`). A transient
  500/timeout is indistinguishable from "no conversation".

### 1.2 MISS conditions (all → `null` → creation attempted)

| # | Condition | Evidence |
|---|---|---|
| C1 | HTTP/transport error on the GET | `chatwootClient.ts:128-130` |
| C2 | Contact has no conversation in the configured inbox | criterion |
| C3 | All conversations in that inbox are `resolved` | criterion |
| C4 | `chatwootInboxId` is `null` → `c.inbox_id === null` is never true | `chatwootClient.ts:126` + nullable schema (H5) |
| C5 | Conversation exists but its inbox differs from the configured one | criterion |
| C6 | Token user cannot see the inbox/conversation → filtered server-side | v3.0.0: `inbox_ids = Current.user.assigned_inboxes.pluck(:id)`; develop: `Conversations::PermissionFilterService` `[cw-src]` |
| C7 | Pagination window exceeded: v3.0.0 returns `order(id: :desc).limit(20)`; develop returns `order(last_activity_at desc, id desc).limit(25)` — an open conversation older than the window is invisible | `[cw-src]` both controllers |

### 1.3 Multiple conversations — which one wins

- v3.0.0: ordered `id DESC` → the **newest** conversation in the first 20.
- develop: ordered `last_activity_at DESC, id DESC` → the **most recently active**
  of the first 25.
- If several non-resolved conversations exist in the inbox, the bridge reuses
  exactly one (the first match) and leaves the others untouched. Duplicates are
  never cleaned up.

### 1.4 Resolved conversations

- A resolved conversation fails the client criterion → the bridge creates a
  **new** conversation on the next message, per legacy behaviour.
- This is a deliberate-vs-accidental question for the rewrite (AGENTS §7 rule 7);
  the contract to preserve is: the legacy bridge never sends messages into a
  `resolved` conversation it opened.

## 2. `createConversation` — creation

`[legacy]` `chatwootClient.ts:133-141`:

```json
POST /api/v1/accounts/{acc}/conversations
{
  "contact_id": 123,
  "inbox_id": 10,          // inboxId param || this.inboxId (may be null — H5)
  "status": "open"
}
```

Returned value is the conversation object itself (`create.json.jbuilder` renders
the conversation partial) `[cw-src]`; caller reads `conversation.id`
(`bridgeProcessor.ts:619-622`).

### 2.1 Missing `source_id` — verified Chatwoot behaviour

Official docs mark `source_id` as required `[cw-docs]` ("Creating a conversation
in chatwoot requires a source id"), but the source proves the API path succeeds
without it for `Channel::Api`:

1. `conversations_controller#create` → `contact_inbox` before-action →
   `ContactInboxBuilder` with `source_id: nil`
   `[cw-src]` (`app/controllers/api/v1/accounts/conversations_controller.rb`,
   `app/builders/contact_inbox_builder.rb`).
2. Builder generates `SecureRandom.uuid` for `Channel::Api`
   (`generate_source_id` case), then `ContactInbox.where(contact_id, inbox_id,
   source_id-uuid).first_or_create!`.
3. **Side effect**: because the UUID is fresh on every call, every
   source_id-less creation adds a **new `contact_inbox` row**
   (`contact_id+inbox_id+new source_id` finds nothing → creates).
4. `ConversationBuilder#perform` = `look_up_exising_conversation || create_new_conversation`;
   the lookup only returns an existing conversation when
   `contact_inbox.inbox.lock_to_single_conversation?` **and** that
   `contact_inbox` already has conversations.
   With a freshly generated source_id/contact_inbox (step 3) the lookup finds
   nothing → **`lock_to_single_conversation` has no effect on source_id-less API
   creations** `[cw-src]`. Reuse is 100 % the bridge's responsibility.
   `[needs live validation]` to confirm this exact chain on the operator's version
   (identical code present in v3.0.0).

### 2.2 Inbox handling

- `inbox_id` is taken from the method parameter if truthy, else `this.inboxId`
  (`chatwootClient.ts:137`).
- If both are null, the payload contains `"inbox_id": null`. Server side:
  `before_action :inbox` returns early when `params[:inbox_id].blank?`, so
  `build_contact_inbox` returns nil and the controller falls to
  `lookup_scope.find_by!(source_id: params[:source_id])` with `source_id` also
  nil → `ActiveRecord::RecordNotFound` → HTTP 404 `[cw-src]`.
  `[needs live validation]` for the exact status/body on the deployed version.
  This is the terminal failure of H5 (conversation never reused, then creation
  cannot succeed).
- If `contact_id` is unknown: `Current.account.contacts.find` → 404 `[cw-src]`.

### 2.3 `status` on create

- `status: "open"` is accepted by the docs payload and merged by
  `ConversationBuilder#conversation_params` `[cw-docs]` `[cw-src]` (v3.0.0 and
  develop). The bridge therefore always requests an `open` conversation.
- Status when the parameter is omitted is out of scope for this contract: the
  bridge always sends `status: "open"` (`[legacy]` `chatwootClient.ts:138`).

## 3. `findOrCreateConversation` — sequence

`[legacy]` `chatwootClient.ts:143-150`; caller `bridgeProcessor.ts:619-622`:

```text
findConversation(contact.id)
  ├─ hit  → reuse (id + status + inbox unchanged; bridge never reopens/updates)
  └─ miss → createConversation(contact.id) → conversation.id
```

- The bridge never calls `PATCH /conversations/{id}` (no status changes, no
  assignee, no labels) — verified: no such call exists in `chatwootClient.ts`.
- After reuse or creation the mapping `(connectionId, whatsappPhone) →
  chatwootContactId, chatwootConversationId` is upserted
  (`bridgeProcessor.ts:624-630`, `db.ts:291-310`). `contacts_map.chatwootConversationId`
  is **not** unique and is only updated on the message path — it can point to an
  old conversation while Chatwoot holds a newer open one.

## 4. Failure modes

| Mode | Trigger | Bridge behaviour |
|---|---|---|
| 404 | conversation create without inbox/source_id (H5); unknown contact | exception → message `failed` (`bridgeProcessor.ts:719-723`) |
| 401/403 | token lacks permission on inbox (`inbox authorize`) | exception → `failed` |
| 422 | validation error while building the conversation (rare for the bridge payload) | exception → `failed` |
| 5xx/timeout (30 s) | Chatwoot unavailable | exception → `failed`, no retry |
| Duplicate `open` conversations | two concurrent handlers both miss C2 (see HYPOTHESES H2); Chatwoot has no uniqueness rule on conversations | two open conversations for one contact; mapping keeps the last one |

## 5. Legacy regression evidence

- Behaviour is exercised indirectly by `server/chatwoot-webhook.test.ts` (webhook
  guards) — there is no unit test of `findConversation`/`createConversation`
  in the legacy suite; the contract above is proven by code + Chatwoot source.

## 6. Open points `[needs live validation]`

1. Actual HTTP status/body when creating a conversation with `inbox_id: null`
   and no `source_id` on the operator's Chatwoot version.
2. Whether `contacts/{id}/conversations` on the deployed version returns 20
   (v3.x) or 25 (develop) items and with which ordering.
3. Whether the deployed version's token user is a member of the connection inbox
   (C6) — the legacy bootstrap never ensures membership on startup; the SPA has
   `chatwoot.updateInboxAgents` (`routers/chatwoot.ts:249-254`).
4. Exact order Chatwoot returns for multiple visible non-resolved conversations
   (decides which one is reused).
