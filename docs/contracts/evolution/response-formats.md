# Contract — send-response parsing and loop-prevention impact

Scope: exactly how `extractSentMessageId` parses Evolution send responses, what
happens for every other shape, and how that feeds the two in-memory dedup caches
(`AGENTS.md` §7 rules 1 and 6). Sources: legacy commit `daeb04b` (v5.4.4),
Evolution GO `0.6.1`/`main`, official docs. Synthetic IDs only.

## 1. The parser

`server/services/bridgeProcessor.ts:95-103`:

```ts
function extractSentMessageId(result: unknown): string {
  if (!result || typeof result !== "object") return "";
  const r = result as Record<string, unknown>;
  const data = (r.data || r) as Record<string, unknown>;
  const info = (data.Info || data.info || {}) as Record<string, unknown>;
  const key = (data.key || {}) as Record<string, unknown>;
  return String(info.ID || info.id || key.id || data.id || "");
}
```

Input is the axios **response body** returned by
`EvolutionClient.sendText`/`sendMedia` (`evolutionClient.ts:88-105`).

| Step | Rule | Consequence |
|---|---|---|
| 1 | non-object / null / `""` / `0` → `""` | nothing extracted |
| 2 | `r.data` if truthy, else `r` | tolerates wrapped and flat bodies; `data: null` falls back to whole body |
| 3 | `info = data.Info \|\| data.info` | PascalCase preferred, camelCase fallback |
| 4 | `key = data.key` | standard-API branch |
| 5 | first **truthy** of `info.ID`, `info.id`, `key.id`, `data.id` | `0`, `""`, `null` skipped; order matters |
| 6 | `String(...)`; nothing found → `""` | numbers are stringified |

## 2. Recognized shapes

| # | Body shape | Path taken | Producer |
|---|---|---|---|
| 1 | `{"message":"success","data":{"Info":{"ID":"3EB0..."},...}}` | `r.data` → `data.Info.ID` | **Evolution GO 0.6.1/0.7.2 `/send/text` + `/send/media`** (`0.6.1/pkg/sendMessage/handler/send_handler.go:73,212`; `MessageSendStruct` at `send_service.go:246-250`, `Info.ID` set at `:2200-2211`) |
| 2 | `{"Info":{"ID":"..."}}` (flat) | `r` → `Info.ID` | hypothetical wrapper `[unverified]` |
| 3 | `{"data":{"key":{"id":"..."}}}` | `r.data` → `key.id` | legacy comment for other API builds (`bridgeProcessor.ts:98`) |
| 4 | `{"key":{"remoteJid":"...","fromMe":true,"id":"..."},...}` (flat) | `r` → `key.id` | standard Evolution API send response (Node) `[needs live validation]` for the pilot's exact build |
| 5 | `{"data":{"id":"11632"}}` / `{"id":123}` | `data.id` | generic wrappers |
| 6 | `{"message":"success","data":{"Info":{"id":"..."}}}` | `data.Info.id` | tolerated alias |

## 3. Shapes that silently return `""`

| Body | Why |
|---|---|
| `{"message":"success","data":{"MessageID":"..."}}` | field name not read |
| `{"data":{"Info":{"MessageID":"..."}}}` | not read |
| `{"data":{"key":{"ID":"..."}}}` | `key.ID` not read (only `key.id`) |
| `{"data":{"messageId":"..."}}` | not read |
| `{"data":{"id":0}}` / `{"data":{"Info":{"ID":""}}}` | falsy → skipped |
| `null`, `[]`, `"ok"`, `{ "data": null }` | step 1 / no fields |
| HTML/text error page with HTTP 200 | body is a string → step 1 → `""` |

## 4. What happens when extraction fails (loop-prevention impact)

Call sites: text (`bridgeProcessor.ts:837-838`), media (`:860-861`), fallback
(`:871-873`). All are `if (sentMsgId) addToSentCache(sentMsgId)` — empty results
are silently ignored, and the send is still logged as `sent`
(`destinationMessageId: ""`, log line `msgId: unknown`, `:839-840`).

1. **WhatsApp-side cache (`sentMessageCache`) never gets the ID**
   (`:34-51`). When the `SendMessage`/`Message` echo arrives with
   `IsFromMe=true`, `isInSentCache` returns false and the message is treated as
   an **external** message (`:515-524`) → forwarded to Chatwoot as `outgoing`.
   If no mapping exists yet, `findOrCreateContact`/`findOrCreateConversation`
   run (`:616-620`) → new contact/conversation possible (the §7 bug).
2. **Chatwoot-side cache (`sentToChatwootCache`)** is populated only when the
   bridge itself posts an outgoing message to Chatwoot and the Chatwoot
   response has `result.id` (`:704-707`); the Chatwoot `message_created`
   webhook is skipped only for cached IDs (`:768-772`). If `result.id` is
   missing too, the full loop is possible: bridge → CW outgoing → CW webhook →
   bridge → WhatsApp resend.
3. **Both caches are in-memory and per-process**, TTL 5 min, cleanup every 60 s
   (`:34-36,59,78-91`). A process restart, or an echo arriving later than the
   TTL (offline sync, delayed delivery), loses loop protection even when
   extraction worked.

## 5. Decision table (for Go regression tests)

| Scenario | Expected legacy behaviour | Go target (ADR-0004) |
|---|---|---|
| Response contains ID (shape 1/3/4) | cache populated; echo suppressed | persist `destination_message_id`; echo matches → `duplicate` |
| Response 200 without ID | no cache; echo reprocessed as external | send marked `unconfirmed`; echo matched by content-independent key or explicitly quarantined, never re-forwarded blindly |
| Response 4xx/5xx | axios throws; log `failed`; no cache | `failed`, no dedup claim reuse |
| Echo after restart / TTL expiry | reprocessed as external | persistent dedup survives restarts (ADR-0004 §8) |

## 6. Phase 7 live tests

1. Send a text via `/send/text` with a valid instance token; capture the raw
   response. Pass if `data.Info.ID` exists and equals the `ID` later delivered in
   the `SendMessage` webhook for that send.
2. Same for `/send/media`.
3. Force a response without ID (e.g. call through a wrapper that returns
   `{"message":"success"}` only) and confirm the legacy bridge logs
   `msgId: unknown` and treats the echo as external (regression fixture).
4. Restart the legacy bridge between send and echo; confirm duplicate forwarding
   (documents H4).

## 7. Open questions (`[needs live validation]`)

1. Exact response of the pilot's build (shape 1 assumed from source; the legacy
   H4 hypothesis is that it differs — ARCHITECTURE.md Q1).
2. Whether any proxy/load balancer in front of Evolution GO rewrites the body.
