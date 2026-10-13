# Contract — Chatwoot Inboxes (create/update + webhook configuration)

Scope: every inbox operation the bridge performs and the exact path that wires the
Chatwoot → bridge webhook.

Sources: legacy `evo-chatwoot-bridge/` @ `daeb04b` (v5.4.4) code + tests; Chatwoot
`v3.0.0` + `develop`; docs
<https://developers.chatwoot.com/api-reference/inboxes/*>.

Marker legend: `[legacy]` · `[cw-src]` · `[cw-docs]` · `[needs live validation]` ·
`[unverified]`.

---

## 1. Operations

| # | Method | Endpoint | Caller |
|---|---|---|---|
| 1.1 | `listInboxes()` | `GET /accounts/{acc}/inboxes` → `payload[]` | SPA (`routers/chatwoot.ts:121-127`) |
| 1.2 | `getInbox(id)` | `GET /accounts/{acc}/inboxes/{id}` | SPA (`routers/chatwoot.ts:129-134`) |
| 1.3 | `createInbox(opts)` | `POST /accounts/{acc}/inboxes` | SPA (`routers/chatwoot.ts:157-204`) |
| 1.4 | `updateInbox(id, settings & webhookUrl)` | `PATCH /accounts/{acc}/inboxes/{id}` | SPA settings + **webhook bootstrap** (`webhookBootstrap.ts:96-105`, `routers/chatwoot.ts:206-233,136-155`) |
| 1.5 | agents: `listInboxAgents`, `listAgents`, `addAgentsToInbox`, `updateInboxAgents`, `removeAgentFromInbox` | `/inbox_members*`, `/agents` | SPA only (`routers/chatwoot.ts:235-254`) |

The message-processing hot path performs **no** inbox call — inboxes are configured
out of band (bootstrap / SPA).

## 2. `createInbox` — exact payload

`[legacy]` `chatwootClient.ts:243-267`:

```json
POST /api/v1/accounts/{acc}/inboxes
{
  "name": "WhatsApp - <instance>",
  "channel": { "type": "api", "webhook_url": "<url or \"\">" },
  "greeting_enabled": true,            // if defined
  "greeting_message": "…",
  "enable_auto_assignment": true,
  "csat_survey_enabled": false,
  "working_hours_enabled": false,
  "out_of_office_message": "…",
  "timezone": "America/Sao_Paulo",
  "allow_messages_after_resolved": true,
  "lock_to_single_conversation": false,
  "sender_name_type": "friendly|professional",
  "business_name": "…"
}
```

- Optional keys are only included when `!== undefined` (lines 253-263). `webhook_url`
  defaults to `""` when not provided (line 248).
- `channel.type = "api"` → `Channel::Api`; the server permits `api`
  (`inboxes_controller.rb#allowed_channel_types`) and creates the channel from
  `Channel::Api::EDITABLE_ATTRS = [:webhook_url, :hmac_mandatory, {additional_attributes: {}}]`
  `[cw-src]`. The legacy bridge never sends `hmac_mandatory`/`secret`.
- Response: inbox partial, `{id, name, channel_type, callback_webhook_url, …}`
  `[cw-src]` `_inbox.json.jbuilder`.
- **Trap — `callback_webhook_url`**: for `Channel::Api` the `Inbox#callback_webhook_url`
  case statement only covers Twilio/Sms/Line/Whatsapp → returns **nil** for API
  inboxes `[cw-src]` (`app/models/inbox.rb`, v3.0.0 and develop). The SPA's
  `mapInbox` reads it (`routers/chatwoot.ts:53`) and `createInbox` returns it
  (`routers/chatwoot.ts:202`) → always `null` for API inboxes in practice
  `[needs live validation]` on the deployed version. The configured webhook URL is
   readable as **`webhook_url`** on pinned v4.12.1 for API inboxes; absence in the
   legacy `callback_webhook_url` mapping does not imply API readback is unavailable.
   See the Chatwoot 4.12.1 source validation (inbox partial L113–119).
- Failure modes: 401/403 (admin required? `inboxes_controller` authorizes create
  with inbox policy `[unverified]` role matrix), 422 validation (`name` required),
  5xx/timeout. Consumer behaviour: `routers/chatwoot.ts:187-189` throws when no
  `id` is returned.

## 3. `updateInbox` — exact payload

`[legacy]` `chatwootClient.ts:270-294`:

```json
PATCH /api/v1/accounts/{acc}/inboxes/{inboxId}
{
  "name": "…",                          // only keys present in settings
  "greeting_enabled": true,
  "greeting_message": "…",
  "enable_auto_assignment": true,
  "csat_survey_enabled": false,
  "working_hours_enabled": false,
  "out_of_office_message": "…",
  "timezone": "…",
  "allow_messages_after_resolved": true,
  "lock_to_single_conversation": false,
  "sender_name_type": "friendly",
  "business_name": "…",
  "enable_email_collect": false,        // update-only field (line 285)
  "channel": { "webhook_url": "…" }     // only when webhookUrl provided (lines 288-290)
}
```

- Server side `[cw-src]` (`inboxes_controller.rb#update`): scalar settings are
  updated on the inbox; a present `channel` key triggers
  `update_channel` → `channel.update!(webhook_url)` for `Channel::Api`.
- SPA mapping (`routers/chatwoot.ts:69-92`): camelCase → snake_case; nullable
  fields are coerced to `""` (`input[key] ?? ""`), which is how the UI **clears**
  `greeting_message`/`out_of_office_message`/`business_name`.
- Failure modes: 404 unknown inbox; 403; 422; 5xx/timeout (30 s). Bootstrap
  swallows nothing — it logs `✗ Chatwoot webhook failed …` and counts failures
  (`webhookBootstrap.ts:107-110,138-146`).

## 4. Webhook configuration path (Chatwoot → bridge)

1. Bridge URL resolution: `BRIDGE_URL` env var wins, otherwise `bridge_settings`
   row `BRIDGE_URL` (`webhookBootstrap.ts:33-52`). If absent, bootstrap **skips
   everything** (lines 123-127).
2. For each active connection with `chatwootInboxId` set:
   - URL = `{bridgeUrl}/api/webhooks/chatwoot/{connectionId}` (`:103`);
   - `PATCH /inboxes/{chatwootInboxId}` body `{channel:{webhook_url: url}}` (`:104`).
   - Connections **without** inbox id are skipped with a log (`:90-93`); the SPA
     `chatwoot.configureWebhook` throws in that case (`routers/chatwoot.ts:141-143`).
3. Only the Chatwoot webhook is reconfigured on startup; the Evolution GO webhook
   is deliberately **not** touched (file header `webhookBootstrap.ts:9-13`,
   log line `:136`) to preserve manual `subscribe` configuration.
4. Delivery on the Chatwoot side `[cw-src]` (`app/listeners/webhook_listener.rb`):
   API-inbox events go to `inbox.channel.webhook_url` via
   `deliver_api_inbox_webhooks` (requires `channel_type == 'Channel::Api'` and a
   non-blank `webhook_url`), delivered asynchronously by `WebhookJob`. Account-level
   webhooks are a separate feature and are **not used** by the bridge.
5. Ingress route: `POST /api/webhooks/chatwoot/:connectionId` — responds `200`
   immediately, then processes inline (`webhooks.ts:38-57`). No authentication,
   no Chatwoot signature check (`webhook.secret`/HMAC never configured) — see
   `webhooks.md` §1 and §4.

Regression evidence `[legacy]`:

- `server/chatwoot-webhook.test.ts:5-96`: exact PATCH payload incl. `channel.webhook_url`
  and `name` combination; `webhook_url` read back from the (mocked) response.
- `server/chatwoot-inbox.test.ts:6-96`: exact POST payload for API inbox creation,
  `channel.type = "api"`, webhook URL, list response.
- `server/inbox-settings.test.ts`: full settings round-trip on create/update.
- `server/chatwoot-webhook.test.ts:134-155`: URL construction incl. trailing-slash trim.

## 5. SPA-facing DTO mapping (for the REST rewrite)

`mapInbox` (`routers/chatwoot.ts:46-66`) exposes: `id, name, channelType, avatarUrl,
phoneNumber, webhookUrl (← callback_webhook_url), greetingEnabled, greetingMessage,
enableAutoAssignment, csatSurveyEnabled, workingHoursEnabled, outOfOfficeMessage,
timezone, allowMessagesAfterResolved, lockToSingleConversation, senderNameType,
businessName`.

- Legacy `callback_webhook_url` mapping is unreliable for API inboxes (§2 trap).
  Pinned v4.12.1 exposes actual `webhook_url`; prefer observed readback for
  status comparison, never silently echo the desired URL as if verified.
  Computed expected URL and observed configured URL are different attributes.

## 6. Open points `[needs live validation]`

1. Role requirements for inbox create/update with the operator's token (agent vs
   administrator).
2. Actual response body of `PATCH /inboxes/{id}` (does it include
   `callback_webhook_url` and what value).
3. Whether `update_channel` on `develop` re-delivers/keeps the webhook after
   `PATCH` (source shows plain `channel.update!`; no re-registration side effect
   expected `[cw-src]`).
