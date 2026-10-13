# Install — evo-chatwoot-bridge (Go)

[English](INSTALL.md) · [Português](INSTALL.pt-BR.md) · [README](../README.md) ·
[Configuration](CONFIGURATION.md) · [Troubleshooting](TROUBLESHOOTING.md)

This guide takes you from zero to a working, bidirectional WhatsApp ⇄ Chatwoot
bridge. Two deployment paths are covered:

- **A. Docker Compose** — any Linux host with Docker.
- **B. EasyPanel** — the reference deployment (Traefik + Docker Swarm), validated
  end to end.

Then, in [§3](#3-create-the-first-admin), you create the first admin, the
connection, and wire the two webhooks.

## 0. Before you start

| You need | Detail |
|---|---|
| **MySQL 8 or 9** | reachable from the bridge container; a database you may write to. Existing tables are preserved; migrations are additive |
| **Evolution GO** | running and **connected to WhatsApp** (instance `connected=true`). Note its base URL, its instance name and its **global API key** (or the instance token) |
| **Chatwoot** | a base URL, an `api_access_token` for an **account admin**, the account id, and an inbox of channel type **API** |
| **A public URL for the bridge** | HTTPS recommended. Evolution GO posts one webhook per instance, so this URL must be the final one |
| **Two secrets** | `WEBHOOK_TOKEN_SECRET` and `SESSION_SECRET`, each 32 random bytes (`openssl rand -base64 32`) |

> **One replica.** Run a single bridge instance. The cross-replica uniqueness
> index is still on the [roadmap](ARCHITECTURE.md), so more than one replica can
> create a duplicate contact under a race.

> **One Evolution webhook per instance.** Evolution GO stores a single webhook
> URL per instance, so this bridge and any other consumer cannot both receive the
> same events. Plan the cutover accordingly (see [§5](#5-cut-over-from-an-existing-bridge)).

---

## 1. Path A — Docker Compose

```sh
git clone https://github.com/estevao-tarifa/evo-chatwoot-bridge-go.git
cd evo-chatwoot-bridge-go

cp deploy/example.env deploy/.env
chmod 600 deploy/.env
$EDITOR deploy/.env        # DATABASE_URL, BRIDGE_URL, WEBHOOK_TOKEN_SECRET,
                           # SESSION_SECRET, ADMIN_USER, ADMIN_PASS
```

The annotated reference for every variable is
[`deploy/example.env`](../deploy/example.env); the rules behind them are in
[`CONFIGURATION.md`](CONFIGURATION.md).

**Run the migration first.** It is a separate, one-shot command. The server does
*start* on an unmigrated database — `/readyz` only pings MySQL, so it stays
`200` — and the missing schema then surfaces as a failed webhook (the claim
`INSERT` errors and the delivery is refused). Migrate before the first start and
confirm the migration reported its applies:

```sh
docker compose -f deploy/docker-compose.prod.yml build
docker compose -f deploy/docker-compose.prod.yml run --rm bridge migrate
docker compose -f deploy/docker-compose.prod.yml up -d
```

Verify:

```sh
curl -fsS http://127.0.0.1:8100/healthz   # {"status":"ok"}
curl -fsS http://127.0.0.1:8100/readyz    # {"status":"ok"}  — fails if MySQL is unreachable
curl -fsS http://127.0.0.1:8100/api/v1/meta
```

`/healthz` says the process is alive; `/readyz` says the database pool is up. A
`readyz` of `unavailable` after a migration almost always means a wrong
`DATABASE_URL` — see [Troubleshooting](TROUBLESHOOTING.md).

Put a reverse proxy (or EasyPanel/Traefik) in front of it so `BRIDGE_URL` is
reachable from Evolution GO and Chatwoot.

---

## 2. Path B — EasyPanel

The bridge is a normal EasyPanel **app** built from this Git repository (or from a
Docker image). The layout that was validated in production:

```text
EasyPanel project
├── app  bridge        build path /backend   internal port 3000
│     DATABASE_URL=mysql://user:pass@<db-service>:3306/<database>
│     BRIDGE_URL=https://<your-bridge-domain>
│     WEBHOOK_TOKEN_SECRET=<openssl rand -base64 32>
│     SESSION_SECRET=<openssl rand -base64 32>
│     APP_ENV=production
│     ADMIN_USER / ADMIN_PASS   (first boot only)
└── app  bridge-ui     (optional) build path /frontend   internal port 80
      BRIDGE_API_BASE_URL=https://<your-ui-domain>    ← origin only, no /api
```

### 2.1 Create the app

1. **Create project** → **Create Service** → **App**.
2. **Source**: GitHub, this repository. **Build**: Dockerfile, path `/backend`.
3. **Ports**: expose the app on the internal port **3000**.
4. **Environment**: paste the variables from the block above.
5. **Domains**: attach your bridge domain; EasyPanel terminates TLS.

### 2.2 Migrate before the first start

The default start command is the server. Run the migration once, before the
server ever starts, from the app's terminal:

```sh
./bridge migrate        # idempotent; prints the applied/applied-already list
```

Or, keeping the compose file, run the one-shot service as in Path A. Only after
that, **deploy/start** the app.

### 2.3 The optional admin SPA

Deploy `frontend/` as a second app built from the same repository with build path
`/frontend` (nginx on port 80). Then attach **two** domains to the UI host:

| Path | Service |
|---|---|
| `/` | `bridge-ui` |
| `/api` | `bridge` |

and set `BRIDGE_API_BASE_URL=https://<your-ui-domain>` (origin **without** a
trailing `/api` — the SPA's routes already include `/api/v1`). Same-origin means
**no CORS** and a first-party session cookie. The SPA is optional: stopping it
never affects message delivery.

<details>
<summary>Prefer to do it through the EasyPanel API?</summary>

Every step above has an API equivalent (`createAppService`, `updateAppEnv`,
`deployAppService`, `startAppService`, `createDomain`). The exact calls, the
identifier caveats and a copy-pasteable sequence are in
[`deploy/README.md`](../deploy/README.md).
</details>

---

## 3. Create the first admin

`GET /api/v1/auth/state` tells you what the bridge expects:

```sh
BRIDGE=https://bridge.example.com
curl -fsS "$BRIDGE/api/v1/auth/state"
# {"state":"needs_setup"}  → no admin exists yet
# {"state":"ready"}        → an admin exists (e.g. seeded by ADMIN_USER/ADMIN_PASS)
```

If it says `needs_setup`:

```sh
curl -sS -c cookies.txt -X POST "$BRIDGE/api/v1/auth/setup" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d '{"username":"admin","password":"<a-long-password>","displayName":"Admin"}'
```

If it says `ready`, log in instead:

```sh
curl -sS -c cookies.txt -X POST "$BRIDGE/api/v1/auth/login" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d '{"username":"admin","password":"<your-password>"}'
```

Every mutation below needs the session cookie **and** `X-CSRF: 1`. That is the
whole auth model: log in, keep the cookie, send the header.

---

## 4. Create the connection and wire the webhooks

One connection = one Evolution instance + one Chatwoot inbox in one Chatwoot
account.

```sh
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d '{
        "name":"WhatsApp Support",
        "evolutionApiUrl":"https://evolution.example.com",
        "evolutionApiKey":"<evolution global api key>",
        "evolutionInstanceName":"my-instance",
        "chatwootApiUrl":"https://chatwoot.example.com",
        "chatwootApiToken":"<chatwoot api_access_token>",
        "chatwootAccountId":1,
        "chatwootInboxId":1,
        "isActive":true
      }'
```

Then, in order:

```sh
ID=<the id returned above>

# a) verify both upstreams are reachable and the credentials work
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/test" -H 'X-CSRF: 1'

# b) persist the Evolution instance token (used to validate the Evolution ingress)
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/resolve-token" -H 'X-CSRF: 1'

# c) configure BOTH webhooks in one call (Evolution + the Chatwoot inbox)
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/webhook" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' -d '{}'

# d) confirm what is configured (URLs masked)
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-status"
# {"evolution":{"configured":true,...},"chatwoot":{"configured":true,...},"instanceStatus":"..."}
```

Step **(c)** does the two wirings for you:

- **Evolution**: `POST /instance/connect` on your instance with
  `{webhookUrl, subscribe}` and the `apikey` header, where `webhookUrl` is
  `https://<BRIDGE_URL>/api/webhooks/evolution/<id>/<token>`.
- **Chatwoot**: sets the inbox's webhook URL to
  `https://<BRIDGE_URL>/api/webhooks/chatwoot/<id>/<token>`.

The `<token>` is derived, never stored in plaintext elsewhere:
`base64url(HMAC-SHA256(WEBHOOK_TOKEN_SECRET, "webhook:<id>"))`. If you need to
wire the platforms by hand, the two token-bearing URLs are returned **only** by
the two dedicated endpoints (authenticated, `Cache-Control: no-store`):

```sh
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-url/evolution"
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-url/chatwoot"
```

> Rotating `WEBHOOK_TOKEN_SECRET` changes both URLs. Re-run step **(c)** right
> after, or both platforms keep posting to a URL that now answers `401`.

### 4.1 If you wire the platforms by hand

**Evolution GO** — the events list matters. On Evolution GO 0.7.2 the accepted
names are the v1 names; v2 names are silently ignored by some builds, which stops
delivery without an error:

```sh
curl -sS -X POST "$EVO_URL/instance/connect" \
  -H "apikey: $EVOLUTION_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
        "instanceName":"my-instance",
        "webhookUrl":"https://bridge.example.com/api/webhooks/evolution/1/<token>",
        "subscribe":["MESSAGE","SEND_MESSAGE","READ_RECEIPT","PRESENCE",
          "HISTORY_SYNC","CHAT_PRESENCE","CALL","CONNECTION","LABEL","CONTACT",
          "GROUP","NEWSLETTER","QRCODE","BUTTON_CLICK","PICTURE","USER_ABOUT"]
      }'
```

**Chatwoot** — Inbox settings → Configuration → *Webhook URL*:
`https://bridge.example.com/api/webhooks/chatwoot/1/<token>`. If you set
`CHATWOOT_WEBHOOK_SECRET`, the route additionally requires a valid account HMAC
and fails closed.

### 4.2 Bring existing history across (optional)

```sh
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/contacts/sync" -H 'X-CSRF: 1'
# {"total":…,"synced":…,"skipped":…,"failed":…}
```

This seeds `contacts_map` from the existing Chatwoot contacts so already-known
identities are reused instead of re-created.

---

## 5. Cut over from an existing bridge

Evolution GO stores **one** webhook per instance. To move from another bridge to
this one without a gap:

1. Deploy this bridge and create the connection (§4), but do **not** flip the
   webhook yet.
2. Record the current Evolution webhook URL — that is your rollback value.
3. Run the migration and start this bridge.
4. Point the Evolution webhook at this bridge (step **c** above).
5. Send one message in each direction and confirm it in
   `GET /api/v1/logs`.
6. Keep the previous bridge running (or its image) until you are satisfied; the
   flip is a single `POST /instance/connect` call to revert.

While the webhook points here, the previous bridge receives nothing. Keep the
window short.

---

## 6. Verify the end to end

| # | Action | Expected |
|---|---|---|
| 1 | Customer sends a WhatsApp message | new `message_logs` row, `direction=whatsapp_to_chatwoot`, `status=sent`; the message appears in the mapped Chatwoot conversation |
| 2 | The same webhook is replayed (or delivered twice) | second delivery → `duplicate`; **no** second message in Chatwoot |
| 3 | Agent replies in Chatwoot | row `direction=chatwoot_to_whatsapp`, `status=sent`; the reply arrives on WhatsApp once |
| 4 | The bridge's own send echoes back | classified as its own message — never forwarded again (loop break) |
| 5 | A message sent from WhatsApp Web directly | **reuses** the existing contact/conversation; no new contact, no renamed group |
| 6 | `/api/v1/connections/$ID/webhook-status` | both `configured:true` |

Inspect everything:

```sh
curl -sS -b cookies.txt "$BRIDGE/api/v1/logs?connectionId=$ID&limit=20"
curl -sS -b cookies.txt "$BRIDGE/api/v1/dashboard/stats"
```

Quarantine check (rule: a send with an unknown outcome is never blindly retried):

```sql
SELECT id, direction, message_type, status, source_message_id, dest_message_id
FROM message_logs
WHERE connection_id = <ID> ORDER BY id DESC LIMIT 20;
```

```sql
SELECT external_id, direction, status, error_code, attempt
FROM processed_messages
WHERE connection_id = <ID> ORDER BY updated_at DESC LIMIT 20;
```

`status='quarantined'` is a deliberate stop, not a crash. See
[ARCHITECTURE.md §3](ARCHITECTURE.md) and
[Troubleshooting](TROUBLESHOOTING.md#messages-stuck-in-quarantine).

---

## 7. Upgrade and rollback

**Upgrade**

```sh
git fetch && git checkout <tag>
docker compose -f deploy/docker-compose.prod.yml run --rm bridge migrate
docker compose -f deploy/docker-compose.prod.yml up -d
```

Migrations are forward-only: run the migration **before** the new server. It is
idempotent and locked (`GET_LOCK`), so concurrent starts are safe.

**Rollback**

The schema is additive, so an older binary keeps working on a migrated schema.
Redeploy the previous image/tag; no down-migration is needed or provided.

**Uninstall**

Stop the app, remove the containers/volumes you created, and drop the database if
you wish. Nothing outside your own database and your two webhook configurations
was touched by this bridge — remove or repoint the Evolution webhook and the
Chatwoot inbox webhook before you decommission it, otherwise they will keep
posting to a dead URL.

## 8. Next

- [CONFIGURATION.md](CONFIGURATION.md) — every variable and per-connection field.
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — the failure modes seen in practice.
- [ARCHITECTURE.md](ARCHITECTURE.md) — why it is built this way.
- [deploy/README.md](../deploy/README.md) — compose reference and the EasyPanel API path.
