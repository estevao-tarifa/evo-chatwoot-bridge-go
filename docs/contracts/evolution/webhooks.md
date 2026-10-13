# Contract — Evolution webhook ingress (as implemented by the legacy bridge)

Scope: every payload shape the legacy `processEvolutionWebhook` parser handles,
with `file:line` evidence, fallbacks, and the gaps that feed the identity bug
(`AGENTS.md` §7). Sources:

| Source | Revision | Role |
|---|---|---|
| `evo-chatwoot-bridge/` (read-only) | commit `daeb04b07aa0ae790a31c22de800be8e0d4ab42e`, tag `v5.4.4` (2026-04-07) | canonical implementation |
| Evolution GO source | tag `0.6.1` commit `89eaae337e61` (2026-03-25) — closest to bridge release; `main` commit `9337afc47e10` (2026-07-03, == tag `0.7.2`) | payload producer |
| Official docs | <https://docs.evolutionfoundation.com.br/en/evolution-go/webhooks> and <https://docs.evolutionfoundation.com.br/evolution-api/configuration/webhooks> | cross-check |

All examples below are **synthetic** (`5511900000000`) — no real data, no tokens.
Anything not proven by the two sources above is marked `[needs live validation]`
or `[unverified]`.

## 1. Inbound endpoint

- `POST /api/webhooks/evolution/:connectionId` — any JSON body; immediately
  `200 {received:true}`, then async `processEvolutionWebhook`
  (`server/webhooks.ts:11-31`). Body limit `50mb` (`server/_core/index.ts:39`).
- **Legacy has no webhook authentication** (per the legacy endpoint audit).
  The Go rewrite adds a per-connection URL token (ADR-0006); Evolution GO offers
  no signature/HMAC in 0.6.1/0.7.2 source (`pkg/events/webhook/webhook_producer.go`
  sends only `Content-Type: application/json`).

## 2. Envelope and event-name acceptance

Root parsed fields (`server/services/bridgeProcessor.ts`):

| Field | Read | Line |
|---|---|---|
| `event` | `String(payload.event \|\| "")` | 498 |
| `data` | `payload.data`; missing → silent no-op | 507-509 |

Accepted events:

| `payload.event` value | Accepted | Behaviour | Line |
|---|---|---|---|
| `Message` | yes | message path | 501 |
| `MESSAGE` | yes | message path | 501 |
| `messages.upsert` | yes | message path (standard API) | 501 |
| `MESSAGES_UPSERT` | yes | message path (standard API v2) | 501 |
| `SendMessage` | yes | message path → cache-based loop filter | 501 |
| `SENDMESSAGE` | yes | same | 501 |
| `call` / `CALL` / `Call` | yes | `handleCallEvent` → optional call rejection | 502-504 |
| anything else | no | return, no log | 505 |

Event-name findings for Phase 7:

1. **Call events**: Evolution GO 0.6.1/0.7.2 emits `CallOffer`, `CallAccept`,
   `CallTerminate`, `CallOfferNotice`, `CallRelayLatency`
   (`0.6.1/pkg/whatsmeow/service/whatsmeow.go:1676,1710,1714`; `main` same at
   `:1905,1937,1941`). None matches the legacy `["call","CALL","Call"]` set →
   **call rejection is likely dead code on these versions**
   `[needs live validation]`. The official docs confirm emitted names
   `CallOffer`/`CallRelayLatency`/`CallTerminate`.
2. **Standard API `SEND_MESSAGE`** (Node) is *not* in `MSG_EVENTS`; a
   `send-message` subscription alone yields webhooks the bridge silently drops.
   Standard API v2 sent messages normally also arrive as `MESSAGES_UPSERT` with
   `data.key.fromMe=true`, which is handled.
3. Webhook **delivery** depends on the instance's subscribed event list inside
   Evolution GO (`CallWebhook` gates `Message` on `MESSAGE`,
   `SendMessage` on `SEND_MESSAGE`; `0.6.1/.../whatsmeow.go:1913-1922`).

### 2.1 Webhook configuration (subscribe) — version-sensitive

Legacy `setWebhook` posts to `/instance/connect` with
`subscribe: ["Message","SendMessage","Receipt","ChatPresence","ConnectionUpdate",
"QrcodeUpdated","Presence","HistorySync","Call","Label","Contact","Group",
"Newsletter"]` (`server/services/evolutionClient.ts:115-158`). The comment says
Evolution GO uppercases internally. In 0.6.1 **and** 0.7.2 the server validates
`subscribe` values against the exact uppercase enum set
(`MESSAGE,SEND_MESSAGE,READ_RECEIPT,PRESENCE,HISTORY_SYNC,CHAT_PRESENCE,CALL,
CONNECTION,LABEL,CONTACT,GROUP,NEWSLETTER,QRCODE,BUTTON_CLICK,PICTURE,USER_ABOUT`
or `ALL`; `0.6.1/pkg/instance/service/instance_service.go:209-230`,
`pkg/internal/event_types/event_types.go`) and **discards** unknown values.
PascalCase entries therefore do not configure anything on these versions.
Legacy mitigation: `webhookBootstrap.ts` deliberately never rewrites the EVO
webhook after creation "to preserve manually configured events"
(`server/services/webhookBootstrap.ts:9-13,136`). `[needs live validation]`:
which Evolution GO version the pilot runs, and whether the webhook is configured
via the manager panel (expected) or by `setWebhook`. The Go client must send
`ALL` or exact enum values.

## 3. `data` shapes — Evolution GO (PascalCase)

Producer evidence: `data` is the whatsmeow `*events.Message` marshalled with
`encoding/json` plus server additions (`0.6.1/pkg/whatsmeow/service/whatsmeow.go:839`,
`:1470-1475`), `Info` = whatsmeow `types.MessageInfo`, `Message` = `waE2E.Message`
(JSON tags), `Message.base64` added by Evolution GO
(`whatsmeow.go:1460`), `groupData` = `whatsmeow.GetGroupInfo()` **only when the
call succeeds** (`whatsmeow.go:1470-1475`). JIDs marshal as strings via
`types.JID.MarshalText` (whatsmeow `types/jid.go:232-239`); `IsFromMe`/`IsGroup`
are real JSON booleans.

### 3.1 Field extraction (`parseInfo`, bridgeProcessor.ts:201-216)

| Parsed field | Read order | Final fallback | Line |
|---|---|---|---|
| `chatJid` | `data.Info.Chat` → `data.Info.chat` → `data.key.remoteJid` | `""` | 207 |
| `senderJid` | `data.Info.Sender` → `data.Info.sender` → `data.key.participant` → `data.key.remoteJid` → `data.Info.Chat` | `""` | 208 |
| `isFromMe` | `data.Info.IsFromMe ?? data.Info.isFromMe ?? data.key.fromMe` | `false` | 209 |
| `isGroup` | `data.Info.IsGroup ?? data.Info.isGroup` else `chatJid.includes("@g.us")` | computed | 210 |
| `pushName` | `data.Info.PushName` → `data.Info.pushName` → `data.pushName` → `data.PushName` | `""` | 211 |
| `groupName` | `data.groupData.Name` → `data.groupData.name` | `""` | 212 |
| `messageId` | `data.Info.ID` → `data.Info.id` → `data.key.id` | `""` | 213 |
| `mediaType` | `data.Info.MediaType` → `data.Info.mediaType` | `""` | 214 |

Not read anywhere: `Info.SenderAlt` (the LID — see `jid-lid-groups.md`),
`Info.Type`, `Info.Timestamp`, `Info.VerifiedName`, `Info.ServerID`.

### 3.2 Message content (`parseMessage`, bridgeProcessor.ts:359-488)

Detection order (first match wins):

| # | Trigger | Result | Line |
|---|---|---|---|
| 1 | `message.conversation` | text | 363-365 |
| 2 | `message.extendedTextMessage.text` | text + mention resolution | 366-372 |
| 3 | `message.imageMessage` | image; `base64` from `message.base64`; `mimetype` default `image/jpeg`; caption | 375-383 |
| 4 | `message.videoMessage` | video; default `video/mp4` | 386-394 |
| 5 | `message.audioMessage` | audio; default `audio/ogg; codecs=opus` | 397-404 |
| 6 | `message.documentMessage` | document; default `application/octet-stream`; `fileName` ← `fileName \|\| title` | 407-416 |
| 7 | `message.stickerMessage` | sticker → **skipped** | 419-421, 573-577 |
| 8 | `message.contactMessage` | text card (`displayName`, `vcard`) | 424-430 |
| 9 | `message.contactsArrayMessage.contacts[]` | names joined `, ` | 431-436 |
| 10 | `message.locationMessage` | lat/lng/name/address + Google Maps URL | 439-450 |
| 11 | `message.reactionMessage` | reaction → **skipped** | 453-456, 573 |
| 12 | `message.protocolMessage` | protocol → **skipped** | 459-461, 573 |
| 13 | `message.viewOnceMessage` / `viewOnceMessageV2` | recurse into `.message` | 464-468 |
| 14 | `message.base64` present | type from `Info.MediaType` (`image`/`video`/`audio`/`document`, else `image`); mime map | 471-485 |
| 15 | none | `unknown` → text `[Tipo de mensagem não suportado]` | 487 |

### 3.3 `contextInfo` fields used

- Mentions: `message.extendedTextMessage.contextInfo.mentionedJID` (fallback
  `mentionedJid`), array of JID strings (`bridgeProcessor.ts:230-234`).
- Ads (click-to-WhatsApp): `extendedTextMessage.contextInfo.externalAdReply`
  (fallback `ExternalAdReply`); fields `title`, `body`, `sourceUrl`,
  `thumbnailUrl`, `sourceApp`, `thumbnail` (string, `Uint8Array` or Buffer →
  base64) (`bridgeProcessor.ts:279-320`). Formatted block appended to text
  (`:325-333`), thumbnail optionally sent as a follow-up image attachment
  (`:666-680`).

## 4. `data` shapes — standard Evolution API (camelCase)

Canonical fixture from the legacy tests (`server/v4.2-rewrite.test.ts:331-354`):
`data.key.remoteJid`, `data.key.fromMe`, `data.key.id`, `data.pushName`,
`data.message`. Official docs confirm event names `MESSAGES_UPSERT`/`messages.upsert`
and the general `{event, instance, data:{...}, date_time, sender, server_url,
apikey}` envelope (docs pages cited above; payload sample from the official docs
preview).

Parser mapping differences:

| Field | camelCase source | Note |
|---|---|---|
| `chatJid` | `data.key.remoteJid` | group JID for group chats |
| `senderJid` | `data.key.participant` → `data.key.remoteJid` | participant present on group messages |
| `isFromMe` | `data.key.fromMe` | |
| `isGroup` | derived from `chatJid` (`@g.us`) | standard payloads carry no `IsGroup` |
| `pushName` | `data.pushName` | |
| `messageId` | `data.key.id` | |
| `groupName` | **absent** | → display name falls back to `"Grupo"` (H3) |
| `Message` | `data.message` | same detection order as §3.2 |

Limitations: no `groupData` → no LID→phone mention resolution and no real group
name; `@lid`-addressed chats are not swapped by the Node API the way Evolution GO
does (see `jid-lid-groups.md` §4).

## 5. Synthetic examples (fixtures for Go parser tests)

### 5.1 Evolution GO 1:1 text

```json
{
  "event": "Message",
  "data": {
    "Info": {
      "Chat": "5511900000001@s.whatsapp.net",
      "Sender": "5511900000001:38@s.whatsapp.net",
      "SenderAlt": "100000000000001@lid",
      "IsFromMe": false,
      "IsGroup": false,
      "ID": "3EB0EXAMPLE0000000001",
      "Type": "text",
      "PushName": "Synthetic Contact",
      "Timestamp": "2026-04-07T12:00:00-03:00",
      "MediaType": ""
    },
    "Message": {
      "conversation": "Oi",
      "messageContextInfo": { "messageSecret": "<redacted>" }
    },
    "IsEphemeral": false,
    "IsViewOnce": false,
    "IsEdit": false
  },
  "instanceId": "<instance-uuid>",
  "instanceToken": "<redacted>"
}
```

### 5.2 Evolution GO group text with mention and `groupData`

```json
{
  "event": "Message",
  "data": {
    "Info": {
      "Chat": "120363000000000001@g.us",
      "Sender": "5511900000002@s.whatsapp.net",
      "SenderAlt": "100000000000002@lid",
      "IsFromMe": false,
      "IsGroup": true,
      "ID": "ACFEXAMPLE0000000002",
      "Type": "text",
      "PushName": "Synthetic Member",
      "MediaType": ""
    },
    "Message": {
      "extendedTextMessage": {
        "text": "@100000000000003 olá",
        "contextInfo": { "mentionedJID": ["100000000000003@lid"] }
      }
    },
    "groupData": {
      "JID": "120363000000000001@g.us",
      "Name": "Synthetic Group",
      "Participants": [
        {
          "JID": "100000000000003@lid",
          "LID": "100000000000003@lid",
          "PhoneNumber": "5511900000003@s.whatsapp.net",
          "DisplayName": ""
        }
      ]
    }
  },
  "instanceId": "<instance-uuid>",
  "instanceToken": "<redacted>"
}
```

Group name `Name` comes from whatsmeow `types.GroupInfo` where `GroupName` is an
embedded struct → JSON-flattened to `groupData.Name` (whatsmeow
`types/group.go:21-50,70-75`). Mentions resolve to `@5511900000003`
(`bridgeProcessor.ts:223-263`; tests `server/v5.3-fixes.test.ts:184-322`).

### 5.3 Evolution GO media (image)

```json
{
  "event": "Message",
  "data": {
    "Info": {
      "Chat": "5511900000001@s.whatsapp.net",
      "Sender": "5511900000001:45@s.whatsapp.net",
      "IsFromMe": false,
      "IsGroup": false,
      "ID": "3EB0EXAMPLE0000000003",
      "Type": "media",
      "PushName": "Synthetic Contact",
      "MediaType": "image"
    },
    "Message": {
      "imageMessage": { "mimetype": "image/jpeg", "caption": "Synthetic caption" },
      "base64": "<base64>"
    }
  },
  "instanceId": "<instance-uuid>",
  "instanceToken": "<redacted>"
}
```

(`base64` present only when `WEBHOOK_FILES=true`, default; with MinIO/S3 the same
slot carries `mediaUrl` instead — official Evolution GO webhook docs.)

### 5.4 `SendMessage` (echo of a bridge send) — **no `groupData`**

```json
{
  "event": "SendMessage",
  "data": {
    "Info": {
      "Chat": "120363000000000001@g.us",
      "Sender": "5511900000009@s.whatsapp.net",
      "IsFromMe": true,
      "IsGroup": true,
      "ID": "3EB0BRIDGESENT000000001"
    },
    "Message": { "extendedTextMessage": { "text": "Resposta do agente" } },
    "MessageContextInfo": {}
  },
  "instanceToken": "<redacted>",
  "instanceId": "<instance-uuid>",
  "instanceName": "synthetic-instance"
}
```

Producer: `0.6.1/pkg/sendMessage/service/send_service.go:2224-2244`, root keys at
`:2292-2294`. **Even for groups there is no `groupData` on `SendMessage`** — the
bridge must not derive group names from echo events (H3).

### 5.5 Status / broadcast / newsletter (filtered)

```json
{
  "event": "Message",
  "data": {
    "Info": {
      "Chat": "status@broadcast",
      "Sender": "5511900000005@s.whatsapp.net",
      "IsFromMe": false,
      "IsGroup": false,
      "ID": "status-example-1",
      "PushName": "Synthetic",
      "MediaType": "image"
    },
    "Message": { "imageMessage": { "mimetype": "image/jpeg", "caption": "promo" } }
  }
}
```

Filtered when `chatJid` contains `@broadcast` or `@newsletter`
(`bridgeProcessor.ts:532-540`; commit `v5.4.4`; tests
`server/v5.3-fixes.test.ts:541-605`).

### 5.6 Standard API (camelCase)

```json
{
  "event": "messages.upsert",
  "instance": "synthetic-instance",
  "data": {
    "key": {
      "remoteJid": "5511900000006@s.whatsapp.net",
      "fromMe": false,
      "id": "3EB0EXAMPLE0000000006"
    },
    "message": { "conversation": "Hello from the standard API" },
    "messageTimestamp": 1709550600,
    "pushName": "Synthetic Person"
  },
  "date_time": "2026-04-07T12:05:00.000Z",
  "sender": "5511900000006",
  "server_url": "https://evolution.example.com",
  "apikey": "<redacted>"
}
```

## 6. Sanitisation and secrets (Go rewrite requirements)

- Legacy log insert runs `sanitizePayload` (`bridgeProcessor.ts:107-130`,
  applied at `:134-145`): strings > 10 000 chars → `[TRUNCATED: n chars]`;
  certain binary keys (`base64`, `thumbnail`, `jpegThumbnail`, …) > 200 chars →
  `[BINARY: n chars]`; payload > 500 000 chars → `{_truncated, _originalSize,
  _preview}`. It also inserts failed events with `rawPayload` sanitised only this
  way.
- **Not redacted by the legacy sanitizer**: `instanceToken` (every Evolution GO
  webhook carries it at the root — `0.6.1/.../whatsmeow.go:1838-1841`),
  `instanceId`, `instanceName`, and `apikey` from standard-API payloads. These
  end up in the `message_logs.rawPayload` JSON column. The Go rewrite must
  redact/omit them at the single sanitizer choke point (`AGENTS.md` §8).
- `Info.VerifiedName.Certificate` (base64-ish strings < 10 000 chars) also
  survives the legacy sanitizer; treat as PII-free but useless — strip.

## 7. Open questions (`[needs live validation]`)

1. Which Evolution GO version runs in the pilot, and are Message/SendMessage
   webhooks configured via `subscribe` (`ALL`?) or the manager panel?
2. Exact call-event names delivered by the deployed version (`CallOffer` vs
   `Call`). If `CallOffer*`, call rejection needs a new handler.
3. Does the deployed version deliver `Message` events for `status@broadcast`
   (`EVENT_IGNORE_STATUS=false` default)? Legacy filters client-side.
4. `WEBHOOK_FILES`/MinIO setting on the pilot (`base64` vs `mediaUrl` in
   `Message`).
5. `[unverified]` whether `data.groupData` is always present for group `Message`
   events on the deployed version when `GetGroupInfo` fails (code path shows it
   can be missing).
