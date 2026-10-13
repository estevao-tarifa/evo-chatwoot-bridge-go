# Contract — Evolution send API (`/send/text`, `/send/media`)

Scope: request fields **as implemented by the legacy bridge**, the matching
Evolution GO 0.6.1 handlers, response shapes, and failure modes. Sources:
legacy commit `daeb04b` (v5.4.4), Evolution GO tag `0.6.1`
(`89eaae337e61`, 2026-03-25 — closest release; differences vs `main`/`0.7.2`
noted), official docs <https://docs.evolutionfoundation.com.br/en/evolution-go/webhooks>.
Synthetic values only.

## 1. Endpoints and auth (see `auth.md`)

| Legacy call | Endpoint | Credential used | Evidence |
|---|---|---|---|
| `EvolutionClient.sendText` | `POST /send/text` | instance token (`apikey` header) | `server/services/evolutionClient.ts:87-93` |
| `EvolutionClient.sendMedia` | `POST /send/media` | instance token | `server/services/evolutionClient.ts:96-105` |

Evolution GO routes: `/send` group behind `Auth` middleware requiring a **valid
instance token** (`0.6.1/pkg/routes/routes.go:111-126`,
`pkg/middleware/auth_middleware.go:21-36`). Both endpoints validate the `number`
field (`jidValidationMiddleware`).

## 2. `/send/text` — request

Legacy body (`evolutionClient.ts:88-91`):

```json
{ "number": "<phone-or-JID>", "text": "<content>" }
```

Evolution GO `TextStruct` (`0.6.1/pkg/sendMessage/service/send_service.go:71-80`):

| Field | Type | Required | Legacy sends | Notes |
|---|---|---|---|---|
| `number` | string | **yes** (`send_handler.go:58-63`) | phone digits for 1:1; full JID for groups | accepts raw number or JID; user-exists check skipped for `@g.us`, `@broadcast`, `@newsletter`, `@lid` (`send_service.go:384-425`) |
| `text` | string | **yes** (`:63`) | Chatwoot content (+ optional `*Agent:*` prefix, `bridgeProcessor.ts:827-831`) | empty → `400 {"error":"message body is required"}` |
| `id` | string | no | not sent | custom message ID; otherwise server-generated (`send_service.go:1911`) |
| `delay` | int32 | no | not sent | typing presence before send |
| `mentionedJid` | string[] | no | not sent | |
| `mentionAll` | bool | no | not sent | |
| `formatJid` | *bool | no | not sent | |
| `quoted` | object `{messageId, participant}` | no | not sent | |
| `forwardingScore` | *uint32 | no | not sent | added in `main`/0.7.2 only |

Sender behaviour: text is wrapped as `extendedTextMessage.text`
(`send_service.go:491-493`), i.e. recipients see an extended text message, not a
plain `conversation`.

## 3. `/send/media` — request

Legacy body (`evolutionClient.ts:97-104`):

```json
{ "number": "...", "type": "image|video|audio|document", "url": "<http(s) URL>", "caption": "...", "filename": "..." }
```

Evolution GO `MediaStruct` (`0.6.1/.../send_service.go:97-109`):

| Field | Type | Required | Legacy sends | Notes |
|---|---|---|---|---|
| `number` | string | yes | same as text | |
| `url` | string | **yes** (`send_handler.go:227-230`) | `att.data_url \|\| att.url` (`bridgeProcessor.ts:848`) | 0.6.1: **must be http(s)** — the server does `http.Get(url)` (`send_service.go:1094`); `main` additionally accepts raw base64 (not a `data:` URI — `base64.StdEncoding.DecodeString` of the whole string fails with the prefix) |
| `type` | string | **yes** (`:233`) | mapped from Chatwoot attachment (`bridgeProcessor.ts:851-856`): `image`, `video`, `audio`, else `document` | valid values 0.6.1: `image`, `video`, `audio`, `document`; unknown → `500 "invalid media type"` (`send_service.go:877`). `main` adds `ptv` |
| `caption` | string | no | `content` (with signature if enabled) or `""` | |
| `filename` | string | no | `att.filename \|\| ""` | key omitted when empty in legacy JSON (axios drops `undefined`) |
| `id`, `delay`, `mentionedJid`, `mentionAll`, `formatJid`, `quoted` | — | no | not sent | |

Server-side media constraints (0.6.1, `send_service.go`):

- `image`: detected MIME must be `image/jpeg|png|webp`; `webp` is re-labelled
  jpeg (`:838-846`).
- `video`: must be `video/mp4` (`:847-852`).
- `audio`: converted to `audio/ogg; codecs=opus` via local ffmpeg or configured
  converter API (`:853-873`).
- `document`: no format check (`:874-875`).
- Newsletter targets reject `document` (`:884-886`).

## 4. Responses

Evolution GO handler wraps the service result (`0.6.1/pkg/sendMessage/handler/send_handler.go:73,212`):

```json
{
  "message": "success",
  "data": {
    "Info": {
      "Chat": "5511900000001@s.whatsapp.net",
      "Sender": "5511900000009@s.whatsapp.net",
      "IsFromMe": true,
      "IsGroup": false,
      "ID": "3EB0GENERATED0000000001",
      "Timestamp": "2026-04-07T12:00:00Z",
      "ServerID": 123456,
      "Type": "ExtendedTextMessage"
    },
    "Message": { "extendedTextMessage": { "text": "..." } },
    "MessageContextInfo": { "stanzaID": "", "participant": "", "quotedMessage": { "conversation": "" } }
  }
}
```

- `MessageSendStruct` = `{Info types.MessageInfo, Message *waE2E.Message,
  MessageContextInfo *waE2E.ContextInfo}` (`send_service.go:246-250`); `Info.ID`
  is the ID used for the WhatsApp stanza (`send_service.go:1911` custom id or
  `GenerateMessageID()`, copied at `:2200-2211`).
- The legacy bridge reads the response body via axios and passes it to
  `extractSentMessageId` (`bridgeProcessor.ts:837,860,871`) — full parsing
  contract in `response-formats.md`.
- A successful send also emits a `SendMessage` webhook with the same
  `Info.ID` (`send_service.go:2224-2244`), which is the loop-detection anchor.

## 5. Failure modes (as implemented)

| # | Failure | Observable | Legacy handling |
|---|---|---|---|
| 1 | Missing `number`/`text`/`url`/`type`, malformed JSON | `400 {"error":"..."}` | axios throws → `catch` → message log `failed` + `recordFailure` (`bridgeProcessor.ts:877-882`) |
| 2 | `apikey` is the global key instead of the instance token | `401 {"error":"not authorized"}` (`auth_middleware.go:21-36`) | same as #1; root cause only visible in logs |
| 3 | Number not registered on WhatsApp (1:1) | `500 {"error":"number ... is not registered on WhatsApp"}` (`send_service.go:425`) | same |
| 4 | Media URL unreachable from the **Evolution GO host** (e.g. Chatwoot `data_url` requiring auth, or `data:` URI on 0.6.1) | `500` (download error / invalid file format) | same; whole attachment loop aborts (no per-attachment recovery, `bridgeProcessor.ts:846-866`) |
| 5 | `image/webp` vs `image/jpeg` mismatch, `video` not mp4, audio conversion failure | `500 {"error":"Invalid file format: ..."}` | same |
| 6 | Evolution GO disconnected client | 500 after 3 internal attempts (`send_service.go:478-520`) | same |
| 7 | Bridge-side timeout | axios `timeout: 30000` for all calls (`evolutionClient.ts:26,37`); media download happens **inside** Evolution GO, so a large file can exceed it | axios error → failed |
| 8 | HTTP 200 but no extractable ID | `extractSentMessageId` returns `""` | send is logged `sent` with `msgId: unknown`; **no cache entry** → echo may be reprocessed as external (H4, `response-formats.md`) |
| 9 | Attachment with neither `data_url` nor `url` | silently skipped (`bridgeProcessor.ts:848-849`) | if all attachments skipped, the log is still marked `sent` (`:863`) — no error |
| 10 | Legacy has no retry/backoff around sends | one attempt per webhook | failures counted by `notificationService` |

## 6. Related outbound calls used by the engine (as implemented)

| Call | Endpoint | Body | Evidence |
|---|---|---|---|
| Mark read (1:1, optional) | `POST /chat/markAsRead` | `{chat: <JID>, messageId: <ID>}` | `evolutionClient.ts:161-173`; failures swallowed |
| Media download fallback | `POST /chat/getBase64FromMediaMessage/{instance}` | `{message:{key:{id}}, convertToMp4}` | `evolutionClient.ts:218-254`; response read as `data.base64 \|\| data.data.base64`, `mimetype \|\| mimeType \|\| data.mimetype`, `data:` prefix stripped; null on failure |
| Health | `GET /server/ok` | — | `evolutionClient.ts:257-264` (public route in evolution-go, `routes.go:78`) |
| Instance status | `GET /instance/status` | — | `evolutionClient.ts:176-183` |
| Call rejection | `sendText` to caller | configured message | `bridgeProcessor.ts:729-749` |

## 7. Open questions (`[needs live validation]`)

1. Exact `/send/text` and `/send/media` response JSON from the pilot instance
   (shape and `Info.ID` presence) — Q1 of `ARCHITECTURE.md` §9; drives H4.
2. Whether the pilot's Evolution GO is 0.6.x (no base64-in-`url`) or `main`
   (base64 accepted).
3. Whether Chatwoot webhook attachments deliver `data_url` reachable from the
   Evolution GO network; otherwise the Go bridge must download and upload
   differently (no such upload endpoint is used today).
4. `[unverified]` maximum accepted media size; legacy body limit is 50 MB
   (`server/_core/index.ts:39`) but no media cap is documented.
