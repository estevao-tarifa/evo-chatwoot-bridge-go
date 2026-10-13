# Contract — Chatwoot Messages (send paths + response shapes)

Scope: how the bridge sends messages to Chatwoot (WhatsApp → Chatwoot direction),
the exact payloads for JSON text vs multipart attachments, `message_type`
semantics on both sides of the API, and the sender types the bridge observes in
Chatwoot webhooks (full webhook contract: `webhooks.md`).

Sources: legacy `evo-chatwoot-bridge/` @ `daeb04b` (v5.4.4); Chatwoot `v3.0.0` +
`develop`; docs <https://developers.chatwoot.com/api-reference/messages/create-new-message>.

Marker legend: `[legacy]` · `[cw-src]` · `[cw-docs]` · `[needs live validation]` ·
`[unverified]`.

---

## 1. Operations

| # | Method | Request | Caller |
|---|---|---|---|
| 1.1 | `sendMessage(conversationId, content, messageType)` | `POST …/conversations/{id}/messages` JSON | text/location/contact/unknown + fallback (`bridgeProcessor.ts:661-663,696-699`) |
| 1.2 | `sendBase64Attachment(conversationId, base64, mime, caption, type, fileName)` | `POST …/messages` multipart | image/video/audio/document (base64 from EVO) + ad thumbnail (`bridgeProcessor.ts:688-695,669-676`) |
| 1.3 | `sendAttachment(conversationId, fileUrl, caption, …)` | `POST …/messages` multipart after downloading `fileUrl` | **defined but dead code in v5.4.4** — no call site (`chatwootClient.ts:163-192`) |

## 2. JSON text send (`sendMessage`)

`[legacy]` `chatwootClient.ts:154-161`:

```http
POST /api/v1/accounts/{acc}/conversations/{conversationId}/messages
Content-Type: application/json

{ "content": "…", "message_type": "incoming|outgoing", "private": false }
```

Direction chosen by the processor `[legacy]` `bridgeProcessor.ts:658-659`:

```ts
const messageDirection = info.isFromMe ? "outgoing" : "incoming";
```

- `incoming` → customer message (the normal WhatsApp → Chatwoot path).
- `outgoing` → own messages not sent by the bridge (external automation / other
  WhatsApp clients), i.e. `IsFromMe=true` and message-id not in the sent cache
  (`bridgeProcessor.ts:512-524`).
- Group messages: content is prefixed `*<senderName>:*\n` for both directions
  (`bridgeProcessor.ts:652-654`); the group itself is the contact.
- Click-to-WhatsApp ads append a formatted ad block to the text
  (`bridgeProcessor.ts:661-662`, `formatAdContext` `:325-333`).

### 2.1 `message_type` semantics (verified in Chatwoot source)

`Messages::MessageBuilder` `[cw-src]` (`app/builders/messages/message_builder.rb`,
identical in `v3.0.0` and `develop`):

- `@message_type = params[:message_type] || 'outgoing'`.
- If the conversation's inbox is **not** `Channel::Api` and `message_type == 'incoming'`
  → `raise 'Incoming messages are only allowed in Api inboxes'` → 422. The bridge
  therefore only works because it uses an API inbox.
- Sender assignment: `incoming` → `sender = conversation.contact`;
  `outgoing` → `sender = Current.user` (owner of the API token) unless
  `sender_type=AgentBot`+`sender_id` are supplied (the bridge never sends those).
- `private` → cast boolean, default `false`; text send always sends `false`,
  multipart omits it (default false) `[legacy]`.
- `content` may be an empty string; not validated on API inboxes.

### 2.2 Chatwoot's stored `message_type` values

`[cw-src]` `app/models/message.rb`: `enum message_type: { incoming: 0, outgoing: 1, activity: 2, template: 3 }`.

- **REST API responses** render `message.message_type_before_type_cast` → **integer**
  `0|1|2|3` (`app/views/api/v1/models/_message.json.jbuilder`) — confirmed by the
  docs schema enum `0..3`.
- **Webhook payloads** render the enum getter → **string** `"incoming"`,
  `"outgoing"`, `"activity"`, `"template"` (`Message#webhook_data`,
  `message_type: message_type`) `[cw-src]`.
- The bridge reads only the webhook string (`processChatwootWebhook`), never the
  REST integer. Porting rule: never compare the REST `message_type` to a string
  and never compare the webhook one to a number (H6-adjacent, HYPOTHESES §H6.3).

### 2.3 Response handling

- `sendMessage` returns the raw body; the processor extracts `result?.id`
  (`bridgeProcessor.ts:704`) and caches it when it authored an `outgoing` message
  (`addToSentToChatwootCache`, `bridgeProcessor.ts:705-707`) to break the
  Chatwoot → WhatsApp loop (`webhooks.md` §4).
- REST message body: `id` (internal message id, number), `content`,
  `message_type` (integer), `status`, `private`, `sender`, `attachments`,
  `conversation_id` (**display_id**) `[cw-src]` `_message.json.jbuilder`.
- Failure modes: 404 conversation not found; 403 no permission; 422 raised
  validation; 5xx/timeout → exception → message log `failed`
  (`bridgeProcessor.ts:719-723`). No retry in legacy.

## 3. Multipart attachment send (`sendBase64Attachment`)

`[legacy]` `chatwootClient.ts:194-222` (used for EVO base64 payloads and ad thumbnails):

```text
POST /api/v1/accounts/{acc}/conversations/{conversationId}/messages
Content-Type: multipart/form-data
  attachments[] : <binary buffer>, filename=<fileName>, contentType=<mime>   // :209-212
  message_type  : "incoming" | "outgoing"                                    // :213
  content       : <caption or group prefix + caption>                        // :214
  (private omitted → defaults false)
```

- File name is built by the caller: `file.<ext>` default, or
  `${msg.type}_${Date.now()}.${ext}`, or document `fileName`
  (`bridgeProcessor.ts:682-694`); ad thumbnail `ad_thumbnail.jpg` (`:675`).
- `sendAttachment` (dead) would have downloaded `fileUrl` with axios
  (`responseType: arraybuffer`, 60 s timeout) and derived filename/content-type
  from the response headers (`chatwootClient.ts:172-175`). **Do not port dead
  code** — flag for the maintainer (it is also an SSRF-shaped primitive: it fetches
  an arbitrary caller-supplied URL).
- Multipart + `message_type=incoming` + `content` is the exact form shown in the
  official docs `[cw-docs]` (only difference: docs use curl `-F`).
- Failure modes: identical to §2.3 plus download-side errors in the dead path.

## 4. What the bridge consumes from CW→EVO attachments (context only)

For inbound agent attachments the processor reads the **webhook** attachment
objects (`bridgeProcessor.ts:780,846-862`): `att.data_url || att.url`,
`att.file_type`, `att.content_type`, `att.filename`. Verified Chatwoot event
fields: `push_event_data` = `id, message_id, file_type, account_id` plus
`extension, content_type, data_url, thumb_url, file_size, width, height` in
develop, and **no `content_type`/`filename`** in v3.0.0 (`[cw-src]`
`app/models/attachment.rb`). Consequence: `att.filename` is always `""` and
`att.content_type` is absent on 3.x → media type falls back to `file_type`
(`bridgeProcessor.ts:851-856`). This belongs to `webhooks.md`; recorded here
because it explains the bridge's `sendMedia(..., fileName)` signature.

## 5. Sender types observed in `message_created` webhooks

`[cw-src]` `webhook_data` per sender model (same in v3.0.0 and develop):

| Sender model | Payload | Bridge filter impact |
|---|---|---|
| `User` (agent) | `{id, name, email, type: "user"}` | processed → forwarded to WhatsApp (outgoing only) |
| `AgentBot` | `{id, name, type: "agent_bot"}` | **skipped** (`bridgeProcessor.ts:763`) |
| `Contact` | full contact data — **no `type` key** | skipped earlier because `message_type === "incoming"` (`bridgeProcessor.ts:762`) |
| `Captain::Assistant` | `[unverified]` — bridge neither filters nor documents it | would be treated like a user sender |

## 6. Failure/edge list

| Edge | Effect | Evidence |
|---|---|---|
| 30 s axios timeout on send | `failed` log, no retry; CW may still have created the message → response lost → **dedup cache not populated** → possible loop (H4-adjacent) | `chatwootClient.ts:56`; `bridgeProcessor.ts:705-707` |
| `result.id` missing in response | message sent but not cached (outgoing case only) | `bridgeProcessor.ts:704-707` |
| Empty content text send | Chatwoot may reject or store empty | `[needs live validation]` |
| Attachment > Chatwoot upload limit | 413/422 | `[needs live validation]` (operator's `MAX_ATTACHMENT_SIZE`) |
| Message to conversation of another account (stale map) | 404 | `[cw-src]` scoping |

## 7. Open points `[needs live validation]`

1. Exact 422 body when `message_type=incoming` is sent to a non-API inbox
   (not reachable in the legacy design — all connections use API inboxes).
2. Chatwoot attachment size limit and its response on the operator's deployment.
3. Real `Captain::Assistant` sender payload, if the operator uses Captain.
