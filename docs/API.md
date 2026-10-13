# API — evo-chatwoot-bridge admin REST surface

Canonical machine-readable contract: [`backend/api/openapi.yaml`](../backend/api/openapi.yaml).
This document is the human-readable companion and the acceptance
checklist. The two are kept in lockstep by `TestOpenAPIContractCoverage`
(`backend/internal/httpapi/openapi_test.go`), which reads the Go `apiRoutes`
table and the OpenAPI spec and fails on any difference in either direction.

## 1. Conventions

| Concern | Rule |
|---|---|
| Base path | Admin/session routes live under `/api/v1/*`. Webhooks (`/api/webhooks/*`) and ops probes (`/healthz`, `/readyz`) stay unversioned. |
| Content type | `application/json; charset=utf-8` with `X-Content-Type-Options: nosniff` on every response. |
| Error envelope | `/api/v1/*` failures are `{"error":{"code":"<stable>","message":"<safe>"}}`. Messages are static and leak no upstream/DB text. |
| Auth | **Session cookie only** (`app_session_id`), `HttpOnly`, `Secure` (configurable), `SameSite` configurable, `Max-Age` 7 days. No bearer token. |
| Unauthenticated signal | HTTP **401** (`error.code == "unauthorized"`). The SPA redirects to login on 401. |
| CSRF | `POST`/`PATCH`/`DELETE`/`PUT` require header `X-CSRF: 1` **and** a JSON body (`Content-Type: application/json`). A missing `X-CSRF` → 403 `csrf`; a non-JSON (or absent) content type → 415 `unsupported_media_type`. `GET` does not. |
| CORS | Exact-origin allowlist from `CORS_ALLOWED_ORIGINS`; credentials allowed; never `*`; preflight `OPTIONS` handled. |
| Rate limiting | Login only: 5 failed attempts per `(IP, username)` per 15 min → 429 + `Retry-After`; success resets. In-memory, per instance (documented limitation). |
| Body caps | `/api/v1/*` JSON 256 KiB (413 over cap); the global 16 MiB ceiling applies to webhooks. |
| Pagination | `limit` int `1..100`, `offset` int `≥0`; out of range → **400 `invalid_request`** (never a silent clamp). Defaults: logs 50, alerts 50. |
| Secrets | **Write-only.** Never returned; readback is a `has*` boolean plus a masked last-4. Clearing is not supported — replace only. |
| Never logged | Tokens, cookies, auth headers, query strings, bodies, webhook path tokens, media/base64. |

### Error-code catalogue

| Code | HTTP | Meaning |
|---|---|---|
| `unauthorized` | 401 | No/invalid session, or bad login credentials. |
| `forbidden` | 403 | Inactive admin; setup already completed. |
| `csrf` | 403 | Missing/invalid `X-CSRF` on a mutation. |
| `invalid_request` | 400 | Malformed body, bad pagination, unknown field. |
| `not_found` | 404 | Unknown connection or route. |
| `conflict` | 409 | Reserved; first-run `setup` lost a concurrent race (UNIQUE username). |
| `rate_limited` | 429 | Login rate limit (with `Retry-After`). |
| `request_too_large` | 413 | Body over the 256 KiB admin cap. |
| `unsupported_media_type` | 415 | A mutation body is not `application/json`. |
| `upstream_error` | 502 | An Evolution/Chatwoot call failed. |
| `internal` | 500 | Unexpected failure; details only in the logs. |

Ops and webhook routes keep their existing `{"error":"…"}` shape.

## 2. Route map

Legend: **session** = valid admin cookie required; **public** = no session.
`{id}` is the numeric connection id.

### Auth / session

| Method | Path | Auth | Body → Response |
|---|---|---|---|
| GET | `/api/v1/auth/state` | public | — → `{state:"needs_setup"\|"ready"}` |
| POST | `/api/v1/auth/setup` | public (403 if any admin exists) | `{username,password,displayName?}` → `{success,user}` + `Set-Cookie` |
| POST | `/api/v1/auth/login` | public + rate limit | `{username,password}` → `{success,user}` + `Set-Cookie` |
| POST | `/api/v1/auth/logout` | public (idempotent) | — → `{success:true}` + clear cookie |
| GET | `/api/v1/auth/me` | public | — → `{user:{id,username,displayName}\|null}` (200 even when null) |

### Dashboard (session)

| Method | Path | Response |
|---|---|---|
| GET | `/api/v1/dashboard/stats` | `{totalConnections,activeConnections,totalMessages,failedMessages,todayMessages,recentLogs[≤10]}` |
| GET | `/api/v1/dashboard/messages-per-hour` | `[{hour,incoming,outgoing,total}]` |
| GET | `/api/v1/dashboard/messages-per-day` | `[{day,incoming,outgoing,total}]` |
| GET | `/api/v1/dashboard/message-types` | `[{type,count}]` |

### Connections (session)

| Method | Path | Request → Response |
|---|---|---|
| GET | `/api/v1/connections` | — → `{connections:[Connection]}` (secrets masked) |
| GET | `/api/v1/connections/{id}` | — → `Connection` |
| POST | `/api/v1/connections` | create fields → `Connection` (201) |
| PATCH | `/api/v1/connections/{id}` | partial fields; absent = unchanged, secrets non-empty = replace, no clear → `Connection` |
| DELETE | `/api/v1/connections/{id}` | — → `{success:true}` (**deactivate**, history preserved, R7) |
| POST | `/api/v1/connections/{id}/test` | — → `{success,evolution:{ok,error?},chatwoot:{ok,error?}}` |
| POST | `/api/v1/connections/{id}/resolve-token` | — → `{success:true,hasToken:true}` (token persisted, **never returned**) |
| POST | `/api/v1/connections/{id}/webhook` | `{bridgeUrl?}` → `{success:true}` (URL **not** returned) |
| GET | `/api/v1/connections/{id}/webhook-status` | — → `{evolution:{configured,urlMasked,error?},chatwoot:{…},instanceStatus}` |
| POST | `/api/v1/connections/{id}/contacts/sync` | — → `{total,synced,skipped,failed,errors?[≤5]}` |
| GET | `/api/v1/connections/{id}/webhook-url/evolution` | — → `{webhookUrl}` (**token-bearing**, `Cache-Control: no-store`) |
| GET | `/api/v1/connections/{id}/webhook-url/chatwoot` | — → `{webhookUrl}` (**token-bearing**, `Cache-Control: no-store`) |

### Chatwoot (session; credentials in the POST body, never a query string)

| Method | Path | Request → Response |
|---|---|---|
| POST | `/api/v1/chatwoot/inboxes/query` | `{chatwootApiUrl,chatwootApiToken,chatwootAccountId}` or `{connectionId}` → `[InboxDTO]` (inbox `webhookUrl` **masked**) |
| POST | `/api/v1/chatwoot/inboxes` | `{creds?\|connectionId?,inboxName,webhookUrl?,…settings}` → `{id,name,channelType,webhookUrlMasked}` (201) |
| PATCH | `/api/v1/connections/{id}/chatwoot/inbox-settings` | `{name?,…settings}` → `{success:true,inbox}` (inbox `webhookUrl` **masked**) |
| POST | `/api/v1/connections/{id}/chatwoot/webhook` | `{bridgeUrl?}` → `{success:true,inboxId}` (URL **not** returned) |

### Notifications (session)

| Method | Path | Request → Response |
|---|---|---|
| GET | `/api/v1/notifications/settings` | — → settings (`smtpPass` masked) or `null` |
| PATCH | `/api/v1/notifications/settings` | partial settings; `smtpPass` write-only → settings |
| GET | `/api/v1/notifications/alerts` | query `connectionId?,limit,offset` → `{alerts[],total}` |
| GET | `/api/v1/notifications/failure-states` | — → `{"<connectionId>":{consecutiveFailures,lastAlertAt}}` |

### Logs / settings (session)

| Method | Path | Request → Response |
|---|---|---|
| GET | `/api/v1/logs` | query `connectionId?,direction?,status?,search?,limit,offset` → `{logs[],total}` |
| GET | `/api/v1/settings/bridge-url` | — → `{url:string\|null,source:"database"\|"env"\|"none"}` |

### Ops

| Method | Path | Auth | Response |
|---|---|---|---|
| GET | `/healthz` | public | `{status:"ok"}` |
| GET | `/readyz` | public | `{status:"ok"\|"unavailable",reason?}` |
| GET | `/api/v1/meta` | public | `{name,version,commit}` |

## 3. Secret handling (ADR-0006 Amendment 4)

- `evolutionApiKey`, `evolutionInstanceToken`, `chatwootApiToken` and
  `notification_settings.smtpPass` are **write-only**: they are never returned in
  any response. A read exposes `hasX` booleans and a masked marker
  (`••••` + last 4 runes). `PATCH`/`POST` replaces a value when a non-empty
  string is supplied; an absent field is unchanged; **clearing is not supported**.
- The derived **webhook URL** (which embeds the per-connection token) is returned
  by **exactly two** endpoints: `GET .../webhook-url/evolution` and
  `GET .../webhook-url/chatwoot`. They are authenticated, sent with
  `Cache-Control: no-store`, and never logged. Every other response masks the URL
  (`…/***`), and the `POST .../webhook` configure endpoints return no URL. The
  Chatwoot inbox DTO (`inboxes/query`, `inboxes`, `inbox-settings`) masks the
  upstream `webhookUrl` (exposed as `webhookUrlMasked`) for the same reason.
- `SESSION_SECRET`, `WEBHOOK_TOKEN_SECRET` and `CHATWOOT_WEBHOOK_SECRET` are env
  only: never returned, never logged. `SESSION_SECRET` is required in production
  (fail-fast).

## 4. Deliberate gaps and dropped procedures

Dropped (no SPA caller / unverified upstream): `system.*`,
`chatwoot.{getInbox,listAgents,listInboxAgents,updateInboxAgents}`,
`contacts.count`, `settings.setBridgeUrl`, the legacy `GET /api/webhooks/health`.

Dropped for now: `notifications.sendTest` (needs SMTP, not
implemented). `notifications/failure-states` returns an empty map because the
dispatch path that would populate it is out of scope — marked
`[needs live validation]`.

## 5. Persistence

No schema change or migration ships. Settings read the
baseline `bridge_settings` table when present; a legacy install without it
reports `source:"env"`/`"none"`.

**Session revocation** (ADR-0006 §1.4). The session token is
`base64url(id|exp|token_version|instance_nonce).HMAC`. Revocation is
**in-process**: `logout` bumps an in-memory, per-admin `token_version`, so every
token issued before it is rejected. The baseline `admin_users` table has no
`token_version` column and no migration ships, so the counter is
**per-instance** (the same documented limitation as the login rate limiter) —
until a future approved migration persists it. Independently, every process
embeds a random **instance nonce** in the token, so **a restart invalidates every
previously issued session** (a logged-out token cannot be resurrected by a reset
counter). A password change is not implemented and does not revoke by
itself (the credential hash is **not** embedded in the token); any future
password-change handler must call `auth.Service.RevokeSessions`.

**Sticky sessions.** Because the instance nonce and the `token_version` counter
are per process, a multi-replica deployment must route a given admin's requests
to the same instance (sticky sessions). A restart — or a request landing on
another replica — invalidates that admin's session. Run a single writer until an
approved migration persists the version and the deployment can drop stickiness.

**First-run setup**. `Setup` serialises its count-then-create
within a process, so two concurrent `POST /api/v1/auth/setup` calls in one
instance produce exactly one admin (the loser gets 403 `forbidden`). Across
instances the guard does not hold: `admin_users` has no singleton constraint and
no migration ships, so two replicas can each create a distinct first
admin. Deploy a single writer until an approved migration adds the constraint.
