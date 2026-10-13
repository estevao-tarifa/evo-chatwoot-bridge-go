# Contract — Chatwoot → Bridge Webhook (`message_created`)

Scope: what Chatwoot delivers to the bridge, the exact fields the bridge consumes,
the filters applied, and the deduplication assumptions this creates.

Sources: legacy `evo-chatwoot-bridge/` @ `daeb04b` (v5.4.4) code + tests; Chatwoot
`v3.0.0` + `develop` (2026-10-10) source; official docs (webhook management only —
**no payload reference page exists** `[cw-docs]`).

Marker legend: `[legacy]` · `[cw-src]` · `[cw-docs]` · `[needs live validation]` ·
`[unverified]`.

---

## 1. Ingestion

- Route: `POST /api/webhooks/chatwoot/:connectionId` (`webhooks.ts:38-57`).
- Responds `200 {received:true}` **before** processing, then processes inline
  (`webhooks.ts:46-56`). No authentication, no IP allow-list, no signature
  validation (§7; H8 in the rewrite assessment).
- The URL is configured per inbox via `PATCH /inboxes/{id}` `{channel:{webhook_url}}`
  (`inboxes.md` §4). All Chatwoot events for that API inbox are delivered to it;
  the bridge ignores everything except `message_created`.

## 2. Bridge filters (exact order, `processChatwootWebhook`)

`[legacy]` `bridgeProcessor.ts:755-776`:

| # | Condition | Action | Evidence |
|---|---|---|---|
| F1 | `payload.event !== "message_created"` | return | `:759` |
| F2 | `message_type === "incoming"` (customer message) | return | `:762` |
| F3 | `private === true` (internal note) | return | `:762` |
| F4 | `sender.type === "agent_bot"` | return | `:763` |
| F5 | `id` present **and** in `sentToChatwootCache` (bridge-authored message) | return (loop break) | `:765-772` |
| F6 | `conversation.id` missing | return | `:774-776` |
| F7 | no `contacts_map` row for `(connectionId, conversation.id)` | throw → logged `failed`, **no entity is created** | `:804-806` |

Notes:

- F2/F3/F4 are proven by `server/chatwoot-webhook.test.ts:98-131`.
- There is **no** check that the payload belongs to the connection's account/inbox
  (`conversation.inbox_id`/`inbox.id` are available in the payload but unused) — H8.
- `message_type` is only compared to the string `"incoming"`; `template` messages
  and any other value pass F2/F3/F4 and are forwarded to WhatsApp if a mapping
  exists (`messages.md` §2.2 for the string/integer asymmetry).
- Activity messages never reach the bridge: Chatwoot only fires the webhook when
  `message.webhook_sendable?` → `incoming? || outgoing? || template?`
  `[cw-src]` `app/models/concerns/message_filter_helpers.rb`, guard in
  `app/listeners/webhook_listener.rb#message_created`).

## 3. Payload fields consumed by the bridge

`[legacy]` usage sites + `[cw-src]` producer (`Message#webhook_data`,
`_message.json.jbuilder` shapes; `Conversation#webhook_data` → `EventDataPresenter#push_data`):

| Field | Consumed at | Chatwoot producer | Notes |
|---|---|---|---|
| `event` | `:759` | `payload.merge(event: "message_created")` | string |
| `id` | `:768` (dedup), `:787` (log) | `message.id` (internal id) | number or string [legacy types] |
| `message_type` | `:762` | enum getter → `"incoming"`/`"outgoing"`/`"template"` | **string in webhooks** |
| `private` | `:762` | boolean | |
| `sender.type` | `:763` | `"user"`, `"agent_bot"`, or absent (`Contact`) | `Contact#webhook_data` has no `type` key |
| `sender.name` | `:829` (signature) | user/agent_bot name | used when `signMessages` enabled |
| `content` | `:788,827,858` | `webhook_content` (caption for media) | may be null |
| `attachments[]` | `:780`, `:846-862` | `attachment.push_event_data` | fields below |
| `attachments[].data_url` (fallback `.url`) | `:848` | present | `.url` does **not** exist in Chatwoot ≥3.0 responses — dead fallback |
| `attachments[].file_type` | `:851` | `image/audio/video/file/location/fallback/share/story_mention/contact/…` | enum in source |
| `attachments[].content_type` | `:852` | only `develop`; **absent in v3.0.0** | 3.x relies on `file_type` |
| `attachments[].filename` | `:859` | **not produced** by `push_event_data` | always `""` in legacy |
| `conversation.id` | `:775`, `:805` | **display_id** (UI number), not internal DB id | consistent with create/list responses (`conversations.md`) |
| `conversation.inbox_id` | — | present but **unused** | H8 gap |
| `account.id` | — | `account.webhook_data` present but unused | H8 gap |

Synthetic example (fields as delivered, values fictitious):

```json
{
  "event": "message_created",
  "id": 10001,
  "content": "Hello",
  "message_type": "outgoing",
  "private": false,
  "source_id": null,
  "content_type": "text",
  "created_at": 1750000000,
  "conversation": { "id": 5001, "inbox_id": 10, "status": "open" },
  "inbox": { "id": 10, "name": "WhatsApp - test" },
  "sender": { "id": 7, "name": "Agent Test", "email": "agent@example.invalid", "type": "user" },
  "attachments": [
    { "id": 9001, "message_id": 10001, "file_type": "image",
      "content_type": "image/png", "data_url": "https://chatwoot.invalid/rails/active_storage/…" }
  ]
}
```

## 4. Delivery guarantees (Chatwoot side)

### Pinned v4.12.1 override (2026-10-11)

The official tag commit `899fce1c9286b793d165c8ad6c0200ab676f185b` distinguishes
**account** from **API-inbox** webhook delivery. Account deliveries pass
`webhook.secret` into the job and emit timestamp/raw-body HMAC when nonblank.
API-inbox delivery does not pass a secret and is unsigned in this call chain;
`hmac_token` is inbound contact-identity verification, not an outbound signing
secret. Do not apply develop's channel-signing behavior to this tag.

Account HMAC input is `timestamp + "." + rawBody`; headers are
`X-Chatwoot-Timestamp` and `X-Chatwoot-Signature: sha256=<hex>`.
`X-Chatwoot-Delivery` alone is not authentication. Account secret is generated
server-side/readable from account webhook JSON; this version does not permit a
caller-supplied secret field. No account subscription/routing change is authorized
here. Legacy API-inbox mode needs protected URLs per ADR-0006. Configured signed
mode must fail closed on missing/invalid signatures, never silently downgrade.
Replay-age limits are receiver policy, not a documented upstream retry horizon.

Pinned listener/job/trigger/model excerpts: the Chatwoot 4.12.1 source
validation. This verifies source capability,
not deployed image provenance or actual headers `[needs live validation]`.

### Historical v3/develop comparison

`[cw-src]` `lib/webhooks/trigger.rb`, `app/jobs/webhook_job.rb`,
`app/listeners/webhook_listener.rb`:

- Delivery is **asynchronous** via `WebhookJob` (Sidekiq) after the message commit.
- v3.0.0: plain `RestClient` POST, 5 s timeout, failures logged and swallowed
  (no retry).
- develop: `SafeFetch` POST, timeout `WEBHOOK_TIMEOUT` env or 5 s; on failure for
  `message_created`/`message_updated` of an **API inbox** webhook, Chatwoot marks
  the message **`failed`** (`Messages::StatusUpdateService`); retryable raises only
  exist for `agent_bot` webhooks with 429/500 — API-inbox webhooks are **not
  retried** `[cw-src]`.
- develop signs API-inbox payloads when a channel secret exists
  (`Channel::Api` includes `WebhookSecretable`):
  `X-Chatwoot-Timestamp`, `X-Chatwoot-Signature: sha256=HMAC_SHA256(secret, "<ts>.<body>")`,
  `X-Chatwoot-Delivery: <uuid>`. The legacy bridge never verifies these headers
  and `updateInbox` never seeds/sends the secret. v3.0.0 has no signature.
  → concrete input for ADR-006 (verify only what is proven; here it **is** proven
  for develop) `[cw-src]` `[needs live validation]` on the operator's version.
- develop `SafeFetch` blocks private-network URLs unless
  `SAFE_FETCH_ALLOW_PRIVATE_NETWORK=true` `[cw-src]` `lib/safe_fetch.rb` — if the
  bridge is reachable only on a private network, webhook delivery requires that env
  flag or a public `BRIDGE_URL`. Deployment note for `deploy/`, not a bridge bug.
- Exactly-once is **not** promised. Duplicate deliveries are possible whenever a
  response is lost; the bridge has no persistent inbound dedup (see §5).

## 5. Dedup assumptions (the loop-break design)

Two in-memory maps in `bridgeProcessor.ts` `[legacy]`:

| Cache | Key | Written when | Read when | TTL |
|---|---|---|---|---|
| `sentMessageCache` (`:34`) | Evolution message id | bridge sends CW→EVO text/media (`:837-838,860-861,872-873`) | EVO webhook `IsFromMe` (`:515-519`) | 5 min (`:35`) |
| `sentToChatwootCache` (`:59`) | Chatwoot **message id** from the POST response (`:704-707`) | bridge creates an `outgoing` message in Chatwoot (external own-message) | CW webhook `message_created` (`:768-772`) | 5 min |

Properties the rewrite must decide on (documented, not fixed here):

1. **In-memory only** — both maps are lost on restart; a Chatwoot delivery delayed
   beyond the restart window is forwarded to WhatsApp as a normal agent message.
2. **TTL 5 min** — a delivery/echo later than 5 min is not recognised (H4).
3. **Response-dependent** — the CW cache key comes from the create response
   (`result.id`); if the response is lost (timeout) the cache is not populated,
   Chatwoot's webhook arrives and the message is re-sent to WhatsApp (loop).
   Same pattern for Conversation ids in `contacts_map`.
4. **No uniqueness in the bridge DB** for `(connectionId, chatwootMessageId)` or
   for `contacts_map` (`drizzle/0001_burly_gressill.sql:19-30`) → duplicates are
   representable.
5. Duplicate Chatwoot deliveries (network-level) are processed twice: the legacy
   bridge has no cross-instance idempotency; ADR-004 (`processed_messages`) is the
   replacement design.

## 6. Evidence tests (legacy)

- `server/chatwoot-webhook.test.ts:98-131`: F1/F2/F3 behaviour.
- `server/v5.3-fixes.test.ts:87-130,403-453`: F5 cache semantics, string/number
  id interop, full loop scenario, TTL expiry.
- `server/v5.3-fixes.test.ts:511-539`: `SendMessage` acceptance on the EVO side
  (mirror of the loop problem).

## 7. Open points `[needs live validation]`

1. Operator's Chatwoot version: signature headers present? `SafeFetch` blocking?
   delivery timeout value?
2. Actual payload for `attachments` on the operator's version (confirm absence of
   `content_type`/`filename`).
3. Behaviour when the bridge returns 200 but processing fails (legacy) — Chatwoot
   considers delivery successful (no retry), so the message is silently lost for
   WhatsApp; only the bridge's `message_logs` records it. Confirm against real
   server logs in Phase 7.
4. `Captain::Assistant` sender shape, if used.
