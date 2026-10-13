# Configuration — evo-chatwoot-bridge (Go)

[README](../README.md) · [Install](INSTALL.md) · [Troubleshooting](TROUBLESHOOTING.md)

Two layers of configuration:

1. **Process environment** — read once at boot by
   `backend/internal/config/config.go`. A set-but-invalid value makes the process
   **fail fast** instead of starting half-configured.
2. **Per-connection fields** — stored in the `connections` table and edited
   through the admin API or the SPA.

---

## 1. Environment variables

The annotated, copy-ready file is [`deploy/example.env`](../deploy/example.env).
This table is the contract; "Required" means the process refuses to start in
`APP_ENV=production` without it.

| Variable | Default | Required | Notes |
|---|---|---|---|
| `PORT` | `3000` | no | HTTP listen port. Keep `3000` for the shipped Dockerfile/compose |
| `DATABASE_URL` | — | **yes** | `mysql://user:pass@host:3306/database`. Parsed strictly: only `tls=…` query params are accepted; other query params (mysql2-style) are rejected. Without it the pool never opens and `/readyz` reports unavailable |
| `LOG_LEVEL` | `info` | no | `debug` \| `info` \| `warn` \| `error` |
| `CORS_ALLOWED_ORIGINS` | empty | no | Comma-separated exact origins. Wildcards are rejected. Only needed when the SPA is served from a **different** origin; same-origin needs nothing |
| `BRIDGE_URL` | — | for webhooks | Absolute `http(s)` public base URL of this bridge, no trailing slash, no userinfo/query/fragment. Used to build and register the two webhook URLs. Without it, the webhook endpoints answer `400 bridge url is not configured` |
| `APP_ENV` | `production` | no | `production` \| `development`. Non-production relaxes the two required secrets |
| `WEBHOOK_TOKEN_SECRET` | — | **yes** (production) | Derives every per-connection webhook path token (below). Rotating it invalidates both URLs of every connection |
| `SESSION_SECRET` | — | **yes** (production) | Signs the admin session cookie. ≥32 random bytes. A restart invalidates every issued session, by design |
| `CHATWOOT_WEBHOOK_SECRET` | empty | no | When set, the Chatwoot ingress additionally requires a valid account HMAC and fails closed. Empty matches the API-inbox default (token only) |
| `ADMIN_USER`, `ADMIN_PASS` | empty | no | Bootstrap admin, seeded **only** on first boot when no admin exists. Set both or neither. Leave empty to reuse existing `admin_users` rows |
| `COOKIE_SECURE` | `true` in production | no | Turn off only for plain-HTTP access; browsers will not send a `Secure` cookie over `http` |
| `COOKIE_SAMESITE` | `lax` | no | `lax` \| `strict` \| `none`. `none` requires `COOKIE_SECURE=true` |
| `QUEUE_CAPACITY` | `1024` | no | Max admitted-but-unfinished jobs in the in-process queue |
| `QUEUE_MAX_BYTES` | `67108864` | no | Byte budget for queued payloads (64 MiB) |
| `WORKERS` | `8` | no | Fixed worker pool size |
| `DRAIN_TIMEOUT` | `30s` | no | Graceful drain budget on `SIGTERM` |
| `STALE_AGE` | `15m` | no | Boot-reconcile cutoff for stale claims; the repository enforces a 15-minute floor |

### Webhook tokens

Every connection gets two ingress URLs:

```text
https://<BRIDGE_URL>/api/webhooks/evolution/<connectionId>/<token>
https://<BRIDGE_URL>/api/webhooks/chatwoot/<connectionId>/<token>
```

with `token = base64url(HMAC-SHA256(WEBHOOK_TOKEN_SECRET, "webhook:<connectionId>"))`.

- Tokens are compared in **constant time**; a wrong or missing token is `401` and
  an unset secret is `503` — the routes fail closed, never open.
- The URLs are returned **only** by
  `GET /api/v1/connections/{id}/webhook-url/evolution|chatwoot` (authenticated,
  `Cache-Control: no-store`, never logged). Everywhere else the URL is masked.
- Rotating `WEBHOOK_TOKEN_SECRET` rotates all tokens: re-run
  `POST /api/v1/connections/{id}/webhook` on every connection afterwards.

---

## 2. Per-connection fields

Stored in `connections`, all editable through `POST /api/v1/connections` (create)
and `PATCH /api/v1/connections/{id}` (partial update; an absent field is
unchanged; a non-empty secret **replaces** the stored one; clearing is not
supported).

### 2.1 Identity and credentials

| Field | Type | Notes |
|---|---|---|
| `name` | string | free label for the operator |
| `evolutionApiUrl` | string | Evolution GO base URL |
| `evolutionApiKey` | string (write-only) | global API key; never returned, only `hasEvolutionApiKey` and a masked marker |
| `evolutionInstanceName` | string | the Evolution instance to use |
| `evolutionInstanceToken` | string (write-only) | persisted by `POST …/resolve-token`; used to validate the Evolution ingress |
| `chatwootApiUrl` | string | Chatwoot base URL |
| `chatwootApiToken` | string (write-only) | an account admin `api_access_token` |
| `chatwootAccountId` | int | Chatwoot account |
| `chatwootInboxId` | int (nullable) | the API-channel inbox messages are created in |
| `chatwootInboxName` | string | cached display name of that inbox |
| `isActive` | bool (default `true`) | an inactive connection is not routed |
| `webhookConfigured` | bool | set by the bridge when it registers the webhooks |

> `DELETE /api/v1/connections/{id}` **deactivates**; it never deletes history.

### 2.2 Behaviour toggles

| Field | Default | Effect |
|---|---|---|
| `ignoreGroups` | `true` | skip group messages entirely for this connection |
| `signMessages` | `false` | prefix outgoing messages with the operator signature |
| `autoReadMessages` | `false` | also send a read receipt for inbound messages |
| `rejectCalls` | `false` | reject incoming calls |
| `rejectCallMessage` | — | message sent when a call is rejected |
| `inboxGreetingEnabled`, `inboxGreetingMessage` | `false` | greeting behaviour for new conversations |
| `inboxAutoAssignment` | `true` | allow Chatwoot auto-assignment |
| `inboxCsatEnabled` | `false` | CSAT survey |
| `inboxWorkingHoursEnabled`, `inboxOutOfOfficeMessage`, `inboxTimezone` | `false`, —, `America/Sao_Paulo` | working-hours behaviour |
| `inboxAllowAfterResolved` | `true` | allow messages after a conversation is resolved |
| `inboxLockSingleConversation` | `false` | keep one conversation per contact |
| `inboxSenderNameType` | `friendly` | which name is pushed to Chatwoot as the sender name |
| `inboxBusinessName` | — | business name when required |
| `n8nWebhookUrl` | — | optional outbound notification hook |

> **Not yet enforced by the engine.** Everything below `ignoreGroups` is
> persisted and editable, but the delivery engine currently applies only the
> routing matrix and `ignoreGroups`. They are listed here because they exist in
> the schema and in the API, not because they change behaviour today — see
> [README §Project status](../README.md#project-status-and-known-limitations).

---

## 3. Admin SPA configuration

The backend serves no assets ([ADR-0007](adr/0007-split-backend-frontend-deployments.md)).
The SPA container renders its runtime config from one variable:

| Variable | Notes |
|---|---|
| `BRIDGE_API_BASE_URL` | the API **origin**, e.g. `https://bridge.example.com` — **no** trailing `/api` (the SPA routes already include `/api/v1`; a trailing `/api` produces `/api/api/v1/…`). With EasyPanel's two-domain routing (`/` → UI, `/api` → bridge) set it to the **UI** origin, which is then same-origin for the browser |

Only one of these is needed: either expose the SPA same-origin behind the bridge
(recommended, no CORS) or set `CORS_ALLOWED_ORIGINS` on the backend.

---

## 4. Migrations

- Forward-only and additive; existing tables and `__drizzle_migrations` are never
  altered destructively.
- Each applied migration is checksum-locked: a modified file is refused, not
  silently re-applied.
- Concurrent starts are serialised with `GET_LOCK`.
- `bridge migrate` is idempotent — running it twice reports the already-applied
  set and exits 0.
- Always run it **before** starting a new server version; an older binary keeps
  working on a migrated schema.

## 5. Database sizing (MySQL)

The bridge's workload is small: **~25 MB of data after two months**, ~20
messages/day. A stock MySQL 8/9 sizes itself for a much larger server, so most of
its resident memory is *configuration*, not data. Measured on a 4 vCPU / 8 GiB
host with the production data set:

| | RSS | `performance_schema` | buffer pool | `max_connections` |
|---|---|---|---|---|
| MySQL defaults | 484 MiB | ON | 128 MB | 151 |
| [`deploy/mysql-tuning.cnf`](../deploy/mysql-tuning.cnf) | **~120 MiB** | OFF | 32 MB | 40 |

What the tuning file changes, and what it costs:

- **`performance_schema = OFF`** — the single biggest win (~250 MB). It is the
  server's internal instrumentation; the bridge never reads it (its only schema
  introspection is two `information_schema` queries in the migration runner).
  You lose the `sys` schema and the dashboards built on it (MySQL Workbench
  performance dashboard, Percona PMM, `mysqld_exporter`). The file enables
  **`slow_query_log`** instead — the slow log does *not* depend on
  `performance_schema`, and it is the tool you actually use to find a slow query.
- **`innodb_buffer_pool_size = 32M`** — with a small pool the working set is
  served from the **OS page cache**, which the kernel reclaims instead of it
  being pinned by the database. Expect more disk reads; irrelevant at this
  traffic level.
- **`max_connections = 40`** — must stay **above 25**, the bridge's connection
  pool (`maxOpenConns` in `backend/internal/storage/mysql/database.go`).
- **`innodb_redo_log_capacity = 32M`** — ~70 MB less on disk (the default is
  100 MB).
- **`mysqlx = OFF`** — the X Protocol listener on 33060 is unused by the bridge;
  closing it removes both memory and an open port.
- **`skip_name_resolve`, `temptable_max_ram = 64M`, `max_heap_table_size = 4M`,
  `table_definition_cache = 400`, `innodb_log_buffer_size = 1M`** — bounds on
  worst-case allocations.

Deliberately **not** changed: `log_bin` stays ON. Measured cost: ~2 MB. Turning it
off (`skip-log-bin`) removes point-in-time recovery and replication — a bad trade
for 2 MB.

Apply it by mounting the file into the server's config directory:

- **Docker Compose (self-managed MySQL):** mount
  `./deploy/mysql-tuning.cnf:/etc/mysql/conf.d/mysql-tuning.cnf:ro`
- **EasyPanel:** the database service already mounts a config file
  (`<project>/<service>/easypanel.cnf` → `/etc/mysql/conf.d/easypanel.cnf`);
  paste the file's contents there.

Validate before restarting, then restart the service:

```sh
# MYSQL_CONTAINER is the name of your MySQL container/service
docker exec "$MYSQL_CONTAINER" mysqld --validate-config   # exit 0 = the file is valid
```

Rollback is the inverse: remove the file (or empty it) and restart.

**Growth.** These figures do not change with data volume: with a small pool the
data lives on disk and in the OS page cache, and RSS stays flat as the tables
grow. If the workload ever reaches millions of rows, raise
`innodb_buffer_pool_size` first — nothing else needs to change.

## 6. Operational tuning

| Situation | Knob |
|---|---|
| Bursts of webhooks from a slow Chatwoot | raise `QUEUE_CAPACITY`/`QUEUE_MAX_BYTES` — the queue rejects (does not drop silently) when full, and the rejection is visible in the logs |
| Slow upstream latency (observed: Chatwoot writes >8 s on shared hosts) | raise `WORKERS` only if the database can take it; the workload is I/O-bound |
| Long deploys with in-flight messages | raise `DRAIN_TIMEOUT` so `SIGTERM` lets workers finish |
| Claims left behind by a killed process | `STALE_AGE` controls the boot reconcile cutoff (floor 15m); a stale claim is quarantined, never re-sent blindly |
