# Installing evo-chatwoot-bridge

This directory is the deployment path for the bridge: one Go binary that keeps
**Evolution GO** (WhatsApp) and **Chatwoot** in sync in both directions, running
against your own MySQL database.

There are two supported ways to install it:

| Path | Guide | When to use it |
|---|---|---|
| Docker Compose (recommended) | §1–§9 | Any host with Docker; you manage MySQL yourself. |
| EasyPanel API | §11 (advanced) | You already run EasyPanel and want the whole app created from the panel's API. |

The optional React admin SPA can be deployed next to the bridge (§8).

## 0. What you need before starting

| Requirement | Notes |
|---|---|
| Docker Engine 24+ with Compose v2.20+ | `profiles:` and the top-level `name:` key in the Compose file need v2.20+. |
| A MySQL 8 database and a user for the bridge | MySQL 8.0 and 9.x are both supported (the test suite runs against both). The bridge only *adds* tables and indexes — it never drops or rewrites existing data. |
| An Evolution GO instance (tested against 0.7.2) | You need its API URL, its **global API key**, the **instance name**, and ideally the **instance token** (the `apikey` used for instance-scoped calls). The bridge needs at least the global key; the instance token is used for the webhook and send calls and can be filled in later with `POST /api/v1/connections/{id}/resolve-token`. |
| A Chatwoot account (v4.12.x) with an API inbox | You need the account ID, the inbox ID, the Chatwoot URL and an API access token. One Chatwoot inbox belongs to exactly one bridge connection. |
| A hostname that reaches the bridge | The bridge builds its webhook URLs from `BRIDGE_URL`, so that URL must be reachable from Evolution GO and Chatwoot. Terminate TLS in front of it (EasyPanel/Traefik, nginx, Caddy, …). |

> **One bridge per connection.** Evolution GO keeps exactly **one** webhook URL
> per instance, and the same is true of the Chatwoot inbox webhook. Running a
> second bridge (or the historical Node bridge) against the same instance means
> both fight over that URL and events are dropped. Shut the old one down or point
> only one of them at the instance.
>
> **One replica.** Session cookies carry a per-process nonce and the work queue
> is in-process. Do not scale the `bridge` service beyond one replica.

## 1. Get the code

```bash
git clone https://github.com/estevao-tarifa/evo-chatwoot-bridge-go.git
cd evo-chatwoot-bridge-go
```

## 2. Give the bridge a network to reach MySQL on

The Compose file does **not** create a database; it joins an external network.
That keeps your MySQL where it already is (its own container, its own credentials,
its own backups).

```bash
docker network create bridge-db-net
```

If MySQL already runs in another Compose project or as a plain container, attach
it to that same network instead of creating a new one:

```bash
docker network connect bridge-db-net YOUR_MYSQL_CONTAINER
```

Then set `DB_NETWORK` in `deploy/.env` (next step) to the network name you used.

## MySQL sizing (recommended)

The schema is tiny and the write rate is low, but a stock MySQL reserves RAM for
a large buffer pool, the performance schema and hundreds of idle connections
(151 by default). Shipping `deploy/mysql-tuning.cnf` cuts that fixed overhead:

| Configuration | RSS | Notes |
|---|---|---|
| MySQL defaults | 484 MiB | ~25 MB of data, `performance_schema` on, 151 connections. |
| `mysql-tuning.cnf` | ~120 MiB | Same data, bridge verified against it. |

Both rows were measured on a real single-host deployment carrying the bridge's
production data set (a 4 vCPU / 8 GiB host). After applying the file, `/readyz`
stays green and the dedup claim SQL, the migrations runner, `GET_LOCK` and
transactions were each verified against the tuned instance.

### Apply it

**Docker Compose** — mount the file read-only into MySQL's `conf.d` directory:

```yaml
services:
  mysql:
    volumes:
      - ./mysql-tuning.cnf:/etc/mysql/conf.d/mysql-tuning.cnf:ro
```

**EasyPanel** — the DB service already mounts `<project>/<service>/easypanel.cnf`
to `/etc/mysql/conf.d/easypanel.cnf`; paste the contents of
`deploy/mysql-tuning.cnf` into that file and redeploy the DB service.

### Validate before you restart

`mysqld --validate-config` parses the file without starting the server; exit `0`
means the configuration is valid. Set `MYSQL_CONTAINER` to your MySQL container
name and run:

```bash
MYSQL_CONTAINER=your-mysql-container
docker exec "$MYSQL_CONTAINER" mysqld --validate-config && echo "config OK"
```

Restart MySQL only after that exits `0`. **Rollback:** remove the mounted file
(or the pasted block) and restart — nothing in the file is destructive; it only
changes runtime sizing.

### Two hard constraints

- **`max_connections` must stay above 25.** The bridge's connection pool is
  `maxOpenConns = 25` in `backend/internal/storage/mysql/database.go`; a lower
  limit starves the pool and `/readyz` turns red.
- **Disabling `performance_schema` removes the `sys` schema** and every dashboard
  built on it — MySQL Workbench's performance dashboard, PMM and
  `mysqld_exporter`. The slow query log stays available and is the intended
  troubleshooting tool here.

### Growth

Memory does **not** grow with data volume — a small pool means the data is served
from disk and the OS page cache — so if the workload ever grows, raise
`innodb_buffer_pool_size` first.

## 3. Write the environment file

```bash
cp deploy/example.env deploy/.env
chmod 600 deploy/.env
openssl rand -base64 32
```

Run `openssl rand -base64 32` once for `WEBHOOK_TOKEN_SECRET` and once for
`SESSION_SECRET`, then edit `deploy/.env` and replace every placeholder. The
file documents each variable with its shape and whether it is required — the
short version:

- `DATABASE_URL` — required; `mysql://USER:PASSWORD@HOST:3306/DATABASE`, where
  `HOST` resolves inside the container on `DB_NETWORK`.
- `BRIDGE_URL` — required for webhooks; the public origin of the bridge, without
  a trailing slash and without `/api`.
- `WEBHOOK_TOKEN_SECRET`, `SESSION_SECRET` — required in the default
  `production` posture; rotating the first one invalidates every webhook URL.
- `ADMIN_USER` / `ADMIN_PASS` — optional; both or neither. When both are set and
  the admin table is still empty, the first start creates that admin.
- `PORT`, `LOG_LEVEL`, `CORS_ALLOWED_ORIGINS`, `COOKIE_*`, `QUEUE_*`, `WORKERS`,
  `DRAIN_TIMEOUT`, `STALE_AGE` — optional, with documented defaults.
- `BRIDGE_HOST_PORT`, `DB_NETWORK` — Compose-only; not read by the binary.

Docker Compose reads `deploy/.env` for both interpolation and the container's
environment, so this single file is all the deployment needs. Never commit it —
`.gitignore` already excludes `.env`.

## 4. Build the image

```bash
VERSION=backend/v1.0.0 COMMIT="$(git rev-parse --short HEAD)" \
  docker compose -f deploy/docker-compose.prod.yml build
```

`backend/Dockerfile` compiles a static binary into a `scratch` image that runs as
UID 65532 with no shell and no package manager; the Compose service adds a
read-only root filesystem, `no-new-privileges` and a full capability drop.

Validate the rendered configuration before starting anything:

```bash
docker compose -f deploy/docker-compose.prod.yml config
```

## 5. Migrate — before the server starts

**The server never migrates at boot.** This is deliberate: schema changes are a
separate, one-shot step so a container restart can never rewrite your database.

```bash
docker compose -f deploy/docker-compose.prod.yml run --rm bridge migrate
```

Without a Compose file, the same step is:

```bash
docker run --rm --network bridge-db-net --env-file deploy/.env \
  evo-chatwoot-bridge:latest migrate
```

Why it must run first: the migration creates `schema_migrations` and
`processed_messages` (the idempotency table) and adds an index on
`contacts_map`. The server starts without them, but the **first webhook then
fails**, because every delivery has to claim a row before it is acknowledged.

The migration set is `0001`–`0004`. It is:

- **forward-only** — no `DROP`, no `DELETE`, no `UPDATE`, no column rewrite, and
  the historical migration bookkeeping table is never touched;
- **additive** — existing rows keep their ids and values;
- **idempotent** — a second run applies nothing and exits 0;
- **serialised** — an advisory lock (`GET_LOCK('evo_bridge_migrate')`) means two
  concurrent runs cannot race.

`migrate` reads only `DATABASE_URL` and logs the host and database name — never
the password. A non-zero exit means nothing was started; fix the DSN and re-run.

## 6. Start the bridge and check its health

```bash
docker compose -f deploy/docker-compose.prod.yml up -d
```

```bash
export BASEURL='http://SEU_HOST:8100'
curl -fsS "$BASEURL/healthz"; echo
curl -fsS "$BASEURL/readyz"; echo
curl -fsS "$BASEURL/api/v1/meta"; echo
```

- `/healthz` — liveness; the process is up (this is also what the container
  healthcheck probes).
- `/readyz` — readiness; the MySQL pool answers. A `503` here almost always means
  `DATABASE_URL` is wrong or the database is unreachable from `DB_NETWORK`.
- `/api/v1/meta` — the API is wired and answering.

Neither probe requires authentication, and neither exposes configuration.

`SEU_HOST` is the machine running Docker and `8100` is `BRIDGE_HOST_PORT` from
`deploy/.env` — replace both. In production `BASEURL` should be your HTTPS
origin (the same value as `BRIDGE_URL`).

## 7. Create the connection and register the webhooks

The admin API is session-cookie authenticated. First-run setup and login are the
only public routes:

```bash
# Only while the admin table is empty (or let ADMIN_USER/ADMIN_PASS do it at boot).
curl -fsS -X POST "$BASEURL/api/v1/auth/setup" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"CHANGE_ME_admin_password","displayName":"Admin"}'; echo

curl -fsS -c /tmp/bridge.jar -X POST "$BASEURL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"CHANGE_ME_admin_password"}'; echo
```

Every mutating admin call needs the session cookie **and** two headers:
`Content-Type: application/json` and `X-CSRF: 1`. A missing JSON content type
answers `415`, a missing CSRF header `403`, a missing session `401`.

Create the connection (this is the same record the SPA edits):

```bash
curl -fsS -b /tmp/bridge.jar -X POST "$BASEURL/api/v1/connections" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d '{
        "name": "my-whatsapp",
        "evolutionApiUrl": "https://evolution.example.com",
        "evolutionApiKey": "YOUR_EVOLUTION_GLOBAL_API_KEY",
        "evolutionInstanceName": "my-instance",
        "evolutionInstanceToken": "YOUR_EVOLUTION_INSTANCE_TOKEN",
        "chatwootApiUrl": "https://chatwoot.example.com",
        "chatwootApiToken": "YOUR_CHATWOOT_API_TOKEN",
        "chatwootAccountId": 1,
        "chatwootInboxId": 1,
        "ignoreGroups": true
      }'; echo
```

Note the `id` in the response — the examples below call it `$CONNECTION_ID`. The
global API key (`evolutionApiKey`) is used for admin routes such as listing
instances; the instance token is used for the instance's own webhook and send
calls. If you left the instance token empty, resolve it once the bridge can reach
Evolution:

```bash
curl -fsS -b /tmp/bridge.jar -X POST "$BASEURL/api/v1/connections/$CONNECTION_ID/resolve-token" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' -d '{}'; echo
```

Register the webhook URLs. The bridge derives them from `BRIDGE_URL` and
`WEBHOOK_TOKEN_SECRET`, so you can either let the bridge push them upstream or
read them and set them by hand:

```bash
# Ask the bridge to configure both upstreams for this connection.
curl -fsS -b /tmp/bridge.jar -X POST "$BASEURL/api/v1/connections/$CONNECTION_ID/webhook" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d "{\"bridgeUrl\":\"$BASEURL\"}"; echo

curl -fsS -b /tmp/bridge.jar -X POST "$BASEURL/api/v1/connections/$CONNECTION_ID/chatwoot/webhook" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d "{\"bridgeUrl\":\"$BASEURL\"}"; echo
```

```bash
# Read back the two token-bearing URLs (authenticated; Cache-Control: no-store).
curl -fsS -b /tmp/bridge.jar "$BASEURL/api/v1/connections/$CONNECTION_ID/webhook-url/evolution"; echo
curl -fsS -b /tmp/bridge.jar "$BASEURL/api/v1/connections/$CONNECTION_ID/webhook-url/chatwoot"; echo
```

They look like `…/api/webhooks/evolution/<id>/<token>` and
`…/api/webhooks/chatwoot/<id>/<token>`. **Treat them as secrets**: they are the
only credential the ingress accepts, they must never be logged or pasted
anywhere, and rotating `WEBHOOK_TOKEN_SECRET` replaces them.

### Wiring Evolution GO by hand

Evolution GO configures its webhook with `POST /instance/connect`, authenticated
with the **instance token** (`apikey`), not the global key:

```bash
export EVO_URL='https://evolution.example.com'
export EVO_INSTANCE_TOKEN='YOUR_EVOLUTION_INSTANCE_TOKEN'
export EVO_WEBHOOK_URL='https://bridge.example.com/api/webhooks/evolution/1/YOUR_DERIVED_TOKEN'

curl -fsS -X POST "$EVO_URL/instance/connect" \
  -H "apikey: $EVO_INSTANCE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"webhookUrl\":\"$EVO_WEBHOOK_URL\",\"subscribe\":[\"MESSAGE\",\"SEND_MESSAGE\",\"READ_RECEIPT\",\"PRESENCE\",\"HISTORY_SYNC\",\"CHAT_PRESENCE\",\"CALL\",\"CONNECTION\",\"LABEL\",\"CONTACT\",\"GROUP\",\"NEWSLETTER\",\"QRCODE\",\"BUTTON_CLICK\",\"PICTURE\",\"USER_ABOUT\"]}"; echo
```

Those 16 names are the uppercase enum Evolution GO accepts, and they are exactly
what the bridge's own configure call sends. Do **not** send
`MESSAGES_UPSERT` — that is an Evolution API v2/Node event name; GO discards
unknown values silently, which would leave the instance subscribed to nothing and
your WhatsApp events would stop arriving.

### Wiring the Chatwoot inbox by hand

```bash
export CHATWOOT_URL='https://chatwoot.example.com'
export CHATWOOT_TOKEN='YOUR_CHATWOOT_API_TOKEN'
export CHATWOOT_ACCOUNT_ID='1'
export CHATWOOT_INBOX_ID='1'
export CHATWOOT_WEBHOOK_URL='https://bridge.example.com/api/webhooks/chatwoot/1/YOUR_DERIVED_TOKEN'

curl -fsS -X PATCH "$CHATWOOT_URL/api/v1/accounts/$CHATWOOT_ACCOUNT_ID/inboxes/$CHATWOOT_INBOX_ID" \
  -H "api_access_token: $CHATWOOT_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"channel\":{\"webhook_url\":\"$CHATWOOT_WEBHOOK_URL\"}}"; echo
```

The `webhook_url` key must be **nested inside `channel`**. A top-level
`webhook_url` is accepted by the API and silently ignored, which looks like a
successful configuration and delivers nothing.

### Before you flip, record what you are replacing

Both upstreams hold a single URL, so pointing them at this bridge stops whatever
was pointed there before. Write the current values down (Chatwoot inbox
`webhook_url`; the Evolution instance's webhook URL and event list) — you need
them for the rollback in §9.

## 8. Verify, then (optionally) add the admin SPA

Check that both upstreams are registered against this connection:

```bash
curl -fsS -b /tmp/bridge.jar "$BASEURL/api/v1/connections/$CONNECTION_ID/webhook-status"; echo
```

Expect `configured=true` for both Evolution and Chatwoot. The Evolution
`instanceStatus` field may read `unknown` against Evolution GO 0.7.2 — that is a
known reporting gap, not a wiring failure; the round trips below are the real
proof.

Then prove the two directions with real traffic:

1. **Chatwoot → WhatsApp**: reply to a conversation in the bridged inbox; the
   message must arrive on the customer's phone.
2. **WhatsApp → Chatwoot**: send a message to the instance from a phone; it must
   appear in the mapped conversation as an *incoming* message, without creating a
   duplicate contact or conversation.
3. **No loop**: the bridge's own message coming back as a webhook is marked
   `duplicate` and is never re-sent. `message_logs` should show exactly one row
   per direction per message.

Useful during debugging: `LOG_LEVEL=debug` in `deploy/.env` (then
`docker compose -f deploy/docker-compose.prod.yml up -d` again) and
`docker compose -f deploy/docker-compose.prod.yml logs -f bridge`. Logs are JSON
on stdout, and secrets, payload bodies and tokens are already stripped.

### The optional frontend (admin SPA)

The SPA is a separate image; the backend never serves it. Start it with the
`ui` profile:

```bash
docker compose --profile ui -f deploy/docker-compose.prod.yml up -d
```

The SPA requests `${BRIDGE_API_BASE_URL}/api/v1/...`. Two rules follow from that,
and both are easy to get wrong:

- **Never include `/api` in `BRIDGE_API_BASE_URL`.** The paths already start with
  `/api/v1`, so `https://bridge.example.com/api` produces
  `https://bridge.example.com/api/api/v1/...` → `404`.
- **Same origin is the supported topology.** Serve the SPA and the API from one
  hostname, with `/api` routed to the bridge, and leave `BRIDGE_API_BASE_URL`
  empty. The session cookie then stays first-party, no CORS is needed, and
  `COOKIE_SAMESITE=lax` keeps working. In EasyPanel this is a second domain
  entry on the bridge service with the path `/api` (§11); behind your own proxy
  it is one location block:

```nginx
location /api/ {
    proxy_pass http://bridge:3000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

  The bridge expects the full `/api/...` path to arrive unmodified — do not strip
  the prefix. Verify with
  `curl -fsS https://ui.example.com/api/v1/meta`.

- **Cross-origin is a fallback, not a default.** If the SPA must live on another
  origin, set `BRIDGE_API_BASE_URL` to the bridge origin (no `/api`), list the
  SPA origin in `CORS_ALLOWED_ORIGINS`, and set `COOKIE_SAMESITE=none` with
  `COOKIE_SECURE=true` so the browser sends the session cookie. Browsers with
  third-party-cookie blocking can still refuse it, which is why same-origin is
  the recommended shape.

The SPA is optional at runtime: stopping it does not affect webhooks or the
bridge. You can also drive everything through the admin API.

## 9. Rollback

Nothing in this deployment is destructive, so a rollback is quick:

```bash
docker compose -f deploy/docker-compose.prod.yml down
```

1. Stop the bridge (above).
2. Point both upstreams back at whatever served the instance before, using the
   values you recorded in §7:
   - Chatwoot: `PATCH /api/v1/accounts/{account}/inboxes/{inbox}` with
     `{"channel":{"webhook_url":"<the previous URL>"}}`;
   - Evolution GO: `POST /instance/connect` with `apikey: <instance token>` and
     `{"webhookUrl":"<the previous URL>","subscribe":[<the previous 16 events>]}`.
3. **The migration does not need to be reverted.** It only *adds*
   `schema_migrations`, `processed_messages` and an index; nothing else reads
   them and no existing row changed. Dropping them is a manual, operator-only
   decision — never a migration.

Data written by the bridge lives in the same tables the bridge reads
(`message_logs`, `contacts_map`), so a rollback does not lose or duplicate
history: the old service reads those rows as-is.

## 10. Troubleshooting

| Symptom | Likely cause |
|---|---|
| `/readyz` returns 503 | `DATABASE_URL` wrong, MySQL not on `DB_NETWORK`, or the user lacks privileges on the schema. |
| `/healthz` 200 but the container is unhealthy | The healthcheck is `/bridge healthcheck`. A failing probe with a healthy process usually means the image was modified; check `docker inspect`. |
| First webhook fails with a missing-table error | `bridge migrate` was never run (or was run against a different database). Run it, then restart: migrate → start, always in that order. |
| Webhook answered `401` | Token mismatch: the URL was registered from a different `WEBHOOK_TOKEN_SECRET` (rotated?) or the path was truncated. Re-read both URLs and re-register them. |
| Admin call answered `415` | Missing `Content-Type: application/json` on a mutation. |
| Admin call answered `403` | Missing `X-CSRF: 1` header on a mutation. |
| Admin call answered `401` after a restart | Session cookies are invalidated by a restart by design (per-process nonce). Log in again. |
| Browser will not hold the session cookie | The admin surface is on plain HTTP while `COOKIE_SECURE=true`. Serve it over HTTPS (or set `COOKIE_SECURE=false` for a local HTTP test only). |
| WhatsApp events stopped right after configuring | The Evolution instance is subscribed to nothing (an invalid `subscribe` list was sent). Re-send the 16-name list in §7 and check the instance's events. |
| SPA logs `net::ERR_FAILED` / shows 404s for `/api/...` | `BRIDGE_API_BASE_URL` includes `/api`, or `/api` is not routed to the bridge on the SPA's origin. |
| Duplicate contacts/conversations appear | A second bridge is running against the same connection, or the two bridges use different databases. Only one writer per connection. |
| The legacy UI shows stale data | Expected: the backend serves no assets. Deploy the SPA (§8) to see live data. |

## 11. Advanced: install through the EasyPanel API

Every step of the EasyPanel install can be scripted. The panel exposes the same
operations as the dashboard over `https://YOUR_PANEL/api`, authenticated with a
bearer token; the request shapes below come from the panel's published OpenAPI
document (<https://easypanel.io/openapi.json>). Field names and which fields a
given panel version *requires* can differ between releases — read the response,
and when in doubt confirm the operation in the dashboard before scripting it.

Caveats worth knowing before you start:

- The bearer token comes from `POST /api/login` and is short-lived; never put it
  in a script, an image or a repository (see [`SECURITY.md`](../SECURITY.md)).
- Your panel user needs permission for the target project, and the project must
  already exist. Names must match `^[a-z0-9-_]+$`.
- The app must join the network your MySQL is on. EasyPanel puts a service on its
  project network, so the DB container name has to resolve there — that is why
  the cutover reused the project where the database lived.
- `replicas` must stay `1`.
- Windows/HTTP: if the panel is behind TLS, keep `https` and the certificate
  resolver (`letsencrypt`) as shown; set `wildcard: false`.

```bash
export PANEL='https://panel.example.com'
export PROJECT='your-project'
export SERVICE='bridge'
export DOMAIN='bridge.example.com'

export PANEL_TOKEN="$(curl -fsS -X POST "$PANEL/api/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"CHANGE_ME_panel_password"}' \
  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
# If PANEL_TOKEN comes back empty, print the raw login response and read the
# token field name from it — the panel's response shape is not part of the
# stable API document.
echo "token length: ${#PANEL_TOKEN}"
```

Create the app service with its source, build, environment and deploy settings
in one call:

```bash
curl -fsS -X POST "$PANEL/api/createAppService" \
  -H "Authorization: Bearer $PANEL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{
        \"projectName\": \"$PROJECT\",
        \"serviceName\": \"$SERVICE\",
        \"source\": {
          \"type\": \"github\",
          \"owner\": \"estevao-tarifa\",
          \"repo\": \"evo-chatwoot-bridge-go\",
          \"ref\": \"main\",
          \"path\": \"/backend\",
          \"autoDeploy\": false
        },
        \"build\": { \"type\": \"dockerfile\", \"file\": \"Dockerfile\" },
        \"deploy\": { \"replicas\": 1, \"zeroDowntime\": true }
      }"; echo
```

Set the environment. `updateAppEnv` **replaces** the whole block, so send every
variable you need in one `env` string (newline separated, the same keys as
`deploy/example.env`; `SEU_HOST`-style placeholders below):

```bash
export DATABASE_URL='mysql://bridge:CHANGE_ME_DB_PASSWORD@YOUR_DB_HOST:3306/YOUR_DATABASE'
export WEBHOOK_TOKEN_SECRET="$(openssl rand -base64 32)"
export SESSION_SECRET="$(openssl rand -base64 32)"

curl -fsS -X POST "$PANEL/api/updateAppEnv" \
  -H "Authorization: Bearer $PANEL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{
        \"projectName\": \"$PROJECT\",
        \"serviceName\": \"$SERVICE\",
        \"env\": \"PORT=3000\nAPP_ENV=production\nLOG_LEVEL=info\nDATABASE_URL=$DATABASE_URL\nBRIDGE_URL=https://$DOMAIN\nADMIN_USER=admin\nADMIN_PASS=CHANGE_ME_admin_password\nWEBHOOK_TOKEN_SECRET=$WEBHOOK_TOKEN_SECRET\nSESSION_SECRET=$SESSION_SECRET\nCOOKIE_SECURE=true\nCOOKIE_SAMESITE=lax\"
      }"; echo
```

Build and deploy (this is what actually rolls the container out):

```bash
curl -fsS -X POST "$PANEL/api/deployAppService" \
  -H "Authorization: Bearer $PANEL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"$PROJECT\",\"serviceName\":\"$SERVICE\"}"; echo
```

Publish the domain. The destination port is the container port (`3000`):

```bash
curl -fsS -X POST "$PANEL/api/createDomain" \
  -H "Authorization: Bearer $PANEL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{
        \"projectName\": \"$PROJECT\",
        \"serviceName\": \"$SERVICE\",
        \"id\": \"bridge\",
        \"host\": \"$DOMAIN\",
        \"path\": \"/\",
        \"https\": true,
        \"wildcard\": false,
        \"certificateResolver\": \"letsencrypt\",
        \"middlewares\": [],
        \"destinationType\": \"service\",
        \"serviceDestination\": {
          \"projectName\": \"$PROJECT\",
          \"serviceName\": \"$SERVICE\",
          \"port\": 3000,
          \"protocol\": \"http\"
        }
      }"; echo
```

Now run the migration **before** the app serves traffic — the server does not
migrate at boot, so either run the one-shot container against the same database
and network, or deploy first and immediately migrate before you register the
webhooks. On the panel host, a file containing just `DATABASE_URL` is enough:

```bash
# write one line: DATABASE_URL=THE_SAME_VALUE_YOU_SENT_TO_updateAppEnv
docker run --rm --network "$PROJECT" --env-file ./migrate.env \
  evo-chatwoot-bridge:latest migrate
```

Then lifecycle: `startAppService` / `stopAppService` take only the two names, so
they are the fast path for a rollback (stop the new app, re-point the two upstream
webhooks) and for bringing the app back:

```bash
curl -fsS -X POST "$PANEL/api/stopAppService" \
  -H "Authorization: Bearer $PANEL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"$PROJECT\",\"serviceName\":\"$SERVICE\"}"; echo

curl -fsS -X POST "$PANEL/api/startAppService" \
  -H "Authorization: Bearer $PANEL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"projectName\":\"$PROJECT\",\"serviceName\":\"$SERVICE\"}"; echo
```

For the SPA, repeat `createAppService` (source path `/frontend`, build
`Dockerfile`) plus `updateAppEnv` with a single variable —
`BRIDGE_API_BASE_URL=` (empty: same origin) — and then give the **bridge** a
second domain entry on the UI's host with the path `/api` so the SPA and the API
share one origin (§8). Verify with
`curl -fsS https://ui.example.com/api/v1/meta`.

## Files in this directory

| File | Purpose |
|---|---|
| `docker-compose.prod.yml` | The deployment: `bridge` (always) and `ui` (profile `ui`), an external DB network, hardening, healthchecks. |
| `example.env` | Every variable the binary reads, plus the two Compose-only ones, with the required/optional posture. |
| `README.md` | This guide. |

Related: [`../backend/Dockerfile`](../backend/Dockerfile) and
[`../frontend/Dockerfile`](../frontend/Dockerfile) (both built by the Compose
file), [`../docs/API.md`](../docs/API.md) (admin REST contract),
[`../docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) (how the pieces fit),
[`../CONTRIBUTING.md`](../CONTRIBUTING.md) and
[`../SECURITY.md`](../SECURITY.md).
