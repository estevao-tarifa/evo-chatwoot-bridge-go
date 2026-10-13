# Contract — Evolution authentication and instance-token resolution

Scope: which credential the legacy bridge uses for each Evolution endpoint, how
`resolveInstanceToken` parses `/instance/all`, and what the Evolution GO server
actually enforces. Sources: legacy commit `daeb04b` (v5.4.4), Evolution GO
`0.6.1` (`89eaae337e61`) + `main` (`9337afc47e10`, == `0.7.2`), official docs
(cited inline). No tokens in examples.

## 1. Credentials in the legacy bridge

`EvolutionConfig` (`server/services/evolutionClient.ts:3-8`):

- `apiKey` — connection field `evolutionApiKey`; comment says it *"Can be
  GLOBAL_API_KEY or instance token"*.
- `instanceToken` — connection field `evolutionInstanceToken` (nullable,
  `drizzle/schema.ts:31`), resolved from the API when absent.

Two HTTP clients with static headers (`evolutionClient.ts:19-38`):

| Client | `apikey` header | Used for | Evidence |
|---|---|---|---|
| `adminClient` | `config.apiKey` (global key) | `GET /instance/all` | `:19-27`, `:49`, `:190` |
| `instanceClient` | `config.instanceToken \|\| config.apiKey` (fallback!) | everything else: `/send/text`, `/send/media`, `/instance/connect`, `/chat/markAsRead`, `/instance/status`, `/user/contacts`, `/chat/getBase64FromMediaMessage/{instance}` | `:29-39`, `:88`, `:97`, `:143`, `:163`, `:178`, `:200`, `:221` |

`setInstanceToken` mutates the instance client's default header in place
(`:42-44`). `healthCheck` uses `adminClient` for `GET /server/ok`
(`:257-264`), even though that route is public in Evolution GO
(`0.6.1/pkg/routes/routes.go:78`).

## 2. Endpoint → credential map (legacy behaviour)

| Endpoint | Legacy client | Credential sent |
|---|---|---|
| `GET /instance/all` | admin | global key (or whatever `apiKey` holds) |
| `POST /instance/connect` | instance | instance token, else `apiKey` fallback |
| `POST /send/text`, `POST /send/media` | instance | instance token, else fallback |
| `POST /chat/markAsRead` | instance | instance token, else fallback |
| `GET /instance/status` | instance | instance token, else fallback |
| `GET /user/contacts` | instance | instance token, else fallback |
| `POST /chat/getBase64FromMediaMessage/{instance}` | instance | instance token, else fallback |
| `GET /server/ok` | admin | global key (ignored server-side) |

The `instanceToken || apiKey` fallback means a deployment that never resolves the
instance token keeps sending the global key to instance endpoints; current
Evolution GO rejects that with `401 {"error":"not authorized"}` (§4).

## 3. `resolveInstanceToken()` — algorithm (bridgeProcessor.ts callers, client at `evolutionClient.ts:47-82`)

1. `GET /instance/all` with the admin client (`:49`).
2. Extract the instance list (`:52`):
   `Array.isArray(data) ? data : (data?.data || data?.instances || [])`.
   Handles Evolution GO `{data:[...], message:"success"}` (server side:
   `0.6.1/pkg/instance/handler/instance_handler.go` `All` →
   `{message:"success", data:[…]}`) and Evolution API v2 flat arrays.
3. Find the instance by name (`:55-60`), first match wins:
   `i.name || i.instanceName || i.instance?.instanceName`.
   Evolution GO items are flat: `{id, name, token, webhook, connected, …}`
   (`0.6.1/pkg/instance/model/instance_model.go`).
4. Extract the token (`:62-71`): `instance.token || instance.apikey ||
   instance.instance?.apikey`; if found → `setInstanceToken(token)` and return.
   A match without any token → warn + `null`.
5. Not found → warn listing available names → `null` (`:74-77`).
6. Any error (network/401/parse) → warn + `null`; **never throws** (`:78-82`).

Test evidence (shapes are the regression fixtures to keep):

- Evolution GO `{data:[{id,name,token,webhook,connected}]}` →
  `server/bugfixes.test.ts:10-43`.
- Evolution API v2 flat `[{instance:{instanceName,apikey,status}}]` →
  `server/bugfixes.test.ts:45-75`.
- Not found → `null` → `server/bugfixes.test.ts:77-104`.

Callers that persist the resolved token:

| Caller | Behaviour | Evidence |
|---|---|---|
| `connections.create` | resolves when `evolutionInstanceToken` absent; saves result (may be `null`) | `server/routers/connections.ts:82-96`, `:107-121` |
| `connections.resolveToken` | resolution or throw with operator-facing message | `server/routers/connections.ts:144-160` |
| `connections.configureWebhook` | resolves if missing, then `setWebhook` | `server/routers/connections.ts:162-185` |
| Startup bootstrap | resolves tokens for active connections (non-destructive) | `server/services/webhookBootstrap.ts:148-167` |
| CW→EVO message flow | resolves on the fly when the stored token is missing | `server/services/bridgeProcessor.ts:798-802` |

## 4. Server side — what Evolution GO enforces (0.6.1 and `main` agree)

Two middlewares (`0.6.1/pkg/middleware/auth_middleware.go:21-49`):

- `Auth`: `apikey` must equal an **instance token** (`GetInstanceByToken`
  → `WHERE token = ?`, `pkg/instance/repository/instance_repository.go:50-58`);
  otherwise `401 {"error":"not authorized"}`. It also injects the resolved
  `instance` into the request context.
- `AuthAdmin`: `apikey` must equal `GLOBAL_API_KEY`; otherwise 401.

Route mapping (`0.6.1/pkg/routes/routes.go:80-126`):

| Group | Middleware | Routes |
|---|---|---|
| `/instance` (admin) | `AuthAdmin` (global key) | `POST /create`, `GET /all`, `GET /info/:instanceId`, `DELETE /delete/:instanceId`, `POST /proxy/:instanceId`, `DELETE /proxy/:instanceId`, `POST /forcereconnect/:instanceId`, `GET /logs/:instanceId` |
| `/instance` (session) | `Auth` (instance token) | `POST /connect`, `GET /status`, `GET /qr`, `POST /pair`, `POST /disconnect`, `POST /reconnect`, `DELETE /logout` |
| `/send/*` | `Auth` (instance token) | text, link, media, poll, sticker, location, contact, button, list, carousel, status/text, status/media |

Note the admin list does **not** include a "list instances by global key +
instanceId" variant for sends. Docs discrepancy: the official webhook page
shows `POST /instance/connect` with `apikey: GLOBAL_API_KEY` and an `instanceId`
header; the 0.6.1/0.7.2 source requires an instance token on that route.
`[needs live validation]` — question: which credential does the deployed
Evolution GO accept on `/instance/connect`? The Go client should follow the
source (instance token), with a documented fallback only if the pilot proves
otherwise.

## 5. Standard Evolution API (Node) note

The legacy design treats `apiKey` as potentially global or instance-level
(`evolutionClient.ts:5`), and `resolveInstanceToken` also parses the v2 shape
`{ instance: { instanceName, apikey } }` (bugfixes.test.ts:45-75). The Go client
must keep the two credential roles explicit but tolerate a single-key
deployment only where the server accepts it (`[needs live validation]` for the
Node API variant; not a pilot target).

## 6. Security requirements for the Go rewrite

Facts found in the legacy implementation / producers:

1. `resolveInstanceToken` logs the first 8 characters of the token
   (`evolutionClient.ts:67`). Also logs response bodies of `setWebhook`
   (`:147`) and errors including provider responses (`:154`). **Do not
   reproduce**: no token fragments, no bodies with credentials (`AGENTS.md` §8).
2. Webhook payloads from Evolution GO carry `instanceToken` at the root
   (`0.6.1/pkg/whatsmeow/service/whatsmeow.go:1838-1841`;
   `send_service.go:2292-2294`), and standard-API payloads carry `apikey`.
   The legacy sanitizer does not remove them before persisting `rawPayload`
   (`webhooks.md` §6). The Go sanitizer must drop `instanceToken`, `apikey`,
   `instanceId`, `instanceName`, auth headers.
3. The resolved token is stored in `connections.evolutionInstanceToken`
   (`drizzle/schema.ts:31`) and must never be returned in admin API responses
   in full (legacy `connections.list` returns whole rows — see the legacy
   endpoint audit; ADR-0006 requires masking).
4. Credentials are per-connection; never reuse one connection's token for
   another instance (`AGENTS.md` §7 rule 8).

## 7. Open questions (`[needs live validation]`)

1. `/instance/connect` credential: instance token (source) vs global key + header
   (docs) on the deployed version.
2. Whether the deployed version returns `token` on `GET /instance/all` items
   (legacy assumes yes; the operator may prefer manual token entry).
3. Token rotation semantics: does `/instance/connect` regenerate `token`?
   `[unverified]` — no rotation flow exists in the legacy bridge.
